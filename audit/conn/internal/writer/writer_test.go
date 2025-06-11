package writer

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"github.com/microsoft/go-otel-audit/audit/msgs"
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
	c := New(fc)

	if err := c.Write(context.Background(), msg); err != nil {
		t.Errorf("Write returned an error: %v", err)
	}
}

// TestWriteDeadline tests that the Write sets a deadline if the context has a deadline or
// a default of 15 seconds if the context does not have a deadline.
// Regression: https://github.com/microsoft/go-otel-audit/issues/17
func TestWriteDeadline(t *testing.T) {
	t.Parallel()

	now := time.Now()
	nower := func() time.Time {
		return now
	}
	setDeadlineCtx, cancel := context.WithDeadline(t.Context(), now.Add(5*time.Second))
	defer cancel()
	setDeadline, _ := setDeadlineCtx.Deadline()

	tests := []struct {
		name         string
		ctx          context.Context
		wantDeadline time.Time
	}{
		{
			name:         "WithDeadline",
			ctx:          setDeadlineCtx,
			wantDeadline: setDeadline,
		},
		{
			name:         "WithoutDeadline",
			ctx:          context.Background(),
			wantDeadline: now.Add(15 * time.Second),
		},
	}

	for _, test := range tests {
		fnc := &fakeNetConn{}
		fc := &fakeConn{Conn: fnc, buff: &bytes.Buffer{}}
		c := New(fc)
		c.now = nower

		msg, err := msgs.New(msgs.ControlPlane)
		if err != nil {
			panic(err)
		}

		c.Write(test.ctx, msg)
		if !fnc.deadline.Equal(test.wantDeadline) {
			t.Errorf("TestWriteDeadline(%s): got %v, want %v", test.name, fnc.deadline, test.wantDeadline)
		}
	}
}
