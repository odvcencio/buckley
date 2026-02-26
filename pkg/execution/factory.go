package execution

import (
	"fmt"

	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/buckley/pkg/tool"
)

// DefaultFactory creates execution strategies based on configuration.
type DefaultFactory struct {
	models    *model.Manager
	registry  *tool.Registry
	store     *storage.Store
	telemetry *telemetry.Hub
	config    FactoryConfig
}

// FactoryConfig provides options for strategy creation.
type FactoryConfig struct {
	// DefaultMaxIterations for tool loops
	DefaultMaxIterations int

	// ConfidenceThreshold for RLM strategy
	ConfidenceThreshold float64

	// RLMSubAgentMaxConcurrent controls RLM sub-agent concurrency
	RLMSubAgentMaxConcurrent int

	// UseTOON enables compact tool result encoding
	UseTOON bool

	// EnableReasoning extracts thinking from reasoning models
	EnableReasoning bool
}

// NewFactory creates a strategy factory.
func NewFactory(models *model.Manager, registry *tool.Registry, store *storage.Store, tel *telemetry.Hub, cfg FactoryConfig) *DefaultFactory {
	if cfg.DefaultMaxIterations <= 0 {
		cfg.DefaultMaxIterations = 25
	}
	if cfg.ConfidenceThreshold <= 0 {
		cfg.ConfidenceThreshold = 0.7
	}

	return &DefaultFactory{
		models:    models,
		registry:  registry,
		store:     store,
		telemetry: tel,
		config:    cfg,
	}
}

// Create returns a strategy for the given mode.
//
// Supported modes:
//   - "rlm": Coordinator pattern with sub-agents (recommended for complex tasks)
//   - "classic": Single-agent execution using the ToolRunner loop
//   - "auto": Defaults to classic unless overridden by caller
func (f *DefaultFactory) Create(mode string) (ExecutionStrategy, error) {
	switch mode {
	case "rlm":
		return f.createRLM()
	case "classic":
		return f.createClassic()
	case "auto", "":
		return f.createClassic()
	default:
		return nil, fmt.Errorf("unknown execution mode: %s", mode)
	}
}

// createRLM creates the RLM coordinator strategy.
func (f *DefaultFactory) createRLM() (ExecutionStrategy, error) {
	return NewRLMStrategy(RLMStrategyConfig{
		StrategyConfig: StrategyConfig{
			Models:               f.models, // *model.Manager implements ModelClient
			Registry:             f.registry,
			DefaultMaxIterations: f.config.DefaultMaxIterations,
			ConfidenceThreshold:  f.config.ConfidenceThreshold,
			EnableReasoning:      f.config.EnableReasoning,
			UseTOON:              f.config.UseTOON,
		},
		ModelManager:          f.models, // RLM runtime needs concrete type
		Store:                 f.store,
		Telemetry:             f.telemetry,
		SubAgentMaxConcurrent: f.config.RLMSubAgentMaxConcurrent,
	})
}

// createClassic creates the single-agent ToolRunner strategy.
func (f *DefaultFactory) createClassic() (ExecutionStrategy, error) {
	return NewClassicStrategy(StrategyConfig{
		Models:               f.models,
		Registry:             f.registry,
		DefaultMaxIterations: f.config.DefaultMaxIterations,
		EnableReasoning:      f.config.EnableReasoning,
	})
}
