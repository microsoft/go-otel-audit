package audit

import (
	"context"
	"errors"
	"log/slog"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Azure/retry/exponential"
	"github.com/gostdlib/base/concurrency/sync"
	"github.com/kylelemons/godebug/pretty"
	"github.com/microsoft/go-otel-audit/v2/audit/conn"
	"github.com/microsoft/go-otel-audit/v2/audit/internal/version"
	"github.com/microsoft/go-otel-audit/v2/audit/msgs"
)

var validRecord = msgs.Record{
	CallerIpAddress:            msgs.MustParseAddr("192.168.0.1"),
	CallerIdentities:           map[msgs.CallerIdentityType][]msgs.CallerIdentityEntry{msgs.UPN: {{"user1@domain.com", "Description"}}},
	OperationCategories:        []msgs.OperationCategory{msgs.UserManagement},
	TargetResources:            map[string][]msgs.TargetResourceEntry{"ResourceType": {{"Name", "Cluster", "DataCenter", "Region"}}},
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

		client := &Client{metrics: newMetrics()}
		client.sendCh = test.sendCh

		err := client.Send(test.ctx, test.msg)
		switch {
		case test.err && err == nil:
			t.Errorf("Expected error, but got no error")
		case !test.err && err != nil:
			t.Errorf("Expected no error, but got error: %v", err)
		case err != nil:
			return
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
		permErr           bool
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
			permErr:           true,
		},
		{
			name:              "sender error, fails permanently due to due many closers open",
			backgroundClosers: 4,
			senderErrs:        []error{errors.New("connection error")},
			newSenderResps:    []newSenderResp{{err: exponential.ErrPermanent}},
			wantDead:          true,
			wantNotifications: 2,
			permErr:           true,
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
					//MaxAttempts:         2,
				},
			),
		)

		if err != nil {
			panic(err)
		}

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		client := &Client{
			notifier:    make(chan NotifyError, 10),
			closers:     &sync.Group{},
			maxClosers:  3,
			sendCh:      make(chan msgs.Msg, 1),
			metrics:     newMetrics(),
			backoff:     back,
			log:         slog.Default(),
			clientDead:  atomic.Bool{},
			testContext: ctx, // Use testContext to control execution deterministically
			testparams:  &testParams{senders: test.newSenderResps},
		}

		closeMe := make(chan struct{})
		if test.backgroundClosers > 0 {
			for i := 0; i < test.backgroundClosers; i++ {
				client.closers.Go(
					t.Context(),
					func(ctx context.Context) error {
						<-closeMe // Wait for the test to signal closure
						return nil
					},
				)
			}
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

		if test.permErr {
			time.Sleep(1 * time.Second)
			cancel()
		}

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
}

func (f *fakeMsgSender) start(ctx context.Context) <-chan error {
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

func TestNewSender(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		goos      string
		connType  conn.Type
		createErr error
		sendCh    chan msgs.Msg

		wantErr bool
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
			name:     "newMsgSender() errors because Client has a nil send channel",
			goos:     "linux",
			connType: conn.TypeDomainSocket,
			wantErr:  true,
		},
	}

	for _, test := range tests {
		client := &Client{
			kver: "test-kernel",
			goos: test.goos,
			create: func() (conn.Audit, error) {
				if test.createErr != nil {
					return nil, test.createErr
				}
				return &fakeAuditCloser{connType: test.connType}, nil
			},
			notifier: make(chan NotifyError, 1),
			closers:  &sync.Group{},
			metrics:  newMetrics(),
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
			AuditVersion: version.Semantic,
			OsVersion:    client.kver,
			Language:     runtime.Version(),
			Destination:  test.connType.String(),
		}

		s := sender.(*msgSender)

		if diff := pretty.Compare(wantHB, s.heartbeat.Heartbeat); diff != "" {
			t.Errorf("TestNewSender(%s): heartbeat mismatch (-want +got):\n%s", test.name, diff)
		}

		if s.conn.Type() != test.connType {
			t.Errorf("TestNewSender(%s): conn type mismatch: got %s, want %s", test.name, s.conn.Type(), test.connType)
		}
	}
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
