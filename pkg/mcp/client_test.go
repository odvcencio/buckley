package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeConfig builds a Config that launches testdata/fakeserver in the given
// mode ("normal", "crash", or "malformed"; see main.go's -mode flag).
// extraArgs are appended after -mode=<mode>, e.g. "-tools=3".
func fakeConfig(t *testing.T, mode string, extraArgs ...string) Config {
	t.Helper()
	args := append([]string{"-mode=" + mode}, extraArgs...)
	return Config{
		Name:    "fake-" + mode,
		Command: fakeServerBin,
		Args:    args,
		Timeout: 5 * time.Second,
	}
}

func TestNewClient_EmptyCommand(t *testing.T) {
	_, err := NewClient(context.Background(), Config{Name: "test"})
	if err == nil {
		t.Fatal("expected error for empty command")
	}
}

func TestNewClient_NonexistentCommand(t *testing.T) {
	_, err := NewClient(context.Background(), Config{
		Name:    "test",
		Command: "definitely-not-a-real-binary-xyz",
		Timeout: time.Second,
	})
	if err == nil {
		t.Fatal("expected error for nonexistent command")
	}
}

func TestNewClient_HandshakeAndListTools(t *testing.T) {
	client, err := NewClient(context.Background(), fakeConfig(t, "normal", "-tools=2"))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	if client.ProtocolVersion() == "" {
		t.Fatal("expected a negotiated protocol version after initialize")
	}
	if info := client.ServerInfo(); info == nil || info.Name != "fake-normal" {
		t.Fatalf("expected server info name fake-normal, got %+v", info)
	}

	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	// echo, fail, mixed_content, synthetic_0, synthetic_1
	if len(tools) != 5 {
		t.Fatalf("expected 5 tools, got %d: %+v", len(tools), tools)
	}
	names := make(map[string]bool, len(tools))
	for _, tl := range tools {
		names[tl.Name] = true
	}
	for _, want := range []string{"echo", "fail", "mixed_content"} {
		if !names[want] {
			t.Fatalf("expected tool %q in list, got %v", want, names)
		}
	}
}

func TestClient_CallTool_Text(t *testing.T) {
	client, err := NewClient(context.Background(), fakeConfig(t, "normal", "-tools=0"))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	res, err := client.CallTool(context.Background(), "echo", map[string]any{"text": "hello mcp"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got error result: %+v", res)
	}
	if len(res.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(res.Content))
	}
}

func TestClient_CallTool_ServerReportedError(t *testing.T) {
	client, err := NewClient(context.Background(), fakeConfig(t, "normal", "-tools=0"))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	res, err := client.CallTool(context.Background(), "fail", nil)
	if err != nil {
		t.Fatalf("CallTool should not return a transport error for a tool-level failure: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError=true for the fail tool")
	}
}

func TestClient_CallTool_MixedContent(t *testing.T) {
	client, err := NewClient(context.Background(), fakeConfig(t, "normal", "-tools=0"))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	res, err := client.CallTool(context.Background(), "mixed_content", nil)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if len(res.Content) != 3 {
		t.Fatalf("expected 3 content blocks (text/image/resource), got %d", len(res.Content))
	}
}

func TestClient_ListTools_NotConnected(t *testing.T) {
	c := &Client{}
	if _, err := c.ListTools(context.Background()); err == nil {
		t.Fatal("expected error listing tools on an unconnected client")
	}
}

func TestClient_CallTool_NotConnected(t *testing.T) {
	c := &Client{}
	if _, err := c.CallTool(context.Background(), "echo", nil); err == nil {
		t.Fatal("expected error calling a tool on an unconnected client")
	}
}

func TestClient_Close_Idempotent(t *testing.T) {
	client, err := NewClient(context.Background(), fakeConfig(t, "normal", "-tools=0"))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("second Close should be a no-op, got: %v", err)
	}
}

func TestClient_MalformedResponse_SurfacesCleanError(t *testing.T) {
	client, err := NewClient(context.Background(), fakeConfig(t, "malformed"))
	if err != nil {
		t.Fatalf("NewClient (handshake should still succeed): %v", err)
	}
	t.Cleanup(func() { client.Close() })

	done := make(chan struct{})
	var listErr error
	go func() {
		_, listErr = client.ListTools(context.Background())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("ListTools did not return after the server sent malformed JSON-RPC (client may be hanging)")
	}
	if listErr == nil {
		t.Fatal("expected an error when the server responds with malformed JSON-RPC")
	}
}

func TestClient_Wait_ReturnsOnServerExit(t *testing.T) {
	client, err := NewClient(context.Background(), fakeConfig(t, "crash"))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	waitDone := make(chan error, 1)
	go func() { waitDone <- client.Wait() }()

	// Trigger the crash by calling the "crash" tool; the server process
	// exits without responding, so this call should fail with a transport
	// error while Wait unblocks concurrently.
	go func() {
		_, _ = client.CallTool(context.Background(), "crash", nil)
	}()

	select {
	case <-waitDone:
	case <-time.After(10 * time.Second):
		t.Fatal("Wait did not return after the server process crashed")
	}
}

func TestEnvSlice(t *testing.T) {
	out := envSlice(map[string]string{"FOO": "bar"})
	if len(out) != 1 || !strings.HasPrefix(out[0], "FOO=") {
		t.Fatalf("unexpected env slice: %v", out)
	}
}

func TestNewClient_ContextCanceledBeforeConnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewClient(ctx, fakeConfig(t, "normal"))
	if err == nil {
		t.Fatal("expected an error connecting with an already-canceled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Logf("connect error (context.Canceled not wrapped directly, acceptable): %v", err)
	}
}
