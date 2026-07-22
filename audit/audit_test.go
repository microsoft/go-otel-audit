package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Azure/retry/exponential"
	"github.com/gostdlib/base/concurrency/sync"
	"github.com/kylelemons/godebug/pretty"
	"github.com/microsoft/go-otel-audit/audit/conn"
	"github.com/microsoft/go-otel-audit/audit/internal/version"
	"github.com/microsoft/go-otel-audit/audit/msgs"
)

var validRecord = msgs.Record{
	CallerIpAddress:            msgs.MustParseAddr("192.168.0.1"),
	CallerIdentities:           map[msgs.CallerIdentityType][]msgs.CallerIdentityEntry{msgs.UPN: {{Identity: "user1@domain.com", Description: "Description"}}},
	OperationCategories:        []msgs.OperationCategory{msgs.UserManagement},
	TargetResources:            map[string][]msgs.TargetResourceEntry{"ResourceType": {{Name: "Name", Cluster: "Cluster", DataCenter: "DataCenter", Region: "Region"}}},
	CallerAccessLevels:         []string{"Level1"},
	OperationAccessLevel:       "AccessLevel",
	OperationName:              "Operation",
	OperationResultDescription: "ResultDescription",
	CallerAgent:                "Agent",
}

func TestSend(t *testing.T) {
	t.Parallel()

	msg := msgs.Msg{
		Type:   msgs.DataPlane,
		Record: validRecord.Clone(),
	}
	invalidMsg := msgs.Msg{
		Type:   msgs.DataPlane,
		Record: validRecord.Clone(),
	}
	invalidMsg.Record.OperationAccessLevel = ""

	tests := []struct {
		name      string
		client    *Client
		sendCh    chan msgs.Msg
		ctx       context.Context
		fillQueue bool
		msg       msgs.Msg
		err       bool
	}{
		{
			name:   "ValidSend",
			sendCh: make(chan msgs.Msg, 1),
			msg:    msg,
		},
		{
			name:   "ValidationErr",
			sendCh: make(chan msgs.Msg, 1),
			msg:    invalidMsg,
			err:    true,
		},
		{
			name:   "QueueFull",
			sendCh: make(chan msgs.Msg),
			msg:    msg,
			err:    true,
		},
	}

	for _, test := range tests {
		if test.ctx == nil {
			test.ctx = context.Background()
		}

		client := &Client{
			serviceTreeID: "e6c9fcb1-7f08-4c1d-9e7a-123456789abc",
			metrics:       mustNewMetrics(),
		}
		client.sendCh = test.sendCh

		err := client.Send(test.ctx, test.msg)
		switch {
		case test.err && err == nil:
			t.Errorf("Expected error, but got no error")
		case !test.err && err != nil:
			t.Errorf("Expected no error, but got error: %v", err)
		case err != nil:
			continue
		}
	}
}

func TestManageSender(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		senderErrs        []error
		newSenderResps    []newSenderResp
		backgroundClosers int
		wantDead          bool
		wantNotifications int
	}{
		{
			name:              "success",
			senderErrs:        []error{nil},
			wantNotifications: 0,
		},
		{
			name:       "sender error, recovers successfully",
			senderErrs: []error{errors.New("connection error"), nil},
			newSenderResps: []newSenderResp{
				{err: errors.New("new sender error")},
				{sender: &fakeMsgSender{}, err: nil},
			},
			wantNotifications: 2,
		},
		{
			name:              "sender error, fails permanently",
			senderErrs:        []error{errors.New("connection error")},
			newSenderResps:    []newSenderResp{{err: exponential.ErrPermanent}},
			wantDead:          true,
			wantNotifications: 3,
		},
		{
			// The sender here would succeed. Only backgroundClosers exceeding maxClosers makes this fail,
			// which is what proves the closers guard runs before we ever try to connect.
			name:              "sender error, fails permanently because too many closers are open",
			backgroundClosers: 4,
			senderErrs:        []error{errors.New("connection error")},
			newSenderResps:    []newSenderResp{{sender: &fakeMsgSender{}}},
			wantDead:          true,
			wantNotifications: 2,
		},
	}

	for _, test := range tests {
		back, err := exponential.New(
			exponential.WithPolicy(
				exponential.Policy{
					InitialInterval:     1 * time.Millisecond,
					Multiplier:          1.1,
					RandomizationFactor: 0.1,
					MaxInterval:         1 * time.Second,
				},
			),
		)

		if err != nil {
			panic(err)
		}

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		client := &Client{
			notifier:            make(chan NotifyError, 10),
			closers:             &sync.Group{},
			maxClosers:          3,
			sendCh:              make(chan msgs.Msg, 1),
			metrics:             mustNewMetrics(),
			manageSenderBackoff: back,
			log:                 slog.Default(),
			clientDead:          atomic.Bool{},
			testContext:         ctx, // Use testContext to control execution deterministically
			testParams:          &testParams{senders: test.newSenderResps},
		}

		closeMe := make(chan struct{})
		for i := 0; i < test.backgroundClosers; i++ {
			client.closers.Go(
				t.Context(),
				func(ctx context.Context) error {
					<-closeMe // Wait for the test to signal closure
					return nil
				},
			)
		}
		defer close(closeMe)

		sender := &fakeMsgSender{
			startErrs: test.senderErrs,
		}

		done := make(chan struct{})
		go func() {
			client.manageSender(sender)
			close(done)
		}()

		<-done

		if client.clientDead.Load() != test.wantDead {
			t.Errorf("TestManageSender(%s): clientDead mismatch, got %v, want %v", test.name, client.clientDead.Load(), test.wantDead)
		}

		close(client.notifier)
		gotNotifications := len(client.notifier)
		if diff := pretty.Compare(test.wantNotifications, gotNotifications); diff != "" {
			t.Errorf("TestManageSender(%s): notifications mismatch (-want +got):\n%s", test.name, diff)
		}
	}
}

// fakeMsgSender simulates msgSender behavior for testing manageSender.
type fakeMsgSender struct {
	startErrs []error
	callCount int

	// started, if non-nil, is closed the first time start() is called. This lets a test
	// observe that a sender was handed off to manageSender.
	started chan struct{}
}

func (f *fakeMsgSender) start(ctx context.Context) <-chan error {
	if f.started != nil {
		close(f.started)
		f.started = nil
	}
	ch := make(chan error, 1)
	if f.callCount < len(f.startErrs) {
		ch <- f.startErrs[f.callCount]
		f.callCount++
	} else {
		ch <- nil
	}
	close(ch)
	return ch
}

// TestReconnect checks that when the backoff gives up, the final notification we send the user carries the
// error that actually caused the failure. A previous version reported every permanent failure as "too many
// closers running", which hid other causes such as a non-linux host using a non-NoOp connection.
func TestReconnect(t *testing.T) {
	t.Parallel()

	// errBoom stands in for a permanent failure that is not the too-many-closers case.
	errBoom := fmt.Errorf("boom: %w", exponential.ErrPermanent)

	tests := []struct {
		name              string
		senders           []newSenderResp
		backgroundClosers int
		// cancelCtx aborts the retry by handing reconnect an already cancelled context, which is the
		// non-permanent way the backoff can give up.
		cancelCtx bool

		wantSender bool
		wantDead   bool
		// wantNotifications is how many notifications reconnect must send. reconnect notifies once per
		// failed connection attempt and once more when it gives up, so counting them is what pins the
		// giving-up notification: asserting only on the last one passes even if that notification is
		// never sent, because a per-attempt notification wraps the same error.
		wantNotifications int
		// wantErrIs, when set, must be wrapped by the last notification reconnect sends.
		wantErrIs error
	}{
		// This row is the baseline the others deviate from: one sender that connects, no closers backed up,
		// no cancellation.
		{
			name:              "Success: the first connection attempt succeeds",
			senders:           []newSenderResp{{sender: &fakeMsgSender{}}},
			wantSender:        true,
			wantNotifications: 0,
		},
		{
			name:              "Success: a retryable failure is retried until a sender is created",
			senders:           []newSenderResp{{err: errors.New("connection refused")}, {sender: &fakeMsgSender{}}},
			wantSender:        true,
			wantNotifications: 1,
		},
		{
			name:              "Error: a permanent failure reports the cause that produced it",
			senders:           []newSenderResp{{err: errBoom}},
			wantDead:          true,
			wantNotifications: 2,
			wantErrIs:         errBoom,
		},
		{
			// Deviates from the baseline only in backgroundClosers. The sender it would get still connects,
			// so if the closers guard did not run first this row would succeed and fail the assertions.
			name:              "Error: too many closers running reports the closers cause",
			senders:           []newSenderResp{{sender: &fakeMsgSender{}}},
			backgroundClosers: 4,
			wantDead:          true,
			wantNotifications: 1,
			wantErrIs:         errTooManyClosers,
		},
		{
			// cancelCtx and the failing sender are coupled: the backoff runs the operation once before it
			// honors the cancelled context, so a sender that connects would return before any abort.
			name:              "Error: a cancelled context aborts the retry and reports the cancellation",
			senders:           []newSenderResp{{err: errors.New("connection refused")}},
			cancelCtx:         true,
			wantDead:          true,
			wantNotifications: 2,
			// The backoff substitutes ErrRetryCanceled for context.Canceled, so that is the sentinel a
			// Notify consumer can actually match on.
			wantErrIs: exponential.ErrRetryCanceled,
		},
	}

	for _, test := range tests {
		policy := exponential.Policy{
			InitialInterval:     1 * time.Millisecond,
			Multiplier:          1.1,
			RandomizationFactor: 0.1,
			MaxInterval:         10 * time.Millisecond,
		}
		back, err := exponential.New(exponential.WithPolicy(policy))
		if err != nil {
			panic(err)
		}

		client := &Client{
			notifier:            make(chan NotifyError, 100),
			closers:             &sync.Group{},
			maxClosers:          3,
			sendCh:              make(chan msgs.Msg, 1),
			metrics:             mustNewMetrics(),
			manageSenderBackoff: back,
			log:                 slog.Default(),
			testParams:          &testParams{senders: test.senders},
		}

		if test.cancelCtx {
			cancelled, cancel := context.WithCancel(t.Context())
			cancel()
			client.testContext = cancelled
		}

		// Group.Go increments Running() synchronously, so the closers are counted by the time reconnect runs.
		closeMe := make(chan struct{})
		for i := 0; i < test.backgroundClosers; i++ {
			client.closers.Go(t.Context(), func(ctx context.Context) error {
				<-closeMe
				return nil
			})
		}
		defer close(closeMe)

		sender := client.reconnect()

		switch {
		case (sender != nil) != test.wantSender:
			t.Errorf("TestReconnect(%s): got sender != nil == %v, want %v", test.name, sender != nil, test.wantSender)
			continue
		case client.clientDead.Load() != test.wantDead:
			t.Errorf("TestReconnect(%s): got clientDead == %v, want %v", test.name, client.clientDead.Load(), test.wantDead)
			continue
		}

		// reconnect has returned, so it is the only writer and it is done. Read exactly what it queued
		// rather than closing the channel, since sendNotify writing to a closed channel would panic even
		// though it sends inside a select.
		n := len(client.notifier)
		notices := make([]error, 0, n)
		for i := 0; i < n; i++ {
			notices = append(notices, (<-client.notifier).Err)
		}

		if len(notices) != test.wantNotifications {
			t.Errorf("TestReconnect(%s): got %d notifications, want %d", test.name, len(notices), test.wantNotifications)
			continue
		}
		if test.wantErrIs == nil {
			continue
		}
		if len(notices) == 0 {
			t.Errorf("TestReconnect(%s): got 0 notifications, want the failure to be reported", test.name)
			continue
		}

		// The failure that stops the connection manager is the last thing reconnect notifies on. Earlier
		// notifications come from the individual connection attempts.
		last := notices[len(notices)-1]
		if !errors.Is(last, test.wantErrIs) {
			t.Errorf("TestReconnect(%s): got final notification err == %v, want it to wrap %v", test.name, last, test.wantErrIs)
		}
	}
}

func TestNewSender(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		goos      string
		connType  conn.Type
		createErr error
		sendCh    chan msgs.Msg

		wantErr bool
		// wantPermanent is whether the returned error must wrap exponential.ErrPermanent. A retryable
		// failure must not, or reconnect would give up and mark the client dead on a recoverable error.
		wantPermanent bool
	}{
		{
			name:      "connection creation fails",
			goos:      "linux",
			createErr: errors.New("connection creation failed"),
			sendCh:    make(chan msgs.Msg, 1),
			wantErr:   true,
		},
		{
			name:     "linux with DomainSocket conn",
			goos:     "linux",
			connType: conn.TypeDomainSocket,
			sendCh:   make(chan msgs.Msg, 1),
		},
		{
			name:     "non-linux with NoOp conn",
			goos:     "darwin",
			connType: conn.TypeNoOP,
			sendCh:   make(chan msgs.Msg, 1),
		},
		{
			name:     "non-linux with DomainSocket conn",
			goos:     "darwin",
			connType: conn.TypeDomainSocket,
			sendCh:   make(chan msgs.Msg, 1),
		},
		{
			name:     "non-linux with DomainSocket conn",
			goos:     "darwin",
			connType: conn.TypeDomainSocket,
			sendCh:   make(chan msgs.Msg, 1),
		},
		{
			name:          "newMsgSender() errors because Client has a nil send channel",
			goos:          "linux",
			connType:      conn.TypeDomainSocket,
			wantErr:       true,
			wantPermanent: true,
		},
	}

	const testServiceTreeID = "e6c9fcb1-7f08-4c1d-9e7a-123456789abc"

	for _, test := range tests {
		client := &Client{
			serviceTreeID: testServiceTreeID,
			kver:          "test-kernel",
			goos:          test.goos,
			create: func() (conn.Audit, error) {
				if test.createErr != nil {
					return nil, test.createErr
				}
				return &fakeAuditCloser{connType: test.connType}, nil
			},
			notifier: make(chan NotifyError, 1),
			closers:  &sync.Group{},
			metrics:  mustNewMetrics(),
			sendCh:   test.sendCh,
			log:      slog.Default(),
		}

		sender, err := client.newSender()
		switch {
		case test.wantErr && err == nil:
			t.Errorf("TestNewSender(%s): got err == nil, want err != nil", test.name)
			continue
		case !test.wantErr && err != nil:
			t.Errorf("TestNewSender(%s): got err == %v, want err == nil", test.name, err)
			continue
		case err != nil:
			if got := errors.Is(err, exponential.ErrPermanent); got != test.wantPermanent {
				t.Errorf("TestNewSender(%s): got errors.Is(err, ErrPermanent) == %v, want %v (err == %v)", test.name, got, test.wantPermanent, err)
			}
			if client.goos != "linux" && test.connType != conn.TypeNoOP {
				client.closers.Wait(t.Context())
				select {
				case notify := <-client.notifier:
					if !strings.Contains(notify.Err.Error(), "failed to close audit connection") {
						t.Errorf("TestNewSender(%s): unexpected notification error: %v", test.name, notify.Err)
					}
				case <-time.After(1 * time.Second):
					t.Errorf("TestNewSender(%s): expected notification for non-NoOp conn on non-linux, but none received", test.name)
				}
			}
			continue
		}

		if sender == nil {
			t.Errorf("TestNewSender(%s): got sender == nil, want non-nil sender", test.name)
			continue
		}

		wantHB := msgs.HeartbeatMsg{
			ServiceTreeID: testServiceTreeID,
			AuditVersion:  version.Semantic,
			OsVersion:     client.kver,
			Language:      runtime.Version(),
			Destination:   test.connType.String(),
		}

		s := sender

		if diff := pretty.Compare(wantHB, s.heartbeat.Heartbeat); diff != "" {
			t.Errorf("TestNewSender(%s): heartbeat mismatch (-want +got):\n%s", test.name, diff)
		}

		if s.conn.Type() != test.connType {
			t.Errorf("TestNewSender(%s): conn type mismatch: got %s, want %s", test.name, s.conn.Type(), test.connType)
		}
	}
}

func TestStartConnManager(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		deferred bool
		// connectFails is the number of newSender() calls that fail with a retryable error before the
		// final sender, which succeeds unless wantDead is set, in which case it is a permanent error.
		// startConnManager() makes the first call; the rest happen during the deferred reconnect.
		connectFails int
		// initialPermanent makes the very first newSender() call fail with a permanent error, which is the
		// one failure deferred mode cannot defer because retrying it could never succeed.
		initialPermanent bool
		wantErr          bool
		wantStarted      bool
		wantDead         bool
		// wantErrIs, when set, must be wrapped by the error startConnManager returns.
		wantErrIs error
	}{
		{
			name:        "Success: initial connection succeeds",
			wantStarted: true,
		},
		{
			name:         "Error: initial connection fails without deferred connection",
			connectFails: 1,
			wantErr:      true,
		},
		{
			name:         "Success: deferred connection serves no-op then upgrades to a real connection",
			deferred:     true,
			connectFails: 2,
			wantStarted:  true,
		},
		{
			name:         "Error: deferred connection dies when reconnect hits a permanent error",
			deferred:     true,
			connectFails: 1,
			wantDead:     true,
		},
		{
			name:             "Error: deferred connection is refused when the initial failure is permanent",
			deferred:         true,
			initialPermanent: true,
			wantErr:          true,
			wantErrIs:        ErrClientDead,
		},
	}

	for _, test := range tests {
		back, err := exponential.New(
			exponential.WithPolicy(
				exponential.Policy{
					InitialInterval:     1 * time.Millisecond,
					Multiplier:          1.1,
					RandomizationFactor: 0.1,
					MaxInterval:         10 * time.Millisecond,
				},
			),
		)
		if err != nil {
			panic(err)
		}

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		started := make(chan struct{})
		senders := make([]newSenderResp, 0, test.connectFails+1)
		switch {
		// A permanent initial failure is the only call startConnManager makes: it refuses to defer.
		case test.initialPermanent:
			senders = append(senders, newSenderResp{err: fmt.Errorf("connection can never succeed: %w", exponential.ErrPermanent)})
		default:
			for i := 0; i < test.connectFails; i++ {
				senders = append(senders, newSenderResp{err: errors.New("connection failed")})
			}
			if test.wantDead {
				senders = append(senders, newSenderResp{err: exponential.ErrPermanent})
				break
			}
			senders = append(senders, newSenderResp{sender: &fakeMsgSender{started: started}})
		}

		client := &Client{
			serviceTreeID:       "e6c9fcb1-7f08-4c1d-9e7a-123456789abc",
			kver:                "test-kernel",
			goos:                "linux",
			deferredConn:        test.deferred,
			notifier:            make(chan NotifyError, 100),
			closers:             &sync.Group{},
			maxClosers:          3,
			sendCh:              make(chan msgs.Msg, 1),
			metrics:             mustNewMetrics(),
			manageSenderBackoff: back,
			log:                 slog.Default(),
			testContext:         ctx,
			testParams:          &testParams{senders: senders},
		}

		err = client.startConnManager()
		switch {
		case err == nil && test.wantErr:
			t.Errorf("TestStartConnManager(%s): got err == nil, want err != nil", test.name)
			continue
		case err != nil && !test.wantErr:
			t.Errorf("TestStartConnManager(%s): got err == %s, want err == nil", test.name, err)
			continue
		case err != nil:
			if test.wantErrIs != nil && !errors.Is(err, test.wantErrIs) {
				t.Errorf("TestStartConnManager(%s): got err == %v, want it to wrap %v", test.name, err, test.wantErrIs)
			}
			continue
		}

		if test.wantStarted {
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Errorf("TestStartConnManager(%s): the real connection sender was never started", test.name)
			}
		}

		if test.wantDead {
			dead := false
			deadline := time.After(5 * time.Second)
			for !dead {
				select {
				case <-deadline:
					t.Errorf("TestStartConnManager(%s): got clientDead == false, want clientDead == true", test.name)
					dead = true
				case <-time.After(time.Millisecond):
					dead = client.clientDead.Load()
				}
			}
		}
	}
}

func TestNewNoopSender(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	client := &Client{
		serviceTreeID: "e6c9fcb1-7f08-4c1d-9e7a-123456789abc",
		kver:          "test-kernel",
		goos:          "linux",
		notifier:      make(chan NotifyError, 1),
		sendCh:        make(chan msgs.Msg, 1),
		metrics:       mustNewMetrics(),
		log:           slog.Default(),
	}

	sender, err := client.newNoopSender()
	if err != nil {
		t.Fatalf("TestNewNoopSender: got err == %s, want err == nil", err)
	}

	s := sender
	if s.conn.Type() != conn.TypeNoOP {
		t.Fatalf("TestNewNoopSender: got conn type %s, want %s", s.conn.Type(), conn.TypeNoOP)
	}

	// The no-op sender should drain messages so the client responds while connecting.
	errCh := sender.start(ctx)
	client.sendCh <- msgs.Msg{Type: msgs.DataPlane, Record: validRecord.Clone()}

	deadline := time.After(5 * time.Second)
	for len(client.sendCh) > 0 {
		select {
		case <-deadline:
			t.Fatalf("TestNewNoopSender: no-op sender did not drain the send channel")
		case <-time.After(time.Millisecond):
		}
	}

	cancel()
	<-errCh
}

func TestSendNotify(t *testing.T) {
	t.Parallel()

	// Send without an error.
	client := Client{notifier: make(chan NotifyError, 1)}
	client.sendNotify(nil)
	select {
	case <-client.notifier:
		t.Fatalf("TestSendNotify(sent NotifyError with no error): got NotifyError, expected message drop")
	default:
	}

	// Send with room in channel.
	client = Client{notifier: make(chan NotifyError, 1)}
	client.sendNotify(ErrQueueFull)
	select {
	case notice := <-client.notifier:
		if notice.Time.IsZero() {
			t.Fatalf("TestSendNotify(sent NotifyError with error): got Time zero, expected non-zero")
		}
		if notice.Err == nil {
			t.Fatalf("TestSendNotify(sent NotifyError with error): got NotifyError.Err.Err == nil, expected non-nil")
		}
	default:
		t.Fatalf("TestSendNotify(sent NotifyError with error): got no NotifyError, expected NotifyError")
	}

	// Send with no room in channel.
	client = Client{notifier: make(chan NotifyError)}
	client.sendNotify(ErrQueueFull)
	select {
	case <-client.notifier:
		t.Fatalf("TestSendNotify(sent NotifyError with error, but no room in queue): got NotifyError, expected message drop")
	default:
	}
}

func TestCloseAuditConn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		closeSendErr error
		wantHang     bool
		wantNotify   bool
	}{
		{
			name:         "CloseSend succeeds",
			closeSendErr: nil,
			wantNotify:   false,
		},
		{
			name:         "CloseSend fails",
			closeSendErr: errors.New("close error"),
			wantNotify:   true,
		},
		{
			name:         "Close hangs",
			closeSendErr: errors.New("close hang error"),
			wantHang:     true,
		},
	}

	for _, test := range tests {
		client := &Client{
			notifier: make(chan NotifyError, 1),
			closers:  &sync.Group{},
		}

		var hanger chan struct{}
		if test.wantHang {
			hanger = make(chan struct{})
		}
		fakeConn := &fakeAuditCloser{closeSendErr: test.closeSendErr, hang: hanger}

		client.closeAuditConn(fakeConn)

		if test.wantHang {
			time.Sleep(1 * time.Second) // Allow time for the hang to occur.
			if client.closers.Running() != 1 {
				t.Errorf("TestCloseAuditConn(%s): want 1 running closer that is hanging, got %d", test.name, client.closers.Running())
			}
			close(hanger)                    // Unblock the hang.
			client.closers.Wait(t.Context()) // Wait for the closer to finish.
			continue
		}

		select {
		case notify := <-client.notifier:
			if !test.wantNotify {
				t.Errorf("TestCloseAuditConn(%s): unexpected notification received: %v", test.name, notify)
			} else if notify.Err == nil || !strings.Contains(notify.Err.Error(), test.closeSendErr.Error()) {
				t.Errorf("notification error mismatch, got: %v, want error containing: %v", notify.Err, test.closeSendErr)
			}
		case <-time.After(1 * time.Second):
			if test.wantNotify {
				t.Errorf("TestCloseAuditConn(%s): expected notification, but none received", test.name)
			}
		}
	}
}

// fakeAuditCloser implements conn.Audit for testing various close scenarios.
type fakeAuditCloser struct {
	conn.Audit

	connType     conn.Type
	closeSendErr error
	hang         chan struct{}
}

func (f *fakeAuditCloser) Type() conn.Type {
	return f.connType
}

func (f *fakeAuditCloser) CloseSend() error {
	if f.hang != nil {
		// Simulate a hang by blocking indefinitely.
		<-f.hang
	}
	return f.closeSendErr
}
