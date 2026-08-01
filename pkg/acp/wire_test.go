package acp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestCurrentModeUpdate_WireShape locks the exact JSON shape of a
// current_mode_update session/update notification. The spec requires the
// field "currentModeId" -- not "modeId" (M1). A regression here means a
// serde-strict client (Zed) will hard-fail deserializing set-mode updates.
func TestCurrentModeUpdate_WireShape(t *testing.T) {
	t.Parallel()

	update := NewCurrentModeUpdate("model:gpt-5")
	data, err := json.Marshal(update)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if raw["sessionUpdate"] != "current_mode_update" {
		t.Fatalf("sessionUpdate = %v, want current_mode_update", raw["sessionUpdate"])
	}
	if raw["currentModeId"] != "model:gpt-5" {
		t.Fatalf("currentModeId = %v, want model:gpt-5", raw["currentModeId"])
	}
	if _, present := raw["modeId"]; present {
		t.Fatalf("wire payload must not carry the legacy modeId field: %v", raw)
	}
}

// TestAgent_SetMode_EmitsCurrentModeIdOverTheWire drives the full
// session/set_mode -> session/update path end to end and asserts the raw
// bytes on the wire carry "currentModeId", matching the ACP schema.
func TestAgent_SetMode_EmitsCurrentModeIdOverTheWire(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	agent := NewAgent("test", "0.1", AgentHandlers{})
	agent.transport = NewTransport(strings.NewReader(""), &out)

	session := &AgentSession{
		ID: "sess-1",
		Modes: &SessionModeState{
			CurrentModeID: "default",
			AvailableModes: []SessionMode{
				{ID: "default", Name: "Default"},
				{ID: "model:gpt-5", Name: "GPT-5"},
			},
		},
	}
	agent.sessions[session.ID] = session

	req := &Request{JSONRPC: "2.0", ID: "1", Method: "session/set_mode", Params: mustMarshal(t, SetModeParams{
		SessionID: session.ID,
		ModeID:    "model:gpt-5",
	})}
	agent.handleSessionSetMode(nil, req) //nolint:staticcheck // ctx unused by this handler's fast path

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected a response and a notification, got %d lines: %q", len(lines), out.String())
	}

	var notif Notification
	if err := json.Unmarshal([]byte(lines[1]), &notif); err != nil {
		t.Fatalf("unmarshal notification: %v", err)
	}
	if notif.Method != "session/update" {
		t.Fatalf("method = %q, want session/update", notif.Method)
	}
	if !bytes.Contains(notif.Params, []byte(`"currentModeId":"model:gpt-5"`)) {
		t.Fatalf("params missing currentModeId on the wire: %s", notif.Params)
	}
	if bytes.Contains(notif.Params, []byte(`"modeId"`)) {
		t.Fatalf("params must not carry the legacy modeId field: %s", notif.Params)
	}
}

// TestShutdown_ReturnsEmptyObjectResult locks M5: a JSON-RPC response must
// carry either "result" or "error". Buckley's shutdown previously sent
// neither by responding with a nil result.
func TestShutdown_ReturnsEmptyObjectResult(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	agent := NewAgent("test", "0.1", AgentHandlers{})
	agent.transport = NewTransport(strings.NewReader(""), &out)

	agent.handleShutdown(nil, &Request{JSONRPC: "2.0", ID: "1", Method: "_shutdown"}) //nolint:staticcheck

	var raw map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &raw); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	result, hasResult := raw["result"]
	_, hasError := raw["error"]
	if !hasResult && !hasError {
		t.Fatalf("response carries neither result nor error: %s", out.String())
	}
	if hasError {
		t.Fatalf("unexpected error in shutdown response: %v", raw["error"])
	}
	resultObj, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("result = %T(%v), want an empty object", result, result)
	}
	if len(resultObj) != 0 {
		t.Fatalf("result = %v, want {}", resultObj)
	}
}

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}
