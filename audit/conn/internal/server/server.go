/*
Package server implements a generic audit server that can accept the audit messages from the client.
This is used exclusively to test the audit client's various conn implementations.

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
	"net/netip"
	"os"
	"time"

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

// AuditRecordTest is a generic AuditRecordTest that accepts connections and reads messages from them, outputting them to a channel.
type AuditRecordTest struct {
	connType string
	addr     string
	l        net.Listener
	msgCh    chan msgs.Record
}

// New creates a new AuditRecordTest server that uses the given connection type and address to listen on.
// The connType must be one recognized by net.Listen, such as "unix" or "tcp".
func New(connType, addr string) (*AuditRecordTest, error) {
	l, err := net.Listen(connType, addr)
	if err != nil {
		return nil, fmt.Errorf("unable to create server socket(%s): %w", addr, err)
	}

	serv := &AuditRecordTest{
		connType: connType,
		addr:     addr,
		l:        l,
		msgCh:    make(chan msgs.Record, 1),
	}
	go serv.accept()
	return serv, nil
}

func (c *AuditRecordTest) restart() {
	l, err := net.Listen(c.connType, c.addr)
	if err != nil {
		panic(fmt.Errorf("unable to create server socket(%s): %w", c.addr, err))
	}
	c.l = l

	go c.accept()
}

// MsgCh returns the channel that messages are sent to.
func (c *AuditRecordTest) MsgCh() <-chan msgs.Record {
	return c.msgCh
}

// Close closes the server.
func (c *AuditRecordTest) Close() error {
	defer close(c.msgCh)
	return c.l.Close()
}

func (c *AuditRecordTest) accept() {
	go func() {
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
			go c.readMsgs(conn)
		}
	}()
}

func (c *AuditRecordTest) readMsgs(conn net.Conn) {
	dec := msgpack.NewDecoder(conn)
	for {
		msgWrap := []any{}
		err := dec.Decode(&msgWrap)
		if err != nil {
			if err != io.EOF {
				log.Println(err)
				log.Printf("%+v\n", msgWrap)
			}
			continue
		}
		msg := msgFromWrap(msgWrap)
		if msg.OperationAccessLevel == "resetConn" {
			log.Println("Resetting connection")
			conn.Close()
			time.Sleep(2 * time.Second)
			go func() {
				if _, err := netip.ParseAddrPort(c.addr); err != nil {
					os.Remove(c.addr) // For domain sockets
				}
				c.restart()
			}()
			return
		}
		c.msgCh <- msg
	}
}
