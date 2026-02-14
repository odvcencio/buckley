package execution

import (
	"time"

	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/telemetry"
)

// RLMStrategyConfig extends StrategyConfig with RLM-specific options.
type RLMStrategyConfig struct {
	StrategyConfig

	// ModelManager is the concrete manager needed by RLM runtime.
	// This is separate from StrategyConfig.Models because the RLM
	// runtime requires the full Manager, not just the ModelClient interface.
	ModelManager *model.Manager

	// Store provides persistence for scratchpad entries.
	Store *storage.Store

	// Telemetry receives iteration events.
	Telemetry *telemetry.Hub

	// CoordinatorModel overrides the model used for coordination (optional).
	CoordinatorModel string

	// MaxWallTime limits total execution time (default 30m).
	MaxWallTime time.Duration

	// MaxTokensBudget limits total tokens across all iterations.
	MaxTokensBudget int

	// SubAgentMaxConcurrent overrides the default sub-agent parallelism.
	SubAgentMaxConcurrent int
}
