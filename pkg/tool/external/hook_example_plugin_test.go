package external

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/v2/pkg/telemetry"
)

// examplePluginDir resolves the repository's real working example plugin
// (plugins/go/hook_logger), used here -- instead of the synthetic
// testdata/hookplugin double -- to prove the full stack (manifest
// loading, DiscoverPlugins, HookRunner, and the hook_logger.sh -> `go
// run` -> main.go process) works end to end against a plugin that ships
// in the repository, not just a test fixture.
func examplePluginDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd failed: %v", err)
	}
	dir := filepath.Join(wd, "..", "..", "..", "plugins", "go", "hook_logger")
	if _, err := os.Stat(filepath.Join(dir, "tool.yaml")); err != nil {
		t.Skipf("example plugin not found at %s (repository layout changed?): %v", dir, err)
	}
	return dir
}

func TestExamplePlugin_HookLogger_DiscoverAndLoad(t *testing.T) {
	dir := examplePluginDir(t)

	tools, err := DiscoverPlugins(filepath.Join(dir, ".."))
	if err != nil {
		t.Fatalf("DiscoverPlugins failed: %v", err)
	}

	var found *ExternalTool
	for _, tool := range tools {
		if tool.Name() == "hook_logger" {
			found = tool
			break
		}
	}
	if found == nil {
		t.Fatal("expected DiscoverPlugins to find the hook_logger example plugin")
	}

	// The example plugin must still behave as an ordinary one-shot tool
	// (its normal, non-hook invocation), not just a hook subscriber.
	result, err := found.Execute(map[string]any{})
	skipIfInterpreterUnavailable(t, err)
	if result != nil && result.Error != "" && strings.Contains(result.Error, "no such file or directory") {
		t.Skipf("example plugin interpreter unavailable in this environment: %s", result.Error)
	}
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("expected the example plugin's normal tool mode to succeed, got %#v", result)
	}
}

func TestExamplePlugin_HookLogger_LogsEventsAndVetoesMarker(t *testing.T) {
	dir := examplePluginDir(t)
	manifest, err := LoadManifest(filepath.Join(dir, "tool.yaml"))
	if err != nil {
		t.Fatalf("LoadManifest failed: %v", err)
	}
	if !manifest.HasHooks() {
		t.Fatal("expected the example plugin's manifest to declare a hooks section")
	}

	logPath := filepath.Join(t.TempDir(), "hook_logger.log")
	executable := filepath.Join(dir, "hook_logger.sh")

	runner := NewHookRunner(nil)
	if regErr := runner.Register(manifest, executable, dir, map[string]string{
		"BUCKLEY_HOOK_LOGGER_LOG": logPath,
	}); regErr != nil {
		skipIfInterpreterUnavailable(t, regErr)
		t.Fatalf("Register failed: %v", regErr)
	}
	t.Cleanup(runner.Close)

	hub := telemetry.NewHub()
	t.Cleanup(hub.Close)
	runner.Subscribe(hub)

	// The example plugin's hooks.events pattern is "tool.*"; publish a
	// matching event carrying a field that looks like a secret, and a
	// non-matching event that must never reach the plugin.
	hub.Publish(telemetry.Event{
		Type: telemetry.EventToolCompleted,
		Data: map[string]any{"token": "leak-me-not", "toolName": "write_file"},
	})
	hub.Publish(telemetry.Event{Type: telemetry.EventPlanCreated, Data: map[string]any{"note": "should not be forwarded"}})

	deadline := time.Now().Add(5 * time.Second)
	var logged string
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(logPath); err == nil && strings.Contains(string(data), string(telemetry.EventToolCompleted)) {
			logged = string(data)
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if logged == "" {
		t.Fatal("timed out waiting for the example plugin to log the matching event")
	}
	if strings.Contains(logged, "leak-me-not") {
		t.Errorf("expected the token field to be redacted before reaching the plugin, got: %s", logged)
	}
	if strings.Contains(logged, string(telemetry.EventPlanCreated)) {
		t.Errorf("expected the non-matching plan.created event never to be forwarded, got: %s", logged)
	}

	// The example plugin vetoes calls to "hook_logger_marker".
	denied, plugin, reason := runner.Veto(context.Background(), "hook_logger_marker", map[string]any{})
	if !denied {
		t.Fatal("expected the example plugin to deny hook_logger_marker")
	}
	if plugin != "hook_logger" {
		t.Errorf("expected the denying plugin to be 'hook_logger', got %q", plugin)
	}
	if reason == "" {
		t.Error("expected a non-empty deny reason")
	}

	// It must allow anything else.
	denied, _, _ = runner.Veto(context.Background(), "read_file", map[string]any{})
	if denied {
		t.Error("expected the example plugin to allow tools other than its marker tool")
	}
}

// skipIfInterpreterUnavailable skips example-plugin tests in execution
// environments that cannot launch the example's shell wrapper (for
// example, minimal review sandboxes without /bin/bash). The hook
// protocol itself stays enforced by the Go testdata plugin tests.
func skipIfInterpreterUnavailable(t *testing.T, err error) {
	t.Helper()
	if err != nil && (errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "no such file or directory")) {
		t.Skipf("example plugin interpreter unavailable in this environment: %v", err)
	}
}
