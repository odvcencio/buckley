package external

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/telemetry"
)

func manifestWithHooks(name string, events []string, preTool *PreToolHooksConfig) *ToolManifest {
	return &ToolManifest{
		Name:        name,
		Description: "test hook plugin",
		Executable:  hookPluginBin,
		Hooks: &HooksConfig{
			Events:  events,
			PreTool: preTool,
		},
	}
}

func waitForLogLine(t *testing.T, path, substr string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			last = string(data)
			if strings.Contains(last, substr) {
				return last
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q in %s; last contents: %s", substr, path, last)
	return ""
}

func TestHookRunner_EventFiltering_OnlyMatchingPluginsReceive(t *testing.T) {
	dir := t.TempDir()
	matchLog := filepath.Join(dir, "match.log")
	skipLog := filepath.Join(dir, "skip.log")

	runner := NewHookRunner(nil)
	matchManifest := manifestWithHooks("matcher", []string{"tool.*"}, nil)
	skipManifest := manifestWithHooks("skipper", []string{"plan.*"}, nil)

	if err := runner.Register(matchManifest, hookPluginBin, "", map[string]string{"HOOKPLUGIN_LOG_FILE": matchLog}); err != nil {
		t.Fatalf("Register(matcher) failed: %v", err)
	}
	if err := runner.Register(skipManifest, hookPluginBin, "", map[string]string{"HOOKPLUGIN_LOG_FILE": skipLog}); err != nil {
		t.Fatalf("Register(skipper) failed: %v", err)
	}
	t.Cleanup(runner.Close)

	hub := telemetry.NewHub()
	t.Cleanup(hub.Close)
	runner.Subscribe(hub)

	hub.Publish(telemetry.Event{Type: telemetry.EventToolCompleted, SessionID: "sess-1"})

	waitForLogLine(t, matchLog, string(telemetry.EventToolCompleted), 2*time.Second)

	// The non-matching plugin should never receive it. Give the forwarder
	// a moment to have processed the event (it already has, since
	// matchLog's write happened after the same Publish call fanned out to
	// both subscribers), then assert skipLog stayed empty.
	time.Sleep(100 * time.Millisecond)
	data, err := os.ReadFile(skipLog)
	if err == nil && strings.TrimSpace(string(data)) != "" {
		t.Errorf("expected no events logged for a non-matching plugin, got: %s", data)
	}
}

func TestHookRunner_EventDelivery_IsSanitized_NeverRaw(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.log")

	runner := NewHookRunner(nil)
	manifest := manifestWithHooks("logger", []string{"*"}, nil)
	if err := runner.Register(manifest, hookPluginBin, "", map[string]string{"HOOKPLUGIN_LOG_FILE": logPath}); err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	t.Cleanup(runner.Close)

	hub := telemetry.NewHub()
	t.Cleanup(hub.Close)
	runner.Subscribe(hub)

	// Simulate a producer that publishes directly to the hub without
	// going through pkg/tool's own arguments/result sanitizing helpers
	// (many event producers do exactly this) -- the hook runner must
	// still redact it before the plugin ever sees the line.
	hub.Publish(telemetry.Event{
		Type:      telemetry.EventToolStarted,
		SessionID: "sess-1",
		Data: map[string]any{
			"password": "hunter2-super-secret",
			"note":     "this is not sensitive",
		},
	})

	raw := waitForLogLine(t, logPath, string(telemetry.EventToolStarted), 2*time.Second)

	if strings.Contains(raw, "hunter2-super-secret") {
		t.Fatalf("plugin received the raw secret value; sanitization did not run: %s", raw)
	}
	if !strings.Contains(raw, "[REDACTED]") {
		t.Errorf("expected the password field to be redacted, got: %s", raw)
	}
	if !strings.Contains(raw, "this is not sensitive") {
		t.Errorf("expected the non-sensitive field to survive sanitization, got: %s", raw)
	}

	// Parse the last logged event line and check the field directly,
	// not just via substring matching.
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	var payload map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &payload); err != nil {
		t.Fatalf("plugin log line is not valid JSON: %v", err)
	}
	event, _ := payload["event"].(map[string]any)
	data, _ := event["data"].(map[string]any)
	if data["password"] != "[REDACTED]" {
		t.Errorf("expected data.password == [REDACTED], got %v", data["password"])
	}
}

func TestHookRunner_Veto_Allow(t *testing.T) {
	runner := NewHookRunner(nil)
	manifest := manifestWithHooks("gate", nil, &PreToolHooksConfig{Tools: []string{"marker_tool"}, TimeoutMs: 1000})
	if err := runner.Register(manifest, hookPluginBin, "", nil); err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	t.Cleanup(runner.Close)

	denied, plugin, reason := runner.Veto(context.Background(), "read_file", map[string]any{})
	if denied {
		t.Fatalf("expected allow for a non-matching tool, got denied by %q: %s", plugin, reason)
	}
}

func TestHookRunner_Veto_Deny(t *testing.T) {
	runner := NewHookRunner(nil)
	manifest := manifestWithHooks("gate", nil, &PreToolHooksConfig{Tools: []string{"marker_tool"}, TimeoutMs: 1000})
	if err := runner.Register(manifest, hookPluginBin, "", map[string]string{"HOOKPLUGIN_DENY_TOOL": "marker_tool"}); err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	t.Cleanup(runner.Close)

	denied, plugin, reason := runner.Veto(context.Background(), "marker_tool", map[string]any{})
	if !denied {
		t.Fatal("expected deny for marker_tool")
	}
	if plugin != "gate" {
		t.Errorf("expected plugin name 'gate', got %q", plugin)
	}
	if reason == "" {
		t.Error("expected a non-empty reason")
	}
}

func TestHookRunner_Veto_Timeout_Advisory_Allows(t *testing.T) {
	runner := NewHookRunner(nil)
	manifest := manifestWithHooks("gate", nil, &PreToolHooksConfig{
		Tools:     []string{"marker_tool"},
		TimeoutMs: 150,
		Enforcing: false,
	})
	if err := runner.Register(manifest, hookPluginBin, "", map[string]string{"HOOKPLUGIN_MODE": "silent"}); err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	t.Cleanup(runner.Close)

	denied, _, _ := runner.Veto(context.Background(), "marker_tool", map[string]any{})
	if denied {
		t.Fatal("expected advisory (non-enforcing) timeout to allow the call")
	}
}

func TestHookRunner_Veto_PluginCrash_Advisory_AllowsAndLogsWarning(t *testing.T) {
	var mu sync.Mutex
	var warnings []string
	runner := NewHookRunner(func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		warnings = append(warnings, fmt.Sprintf(format, args...))
	})
	manifest := manifestWithHooks("gate", nil, &PreToolHooksConfig{
		Tools:     []string{"marker_tool"},
		TimeoutMs: 2000,
		Enforcing: false,
	})
	if err := runner.Register(manifest, hookPluginBin, "", map[string]string{"HOOKPLUGIN_CRASH": "immediate"}); err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	t.Cleanup(runner.Close)

	denied, plugin, reason := runner.Veto(context.Background(), "marker_tool", map[string]any{})
	if denied {
		t.Fatalf("expected a crashed advisory plugin to allow the call, got denied by %q: %s", plugin, reason)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(warnings)
		mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(warnings) == 0 {
		t.Fatal("expected a warning to be logged for the crashed plugin even though the call was allowed")
	}
}

func TestHookRunner_Veto_Timeout_Enforcing_Denies(t *testing.T) {
	var warnings []string
	runner := NewHookRunner(func(format string, args ...any) {
		warnings = append(warnings, format)
	})
	manifest := manifestWithHooks("gate", nil, &PreToolHooksConfig{
		Tools:     []string{"marker_tool"},
		TimeoutMs: 150,
		Enforcing: true,
	})
	if err := runner.Register(manifest, hookPluginBin, "", map[string]string{"HOOKPLUGIN_MODE": "silent"}); err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	t.Cleanup(runner.Close)

	denied, plugin, reason := runner.Veto(context.Background(), "marker_tool", map[string]any{})
	if !denied {
		t.Fatal("expected an enforcing timeout to deny the call")
	}
	if plugin != "gate" {
		t.Errorf("expected plugin name 'gate', got %q", plugin)
	}
	if reason == "" {
		t.Error("expected a non-empty reason")
	}
	if len(warnings) == 0 {
		t.Error("expected a warning to be logged even though enforcing denied the call")
	}
}

func TestHookRunner_Veto_NonMatchingTool_SkipsPlugin(t *testing.T) {
	runner := NewHookRunner(nil)
	manifest := manifestWithHooks("gate", nil, &PreToolHooksConfig{Tools: []string{"write_*"}, TimeoutMs: 1000})
	if err := runner.Register(manifest, hookPluginBin, "", map[string]string{"HOOKPLUGIN_DENY_TOOL": "write_file"}); err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	t.Cleanup(runner.Close)

	denied, _, _ := runner.Veto(context.Background(), "read_file", map[string]any{})
	if denied {
		t.Fatal("expected a tool that doesn't match pre_tool.tools to never be sent to the plugin")
	}
}

func TestHookRunner_Register_NoHooks_NoProcessSpawned(t *testing.T) {
	runner := NewHookRunner(nil)
	manifest := &ToolManifest{Name: "plain", Description: "no hooks", Executable: hookPluginBin}
	if err := runner.Register(manifest, hookPluginBin, "", nil); err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	t.Cleanup(runner.Close)

	if len(runner.snapshotPlugins()) != 0 {
		t.Error("expected no hook process to be registered for a manifest without a hooks section")
	}
}

func TestHookRunner_Close_ShutsDownAllPlugins(t *testing.T) {
	runner := NewHookRunner(nil)
	manifest := manifestWithHooks("gate", []string{"*"}, nil)
	if err := runner.Register(manifest, hookPluginBin, "", nil); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	hub := telemetry.NewHub()
	t.Cleanup(hub.Close)
	runner.Subscribe(hub)

	runner.Close() // Should return without hanging.
}
