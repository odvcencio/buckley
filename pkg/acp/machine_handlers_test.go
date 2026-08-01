package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestMachineMethods_AreUnderscorePrefixed locks M4: every Buckley machine
// extension method is dispatched under its underscore-prefixed name, and
// the legacy non-spec name ("machine/spawn_agent" without the underscore)
// no longer routes anywhere.
func TestMachineMethods_AreUnderscorePrefixed(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	agent := NewAgent("test", "0.1", AgentHandlers{})
	agent.transport = NewTransport(strings.NewReader(""), &out)
	agent.SetMachineHandlers(MachineHandlers{
		OnSpawnAgent: func(ctx context.Context, params *SpawnAgentParams) (*SpawnAgentResult, error) {
			return &SpawnAgentResult{AgentID: "agent-1"}, nil
		},
	})

	req := &Request{
		JSONRPC: "2.0",
		ID:      "1",
		Method:  "_machine/spawn_agent",
		Params:  mustMarshal(t, SpawnAgentParams{SessionID: "s1", Task: "do work"}),
	}
	agent.handleRequest(context.Background(), req)

	var resp Response
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error dispatching _machine/spawn_agent: %+v", resp.Error)
	}

	out.Reset()
	legacyReq := &Request{
		JSONRPC: "2.0",
		ID:      "2",
		Method:  "machine/spawn_agent", // legacy, non-underscore-prefixed name
		Params:  mustMarshal(t, SpawnAgentParams{SessionID: "s1", Task: "do work"}),
	}
	agent.handleRequest(context.Background(), legacyReq)

	var legacyResp Response
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &legacyResp); err != nil {
		t.Fatalf("unmarshal legacy response: %v", err)
	}
	if legacyResp.Error == nil || legacyResp.Error.Code != ErrCodeMethodNotFound {
		t.Fatalf("expected method-not-found for the legacy non-underscore method name, got %+v", legacyResp)
	}
}

// TestShutdownMethod_IsUnderscorePrefixed locks M4 for the shutdown
// extension: only "_shutdown" dispatches; the bare "shutdown" name (which
// collides with an unrelated concept in other JSON-RPC servers) is not
// part of the ACP spec and must not route.
func TestShutdownMethod_IsUnderscorePrefixed(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	agent := NewAgent("test", "0.1", AgentHandlers{})
	agent.transport = NewTransport(strings.NewReader(""), &out)

	agent.handleRequest(context.Background(), &Request{JSONRPC: "2.0", ID: "1", Method: "_shutdown"})

	var resp Response
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error dispatching _shutdown: %+v", resp.Error)
	}

	out.Reset()
	agent.handleRequest(context.Background(), &Request{JSONRPC: "2.0", ID: "2", Method: "shutdown"})

	var legacyResp Response
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &legacyResp); err != nil {
		t.Fatalf("unmarshal legacy response: %v", err)
	}
	if legacyResp.Error == nil || legacyResp.Error.Code != ErrCodeMethodNotFound {
		t.Fatalf("expected method-not-found for the bare 'shutdown' method name, got %+v", legacyResp)
	}
}
