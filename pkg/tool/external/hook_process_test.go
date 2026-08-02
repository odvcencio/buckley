package external

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/telemetry"
)

// warnCollector is a thread-safe sink for HookProcess/HookRunner warning
// callbacks, used to assert that crashes and malformed responses get
// logged.
type warnCollector struct {
	mu       sync.Mutex
	messages []string
}

func (w *warnCollector) record(format string, args ...any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.messages = append(w.messages, fmt.Sprintf(format, args...))
}

func (w *warnCollector) snapshot() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]string, len(w.messages))
	copy(out, w.messages)
	return out
}

func newProcess(t *testing.T, env map[string]string) (*HookProcess, *warnCollector) {
	t.Helper()
	warn := &warnCollector{}
	proc := NewHookProcess("hookplugin", hookPluginBin, "", env)
	proc.SetWarnFunc(warn.record)
	if err := proc.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() { proc.Close() })
	return proc, warn
}

func TestHookProcess_SendEvent_AndClose_CleanShutdown(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "events.log")
	proc, _ := newProcess(t, map[string]string{
		"HOOKPLUGIN_MODE":     "normal",
		"HOOKPLUGIN_LOG_FILE": logFile,
	})

	event := telemetry.Event{Type: telemetry.EventToolCompleted, SessionID: "sess-1"}
	if err := proc.SendEvent(event); err != nil {
		t.Fatalf("SendEvent failed: %v", err)
	}

	// Give the plugin a moment to flush the event to its log file, then
	// close: a well-behaved plugin should exit cleanly (no crash warning)
	// once stdin hits EOF.
	deadline := time.Now().Add(2 * time.Second)
	for {
		data, err := os.ReadFile(logFile)
		if err == nil && strings.Contains(string(data), string(telemetry.EventToolCompleted)) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("plugin did not log the event in time; last read err: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := proc.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

func TestHookProcess_RequestVeto_Allow(t *testing.T) {
	proc, _ := newProcess(t, map[string]string{"HOOKPLUGIN_MODE": "normal"})

	decision, err := proc.RequestVeto(context.Background(), "read_file", map[string]any{"path": "a.go"}, time.Second)
	if err != nil {
		t.Fatalf("RequestVeto failed: %v", err)
	}
	if decision.Denied {
		t.Errorf("expected allow, got deny (reason=%q)", decision.Reason)
	}
}

func TestHookProcess_RequestVeto_Deny(t *testing.T) {
	proc, _ := newProcess(t, map[string]string{
		"HOOKPLUGIN_MODE":      "normal",
		"HOOKPLUGIN_DENY_TOOL": "marker_tool",
	})

	decision, err := proc.RequestVeto(context.Background(), "marker_tool", nil, time.Second)
	if err != nil {
		t.Fatalf("RequestVeto failed: %v", err)
	}
	if !decision.Denied {
		t.Fatal("expected deny")
	}
	if decision.Reason == "" {
		t.Error("expected a non-empty deny reason")
	}
}

func TestHookProcess_RequestVeto_Timeout(t *testing.T) {
	proc, _ := newProcess(t, map[string]string{"HOOKPLUGIN_MODE": "silent"})

	start := time.Now()
	_, err := proc.RequestVeto(context.Background(), "marker_tool", nil, 200*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("expected a timeout error, got: %v", err)
	}
	if elapsed < 200*time.Millisecond {
		t.Errorf("returned before the configured timeout elapsed: %v", elapsed)
	}
}

func TestHookProcess_RequestVeto_MalformedDecision(t *testing.T) {
	proc, _ := newProcess(t, map[string]string{"HOOKPLUGIN_MODE": "malformed"})

	_, err := proc.RequestVeto(context.Background(), "marker_tool", nil, time.Second)
	if err == nil {
		t.Fatal("expected an error for a malformed decision value")
	}
	if !strings.Contains(err.Error(), "malformed") {
		t.Errorf("expected a malformed-decision error, got: %v", err)
	}
}

func TestHookProcess_RequestVeto_MalformedJSON_Ignored(t *testing.T) {
	proc, _ := newProcess(t, map[string]string{"HOOKPLUGIN_MODE": "malformed_json"})

	_, err := proc.RequestVeto(context.Background(), "marker_tool", nil, 300*time.Millisecond)
	if err == nil {
		t.Fatal("expected a timeout error since the plugin's stdout line isn't valid JSON")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("expected a timeout error (garbage output ignored), got: %v", err)
	}
}

func TestHookProcess_CrashImmediately_AdvisoryError(t *testing.T) {
	warn := &warnCollector{}
	proc := NewHookProcess("hookplugin", hookPluginBin, "", map[string]string{"HOOKPLUGIN_CRASH": "immediate"})
	proc.SetWarnFunc(warn.record)
	if err := proc.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() { proc.Close() })

	_, err := proc.RequestVeto(context.Background(), "marker_tool", nil, 2*time.Second)
	if err == nil {
		t.Fatal("expected an error from a crashed hook process")
	}

	deadline := time.Now().Add(2 * time.Second)
	for len(warn.snapshot()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(warn.snapshot()) == 0 {
		t.Error("expected a warning to be logged for the crashed plugin")
	}
}

func TestHookProcess_CrashMidSession_PendingRequestFails(t *testing.T) {
	proc, warn := newProcess(t, map[string]string{
		"HOOKPLUGIN_CRASH":       "after",
		"HOOKPLUGIN_CRASH_AFTER": "1",
	})

	_, err := proc.RequestVeto(context.Background(), "marker_tool", nil, 2*time.Second)
	if err == nil {
		t.Fatal("expected an error once the process crashes mid-request")
	}

	deadline := time.Now().Add(2 * time.Second)
	for len(warn.snapshot()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(warn.snapshot()) == 0 {
		t.Error("expected a warning to be logged for the mid-session crash")
	}
}

func TestHookProcess_Close_NoWarning(t *testing.T) {
	proc, warn := newProcess(t, map[string]string{"HOOKPLUGIN_MODE": "normal"})
	if err := proc.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if msgs := warn.snapshot(); len(msgs) != 0 {
		t.Errorf("expected no crash warning for a clean shutdown, got: %v", msgs)
	}
}

func TestHookProcess_Close_Idempotent(t *testing.T) {
	proc, _ := newProcess(t, map[string]string{"HOOKPLUGIN_MODE": "normal"})
	if err := proc.Close(); err != nil {
		t.Fatalf("first Close failed: %v", err)
	}
	if err := proc.Close(); err != nil {
		t.Fatalf("second Close failed: %v", err)
	}
}

func TestHookProcess_Close_NeverStarted(t *testing.T) {
	proc := NewHookProcess("hookplugin", hookPluginBin, "", nil)
	if err := proc.Close(); err != nil {
		t.Fatalf("Close on an unstarted process should be a no-op, got: %v", err)
	}
}
