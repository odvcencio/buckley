package acp

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"
)

// startFakeClientAgent wires an Agent directly to one end of a pair of
// pipes and returns a Transport for a fake client on the other end. The
// agent's transport is assigned synchronously (as agent.Serve itself does
// at the top of its loop) before the response-routing reader goroutine
// starts, so there is a single, race-free writer of agent.transport --
// exactly the ordering agent.Serve guarantees in production, where
// RequestClientPermission is only ever called from a goroutine Serve
// itself spawned after that assignment.
func startFakeClientAgent(t *testing.T) (agent *Agent, client *Transport) {
	t.Helper()

	toClientR, toClientW := io.Pipe()
	fromClientR, fromClientW := io.Pipe()

	agent = NewAgent("test", "0.1", AgentHandlers{})
	agent.transport = NewTransport(fromClientR, toClientW)
	client = NewTransport(toClientR, fromClientW)

	go driveTransportReadLoop(agent.transport, nil)
	t.Cleanup(func() {
		_ = toClientW.Close()
		_ = fromClientW.Close()
	})

	return agent, client
}

// TestRequestClientPermission_Allowed drives the full
// session/request_permission flow: the fake client reads the request,
// selects the "allow" option, and RequestClientPermission returns a
// "selected" outcome with that option id.
func TestRequestClientPermission_Allowed(t *testing.T) {
	t.Parallel()

	agent, client := startFakeClientAgent(t)
	go respondToNextPermissionRequest(client, func(req Request) Response {
		return Response{JSONRPC: "2.0", ID: req.ID, Result: RequestPermissionResult{
			Outcome: RequestPermissionOutcome{Outcome: RequestPermissionOutcomeSelected, OptionID: "allow"},
		}}
	})

	outcome, err := agent.RequestClientPermission(context.Background(), "sess-1", ToolCallUpdate{
		ToolCallID: "call-1",
		Title:      "Run: rm -rf build",
		Kind:       ToolKindExecute,
	}, []PermissionOption{
		{OptionID: "allow", Name: "Allow", Kind: PermissionOptionKindAllowOnce},
		{OptionID: "reject", Name: "Reject", Kind: PermissionOptionKindRejectOnce},
	}, 5*time.Second)
	if err != nil {
		t.Fatalf("RequestClientPermission: %v", err)
	}
	if outcome.Outcome != RequestPermissionOutcomeSelected || outcome.OptionID != "allow" {
		t.Fatalf("outcome = %+v, want selected/allow", outcome)
	}
}

// TestRequestClientPermission_Denied covers the reject path.
func TestRequestClientPermission_Denied(t *testing.T) {
	t.Parallel()

	agent, client := startFakeClientAgent(t)
	go respondToNextPermissionRequest(client, func(req Request) Response {
		return Response{JSONRPC: "2.0", ID: req.ID, Result: RequestPermissionResult{
			Outcome: RequestPermissionOutcome{Outcome: RequestPermissionOutcomeSelected, OptionID: "reject"},
		}}
	})

	outcome, err := agent.RequestClientPermission(context.Background(), "sess-1", ToolCallUpdate{ToolCallID: "call-1"}, []PermissionOption{
		{OptionID: "allow", Name: "Allow", Kind: PermissionOptionKindAllowOnce},
		{OptionID: "reject", Name: "Reject", Kind: PermissionOptionKindRejectOnce},
	}, 5*time.Second)
	if err != nil {
		t.Fatalf("RequestClientPermission: %v", err)
	}
	if outcome.Outcome != RequestPermissionOutcomeSelected || outcome.OptionID != "reject" {
		t.Fatalf("outcome = %+v, want selected/reject", outcome)
	}
}

// TestRequestClientPermission_Cancelled covers the spec's cancellation
// outcome: the client responds with RequestPermissionOutcome::Cancelled
// (no optionId) when a session/cancel notification arrives mid-prompt.
func TestRequestClientPermission_Cancelled(t *testing.T) {
	t.Parallel()

	agent, client := startFakeClientAgent(t)
	go respondToNextPermissionRequest(client, func(req Request) Response {
		return Response{JSONRPC: "2.0", ID: req.ID, Result: RequestPermissionResult{
			Outcome: RequestPermissionOutcome{Outcome: RequestPermissionOutcomeCancelled},
		}}
	})

	outcome, err := agent.RequestClientPermission(context.Background(), "sess-1", ToolCallUpdate{ToolCallID: "call-1"}, []PermissionOption{
		{OptionID: "allow", Name: "Allow", Kind: PermissionOptionKindAllowOnce},
	}, 5*time.Second)
	if err != nil {
		t.Fatalf("RequestClientPermission: %v", err)
	}
	if outcome.Outcome != RequestPermissionOutcomeCancelled {
		t.Fatalf("outcome = %+v, want cancelled", outcome)
	}
	if outcome.OptionID != "" {
		t.Fatalf("outcome.OptionID = %q, want empty for a cancelled outcome", outcome.OptionID)
	}
}

// TestRequestClientPermission_TimesOutWhenClientNeverResponds proves the
// turn does not deadlock when the client never answers: the call returns a
// bounded error instead of blocking forever, so the caller can fall back.
func TestRequestClientPermission_TimesOutWhenClientNeverResponds(t *testing.T) {
	t.Parallel()

	agent, client := startFakeClientAgent(t)
	// Drain the request but never respond.
	go func() {
		for {
			if _, err := client.ReadMessage(); err != nil {
				return
			}
		}
	}()

	start := time.Now()
	_, err := agent.RequestClientPermission(context.Background(), "sess-1", ToolCallUpdate{ToolCallID: "call-1"}, []PermissionOption{
		{OptionID: "allow", Name: "Allow", Kind: PermissionOptionKindAllowOnce},
	}, 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected a timeout error when the client never responds")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("RequestClientPermission took %s, want a bounded wait", elapsed)
	}
}

// respondToNextPermissionRequest reads messages from client until it finds
// a session/request_permission request, answers it with resp(req), then
// keeps draining silently so the underlying pipe never blocks Buckley.
func respondToNextPermissionRequest(client *Transport, resp func(Request) Response) {
	for {
		msg, err := client.ReadMessage()
		if err != nil {
			return
		}
		var req Request
		if err := json.Unmarshal(msg, &req); err != nil || req.Method != "session/request_permission" {
			continue
		}
		_ = client.WriteMessage(withJSONRPCEnvelope(resp(req)))
		// Keep draining any further traffic so writes never block.
		for {
			if _, err := client.ReadMessage(); err != nil {
				return
			}
		}
	}
}

func withJSONRPCEnvelope(resp Response) Response {
	if resp.JSONRPC == "" {
		resp.JSONRPC = "2.0"
	}
	return resp
}
