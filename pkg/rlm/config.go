package rlm

import (
	"fmt"
	"strings"
	"time"
)

// Weight identifies a subagent tier.
type Weight string

const (
	WeightTrivial   Weight = "trivial"
	WeightLight     Weight = "light"
	WeightMedium    Weight = "medium"
	WeightHeavy     Weight = "heavy"
	WeightReasoning Weight = "reasoning"
)

const (
	scratchpadEvictionPolicyLRU  = "lru"
	scratchpadEvictionPolicyFIFO = "fifo"
)

// CoordinatorConfig controls coordinator behavior.
type CoordinatorConfig struct {
	Model           string
	MaxIterations   int
	MaxTokensBudget int
	MaxWallTime     time.Duration
	// ConfidenceThreshold is prompt/context guidance only; it must not turn an
	// explicit set_answer(ready=false) draft into a completed answer.
	ConfidenceThreshold float64
	// StreamPartials controls coordinator progress publication to iteration
	// hooks and rlm iteration telemetry. It does not enable text-token
	// streaming.
	StreamPartials bool
}

// TierConfig controls model routing for subagents in a weight tier.
type TierConfig struct {
	Model             string
	Provider          string
	Models            []string
	MaxCostPerMillion float64
	MinContextWindow  int
	Prefer            []string
	Requires          []string
}

// SubAgentRuntimeConfig provides a global compatibility layer for sub-agent defaults.
type SubAgentRuntimeConfig struct {
	Model         string
	MaxConcurrent int
	// Timeout is the cooperative per-active-task execution deadline. The timer
	// starts after concurrency/rate queueing, uses one budget across retries,
	// defaults to 5m, and remains bounded by any parent context.
	Timeout time.Duration
}

// ScratchpadConfig controls scratchpad retention and limits.
type ScratchpadConfig struct {
	MaxEntriesMemory  int
	MaxRawBytesMemory int64
	EvictionPolicy    string
	DefaultTTL        time.Duration
	PersistArtifacts  bool
	PersistDecisions  bool
}

// Config is the top-level configuration for experimental coordinated execution.
type Config struct {
	Coordinator CoordinatorConfig
	SubAgent    SubAgentRuntimeConfig
	Tiers       map[Weight]TierConfig
	Scratchpad  ScratchpadConfig
}

// DefaultConfig returns a baseline coordinator–worker runtime configuration.
func DefaultConfig() Config {
	return Config{
		Coordinator: CoordinatorConfig{
			Model:               "auto",
			MaxIterations:       10,
			MaxTokensBudget:     100000,
			MaxWallTime:         10 * time.Minute,
			ConfidenceThreshold: 0.95,
			StreamPartials:      true,
		},
		SubAgent: SubAgentRuntimeConfig{
			MaxConcurrent: defaultBatchConcurrency,
			Timeout:       5 * time.Minute,
		},
		Tiers: DefaultTiers(),
		Scratchpad: ScratchpadConfig{
			MaxEntriesMemory:  1000,
			MaxRawBytesMemory: 50 * 1024 * 1024,
			EvictionPolicy:    scratchpadEvictionPolicyLRU,
			DefaultTTL:        time.Hour,
			PersistArtifacts:  true,
			PersistDecisions:  true,
		},
	}
}

// DefaultTiers returns the default tier configuration.
func DefaultTiers() map[Weight]TierConfig {
	return map[Weight]TierConfig{
		WeightTrivial: {
			MaxCostPerMillion: 0.50,
			MinContextWindow:  8000,
			Prefer:            []string{"speed", "cost"},
		},
		WeightLight: {
			MaxCostPerMillion: 3.00,
			MinContextWindow:  16000,
			Prefer:            []string{"cost", "quality"},
		},
		WeightMedium: {
			MaxCostPerMillion: 10.00,
			MinContextWindow:  32000,
			Prefer:            []string{"quality", "cost"},
		},
		WeightHeavy: {
			MaxCostPerMillion: 30.00,
			MinContextWindow:  64000,
			Prefer:            []string{"quality"},
		},
		WeightReasoning: {
			MinContextWindow: 100000,
			Prefer:           []string{"quality"},
			Requires:         []string{"extended_thinking"},
		},
	}
}

// Normalize fills missing defaults.
func (c *Config) Normalize() {
	if c == nil {
		return
	}
	if c.Tiers == nil {
		c.Tiers = DefaultTiers()
	} else {
		defaults := DefaultTiers()
		for key, tier := range defaults {
			if _, ok := c.Tiers[key]; !ok {
				c.Tiers[key] = tier
			}
		}
	}
	if c.Coordinator.MaxIterations <= 0 {
		c.Coordinator.MaxIterations = 10
	}
	if c.Coordinator.MaxTokensBudget <= 0 {
		c.Coordinator.MaxTokensBudget = 100000
	}
	if c.Coordinator.MaxWallTime <= 0 {
		c.Coordinator.MaxWallTime = 10 * time.Minute
	}
	if c.Coordinator.ConfidenceThreshold <= 0 {
		c.Coordinator.ConfidenceThreshold = 0.95
	}
	if c.SubAgent.MaxConcurrent <= 0 {
		c.SubAgent.MaxConcurrent = defaultBatchConcurrency
	}
	if c.SubAgent.Timeout <= 0 {
		c.SubAgent.Timeout = 5 * time.Minute
	}
	if c.Scratchpad.MaxEntriesMemory <= 0 {
		c.Scratchpad.MaxEntriesMemory = 1000
	}
	if c.Scratchpad.MaxRawBytesMemory <= 0 {
		c.Scratchpad.MaxRawBytesMemory = 50 * 1024 * 1024
	}
	if c.Scratchpad.DefaultTTL <= 0 {
		c.Scratchpad.DefaultTTL = time.Hour
	}
	c.Scratchpad.EvictionPolicy = normalizeScratchpadEvictionPolicy(c.Scratchpad.EvictionPolicy)
}

func (c Config) Validate() error {
	if err := validateScratchpadEvictionPolicy(c.Scratchpad.EvictionPolicy); err != nil {
		return err
	}
	return validateTierConfigs(c.Tiers)
}

func validateScratchpadEvictionPolicy(policy string) error {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "", scratchpadEvictionPolicyLRU, scratchpadEvictionPolicyFIFO:
		return nil
	default:
		return fmt.Errorf("rlm scratchpad eviction_policy must be lru or fifo")
	}
}

func validateTierConfigs(tiers map[Weight]TierConfig) error {
	valid := map[Weight]bool{}
	for _, weight := range Weights() {
		valid[weight] = true
	}
	for weight, tier := range tiers {
		if !valid[weight] {
			return fmt.Errorf("rlm tier %q must be one of: trivial, light, medium, heavy, reasoning", weight)
		}
		if tier.MaxCostPerMillion < 0 {
			return fmt.Errorf("rlm tier %q max_cost_per_million must be >= 0", weight)
		}
		if tier.MinContextWindow < 0 {
			return fmt.Errorf("rlm tier %q min_context_window must be >= 0", weight)
		}
	}
	return nil
}

func normalizeScratchpadEvictionPolicy(policy string) string {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case scratchpadEvictionPolicyFIFO:
		return scratchpadEvictionPolicyFIFO
	default:
		return scratchpadEvictionPolicyLRU
	}
}

// Weights returns weight tiers in stable order.
func Weights() []Weight {
	return []Weight{
		WeightTrivial,
		WeightLight,
		WeightMedium,
		WeightHeavy,
		WeightReasoning,
	}
}
