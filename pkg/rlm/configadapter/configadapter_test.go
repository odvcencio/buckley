package configadapter

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/rlm"
)

func TestResolve(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.Config
		want func() rlm.Config
	}{
		{
			name: "nil keeps defaults",
			want: rlm.DefaultConfig,
		},
		{
			name: "zero keeps defaults",
			cfg:  &config.Config{},
			want: rlm.DefaultConfig,
		},
		{
			name: "false compatibility progress option",
			cfg: &config.Config{RLM: config.RLMConfig{
				Coordinator: config.RLMCoordinatorConfig{
					Model:          "coordinator",
					StreamPartials: false,
				},
				Scratchpad: config.RLMScratchpadConfig{
					PersistArtifacts: false,
					PersistDecisions: false,
				},
			}},
			want: func() rlm.Config {
				want := rlm.DefaultConfig()
				want.Coordinator.Model = "coordinator"
				want.Coordinator.StreamPartials = false
				want.Scratchpad.PersistArtifacts = false
				want.Scratchpad.PersistDecisions = false
				return want
			},
		},
		{
			name: "zero scalars preserve defaults after activation",
			cfg: &config.Config{RLM: config.RLMConfig{
				Coordinator: config.RLMCoordinatorConfig{Model: "coordinator"},
				SubAgent:    config.RLMSubAgentConfig{Model: "worker"},
				Scratchpad:  config.RLMScratchpadConfig{EvictionPolicy: "fifo"},
			}},
			want: func() rlm.Config {
				want := rlm.DefaultConfig()
				want.Coordinator.Model = "coordinator"
				want.Coordinator.StreamPartials = false
				want.SubAgent.Model = "worker"
				want.Scratchpad.EvictionPolicy = "fifo"
				want.Scratchpad.PersistArtifacts = false
				want.Scratchpad.PersistDecisions = false
				return want
			},
		},
		{
			name: "empty tier lists override defaults",
			cfg: &config.Config{RLM: config.RLMConfig{Tiers: map[string]config.RLMTierConfig{
				"light": {
					Models:   []string{},
					Prefer:   []string{},
					Requires: []string{},
				},
			}}},
			want: func() rlm.Config {
				want := rlm.DefaultConfig()
				light := want.Tiers[rlm.WeightLight]
				light.Models = []string{}
				light.Prefer = []string{}
				light.Requires = []string{}
				want.Tiers[rlm.WeightLight] = light
				return want
			},
		},
		{
			name: "tier pin caps and requirements",
			cfg: &config.Config{RLM: config.RLMConfig{Tiers: map[string]config.RLMTierConfig{
				"reasoning": {
					Model:             "reasoning-pin",
					Provider:          "openrouter",
					Models:            []string{"reasoning-pin", "reasoning-fallback"},
					MaxCostPerMillion: 18.5,
					MinContextWindow:  196000,
					Prefer:            []string{"quality", "cost"},
					Requires:          []string{"extended_thinking", "reasoning"},
				},
			}}},
			want: func() rlm.Config {
				want := rlm.DefaultConfig()
				want.Tiers[rlm.WeightReasoning] = rlm.TierConfig{
					Model:             "reasoning-pin",
					Provider:          "openrouter",
					Models:            []string{"reasoning-pin", "reasoning-fallback"},
					MaxCostPerMillion: 18.5,
					MinContextWindow:  196000,
					Prefer:            []string{"quality", "cost"},
					Requires:          []string{"extended_thinking", "reasoning"},
				}
				return want
			},
		},
		{
			name: "all active public fields",
			cfg: &config.Config{RLM: config.RLMConfig{
				Coordinator: config.RLMCoordinatorConfig{
					Model:               "coordinator",
					MaxIterations:       17,
					MaxTokensBudget:     12345,
					MaxWallTime:         42 * time.Second,
					ConfidenceThreshold: 0.42,
					StreamPartials:      false,
				},
				SubAgent: config.RLMSubAgentConfig{
					Model:         "worker",
					MaxConcurrent: 9,
					Timeout:       8 * time.Second,
				},
				Scratchpad: config.RLMScratchpadConfig{
					MaxEntriesMemory:  77,
					MaxRawBytesMemory: 88,
					EvictionPolicy:    "fifo",
					DefaultTTL:        13 * time.Second,
					PersistArtifacts:  false,
					PersistDecisions:  false,
				},
			}},
			want: func() rlm.Config {
				want := rlm.DefaultConfig()
				want.Coordinator = rlm.CoordinatorConfig{
					Model:               "coordinator",
					MaxIterations:       17,
					MaxTokensBudget:     12345,
					MaxWallTime:         42 * time.Second,
					ConfidenceThreshold: 0.42,
					StreamPartials:      false,
				}
				want.SubAgent = rlm.SubAgentRuntimeConfig{
					Model:         "worker",
					MaxConcurrent: 9,
					Timeout:       8 * time.Second,
				}
				want.Scratchpad = rlm.ScratchpadConfig{
					MaxEntriesMemory:  77,
					MaxRawBytesMemory: 88,
					EvictionPolicy:    "fifo",
					DefaultTTL:        13 * time.Second,
					PersistArtifacts:  false,
					PersistDecisions:  false,
				}
				return want
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want(), Resolve(tt.cfg))
		})
	}
}

func TestResolveClonesTierLists(t *testing.T) {
	cfg := &config.Config{RLM: config.RLMConfig{Tiers: map[string]config.RLMTierConfig{
		"light": {
			Models:   []string{"configured-light", "configured-fallback"},
			Prefer:   []string{"cost", "quality"},
			Requires: []string{"extended_thinking"},
		},
	}}}

	got := Resolve(cfg)
	source := cfg.RLM.Tiers["light"]
	source.Models[0] = "mutated-source"
	source.Prefer[0] = "mutated-source"
	source.Requires[0] = "mutated-source"
	cfg.RLM.Tiers["light"] = source

	light := got.Tiers[rlm.WeightLight]
	assert.Equal(t, []string{"configured-light", "configured-fallback"}, light.Models)
	assert.Equal(t, []string{"cost", "quality"}, light.Prefer)
	assert.Equal(t, []string{"extended_thinking"}, light.Requires)
}
