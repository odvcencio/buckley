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
		// Name the effort setting: a caller who passes --effort high reads
		// "reasoning disabled" as a note, not as "your flag did nothing".
		return profile, fmt.Sprintf("Reasoning disabled for forced tool selection on %s (%s): this model cannot reason and answer a required tool call in one request, so --effort and BUCKLEY_EFFORT_* have no effect on this command.", capability.Model, capability.ProviderID)
	}
	return profile, ""
}
