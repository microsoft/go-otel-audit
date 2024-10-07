package base

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/microsoft/go-otel-audit/audit/conn"
	"github.com/microsoft/go-otel-audit/audit/msgs"
)

type badConn struct {
	conn.Audit
}

func (b *badConn) Write(context.Context, msgs.Msg) error {
	return errors.New("error")
}

// https://github.com/microsoft/go-otel-audit/issues/15
// Basically, this is when we have more buffer than issues coming in, the far side stops
// processing the messages and we requeue them until the buffer is full, which never occurs.
func TestTimeoutCausesRetryLoop(t *testing.T) {
	t.Parallel()

	m := &metrics{}
	m.init()

	c := &Client{
		stopSend: make(chan chan struct{}),
		sendCh:   make(chan SendMsg, 1),
		metrics:  m,
	}
	var i conn.Audit = &badConn{}
	c.conn.Store(&i)

	go c.sender()

	c.sendCh <- SendMsg{
		Ctx: context.Background(),
		Msg: msgs.Msg{
			Type:   msgs.DataPlane,
			Record: validRecord.Clone(),
		},
	}

	time.Sleep(2 * time.Second)

	if c.metrics.requeuedCounter.Load() > 1 {
		t.Fatalf("TestTimeoutCausesRetryLoop: requeuedCounter should not have been incremented more than once")
	}
}
