package scenarios

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/microsoft/go-otel-audit/audit"
	"github.com/microsoft/go-otel-audit/audit/conn"
	"github.com/microsoft/go-otel-audit/audit/internal/server"
	"github.com/microsoft/go-otel-audit/audit/msgs"
)

func TestSlowListeningMDSD(t *testing.T) {
	t.Parallel()

	const auditLogQueueSize = 1

	var validRecord = msgs.Record{
		CallerIpAddress:            msgs.MustParseAddr("192.168.0.1"),
		CallerIdentities:           map[msgs.CallerIdentityType][]msgs.CallerIdentityEntry{msgs.UPN: {{Identity: "user1@domain.com", Description: "Description"}}},
		OperationCategories:        []msgs.OperationCategory{msgs.UserManagement},
		TargetResources:            map[string][]msgs.TargetResourceEntry{"ResourceType": {{Name: "Name", Cluster: "Cluster", DataCenter: "DataCenter", Region: "Region"}}},
		CallerAccessLevels:         []string{"Level1"},
		OperationAccessLevel:       "AccessLevel",
		OperationName:              "Operation",
		OperationResultDescription: "ResultDescription",
		CallerAgent:                "Agent",
	}

	id := uuid.New().String()
	socketName := fmt.Sprintf("/tmp/mysocket-%s.sock", id)
	defer os.Remove(socketName)

	// Create a Unix domain socket and listen for incoming connections.
	// However, for this, we are not going to listen at all.
	socket, err := net.Listen("unix", socketName)
	if err != nil {
		panic(err)
	}
	defer socket.Close()

	// Create a function that will create a new connection to the remote audit server.
	// We use this function to create a new connection when the connection is broken.
	cc := func() (conn.Audit, error) {
		return conn.NewDomainSocket(conn.DomainSocketPath(socketName))
	}

	// Creates the smart client to the remote audit server.
	// You should only create one of these, preferrably in main().
	c, err := audit.New(t.Context(), uuid.New(), cc, audit.WithQueueSize(auditLogQueueSize))
	if err != nil {
		panic(err)
	}

	go func() {
		c, err := socket.Accept()
		if err != nil {
			panic(err)
		}

		b := make([]byte, 1024)
		for {
			c.Read(b)
			time.Sleep(500 * time.Millisecond)
		}
	}()

	for i := 0; i < 10000; i++ {
		// Send a message to the remote audit server.
		if err := c.Send(context.Background(), msgs.Msg{Type: msgs.ControlPlane, Record: validRecord}); err != nil {
			log.Printf("msg(%d): %s}", i, err)
		}
	}
}

// TestDeferredConnection is an end to end test of WithDeferredConnection() against a domain socket
// that does not exist when the client is created. Without the option, New() must return an error.
// With the option, New() must return a client that accepts sends as a no-op while it keeps retrying
// the connection in the background, then delivers messages once the audit server comes up.
func TestDeferredConnection(t *testing.T) {
	t.Parallel()

	var validRecord = msgs.Record{
		CallerIpAddress:            msgs.MustParseAddr("192.168.0.1"),
		CallerIdentities:           map[msgs.CallerIdentityType][]msgs.CallerIdentityEntry{msgs.UPN: {{Identity: "user1@domain.com", Description: "Description"}}},
		OperationCategories:        []msgs.OperationCategory{msgs.UserManagement},
		TargetResources:            map[string][]msgs.TargetResourceEntry{"ResourceType": {{Name: "Name", Cluster: "Cluster", DataCenter: "DataCenter", Region: "Region"}}},
		CallerAccessLevels:         []string{"Level1"},
		OperationAccessLevel:       "AccessLevel",
		OperationName:              "Operation",
		OperationResultDescription: "ResultDescription",
		CallerAgent:                "Agent",
	}

	id := uuid.New().String()
	socketName := fmt.Sprintf("/tmp/mysocket-%s.sock", id)
	defer os.Remove(socketName)

	// Create a function that will create a new connection to the remote audit server.
	// The socket does not exist yet, so connections will fail until the server comes up.
	cc := func() (conn.Audit, error) {
		return conn.NewDomainSocket(conn.DomainSocketPath(socketName))
	}

	// Without WithDeferredConnection(), New() must fail because there is no audit server.
	if _, err := audit.New(t.Context(), uuid.New(), cc); err == nil {
		t.Fatalf("TestDeferredConnection: audit.New() without WithDeferredConnection(): got err == nil, want err != nil")
	}

	// With WithDeferredConnection(), New() must return a working client backed by a no-op sender.
	c, err := audit.New(t.Context(), uuid.New(), cc, audit.WithDeferredConnection())
	if err != nil {
		t.Fatalf("TestDeferredConnection: audit.New() with WithDeferredConnection(): got err == %s, want err == nil", err)
	}

	// The no-op client must accept sends while the audit server is down.
	if err := c.Send(t.Context(), msgs.Msg{Type: msgs.ControlPlane, Record: validRecord}); err != nil {
		t.Fatalf("TestDeferredConnection: Send() while disconnected: got err == %s, want err == nil", err)
	}

	// The client keeps retrying the connection in the background. Every failed attempt emits
	// a notification, so seeing two of them proves the retry loop is running.
	deadline := time.After(30 * time.Second)
	for retries := 0; retries < 2; {
		select {
		case <-c.Notify():
			retries++
		case <-deadline:
			t.Fatalf("TestDeferredConnection: client never retried the connection in the background")
		}
	}

	// Bring up the audit server. The background retries should now succeed and the client
	// should switch from the no-op sender to a real connection.
	serv, err := server.New("unix", socketName)
	if err != nil {
		panic(err)
	}
	defer serv.Close()

	// Keep sending until a record reaches the server, which proves the client switched from
	// the no-op sender to the real connection. Messages sent before the switch are dropped.
	deadline = time.After(30 * time.Second)
	for {
		if err := c.Send(t.Context(), msgs.Msg{Type: msgs.ControlPlane, Record: validRecord}); err != nil {
			t.Fatalf("TestDeferredConnection: Send() after the server came up: got err == %s, want err == nil", err)
		}
		select {
		case <-serv.MsgCh():
			return
		case <-deadline:
			t.Fatalf("TestDeferredConnection: no message reached the audit server after it became available")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func TestNonExistingSocket(t *testing.T) {
	t.Parallel()

	id := uuid.New().String()
	socketName := fmt.Sprintf("/tmp/mysocket-%s.sock", id)

	// Create a function that will create a new connection to the remote audit server.
	// We use this function to create a new connection when the connection is broken.
	cc := func() (conn.Audit, error) {
		return conn.NewDomainSocket(conn.DomainSocketPath(socketName))
	}

	// Creates the smart client to the remote audit server.
	// You should only create one of these, preferrably in main().
	_, err := audit.New(t.Context(), uuid.New(), cc)
	if err != nil {
		return
	}
	t.Fatalf("TestNonExistingSocket: expected error, got nil")
}
