package rlm

import "time"

// CoordinatorConfig controls coordinator behavior.
type CoordinatorConfig struct {
	Model               string
	MaxIterations       int
	MaxTokensBudget     int
	MaxWallTime         time.Duration
	ConfidenceThreshold float64
	StreamPartials      bool
}

// SubAgentConfig controls sub-agent execution.
// Simplified from the original 5-tier system - all sub-agents use the same model.
// Patterns will emerge from usage; add tiers back when data shows need.
type SubAgentConfig struct {
	Model         string        // Model for all sub-agents (default: execution model)
	MaxConcurrent int           // Parallel execution limit
	Timeout       time.Duration // Per-task timeout
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
// Simplified: orchestrator + single sub-agent class (no tiers).
type Config struct {
	Coordinator CoordinatorConfig
	SubAgent    SubAgentConfig
	Scratchpad  ScratchpadConfig
}

// DefaultConfig returns a baseline RLM configuration.
func DefaultConfig() Config {
	return Config{
		Coordinator: CoordinatorConfig{
			Model:               "auto",
			MaxIterations:       10,
			MaxTokensBudget:     0, // 0 = unlimited, let the model work until done
			MaxWallTime:         10 * time.Minute,
			ConfidenceThreshold: 0.95,
			StreamPartials:      true,
		},
		SubAgent: SubAgentConfig{
			Model:         "", // Empty = use execution model
			MaxConcurrent: 5,
			Timeout:       5 * time.Minute,
		},
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

// Normalize fills missing defaults.
func (c *Config) Normalize() {
	if c == nil {
		return
	}
	if c.Coordinator.MaxIterations <= 0 {
		c.Coordinator.MaxIterations = 10
	}
	// MaxTokensBudget 0 = unlimited (don't override)
	if c.Coordinator.MaxWallTime <= 0 {
		c.Coordinator.MaxWallTime = 10 * time.Minute
	}
	if c.Coordinator.ConfidenceThreshold <= 0 {
		c.Coordinator.ConfidenceThreshold = 0.95
	}
	if c.SubAgent.MaxConcurrent <= 0 {
		c.SubAgent.MaxConcurrent = 5
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
	if c.Scratchpad.EvictionPolicy == "" {
		c.Scratchpad.EvictionPolicy = "lru"
	}
}
