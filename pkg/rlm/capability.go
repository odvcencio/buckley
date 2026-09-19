package rlm

import "m31labs.dev/buckley/pkg/model"

func catalogConfirmedToollessModel(manager *model.Manager, modelID string) (toolless bool) {
	if manager == nil {
		return false
	}
	defer func() {
		if recover() != nil {
			toolless = false
		}
	}()
	tools := manager.ResolveParameterCapability(modelID, "tools")
	functions := manager.ResolveParameterCapability(modelID, "functions")
	return tools.State == model.CapabilityNotAdvertised &&
		functions.State == model.CapabilityNotAdvertised
}

func catalogConfirmedToollessRoute(manager *model.Manager, route model.ModelRoute) (toolless bool) {
	if manager == nil {
		return false
	}
	defer func() {
		if recover() != nil {
			toolless = false
		}
	}()
	tools := manager.ResolveParameterCapabilityForRoute(route, "tools")
	functions := manager.ResolveParameterCapabilityForRoute(route, "functions")
	return tools.State == model.CapabilityNotAdvertised &&
		functions.State == model.CapabilityNotAdvertised
}
