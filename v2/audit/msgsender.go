package audit

import (
	"fmt"
	"testing"
	"time"

	"github.com/gostdlib/base/context"
	"github.com/microsoft/go-otel-audit/v2/audit/conn"
	"github.com/microsoft/go-otel-audit/v2/audit/msgs"
)

// msgSender handles the sending of messages to the audit server for a Client.
type msgSender struct {
	conn      conn.Audit
	client    *Client
	heartbeat msgs.Msg

	ticker   *time.Ticker
	tickerCh <-chan time.Time

	testParams *testParams // For testing purposes, allows for faking in tests.
}

type msgSenderArgs struct {
	conn      conn.Audit
	heartbeat msgs.Msg
	client    *Client
}

func (m msgSenderArgs) validate() error {
	if m.conn == nil {
		return fmt.Errorf("audit connection cannot be nil")
	}
	if m.heartbeat.Type != msgs.Heartbeat {
		return fmt.Errorf("heartbeat message type must be %v, got %v", msgs.Heartbeat, m.heartbeat.Type)
	}
	if m.client == nil {
		return fmt.Errorf("audit client cannot be nil")
	}
	if m.client.notifier == nil {
		return fmt.Errorf("notifier channel cannot be nil")
	}
	if m.client.sendCh == nil {
		return fmt.Errorf("send channel cannot be nil")
	}
	if m.client.metrics == nil {
		return fmt.Errorf("metrics cannot be nil")
	}
	return nil
}

// newMsgSender creates a new message sender for the audit client.
func newMsgSender(args msgSenderArgs) (*msgSender, error) {
	if err := args.validate(); err != nil {
		return nil, fmt.Errorf("invalid msgSenderArgs: %w", err)
	}

	return &msgSender{
		conn:      args.conn,
		client:    args.client,
		heartbeat: args.heartbeat,
	}, nil
}

// start starts the message sender. It returns a channel that will receive an error if the sender fails.
// If the channel returns an error that is nil, it means the caller stopped the sender.
func (m *msgSender) start(ctx context.Context) <-chan error {
	ch := make(chan error, 1)
	go func() {
		defer close(ch)
		if err := m.sender(ctx); err != nil {
			select {
			case ch <- err:
			default:
			}
		}
	}()
	return ch
}

// sender is the async sender for the audit client. The context is for cancellations in tests.
func (m *msgSender) sender(ctx context.Context) error {
	defer func() {
		if m.ticker != nil {
			m.ticker.Stop()
		}
	}()

	for {
		// Used to stop the sender in a test.
		if ctx.Err() != nil {
			return nil
		}
		if err := m.serviceMsg(ctx); err != nil {
			return err
		}
	}
}

// service msg handles the sending of messages to the audit server. It yanks the a message off the send channel
// and writes it to the audit server. If the ticker is set, it will also send a heartbeat message at intervals.
// Heartbeats only happens after a successful message has been sent, which is a requirment in the designs for the service.
// ctx is used purely for cancellation in tests.
func (m *msgSender) serviceMsg(ctx context.Context) (err error) {
	if testing.Testing() && m.testParams != nil && m.testParams.serviceMsgVals != nil {
		return m.testParams.serviceMsg()
	}

	defer func() {
		if err != nil {
			m.ticker.Stop()
		}
	}()

	select {
	case <-ctx.Done():
		return
	// Send message and if its the first message, also send a heartbeat. Start a ticker for heartbeats.
	case msg := <-m.client.sendCh:
		if err := m.write(msg); err != nil {
			return err
		}
		// We only send a heartbeat after the first message has been sent successfully.
		if m.ticker == nil {
			if err := m.write(m.heartbeat); err != nil {
				return err
			}
			m.ticker = time.NewTicker(30 * time.Minute)
			m.tickerCh = m.ticker.C
		}
	// Send heartbeat if we have one and the ticker is set.
	case <-m.tickerCh:
		if err := m.write(m.heartbeat); err != nil {
			return err
		}
	}
	return nil
}

// write writes a message to the audit server.
func (m *msgSender) write(msg msgs.Msg) error {
	if testing.Testing() && m.testParams != nil && m.testParams.writerVals != nil {
		return m.testParams.writer(msg)
	}

	ctx := context.Background()
	if err := m.conn.Write(msg); err != nil {
		m.client.metrics.msgErrs.Add(ctx, 1)
		// Requeue the message if we can, drop if we can't.
		select {
		case m.client.sendCh <- msg:
			m.client.metrics.msgsRequeued.Add(ctx, 1)
		default:
			m.client.metrics.msgsDropped.Add(ctx, 1)
		}
		m.client.sendNotify(err)
		return err
	}
	m.client.metrics.msgsSent.Add(ctx, 1)
	return nil
}
