package model

import (
	"strings"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/rules"
)

// ResolvePhaseModel selects the model for a runtime phase, honoring an explicit override.
func ResolvePhaseModel(cfg *config.Config, checker ReasoningChecker, engine *rules.Engine, phase, override string) string {
	override = strings.TrimSpace(override)
	if override != "" {
		return override
	}
	if cfg == nil {
		return ""
	}

	resolver := NewResolver(engine, ResolverConfig{
		Planning:  cfg.Models.Planning,
		Execution: cfg.Models.Execution,
		Review:    cfg.Models.Review,
	}, checker)

	if resolved := strings.TrimSpace(resolver.Resolve(phase)); resolved != "" {
		return resolved
	}
	return strings.TrimSpace(defaultModelForPhase(cfg, phase))
}

// ResolveReasoningEffort determines the reasoning effort for a phase/model pair.
func ResolveReasoningEffort(cfg *config.Config, checker ReasoningChecker, engine *rules.Engine, modelID, phase string) string {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" || checker == nil || !checker.SupportsReasoning(modelID) {
		return ""
	}

	configured := "auto"
	if cfg != nil {
		switch strings.ToLower(strings.TrimSpace(cfg.Models.Reasoning)) {
		case "", "auto":
			configured = "auto"
		case "off", "none":
			configured = "off"
		case "low", "medium", "high":
			configured = strings.ToLower(strings.TrimSpace(cfg.Models.Reasoning))
		default:
			configured = "auto"
		}
	}

	if engine != nil {
		result, err := engine.EvalStrategy("reasoning", "reasoning_mode", map[string]any{
			"reasoning": map[string]any{"config": configured},
			"task":      map[string]any{"phase": phase},
			"model":     map[string]any{"supports_reasoning": true},
		})
		if err == nil {
			effort, _ := result.Params["effort"].(string)
			effort = strings.TrimSpace(effort)
			if effort == "" || effort == "none" {
				return ""
			}
			return effort
		}
	}

	switch configured {
	case "off":
		return ""
	case "low", "medium", "high":
		return configured
	default:
		if phase == "planning" || phase == "review" {
			return "high"
		}
		return ""
	}
}

// InferModelTier returns a coarse model tier for prompt assembly policies.
func InferModelTier(modelID string) string {
	lower := strings.ToLower(strings.TrimSpace(modelID))
	switch {
	case lower == "":
		return "standard"
	case strings.Contains(lower, "haiku"),
		strings.Contains(lower, "mini"),
		strings.Contains(lower, "flash"),
		strings.Contains(lower, "nano"),
		strings.Contains(lower, "lite"),
		strings.Contains(lower, "small"),
		strings.Contains(lower, "fast"):
		return "fast"
	case strings.Contains(lower, "opus"),
		strings.Contains(lower, "o1"),
		strings.Contains(lower, "o3"),
		strings.Contains(lower, "gpt-5"),
		strings.Contains(lower, "thinking"),
		strings.Contains(lower, "pro"):
		return "premium"
	default:
		return "standard"
	}
}

func defaultModelForPhase(cfg *config.Config, phase string) string {
	if cfg == nil {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "planning":
		return cfg.Models.Planning
	case "review":
		return cfg.Models.Review
	default:
		return cfg.Models.Execution
	}
}
