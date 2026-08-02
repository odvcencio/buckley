package acp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/telemetry"
)

func TestMachineBridge_TranslatesSpawned(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	var buf bytes.Buffer
	agent := NewAgent("test", "0.1", AgentHandlers{})
	agent.transport = NewTransport(strings.NewReader(""), &buf)

	bridge := NewMachineBridge(agent, hub, "sess-1", false)

	hub.Publish(telemetry.Event{
		Type:      telemetry.EventMachineSpawned,
		Timestamp: time.Now(),
		Data:      map[string]any{"agent_id": "a1", "modality": "classic"},
	})

	// Give the bridge goroutine time to process, then close to synchronize
	time.Sleep(200 * time.Millisecond)
	bridge.Close()

	output := buf.String()
	if output == "" {
		t.Fatal("expected notification output")
	}

	var notif Notification
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &notif); err != nil {
		t.Fatalf("unmarshal notification: %v", err)
	}
	if notif.Method != "_machine/notify" {
		t.Errorf("method = %q, want _machine/notify", notif.Method)
	}

	var params MachineNotifyParams
	if err := json.Unmarshal(notif.Params, &params); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if params.SessionID != "sess-1" {
		t.Errorf("sessionId = %q", params.SessionID)
	}
	if params.Kind != MachineEventAgent {
		t.Errorf("kind = %q", params.Kind)
	}
}

func TestMachineBridge_TranslatesStateChange(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	var buf bytes.Buffer
	agent := NewAgent("test", "0.1", AgentHandlers{})
	agent.transport = NewTransport(strings.NewReader(""), &buf)

	bridge := NewMachineBridge(agent, hub, "sess-1", false)

	hub.Publish(telemetry.Event{
		Type:      telemetry.EventMachineState,
		Timestamp: time.Now(),
		Data:      map[string]any{"agent_id": "a1", "from": "idle", "to": "calling_model"},
	})

	time.Sleep(200 * time.Millisecond)
	bridge.Close()

	output := buf.String()
	if output == "" {
		t.Fatal("expected notification output")
	}

	var notif Notification
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &notif); err != nil {
		t.Fatalf("unmarshal notification: %v", err)
	}

	var params MachineNotifyParams
	if err := json.Unmarshal(notif.Params, &params); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if params.Kind != MachineEventState {
		t.Errorf("kind = %q", params.Kind)
	}
}

func TestMachineBridge_TranslatesLockAcquired(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	var buf bytes.Buffer
	agent := NewAgent("test", "0.1", AgentHandlers{})
	agent.transport = NewTransport(strings.NewReader(""), &buf)

	bridge := NewMachineBridge(agent, hub, "sess-1", false)

	hub.Publish(telemetry.Event{
		Type:      telemetry.EventMachineLockAcquired,
		Timestamp: time.Now(),
		Data:      map[string]any{"agent_id": "a1", "path": "pkg/auth/login.go", "mode": "write"},
	})

	time.Sleep(200 * time.Millisecond)
	bridge.Close()

	output := buf.String()
	if output == "" {
		t.Fatal("expected notification output")
	}

	var notif Notification
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &notif); err != nil {
		t.Fatalf("unmarshal notification: %v", err)
	}

	var params MachineNotifyParams
	if err := json.Unmarshal(notif.Params, &params); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if params.Kind != MachineEventLock {
		t.Errorf("kind = %q", params.Kind)
	}
}

func TestMachineBridge_IgnoresUnrelatedEvents(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	var buf bytes.Buffer
	agent := NewAgent("test", "0.1", AgentHandlers{})
	agent.transport = NewTransport(strings.NewReader(""), &buf)

	bridge := NewMachineBridge(agent, hub, "sess-1", false)

	hub.Publish(telemetry.Event{
		Type:      telemetry.EventToolStarted,
		Timestamp: time.Now(),
		Data:      map[string]any{"tool": "read_file"},
	})

	time.Sleep(200 * time.Millisecond)
	bridge.Close()

	if buf.String() != "" {
		t.Errorf("expected no output for unrelated events, got %q", buf.String())
	}
}

func TestMachineBridge_Close(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	var buf bytes.Buffer
	agent := NewAgent("test", "0.1", AgentHandlers{})
	agent.transport = NewTransport(strings.NewReader(""), &buf)

	bridge := NewMachineBridge(agent, hub, "sess-1", false)
	bridge.Close()

	// After close, events should not produce output
	hub.Publish(telemetry.Event{
		Type:      telemetry.EventMachineSpawned,
		Timestamp: time.Now(),
		Data:      map[string]any{"agent_id": "a1", "modality": "classic"},
	})

	time.Sleep(200 * time.Millisecond)

	if buf.String() != "" {
		t.Errorf("expected no output after close, got %q", buf.String())
	}
}

func TestMachineBridge_GatesToolPayloadsByDefault(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	agent := NewAgent("test", "0.1", AgentHandlers{})
	bridge := NewMachineBridge(agent, hub, "sess-1", false)
	defer bridge.Close()

	evt := telemetry.Event{
		Type: telemetry.EventToolCompleted,
		Data: map[string]any{
			"toolName":  "read_file",
			"arguments": `{"path":".env"}`,
			"result":    "SECRET=abc123",
		},
	}
	stripped := bridge.prepareEvent(evt)
	if _, ok := stripped.Data["arguments"]; ok {
		t.Fatalf("expected arguments stripped by default: %+v", stripped.Data)
	}
	if _, ok := stripped.Data["result"]; ok {
		t.Fatalf("expected result stripped by default: %+v", stripped.Data)
	}
	if stripped.Data["toolName"] != "read_file" {
		t.Fatalf("expected non-payload fields preserved: %+v", stripped.Data)
	}
}

func TestMachineBridge_PassesToolPayloadsWhenAllowed(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	agent := NewAgent("test", "0.1", AgentHandlers{})
	bridge := NewMachineBridge(agent, hub, "sess-1", true)
	defer bridge.Close()

	evt := telemetry.Event{
		Type: telemetry.EventToolCompleted,
		Data: map[string]any{
			"arguments": `{"path":".env"}`,
			"result":    "SECRET=abc123",
		},
	}
	passed := bridge.prepareEvent(evt)
	if passed.Data["arguments"] != `{"path":".env"}` || passed.Data["result"] != "SECRET=abc123" {
		t.Fatalf("expected payload fields preserved when allowed: %+v", passed.Data)
	}
}

func TestDataStr(t *testing.T) {
	data := map[string]any{
		"key1": "value1",
		"key2": 42,
	}

	if got := dataStr(data, "key1"); got != "value1" {
		t.Errorf("dataStr(key1) = %q", got)
	}
	if got := dataStr(data, "key2"); got != "" {
		t.Errorf("dataStr(key2) = %q, want empty for non-string", got)
	}
	if got := dataStr(data, "missing"); got != "" {
		t.Errorf("dataStr(missing) = %q", got)
	}
	if got := dataStr(nil, "key1"); got != "" {
		t.Errorf("dataStr(nil map) = %q", got)
	}
}
