package model

import (
	"fmt"
	"sort"
	"strings"
)

// CapabilityState reports provider metadata evidence for an optional request
// capability. Unknown stays disabled; it is distinct from a provider not
// advertising the parameter in authoritative metadata.
type CapabilityState string

const (
	CapabilitySupported     CapabilityState = "supported"
	CapabilityNotAdvertised CapabilityState = "not_advertised"
	CapabilityUnknown       CapabilityState = "unknown"
)

// CapabilityResolution is a provider-neutral capability probe result.
type CapabilityResolution struct {
	Model      string
	ProviderID string
	Capability string
	State      CapabilityState
	Source     string
}

func (r CapabilityResolution) Supported() bool {
	return r.State == CapabilitySupported
}

// OfferTools reports whether Buckley should include tool schemas for a model.
// It is an eligibility decision, not a guarantee that the upstream provider
// will accept or honor tool calls. Only conclusive catalog evidence that both
// modern tools and legacy functions are absent makes a model ineligible;
// unknown or future metadata remains eligible so newly capable models do not
// silently lose tools.
func (m *Manager) OfferTools(modelID string) bool {
	if strings.TrimSpace(modelID) == "" {
		return false
	}
	if m == nil {
		return true
	}
	tools := m.ResolveParameterCapability(modelID, "tools")
	functions := m.ResolveParameterCapability(modelID, "functions")
	return shouldOfferToolsFromCapabilities(tools, functions)
}

// OfferToolsForRoute applies OfferTools semantics to an already-resolved
// provider route so provider-qualified collisions use the selected provider's
// metadata. The result is still only request eligibility; provider support can
// fail at dispatch time.
func (m *Manager) OfferToolsForRoute(route ModelRoute) bool {
	if strings.TrimSpace(route.SelectedModel) == "" {
		return false
	}
	if m == nil {
		return true
	}
	tools := m.ResolveParameterCapabilityForRoute(route, "tools")
	functions := m.ResolveParameterCapabilityForRoute(route, "functions")
	return shouldOfferToolsFromCapabilities(tools, functions)
}

// ToolsCatalogConfirmedUnavailableForRoute reports the exact route-safe
// negative case: provider metadata conclusively says neither modern tools nor
// legacy functions are advertised. This is narrower than "no tools are
// currently attached" and should only mark requests whose omission is a
// catalog-confirmed capability decision.
func (m *Manager) ToolsCatalogConfirmedUnavailableForRoute(route ModelRoute) bool {
	if strings.TrimSpace(route.SelectedModel) == "" || m == nil {
		return false
	}
	tools := m.ResolveParameterCapabilityForRoute(route, "tools")
	functions := m.ResolveParameterCapabilityForRoute(route, "functions")
	return tools.State == CapabilityNotAdvertised &&
		functions.State == CapabilityNotAdvertised
}

func shouldOfferToolsFromCapabilities(tools, functions CapabilityResolution) bool {
	return tools.State != CapabilityNotAdvertised ||
		functions.State != CapabilityNotAdvertised
}

func (m *ModelInfo) markSupportedParametersComplete() {
	if m == nil {
		return
	}
	m.supportedParametersComplete = true
}

func (m *ModelInfo) markSupportedParameterEvidence(params ...string) {
	m.setSupportedParameterEvidence(params...)
}

func (m *ModelInfo) setSupportedParameterEvidence(params ...string) {
	if m == nil || len(params) == 0 {
		return
	}
	for _, param := range params {
		param = strings.TrimSpace(param)
		if param == "" {
			continue
		}
		if m.supportedParameterEvidence == nil {
			m.supportedParameterEvidence = make(map[string]struct{}, len(params))
		}
		m.supportedParameterEvidence[param] = struct{}{}
	}
}

func (m ModelInfo) supportedParameterEvidenceList() []string {
	if len(m.supportedParameterEvidence) == 0 {
		return nil
	}
	out := make([]string, 0, len(m.supportedParameterEvidence))
	for param := range m.supportedParameterEvidence {
		out = append(out, param)
	}
	sort.Strings(out)
	return out
}

func (m ModelInfo) resolveParameterCapability(parameter string) CapabilityState {
	parameter = strings.TrimSpace(parameter)
	if parameter == "" {
		return CapabilityUnknown
	}
	for _, candidate := range m.SupportedParameters {
		if candidate == parameter {
			return CapabilitySupported
		}
	}
	if m.supportedParametersComplete {
		return CapabilityNotAdvertised
	}
	if _, ok := m.supportedParameterEvidence[parameter]; ok {
		return CapabilityNotAdvertised
	}
	return CapabilityUnknown
}

func (m *Manager) ResolveParameterCapability(modelID, parameter string) CapabilityResolution {
	modelID = strings.TrimSpace(modelID)
	parameter = strings.TrimSpace(parameter)
	result := CapabilityResolution{
		Model:      modelID,
		ProviderID: m.ProviderIDForModel(modelID),
		Capability: parameter,
		State:      CapabilityUnknown,
		Source:     "metadata_unavailable",
	}
	if m == nil || modelID == "" || parameter == "" {
		return result
	}
	info, providerID, source, err := m.modelCapabilityInfo(modelID)
	if err != nil {
		if providerID != "" {
			result.ProviderID = providerID
		}
		return result
	}
	result.Model = info.ID
	result.ProviderID = providerID
	result.Source = source
	result.State = info.resolveParameterCapability(parameter)
	return result
}

func (m *Manager) ResolveParameterCapabilityForRoute(route ModelRoute, parameter string) CapabilityResolution {
	modelID := strings.TrimSpace(route.SelectedModel)
	parameter = strings.TrimSpace(parameter)
	result := CapabilityResolution{
		Model:      modelID,
		ProviderID: strings.TrimSpace(route.ProviderID),
		Capability: parameter,
		State:      CapabilityUnknown,
		Source:     "metadata_unavailable",
	}
	if m == nil || modelID == "" || parameter == "" || result.ProviderID == "" {
		return result
	}
	info, source, err := m.modelCapabilityInfoForRoute(route)
	if err != nil {
		return result
	}
	result.Model = info.ID
	result.Source = source
	result.State = info.resolveParameterCapability(parameter)
	return result
}

func (m *Manager) ResolveReasoningCapability(modelID string) CapabilityResolution {
	result := CapabilityResolution{
		Model:      strings.TrimSpace(modelID),
		ProviderID: m.ProviderIDForModel(modelID),
		Capability: "reasoning",
		State:      CapabilityUnknown,
		Source:     "metadata_unavailable",
	}
	if m == nil || strings.TrimSpace(modelID) == "" {
		return result
	}
	info, providerID, source, err := m.modelCapabilityInfo(modelID)
	if err != nil {
		if providerID != "" {
			result.ProviderID = providerID
		}
		return result
	}
	result.Model = info.ID
	result.ProviderID = providerID
	result.Source = source
	for _, parameter := range []string{"reasoning", "reasoning_effort"} {
		if info.resolveParameterCapability(parameter) == CapabilitySupported {
			result.State = CapabilitySupported
			return result
		}
	}
	if info.resolveParameterCapability("reasoning") == CapabilityNotAdvertised &&
		info.resolveParameterCapability("reasoning_effort") == CapabilityNotAdvertised {
		result.State = CapabilityNotAdvertised
		return result
	}
	return result
}

func (m *Manager) ResolveReasoningCapabilityForRoute(route ModelRoute) CapabilityResolution {
	result := CapabilityResolution{
		Model:      strings.TrimSpace(route.SelectedModel),
		ProviderID: strings.TrimSpace(route.ProviderID),
		Capability: "reasoning",
		State:      CapabilityUnknown,
		Source:     "metadata_unavailable",
	}
	if m == nil || strings.TrimSpace(route.SelectedModel) == "" || result.ProviderID == "" {
		return result
	}
	info, source, err := m.modelCapabilityInfoForRoute(route)
	if err != nil {
		return result
	}
	result.Model = info.ID
	result.Source = source
	for _, parameter := range []string{"reasoning", "reasoning_effort"} {
		if info.resolveParameterCapability(parameter) == CapabilitySupported {
			result.State = CapabilitySupported
			return result
		}
	}
	if info.resolveParameterCapability("reasoning") == CapabilityNotAdvertised &&
		info.resolveParameterCapability("reasoning_effort") == CapabilityNotAdvertised {
		result.State = CapabilityNotAdvertised
	}
	return result
}

func (m *Manager) modelCapabilityInfo(modelID string) (ModelInfo, string, string, error) {
	info, providerID, source, err := m.lookupModelInfo(modelID)
	if err != nil || info == nil {
		return ModelInfo{}, providerID, "metadata_unavailable", fmt.Errorf("model metadata unavailable")
	}
	return *info, providerID, source, nil
}

func (m *Manager) modelCapabilityInfoForRoute(route ModelRoute) (ModelInfo, string, error) {
	providerID := strings.TrimSpace(route.ProviderID)
	modelID := strings.TrimSpace(route.SelectedModel)
	if m == nil || providerID == "" || modelID == "" {
		return ModelInfo{}, "metadata_unavailable", fmt.Errorf("model metadata unavailable")
	}
	provider := m.providers[providerID]
	if provider == nil {
		return ModelInfo{}, "metadata_unavailable", fmt.Errorf("provider not configured: %s", providerID)
	}
	candidates := m.modelInfoCandidatesForRoute(providerID, modelID)
	for _, candidate := range candidates {
		m.catalogMu.RLock()
		info, ok := m.catalog[candidate]
		owner := m.modelProviders[candidate]
		m.catalogMu.RUnlock()
		if ok && (owner == "" || owner == providerID) {
			return info, "catalog", nil
		}
	}
	if _, ok := provider.(*OpenRouterProvider); ok {
		return ModelInfo{}, "metadata_unavailable", fmt.Errorf("model metadata unavailable")
	}
	for _, candidate := range candidates {
		info, err := provider.GetModelInfo(candidate)
		if err == nil && info != nil {
			return *info, "provider", nil
		}
	}
	return ModelInfo{}, "metadata_unavailable", fmt.Errorf("model metadata unavailable")
}

func (m *Manager) modelInfoCandidatesForRoute(providerID, modelID string) []string {
	providerID = strings.TrimSpace(providerID)
	modelID = strings.TrimSpace(modelID)
	if providerID == "" || modelID == "" {
		return nil
	}

	seen := map[string]struct{}{}
	candidates := make([]string, 0, 3)
	add := func(candidate string) {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			return
		}
		if _, ok := seen[candidate]; ok {
			return
		}
		seen[candidate] = struct{}{}
		candidates = append(candidates, candidate)
	}

	add(modelID)
	if selectedProviderID, upstreamModelID, ok := m.explicitProviderQualifiedModel(modelID); ok {
		if selectedProviderID == providerID {
			add(upstreamModelID)
		}
		return candidates
	}
	if !strings.Contains(modelID, "/") {
		add(providerID + "/" + modelID)
	}

	return candidates
}
