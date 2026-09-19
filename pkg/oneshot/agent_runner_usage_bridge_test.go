package oneshot

import (
	"context"
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

func TestAgentRunnerPreservesRichRLMUsageInTraceAndLedger(t *testing.T) {
	tests := []struct {
		name        string
		response    string
		wantUsage   transparency.TokenUsage
		wantUnknown bool
	}{
		{
			name:        "total only",
			response:    `"usage":{"total_tokens":42}`,
			wantUsage:   transparency.TokenUsage{ReportedTotal: 42, Unclassified: 42, UsageEvidencePresent: true},
			wantUnknown: true,
		},
		{
			name: "reported details",
			response: `"usage":{
				"prompt_tokens":100,
				"completion_tokens":50,
				"total_tokens":170,
				"prompt_tokens_details":{"cached_tokens":30},
				"completion_tokens_details":{"reasoning_tokens":20},
				"cache_write_tokens":12
			}`,
			wantUsage: transparency.TokenUsage{
				Input:                100,
				Output:               50,
				Unclassified:         20,
				ReportedTotal:        170,
				ReportedReasoning:    intPtr(20),
				ReportedCachedInput:  intPtr(30),
				ReportedCacheWrite:   12,
				UsageEvidencePresent: true,
			},
			wantUnknown: true,
		},
		{
			name:        "missing usage",
			response:    ``,
			wantUsage:   transparency.TokenUsage{UsageEvidenceMissing: true},
			wantUnknown: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			mgr := newAgentRunnerUsageBridgeManager(t, tt.response, &calls)
			ledger := transparency.NewCostLedger()
			runner := NewAgentRunner(AgentRunnerConfig{
				Models:   mgr,
				Registry: tool.NewEmptyRegistry(),
				ModelID:  "vendor/usage-rich",
				Ledger:   ledger,
			})

			result, err := runner.Run(context.Background(), "system", "task", nil, AgentExecutionOpts{MaxIterations: 1})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("model calls = %d, want one", calls.Load())
			}
			if result == nil || result.Trace == nil {
				t.Fatalf("result = %#v, want trace", result)
			}
			assertRichUsage(t, result.Usage, tt.wantUsage)
			assertRichUsage(t, result.Trace.Tokens, tt.wantUsage)
			summary := ledger.Summary()
			assertRichUsage(t, summary.SessionTokens, tt.wantUsage)
			if result.Trace.CostUnknown != tt.wantUnknown || summary.SessionCostUnknown != tt.wantUnknown {
				t.Fatalf("cost unknown trace/ledger = %v/%v, want %v", result.Trace.CostUnknown, summary.SessionCostUnknown, tt.wantUnknown)
			}
			if got := result.ModelExecutions; len(got) != 1 || got[0].ResponseID != "chatcmpl-usage-rich" || got[0].ResponseModel != "vendor/usage-rich-wire" {
				t.Fatalf("model executions = %+v, want retained response identity", got)
			}
		})
	}
}

func TestAgentRunnerRichUsageCostClassificationPreservesKnownFreeAndCodexZero(t *testing.T) {
	missing := transparency.TokenUsage{UsageEvidenceMissing: true}
	paid := newAgentRunnerPricingCatalogManager(t, `{"data":[{"id":"vendor/paid","pricing":{"prompt":"0.000003","completion":"0.000015"}}]}`)
	if cost, unknown := agentRunnerInvocationCost(paid, "openrouter", "vendor/paid", missing); cost != 0 || !unknown {
		t.Fatalf("paid missing usage cost = %v unknown=%v, want unknown subtotal", cost, unknown)
	}
	free := newAgentRunnerPricingCatalogManager(t, `{"data":[{"id":"vendor/free","pricing":{"prompt":"0","completion":"0"}}]}`)
	if cost, unknown := agentRunnerInvocationCost(free, "openrouter", "vendor/free", missing); cost != 0 || unknown {
		t.Fatalf("known-free missing usage cost = %v unknown=%v, want known zero", cost, unknown)
	}
	if cost, unknown := agentRunnerInvocationCost(paid, "codex", "vendor/paid", missing); cost != 0 || unknown {
		t.Fatalf("codex subscription cost = %v unknown=%v, want known zero", cost, unknown)
	}
}

func TestAgentRunnerLegacyScalarFallbackPreservesPositiveRemainder(t *testing.T) {
	got := agentResultTokenUsage(&AgentResult{
		TokensUsed:   37,
		InputTokens:  10,
		OutputTokens: 20,
	})
	assertRichUsage(t, got, transparency.TokenUsage{Input: 10, Output: 20, Unclassified: 7})
}

func newAgentRunnerUsageBridgeManager(t *testing.T, usageObject string, calls *atomic.Int32) *model.Manager {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/models":
			fmt.Fprint(w, `{"data":[{"id":"vendor/usage-rich","pricing":{"prompt":"0.000003","completion":"0.000015"}}]}`)
		case "/chat/completions":
			calls.Add(1)
			usage := strings.TrimSpace(usageObject)
			if usage != "" {
				usage = "," + usage
			}
			fmt.Fprintf(w, `{
				"id":"chatcmpl-usage-rich",
				"model":"vendor/usage-rich-wire",
				"choices":[{"index":0,"message":{"role":"assistant","content":"rich usage answer"},"finish_reason":"stop"}]%s
			}`, usage)
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

func assertRichUsage(t *testing.T, got, want transparency.TokenUsage) {
	t.Helper()
	got = transparency.CloneTokenUsage(got)
	want = transparency.CloneTokenUsage(want)
	if got.Input != want.Input || got.Output != want.Output || got.Reasoning != want.Reasoning ||
		got.Unclassified != want.Unclassified || got.CachedInput != want.CachedInput ||
		got.ReportedTotal != want.ReportedTotal || got.ReportedCacheWrite != want.ReportedCacheWrite ||
		got.ReportedUsageInconsistent != want.ReportedUsageInconsistent || got.Estimated != want.Estimated ||
		got.UsageEvidencePresent != want.UsageEvidencePresent || got.UsageEvidenceMissing != want.UsageEvidenceMissing {
		t.Fatalf("usage = %+v, want %+v", got, want)
	}
	if (got.ReportedReasoning == nil) != (want.ReportedReasoning == nil) {
		t.Fatalf("ReportedReasoning = %v, want %v", got.ReportedReasoning, want.ReportedReasoning)
	}
	if got.ReportedReasoning != nil && *got.ReportedReasoning != *want.ReportedReasoning {
		t.Fatalf("ReportedReasoning = %d, want %d", *got.ReportedReasoning, *want.ReportedReasoning)
	}
	if (got.ReportedCachedInput == nil) != (want.ReportedCachedInput == nil) {
		t.Fatalf("ReportedCachedInput = %v, want %v", got.ReportedCachedInput, want.ReportedCachedInput)
	}
	if got.ReportedCachedInput != nil && *got.ReportedCachedInput != *want.ReportedCachedInput {
		t.Fatalf("ReportedCachedInput = %d, want %d", *got.ReportedCachedInput, *want.ReportedCachedInput)
	}
}

func intPtr(value int) *int {
	return &value
}
