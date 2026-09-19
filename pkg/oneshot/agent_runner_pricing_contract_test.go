package oneshot

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
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/transparency"
)

func TestAgentRunnerUnknownModelRecordsUnknownCostWhenPricingUnavailable(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"id":"chatcmpl-unknown-priced",
			"model":"future-model-wire",
			"choices":[{"index":0,"message":{"role":"assistant","content":"future model answered"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1000000,"completion_tokens":1000000,"total_tokens":2000000}
		}`)
	}))
	defer server.Close()

	mgr := newAgentRunnerUnknownPricingManager(t, server)
	if info, err := mgr.GetModelInfo("future-model"); err != nil || info == nil || info.PricingKnown {
		t.Fatalf("GetModelInfo(future-model) = %#v, %v; want usable model with unknown pricing", info, err)
	}
	ledger := transparency.NewCostLedger()
	runner := NewAgentRunner(AgentRunnerConfig{
		Models:   mgr,
		Registry: tool.NewEmptyRegistry(),
		ModelID:  "future-model",
		Ledger:   ledger,
	})

	result, err := runner.Run(context.Background(), "system", "task", nil, AgentExecutionOpts{MaxIterations: 1})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("model calls = %d, want successful unknown-model dispatch", calls.Load())
	}
	if result == nil || result.Trace == nil {
		t.Fatalf("result = %#v, want trace", result)
	}
	if result.ProviderID != "openai" {
		t.Fatalf("provider = %q, want default openai route", result.ProviderID)
	}
	if result.TokensUsed != 2000000 || result.InputTokens != 1000000 || result.OutputTokens != 1000000 {
		t.Fatalf("result tokens = total:%d input:%d output:%d, want exact provider usage retained", result.TokensUsed, result.InputTokens, result.OutputTokens)
	}
	wantUsage := transparency.TokenUsage{Input: 1000000, Output: 1000000, ReportedTotal: 2000000, UsageEvidencePresent: true}
	if result.Trace.Tokens != wantUsage {
		t.Fatalf("trace tokens = %+v, want exact provider usage retained", result.Trace.Tokens)
	}
	if got := result.ModelExecutions; len(got) != 1 || got[0].RequestedModel != "future-model" || got[0].SelectedModel != "future-model" || got[0].ProviderID != "openai" || got[0].ResponseModel != "future-model-wire" || got[0].ResponseID != "chatcmpl-unknown-priced" {
		t.Fatalf("result model executions = %+v, want exact response identity retained", got)
	}
	if got := result.Trace.ModelExecutions; len(got) != 1 || got[0].RequestedModel != "future-model" || got[0].SelectedModel != "future-model" || got[0].ProviderID != "openai" || got[0].ResponseModel != "future-model-wire" || got[0].ResponseID != "chatcmpl-unknown-priced" {
		t.Fatalf("trace model executions = %+v, want exact response identity retained", got)
	}
	if result.Trace.Cost != 0 || !result.Trace.CostUnknown {
		t.Fatalf("trace cost = %v unknown=%v, want unknown zero known-subtotal when pricing metadata is unavailable", result.Trace.Cost, result.Trace.CostUnknown)
	}
	summary := ledger.Summary()
	if summary.SessionCost != 0 || !summary.SessionCostUnknown || ledger.InvocationCount() != 1 {
		t.Fatalf("ledger summary/count = %+v/%d, want unknown cost entry with tokens retained", summary, ledger.InvocationCount())
	}
	if summary.SessionTokens != wantUsage {
		t.Fatalf("ledger tokens = %+v, want exact usage retained once", summary.SessionTokens)
	}
}

func TestAgentRunnerUnknownModelCostBudgetFailsBeforeDispatchWithoutFallbackTraceSpend(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	mgr := newAgentRunnerUnknownPricingManager(t, server)
	runner := NewAgentRunner(AgentRunnerConfig{
		Models:   mgr,
		Registry: tool.NewEmptyRegistry(),
		ModelID:  "future-model",
	})

	result, err := runner.Run(context.Background(), "system", "task", nil, AgentExecutionOpts{
		MaxIterations: 1,
		MaxCostUSD:    0.01,
	})
	if err == nil || !strings.Contains(err.Error(), "resolve model pricing for cost budget") {
		t.Fatalf("Run error = %v, want safe pricing-admission failure", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("model calls = %d, want no dispatch without authoritative pricing under cost ceiling", calls.Load())
	}
	if result == nil || !result.Incomplete {
		t.Fatalf("result = %#v, want incomplete diagnostic result from failed admission", result)
	}
	if result.Trace == nil || result.Trace.Cost != 0 || result.TokensUsed != 0 {
		t.Fatalf("trace/tokens = %#v/%d, want no fallback spend after pre-dispatch admission failure", result.Trace, result.TokensUsed)
	}
}

func TestAgentRunnerRecordsCatalogCostClassificationInTraceAndLedger(t *testing.T) {
	tests := []struct {
		name        string
		modelID     string
		catalogJSON string
		wantCost    float64
		wantUnknown bool
		responseID  string
	}{
		{
			name:        "known paid",
			modelID:     "vendor/paid",
			catalogJSON: `{"data":[{"id":"vendor/paid","pricing":{"prompt":"0.000003","completion":"0.000015"}}]}`,
			wantCost:    18,
			responseID:  "chatcmpl-paid",
		},
		{
			name:        "known free",
			modelID:     "vendor/free",
			catalogJSON: `{"data":[{"id":"vendor/free","pricing":{"prompt":"0","completion":"0"}}]}`,
			wantCost:    0,
			responseID:  "chatcmpl-free",
		},
		{
			name:        "unknown pricing",
			modelID:     "vendor/unknown",
			catalogJSON: `{"data":[{"id":"vendor/unknown","pricing":{"prompt":null,"completion":"0"}}]}`,
			wantCost:    0,
			wantUnknown: true,
			responseID:  "chatcmpl-unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			mgr := newAgentRunnerPricingCompletionManager(t, tt.catalogJSON, &calls)
			ledger := transparency.NewCostLedger()
			runner := NewAgentRunner(AgentRunnerConfig{
				Models:   mgr,
				Registry: tool.NewEmptyRegistry(),
				ModelID:  tt.modelID,
				Ledger:   ledger,
			})

			result, err := runner.Run(context.Background(), "system", "task", nil, AgentExecutionOpts{MaxIterations: 1})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("model calls = %d, want one dispatch", calls.Load())
			}
			if result == nil || result.Trace == nil {
				t.Fatalf("result = %#v, want trace", result)
			}
			if result.Trace.Cost != tt.wantCost || result.Trace.CostUnknown != tt.wantUnknown {
				t.Fatalf("trace cost = %v unknown=%v, want %v/%v", result.Trace.Cost, result.Trace.CostUnknown, tt.wantCost, tt.wantUnknown)
			}
			summary := ledger.Summary()
			wantUsage := transparency.TokenUsage{Input: 1000000, Output: 1000000, ReportedTotal: 2000000, UsageEvidencePresent: true}
			if summary.SessionCost != tt.wantCost || summary.SessionCostUnknown != tt.wantUnknown || summary.SessionTokens != wantUsage {
				t.Fatalf("ledger summary = %+v, want cost %v unknown %v and exact usage", summary, tt.wantCost, tt.wantUnknown)
			}
			if got := result.Trace.ModelExecutions; len(got) != 1 || got[0].RequestedModel != tt.modelID || got[0].SelectedModel != tt.modelID || got[0].ProviderID != "openrouter" || got[0].ResponseModel != tt.modelID+"-wire" || got[0].ResponseID != tt.responseID {
				t.Fatalf("trace model executions = %+v, want routed response identity", got)
			}
		})
	}
}

func newAgentRunnerUnknownPricingManager(t *testing.T, server *httptest.Server) *model.Manager {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return mgr
}

func newAgentRunnerPricingCompletionManager(t *testing.T, catalogJSON string, calls *atomic.Int32) *model.Manager {
	t.Helper()
	if !json.Valid([]byte(catalogJSON)) {
		t.Fatalf("invalid catalog JSON")
	}
	var catalog struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(catalogJSON), &catalog); err != nil || len(catalog.Data) != 1 {
		t.Fatalf("catalog fixture must contain one model: %v", err)
	}
	modelID := catalog.Data[0].ID
	responseID := "chatcmpl-" + strings.ReplaceAll(strings.TrimPrefix(modelID, "vendor/"), "/", "-")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/models":
			fmt.Fprint(w, catalogJSON)
		case "/chat/completions":
			calls.Add(1)
			fmt.Fprintf(w, `{
				"id":%q,
				"model":%q,
				"choices":[{"index":0,"message":{"role":"assistant","content":"catalog-priced answer"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":1000000,"completion_tokens":1000000,"total_tokens":2000000}
			}`, responseID, modelID+"-wire")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	cfg := config.DefaultConfig()
	cfg.Providers.OpenRouter.Enabled = true
	cfg.Providers.OpenRouter.APIKey = "test-key"
	cfg.Providers.OpenRouter.BaseURL = server.URL
	cfg.Providers.OpenAI.Enabled = false
	cfg.Models.DefaultProvider = "openrouter"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return mgr
}

func TestAgentRunnerInvocationCostClassifiesPricingKnownBoundary(t *testing.T) {
	tests := []struct {
		name        string
		catalogJSON string
		providerID  string
		modelID     string
		wantCost    float64
		wantUnknown bool
	}{
		{
			name:        "known paid openrouter pricing",
			catalogJSON: `{"data":[{"id":"vendor/paid","pricing":{"prompt":"0.000003","completion":"0.000015"}}]}`,
			providerID:  "openrouter",
			modelID:     "vendor/paid",
			wantCost:    18,
		},
		{
			name:        "known free openrouter pricing",
			catalogJSON: `{"data":[{"id":"vendor/free","pricing":{"prompt":"0","completion":"0"}}]}`,
			providerID:  "openrouter",
			modelID:     "vendor/free",
			wantCost:    0,
		},
		{
			name:        "unknown pricing is not free",
			catalogJSON: `{"data":[{"id":"vendor/unknown","pricing":{"prompt":null,"completion":"0"}}]}`,
			providerID:  "openrouter",
			modelID:     "vendor/unknown",
			wantCost:    0,
			wantUnknown: true,
		},
		{
			name:        "native codex subscription cost is known zero",
			catalogJSON: `{"data":[{"id":"ignored","pricing":{"prompt":"0.000003","completion":"0.000015"}}]}`,
			providerID:  "codex",
			modelID:     "vendor/paid",
			wantCost:    0,
		},
	}
	tokens := transparency.TokenUsage{Input: 1000000, Output: 1000000}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := newAgentRunnerPricingCatalogManager(t, tt.catalogJSON)
			cost, unknown := agentRunnerInvocationCost(mgr, tt.providerID, tt.modelID, tokens)
			if cost != tt.wantCost || unknown != tt.wantUnknown {
				t.Fatalf("agentRunnerInvocationCost = %v/%v, want %v/%v", cost, unknown, tt.wantCost, tt.wantUnknown)
			}
		})
	}
}

func newAgentRunnerPricingCatalogManager(t *testing.T, catalogJSON string) *model.Manager {
	t.Helper()
	if !json.Valid([]byte(catalogJSON)) {
		t.Fatalf("invalid catalog JSON")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("unexpected request path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, catalogJSON)
	}))
	t.Cleanup(server.Close)

	cfg := config.DefaultConfig()
	cfg.Providers.OpenRouter.Enabled = true
	cfg.Providers.OpenRouter.APIKey = "test-key"
	cfg.Providers.OpenRouter.BaseURL = server.URL
	cfg.Providers.OpenAI.Enabled = false
	cfg.Models.DefaultProvider = "openrouter"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return mgr
}
