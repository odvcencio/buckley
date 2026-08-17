package tool

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/telemetry"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

type telemetryTool struct{}

type panicTelemetryJSON struct{}

func (panicTelemetryJSON) MarshalJSON() ([]byte, error) {
	panic("telemetry preparation panic")
}

func (telemetryTool) Name() string {
	return "telemetry_tool"
}

func (telemetryTool) Description() string {
	return "telemetry tool"
}

func (telemetryTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{Type: "object"}
}

func (telemetryTool) Execute(params map[string]any) (*builtin.Result, error) {
	return &builtin.Result{Success: true}, nil
}

func TestTelemetryMiddlewarePublishesToolEvents(t *testing.T) {
	hub := telemetry.NewHub()
	eventCh, unsubscribe := hub.Subscribe()
	t.Cleanup(unsubscribe)

	r := NewEmptyRegistry()
	r.Register(telemetryTool{})
	r.EnableTelemetry(hub, "session-1")

	if _, err := r.Execute("telemetry_tool", map[string]any{"param": "value"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := map[telemetry.EventType]bool{
		telemetry.EventToolStarted:   true,
		telemetry.EventToolCompleted: true,
	}
	got := map[telemetry.EventType]bool{}

	deadline := time.After(1 * time.Second)
	for len(got) < len(want) {
		select {
		case event := <-eventCh:
			if want[event.Type] {
				got[event.Type] = true
			}
		case <-deadline:
			t.Fatalf("timed out waiting for telemetry events: got %#v", got)
		}
	}
}

func TestTelemetryMiddlewarePublishesShellEvents(t *testing.T) {
	hub := telemetry.NewHub()
	eventCh, unsubscribe := hub.Subscribe()
	t.Cleanup(unsubscribe)

	r := NewEmptyRegistry()
	r.Register(&builtin.ShellCommandTool{})
	r.EnableTelemetry(hub, "session-2")

	if _, err := r.Execute("run_shell", map[string]any{
		"command":         "echo telemetry",
		"timeout_seconds": 5,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := map[telemetry.EventType]bool{
		telemetry.EventShellCommandStarted:   true,
		telemetry.EventShellCommandCompleted: true,
	}
	got := map[telemetry.EventType]bool{}

	deadline := time.After(2 * time.Second)
	for len(got) < len(want) {
		select {
		case event := <-eventCh:
			if want[event.Type] {
				got[event.Type] = true
			}
		case <-deadline:
			t.Fatalf("timed out waiting for shell telemetry events: got %#v", got)
		}
	}
}

func TestShellTelemetryProjectsNoteAsOpaqueSummary(t *testing.T) {
	hub := telemetry.NewHub()
	eventCh, unsubscribe := hub.Subscribe()
	t.Cleanup(unsubscribe)

	r := NewEmptyRegistry()
	r.EnableTelemetry(hub, "session-shell-note")
	rawNote := "password=top-secret " + string([]byte{0xff}) + strings.Repeat("sensitive detail ", 2_000)
	result, err := r.executeWithShellTelemetry(func(map[string]any) (*builtin.Result, error) {
		return &builtin.Result{
			Success:     true,
			DisplayData: map[string]any{"message": rawNote},
		}, nil
	}, map[string]any{"command": "echo safe"})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("shell execution changed by telemetry: result=%#v err=%v", result, err)
	}

	for _, event := range drainTelemetryEvents(eventCh) {
		if event.Type != telemetry.EventShellCommandCompleted {
			continue
		}
		note, ok := event.Data["note"].(string)
		if !ok {
			t.Fatalf("shell note projection missing: %#v", event.Data)
		}
		if note != telemetry.OpaqueTextSummary(rawNote, "note") {
			t.Fatalf("shell note = %q, want opaque summary", note)
		}
		if strings.Contains(note, "top-secret") || strings.Contains(note, "sensitive detail") {
			t.Fatalf("shell note leaked arbitrary content: %q", note)
		}
		if !utf8.ValidString(note) || len(note) > 128 {
			t.Fatalf("shell note is not bounded valid UTF-8: len=%d value=%q", len(note), note)
		}
		wantBytes := len(telemetry.RedactCredentials(telemetry.NormalizeText(rawNote)))
		if event.Data["note_bytes"] != wantBytes {
			t.Fatalf("note_bytes = %#v, want %d", event.Data["note_bytes"], wantBytes)
		}
		if event.Data["note_fingerprint"] != telemetry.FingerprintText(rawNote) {
			t.Fatalf("note_fingerprint = %#v, want deterministic projection", event.Data["note_fingerprint"])
		}
		return
	}
	t.Fatal("shell completion telemetry event missing")
}

func TestTelemetryMiddlewareCompletionIncludesDownstreamMetadata(t *testing.T) {
	hub := telemetry.NewHub()
	eventCh, unsubscribe := hub.Subscribe()
	t.Cleanup(unsubscribe)

	r := NewEmptyRegistry()
	r.EnableTelemetry(hub, "session-completion-metadata")
	exec := r.telemetryMiddleware()(func(ctx *ExecutionContext) (*builtin.Result, error) {
		ctx.Metadata["panic_stack"] = `{"api_key":"stack-secret","frame":"downstream stack"}`
		ctx.Metadata["panic_value"] = "downstream panic"
		ctx.Metadata["result_truncated"] = true
		return &builtin.Result{
			Success: true,
			Data:    map[string]any{"truncated": true},
		}, nil
	})

	if _, err := exec(&ExecutionContext{
		ToolName: "telemetry_tool",
		CallID:   "call-completion-metadata",
		Params:   map[string]any{"param": "value"},
		Metadata: map[string]any{},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	deadline := time.After(time.Second)
	for {
		select {
		case event := <-eventCh:
			if event.Type != telemetry.EventToolCompleted {
				continue
			}
			stack, _ := event.Data["panic_stack"].(string)
			if strings.Contains(stack, "stack-secret") || !strings.Contains(stack, "downstream stack") || !strings.Contains(stack, "REDACTED") {
				t.Fatalf("completion event panic_stack was not sanitized: %q", stack)
			}
			if got := event.Data["panic_value"]; got != "[REDACTED: panic value]" {
				t.Fatalf("completion event panic_value = %v, want redaction marker", got)
			}
			if got := event.Data["panic_type"]; got != "string" {
				t.Fatalf("completion event panic_type = %v, want string", got)
			}
			if got := event.Data["result_truncated"]; got != true {
				t.Fatalf("completion event result_truncated = %v, want true", got)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for completion telemetry event")
		}
	}
}

func TestTelemetryMiddlewarePublishesFailureOnDownstreamPanic(t *testing.T) {
	hub := telemetry.NewHub()
	eventCh, unsubscribe := hub.Subscribe()
	t.Cleanup(unsubscribe)

	r := NewEmptyRegistry()
	r.EnableTelemetry(hub, "session-panic")
	exec := PanicRecovery()(r.telemetryMiddleware()(func(ctx *ExecutionContext) (*builtin.Result, error) {
		ctx.Params = map[string]any{
			"secret": "must-not-leak",
		}
		ctx.Metadata["result_truncated"] = true
		panic("boom")
	}))

	result, err := exec(&ExecutionContext{
		ToolName: "panic_tool",
		CallID:   "call-panic",
		Params:   map[string]any{"initial": "value"},
		Metadata: map[string]any{},
	})
	if err == nil {
		t.Fatal("expected outer panic recovery error")
	}
	if result == nil || result.Success {
		t.Fatalf("expected a recovered failure result, got %#v", result)
	}

	events := drainTelemetryEvents(eventCh)
	if len(events) != 2 || events[0].Type != telemetry.EventToolStarted || events[1].Type != telemetry.EventToolFailed {
		t.Fatalf("panic event sequence = %v, want [tool.started tool.failed]", telemetryEventTypes(events))
	}
	started, failed := events[0], events[1]

	if started.TaskID != "call-panic" || failed.TaskID != "call-panic" {
		t.Fatalf("unexpected event task IDs: started=%q failed=%q", started.TaskID, failed.TaskID)
	}
	if failed.Data["success"] != false {
		t.Fatalf("failed event did not include a failure result: %#v", failed.Data)
	}
	if failed.Data["result_truncated"] != true {
		t.Fatalf("failed event lost post-execution metadata: %#v", failed.Data)
	}
	arguments, _ := failed.Data["arguments"].(string)
	if strings.Contains(arguments, "must-not-leak") {
		t.Fatalf("failed event leaked post-execution secret: %s", arguments)
	}
	if !strings.Contains(arguments, "REDACTED") {
		t.Fatalf("failed event did not retain sanitized post-execution arguments: %s", arguments)
	}
}

func TestTelemetryMiddlewarePreparationPanicStillCompletesTool(t *testing.T) {
	hub := telemetry.NewHub()
	eventCh, unsubscribe := hub.Subscribe()
	t.Cleanup(unsubscribe)

	r := NewEmptyRegistry()
	r.EnableTelemetry(hub, "session-preparation-panic")
	exec := r.telemetryMiddleware()(func(ctx *ExecutionContext) (*builtin.Result, error) {
		return &builtin.Result{Success: true, Data: map[string]any{"bad": panicTelemetryJSON{}}}, nil
	})

	result, err := exec(&ExecutionContext{
		ToolName: "telemetry_tool",
		CallID:   "call-preparation-panic",
		Params:   map[string]any{"bad": panicTelemetryJSON{}},
		Metadata: map[string]any{},
	})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("telemetry preparation changed tool result: result=%#v err=%v", result, err)
	}
	events := drainTelemetryEvents(eventCh)
	if len(events) != 2 || events[0].Type != telemetry.EventToolStarted || events[1].Type != telemetry.EventToolCompleted {
		t.Fatalf("preparation panic event sequence = %v, want [tool.started tool.completed]", telemetryEventTypes(events))
	}
}

func TestTelemetryMiddlewarePublisherPanicDoesNotFailSuccessfulTool(t *testing.T) {
	r := NewEmptyRegistry()
	r.EnableTelemetry(telemetry.NewHub(), "session-publisher-panic")
	var attempts []telemetry.EventType
	r.telemetryPublish = func(event telemetry.Event) {
		attempts = append(attempts, event.Type)
		panic("publisher panic")
	}
	exec := r.telemetryMiddleware()(func(ctx *ExecutionContext) (*builtin.Result, error) {
		return &builtin.Result{Success: true}, nil
	})

	result, err := exec(&ExecutionContext{ToolName: "telemetry_tool", CallID: "call-publisher-panic", Metadata: map[string]any{}})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("publisher panic changed tool result: result=%#v err=%v", result, err)
	}
	if len(attempts) != 2 || attempts[0] != telemetry.EventToolStarted || attempts[1] != telemetry.EventToolCompleted {
		t.Fatalf("publisher attempts = %v, want [tool.started tool.completed]", attempts)
	}
}

func TestTelemetryMiddlewarePreservesDownstreamPanicWhenTerminalTelemetryPanics(t *testing.T) {
	r := NewEmptyRegistry()
	r.EnableTelemetry(telemetry.NewHub(), "session-original-panic")
	var attempts []telemetry.EventType
	r.telemetryPublish = func(event telemetry.Event) {
		attempts = append(attempts, event.Type)
		if event.Type == telemetry.EventToolFailed {
			panic("terminal publisher panic")
		}
	}
	original := &struct{ label string }{label: "original"}
	exec := r.telemetryMiddleware()(func(ctx *ExecutionContext) (*builtin.Result, error) {
		ctx.Params = map[string]any{"bad": panicTelemetryJSON{}}
		panic(original)
	})

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_, _ = exec(&ExecutionContext{ToolName: "panic_tool", CallID: "call-original-panic", Params: map[string]any{}, Metadata: map[string]any{}})
	}()
	if recovered != original {
		t.Fatalf("recovered panic = %#v, want original %#v", recovered, original)
	}
	if len(attempts) != 2 || attempts[0] != telemetry.EventToolStarted || attempts[1] != telemetry.EventToolFailed {
		t.Fatalf("panic publisher attempts = %v, want [tool.started tool.failed]", attempts)
	}
}

func TestShellTelemetryPublishesOneTerminalFailureOnDownstreamPanic(t *testing.T) {
	hub := telemetry.NewHub()
	eventCh, unsubscribe := hub.Subscribe()
	t.Cleanup(unsubscribe)

	r := NewEmptyRegistry()
	r.EnableTelemetry(hub, "session-shell-panic")
	original := &struct{ label string }{label: "shell panic"}
	exec := PanicRecovery()(r.telemetryMiddleware()(func(ctx *ExecutionContext) (*builtin.Result, error) {
		panic(original)
	}))

	result, err := exec(&ExecutionContext{
		ToolName: "run_shell",
		CallID:   "call-shell-panic",
		Params:   map[string]any{"command": "echo safe"},
		Metadata: map[string]any{},
	})
	if err == nil || result == nil || result.Success {
		t.Fatalf("expected outer recovery failure, result=%#v err=%v", result, err)
	}

	events := drainTelemetryEvents(eventCh)
	want := []telemetry.EventType{
		telemetry.EventToolStarted,
		telemetry.EventShellCommandStarted,
		telemetry.EventShellCommandFailed,
		telemetry.EventToolFailed,
	}
	if got := telemetryEventTypes(events); !telemetryEventTypesEqual(got, want) {
		t.Fatalf("shell panic event sequence = %v, want %v", got, want)
	}
}

func TestShellTelemetryPreservesDownstreamPanicWhenPublisherPanics(t *testing.T) {
	r := NewEmptyRegistry()
	r.EnableTelemetry(telemetry.NewHub(), "session-shell-publisher-panic")
	var attempts []telemetry.EventType
	r.telemetryPublish = func(event telemetry.Event) {
		attempts = append(attempts, event.Type)
		panic("publisher panic")
	}
	original := &struct{ label string }{label: "original shell panic"}

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_, _ = r.executeWithShellTelemetry(func(map[string]any) (*builtin.Result, error) {
			panic(original)
		}, map[string]any{"command": "echo safe"})
	}()

	if recovered != original {
		t.Fatalf("recovered panic = %#v, want original %#v", recovered, original)
	}
	want := []telemetry.EventType{
		telemetry.EventShellCommandStarted,
		telemetry.EventShellCommandFailed,
	}
	if !telemetryEventTypesEqual(attempts, want) {
		t.Fatalf("shell publisher attempts = %v, want %v", attempts, want)
	}
}

func drainTelemetryEvents(eventCh <-chan telemetry.Event) []telemetry.Event {
	var events []telemetry.Event
	for {
		select {
		case event := <-eventCh:
			events = append(events, event)
		default:
			return events
		}
	}
}

func telemetryEventTypes(events []telemetry.Event) []telemetry.EventType {
	types := make([]telemetry.EventType, len(events))
	for i := range events {
		types[i] = events[i].Type
	}
	return types
}

func telemetryEventTypesEqual(left, right []telemetry.EventType) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
