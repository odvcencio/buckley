// pkg/ralph/control_test.go
package ralph

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseControlConfig_ValidYAML(t *testing.T) {
	yaml := `
backends:
  claude:
    command: "claude"
    args: ["-p", "{prompt}", "--workdir", "{sandbox}"]
    options:
      model: "opus"
    enabled: true
  buckley:
    type: internal
    options:
      execution_model: "anthropic/claude-sonnet-4-20250514"
    enabled: false

mode: sequential

schedule:
  - trigger: { every_iterations: 10 }
    action: rotate_backend
  - trigger: { on_error: "rate_limit" }
    action: next_backend

override:
  paused: false
  active_backends: [claude, codex]
  backend_options:
    claude:
      model: "sonnet"
`
	cfg, err := ParseControlConfig([]byte(yaml))
	if err != nil {
		t.Fatalf("ParseControlConfig failed: %v", err)
	}

	// Verify backends
	if len(cfg.Backends) != 2 {
		t.Errorf("expected 2 backends, got %d", len(cfg.Backends))
	}

	claude, ok := cfg.Backends["claude"]
	if !ok {
		t.Fatal("expected to find 'claude' backend")
	}
	if claude.Command != "claude" {
		t.Errorf("expected command 'claude', got %q", claude.Command)
	}
	if len(claude.Args) != 4 {
		t.Errorf("expected 4 args, got %d", len(claude.Args))
	}
	if claude.Options["model"] != "opus" {
		t.Errorf("expected model 'opus', got %q", claude.Options["model"])
	}
	if !claude.Enabled {
		t.Error("expected claude to be enabled")
	}

	buckley, ok := cfg.Backends["buckley"]
	if !ok {
		t.Fatal("expected to find 'buckley' backend")
	}
	if buckley.Type != "internal" {
		t.Errorf("expected type 'internal', got %q", buckley.Type)
	}
	if buckley.Enabled {
		t.Error("expected buckley to be disabled")
	}

	// Verify mode
	if cfg.Mode != "sequential" {
		t.Errorf("expected mode 'sequential', got %q", cfg.Mode)
	}

	// Verify schedule
	if len(cfg.Schedule) != 2 {
		t.Errorf("expected 2 schedule rules, got %d", len(cfg.Schedule))
	}
	if cfg.Schedule[0].Trigger.EveryIterations != 10 {
		t.Errorf("expected every_iterations 10, got %d", cfg.Schedule[0].Trigger.EveryIterations)
	}
	if cfg.Schedule[0].Action != "rotate_backend" {
		t.Errorf("expected action 'rotate_backend', got %q", cfg.Schedule[0].Action)
	}
	if cfg.Schedule[1].Trigger.OnError != "rate_limit" {
		t.Errorf("expected on_error 'rate_limit', got %q", cfg.Schedule[1].Trigger.OnError)
	}

	// Verify override
	if cfg.Override.Paused {
		t.Error("expected paused to be false")
	}
	if len(cfg.Override.ActiveBackends) != 2 {
		t.Errorf("expected 2 active backends, got %d", len(cfg.Override.ActiveBackends))
	}
	if cfg.Override.BackendOptions["claude"]["model"] != "sonnet" {
		t.Errorf("expected backend option model 'sonnet', got %q", cfg.Override.BackendOptions["claude"]["model"])
	}
}

func TestParseControlConfig_InvalidYAML(t *testing.T) {
	yaml := `invalid: yaml: content: [broken`

	_, err := ParseControlConfig([]byte(yaml))
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

func TestParseControlConfig_EmptyYAML(t *testing.T) {
	cfg, err := ParseControlConfig([]byte(""))
	if err != nil {
		t.Fatalf("ParseControlConfig failed: %v", err)
	}
	if cfg == nil {
		t.Error("expected non-nil config for empty YAML")
	}
}

func TestParseControlConfig_MinimalConfig(t *testing.T) {
	yaml := `
backends:
  claude:
    command: "claude"
    enabled: true
mode: sequential
`
	cfg, err := ParseControlConfig([]byte(yaml))
	if err != nil {
		t.Fatalf("ParseControlConfig failed: %v", err)
	}

	if len(cfg.Backends) != 1 {
		t.Errorf("expected 1 backend, got %d", len(cfg.Backends))
	}
	if cfg.Mode != "sequential" {
		t.Errorf("expected mode 'sequential', got %q", cfg.Mode)
	}
}

func TestLoadControlConfig_ValidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ralph-control.yaml")

	content := `
backends:
  test:
    command: "test-cli"
    enabled: true
mode: parallel
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	cfg, err := LoadControlConfig(path)
	if err != nil {
		t.Fatalf("LoadControlConfig failed: %v", err)
	}

	if cfg.Mode != "parallel" {
		t.Errorf("expected mode 'parallel', got %q", cfg.Mode)
	}
	if len(cfg.Backends) != 1 {
		t.Errorf("expected 1 backend, got %d", len(cfg.Backends))
	}
}

func TestLoadControlConfig_FileNotFound(t *testing.T) {
	_, err := LoadControlConfig("/nonexistent/path/ralph-control.yaml")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestControlConfig_Validate_ValidModes(t *testing.T) {
	tests := []struct {
		name string
		mode string
	}{
		{"sequential", "sequential"},
		{"parallel", "parallel"},
		{"round_robin", "round_robin"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &ControlConfig{
				Mode: tt.mode,
				Backends: map[string]BackendConfig{
					"test": {Command: "test-cli", Enabled: true},
				},
			}
			if err := cfg.Validate(); err != nil {
				t.Errorf("Validate failed for mode %q: %v", tt.mode, err)
			}
		})
	}
}

func TestControlConfig_Validate_InvalidMode(t *testing.T) {
	cfg := &ControlConfig{
		Mode: "invalid_mode",
		Backends: map[string]BackendConfig{
			"test": {Command: "test-cli", Enabled: true},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Error("expected error for invalid mode")
	}
}

func TestControlConfig_Validate_EmptyMode(t *testing.T) {
	cfg := &ControlConfig{
		Mode: "",
		Backends: map[string]BackendConfig{
			"test": {Command: "test-cli", Enabled: true},
		},
	}
	err := cfg.Validate()
	if err != nil {
		t.Fatalf("Validate failed for empty mode: %v", err)
	}
	if cfg.Mode != ModeSequential {
		t.Errorf("expected mode %q, got %q", ModeSequential, cfg.Mode)
	}
}

func TestControlConfig_Validate_TimeSlicedRotationRequiresInterval(t *testing.T) {
	cfg := &ControlConfig{
		Mode: ModeSequential,
		Rotation: RotationConfig{
			Mode: RotationTimeSliced,
		},
		Backends: map[string]BackendConfig{
			"test": {Command: "test-cli", Enabled: true},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Error("expected error for time_sliced rotation without interval")
	}
}

func TestControlConfig_Validate_InvalidContextPct(t *testing.T) {
	cfg := &ControlConfig{
		Mode: ModeSequential,
		Backends: map[string]BackendConfig{
			"test": {
				Command: "test-cli",
				Enabled: true,
				Thresholds: BackendThresholds{
					MaxContextPct: 120,
				},
			},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Error("expected error for invalid max_context_pct")
	}
}

func TestControlConfig_Validate_ModelRuleRequiresFields(t *testing.T) {
	cfg := &ControlConfig{
		Mode: ModeSequential,
		Backends: map[string]BackendConfig{
			"test": {
				Command: "test-cli",
				Enabled: true,
				Models: BackendModels{
					Rules: []ModelRule{
						{When: "consec_errors >= 2"},
					},
				},
			},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Error("expected error for model rule missing model")
	}
}

func TestControlConfig_Validate_NoBackends(t *testing.T) {
	cfg := &ControlConfig{
		Mode:     "sequential",
		Backends: map[string]BackendConfig{},
	}
	err := cfg.Validate()
	if err == nil {
		t.Error("expected error when no backends defined")
	}
}

func TestControlConfig_Validate_NilBackends(t *testing.T) {
	cfg := &ControlConfig{
		Mode:     "sequential",
		Backends: nil,
	}
	err := cfg.Validate()
	if err == nil {
		t.Error("expected error when backends is nil")
	}
}

func TestControlConfig_Validate_InternalBackendWithCommand(t *testing.T) {
	cfg := &ControlConfig{
		Mode: "sequential",
		Backends: map[string]BackendConfig{
			"buckley": {
				Type:    "internal",
				Command: "should-not-have-command",
				Enabled: true,
			},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Error("expected error for internal backend with command")
	}
}

func TestControlConfig_Validate_InternalBackendWithArgs(t *testing.T) {
	cfg := &ControlConfig{
		Mode: "sequential",
		Backends: map[string]BackendConfig{
			"buckley": {
				Type:    "internal",
				Args:    []string{"--should", "--not", "--have", "--args"},
				Enabled: true,
			},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Error("expected error for internal backend with args")
	}
}

func TestControlConfig_Validate_ExternalBackendWithoutCommand(t *testing.T) {
	cfg := &ControlConfig{
		Mode: "sequential",
		Backends: map[string]BackendConfig{
			"external": {
				Type:    "", // empty type means external
				Command: "", // missing command
				Enabled: true,
			},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Error("expected error for external backend without command")
	}
}

func TestControlConfig_Validate_ValidInternalBackend(t *testing.T) {
	cfg := &ControlConfig{
		Mode: "sequential",
		Backends: map[string]BackendConfig{
			"buckley": {
				Type: "internal",
				Options: map[string]string{
					"execution_model": "anthropic/claude-sonnet-4-20250514",
				},
				Enabled: true,
			},
		},
	}
	err := cfg.Validate()
	if err != nil {
		t.Errorf("Validate failed for valid internal backend: %v", err)
	}
}

func TestControlConfig_Validate_ValidExternalBackend(t *testing.T) {
	cfg := &ControlConfig{
		Mode: "sequential",
		Backends: map[string]BackendConfig{
			"claude": {
				Command: "claude",
				Args:    []string{"-p", "{prompt}"},
				Enabled: true,
			},
		},
	}
	err := cfg.Validate()
	if err != nil {
		t.Errorf("Validate failed for valid external backend: %v", err)
	}
}

func TestControlConfig_Validate_MixedBackends(t *testing.T) {
	cfg := &ControlConfig{
		Mode: "round_robin",
		Backends: map[string]BackendConfig{
			"claude": {
				Command: "claude",
				Args:    []string{"-p", "{prompt}"},
				Enabled: true,
			},
			"buckley": {
				Type: "internal",
				Options: map[string]string{
					"execution_model": "anthropic/claude-sonnet-4-20250514",
				},
				Enabled: false,
			},
		},
	}
	err := cfg.Validate()
	if err != nil {
		t.Errorf("Validate failed for mixed backends: %v", err)
	}
}

func TestScheduleTrigger_Fields(t *testing.T) {
	trigger := ScheduleTrigger{
		EveryIterations: 5,
		OnError:         "rate_limit",
		When:            "idle",
		Cron:            "0 */6 * * *",
	}

	if trigger.EveryIterations != 5 {
		t.Errorf("expected EveryIterations 5, got %d", trigger.EveryIterations)
	}
	if trigger.OnError != "rate_limit" {
		t.Errorf("expected OnError 'rate_limit', got %q", trigger.OnError)
	}
	if trigger.When != "idle" {
		t.Errorf("expected When 'idle', got %q", trigger.When)
	}
	if trigger.Cron != "0 */6 * * *" {
		t.Errorf("expected Cron '0 */6 * * *', got %q", trigger.Cron)
	}
}

func TestScheduleRule_Fields(t *testing.T) {
	rule := ScheduleRule{
		Trigger: ScheduleTrigger{EveryIterations: 10},
		Action:  "set_mode",
		Mode:    "parallel",
		Backend: "claude",
		Reason:  "testing",
	}

	if rule.Action != "set_mode" {
		t.Errorf("expected Action 'set_mode', got %q", rule.Action)
	}
	if rule.Mode != "parallel" {
		t.Errorf("expected Mode 'parallel', got %q", rule.Mode)
	}
	if rule.Backend != "claude" {
		t.Errorf("expected Backend 'claude', got %q", rule.Backend)
	}
	if rule.Reason != "testing" {
		t.Errorf("expected Reason 'testing', got %q", rule.Reason)
	}
}

func TestOverrideConfig_Fields(t *testing.T) {
	override := OverrideConfig{
		Paused:         true,
		ActiveBackends: []string{"claude", "codex"},
		NextAction:     "rotate_backend",
		BackendOptions: map[string]map[string]string{
			"claude": {"model": "sonnet"},
		},
	}

	if !override.Paused {
		t.Error("expected Paused to be true")
	}
	if len(override.ActiveBackends) != 2 {
		t.Errorf("expected 2 active backends, got %d", len(override.ActiveBackends))
	}
	if override.NextAction != "rotate_backend" {
		t.Errorf("expected NextAction 'rotate_backend', got %q", override.NextAction)
	}
	if override.BackendOptions["claude"]["model"] != "sonnet" {
		t.Errorf("expected model 'sonnet', got %q", override.BackendOptions["claude"]["model"])
	}
}

func TestParseControlConfig_AllScheduleActions(t *testing.T) {
	yaml := `
backends:
  test:
    command: "test"
    enabled: true
mode: sequential
schedule:
  - trigger: { every_iterations: 10 }
    action: rotate_backend
  - trigger: { on_error: "rate_limit" }
    action: next_backend
  - trigger: { when: "idle" }
    action: pause
    reason: "idle pause"
  - trigger: { cron: "0 */6 * * *" }
    action: resume
  - trigger: { every_iterations: 50 }
    action: set_mode
    mode: parallel
  - trigger: { on_error: "quota" }
    action: set_backend
    backend: fallback
`
	cfg, err := ParseControlConfig([]byte(yaml))
	if err != nil {
		t.Fatalf("ParseControlConfig failed: %v", err)
	}

	if len(cfg.Schedule) != 6 {
		t.Errorf("expected 6 schedule rules, got %d", len(cfg.Schedule))
	}

	// Check pause action with reason
	pauseRule := cfg.Schedule[2]
	if pauseRule.Action != "pause" {
		t.Errorf("expected action 'pause', got %q", pauseRule.Action)
	}
	if pauseRule.Reason != "idle pause" {
		t.Errorf("expected reason 'idle pause', got %q", pauseRule.Reason)
	}

	// Check set_mode action with mode
	setModeRule := cfg.Schedule[4]
	if setModeRule.Action != "set_mode" {
		t.Errorf("expected action 'set_mode', got %q", setModeRule.Action)
	}
	if setModeRule.Mode != "parallel" {
		t.Errorf("expected mode 'parallel', got %q", setModeRule.Mode)
	}

	// Check set_backend action with backend
	setBackendRule := cfg.Schedule[5]
	if setBackendRule.Action != "set_backend" {
		t.Errorf("expected action 'set_backend', got %q", setBackendRule.Action)
	}
	if setBackendRule.Backend != "fallback" {
		t.Errorf("expected backend 'fallback', got %q", setBackendRule.Backend)
	}
}
