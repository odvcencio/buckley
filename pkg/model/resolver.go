package model

import (
	"github.com/odvcencio/buckley/pkg/rules"
	"github.com/odvcencio/buckley/pkg/transparency"
)

// ReasoningChecker tests whether a model supports reasoning parameters.
type ReasoningChecker interface {
	SupportsReasoning(modelID string) bool
}

// ResolverConfig holds the static model assignments from config.
type ResolverConfig struct {
	Planning  string
	Execution string
	Review    string
}

// Resolver selects models by consulting arbiter rules with config fallback.
type Resolver struct {
	engine         *rules.Engine
	config         ResolverConfig
	checker        ReasoningChecker
	usageTracker   *transparency.UsageTracker
	sessionBudget  float64
	dailyBudget    float64
	monthlyBudget  float64
}

// NewResolver creates a Resolver. engine and checker may be nil; the resolver
// will fall back to config defaults when either is unavailable.
func NewResolver(engine *rules.Engine, cfg ResolverConfig, checker ReasoningChecker) *Resolver {
	return &Resolver{
		engine:  engine,
		config:  cfg,
		checker: checker,
	}
}

// SetUsageTracker configures cost-based budget overlay for model selection.
// When set, Resolve() will check cost/budgets rules after routing and may
// downgrade models or halt when budgets are exhausted.
func (r *Resolver) SetUsageTracker(ut *transparency.UsageTracker, sessionBudget, dailyBudget, monthlyBudget float64) {
	if r == nil {
		return
	}
	r.usageTracker = ut
	r.sessionBudget = sessionBudget
	r.dailyBudget = dailyBudget
	r.monthlyBudget = monthlyBudget
}

// Resolve returns the model ID for the given phase.
// It consults the routing.arb strategy first, then falls back to config.
// When a UsageTracker is configured, it also checks cost/budgets rules
// and may downgrade or halt based on budget utilization.
func (r *Resolver) Resolve(phase string) string {
	if r == nil {
		return ""
	}

	selected := r.resolveRouting(phase)

	// Cost overlay: check budgets after routing decision.
	if r.usageTracker != nil && r.engine != nil {
		costFacts := r.usageTracker.BuildCostFacts(r.sessionBudget, r.dailyBudget, r.monthlyBudget)
		costResult, err := r.engine.EvalStrategy("cost/budgets", "cost_policy", costFacts.ToMap())
		if err == nil {
			switch costResult.Params["action"] {
			case "downgrade_model":
				if tier, ok := costResult.Params["target_tier"].(string); ok && tier != "" {
					return r.cheapestModelForTier(tier)
				}
			case "halt":
				return ""
			}
		}
	}

	return selected
}

// resolveRouting performs the routing.arb evaluation with config fallback.
func (r *Resolver) resolveRouting(phase string) string {
	if r.engine != nil {
		supportsReasoning := false
		if r.checker != nil {
			configModel := r.configModelForPhase(phase)
			supportsReasoning = r.checker.SupportsReasoning(configModel)
		}
		result, err := r.engine.EvalStrategy("routing", "model_select", map[string]any{
			"task":  map[string]any{"phase": phase},
			"model": map[string]any{"supports_reasoning": supportsReasoning},
		})
		if err == nil {
			if modelID, ok := result.Params["model"].(string); ok && modelID != "" {
				return modelID
			}
		}
	}
	return r.configModelForPhase(phase)
}

// cheapestModelForTier maps a tier name to an inexpensive model.
func (r *Resolver) cheapestModelForTier(tier string) string {
	switch tier {
	case "light":
		return "claude-haiku-4-20250514"
	default:
		return r.config.Execution
	}
}

// configModelForPhase returns the static config model for a phase.
func (r *Resolver) configModelForPhase(phase string) string {
	switch phase {
	case "planning":
		return r.config.Planning
	case "execution":
		return r.config.Execution
	case "review":
		return r.config.Review
	default:
		return r.config.Execution
	}
}
