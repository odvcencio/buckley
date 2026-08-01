package external

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadManifest_Success(t *testing.T) {
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "tool.yaml")

	content := `
name: test_tool
description: A test tool
parameters:
  type: object
  properties:
    param1:
      type: string
      description: First parameter
  required:
    - param1
executable: ./test.sh
timeout_ms: 30000
`
	if err := os.WriteFile(manifestPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	manifest, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadManifest failed: %v", err)
	}

	if manifest.Name != "test_tool" {
		t.Errorf("expected name 'test_tool', got %q", manifest.Name)
	}
	if manifest.Description != "A test tool" {
		t.Errorf("expected description 'A test tool', got %q", manifest.Description)
	}
	if manifest.Executable != "./test.sh" {
		t.Errorf("expected executable './test.sh', got %q", manifest.Executable)
	}
	if manifest.TimeoutMs != 30000 {
		t.Errorf("expected timeout 30000, got %d", manifest.TimeoutMs)
	}
}

func TestLoadManifest_FileNotFound(t *testing.T) {
	_, err := LoadManifest("/nonexistent/tool.yaml")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestLoadManifest_InvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "tool.yaml")

	content := `
name: test
invalid yaml: {{{
`
	if err := os.WriteFile(manifestPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadManifest(manifestPath)
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

func TestLoadManifest_ValidationFails(t *testing.T) {
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "tool.yaml")

	content := `
name: test_tool
# Missing description and executable
`
	if err := os.WriteFile(manifestPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadManifest(manifestPath)
	if err == nil {
		t.Error("expected validation error")
	}
}

func TestToolManifest_Validate_MissingName(t *testing.T) {
	manifest := &ToolManifest{
		Description: "test",
		Executable:  "./test.sh",
	}

	err := manifest.Validate()
	if err == nil {
		t.Error("expected error for missing name")
	}
}

func TestToolManifest_Validate_MissingDescription(t *testing.T) {
	manifest := &ToolManifest{
		Name:       "test",
		Executable: "./test.sh",
	}

	err := manifest.Validate()
	if err == nil {
		t.Error("expected error for missing description")
	}
}

func TestToolManifest_Validate_MissingExecutable(t *testing.T) {
	manifest := &ToolManifest{
		Name:        "test",
		Description: "test",
	}

	err := manifest.Validate()
	if err == nil {
		t.Error("expected error for missing executable")
	}
}

func TestToolManifest_Validate_DefaultTimeout(t *testing.T) {
	manifest := &ToolManifest{
		Name:        "test",
		Description: "test",
		Executable:  "./test.sh",
		TimeoutMs:   0,
	}

	err := manifest.Validate()
	if err != nil {
		t.Errorf("validation should succeed, got: %v", err)
	}

	if manifest.TimeoutMs != 120000 {
		t.Errorf("expected default timeout 120000, got %d", manifest.TimeoutMs)
	}
}

func TestToolManifest_Validate_NilParameters(t *testing.T) {
	manifest := &ToolManifest{
		Name:        "test",
		Description: "test",
		Executable:  "./test.sh",
		TimeoutMs:   30000,
		Parameters:  nil,
	}

	err := manifest.Validate()
	if err != nil {
		t.Errorf("validation should succeed, got: %v", err)
	}

	if manifest.Parameters == nil {
		t.Error("expected parameters to be initialized to empty map")
	}
}

func TestToolManifest_Validate_Success(t *testing.T) {
	manifest := &ToolManifest{
		Name:        "test",
		Description: "test",
		Executable:  "./test.sh",
		TimeoutMs:   30000,
		Parameters:  map[string]any{"type": "object"},
	}

	err := manifest.Validate()
	if err != nil {
		t.Errorf("validation should succeed, got: %v", err)
	}
}

// --- hooks: section -------------------------------------------------------

func TestLoadManifest_NoHooksSection_BackwardCompatible(t *testing.T) {
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "tool.yaml")
	content := `
name: legacy_tool
description: A tool with no hooks section
executable: ./legacy.sh
`
	if err := os.WriteFile(manifestPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	manifest, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadManifest failed: %v", err)
	}
	if manifest.Hooks != nil {
		t.Fatalf("expected nil Hooks for a manifest without a hooks section, got %+v", manifest.Hooks)
	}
	if manifest.HasHooks() {
		t.Error("expected HasHooks() to be false for a manifest without a hooks section")
	}
	if manifest.MatchesEvent("tool.completed") {
		t.Error("expected MatchesEvent to be false without a hooks section")
	}
	if manifest.MatchesPreToolTool("write_file") {
		t.Error("expected MatchesPreToolTool to be false without a hooks section")
	}
}

func TestLoadManifest_WithHooksSection(t *testing.T) {
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "tool.yaml")
	content := `
name: hook_tool
description: A tool with a hooks section
executable: ./hook_tool.sh
hooks:
  events:
    - "tool.*"
    - "task.completed"
  pre_tool:
    tools:
      - "write_file"
      - "run_shell"
    timeout_ms: 2500
    enforcing: true
`
	if err := os.WriteFile(manifestPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	manifest, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadManifest failed: %v", err)
	}
	if manifest.Hooks == nil {
		t.Fatal("expected non-nil Hooks")
	}
	if !manifest.HasHooks() {
		t.Error("expected HasHooks() to be true")
	}
	if len(manifest.Hooks.Events) != 2 {
		t.Errorf("expected 2 event patterns, got %d", len(manifest.Hooks.Events))
	}
	if !manifest.MatchesEvent("tool.completed") {
		t.Error("expected 'tool.*' to match 'tool.completed'")
	}
	if !manifest.MatchesEvent("task.completed") {
		t.Error("expected literal pattern 'task.completed' to match itself")
	}
	if manifest.MatchesEvent("plan.created") {
		t.Error("did not expect 'plan.created' to match any configured pattern")
	}
	if manifest.Hooks.PreTool == nil {
		t.Fatal("expected non-nil PreTool")
	}
	if !manifest.Hooks.HasPreTool() {
		t.Error("expected HasPreTool() to be true")
	}
	if !manifest.MatchesPreToolTool("write_file") {
		t.Error("expected 'write_file' to match pre_tool.tools")
	}
	if manifest.MatchesPreToolTool("read_file") {
		t.Error("did not expect 'read_file' to match pre_tool.tools")
	}
	if !manifest.Hooks.PreTool.Enforcing {
		t.Error("expected Enforcing to be true")
	}
	if got, want := manifest.Hooks.PreTool.TimeoutOrDefault(), 2500*time.Millisecond; got != want {
		t.Errorf("expected timeout %v, got %v", want, got)
	}
}

func TestLoadManifest_HooksSection_EventsOnly(t *testing.T) {
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "tool.yaml")
	content := `
name: event_only_tool
description: Subscribes to events but has no pre_tool veto
executable: ./tool.sh
hooks:
  events:
    - "*"
`
	if err := os.WriteFile(manifestPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	manifest, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadManifest failed: %v", err)
	}
	if !manifest.HasHooks() {
		t.Error("expected HasHooks() to be true for an events-only hooks section")
	}
	if manifest.Hooks.HasPreTool() {
		t.Error("expected HasPreTool() to be false when pre_tool is omitted")
	}
	if manifest.MatchesPreToolTool("anything") {
		t.Error("expected MatchesPreToolTool to be false without a pre_tool block")
	}
}

func TestPreToolHooksConfig_TimeoutOrDefault(t *testing.T) {
	var nilCfg *PreToolHooksConfig
	if got, want := nilCfg.TimeoutOrDefault(), DefaultPreToolTimeoutMs*time.Millisecond; got != want {
		t.Errorf("nil receiver: expected %v, got %v", want, got)
	}

	zero := &PreToolHooksConfig{}
	if got, want := zero.TimeoutOrDefault(), DefaultPreToolTimeoutMs*time.Millisecond; got != want {
		t.Errorf("zero timeout_ms: expected default %v, got %v", want, got)
	}

	explicit := &PreToolHooksConfig{TimeoutMs: 500}
	if got, want := explicit.TimeoutOrDefault(), 500*time.Millisecond; got != want {
		t.Errorf("expected explicit timeout %v, got %v", want, got)
	}
}

func TestToolManifest_Validate_HooksSection_InvalidGlob(t *testing.T) {
	manifest := &ToolManifest{
		Name:        "test",
		Description: "test",
		Executable:  "./test.sh",
		Hooks: &HooksConfig{
			Events: []string{"["},
		},
	}
	if err := manifest.Validate(); err == nil {
		t.Error("expected error for invalid glob pattern in events")
	}
}

func TestToolManifest_Validate_HooksSection_EmptyPattern(t *testing.T) {
	manifest := &ToolManifest{
		Name:        "test",
		Description: "test",
		Executable:  "./test.sh",
		Hooks: &HooksConfig{
			PreTool: &PreToolHooksConfig{Tools: []string{""}},
		},
	}
	if err := manifest.Validate(); err == nil {
		t.Error("expected error for empty pre_tool.tools pattern")
	}
}

func TestToolManifest_Validate_HooksSection_NegativeTimeout(t *testing.T) {
	manifest := &ToolManifest{
		Name:        "test",
		Description: "test",
		Executable:  "./test.sh",
		Hooks: &HooksConfig{
			PreTool: &PreToolHooksConfig{Tools: []string{"write_file"}, TimeoutMs: -1},
		},
	}
	if err := manifest.Validate(); err == nil {
		t.Error("expected error for negative pre_tool.timeout_ms")
	}
}

func TestToolManifest_Validate_HooksSection_EmptyIsValid(t *testing.T) {
	manifest := &ToolManifest{
		Name:        "test",
		Description: "test",
		Executable:  "./test.sh",
		Hooks:       &HooksConfig{},
	}
	if err := manifest.Validate(); err != nil {
		t.Errorf("expected an empty hooks section to be valid, got: %v", err)
	}
	if manifest.HasHooks() {
		t.Error("expected an empty hooks section to report HasHooks() == false")
	}
}
