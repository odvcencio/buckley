package main

import (
	"fmt"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/rules"
)

func applyOneshotReasoningCompatibility(engine *rules.Engine, profile oneshot.RequestProfile, capability model.CapabilityResolution) (oneshot.RequestProfile, string) {
	if engine == nil || !capability.Supported() {
		return profile, ""
	}
	result, err := engine.EvalStrategy("oneshot", "oneshot_reasoning_compatibility", map[string]any{
		"provider": map[string]any{"id": capability.ProviderID},
		"model":    map[string]any{"id": capability.Model},
	})
	if err != nil {
		return profile, ""
	}
	profile.DisableReasoningForForcedTools = policyBool(result.Params["disable_reasoning_for_forced_tools"])
	if profile.DisableReasoningForForcedTools && profile.RequireTool {
		return profile, fmt.Sprintf("Reasoning disabled for forced tool selection on %s (%s).", capability.Model, capability.ProviderID)
	}
	return profile, ""
}
