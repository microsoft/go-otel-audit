package conn

import (
	"os"
	"testing"
	"time"

	"github.com/microsoft/go-otel-audit/v2/audit/conn/internal/server"
	"github.com/microsoft/go-otel-audit/v2/audit/msgs"
)

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

func TestDeleteSocket(t *testing.T) {
	serv, err := server.New("unix", "/tmp/audit.sock")
	if err != nil {
		t.Fatalf("unable to create server: %v", err)
	}

	go func() {
		for range serv.MsgCh() {
			t.Log("received message on server")
		}
	}()

	conn, err := NewDomainSocket(DSPath("/tmp/audit.sock"))
	if err != nil {
		t.Fatalf("unable to create domain socket client: %v", err)
	}

	msg := msgs.Msg{
		Type:   msgs.DataPlane,
		Record: validRecord.Clone(),
	}

	go func() {
		time.Sleep(2 * time.Second)
		if err := os.Remove("/tmp/audit.sock"); err != nil {
			panic("unable to remove the domain socket for test")
		}
		t.Log("removed domain socket")

		if err := serv.Close(); err != nil {
			panic("unable to close the first server")
		}
		t.Log("closed first server")
	}()

	for i := 0; i < 100; i++ {
		if err := conn.Write(msg); err != nil {
			t.Fatalf("unable to write message to domain socket: %v", err)
		}
		t.Log("wrote message to domain socket")
		time.Sleep(1 * time.Second)
	}
}
