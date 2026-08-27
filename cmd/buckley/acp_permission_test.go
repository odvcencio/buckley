package main

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/acp"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tool"
)

// startFakeACPAgentForPermissionTests wires a real *acp.Agent to a pipe pair
// and returns a client-side Transport plus a cancel func for cleanup. It
// waits for a full initialize round trip before returning: since
// agent.transport is assigned inside agent.Serve's goroutine, waiting for a
// response the same goroutine sent establishes a happens-before edge (via
// io.Pipe's internal channel synchronization) before the test calls
// RequestClientPermission from a different goroutine -- exactly how a real
// ACP client's first exchange is used in production, not a testing hack.
func startFakeACPAgentForPermissionTests(t *testing.T) (agent *acp.Agent, client *acp.Transport) {
	t.Helper()

	toClientR, toClientW := io.Pipe()
	fromClientR, fromClientW := io.Pipe()

	agent = acp.NewAgent("test", "0.1", acp.AgentHandlers{})
	client = acp.NewTransport(toClientR, fromClientW)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = toClientW.Close()
		_ = fromClientW.Close()
	})
	go func() {
		_ = agent.Serve(ctx, fromClientR, toClientW)
	}()

	initParams, err := json.Marshal(acp.InitializeParams{ProtocolVersion: acp.ProtocolVersion})
	if err != nil {
		t.Fatalf("marshal initialize params: %v", err)
	}
	if err := client.WriteMessage(&acp.Request{JSONRPC: "2.0", ID: "init", Method: "initialize", Params: initParams}); err != nil {
		t.Fatalf("write initialize: %v", err)
	}
	for {
		msg, err := client.ReadMessage()
		if err != nil {
			t.Fatalf("read initialize response: %v", err)
		}
		var resp acp.Response
		if err := json.Unmarshal(msg, &resp); err == nil && resp.ID == "init" {
			break
		}
	}

	return agent, client
}

// respondToNextPermissionRequestWith answers the next
// session/request_permission request client receives with the given
// optionId (or, if optionId == "cancelled", the Cancelled outcome), then
// keeps draining silently.
func respondToNextPermissionRequestWith(client *acp.Transport, optionID string) {
	go func() {
		for {
			msg, err := client.ReadMessage()
			if err != nil {
				return
			}
			var req acp.Request
			if err := json.Unmarshal(msg, &req); err != nil || req.Method != "session/request_permission" {
				continue
			}
			var result acp.RequestPermissionResult
			if optionID == "cancelled" {
				result.Outcome = acp.RequestPermissionOutcome{Outcome: acp.RequestPermissionOutcomeCancelled}
			} else {
				result.Outcome = acp.RequestPermissionOutcome{Outcome: acp.RequestPermissionOutcomeSelected, OptionID: optionID}
			}
			_ = client.SendResponse(req.ID, result)
		}
	}()
}

func acpTestToolCall(name string) model.ToolCall {
	return model.ToolCall{ID: "call-1", Function: model.FunctionCall{Name: name}}
}

func TestRequestACPToolPermission_ReadOnlyToolNeverAsks(t *testing.T) {
	t.Parallel()

	registry := tool.NewRegistry()
	allowed, reason := requestACPToolPermission(context.Background(), nil, nil, registry, "sess-1", acpTestToolCall("read_file"), map[string]any{"path": "a.go"}, "", nil)
	if !allowed {
		t.Fatalf("read-only tool should never be denied, reason=%q", reason)
	}
}

func TestRequestACPToolPermission_NoAgentUsesFallbackPolicy(t *testing.T) {
	t.Parallel()

	registry := tool.NewRegistry()

	// edit_file is a modifying (non-destructive) tool: the fallback policy
	// auto-approves it.
	allowed, reason := requestACPToolPermission(context.Background(), nil, nil, registry, "sess-1", acpTestToolCall("edit_file"), map[string]any{"path": "a.go"}, "", nil)
	if !allowed {
		t.Fatalf("expected fallback to auto-approve a modifying tool, reason=%q", reason)
	}

	// run_shell is destructive: the fallback policy denies it.
	allowed, reason = requestACPToolPermission(context.Background(), nil, nil, registry, "sess-1", acpTestToolCall("run_shell"), map[string]any{"command": "rm -rf /"}, "", nil)
	if allowed {
		t.Fatal("expected fallback to deny a destructive tool")
	}
	if reason == "" {
		t.Fatal("expected a denial reason")
	}
}

func TestRequestACPToolPermission_ClientAllows(t *testing.T) {
	t.Parallel()

	agent, client := startFakeACPAgentForPermissionTests(t)
	respondToNextPermissionRequestWith(client, "allow")

	registry := tool.NewRegistry()
	allowed, reason := requestACPToolPermission(context.Background(), nil, agent, registry, "sess-1", acpTestToolCall("run_shell"), map[string]any{"command": "go test ./..."}, "", nil)
	if !allowed {
		t.Fatalf("expected the client's allow decision to be honored, reason=%q", reason)
	}
}

func TestRequestACPToolPermission_ClientDenies(t *testing.T) {
	t.Parallel()

	agent, client := startFakeACPAgentForPermissionTests(t)
	respondToNextPermissionRequestWith(client, "reject")

	registry := tool.NewRegistry()
	allowed, reason := requestACPToolPermission(context.Background(), nil, agent, registry, "sess-1", acpTestToolCall("edit_file"), map[string]any{"path": "a.go"}, "", nil)
	if allowed {
		t.Fatal("expected the client's reject decision to be honored")
	}
	if reason == "" {
		t.Fatal("expected a denial reason")
	}
}

func TestRequestACPToolPermission_ClientCancels(t *testing.T) {
	t.Parallel()

	agent, client := startFakeACPAgentForPermissionTests(t)
	respondToNextPermissionRequestWith(client, "cancelled")

	registry := tool.NewRegistry()
	allowed, reason := requestACPToolPermission(context.Background(), nil, agent, registry, "sess-1", acpTestToolCall("edit_file"), map[string]any{"path": "a.go"}, "", nil)
	if allowed {
		t.Fatal("a cancelled permission request must not be treated as allowed")
	}
	if reason == "" {
		t.Fatal("expected a cancellation reason")
	}
}

// TestRequestACPToolPermission_TimeoutFallsBackWithoutDeadlock proves that
// when the client never answers, the call returns within a bounded time
// using the default risk policy instead of hanging the turn forever.
func TestRequestACPToolPermission_TimeoutFallsBackWithoutDeadlock(t *testing.T) {
	t.Parallel()

	agent, client := startFakeACPAgentForPermissionTests(t)
	// Drain but never respond.
	go func() {
		for {
			if _, err := client.ReadMessage(); err != nil {
				return
			}
		}
	}()

	var warnings []string
	logf := func(format string, args ...interface{}) {
		warnings = append(warnings, format)
	}

	registry := tool.NewRegistry()
	start := time.Now()
	allowed, reason := requestACPToolPermissionWithTimeout(context.Background(), nil, agent, registry, "sess-1", acpTestToolCall("run_shell"), map[string]any{"command": "rm -rf /"}, "", logf, 100*time.Millisecond)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("took %s, want a bounded wait", elapsed)
	}
	if allowed {
		t.Fatal("expected the destructive-tool fallback to deny when the client never responds")
	}
	if reason == "" {
		t.Fatal("expected a denial reason")
	}
	if len(warnings) == 0 {
		t.Fatal("expected a logged warning when falling back after a failed client request")
	}
}

func TestRequestACPToolPermission_OneShotShellApprovalModes(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	tests := []struct {
		name    string
		cfg     *config.Config
		command string
		want    bool
	}{
		{name: "safe allows read-only search", cfg: &config.Config{Approval: config.ApprovalConfig{Mode: "safe"}}, command: "rg TODO ./pkg", want: true},
		{name: "ask requires a client for read-only shell", cfg: &config.Config{Approval: config.ApprovalConfig{Mode: "ask"}}, command: "rg TODO ./pkg", want: false},
		{name: "safe requires a client for workspace build", cfg: &config.Config{Approval: config.ApprovalConfig{Mode: "safe"}}, command: "go build ./...", want: false},
		{name: "auto allows workspace test", cfg: &config.Config{Approval: config.ApprovalConfig{Mode: "auto"}}, command: "go test ./...", want: true},
		{name: "auto allows workspace build", cfg: &config.Config{Approval: config.ApprovalConfig{Mode: "auto"}}, command: "go build ./...", want: true},
		{name: "auto refuses external write", cfg: &config.Config{Approval: config.ApprovalConfig{Mode: "auto"}}, command: "touch /tmp/buckley-oneshot-out", want: false},
		{name: "auto refuses network by default", cfg: &config.Config{Approval: config.ApprovalConfig{Mode: "auto"}}, command: "curl https://example.com", want: false},
		{name: "auto permits configured network", cfg: &config.Config{Approval: config.ApprovalConfig{Mode: "auto", AllowNetwork: true}}, command: "curl https://example.com", want: true},
		{name: "yolo follows existing full autonomy semantics", cfg: &config.Config{Approval: config.ApprovalConfig{Mode: "yolo"}}, command: "rm -rf ./build", want: true},
		{name: "nil config defaults to safe", cfg: nil, command: "rg TODO ./pkg", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			allowed, reason := requestACPToolPermission(context.Background(), tt.cfg, nil, tool.NewRegistry(), "sess-1", acpTestToolCall("run_shell"), map[string]any{"command": tt.command}, workspace, nil)
			if allowed != tt.want {
				t.Fatalf("allowed=%v, want %v, reason=%q", allowed, tt.want, reason)
			}
			if !allowed && reason == "" {
				t.Fatal("expected a denial reason")
			}
		})
	}
}

func TestRequestACPToolPermission_NonShellToolsKeepLegacyFallback(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"", "ask", "safe", "auto", "yolo"} {
		cfg := &config.Config{Approval: config.ApprovalConfig{Mode: mode}}
		allowed, reason := requestACPToolPermission(context.Background(), cfg, nil, tool.NewRegistry(), "sess-1", acpTestToolCall("edit_file"), map[string]any{"path": "a.go"}, "", nil)
		if !allowed {
			t.Fatalf("mode %q: legacy modifying fallback denied: %s", mode, reason)
		}
	}
}
