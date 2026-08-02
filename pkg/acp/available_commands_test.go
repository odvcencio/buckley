package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestNewAvailableCommandsUpdate_WireShape locks the exact JSON shape of an
// available_commands_update session update (S6).
func TestNewAvailableCommandsUpdate_WireShape(t *testing.T) {
	t.Parallel()

	update := NewAvailableCommandsUpdate([]AvailableCommand{
		{Name: "code-review", Description: "Review code"},
	})
	data, err := json.Marshal(update)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if raw["sessionUpdate"] != "available_commands_update" {
		t.Fatalf("sessionUpdate = %v, want available_commands_update", raw["sessionUpdate"])
	}
	commands, ok := raw["availableCommands"].([]any)
	if !ok || len(commands) != 1 {
		t.Fatalf("availableCommands = %v, want 1 entry", raw["availableCommands"])
	}
}

// TestAgent_SessionNew_BroadcastsAvailableCommands locks S6: session/new
// must trigger an available_commands_update notification (there is no
// equivalent inline field on NewSessionResult, unlike modes/configOptions)
// once OnSessionCommands returns a non-empty list.
func TestAgent_SessionNew_BroadcastsAvailableCommands(t *testing.T) {
	t.Parallel()

	want := []AvailableCommand{{Name: "code-review", Description: "Review code"}}

	var out bytes.Buffer
	agent := NewAgent("test", "0.1", AgentHandlers{
		OnSessionCommands: func(ctx context.Context, session *AgentSession) ([]AvailableCommand, error) {
			return want, nil
		},
	})
	agent.transport = NewTransport(strings.NewReader(""), &out)

	req := &Request{JSONRPC: "2.0", ID: "1", Method: "session/new", Params: mustMarshal(t, NewSessionParams{Cwd: "/workspace"})}
	agent.handleSessionNew(nil, req) //nolint:staticcheck

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
	if !bytes.Contains(notif.Params, []byte(`"sessionUpdate":"available_commands_update"`)) {
		t.Fatalf("params missing available_commands_update discriminator: %s", notif.Params)
	}
	if !bytes.Contains(notif.Params, []byte(`"name":"code-review"`)) {
		t.Fatalf("params missing the code-review command: %s", notif.Params)
	}
}

// TestAgent_SessionNew_NoCommandsSendsNoNotification asserts Buckley does
// not emit an empty available_commands_update when there is nothing to
// advertise (e.g. no skills registered), matching the modes/configOptions
// "nil means nothing to advertise" convention.
func TestAgent_SessionNew_NoCommandsSendsNoNotification(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	agent := NewAgent("test", "0.1", AgentHandlers{
		OnSessionCommands: func(ctx context.Context, session *AgentSession) ([]AvailableCommand, error) {
			return nil, nil
		},
	})
	agent.transport = NewTransport(strings.NewReader(""), &out)

	req := &Request{JSONRPC: "2.0", ID: "1", Method: "session/new", Params: mustMarshal(t, NewSessionParams{Cwd: "/workspace"})}
	agent.handleSessionNew(nil, req) //nolint:staticcheck

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected only the session/new response, got %d lines: %q", len(lines), out.String())
	}
}
