package writer

import (
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/microsoft/go-otel-audit/v2/audit/msgs"
)

type fakeNetConn struct {
	net.Conn
	deadline time.Time
}

func (f *fakeNetConn) SetWriteDeadline(t time.Time) error {
	f.deadline = t
	return nil
}

type fakeConn struct {
	net.Conn

	buff *bytes.Buffer
}

func (f *fakeConn) Write(b []byte) (int, error) {
	l, err := f.buff.Write(b)
	if err != nil {
		return 0, err
	}
	return l, nil
}

func TestWrite(t *testing.T) {
	t.Parallel()

	msg, err := msgs.New(msgs.ControlPlane)
	if err != nil {
		t.Fatalf("Failed to create a new message: %v", err)
	}

	fc := &fakeConn{Conn: &fakeNetConn{}, buff: &bytes.Buffer{}}
	c, err := New(fc)
	if err != nil {
		panic(err)
	}

	if err := c.Write(msg); err != nil {
		t.Errorf("TestWrite: .Write() returned an error: %v", err)
	}
}
