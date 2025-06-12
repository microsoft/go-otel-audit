package conn

import (
	"context"
	"fmt"
	"testing"

	"github.com/microsoft/go-otel-audit/v2/audit/msgs"
)

// TestHangConn is a test connection that hangs on CloseSend to demonstrate a hanging bug.
type TestHangConn struct {
	hangOnCloseSend bool
}

// NewTestHangConn creates a new test connection that hangs on CloseSend.
func NewTestHangConn() TestHangConn {
	if !testing.Testing() {
		panic("NewTestHangConn should only be used in tests")
	}
	return TestHangConn{hangOnCloseSend: true}
}

// Type returns the connection type for TestHangConn.
func (t TestHangConn) Type() Type {
	return TypeTCP
}

// Write writes a message to the connection. This is a no-op for TestHangConn.
func (t TestHangConn) Write(ctx context.Context, msg msgs.Msg) error {
	// Process writes normally
	return nil
}

// CloseSend closes the send side of the connection. This will hang indefinitely
// to simulate a bug where bufio.Writer.Flush() hangs when the network is slow or broken.
func (t TestHangConn) CloseSend(ctx context.Context) error {
	if t.hangOnCloseSend {
		fmt.Println("TestHangConn.CloseSend() called - simulating Flush() hang...")
		// Simulate the hang that occurs in bufio.Writer.Flush() when network is slow/broken
		select {} // This blocks forever, just like a real Flush() hang would
	}
	return nil
}

func (t TestHangConn) private() {}
