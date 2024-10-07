package regression

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/gostdlib/concurrency/prim/wait"
	"github.com/microsoft/go-otel-audit/audit"
	"github.com/microsoft/go-otel-audit/audit/base"
	"github.com/microsoft/go-otel-audit/audit/conn"
	"github.com/microsoft/go-otel-audit/audit/msgs"
)

// TestNonListeningMDSDCausesCloseToBlockForever was a bug found where Close() never closed due to waiting for the queue
// to empty. If the listener isn't working, this never happens.
// This test takes some time (around 20 seconds), because it has to hit a timeout that isn't worth faking.
func TestNonListeningMDSDCausesCloseToBlockForever(t *testing.T) {
	t.Parallel()

	const auditLogQueueSize = 4000

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

	// This is optional if you want to get notifications of logging problems or do something
	// with messages that are not sent.
	/*
		go func() {
			for notifyMsg := range c.Notify() {
				log.Println(notifyMsg)
			}
		}()
	*/

	ctx := context.Background()
	g := wait.Group{}
	for i := 0; i < 10; i++ {
		g.Go(
			ctx,
			func(ctx context.Context) error {
				for i := 0; i < 1000; i++ {
					// Send a message to the remote audit server.
					if err := c.Send(context.Background(), msgs.Msg{Type: msgs.ControlPlane, Record: validRecord}); err != nil {
						log.Printf("msg(%d): %s}", i, err)
					}
				}
				return nil
			},
		)
	}

	g.Wait(ctx)
}
