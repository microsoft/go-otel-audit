package writer

import (
	"bytes"
	"context"
	"net"
	"testing"

	"github.com/microsoft/go-otel-audit/audit/msgs"
)

type fakeConn struct {
	net.Conn

	buff *bytes.Buffer
}

func (f *fakeConn) Write(b []byte) (int, error) {
	f.buff.Write(b)
	return len(b), nil
}

func TestWrite(t *testing.T) {
	msg, err := msgs.New(msgs.ControlPlane)
	if err != nil {
		t.Fatalf("Failed to create a new message: %v", err)
	}

	fc := &fakeConn{buff: &bytes.Buffer{}}
	c := New(fc)

	if err := c.Write(context.Background(), msg); err != nil {
		t.Errorf("Write returned an error: %v", err)
	}
}
