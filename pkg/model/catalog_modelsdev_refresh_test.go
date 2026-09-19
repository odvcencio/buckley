package model

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"m31labs.dev/buckley/pkg/config"
)

func fetchModelsDevCatalogFixture(t *testing.T, body string) ModelsDevCatalog {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	catalog, err := FetchModelsDevCatalog(context.Background(), server.Client(), server.URL)
	if err != nil {
		t.Fatalf("FetchModelsDevCatalog: %v", err)
	}
	return catalog
}

func hasModelParameter(params []string, want string) bool {
	for _, param := range params {
		if param == want {
			return true
		}
	}
	return false
}

func requireModelParameter(t *testing.T, params []string, want string) {
	t.Helper()
	if !hasModelParameter(params, want) {
		t.Fatalf("supported parameters = %v, missing %q", params, want)
	}
}

func requireNoModelParameter(t *testing.T, params []string, want string) {
	t.Helper()
	if hasModelParameter(params, want) {
		t.Fatalf("supported parameters = %v, unexpectedly contains %q", params, want)
	}
}

func TestMergeModelsDevCatalog_PartialCapabilityRefreshUsesJSONPresence(t *testing.T) {
	catalog := fetchModelsDevCatalogFixture(t, `{
	  "openai": {
	    "models": {
	      "gpt-partial": {
	        "tool_call": false,
	        "reasoning": null,
	        "temperature": true
	      }
	    }
	  }
	}`)
	base := map[string]ModelInfo{
		"openai/gpt-partial": {
			ID: "openai/gpt-partial",
			SupportedParameters: []string{
				"tools", "functions", "reasoning", "reasoning_effort",
				"parallel_tool_calls", "tool_choice", "vendor_future",
			},
		},
	}

	got := MergeModelsDevCatalog(base, catalog)["openai/gpt-partial"].SupportedParameters
	requireNoModelParameter(t, got, "tools")
	requireNoModelParameter(t, got, "functions")
	requireModelParameter(t, got, "reasoning")
	requireModelParameter(t, got, "reasoning_effort")
	requireModelParameter(t, got, "parallel_tool_calls")
	requireModelParameter(t, got, "tool_choice")
	requireModelParameter(t, got, "vendor_future")
	requireModelParameter(t, got, "temperature")
}

func TestMergeModelsDevCatalog_ExplicitFalseRevokesStaleAliases(t *testing.T) {
	catalog := fetchModelsDevCatalogFixture(t, `{
	  "openai": {
	    "models": {
	      "gpt-revoked": {
	        "tool_call": false,
	        "reasoning": false,
	        "attachment": false,
	        "temperature": false,
	        "structured_output": false
	      }
	    }
	  }
	}`)
	base := map[string]ModelInfo{
		"openai/gpt-revoked": {
			ID: "openai/gpt-revoked",
			SupportedParameters: []string{
				"tools", "functions", "reasoning", "reasoning_effort",
				"attachment", "temperature", "structured_output",
				"parallel_tool_calls", "tool_choice", "vendor_future",
			},
		},
	}

	got := MergeModelsDevCatalog(base, catalog)["openai/gpt-revoked"].SupportedParameters
	for _, param := range []string{"tools", "functions", "reasoning", "reasoning_effort", "attachment", "temperature", "structured_output"} {
		requireNoModelParameter(t, got, param)
	}
	for _, param := range []string{"parallel_tool_calls", "tool_choice", "vendor_future"} {
		requireModelParameter(t, got, param)
	}
}

func TestMergeModelsDevCatalog_MissingAndNullCapabilitiesAreNotAuthoritativeFalse(t *testing.T) {
	catalog := fetchModelsDevCatalogFixture(t, `{
	  "openai": {
	    "models": {
	      "gpt-unknown": {
	        "tool_call": null
	      }
	    }
	  }
	}`)
	base := map[string]ModelInfo{
		"openai/gpt-unknown": {
			ID:                  "openai/gpt-unknown",
			SupportedParameters: []string{"tools", "functions", "reasoning", "reasoning_effort", "vendor_future"},
		},
	}

	got := MergeModelsDevCatalog(base, catalog)["openai/gpt-unknown"].SupportedParameters
	for _, param := range []string{"tools", "functions", "reasoning", "reasoning_effort", "vendor_future"} {
		requireModelParameter(t, got, param)
	}
}

func TestMergeModelsDevCatalog_GoLiteralTrueRetainsLegacyPositiveMeaning(t *testing.T) {
	merged := MergeModelsDevCatalog(nil, ModelsDevCatalog{
		"openai": {Models: map[string]ModelsDevModel{
			"gpt-literal": {ToolCall: true, Reasoning: true, Attachment: true, Temperature: true, StructuredOutput: true},
		}},
	})

	got := merged["openai/gpt-literal"].SupportedParameters
	for _, param := range []string{"tools", "reasoning", "attachment", "temperature", "structured_output"} {
		requireModelParameter(t, got, param)
	}
}

func TestMergeModelsDevCatalog_DecodedPresenceUsesCurrentPublicBoolValue(t *testing.T) {
	catalog := fetchModelsDevCatalogFixture(t, `{
	  "openai": {"models": {"gpt-edited": {"tool_call": false}}}
	}`)
	provider := catalog["openai"]
	model := provider.Models["gpt-edited"]
	model.ToolCall = true
	provider.Models["gpt-edited"] = model
	catalog["openai"] = provider

	got := MergeModelsDevCatalog(nil, catalog)["openai/gpt-edited"].SupportedParameters
	requireModelParameter(t, got, "tools")
}

func TestMergeModelsDevCatalog_DoesNotAliasBaseSupportedParameterSlice(t *testing.T) {
	baseParams := []string{"tools", "vendor_future"}
	baseInfo := ModelInfo{ID: "openai/gpt-alias", SupportedParameters: baseParams}
	baseInfo.markSupportedParameterEvidence("reasoning")
	base := map[string]ModelInfo{
		"openai/gpt-alias": baseInfo,
	}
	catalog := fetchModelsDevCatalogFixture(t, `{
	  "openai": {"models": {"gpt-alias": {"tool_call": false}}}
	}`)

	merged := MergeModelsDevCatalog(base, catalog)
	got := merged["openai/gpt-alias"].SupportedParameters
	requireNoModelParameter(t, got, "tools")
	requireModelParameter(t, got, "vendor_future")
	got[0] = "mutated"
	if base["openai/gpt-alias"].SupportedParameters[0] != "tools" ||
		base["openai/gpt-alias"].SupportedParameters[1] != "vendor_future" {
		t.Fatalf("base supported parameters mutated through merged slice: %v", base["openai/gpt-alias"].SupportedParameters)
	}
	if _, ok := base["openai/gpt-alias"].supportedParameterEvidence["tools"]; ok {
		t.Fatalf("base supported parameter evidence mutated: %v", base["openai/gpt-alias"].supportedParameterEvidenceList())
	}
}

func TestModelsDevCatalogRefresh_CacheRoundTripAndManagerConsumers(t *testing.T) {
	catalog := fetchModelsDevCatalogFixture(t, `{
	  "openai": {
	    "models": {
	      "gpt-cache": {
	        "tool_call": false,
	        "reasoning": false
	      }
	    }
	  }
	}`)
	base := map[string]ModelInfo{
		"openai/gpt-cache": {
			ID:                  "openai/gpt-cache",
			SupportedParameters: []string{"tools", "functions", "reasoning", "reasoning_effort", "vendor_future"},
		},
	}
	merged := MergeModelsDevCatalog(base, catalog)
	cachePath := filepath.Join(t.TempDir(), "model_catalog.json")
	if err := SaveCatalogCache(cachePath, merged); err != nil {
		t.Fatalf("SaveCatalogCache: %v", err)
	}
	loaded, err := LoadCatalogCache(cachePath)
	if err != nil {
		t.Fatalf("LoadCatalogCache: %v", err)
	}
	mgr := &Manager{
		config:  &config.Config{},
		catalog: loaded,
	}
	if mgr.SupportsTools("openai/gpt-cache") {
		t.Fatal("SupportsTools true after explicit models.dev tool_call=false refresh")
	}
	if mgr.SupportsReasoning("openai/gpt-cache") {
		t.Fatal("SupportsReasoning true after explicit models.dev reasoning=false refresh")
	}
	if got := mgr.ResolveParameterCapability("openai/gpt-cache", "tools"); got.State != CapabilityNotAdvertised {
		t.Fatalf("tools capability = %+v, want not_advertised", got)
	}
	if got := mgr.ResolveParameterCapability("openai/gpt-cache", "vendor_future"); got.State != CapabilitySupported {
		t.Fatalf("vendor_future capability = %+v, want supported", got)
	}
	if got := mgr.ResolveParameterCapability("openai/gpt-cache", "temperature"); got.State != CapabilityUnknown {
		t.Fatalf("temperature capability = %+v, want unknown", got)
	}
	requireModelParameter(t, loaded["openai/gpt-cache"].SupportedParameters, "vendor_future")
}

func TestModelsDevCatalogRefresh_PartialEvidenceSurvivesSecondCacheDecode(t *testing.T) {
	catalog := fetchModelsDevCatalogFixture(t, `{
	  "openai": {"models": {"gpt-cache": {"tool_call": false, "temperature": true}}}
	}`)
	base := map[string]ModelInfo{
		"openai/gpt-cache": {
			ID:                  "openai/gpt-cache",
			SupportedParameters: []string{"tools", "functions", "reasoning", "reasoning_effort", "vendor_future"},
		},
	}
	first := MergeModelsDevCatalog(base, catalog)
	cachePath := filepath.Join(t.TempDir(), "model_catalog.json")
	if err := SaveCatalogCache(cachePath, first); err != nil {
		t.Fatalf("SaveCatalogCache first: %v", err)
	}
	second, err := LoadCatalogCache(cachePath)
	if err != nil {
		t.Fatalf("LoadCatalogCache second: %v", err)
	}
	if err := SaveCatalogCache(cachePath, second); err != nil {
		t.Fatalf("SaveCatalogCache second: %v", err)
	}
	third, err := LoadCatalogCache(cachePath)
	if err != nil {
		t.Fatalf("LoadCatalogCache third: %v", err)
	}
	info := third["openai/gpt-cache"]
	if got := info.resolveParameterCapability("tools"); got != CapabilityNotAdvertised {
		t.Fatalf("tools state = %s, want not_advertised", got)
	}
	if got := info.resolveParameterCapability("temperature"); got != CapabilitySupported {
		t.Fatalf("temperature state = %s, want supported", got)
	}
	if got := info.resolveParameterCapability("reasoning"); got != CapabilitySupported {
		t.Fatalf("reasoning state = %s, want supported from preserved stale positive", got)
	}
	if got := info.resolveParameterCapability("parallel_tool_calls"); got != CapabilityUnknown {
		t.Fatalf("parallel_tool_calls state = %s, want unknown for partial metadata", got)
	}
}

func TestFetchModelsDevCatalog_MalformedCapabilityBoolFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"openai":{"models":{"gpt-bad":{"tool_call":"false"}}}}`))
	}))
	defer server.Close()

	if _, err := FetchModelsDevCatalog(context.Background(), server.Client(), server.URL); err == nil {
		t.Fatal("expected malformed capability bool to fail decoding")
	}
}
