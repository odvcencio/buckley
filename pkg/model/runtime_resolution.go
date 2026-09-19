package model

import (
	"fmt"
	"strings"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/rules"
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
		Light:     cfg.Models.Light,
		Review:    cfg.Models.Review,
	}, checker)

	if resolved := strings.TrimSpace(resolver.Resolve(phase)); resolved != "" {
		return resolved
	}
	return strings.TrimSpace(defaultModelForPhase(cfg, phase))
}

// ResolvePhaseModelRequired resolves a phase without silently substituting an
// unrelated provider or model. Callers that are about to dispatch a request
// should use this form so an incomplete configuration is visible to the
// operator instead of becoming an unexpected bill or a misleading trace.
func ResolvePhaseModelRequired(cfg *config.Config, checker ReasoningChecker, engine *rules.Engine, phase, override string) (string, error) {
	modelID := strings.TrimSpace(ResolvePhaseModel(cfg, checker, engine, phase, override))
	if modelID == "" {
		phase = strings.TrimSpace(phase)
		if phase == "" {
			phase = "execution"
		}
		return "", fmt.Errorf("no model resolved for %s phase; configure models.%s or pass an explicit model override; Buckley will not substitute another model", phase, phase)
	}
	return modelID, nil
}

// ResolveReasoningEffort determines the reasoning effort for a phase/model pair.
func ResolveReasoningEffort(cfg *config.Config, checker ReasoningChecker, engine *rules.Engine, modelID, phase string) string {
	return ResolveReasoningEffortForTask(cfg, checker, engine, modelID, phase, "")
}

// ResolveReasoningEffortForTask determines the reasoning effort for a
// phase/model/task tuple. Task names let one-shot utility commands opt into
// bounded useful reasoning without changing the broader execution default.
func ResolveReasoningEffortForTask(cfg *config.Config, checker ReasoningChecker, engine *rules.Engine, modelID, phase, taskName string) string {
	effort, _ := ResolveReasoningEffortForTaskWithCapability(cfg, checker, engine, modelID, phase, taskName)
	return effort
}

// ResolveReasoningEffortForTaskWithCapability determines the reasoning effort
// and returns any richer capability evidence exposed by the checker.
func ResolveReasoningEffortForTaskWithCapability(cfg *config.Config, checker ReasoningChecker, engine *rules.Engine, modelID, phase, taskName string) (string, CapabilityResolution) {
	modelID = strings.TrimSpace(modelID)
	capability := CapabilityResolution{
		Model:      modelID,
		Capability: "reasoning",
		State:      CapabilityUnknown,
		Source:     "checker_unavailable",
	}
	if modelID == "" || checker == nil {
		return "", capability
	}
	if resolver, ok := checker.(interface {
		ResolveReasoningCapability(string) CapabilityResolution
	}); ok {
		capability = resolver.ResolveReasoningCapability(modelID)
	} else if checker.SupportsReasoning(modelID) {
		capability = CapabilityResolution{Model: modelID, Capability: "reasoning", State: CapabilitySupported, Source: "boolean_checker"}
	} else {
		capability = CapabilityResolution{Model: modelID, Capability: "reasoning", State: CapabilityUnknown, Source: "boolean_checker"}
	}
	if !capability.Supported() {
		return "", capability
	}
	phase = strings.ToLower(strings.TrimSpace(phase))
	taskName = strings.ToLower(strings.TrimSpace(taskName))

	configured := "auto"
	if cfg != nil {
		switch strings.ToLower(strings.TrimSpace(cfg.Models.Reasoning)) {
		case "", "auto":
			configured = "auto"
		case "off", "none":
			configured = "off"
		case "minimal", "low", "medium", "high", "xhigh":
			configured = strings.ToLower(strings.TrimSpace(cfg.Models.Reasoning))
		default:
			configured = "auto"
		}
	}

	if engine != nil {
		result, err := engine.EvalStrategy("reasoning", "reasoning_mode", map[string]any{
			"reasoning": map[string]any{"config": configured},
			"task":      map[string]any{"phase": phase, "name": taskName, "command": taskName},
			"model":     map[string]any{"supports_reasoning": true},
		})
		if err == nil {
			effort, _ := result.Params["effort"].(string)
			effort = strings.TrimSpace(effort)
			if effort == "" || effort == "none" {
				return "", capability
			}
			return effort, capability
		}
	}

	switch configured {
	case "off":
		return "", capability
	case "minimal", "low", "medium", "high", "xhigh":
		return configured, capability
	default:
		if phase == "planning" || phase == "review" {
			return "high", capability
		}
		switch taskName {
		case "commit":
			return "low", capability
		case "pr":
			return "medium", capability
		}
		return "", capability
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
