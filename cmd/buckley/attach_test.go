package main

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	ipcpb "m31labs.dev/buckley/v2/pkg/ipc/proto"
)

func TestNormalizeAttachAddress(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", defaultAttachAddress},
		{"127.0.0.1:4488", "http://127.0.0.1:4488"},
		{"localhost:4488", "http://localhost:4488"},
		{"http://127.0.0.1:4488", "http://127.0.0.1:4488"},
		{"https://buckley.example.com", "https://buckley.example.com"},
		{"  127.0.0.1:4488  ", "http://127.0.0.1:4488"},
	}
	for _, tt := range tests {
		if got := normalizeAttachAddress(tt.in); got != tt.want {
			t.Errorf("normalizeAttachAddress(%q)=%q want %q", tt.in, got, tt.want)
		}
	}
}

func TestRenderSessionListEmpty(t *testing.T) {
	got := renderSessionList(nil)
	if got != "No active sessions\n" {
		t.Fatalf("renderSessionList(nil)=%q want %q", got, "No active sessions\n")
	}
}

func TestRenderSessionListIncludesEverySession(t *testing.T) {
	sessions := []*ipcpb.SessionSummary{
		{Id: "s1", Status: "active", GitBranch: "main"},
		{Id: "s2", Status: "idle", GitBranch: "feature/x"},
	}
	got := renderSessionList(sessions)
	for _, want := range []string{"s1", "active", "main", "s2", "idle", "feature/x"} {
		if !strings.Contains(got, want) {
			t.Errorf("renderSessionList output missing %q; got:\n%s", want, got)
		}
	}
}

func TestRenderSessionHeaderIncludesTranscript(t *testing.T) {
	detail := &ipcpb.SessionDetail{
		Session: &ipcpb.SessionSummary{Id: "s1", Status: "active", GitBranch: "main"},
		RecentMessages: []*ipcpb.Message{
			{Role: "user", Content: "hello there"},
			{Role: "assistant", Content: "hi"},
		},
	}
	got := renderSessionHeader(detail)
	for _, want := range []string{"s1", "active", "main", "USER", "hello there", "ASSISTANT", "hi"} {
		if !strings.Contains(got, want) {
			t.Errorf("renderSessionHeader output missing %q; got:\n%s", want, got)
		}
	}
}

func TestRenderSessionHeaderNilSafe(t *testing.T) {
	if got := renderSessionHeader(nil); got != "" {
		t.Fatalf("renderSessionHeader(nil)=%q want empty", got)
	}
	if got := renderSessionHeader(&ipcpb.SessionDetail{}); got != "" {
		t.Fatalf("renderSessionHeader(empty)=%q want empty", got)
	}
}

func mustStruct(t *testing.T, m map[string]any) *structpb.Struct {
	t.Helper()
	s, err := structpb.NewStruct(m)
	if err != nil {
		t.Fatalf("structpb.NewStruct: %v", err)
	}
	return s
}

func TestFormatAttachEventSkipsControlEvents(t *testing.T) {
	for _, eventType := range []string{"server.hello", "server.keepalive", "server.backpressure", "sessions.snapshot", "view.patch"} {
		evt := &ipcpb.Event{Type: eventType}
		if got := formatAttachEvent(evt); got != "" {
			t.Errorf("formatAttachEvent(%q)=%q want empty (control event)", eventType, got)
		}
	}
}

func TestFormatAttachEventNilSafe(t *testing.T) {
	if got := formatAttachEvent(nil); got != "" {
		t.Fatalf("formatAttachEvent(nil)=%q want empty", got)
	}
}

func TestFormatAttachEventMessageCreated(t *testing.T) {
	evt := &ipcpb.Event{
		Type:    "message.created",
		Payload: mustStruct(t, map[string]any{"role": "assistant", "content": "  working on it  "}),
	}
	got := formatAttachEvent(evt)
	if !strings.Contains(got, "ASSISTANT") || !strings.Contains(got, "working on it") {
		t.Fatalf("formatAttachEvent(message.created)=%q missing role/content", got)
	}
	if strings.Contains(got, "  working on it  ") {
		t.Fatalf("formatAttachEvent(message.created)=%q did not trim content", got)
	}
}

func TestFormatAttachEventToolLifecycle(t *testing.T) {
	started := &ipcpb.Event{
		Type:    "tool.started",
		Payload: mustStruct(t, map[string]any{"name": "bash"}),
	}
	if got := formatAttachEvent(started); !strings.Contains(got, "bash") || !strings.Contains(got, "started") {
		t.Fatalf("formatAttachEvent(tool.started)=%q missing tool name/started", got)
	}

	completed := &ipcpb.Event{
		Type:    "tool.completed",
		Payload: mustStruct(t, map[string]any{"name": "bash"}),
	}
	if got := formatAttachEvent(completed); !strings.Contains(got, "bash") || !strings.Contains(got, "completed") {
		t.Fatalf("formatAttachEvent(tool.completed)=%q missing tool name/completed", got)
	}
}

func TestFormatAttachEventApprovalAndState(t *testing.T) {
	approval := &ipcpb.Event{
		Type:    "approval.required",
		Payload: mustStruct(t, map[string]any{"toolName": "write"}),
	}
	if got := formatAttachEvent(approval); !strings.Contains(got, "approval required") || !strings.Contains(got, "write") {
		t.Fatalf("formatAttachEvent(approval.required)=%q missing content", got)
	}

	state := &ipcpb.Event{
		Type:    "state.changed",
		Payload: mustStruct(t, map[string]any{"state": "idle"}),
	}
	if got := formatAttachEvent(state); !strings.Contains(got, "idle") {
		t.Fatalf("formatAttachEvent(state.changed)=%q missing state", got)
	}
}

func TestFormatAttachEventUnknownTypeFallsBackToTypeName(t *testing.T) {
	evt := &ipcpb.Event{Type: "command.queued"}
	got := formatAttachEvent(evt)
	if !strings.Contains(got, "command.queued") {
		t.Fatalf("formatAttachEvent(command.queued)=%q want fallback to type name", got)
	}
}

func TestFormatAttachEventIncludesTimestampPrefix(t *testing.T) {
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	evt := &ipcpb.Event{
		Type:      "command.queued",
		Timestamp: timestamppb.New(ts),
	}
	got := formatAttachEvent(evt)
	if !strings.HasPrefix(got, "[") {
		t.Fatalf("formatAttachEvent with timestamp=%q want a leading [HH:MM:SS] prefix", got)
	}
}

func TestFormatAttachEventTelemetryReusesRemoteHelper(t *testing.T) {
	evt := &ipcpb.Event{
		Type: "telemetry.task.completed",
		Payload: mustStruct(t, map[string]any{
			"type": "task.completed",
			"data": map[string]any{"taskId": "task-42"},
		}),
	}
	got := formatAttachEvent(evt)
	want := formatTelemetryDetail("task.completed", map[string]any{"taskId": "task-42"})
	if want == "" {
		t.Fatalf("expected formatTelemetryDetail to produce a message for task.completed")
	}
	if !strings.Contains(got, want) {
		t.Fatalf("formatAttachEvent(telemetry)=%q want to contain formatTelemetryDetail output %q", got, want)
	}
}

func TestParseAttachFlagsSessionIDPositional(t *testing.T) {
	t.Setenv("BUCKLEY_IPC_TOKEN", "")
	opts, err := parseAttachFlags([]string{"--addr", "127.0.0.1:9999", "abc-123"})
	if err != nil {
		t.Fatalf("parseAttachFlags: %v", err)
	}
	if opts.SessionID != "abc-123" {
		t.Fatalf("opts.SessionID=%q want %q", opts.SessionID, "abc-123")
	}
	if opts.Addr != "http://127.0.0.1:9999" {
		t.Fatalf("opts.Addr=%q want normalized loopback URL", opts.Addr)
	}
}

func TestParseAttachFlagsNoSessionID(t *testing.T) {
	opts, err := parseAttachFlags([]string{"--addr", "127.0.0.1:9999"})
	if err != nil {
		t.Fatalf("parseAttachFlags: %v", err)
	}
	if opts.SessionID != "" {
		t.Fatalf("opts.SessionID=%q want empty (list mode)", opts.SessionID)
	}
}

func TestParseAttachFlagsTokenFromEnv(t *testing.T) {
	t.Setenv("BUCKLEY_IPC_TOKEN", "env-token")
	opts, err := parseAttachFlags([]string{"--addr", "127.0.0.1:9999"})
	if err != nil {
		t.Fatalf("parseAttachFlags: %v", err)
	}
	if opts.Token != "env-token" {
		t.Fatalf("opts.Token=%q want %q", opts.Token, "env-token")
	}
}

func TestParseAttachFlagsTokenFlagOverridesEnv(t *testing.T) {
	t.Setenv("BUCKLEY_IPC_TOKEN", "env-token")
	opts, err := parseAttachFlags([]string{"--addr", "127.0.0.1:9999", "--token", "flag-token"})
	if err != nil {
		t.Fatalf("parseAttachFlags: %v", err)
	}
	if opts.Token != "flag-token" {
		t.Fatalf("opts.Token=%q want %q", opts.Token, "flag-token")
	}
}
