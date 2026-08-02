package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"m31labs.dev/buckley/v2/pkg/acp"
	"m31labs.dev/buckley/v2/pkg/tool"
)

// acpFakeMCPServerBin holds the path to the compiled pkg/mcp/testdata/
// fakeserver binary, built once by TestMain and reused across every test in
// this package that needs a real MCP stdio server to spawn.
var acpFakeMCPServerBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "buckley-acp-fakeserver-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "acp test setup: mkdtemp:", err)
		os.Exit(1)
	}

	bin := filepath.Join(dir, "fakeserver")
	build := exec.Command("go", "build", "-o", bin, "m31labs.dev/buckley/v2/pkg/mcp/testdata/fakeserver")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "acp test setup: building fakeserver: %v\n%s\n", err, out)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	acpFakeMCPServerBin = bin

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// TestAttachACPMcpServers_SpawnsStdioAndBridgesTools drives S5's core
// behavior: a stdio server declared in session/new's mcpServers is spawned
// and its tools are bridged into the session's tool registry via the
// existing pkg/tool mcp bridging (RegisterMCPTools) -- not silently
// discarded.
func TestAttachACPMcpServers_SpawnsStdioAndBridgesTools(t *testing.T) {
	t.Parallel()

	var logs []string
	logf := func(format string, args ...interface{}) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	registry := tool.NewEmptyRegistry()
	declared := []acp.McpServer{
		{Name: "fake", Command: acpFakeMCPServerBin, Args: []string{"-tools=0"}},
	}

	manager := attachACPMcpServers(context.Background(), registry, declared, logf)
	if manager == nil {
		t.Fatalf("attachACPMcpServers returned nil manager; logs=%v", logs)
	}
	t.Cleanup(func() { _ = manager.Close() })

	// fakeserver in normal mode with -tools=0 exposes echo, fail, and
	// mixed_content -- three tools that RegisterMCPTools bridges as
	// mcp_fake_<tool>.
	for _, name := range []string{"mcp_fake_echo", "mcp_fake_fail", "mcp_fake_mixed_content"} {
		if _, ok := registry.Get(name); !ok {
			t.Errorf("registry missing bridged tool %q; registered tools logged: %v", name, logs)
		}
	}
}

// TestAttachACPMcpServers_SkipsUnsupportedTransports locks the other half
// of S5: http/sse declarations are not spawned as processes (Buckley
// advertises mcpCapabilities.http/sse=false), so a session declaring only
// unsupported transports gets no manager and no crash.
func TestAttachACPMcpServers_SkipsUnsupportedTransports(t *testing.T) {
	t.Parallel()

	registry := tool.NewEmptyRegistry()
	declared := []acp.McpServer{
		{Type: acp.McpServerKindHTTP, Name: "remote-http", URL: "https://example.com/mcp"},
		{Type: acp.McpServerKindSSE, Name: "remote-sse", URL: "https://example.com/mcp/sse"},
	}

	manager := attachACPMcpServers(context.Background(), registry, declared, func(string, ...interface{}) {})
	if manager != nil {
		t.Fatalf("expected nil manager for http/sse-only declarations, got %+v", manager)
		_ = manager.Close()
	}
	if len(registry.List()) != 0 {
		t.Fatalf("registry should stay empty when no stdio server connects, got %d tools", len(registry.List()))
	}
}

// TestAttachACPMcpServers_NoServersReturnsNil covers the common case: a
// session/new with no mcpServers at all must not attempt to build a
// manager.
func TestAttachACPMcpServers_NoServersReturnsNil(t *testing.T) {
	t.Parallel()

	registry := tool.NewEmptyRegistry()
	if manager := attachACPMcpServers(context.Background(), registry, nil, func(string, ...interface{}) {}); manager != nil {
		t.Fatalf("expected nil manager for no declared servers, got %+v", manager)
	}
}
