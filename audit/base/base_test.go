package base

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/microsoft/go-otel-audit/audit/conn"
	"github.com/microsoft/go-otel-audit/audit/msgs"
)

func init() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
}

func TestIsUnrecoverable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"NilError", nil, false},
		{"ErrConnection", ErrConnection, true},
		{"ErrValidation", ErrValidation, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsUnrecoverable(test.err); got != test.expected {
				t.Errorf("Expected IsUnrecoverable(%v) to return %v, but got %v", test.err, test.expected, got)
			}
		})
	}
}

func TestIsQueueFull(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"NilError", nil, false},
		{"ErrQueueFull", ErrQueueFull, true},
		{"ErrConnection", ErrConnection, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := errors.Is(test.err, ErrQueueFull); got != test.expected {
				t.Errorf("Expected IsQueueFull(%v) to return %v, but got %v", test.err, test.expected, got)
			}
		})
	}
}

func TestIsValidationErr(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"NilError", nil, false},
		{"ErrValidation", ErrValidation, true},
		{"ErrConnection", ErrConnection, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := errors.Is(test.err, ErrValidation); got != test.expected {
				t.Errorf("Expected IsValidationErr(%v) to return %v, but got %v", test.err, test.expected, got)
			}
		})
	}
}

func TestSettingsDefaults(t *testing.T) {
	t.Parallel()

	var tests = []struct {
		name                      string
		inputSettings             Settings
		expecteQueueSize          int
		expectedRecoveryQueueSize int
	}{
		{
			name:             "ZeroValue",
			inputSettings:    Settings{},
			expecteQueueSize: DefaultQueueSize,
		},
		{
			name: "ValidAttributesNotOverwritten",
			inputSettings: Settings{
				QueueSize: 100,
			},
			expecteQueueSize: 100,
		},
		{
			name: "InvalidAttributesOverwritten",
			inputSettings: Settings{
				QueueSize: -1, // Invalid value
			},
			expecteQueueSize: DefaultQueueSize,
		},
	}

	for _, test := range tests {
		defaultSettings := test.inputSettings.defaults()
		if defaultSettings.QueueSize != test.expecteQueueSize {
			t.Errorf("TestSettingsDefaults(%s): got QueueSize %d, but want %d", test.name, defaultSettings.QueueSize, test.expecteQueueSize)
		}
	}
}

func TestNewClient(t *testing.T) {
	t.Parallel()

	// Create a test table with different test cases for the New function.
	var tests = []struct {
		name          string
		conn          conn.Audit
		extraClient   bool
		options       []Option
		err           bool
		expectWarning bool
	}{
		{
			name:          "Valid Client",
			conn:          &validAuditConnection{},
			options:       nil,
			expectWarning: false,
		},
		{
			name:          "Nil Connection",
			conn:          nil,
			options:       nil,
			err:           true,
			expectWarning: false,
		},
	}

	for _, test := range tests {
		lh := &logHandler{}
		logger := slog.New(lh)

		clients := 1
		if test.extraClient {
			clients = 2
		}

		for i := 0; i < clients; i++ {
			test.options = append(test.options, WithLogger(logger))
			client, err := New(test.conn, test.options...)
			if err == nil {
				defer client.Close(context.Background())
			}

			// Check if the error matches the expected error.
			switch {
			case test.err && err == nil:
				t.Errorf("TestNewClient(%s): Expected error, but got no error", test.name)
			case !test.err && err != nil:
				t.Errorf("TestNewClient(%s): Expected no error, but got error: %v", test.name, err)
			case err != nil:
				return
			}
		}

		// Check if the warning is logged as expected.
		if test.expectWarning {
			if len(lh.records) != 1 {
				t.Errorf("Expected warning message for multiple client creation")
			}
		}
	}
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

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name      string
		client    *Client
		sendCh    chan SendMsg
		ctx       context.Context
		fillQueue bool
		msg       msgs.Msg
		err       bool
	}{
		{
			name:   "ValidSend",
			sendCh: make(chan SendMsg, 1),
			msg:    msg,
		},
		{
			name:   "ValidationErr",
			sendCh: make(chan SendMsg, 1),
			msg:    invalidMsg,
			err:    true,
		},
		{
			name:      "QueueFull",
			sendCh:    make(chan SendMsg, 1),
			fillQueue: true,
			msg:       msg,
			err:       true,
		},
		{
			name:   "ContextCancelled",
			sendCh: make(chan SendMsg, 1),
			ctx:    cancelledCtx,
			msg:    msg,
			err:    true,
		},
	}

	for _, test := range tests {
		if test.ctx == nil {
			test.ctx = context.Background()
		}
		client := &Client{}
		client.sendCh = test.sendCh

		// Note: for the QueueFull test, we need to fill the send channel to force the select
		// to choose ctx.Done() over the send channel.
		if test.fillQueue || test.ctx.Err() != nil {
			for {
				sm := SendMsg{Ctx: context.Background(), Msg: msg}
				select {
				case client.sendCh <- sm:
					continue
				default:
				}
				break
			}
		}

		// Call the Send() method with the specified record and context.
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

func TestSendWithTimeout(t *testing.T) {
	t.Parallel()

	msg := msgs.Msg{
		Type:   msgs.DataPlane,
		Record: validRecord.Clone(),
	}
	client := &Client{}

	// Fill the send queue.
	client.sendCh = make(chan SendMsg, 1)
	client.sendCh <- SendMsg{Ctx: context.Background(), Msg: msg}

	// Pull off a message after a second.
	go func() {
		time.Sleep(1 * time.Second)
		<-client.sendCh
	}()

	// Call the Send() method with the specified record and context.
	err := client.Send(context.Background(), msg, WithTimeout(3*time.Second))
	if err != nil {
		t.Errorf("Expected no error, but got error: %v", err)
	}
}

func TestReset(t *testing.T) {
	t.Parallel()

	must := func() *Client {
		c, err := New(conn.NewNoOP(), WithSettings(Settings{QueueSize: 1}))
		if err != nil {
			panic(err)
		}
		return c
	}

	msg := msgs.Msg{
		Type:   msgs.DataPlane,
		Record: validRecord.Clone(),
	}

	tests := []struct {
		name    string
		client  *Client
		newConn conn.Audit
		err     bool
	}{
		{
			name:    "Nil client",
			newConn: conn.NewNoOP(),
			err:     true,
		},
		{
			name:   "Nil connection",
			client: must(),
			err:    true,
		},
		{
			name:    "Valid reset",
			client:  must(),
			newConn: conn.NewNoOP(),
		},
	}

	for _, test := range tests {
		ctx := context.Background()
		err := test.client.Reset(ctx, test.newConn)
		switch {
		case test.err && err == nil:
			t.Errorf("Expected error, but got no error")
		case !test.err && err != nil:
			t.Errorf("Expected no error, but got error: %v", err)
		case err != nil:
			return
		}
		defer test.client.Close(ctx)

		// Critical to make sure next part works.
		if cap(test.client.sendCh) != 1 {
			t.Errorf("Expected send channel to have a buffer of 1, but got %d", cap(test.client.sendCh))
		}

		// This makes sure that the go routine that processes the send channel is running.
		for i := 0; i < 10; i++ {
			if err := test.client.Send(ctx, msg); err != nil {
				t.Errorf("Expected no error, but got error: %v", err)
			}
			time.Sleep(10 * time.Millisecond) // Only a buffer of 1, so this keeps it from filling.
		}
	}
}

func TestResetOnRunningClient(t *testing.T) {
	t.Parallel()

	numMsgs := 100000

	msg := msgs.Msg{
		Type:   msgs.DataPlane,
		Record: validRecord.Clone(),
	}

	conn0 := &auditMsgCollector{errAfter: 1000}
	conn1 := &auditMsgCollector{}

	client, err := New(conn0)
	if err != nil {
		t.Fatalf("Expected no error, but got error: %v", err)
	}
	defer client.Close(context.Background())

	once := sync.Once{}

	for i := 0; i < numMsgs; i++ {
	tryAgain:
		if err := client.Send(context.Background(), msg); err != nil {
			if errors.Is(err, ErrQueueFull) {
				time.Sleep(100 * time.Nanosecond)
				goto tryAgain
			}
			if IsUnrecoverable(err) {
				once.Do(func() {
					if err := client.Reset(context.Background(), conn1); err != nil {
						t.Errorf("Expected no error, but got error: %v", err)
					}
				})
			}
		}
		time.Sleep(100 * time.Nanosecond) // Keep from a queue full error.
	}

	client.Close(context.Background())

	if len(conn0.recMsgs)+len(conn1.recMsgs) != numMsgs {
		t.Errorf("Expected %d messages, but got %d", numMsgs, len(conn0.recMsgs)+len(conn1.recMsgs))
	}
}

func recFromID(msg msgs.Record, id int) msgs.Record {
	m := msg.Clone()
	m.CallerAgent = strconv.Itoa(id)
	return m
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

type validAuditConnection struct {
	conn.Audit

	errOnClose bool
}

func (c *validAuditConnection) Type() conn.Type {
	return conn.TypeNoOP
}

func (c *validAuditConnection) CloseSend(ctx context.Context) error {
	if c.errOnClose {
		return errors.New("error on close")
	}
	return nil
}

type logHandler struct {
	slog.Handler
	records []slog.Record
	mu      sync.Mutex
}

func (l *logHandler) Enabled(context.Context, slog.Level) bool {
	return true
}

func (l *logHandler) Handle(ctx context.Context, rec slog.Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.records = append(l.records, rec)
	return l.Handler.Handle(ctx, rec)
}
