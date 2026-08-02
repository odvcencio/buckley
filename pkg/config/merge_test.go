package config

import "testing"

// TestMergeConfigsFixesZeroOverrideBug proves the reflection-driven walker
// closes the zero-override gap PR #105's review flagged: several
// pre-reflection mergers gated non-bool scalars on
// `override.Field != zeroValue`, so an explicit zero/empty value in an
// override layer silently kept the base default instead of landing.
// mergeConfigs gates every field on raw presence instead, so an explicit
// zero always lands.
func TestMergeConfigsFixesZeroOverrideBug(t *testing.T) {
	base := DefaultConfig()
	if base.CostManagement.SessionBudget == 0 {
		t.Fatalf("expected a non-zero default session budget to exercise the fix")
	}
	if base.RetryPolicy.MaxRetries == 0 {
		t.Fatalf("expected a non-zero default max_retries to exercise the fix")
	}
	if base.Compaction.TaskInterval == 0 {
		t.Fatalf("expected a non-zero default compaction task_interval to exercise the fix")
	}
	if base.Artifacts.PlanningDir == "" {
		t.Fatalf("expected a non-empty default artifacts.planning_dir to exercise the fix")
	}

	override := &Config{}
	override.CostManagement.SessionBudget = 0
	override.RetryPolicy.MaxRetries = 0
	override.Compaction.TaskInterval = 0
	override.Artifacts.PlanningDir = ""
	raw := map[string]any{
		"cost_management": map[string]any{"session_budget": 0},
		"retry_policy":    map[string]any{"max_retries": 0},
		"compaction":      map[string]any{"task_interval": 0},
		"artifacts":       map[string]any{"planning_dir": ""},
	}

	mergeConfigs(base, override, raw, false)

	if base.CostManagement.SessionBudget != 0 {
		t.Errorf("expected explicit cost_management.session_budget=0 to override the default, got %v", base.CostManagement.SessionBudget)
	}
	if base.RetryPolicy.MaxRetries != 0 {
		t.Errorf("expected explicit retry_policy.max_retries=0 to override the default, got %v", base.RetryPolicy.MaxRetries)
	}
	if base.Compaction.TaskInterval != 0 {
		t.Errorf("expected explicit compaction.task_interval=0 to override the default, got %v", base.Compaction.TaskInterval)
	}
	if base.Artifacts.PlanningDir != "" {
		t.Errorf("expected explicit artifacts.planning_dir=\"\" to override the default, got %q", base.Artifacts.PlanningDir)
	}
}

// TestMergeConfigsPreservesZeroFieldsWhenAbsent is the control case for
// TestMergeConfigsFixesZeroOverrideBug: when an override layer doesn't
// mention a field at all, the base default must still survive.
func TestMergeConfigsPreservesZeroFieldsWhenAbsent(t *testing.T) {
	base := DefaultConfig()
	wantBudget := base.CostManagement.SessionBudget
	wantRetries := base.RetryPolicy.MaxRetries

	override := &Config{}
	raw := map[string]any{}

	mergeConfigs(base, override, raw, false)

	if base.CostManagement.SessionBudget != wantBudget {
		t.Errorf("expected cost_management.session_budget default to survive an absent override, got %v", base.CostManagement.SessionBudget)
	}
	if base.RetryPolicy.MaxRetries != wantRetries {
		t.Errorf("expected retry_policy.max_retries default to survive an absent override, got %v", base.RetryPolicy.MaxRetries)
	}
}

// TestMergeConfigsClosesLegacyCoverageGaps proves the reflective walker
// covers sections the hand-written mergers never wired up at all:
// sandbox.docker.*, notify.*, and prompt_cache.* had no merge logic in
// any loader_merge_*.go file, so setting them in a project or user
// config.yaml silently had no effect. Walking the whole Config struct by
// its yaml tags, instead of a hand-maintained per-field list, closes this
// class of gap by construction.
func TestMergeConfigsClosesLegacyCoverageGaps(t *testing.T) {
	base := DefaultConfig()
	if base.Sandbox.DockerSandbox.Enabled {
		t.Fatalf("expected docker sandbox disabled by default")
	}
	if base.Notify.Enabled {
		t.Fatalf("expected notify disabled by default")
	}
	if base.PromptCache.Enabled {
		t.Fatalf("expected prompt cache disabled by default")
	}

	override := &Config{}
	override.Sandbox.DockerSandbox.Enabled = true
	override.Sandbox.DockerSandbox.Image = "custom:image"
	override.Notify.Enabled = true
	override.Notify.Slack.WebhookURL = "https://hooks.example/webhook"
	override.PromptCache.Enabled = true
	override.PromptCache.SystemMessages = 3
	raw := map[string]any{
		"sandbox": map[string]any{
			"docker": map[string]any{
				"enabled": true,
				"image":   "custom:image",
			},
		},
		"notify": map[string]any{
			"enabled": true,
			"slack": map[string]any{
				"webhook_url": "https://hooks.example/webhook",
			},
		},
		"prompt_cache": map[string]any{
			"enabled":         true,
			"system_messages": 3,
		},
	}

	mergeConfigs(base, override, raw, false)

	if !base.Sandbox.DockerSandbox.Enabled || base.Sandbox.DockerSandbox.Image != "custom:image" {
		t.Errorf("expected sandbox.docker.* to merge from YAML, got %+v", base.Sandbox.DockerSandbox)
	}
	if !base.Notify.Enabled || base.Notify.Slack.WebhookURL != "https://hooks.example/webhook" {
		t.Errorf("expected notify.* to merge from YAML, got %+v", base.Notify)
	}
	if !base.PromptCache.Enabled || base.PromptCache.SystemMessages != 3 {
		t.Errorf("expected prompt_cache.* to merge from YAML, got %+v", base.PromptCache)
	}
}

// TestMergeConfigsProjectScopeCannotLoosenSecurityBoundary asserts
// approval.mode, sandbox.mode, and sandbox.allow_unsafe stay
// user-config-only: a project config layer (./.buckley/config.yaml)
// setting any of them must be ignored, matching mergeApprovalConfig and
// mergeSandboxConfig's `if !projectScope` guard.
func TestMergeConfigsProjectScopeCannotLoosenSecurityBoundary(t *testing.T) {
	base := DefaultConfig()
	wantMode := base.Approval.Mode
	wantSandboxMode := base.Sandbox.Mode

	override := &Config{}
	override.Approval.Mode = "yolo"
	override.Sandbox.Mode = "disabled"
	override.Sandbox.AllowUnsafe = true
	raw := map[string]any{
		"approval": map[string]any{"mode": "yolo"},
		"sandbox": map[string]any{
			"mode":         "disabled",
			"allow_unsafe": true,
		},
	}

	mergeConfigs(base, override, raw, true)

	if base.Approval.Mode != wantMode {
		t.Errorf("expected project-scoped approval.mode override to be ignored, got %q", base.Approval.Mode)
	}
	if base.Sandbox.Mode != wantSandboxMode {
		t.Errorf("expected project-scoped sandbox.mode override to be ignored, got %q", base.Sandbox.Mode)
	}
	if base.Sandbox.AllowUnsafe {
		t.Errorf("expected project-scoped sandbox.allow_unsafe override to be ignored")
	}

	// The same override, applied at user scope, must take effect.
	base2 := DefaultConfig()
	mergeConfigs(base2, override, raw, false)
	if base2.Approval.Mode != "yolo" {
		t.Errorf("expected user-scoped approval.mode override to apply, got %q", base2.Approval.Mode)
	}
	if base2.Sandbox.Mode != "disabled" || !base2.Sandbox.AllowUnsafe {
		t.Errorf("expected user-scoped sandbox overrides to apply, got %+v", base2.Sandbox)
	}
}

// TestMergeConfigsModelRoutingMergesPerKey asserts providers.model_routing
// merges by key, retaining the built-in prefixes a project or user config
// doesn't mention, matching mergeProviderConfig's per-key ModelRouting
// merge.
func TestMergeConfigsModelRoutingMergesPerKey(t *testing.T) {
	base := DefaultConfig()
	if _, ok := base.Providers.ModelRouting["openai/"]; !ok {
		t.Fatalf("expected a built-in openai/ routing entry")
	}

	override := &Config{Providers: ProviderConfig{ModelRouting: map[string]string{
		"custom/": "customprovider",
	}}}
	raw := map[string]any{
		"providers": map[string]any{
			"model_routing": map[string]any{"custom/": "customprovider"},
		},
	}

	mergeConfigs(base, override, raw, false)

	if base.Providers.ModelRouting["custom/"] != "customprovider" {
		t.Errorf("expected custom/ routing entry to merge in")
	}
	if base.Providers.ModelRouting["openai/"] != "openai" {
		t.Errorf("expected built-in openai/ routing entry to survive the per-key merge")
	}
}
