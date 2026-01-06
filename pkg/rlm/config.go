package rlm

import "time"

// Weight identifies a subagent tier.
type Weight string

const (
	WeightTrivial   Weight = "trivial"
	WeightLight     Weight = "light"
	WeightMedium    Weight = "medium"
	WeightHeavy     Weight = "heavy"
	WeightReasoning Weight = "reasoning"
)

// CoordinatorConfig controls coordinator behavior.
type CoordinatorConfig struct {
	Model               string
	MaxIterations       int
	MaxTokensBudget     int
	MaxWallTime         time.Duration
	ConfidenceThreshold float64
	StreamPartials      bool
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

// ScratchpadConfig controls scratchpad retention and limits.
type ScratchpadConfig struct {
	MaxEntriesMemory  int
	MaxRawBytesMemory int64
	EvictionPolicy    string
	DefaultTTL        time.Duration
	PersistArtifacts  bool
	PersistDecisions  bool
}

// Config is the top-level configuration for RLM.
type Config struct {
	Coordinator CoordinatorConfig
	Tiers       map[Weight]TierConfig
	Scratchpad  ScratchpadConfig
}

// DefaultConfig returns a baseline RLM configuration.
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
		Tiers: DefaultTiers(),
		Scratchpad: ScratchpadConfig{
			MaxEntriesMemory:  1000,
			MaxRawBytesMemory: 50 * 1024 * 1024,
			EvictionPolicy:    "lru",
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
	if c.Scratchpad.MaxEntriesMemory <= 0 {
		c.Scratchpad.MaxEntriesMemory = 1000
	}
	if c.Scratchpad.MaxRawBytesMemory <= 0 {
		c.Scratchpad.MaxRawBytesMemory = 50 * 1024 * 1024
	}
	if c.Scratchpad.DefaultTTL <= 0 {
		c.Scratchpad.DefaultTTL = time.Hour
	}
	if c.Scratchpad.EvictionPolicy == "" {
		c.Scratchpad.EvictionPolicy = "lru"
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
