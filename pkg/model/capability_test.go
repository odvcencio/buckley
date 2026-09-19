package model

import (
	"encoding/json"
	"testing"

	"m31labs.dev/buckley/pkg/config"
)

func TestModelInfoSupportedParametersPresenceStates(t *testing.T) {
	for _, tt := range []struct {
		name         string
		body         string
		wantState    CapabilityState
		wantParams   []string
		wantComplete bool
	}{
		{
			name:      "omitted is unknown",
			body:      `{"id":"future/model","context_length":128000}`,
			wantState: CapabilityUnknown,
		},
		{
			name:      "null is unknown",
			body:      `{"id":"future/model","supported_parameters":null,"context_length":128000}`,
			wantState: CapabilityUnknown,
		},
		{
			name:         "explicit empty is not advertised",
			body:         `{"id":"future/model","supported_parameters":[],"context_length":128000}`,
			wantState:    CapabilityNotAdvertised,
			wantComplete: true,
		},
		{
			name:         "explicit list supports listed parameter",
			body:         `{"id":"future/model","supported_parameters":["reasoning_effort"],"context_length":128000}`,
			wantState:    CapabilitySupported,
			wantParams:   []string{"reasoning_effort"},
			wantComplete: true,
		},
		{
			name:      "future object shape is unknown",
			body:      `{"id":"future/model","supported_parameters":{"reasoning":{"supported":true}},"context_length":128000}`,
			wantState: CapabilityUnknown,
		},
		{
			name:      "mixed array shape is unknown",
			body:      `{"id":"future/model","supported_parameters":["tools",7],"context_length":128000}`,
			wantState: CapabilityUnknown,
		},
		{
			name:      "null-only array shape is unknown",
			body:      `{"id":"future/model","supported_parameters":[null],"context_length":128000}`,
			wantState: CapabilityUnknown,
		},
		{
			name:      "reasoning plus null shape is unknown",
			body:      `{"id":"future/model","supported_parameters":["reasoning_effort",null],"context_length":128000}`,
			wantState: CapabilityUnknown,
		},
		{
			name:      "malformed shape with true marker is unknown",
			body:      `{"id":"future/model","supported_parameters":["reasoning_effort",null],"x_buckley_supported_parameters_complete":true,"context_length":128000}`,
			wantState: CapabilityUnknown,
		},
		{
			name:      "malformed shape with evidence marker is unknown",
			body:      `{"id":"future/model","supported_parameters":["reasoning_effort",null],"x_buckley_supported_parameter_evidence":["reasoning_effort"],"context_length":128000}`,
			wantState: CapabilityUnknown,
		},
		{
			name:         "marker-only complete remains not advertised",
			body:         `{"id":"future/model","x_buckley_supported_parameters_complete":true,"context_length":128000}`,
			wantState:    CapabilityNotAdvertised,
			wantComplete: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var info ModelInfo
			if err := json.Unmarshal([]byte(tt.body), &info); err != nil {
				t.Fatalf("Unmarshal ModelInfo: %v", err)
			}
			if info.ID != "future/model" || info.ContextLength != 128000 {
				t.Fatalf("usable fields not preserved: %+v", info)
			}
			if got := info.resolveParameterCapability("reasoning_effort"); got != tt.wantState {
				t.Fatalf("reasoning_effort state = %s, want %s", got, tt.wantState)
			}
			if info.supportedParametersComplete != tt.wantComplete {
				t.Fatalf("complete = %v, want %v", info.supportedParametersComplete, tt.wantComplete)
			}
			for _, param := range tt.wantParams {
				if !containsString(info.SupportedParameters, param) {
					t.Fatalf("supported parameters = %v, missing %q", info.SupportedParameters, param)
				}
			}
		})
	}
}

func TestModelInfoJSONRoundTripPreservesPartialParameterEvidence(t *testing.T) {
	info := ModelInfo{ID: "future/model", SupportedParameters: []string{"temperature"}}
	info.markSupportedParameterEvidence("tools", "functions", "temperature")

	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("Marshal ModelInfo: %v", err)
	}
	var first ModelInfo
	if err := json.Unmarshal(data, &first); err != nil {
		t.Fatalf("first Unmarshal ModelInfo: %v", err)
	}
	data, err = json.Marshal(first)
	if err != nil {
		t.Fatalf("Marshal first ModelInfo: %v", err)
	}
	var second ModelInfo
	if err := json.Unmarshal(data, &second); err != nil {
		t.Fatalf("second Unmarshal ModelInfo: %v", err)
	}

	if got := second.resolveParameterCapability("temperature"); got != CapabilitySupported {
		t.Fatalf("temperature state = %s, want supported", got)
	}
	if got := second.resolveParameterCapability("tools"); got != CapabilityNotAdvertised {
		t.Fatalf("tools state = %s, want not_advertised", got)
	}
	if got := second.resolveParameterCapability("reasoning"); got != CapabilityUnknown {
		t.Fatalf("reasoning state = %s, want unknown", got)
	}
}

func TestModelInfoMalformedInternalMarkersDoNotGrantCapability(t *testing.T) {
	var info ModelInfo
	if err := json.Unmarshal([]byte(`{
	  "id": "future/model",
	  "context_length": 128000,
	  "x_buckley_supported_parameters_complete": "true",
	  "x_buckley_supported_parameter_evidence": {"reasoning": true}
	}`), &info); err != nil {
		t.Fatalf("Unmarshal ModelInfo: %v", err)
	}
	if got := info.resolveParameterCapability("reasoning"); got != CapabilityUnknown {
		t.Fatalf("reasoning state = %s, want unknown", got)
	}
}

func TestManagerResolveReasoningCapabilityUnknownFutureModelStillRoutes(t *testing.T) {
	prov := &stubProvider{id: "p1"}
	mgr := &Manager{
		config: &config.Config{
			Models: config.ModelConfig{DefaultProvider: "p1", FallbackChains: map[string][]string{}},
		},
		providers:     map[string]Provider{"p1": prov},
		providerOrder: []string{"p1"},
		catalog:       map[string]ModelInfo{},
	}

	route, err := mgr.ResolveModelRoute("future-16-alpha")
	if err != nil {
		t.Fatalf("ResolveModelRoute: %v", err)
	}
	if route.ProviderID != "p1" || route.SelectedModel != "future-16-alpha" {
		t.Fatalf("route = %+v, want provider p1 selected future-16-alpha", route)
	}
	capability := mgr.ResolveReasoningCapability("future-16-alpha")
	if capability.State != CapabilityUnknown {
		t.Fatalf("reasoning capability = %+v, want unknown", capability)
	}
	if mgr.SupportsReasoning("future-16-alpha") {
		t.Fatal("SupportsReasoning true for unknown metadata")
	}
}

func TestManagerResolveReasoningCapabilityUsesSelectedProviderMetadataOnly(t *testing.T) {
	const modelID = "vendor/shared"
	selectedInfo := ModelInfo{ID: modelID, SupportedParameters: []string{"reasoning_effort"}}
	selectedInfo.markSupportedParametersComplete()
	collisionInfo := ModelInfo{ID: modelID, SupportedParameters: []string{"tools"}}
	collisionInfo.markSupportedParametersComplete()
	selected := &stubProvider{id: "selected", catalog: ModelCatalog{Data: []ModelInfo{selectedInfo}}}
	other := &stubProvider{id: "other", catalog: ModelCatalog{Data: []ModelInfo{collisionInfo}}}
	mgr := &Manager{
		config: &config.Config{
			Models: config.ModelConfig{DefaultProvider: "selected", FallbackChains: map[string][]string{}},
		},
		providers:      map[string]Provider{"selected": selected, "other": other},
		providerOrder:  []string{"other", "selected"},
		catalog:        map[string]ModelInfo{modelID: collisionInfo},
		providerModels: map[string][]string{"selected": {modelID}, "other": {modelID}},
		modelProviders: map[string]string{modelID: "other"},
	}

	if got := mgr.ResolveReasoningCapability(modelID); got.State != CapabilitySupported || got.ProviderID != "selected" {
		t.Fatalf("reasoning capability = %+v, want selected provider support", got)
	}
	if got := mgr.ResolveParameterCapability(modelID, "tools"); got.State != CapabilityNotAdvertised || got.ProviderID != "selected" {
		t.Fatalf("tools capability = %+v, want selected provider not_advertised", got)
	}
}

func TestManagerResolveParameterCapabilityForRouteUsesSelectedProviderMetadata(t *testing.T) {
	const modelID = "vendor/shared"
	selectedInfo := ModelInfo{ID: modelID, SupportedParameters: []string{"tools"}}
	selectedInfo.markSupportedParametersComplete()
	collisionInfo := ModelInfo{ID: modelID, SupportedParameters: []string{}}
	collisionInfo.markSupportedParametersComplete()
	selected := &stubProvider{id: "selected", catalog: ModelCatalog{Data: []ModelInfo{selectedInfo}}}
	other := &stubProvider{id: "other", catalog: ModelCatalog{Data: []ModelInfo{collisionInfo}}}
	mgr := &Manager{
		config: &config.Config{
			Models:    config.ModelConfig{DefaultProvider: "selected", FallbackChains: map[string][]string{}},
			Providers: config.ProviderConfig{ModelRouting: map[string]string{}},
		},
		providers:      map[string]Provider{"selected": selected, "other": other},
		providerOrder:  []string{"other", "selected"},
		catalog:        map[string]ModelInfo{modelID: collisionInfo},
		providerModels: map[string][]string{"selected": {modelID}, "other": {modelID}},
		modelProviders: map[string]string{modelID: "other"},
	}
	route := ModelRoute{RequestedModel: "alias/model", SelectedModel: modelID, ProviderID: "selected"}

	if got := mgr.ResolveParameterCapabilityForRoute(route, "tools"); got.State != CapabilitySupported || got.ProviderID != "selected" {
		t.Fatalf("route tools capability = %+v, want selected provider support", got)
	}
	if got := mgr.ResolveParameterCapabilityForRoute(route, "functions"); got.State != CapabilityNotAdvertised || got.ProviderID != "selected" {
		t.Fatalf("route functions capability = %+v, want selected provider not_advertised", got)
	}
}

func TestResolveReasoningCapabilityRequiresBothAliasesNotAdvertised(t *testing.T) {
	info := ModelInfo{ID: "future/model"}
	info.markSupportedParameterEvidence("reasoning")
	mgr := &Manager{
		config:  &config.Config{},
		catalog: map[string]ModelInfo{"future/model": info},
	}

	if got := mgr.ResolveReasoningCapability("future/model"); got.State != CapabilityUnknown {
		t.Fatalf("one negative alias capability = %+v, want unknown", got)
	}

	info.markSupportedParameterEvidence("reasoning_effort")
	mgr.catalog["future/model"] = info
	if got := mgr.ResolveReasoningCapability("future/model"); got.State != CapabilityNotAdvertised {
		t.Fatalf("both negative aliases capability = %+v, want not_advertised", got)
	}
}

func TestManagerOfferToolsOnlyFalseWhenBothToolAliasesNotAdvertised(t *testing.T) {
	knownNoTools := ModelInfo{ID: "openai/o1-mini", SupportedParameters: []string{}}
	knownNoTools.markSupportedParametersComplete()
	knownTools := ModelInfo{ID: "openai/gpt-tools", SupportedParameters: []string{"tools"}}
	knownTools.markSupportedParametersComplete()
	legacyFunctions := ModelInfo{ID: "openai/gpt-functions", SupportedParameters: []string{"functions"}}
	legacyFunctions.markSupportedParametersComplete()
	partialNegative := ModelInfo{ID: "openai/partial-negative"}
	partialNegative.markSupportedParameterEvidence("tools")
	future := ModelInfo{ID: "openai/future"}

	mgr := &Manager{
		config:  &config.Config{},
		catalog: map[string]ModelInfo{},
	}
	for _, info := range []ModelInfo{knownNoTools, knownTools, legacyFunctions, partialNegative, future} {
		mgr.catalog[info.ID] = info
	}

	tests := []struct {
		modelID string
		want    bool
	}{
		{modelID: "openai/o1-mini", want: false},
		{modelID: "openai/gpt-tools", want: true},
		{modelID: "openai/gpt-functions", want: true},
		{modelID: "openai/partial-negative", want: true},
		{modelID: "openai/future", want: true},
		{modelID: "openai/unknown-not-in-catalog", want: true},
	}
	for _, tt := range tests {
		if got := mgr.OfferTools(tt.modelID); got != tt.want {
			t.Fatalf("OfferTools(%q) = %v, want %v", tt.modelID, got, tt.want)
		}
	}
}
