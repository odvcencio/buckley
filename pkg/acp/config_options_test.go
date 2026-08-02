package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestSessionConfigOption_WireShape locks the exact JSON shape of a
// "select" SessionConfigOption (S8) -- the shape CodeCompanion's model
// picker looks for via session/set_config_option's response and
// config_option_update.
func TestSessionConfigOption_WireShape(t *testing.T) {
	t.Parallel()

	option := NewModelConfigOption("model", "Model", "openai/gpt-4o", []SessionConfigSelectOption{
		{Value: "openai/gpt-4o", Name: "GPT-4o"},
		{Value: "anthropic/claude", Name: "Claude"},
	})

	data, err := json.Marshal(option)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if raw["id"] != "model" {
		t.Fatalf("id = %v, want model", raw["id"])
	}
	if raw["category"] != "model" {
		t.Fatalf("category = %v, want model", raw["category"])
	}
	if raw["type"] != "select" {
		t.Fatalf("type = %v, want select", raw["type"])
	}
	if raw["currentValue"] != "openai/gpt-4o" {
		t.Fatalf("currentValue = %v, want openai/gpt-4o", raw["currentValue"])
	}
	options, ok := raw["options"].([]any)
	if !ok || len(options) != 2 {
		t.Fatalf("options = %v, want 2 entries", raw["options"])
	}
}

// TestParseConfigOptionValue covers both value shapes
// SetConfigOptionParams.Value can carry: a boolean (type="boolean") and a
// value id string (type absent, the untagged default per the ACP schema).
func TestParseConfigOptionValue(t *testing.T) {
	t.Parallel()

	t.Run("boolean", func(t *testing.T) {
		t.Parallel()
		params := SetConfigOptionParams{Type: "boolean", Value: json.RawMessage(`true`)}
		got, err := ParseConfigOptionValue(params)
		if err != nil {
			t.Fatalf("ParseConfigOptionValue: %v", err)
		}
		if got.Boolean == nil || *got.Boolean != true {
			t.Fatalf("Boolean = %v, want true", got.Boolean)
		}
	})

	t.Run("value id (untagged default)", func(t *testing.T) {
		t.Parallel()
		params := SetConfigOptionParams{Value: json.RawMessage(`"openai/gpt-4o"`)}
		got, err := ParseConfigOptionValue(params)
		if err != nil {
			t.Fatalf("ParseConfigOptionValue: %v", err)
		}
		if got.ValueID != "openai/gpt-4o" {
			t.Fatalf("ValueID = %q, want openai/gpt-4o", got.ValueID)
		}
	})
}

// TestAgent_SessionNew_AdvertisesConfigOptions locks S8's session/new half:
// OnSessionConfigOptions' result must land in NewSessionResult.configOptions
// and on the stored AgentSession, the same way OnSessionModes already
// populates Modes.
func TestAgent_SessionNew_AdvertisesConfigOptions(t *testing.T) {
	t.Parallel()

	want := []SessionConfigOption{
		NewModelConfigOption("model", "Model", "openai/gpt-4o", []SessionConfigSelectOption{
			{Value: "openai/gpt-4o", Name: "GPT-4o"},
		}),
	}

	var out bytes.Buffer
	agent := NewAgent("test", "0.1", AgentHandlers{
		OnSessionConfigOptions: func(ctx context.Context, session *AgentSession) ([]SessionConfigOption, error) {
			return want, nil
		},
	})
	agent.transport = NewTransport(strings.NewReader(""), &out)

	req := &Request{JSONRPC: "2.0", ID: "1", Method: "session/new", Params: mustMarshal(t, NewSessionParams{Cwd: "/workspace"})}
	agent.handleSessionNew(nil, req) //nolint:staticcheck

	var resp Response
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	data, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var result NewSessionResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal NewSessionResult: %v", err)
	}
	if len(result.ConfigOptions) != 1 || result.ConfigOptions[0].ID != "model" {
		t.Fatalf("result.ConfigOptions = %+v, want the model option", result.ConfigOptions)
	}

	agent.sessionsMu.RLock()
	session := agent.sessions[result.SessionID]
	agent.sessionsMu.RUnlock()
	if session == nil || len(session.ConfigOptions) != 1 {
		t.Fatalf("session.ConfigOptions = %+v, want 1 entry", session)
	}
}

// TestAgent_SetConfigOption_RespondsAndBroadcasts drives
// session/set_config_option end to end: the response must carry the
// updated config options, a config_option_update notification must follow
// on the wire, and the session's stored ConfigOptions must reflect the
// change -- the same result-plus-notification contract set_mode already
// has for current_mode_update.
func TestAgent_SetConfigOption_RespondsAndBroadcasts(t *testing.T) {
	t.Parallel()

	updated := []SessionConfigOption{
		NewModelConfigOption("model", "Model", "anthropic/claude", []SessionConfigSelectOption{
			{Value: "openai/gpt-4o", Name: "GPT-4o"},
			{Value: "anthropic/claude", Name: "Claude"},
		}),
	}

	var gotConfigID string
	var gotValue ConfigOptionValue
	var out bytes.Buffer
	agent := NewAgent("test", "0.1", AgentHandlers{
		OnSetConfigOption: func(ctx context.Context, session *AgentSession, configID string, value ConfigOptionValue) ([]SessionConfigOption, error) {
			gotConfigID = configID
			gotValue = value
			return updated, nil
		},
	})
	agent.transport = NewTransport(strings.NewReader(""), &out)
	session := &AgentSession{ID: "sess-1"}
	agent.sessions[session.ID] = session

	req := &Request{JSONRPC: "2.0", ID: "1", Method: "session/set_config_option", Params: mustMarshal(t, SetConfigOptionParams{
		SessionID: session.ID,
		ConfigID:  "model",
		Value:     json.RawMessage(`"anthropic/claude"`),
	})}
	agent.handleSessionSetConfigOption(nil, req) //nolint:staticcheck

	if gotConfigID != "model" {
		t.Fatalf("configId passed to handler = %q, want model", gotConfigID)
	}
	if gotValue.ValueID != "anthropic/claude" {
		t.Fatalf("value passed to handler = %+v, want anthropic/claude", gotValue)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected a response and a notification, got %d lines: %q", len(lines), out.String())
	}

	var resp Response
	if err := json.Unmarshal([]byte(lines[0]), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	data, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var result SetConfigOptionResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal SetConfigOptionResult: %v", err)
	}
	if len(result.ConfigOptions) != 1 || result.ConfigOptions[0].CurrentValue != "anthropic/claude" {
		t.Fatalf("result.ConfigOptions = %+v, want currentValue anthropic/claude", result.ConfigOptions)
	}

	var notif Notification
	if err := json.Unmarshal([]byte(lines[1]), &notif); err != nil {
		t.Fatalf("unmarshal notification: %v", err)
	}
	if notif.Method != "session/update" {
		t.Fatalf("method = %q, want session/update", notif.Method)
	}
	if !bytes.Contains(notif.Params, []byte(`"sessionUpdate":"config_option_update"`)) {
		t.Fatalf("params missing config_option_update discriminator: %s", notif.Params)
	}
	if !bytes.Contains(notif.Params, []byte(`"currentValue":"anthropic/claude"`)) {
		t.Fatalf("params missing updated currentValue: %s", notif.Params)
	}

	agent.sessionsMu.RLock()
	stored := agent.sessions[session.ID]
	agent.sessionsMu.RUnlock()
	if len(stored.ConfigOptions) != 1 || stored.ConfigOptions[0].CurrentValue != "anthropic/claude" {
		t.Fatalf("stored session.ConfigOptions = %+v, want currentValue anthropic/claude", stored.ConfigOptions)
	}
}

// TestAgent_SetConfigOption_UnknownSessionErrors locks the same
// session-not-found contract every other session/* handler has.
func TestAgent_SetConfigOption_UnknownSessionErrors(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	agent := NewAgent("test", "0.1", AgentHandlers{})
	agent.transport = NewTransport(strings.NewReader(""), &out)

	req := &Request{JSONRPC: "2.0", ID: "1", Method: "session/set_config_option", Params: mustMarshal(t, SetConfigOptionParams{
		SessionID: "missing",
		ConfigID:  "model",
		Value:     json.RawMessage(`"x"`),
	})}
	agent.handleSessionSetConfigOption(nil, req) //nolint:staticcheck

	var resp Response
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("expected an error response for an unknown session")
	}
	if resp.Error.Code != ErrCodeSessionNotFound {
		t.Fatalf("error code = %d, want %d", resp.Error.Code, ErrCodeSessionNotFound)
	}
}
