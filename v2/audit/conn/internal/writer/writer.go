// Package writer implements a writer to a remote audit server of any type that can be connected to
// by a net.Conn object.
package writer

import (
	"net"
	"time"

	"github.com/microsoft/go-otel-audit/v2/audit/msgs"
)

type now func() time.Time

// Conn is a generic writer to a remote audit server. It can use anything that implements the net.Conn interface.
type Conn struct {
	conn net.Conn
	now  now
}

// Option is an optional argument for the New() function to configure the connection.
type Option func(c *Conn) error

// New creates a new connection to the remote audit server.
func New(conn net.Conn, options ...Option) (*Conn, error) {
	for _, o := range options {
		if err := o(&Conn{conn: conn, now: time.Now}); err != nil {
			return nil, err
		}
	}
	return &Conn{conn: conn, now: time.Now}, nil
}

// Write writes a message to the remote audit server. Setting a timeout
// on the context will set the write deadline.
func (c *Conn) Write(msg msgs.Msg) error {
	c.conn.SetWriteDeadline(c.now().Add(15 * time.Second))

	var b []byte
	var err error
	b, err = msgs.MarshalMsgpack(msg)
	if err != nil {
		return err
	}

	_, err = c.conn.Write(b)
	return err
}

// CloseSend closes the send channel to the remote audit server.
func (c *Conn) CloseSend() error {
	return c.conn.Close()
}
