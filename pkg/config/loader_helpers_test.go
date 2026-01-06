package config

import "testing"

func TestMergeConfigsPreservesBooleanDefaults(t *testing.T) {
	base := DefaultConfig()
	override := &Config{
		Models: ModelConfig{
			Planning: "custom-plan",
		},
	}
	raw := map[string]any{
		"models": map[string]any{
			"planning": "custom-plan",
		},
	}

	mergeConfigs(base, override, raw)

	if !base.Providers.OpenRouter.Enabled {
		t.Fatalf("OpenRouter enabled flag should remain true when not overridden")
	}
	if base.Models.Planning != "custom-plan" {
		t.Fatalf("expected planning model to be overridden")
	}
}

func TestMergeConfigsRespectsBooleanOverrides(t *testing.T) {
	base := DefaultConfig()
	override := &Config{}
	override.Providers.OpenRouter.Enabled = false
	raw := map[string]any{
		"providers": map[string]any{
			"openrouter": map[string]any{
				"enabled": false,
			},
		},
	}

	mergeConfigs(base, override, raw)

	if base.Providers.OpenRouter.Enabled {
		t.Fatalf("expected OpenRouter enabled flag to update when override is explicit")
	}
}

func TestMergeConfigsHandlesEncodingOverride(t *testing.T) {
	base := DefaultConfig()
	override := &Config{}
	override.Encoding.UseToon = false
	raw := map[string]any{
		"encoding": map[string]any{
			"use_toon": false,
		},
	}

	mergeConfigs(base, override, raw)

	if base.Encoding.UseToon {
		t.Fatalf("expected encoding.use_toon to update when override provided")
	}
}

func TestMergeConfigsRespectsApprovalListOverrides(t *testing.T) {
	base := DefaultConfig()
	if len(base.Approval.AllowedTools) == 0 {
		t.Fatalf("expected default allowed tools to be non-empty")
	}

	override := &Config{}
	override.Approval.AllowedTools = []string{}
	raw := map[string]any{
		"approval": map[string]any{
			"allowed_tools": []any{},
		},
	}

	mergeConfigs(base, override, raw)

	if len(base.Approval.AllowedTools) != 0 {
		t.Fatalf("expected approval.allowed_tools to be overridden to empty list")
	}
}

func TestMergeConfigsRespectsBatchOverrides(t *testing.T) {
	base := DefaultConfig()
	if base.Batch.Enabled {
		t.Fatalf("expected batch to be disabled by default")
	}

	override := &Config{}
	override.Batch.Enabled = true
	override.Batch.JobTemplate.Command = []string{"buckley"}
	override.Batch.JobTemplate.EnvFromSecrets = []string{"buckley-env"}
	override.Batch.JobTemplate.ConfigMap = "buckley-config"
	raw := map[string]any{
		"batch": map[string]any{
			"enabled": true,
			"job_template": map[string]any{
				"command":          []any{"buckley"},
				"env_from_secrets": []any{"buckley-env"},
				"config_map":       "buckley-config",
			},
		},
	}

	mergeConfigs(base, override, raw)

	if !base.Batch.Enabled {
		t.Fatalf("expected batch.enabled to update when override is explicit")
	}
	if len(base.Batch.JobTemplate.Command) != 1 || base.Batch.JobTemplate.Command[0] != "buckley" {
		t.Fatalf("expected batch.job_template.command to be overridden")
	}
	if len(base.Batch.JobTemplate.EnvFromSecrets) != 1 || base.Batch.JobTemplate.EnvFromSecrets[0] != "buckley-env" {
		t.Fatalf("expected batch.job_template.env_from_secrets to be overridden")
	}
	if base.Batch.JobTemplate.ConfigMap != "buckley-config" {
		t.Fatalf("expected batch.job_template.config_map to be overridden")
	}
}

func TestMergeConfigsRespectsInputOverrides(t *testing.T) {
	base := DefaultConfig()

	override := &Config{}
	override.Input.Video.Enabled = true
	override.Input.Video.MaxFrames = 9
	raw := map[string]any{
		"input": map[string]any{
			"video": map[string]any{
				"enabled":    true,
				"max_frames": 9,
			},
		},
	}

	mergeConfigs(base, override, raw)

	if !base.Input.Video.Enabled {
		t.Fatalf("expected input.video.enabled to update when override is explicit")
	}
	if base.Input.Video.MaxFrames != 9 {
		t.Fatalf("expected input.video.max_frames to update when override is explicit")
	}
}

func TestMergeConfigsRespectsModelUtilityOverrides(t *testing.T) {
	base := DefaultConfig()
	base.Models.FallbackChains["openai/gpt-4o-mini"] = []string{"openai/gpt-4o-mini"}
	if len(base.Models.VisionFallback) == 0 {
		t.Fatalf("expected default vision fallback to be non-empty")
	}

	override := &Config{
		Models: ModelConfig{
			DefaultProvider: "openai",
			Reasoning:       "high",
			VisionFallback:  []string{},
			FallbackChains:  map[string][]string{},
			Utility: UtilityModelConfig{
				Commit:     "openai/gpt-4.1-mini",
				PR:         "openai/gpt-4.1-mini",
				Compaction: "openai/gpt-4o-mini",
				TodoPlan:   "openai/gpt-4o-mini",
			},
		},
	}
	raw := map[string]any{
		"models": map[string]any{
			"default_provider": "openai",
			"reasoning":        "high",
			"vision_fallback":  []any{},
			"fallback_chains":  map[string]any{},
			"utility": map[string]any{
				"commit":     "openai/gpt-4.1-mini",
				"pr":         "openai/gpt-4.1-mini",
				"compaction": "openai/gpt-4o-mini",
				"todo_plan":  "openai/gpt-4o-mini",
			},
		},
	}

	mergeConfigs(base, override, raw)

	if base.Models.DefaultProvider != "openai" {
		t.Fatalf("expected models.default_provider to be overridden")
	}
	if base.Models.Reasoning != "high" {
		t.Fatalf("expected models.reasoning to be overridden")
	}
	if base.Models.Utility.Commit != "openai/gpt-4.1-mini" {
		t.Fatalf("expected models.utility.commit to be overridden")
	}
	if base.Models.Utility.PR != "openai/gpt-4.1-mini" {
		t.Fatalf("expected models.utility.pr to be overridden")
	}
	if base.Models.Utility.Compaction != "openai/gpt-4o-mini" {
		t.Fatalf("expected models.utility.compaction to be overridden")
	}
	if base.Models.Utility.TodoPlan != "openai/gpt-4o-mini" {
		t.Fatalf("expected models.utility.todo_plan to be overridden")
	}
	if len(base.Models.VisionFallback) != 0 {
		t.Fatalf("expected models.vision_fallback to be overridable to an empty list")
	}
	if len(base.Models.FallbackChains) != 0 {
		t.Fatalf("expected models.fallback_chains to be overridable to an empty map")
	}
}
