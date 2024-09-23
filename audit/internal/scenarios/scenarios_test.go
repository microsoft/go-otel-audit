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
	"github.com/microsoft/go-otel-audit/audit/base"
	"github.com/microsoft/go-otel-audit/audit/conn"
	"github.com/microsoft/go-otel-audit/audit/msgs"
)

func TestSlowListeningMDSD(t *testing.T) {
	t.Parallel()

	const auditLogQueueSize = 1

	var validRecord = msgs.Record{
		CallerIpAddress:            msgs.MustParseAddr("192.168.0.1"),
		CallerIdentities:           map[msgs.CallerIdentityType][]msgs.CallerIdentityEntry{msgs.UPN: {{"user1@domain.com", "Description"}}},
		OperationCategories:        []msgs.OperationCategory{msgs.UserManagement},
		TargetResources:            map[string][]msgs.TargetResourceEntry{"ResourceType": {{"Name", "Cluster", "DataCenter", "Region"}}},
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
		return conn.NewDomainSocket(conn.DSPath(socketName))
	}

	// Creates the smart client to the remote audit server.
	// You should only create one of these, preferrably in main().
	c, err := audit.New(cc, audit.WithAuditOptions(base.WithSettings(base.Settings{QueueSize: auditLogQueueSize})))
	if err != nil {
		panic(err)
	}
	defer c.Close(context.Background())

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

func TestNonExistingSocket(t *testing.T) {
	t.Parallel()

	id := uuid.New().String()
	socketName := fmt.Sprintf("/tmp/mysocket-%s.sock", id)

	// Create a function that will create a new connection to the remote audit server.
	// We use this function to create a new connection when the connection is broken.
	cc := func() (conn.Audit, error) {
		return conn.NewDomainSocket(conn.DSPath(socketName))
	}

	// Creates the smart client to the remote audit server.
	// You should only create one of these, preferrably in main().
	_, err := audit.New(cc)
	if err != nil {
		return
	}
	t.Fatalf("TestNonExistingSocket: expected error, got nil")
}
