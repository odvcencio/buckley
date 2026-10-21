package mcp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

func TestCommandTransport_ExitingServerPreservesBufferedMessages(t *testing.T) {
	transport := &commandTransport{command: exec.Command(fakeServerBin, "-mode=burst"), limit: 1024}
	connection, err := transport.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for range 64 {
		message, err := connection.Read(ctx)
		if err != nil {
			t.Fatalf("buffered response dropped: %v", err)
		}
		response, ok := message.(*jsonrpc.Response)
		if !ok || string(response.Result) != `{"evidence":"final-buffered-result"}` {
			t.Fatalf("wrong response: %+v", message)
		}
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	t.Log("responses_preserved=64 parent_reaped=true close_idempotent=true")
}

func TestCommandTransport_GracefulShutdownPreservesOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cleanup")
	transport := &commandTransport{command: exec.Command(fakeServerBin, "-mode=shutdown", "-pid-file="+path), limit: 1024}
	connection, err := transport.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "cleaned" {
		t.Fatalf("server could not finish cleanup: %q %v", data, err)
	}
	t.Log("stdin_eof=true final_shutdown_write_ok=true cleanup_completed=true")
}
