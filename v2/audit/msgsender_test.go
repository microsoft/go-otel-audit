package audit

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kylelemons/godebug/pretty"
	"github.com/microsoft/go-otel-audit/v2/audit/conn"
	"github.com/microsoft/go-otel-audit/v2/audit/msgs"
)

func TestMsgSenderArgsValidate(t *testing.T) {
	t.Parallel()

	validHeartbeat := msgs.Msg{Type: msgs.Heartbeat}

	tests := []struct {
		name    string
		args    msgSenderArgs
		wantErr bool
	}{
		{
			name: "Valid args",
			args: msgSenderArgs{
				conn:      &fakeAudit{},
				heartbeat: validHeartbeat,
				client: &Client{
					notifier: make(chan NotifyError, 1),
					sendCh:   make(chan msgs.Msg, 1),
					metrics:  &metrics{},
				},
			},
		},
		{
			name: "Nil conn",
			args: msgSenderArgs{
				conn:      nil,
				heartbeat: validHeartbeat,
				client: &Client{
					notifier: make(chan NotifyError, 1),
					sendCh:   make(chan msgs.Msg, 1),
					metrics:  &metrics{},
				},
			},
			wantErr: true,
		},
		{
			name: "Invalid heartbeat type",
			args: msgSenderArgs{
				conn:      &fakeAudit{},
				heartbeat: msgs.Msg{Type: msgs.DataPlane},
				client: &Client{
					notifier: make(chan NotifyError, 1),
					sendCh:   make(chan msgs.Msg, 1),
					metrics:  &metrics{},
				},
			},
			wantErr: true,
		},
		{
			name: "Nil notifier channel",
			args: msgSenderArgs{
				conn:      &fakeAudit{},
				heartbeat: validHeartbeat,
				client: &Client{
					notifier: nil,
					sendCh:   make(chan msgs.Msg, 1),
					metrics:  &metrics{},
				},
			},
			wantErr: true,
		},
		{
			name: "Nil send channel",
			args: msgSenderArgs{
				conn:      &fakeAudit{},
				heartbeat: validHeartbeat,
				client: &Client{
					notifier: make(chan NotifyError, 1),
					sendCh:   nil,
					metrics:  &metrics{},
				},
			},
			wantErr: true,
		},
		{
			name: "Nil metrics",
			args: msgSenderArgs{
				conn:      &fakeAudit{},
				heartbeat: validHeartbeat,
				client: &Client{
					notifier: make(chan NotifyError, 1),
					sendCh:   make(chan msgs.Msg, 1),
					metrics:  nil,
				},
			},
			wantErr: true,
		},
	}

	for _, test := range tests {
		err := test.args.validate()
		switch {
		case test.wantErr && err == nil:
			t.Errorf("TestMsgSenderArgsValidate(%s): got err == nil, want err != nil", test.name)
		case !test.wantErr && err != nil:
			t.Errorf("TestMsgSenderArgsValidate(%s): got err == %v, want err == nil", test.name, err)
		}
	}
}

func TestNewMsgSender(t *testing.T) {
	t.Parallel()

	validHeartbeat := msgs.Msg{Type: msgs.Heartbeat}
	validClient := &Client{
		notifier: make(chan NotifyError, 1),
		sendCh:   make(chan msgs.Msg, 1),
		metrics:  &metrics{},
	}
	validConn := &fakeAudit{}

	tests := []struct {
		name    string
		args    msgSenderArgs
		wantErr bool
	}{
		{
			name: "Valid args",
			args: msgSenderArgs{
				conn:      validConn,
				heartbeat: validHeartbeat,
				client:    validClient,
			},
			wantErr: false,
		},
		{
			name: "Invalid args - nil conn",
			args: msgSenderArgs{
				conn:      nil,
				heartbeat: validHeartbeat,
				client:    validClient,
			},
			wantErr: true,
		},
	}

	for _, test := range tests {
		got, err := newMsgSender(test.args)
		switch {
		case test.wantErr && err == nil:
			t.Errorf("TestNewMsgSender(%s) got err == nil, want err != nil", test.name)
			continue
		case !test.wantErr && err != nil:
			t.Errorf("TestNewMsgSender(%s) got err == %v, want err == nil", test.name, err)
			continue
		case err != nil:
			continue
		}

		// Verify fields are correctly set
		if got.conn != test.args.conn {
			t.Errorf("TestNewMsgSender(%s): .conn = %v, want %v", test.name, got.conn, test.args.conn)
		}
		if got.heartbeat.Type != test.args.heartbeat.Type {
			t.Errorf("TestNewMsgSender(%s): .heartbeat = %v, want %v", test.name, got.heartbeat, test.args.heartbeat)
		}
	}
}

func TestMsgSenderStart(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		params  *testParams
		wantErr bool
	}{
		{
			name: "Ends with nil",
			params: &testParams{
				serviceMsgVals: []error{nil},
			},
			wantErr: false,
		},
		{
			name: "Ends with error",
			params: &testParams{
				serviceMsgVals: []error{nil, fmt.Errorf("test error")},
			},
			wantErr: true,
		},
	}

	for _, test := range tests {
		sender := &msgSender{
			testParams: test.params,
		}
		ctx := t.Context()
		if !test.wantErr {
			var cancel context.CancelFunc
			ctx, cancel = context.WithCancel(ctx)
			go func() {
				time.Sleep(1 * time.Second)
				cancel() // Cancel the context to stop the sender
			}()
		}

		err := <-sender.start(ctx)
		switch {
		case test.wantErr && err == nil:
			t.Errorf("TestMsgSenderStart(%s): got err == nil, want err != nil", test.name)
			continue
		case !test.wantErr && err != nil:
			t.Errorf("TestMsgSenderStart(%s): got err == %v, want err == nil", test.name, err)
			continue
		}
	}

}

func TestServiceMsg(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		msgs    []msgs.Msg
		want    map[msgs.Type]int
		params  *testParams
		wantErr bool
	}{
		{
			name: "Success",
			msgs: []msgs.Msg{
				{Type: msgs.ControlPlane}, // <- This will be followed by a heartbeat
				{Type: msgs.DataPlane},
			},
			want: map[msgs.Type]int{
				msgs.ControlPlane: 1,
				msgs.Heartbeat:    1,
				msgs.DataPlane:    1,
			},
			params: &testParams{
				writerVals: []error{nil, nil, nil}, // The extra is the heartbeat
			},
		},
		{
			name: "Error on fourth message",
			msgs: []msgs.Msg{
				{Type: msgs.ControlPlane}, // <- This will be followed by a heartbeat
				{Type: msgs.DataPlane},
				{Type: msgs.DataPlane},
			},
			params: &testParams{
				writerVals: []error{nil, nil, nil, fmt.Errorf("test error")}, // The extra is the heartbeat
			},
			want: map[msgs.Type]int{
				msgs.ControlPlane: 1,
				msgs.Heartbeat:    1,
				msgs.DataPlane:    1,
			},
			wantErr: true,
		},
	}

	for _, test := range tests {
		sender := &msgSender{
			testParams: test.params,
			client:     &Client{sendCh: make(chan msgs.Msg, 1)},
			heartbeat:  msgs.Msg{Type: msgs.Heartbeat},
		}

		ctx, cancel := context.WithCancel(t.Context())

		recvWg := sync.WaitGroup{}
		recvWg.Add(1)
		loopLen := len(test.params.writerVals) - 1
		var recvErr error
		go func() {
			defer recvWg.Done()
			for i := 0; i < loopLen; i++ {
				recvErr = sender.serviceMsg(ctx)
				if recvErr != nil {
					cancel()
					break
				}
			}
		}()

		sendWg := sync.WaitGroup{}
		sendWg.Add(1)
		go func() {
			defer sendWg.Done()
			for _, msg := range test.msgs {
				sender.client.sendCh <- msg
			}
		}()

		sendWg.Wait()
		recvWg.Wait()

		switch {
		case test.wantErr && recvErr == nil:
			t.Errorf("TestServiceMsg(%s): got err == %v, want err != nil", test.name, recvErr)
		case !test.wantErr && recvErr != nil:
			t.Errorf("TestServiceMsg(%s): got err == %v, want err == nil", test.name, recvErr)
		}

		if diff := (&pretty.Config{PrintStringers: true}).Compare(test.want, sender.testParams.writeCount); diff != "" {
			t.Errorf("TestServiceMsg(%s): writeCount mismatch (-want +got):\n%s", test.name, diff)
		}
	}
}

func TestMsgSenderWrite(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		sendCh  chan msgs.Msg
		msg     msgs.Msg
		wantErr bool
	}{
		{
			name:   "Success",
			sendCh: make(chan msgs.Msg, 1),
			msg:    msgs.Msg{Type: msgs.DataPlane},
		},
		{
			name:    "Error + requeue success",
			sendCh:  make(chan msgs.Msg, 1),
			msg:     msgs.Msg{Type: msgs.DataPlane},
			wantErr: true, // Simulate an error after the first message
		},
		{
			name:    "Error + requeue fail",
			sendCh:  make(chan msgs.Msg), // No capacity to requeue
			msg:     msgs.Msg{Type: msgs.DataPlane},
			wantErr: true,
		},
	}

	for _, test := range tests {
		errAfter := 0
		if test.wantErr {
			errAfter = -1 // Force an error after the first message
		}

		sender := &msgSender{
			conn: &fakeDomainSocket{errAfter: errAfter},
			client: &Client{
				sendCh:   test.sendCh,
				notifier: make(chan NotifyError, 1),
				metrics:  newMetrics(),
			},
		}

		err := sender.write(test.msg)
		switch {
		case test.wantErr && err == nil:
			t.Errorf("TestMsgSenderWrite(%s): got err == nil, want err != nil", test.name)
			continue
		case !test.wantErr && err != nil:
			t.Errorf("TestMsgSenderWrite(%s): got err == %v, want err == nil", test.name, err)
			continue
		}

		if err != nil {
			if cap(test.sendCh) > 0 {
				if len(test.sendCh) != 1 {
					t.Errorf("TestMsgSenderWrite(%s): expected errored message to be requeued, was not", test.name)
				}
			}
			select {
			case <-sender.client.notifier:
			default:
				t.Errorf("TestMsgSenderWrite(%s): expected notifier to receive an error, did not", test.name)
			}
		}
	}

}

type fakeAudit struct {
	conn.Audit
}

var _ conn.Audit = &fakeDomainSocket{}

type fakeDomainSocket struct {
	conn.Audit // Embed the Audit interface to satisfy the conn.Audit interface, basically need the private method.
	recMsgs    []msgs.Msg
	hbMsgs     []msgs.Msg
	mu         sync.Mutex

	num      atomic.Uint64
	errAfter int
}

func (a *fakeDomainSocket) Type() conn.Type {
	return conn.TypeDomainSocket
}

func (a *fakeDomainSocket) collected(i int) {
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

func (a *fakeDomainSocket) Write(msg msgs.Msg) error {
	if a.errAfter < 0 {
		return errors.New("error on first write")
	}
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

func (a *fakeDomainSocket) CloseSend() error {
	return nil
}
