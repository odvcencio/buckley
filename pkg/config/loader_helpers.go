package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// loadAndMerge loads a YAML file and merges it into the config.
func loadAndMerge(cfg *Config, path string, projectScope bool) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var override Config
	if err := yaml.Unmarshal(data, &override); err != nil {
		return fmt.Errorf("parsing YAML from %s: %w", path, err)
	}

	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parsing YAML from %s: %w", path, err)
	}

	mergeConfigs(cfg, &override, raw, projectScope)
	return nil
}

// mergeConfigs merges override into base.
func mergeConfigs(base, override *Config, raw map[string]any, projectScope bool) {
	if override == nil {
		return
	}

	mergeACPConfig(base, override, raw)
	mergeModelConfig(base, override, raw)
	mergeProviderConfig(base, override, raw)
	mergeExperimentAndEncodingConfig(base, override, raw)
	mergePersonalityConfig(base, override, raw)
	mergeMemoryConfig(base, override, raw)
	mergeOrchestratorConfig(base, override, raw)
	mergeExecutionAndRLMConfig(base, override, raw)
	mergeApprovalConfig(base, override, raw, projectScope)
	mergeSandboxConfig(base, override, raw, projectScope)
	mergeToolMiddlewareConfig(base, override, raw)
	mergeBatchConfig(base, override, raw)
	mergeGitCloneConfig(base, override, raw)
	mergeGitEventsConfig(base, override, raw)
	mergeBuckbotConfig(base, override, raw)
	mergeInputConfig(base, override, raw)
	mergeWorktreeConfig(base, override, raw)
	mergeIPCConfig(base, override, raw)
	mergeWorkflowPhaseConfig(base, override)
	mergeCostConfig(base, override)
	mergeRetryPolicyConfig(base, override)
	mergeArtifactsConfig(base, override, raw)
	mergeWorkflowConfig(base, override, raw)
	mergeCompactionConfig(base, override, raw)
	mergeUIConfig(base, override, raw)
	mergeCommentingConfig(base, override, raw)
	mergeDiagnosticsConfig(base, override, raw)
}

func mergeBuckbotConfig(base, override *Config, raw map[string]any) {
	if boolFieldSet(raw, "buckbot", "enabled") {
		base.Buckbot.Enabled = override.Buckbot.Enabled
	}
	if boolFieldSet(raw, "buckbot", "secret") {
		base.Buckbot.Secret = override.Buckbot.Secret
	}
	if boolFieldSet(raw, "buckbot", "webhook_bind") {
		base.Buckbot.WebhookBind = override.Buckbot.WebhookBind
	}
	if boolFieldSet(raw, "buckbot", "model") {
		base.Buckbot.Model = override.Buckbot.Model
	}
	if boolFieldSet(raw, "buckbot", "critic_model") {
		base.Buckbot.CriticModel = override.Buckbot.CriticModel
	}
	if boolFieldSet(raw, "buckbot", "per_review_budget_usd") {
		base.Buckbot.PerReviewBudgetUSD = override.Buckbot.PerReviewBudgetUSD
	}
	if boolFieldSet(raw, "buckbot", "monthly_budget_usd") {
		base.Buckbot.MonthlyBudgetUSD = override.Buckbot.MonthlyBudgetUSD
	}
	if boolFieldSet(raw, "buckbot", "max_review_iterations") {
		base.Buckbot.MaxReviewIterations = override.Buckbot.MaxReviewIterations
	}
	if boolFieldSet(raw, "buckbot", "max_validation_attempts") {
		base.Buckbot.MaxValidationAttempts = override.Buckbot.MaxValidationAttempts
	}
	if boolFieldSet(raw, "buckbot", "max_diff_bytes") {
		base.Buckbot.MaxDiffBytes = override.Buckbot.MaxDiffBytes
	}
}

func boolFieldSet(raw map[string]any, path ...string) bool {
	if len(path) == 0 || raw == nil {
		return false
	}
	current := any(raw)
	for _, key := range path {
		m, ok := current.(map[string]any)
		if !ok {
			return false
		}
		val, ok := m[key]
		if !ok {
			return false
		}
		current = val
	}
	return true
}
