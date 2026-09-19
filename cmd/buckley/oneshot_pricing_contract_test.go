package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tools"
	"m31labs.dev/buckley/pkg/transparency"
)

func TestNewOneshotToolInvokerRecordsAuthoritativePricingContract(t *testing.T) {
	tests := []struct {
		name           string
		commandName    string
		modelID        string
		catalogJSON    string
		wantCost       float64
		wantUnknown    bool
		wantCatalogHit bool
	}{
		{
			name:           "commit known paid",
			commandName:    "commit",
			modelID:        "vendor/paid",
			catalogJSON:    `{"data":[{"id":"vendor/paid","pricing":{"prompt":"0.000003","completion":"0.000015"}}]}`,
			wantCost:       18,
			wantCatalogHit: true,
		},
		{
			name:           "pr known free",
			commandName:    "pr",
			modelID:        "vendor/free",
			catalogJSON:    `{"data":[{"id":"vendor/free","pricing":{"prompt":"0","completion":"0"}}]}`,
			wantCost:       0,
			wantCatalogHit: true,
		},
		{
			name:           "commit malformed partial pricing",
			commandName:    "commit",
			modelID:        "vendor/partial",
			catalogJSON:    `{"data":[{"id":"vendor/partial","pricing":{"prompt":null,"completion":"0"}}]}`,
			wantUnknown:    true,
			wantCatalogHit: true,
		},
		{
			name:        "pr missing metadata lookup",
			commandName: "pr",
			modelID:     "future-model",
			catalogJSON: `{"data":[]}`,
			wantUnknown: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var modelCalls atomic.Int32
			mgr := newOneshotPricingTestManager(t, tt.catalogJSON, tt.modelID, &modelCalls)
			if info, err := mgr.GetModelInfo(tt.modelID); tt.wantCatalogHit && (err != nil || info == nil) {
				t.Fatalf("GetModelInfo(%s) = %#v, %v; want catalog metadata", tt.modelID, info, err)
			} else if !tt.wantCatalogHit && err == nil {
				t.Fatalf("GetModelInfo(%s) unexpectedly succeeded: %#v", tt.modelID, info)
			}

			ledger := transparency.NewCostLedger()
			invoker, err := newOneshotToolInvoker(oneshotBackendAPI, tt.commandName, tt.modelID, pricingTestConfig(), mgr, ledger)
			if err != nil {
				t.Fatalf("newOneshotToolInvoker: %v", err)
			}
			result, trace, err := invoker.Invoke(context.Background(), "system", "user", pricingTestTool(), nil)
			if err != nil {
				t.Fatalf("Invoke: %v", err)
			}
			if modelCalls.Load() != 1 {
				t.Fatalf("model calls = %d, want one local HTTP dispatch", modelCalls.Load())
			}
			if result == nil || result.ToolCall == nil {
				t.Fatalf("result = %#v, want structured tool result", result)
			}
			if trace == nil || result.Trace != trace {
				t.Fatalf("trace/result trace = %#v/%#v, want shared trace", trace, result.Trace)
			}
			wantTokens := transparency.TokenUsage{Input: 1000000, Output: 1000000, ReportedTotal: 2000000, UsageEvidencePresent: true}
			if trace.Tokens != wantTokens {
				t.Fatalf("trace tokens = %+v, want exact local provider usage", trace.Tokens)
			}
			if trace.Cost != tt.wantCost || trace.CostUnknown != tt.wantUnknown {
				t.Fatalf("trace cost = %v unknown=%v, want %v/%v", trace.Cost, trace.CostUnknown, tt.wantCost, tt.wantUnknown)
			}
			summary := ledger.Summary()
			if summary.InvocationCount != 1 || summary.SessionCost != tt.wantCost || summary.SessionCostUnknown != tt.wantUnknown || summary.SessionTokens != wantTokens {
				t.Fatalf("ledger summary = %+v, want one retained invocation cost %v unknown %v", summary, tt.wantCost, tt.wantUnknown)
			}
			wantResponseID := "chatcmpl-" + strings.ReplaceAll(tt.modelID, "/", "-")
			if got := trace.ModelExecutions; len(got) != 1 || got[0].RequestedModel != tt.modelID || got[0].SelectedModel != tt.modelID || got[0].ProviderID != "openrouter" || got[0].ResponseModel != tt.modelID+"-wire" || got[0].ResponseID != wantResponseID {
				t.Fatalf("trace model executions = %+v, want routed local response identity", got)
			}
		})
	}
}

func TestResolveOneshotToolPricingMarksUnknownWithoutManager(t *testing.T) {
	pricing, unknown := resolveOneshotToolPricing(model.ModelRoute{ProviderID: "openrouter", SelectedModel: "future-model"}, nil)
	if pricing != (transparency.ModelPricing{}) || !unknown {
		t.Fatalf("resolveOneshotToolPricing without manager = %+v/%v, want zero known subtotal plus unknown", pricing, unknown)
	}

	pricing, unknown = resolveOneshotToolPricing(model.ModelRoute{ProviderID: "codex", SelectedModel: "codex/gpt-5.6-sol"}, nil)
	if pricing != (transparency.ModelPricing{}) || unknown {
		t.Fatalf("resolveOneshotToolPricing for codex = %+v/%v, want known zero subscription cost", pricing, unknown)
	}
}

func TestNewOneshotToolInvokerPricesAuthoritativeRouteWithoutExtraResolution(t *testing.T) {
	var (
		hookCalls  atomic.Int32
		modelCalls atomic.Int32
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/models":
			_, _ = fmt.Fprint(w, `{"data":[
				{"id":"route-alias","pricing":{"prompt":"0.000099","completion":"0.000099"}},
				{"id":"selected-priced","pricing":{"prompt":"0.000003","completion":"0.000015"}}
			]}`)
		case "/chat/completions":
			var request struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode provider request: %v", err)
				return
			}
			if request.Model != "selected-priced" {
				t.Errorf("provider model = %q, want selected route model", request.Model)
			}
			modelCalls.Add(1)
			_, _ = fmt.Fprint(w, `{
				"id":"chatcmpl-route-priced",
				"model":"selected-priced",
				"choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call-route-priced","type":"function","function":{"name":"record_pricing","arguments":"{\"answer\":\"ok\"}"}}]},"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":1000000,"completion_tokens":1000000,"total_tokens":2000000}
			}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	cfg := config.DefaultConfig()
	cfg.Models.DefaultProvider = "openai_compatible"
	cfg.Providers.OpenRouter.Enabled = false
	cfg.Providers.OpenAI.Enabled = false
	cfg.Providers.OpenAICompatible.Enabled = true
	cfg.Providers.OpenAICompatible.APIKey = "test-key"
	cfg.Providers.OpenAICompatible.BaseURL = server.URL
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	mgr.RoutingHooks().Register(func(decision *model.RoutingDecision) *model.RoutingDecision {
		if decision != nil && decision.RequestedModel == "route-alias" {
			hookCalls.Add(1)
			decision.SelectedModel = "openai_compatible/selected-priced"
		}
		return decision
	})

	invoker, err := newOneshotToolInvoker(oneshotBackendAPI, "commit", "route-alias", cfg, mgr, transparency.NewCostLedger())
	if err != nil {
		t.Fatalf("newOneshotToolInvoker: %v", err)
	}
	if got := hookCalls.Load(); got != 1 {
		t.Fatalf("route hook calls during construction = %d, want one authoritative decision", got)
	}

	_, trace, err := invoker.Invoke(context.Background(), "system", "user", pricingTestTool(), nil)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if got := hookCalls.Load(); got != 2 {
		t.Fatalf("route hook calls after dispatch = %d, want construction plus one pre-dispatch recheck", got)
	}
	if got := modelCalls.Load(); got != 1 {
		t.Fatalf("provider model calls = %d, want one route-bound dispatch", got)
	}
	if trace == nil || trace.Cost != 18 || trace.CostUnknown {
		t.Fatalf("trace pricing = %#v, want selected-route $18 known cost", trace)
	}
}

func TestResolveOneshotToolPricingUsesSelectedRouteCatalog(t *testing.T) {
	tests := []struct {
		name        string
		selected    string
		selectedRow string
		wantPricing transparency.ModelPricing
		wantUnknown bool
	}{
		{
			name:        "paid selected route ignores free requested alias",
			selected:    "selected-paid",
			selectedRow: `{"id":"selected-paid","pricing":{"prompt":"0.000003","completion":"0.000015"}}`,
			wantPricing: transparency.ModelPricing{InputPerMillion: 3, OutputPerMillion: 15},
		},
		{
			name:        "free selected route remains known",
			selected:    "selected-free",
			selectedRow: `{"id":"selected-free","pricing":{"prompt":"0","completion":"0"}}`,
			wantPricing: transparency.ModelPricing{},
		},
		{
			name:        "missing selected route stays unknown despite priced alias",
			selected:    "selected-unknown",
			wantUnknown: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var hookCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/models" {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				rows := `{"id":"route-alias","pricing":{"prompt":"0","completion":"0"}}`
				if tt.selectedRow != "" {
					rows += "," + tt.selectedRow
				}
				_, _ = fmt.Fprintf(w, `{"data":[%s]}`, rows)
			}))
			t.Cleanup(server.Close)

			cfg := config.DefaultConfig()
			cfg.Models.DefaultProvider = "openai_compatible"
			cfg.Providers.OpenRouter.Enabled = false
			cfg.Providers.OpenAI.Enabled = false
			cfg.Providers.OpenAICompatible.Enabled = true
			cfg.Providers.OpenAICompatible.APIKey = "test-key"
			cfg.Providers.OpenAICompatible.BaseURL = server.URL
			mgr, err := model.NewManager(cfg)
			if err != nil {
				t.Fatalf("NewManager: %v", err)
			}
			mgr.RoutingHooks().Register(func(decision *model.RoutingDecision) *model.RoutingDecision {
				if decision != nil && decision.RequestedModel == "route-alias" {
					hookCalls.Add(1)
					decision.SelectedModel = "openai_compatible/" + tt.selected
				}
				return decision
			})

			route, err := mgr.ResolveModelRoute("route-alias")
			if err != nil {
				t.Fatalf("ResolveModelRoute: %v", err)
			}
			pricing, unknown := resolveOneshotToolPricing(route, mgr)
			if pricing != tt.wantPricing || unknown != tt.wantUnknown {
				t.Fatalf("route pricing = %+v/%v, want %+v/%v", pricing, unknown, tt.wantPricing, tt.wantUnknown)
			}
			if got := hookCalls.Load(); got != 1 {
				t.Fatalf("route hook calls = %d, want only the explicit route decision", got)
			}
		})
	}
}

func newOneshotPricingTestManager(t *testing.T, catalogJSON, modelID string, modelCalls *atomic.Int32) *model.Manager {
	t.Helper()
	if !json.Valid([]byte(catalogJSON)) {
		t.Fatalf("invalid catalog JSON")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/models":
			fmt.Fprint(w, catalogJSON)
		case "/chat/completions":
			modelCalls.Add(1)
			fmt.Fprintf(w, `{
				"id":%q,
				"model":%q,
				"choices":[{
					"index":0,
					"message":{
						"role":"assistant",
						"tool_calls":[{
							"id":"call-pricing",
							"type":"function",
							"function":{"name":"record_pricing","arguments":"{\"answer\":\"ok\"}"}
						}]
					},
					"finish_reason":"tool_calls"
				}],
				"usage":{"prompt_tokens":1000000,"completion_tokens":1000000,"total_tokens":2000000}
			}`, "chatcmpl-"+strings.ReplaceAll(modelID, "/", "-"), modelID+"-wire")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	cfg := pricingTestConfig()
	cfg.Providers.OpenRouter.BaseURL = server.URL
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return mgr
}

func pricingTestConfig() *config.Config {
	cfg := config.DefaultConfig()
	cfg.Providers.OpenRouter.Enabled = true
	cfg.Providers.OpenRouter.APIKey = "test-key"
	cfg.Providers.OpenAI.Enabled = false
	cfg.Models.DefaultProvider = "openrouter"
	return cfg
}

func pricingTestTool() tools.Definition {
	return tools.Definition{
		Name:        "record_pricing",
		Description: "record pricing fixture",
		Parameters: tools.ObjectSchema(map[string]tools.Property{
			"answer": tools.StringProperty("answer"),
		}, "answer"),
	}
}
