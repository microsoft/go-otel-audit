package conn

import (
	"log"
	"os"
	"testing"
	"time"

	"github.com/microsoft/go-otel-audit/v2/audit/conn/internal/server"
	"github.com/microsoft/go-otel-audit/v2/audit/msgs"

	"github.com/kylelemons/godebug/pretty"
)

func init() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
}

var stdMsg = msgs.Msg{
	Type: msgs.ControlPlane,
	Record: msgs.Record{
		CallerIpAddress:            msgs.MustParseAddr("192.168.0.1"),
		CallerIdentities:           map[msgs.CallerIdentityType][]msgs.CallerIdentityEntry{msgs.UPN: {{"user1@domain.com", "Description"}}},
		OperationCategories:        []msgs.OperationCategory{msgs.UserManagement},
		TargetResources:            map[string][]msgs.TargetResourceEntry{"ResourceType": {{"Name", "Cluster", "DataCenter", "Region"}}},
		CallerAccessLevels:         []string{"Level1"},
		OperationAccessLevel:       "AccessLevel",
		OperationName:              "Operation",
		OperationResultDescription: "ResultDescription",
		CallerAgent:                "Agent",
	},
}

func msgExpect(a msgs.Record, t time.Time) []any {
	return []any{
		"AsmAuditDP", // event name
		[]any{
			[]any{
				t.Unix(), // timestamp in seconds
				a,
			},
		},
		struct{ TimeFormat string }{TimeFormat: "DateTime"}, // required by Geneva Agent
	}
}

func TestWriters(t *testing.T) {
	const dsAddr = "/tmp/audit.sock"
	const tcpAddr = "127.0.0.1:63424"

	tests := []struct {
		// desc is a description of the test.
		desc string
		// newServer is a function that creates a new server.
		newServer func() (*server.AuditRecordTest, error)
		// newConn is a function that creates a new connection to the server.
		newConn func() (Audit, error)
		// resetErr is true if we want to reset the connection somewhere in the test.
		resetErr bool
	}{
		{
			desc: "domain socket",
			newServer: func() (*server.AuditRecordTest, error) {
				return server.New("unix", dsAddr)
			},
			newConn: func() (Audit, error) {
				return NewDomainSocket(DSPath(dsAddr))
			},
		},
		{
			desc: "tcp socket",
			newServer: func() (*server.AuditRecordTest, error) {
				return server.New("tcp", tcpAddr)
			},
			newConn: func() (Audit, error) {
				return NewTCPConn(tcpAddr)
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.desc, func(t *testing.T) {
			os.Remove("/tmp/audit.sock")

			serv, err := test.newServer()
			if err != nil {
				t.Fatalf("unable to create server: %v", err)
			}
			defer serv.Close()

			w, err := test.newConn()
			if err != nil {
				t.Fatalf("unable to create writer: %v", err)
			}
			defer w.CloseSend()

			numTestMsgs := 5001
			collect := []msgs.Record{}
			collectDone := make(chan struct{})
			count := 0
			go func() {
				defer close(collectDone)
				for msg := range serv.MsgCh() {
					collect = append(collect, msg)
					count++
					if count%1000 == 0 {
						log.Println(count)
					}
					if count == numTestMsgs {
						return
					}
				}
			}()

			var hadErr bool
			for i := 0; i < numTestMsgs; i++ {
				err := w.Write(stdMsg)
				if err != nil {
					hadErr = true
					break
				}
			}

			if err := w.CloseSend(); err != nil {
				hadErr = true
			}

			if !hadErr {
				<-collectDone
			}

			expect := 0
			switch {
			case test.resetErr && !hadErr:
				t.Fatalf("expected an error, but didn't get one")
			case !test.resetErr && hadErr:
				t.Fatalf("expected no error, but got one")
			case hadErr:
				expect = numTestMsgs - 1
			case !hadErr:
				expect = numTestMsgs
			}

			// We should get the first 5000 messages, then the server dies, then we silently drop messages,
			// then when the server comes back up, we should get some of the messages. We don't know how many.
			if len(collect) < expect {
				t.Fatalf("expected at least %d messages, got %d", expect, len(collect))
			}

			for _, msg := range collect {
				if diff := pretty.Compare(stdMsg.Record, msg); diff != "" {
					t.Fatalf("msg.Record was not as expected: -want/+got:\n %s", diff)
				}
			}
		})
	}
}
