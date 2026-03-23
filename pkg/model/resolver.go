package model

import "github.com/odvcencio/buckley/pkg/rules"

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
	engine  *rules.Engine
	config  ResolverConfig
	checker ReasoningChecker
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

// Resolve returns the model ID for the given phase.
// It consults the routing.arb strategy first, then falls back to config.
func (r *Resolver) Resolve(phase string) string {
	if r == nil {
		return ""
	}
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
