package audit

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/microsoft/go-otel-audit/audit/base"
	"github.com/microsoft/go-otel-audit/audit/conn"
	"github.com/microsoft/go-otel-audit/audit/msgs"
)

func TestSend(t *testing.T) {
	t.Parallel()

	replaceRan := false

	tests := []struct {
		name             string
		client           *Client
		baseClient       *base.Client
		wantRanReplace   bool
		wantNotification bool
		err              bool
	}{
		{
			name: "Error: IsUnrecoverable()",
			client: &Client{
				sendRunner: func(ctx context.Context, msg msgs.Msg) error {
					return base.ErrConnection
				},
				replaceBackoffRunner: func(ctx context.Context) error {
					replaceRan = true
					return nil
				},
			},
			wantRanReplace: true,
		},
		{
			name: "Error: IsQueueFull()",
			client: &Client{
				sendRunner: func(ctx context.Context, msg msgs.Msg) error {
					return base.ErrQueueFull
				},
				notifier: make(chan NotifyError, 1),
			},
			wantNotification: true,
			err:              true,
		},
		{
			name: "Error: IsValidationErr()",
			client: &Client{
				sendRunner: func(ctx context.Context, msg msgs.Msg) error {
					return base.ErrValidation
				},
			},
			err: true,
		},
		{
			name: "Error: error is uncategorized",
			client: &Client{
				sendRunner: func(ctx context.Context, msg msgs.Msg) error {
					return errors.New("error")
				},
			},
			err: true,
		},
		{
			name: "Success",
			client: &Client{
				sendRunner: func(ctx context.Context, msg msgs.Msg) error {
					return nil
				},
			},
		},
	}

	for _, test := range tests {
		replaceRan = false

		err := test.client.Send(context.Background(), msgs.Msg{})
		switch {
		case test.err && err == nil:
			t.Errorf("TestSend(%s): got err == nil, want err != nil", test.name)
			continue
		case !test.err && err != nil:
			t.Errorf("TestSend(%s): got err == %s, want err == nil", test.name, err)
			continue
		case err != nil:
			continue
		}

		if replaceRan != test.wantRanReplace {
			t.Errorf("TestSend(%s): got replaceRan == %t, want replaceRan == %t", test.name, replaceRan, test.wantRanReplace)
		}
		if test.wantNotification {
			<-test.client.Notify() // If we don't get a notification, the test will fail.
		}
	}
}

func TestClose(t *testing.T) {
	t.Parallel()

	clientToClose, err := base.New(conn.NewNoOP())
	if err != nil {
		panic(err)
	}

	tests := []struct {
		name   string
		client *Client
	}{
		{
			name: "Success with nil .client",
			client: &Client{
				notifier: make(chan NotifyError),
			},
		},
		{
			name: "Success with non-nil .client",
			client: &Client{
				client:   clientToClose,
				notifier: make(chan NotifyError),
			},
		},
	}

	for _, test := range tests {
		err := test.client.Close(context.Background())
		if err != nil {
			t.Errorf("TestClose(%s): got err == %s", test.name, err)
		}
		<-test.client.Notify() // Make sure the notifer is closed.
	}
}

type fakeAudit struct {
	id int
	conn.Audit
}

func TestReplace(t *testing.T) {
	t.Parallel()

	bc, err := base.New(fakeAudit{id: 0, Audit: conn.NewNoOP()})
	if err != nil {
		panic(err)
	}

	tests := []struct {
		name            string
		replacingClient bool
		baseClient      *base.Client
		create          func() (conn.Audit, error)
		wantFakeAuditID int
		err             bool
	}{
		{
			name:            "Client is already being replaced",
			replacingClient: true,
			baseClient:      bc,
			wantFakeAuditID: 0,
		},
		{
			name: "CreateConn fails",
			create: func() (conn.Audit, error) {
				return nil, errors.New("error")
			},
			baseClient: &base.Client{},
			err:        true,
		},
		{
			name: "base.Client.Reset() fails",
			create: func() (conn.Audit, error) {
				return fakeAudit{id: 1, Audit: conn.NewNoOP()}, nil
			},
			baseClient: nil, // This causes an automatic failure
			err:        true,
		},
		{
			name: "Success",
			create: func() (conn.Audit, error) {
				return fakeAudit{id: 1, Audit: conn.NewNoOP()}, nil
			},
			baseClient:      bc,
			wantFakeAuditID: 1,
		},
	}

	for _, test := range tests {
		client := &Client{create: test.create, client: test.baseClient}
		client.replacingClient.Store(test.replacingClient)

		err := client.replace(context.Background())
		switch {
		case test.err && err == nil:
			t.Errorf("TestReplace(%s): got err == nil, want err != nil", test.name)
			<-client.Notify() // Make sure the error is sent to the notifier.
			continue
		case !test.err && err != nil:
			t.Errorf("TestReplace(%s): got err == %s, want err == nil", test.name, err)
			continue
		case err != nil:
			continue
		}

		if client.client.Conn().(fakeAudit).id != test.wantFakeAuditID {
			t.Errorf("TestReplace(%s): got fakeAudit.id == %d, want fakeAudit.id == %d", test.name, client.client.Conn().(fakeAudit).id, test.wantFakeAuditID)
		}
	}
}

func TestSendNotification(t *testing.T) {
	t.Parallel()

	// Send without an error.
	client := Client{notifier: make(chan NotifyError, 1)}
	client.sendNotification(nil)
	select {
	case <-client.notifier:
		t.Fatalf("TestSendNotification(sent NotifyError with no error): got NotifyError, expected message drop")
	default:
	}

	// Send with room in channel.
	client = Client{notifier: make(chan NotifyError, 1)}
	client.sendNotification(base.ErrQueueFull)
	select {
	case notice := <-client.notifier:
		if notice.Time.IsZero() {
			t.Fatalf("TestSendNotification(sent NotifyError with error): got Time zero, expected non-zero")
		}
		if notice.Err == nil {
			t.Fatalf("TestSendNotification(sent NotifyError with error): got NotifyError.Err.Err == nil, expected non-nil")
		}
	default:
		t.Fatalf("TestSendNotification(sent NotifyError with error): got no NotifyError, expected NotifyError")
	}

	// Send with no room in channel.
	client = Client{notifier: make(chan NotifyError)}
	client.sendNotification(base.ErrQueueFull)
	select {
	case <-client.notifier:
		t.Fatalf("TestSendNotification(sent NotifyError with error, but no room in queue): got NotifyError, expected message drop")
	default:
	}
}

type auditMsgCollector struct {
	conn.Audit

	recMsgs []msgs.Msg
	hbMsgs  []msgs.Msg
	mu      sync.Mutex

	num      atomic.Uint64
	errAfter int
}

func (a *auditMsgCollector) Type() conn.Type {
	return conn.TypeDomainSocket
}

func (a *auditMsgCollector) collected(i int) {
	for {
		a.mu.Lock()
		if len(a.recMsgs) >= i {
			a.mu.Unlock()
			return
		}
		a.mu.Unlock()
		time.Sleep(100 * time.Nanosecond)
	}
}

func (a *auditMsgCollector) Write(ctx context.Context, msg msgs.Msg) error {
	if n := a.num.Add(1); a.errAfter > 0 && n > uint64(a.errAfter) {
		return errors.New("error after")
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	switch msg.Type {
	case msgs.Heartbeat:
		a.hbMsgs = append(a.hbMsgs, msg)
	case msgs.DataPlane, msgs.ControlPlane:
		a.recMsgs = append(a.recMsgs, msg)
	default:
		return errors.New("unknown message type")
	}
	return nil
}

func (a *auditMsgCollector) CloseSend(ctx context.Context) error {
	return nil
}

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

// TestResetHappensOnClient is a practical test that shows that the client handles calling base.Client.Reset()
// when an unrecoverable error is returned from base.Client.Send().
func TestResetHappensOnClient(t *testing.T) {
	t.Parallel()

	numMsgs := 100000

	msg := msgs.Msg{
		Type:   msgs.DataPlane,
		Record: validRecord.Clone(),
	}

	conn0 := &auditMsgCollector{errAfter: 1000}
	conn1 := &auditMsgCollector{}

	ccCalled := false
	cc := func() (conn.Audit, error) {
		if ccCalled {
			return conn1, nil
		}
		ccCalled = true
		return conn0, nil
	}

	client, err := New(cc, WithAuditOptions(base.WithSettings(base.Settings{QueueSize: 4000})))
	if err != nil {
		panic(err)
	}

	for i := 0; i < numMsgs; i++ {
	tryAgain:
		if err := client.Send(context.Background(), msg); err != nil {
			if errors.Is(err, base.ErrQueueFull) {
				time.Sleep(100 * time.Millisecond)
				goto tryAgain
			}
		}
	}
	client.Close(context.Background())

	if len(conn0.recMsgs)+len(conn1.recMsgs) != numMsgs {
		t.Errorf("Expected %d messages, but got %d", numMsgs, len(conn0.recMsgs)+len(conn1.recMsgs))
	}
}
