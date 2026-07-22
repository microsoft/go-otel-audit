/*
Package server implements a generic audit server that can accept the audit messages from the client.
This is used exclusively in tests, such as testing the audit client's various conn implementations
and end to end scenarios.

Here's an example of retrieving the messages from the server running on a unix socket:

	serv, err := server.New("unix", "/tmp/audit.sock")
	if err != nil {
		t.Fatalf("unable to create server: %v", err)
	}
	defer serv.Close()

	go func() {
		for msg := range serv.MsgCh() {
			log.Println(msg) // Or whatever you want to do with them
		}
	}()
*/
package server

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"

	"github.com/gostdlib/base/concurrency/sync"
	"github.com/gostdlib/base/context"
	"github.com/microsoft/go-otel-audit/audit/msgs"

	"github.com/go-json-experiment/json"
	"github.com/vmihailenco/msgpack/v4"
)

// msgFromWrap converts the wrapped message into a msgs.Record.
func msgFromWrap(a []any) msgs.Record {
	// This gets the map[string]any from the wrapped message
	// that represents our record.
	m := a[1].([]any)[0].([]any)[1].(map[string]any)

	// We are now going to remarshal it. We couldn't unmarshal it
	// originally to the concrete type because of bugs in msgpack.
	// So now we write it out to JSON.
	b, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}

	// Now we can unmarshal it to the concrete type.
	msg := msgs.Record{}

	if err := json.Unmarshal(b, &msg); err != nil {
		log.Println(string(b))
		panic(err)
	}
	return msg
}

// AuditRecordTest is a generic AuditRecordTest that accepts connections and reads messages from them,
// outputting them to a channel.
type AuditRecordTest struct {
	connType string
	addr     string
	l        net.Listener
	msgCh    chan msgs.Record

	// routines runs the accept loop and one reader per accepted connection, so this package owns no
	// bare goroutines.
	routines *sync.Group

	closeOnce sync.Once
}

// New creates a new AuditRecordTest server that uses the given connection type and address to listen on.
// The connType must be one recognized by net.Listen, such as "unix" or "tcp".
func New(connType, addr string) (*AuditRecordTest, error) {
	l, err := net.Listen(connType, addr)
	if err != nil {
		return nil, fmt.Errorf("unable to create server socket(%s): %w", addr, err)
	}

	ctx := context.Background()
	routines := context.Pool(ctx).Sub(ctx, "auditTestServer").Group()

	serv := &AuditRecordTest{
		connType: connType,
		addr:     addr,
		l:        l,
		msgCh:    make(chan msgs.Record, 1),
		routines: &routines,
	}

	serv.routines.Go(ctx, func(ctx context.Context) error {
		serv.accept(ctx)
		return nil
	})
	return serv, nil
}

// MsgCh returns the channel that messages are sent to. It is never closed: connections accepted before
// Close stay readable afterwards, so there is no point at which the server can promise no further sends.
// Read it with a select rather than ranging over it if you need the read to terminate.
func (c *AuditRecordTest) MsgCh() <-chan msgs.Record {
	return c.msgCh
}

// Close stops the server from accepting new connections. Connections already accepted are left alone and
// stay readable, which is what lets a test delete the socket and close the server while an established
// client keeps writing. It is safe to call more than once.
func (c *AuditRecordTest) Close() error {
	var err error
	c.closeOnce.Do(func() {
		log.Println("Closing server")
		err = c.l.Close()
	})
	return err
}

func (c *AuditRecordTest) accept(ctx context.Context) {
	for {
		conn, err := c.l.Accept()
		if err != nil {
			c.l.Close()
			if err != io.EOF {
				// This seems to be the error that happens once a conn is closed for a UDS listener.
				var opErr *net.OpError
				if errors.As(err, &opErr) && opErr.Op != "accept" {
					log.Println(err)
				}
			}
			return
		}
		log.Println("Accepted connection")

		c.routines.Go(ctx, func(ctx context.Context) error {
			c.readMsgs(conn)
			return nil
		})
	}
}

func (c *AuditRecordTest) readMsgs(conn net.Conn) {
	dec := msgpack.NewDecoder(conn)
	for {
		msgWrap := []any{}
		if err := dec.Decode(&msgWrap); err != nil {
			// Return rather than continue. Once the peer is gone Decode returns io.EOF immediately and
			// forever, so continuing here spins a goroutine at full tilt for the life of the process. A
			// stream that fails to decode for any other reason is desynced and will not recover either.
			if err != io.EOF {
				log.Println(err)
				log.Printf("%+v\n", msgWrap)
			}
			return
		}
		msg := msgFromWrap(msgWrap)
		if msg.OperationAccessLevel == "resetConn" {
			log.Println("Resetting connection")
			conn.Close()
			return
		}
		c.msgCh <- msg
	}
}
