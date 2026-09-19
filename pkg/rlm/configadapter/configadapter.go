// Package configadapter translates Buckley's public RLM configuration into
// runtime configuration.
package configadapter

import (
	"strings"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/rlm"
)

// Resolve overlays the public compatibility configuration onto RLM defaults.
// It is deliberately pure so each runtime topology receives identical config
// semantics without sharing construction dependencies.
func Resolve(cfg *config.Config) rlm.Config {
	base := rlm.DefaultConfig()
	if cfg == nil || cfg.RLM.IsZero() {
		return base
	}

	runtimeCfg := cfg.RLM
	if strings.TrimSpace(runtimeCfg.Coordinator.Model) != "" {
		base.Coordinator.Model = runtimeCfg.Coordinator.Model
	}
	if runtimeCfg.Coordinator.MaxIterations != 0 {
		base.Coordinator.MaxIterations = runtimeCfg.Coordinator.MaxIterations
	}
	if runtimeCfg.Coordinator.MaxTokensBudget != 0 {
		base.Coordinator.MaxTokensBudget = runtimeCfg.Coordinator.MaxTokensBudget
	}
	if runtimeCfg.Coordinator.MaxWallTime != 0 {
		base.Coordinator.MaxWallTime = runtimeCfg.Coordinator.MaxWallTime
	}
	if runtimeCfg.Coordinator.ConfidenceThreshold != 0 {
		base.Coordinator.ConfidenceThreshold = runtimeCfg.Coordinator.ConfidenceThreshold
	}
	nonTierConfigActive := hasNonTierValue(runtimeCfg)
	if nonTierConfigActive {
		base.Coordinator.StreamPartials = runtimeCfg.Coordinator.StreamPartials
	}

	if strings.TrimSpace(runtimeCfg.SubAgent.Model) != "" {
		base.SubAgent.Model = runtimeCfg.SubAgent.Model
	}
	if runtimeCfg.SubAgent.MaxConcurrent != 0 {
		base.SubAgent.MaxConcurrent = runtimeCfg.SubAgent.MaxConcurrent
	}
	if runtimeCfg.SubAgent.Timeout != 0 {
		base.SubAgent.Timeout = runtimeCfg.SubAgent.Timeout
	}

	if runtimeCfg.Scratchpad.MaxEntriesMemory != 0 {
		base.Scratchpad.MaxEntriesMemory = runtimeCfg.Scratchpad.MaxEntriesMemory
	}
	if runtimeCfg.Scratchpad.MaxRawBytesMemory != 0 {
		base.Scratchpad.MaxRawBytesMemory = runtimeCfg.Scratchpad.MaxRawBytesMemory
	}
	if strings.TrimSpace(runtimeCfg.Scratchpad.EvictionPolicy) != "" {
		base.Scratchpad.EvictionPolicy = runtimeCfg.Scratchpad.EvictionPolicy
	}
	if runtimeCfg.Scratchpad.DefaultTTL != 0 {
		base.Scratchpad.DefaultTTL = runtimeCfg.Scratchpad.DefaultTTL
	}
	if nonTierConfigActive {
		base.Scratchpad.PersistArtifacts = runtimeCfg.Scratchpad.PersistArtifacts
		base.Scratchpad.PersistDecisions = runtimeCfg.Scratchpad.PersistDecisions
	}
	applyConfiguredTiers(&base, runtimeCfg.Tiers)

	base.Normalize()
	return base
}

func applyConfiguredTiers(base *rlm.Config, configured map[string]config.RLMTierConfig) {
	if base == nil || len(configured) == 0 {
		return
	}
	if base.Tiers == nil {
		base.Tiers = rlm.DefaultTiers()
	}
	for name, tier := range configured {
		weight := rlm.Weight(strings.TrimSpace(name))
		if weight == "" || !tierHasValue(tier) {
			continue
		}
		current := base.Tiers[weight]
		if value := strings.TrimSpace(tier.Model); value != "" {
			current.Model = value
		}
		if value := strings.TrimSpace(tier.Provider); value != "" {
			current.Provider = value
		}
		if tier.Models != nil {
			current.Models = append([]string{}, tier.Models...)
		}
		if tier.MaxCostPerMillion != 0 {
			current.MaxCostPerMillion = tier.MaxCostPerMillion
		}
		if tier.MinContextWindow != 0 {
			current.MinContextWindow = tier.MinContextWindow
		}
		if tier.Prefer != nil {
			current.Prefer = append([]string{}, tier.Prefer...)
		}
		if tier.Requires != nil {
			current.Requires = append([]string{}, tier.Requires...)
		}
		base.Tiers[weight] = current
	}
}

func hasNonTierValue(runtimeCfg config.RLMConfig) bool {
	return runtimeCfg.Coordinator.Model != "" ||
		runtimeCfg.Coordinator.MaxIterations != 0 ||
		runtimeCfg.Coordinator.MaxTokensBudget != 0 ||
		runtimeCfg.Coordinator.MaxWallTime != 0 ||
		runtimeCfg.Coordinator.ConfidenceThreshold != 0 ||
		runtimeCfg.Coordinator.StreamPartials ||
		runtimeCfg.SubAgent.Model != "" ||
		runtimeCfg.SubAgent.MaxConcurrent != 0 ||
		runtimeCfg.SubAgent.Timeout != 0 ||
		runtimeCfg.Scratchpad.MaxEntriesMemory != 0 ||
		runtimeCfg.Scratchpad.MaxRawBytesMemory != 0 ||
		runtimeCfg.Scratchpad.EvictionPolicy != "" ||
		runtimeCfg.Scratchpad.DefaultTTL != 0 ||
		runtimeCfg.Scratchpad.PersistArtifacts ||
		runtimeCfg.Scratchpad.PersistDecisions
}

func tierHasValue(tier config.RLMTierConfig) bool {
	return strings.TrimSpace(tier.Model) != "" ||
		strings.TrimSpace(tier.Provider) != "" ||
		tier.Models != nil ||
		tier.MaxCostPerMillion != 0 ||
		tier.MinContextWindow != 0 ||
		tier.Prefer != nil ||
		tier.Requires != nil
}
