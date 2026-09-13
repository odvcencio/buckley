package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tools"
	"m31labs.dev/buckley/pkg/transparency"
)

func TestCostProvenance_APIUsesCatalog(t *testing.T) {
	for _, tc := range []struct {
		name, catalog string
		unknown       bool
		cost          float64
	}{
		{"paid", `{"data":[{"id":"vendor/test","pricing":{"prompt":"0.000003","completion":"0.000015"}}]}`, false, 18},
		{"free", `{"data":[{"id":"vendor/test","pricing":{"prompt":"0","completion":"0"}}]}`, false, 0},
		{"partial", `{"data":[{"id":"vendor/test","pricing":{"prompt":null,"completion":"0"}}]}`, true, 0},
		{"missing", `{"data":[]}`, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/models":
					fmt.Fprint(w, tc.catalog)
				case "/chat/completions":
					calls.Add(1)
					fmt.Fprint(w, `{"id":"response-test","model":"vendor/test","choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-test","type":"function","function":{"name":"record","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1000000,"completion_tokens":1000000,"total_tokens":2000000}}`)
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
				t.Fatal(err)
			}
			ledger := transparency.NewCostLedger()
			inv, err := newOneshotToolInvoker(oneshotBackendAPI, "vendor/test", cfg, mgr, ledger)
			if err != nil {
				t.Fatal(err)
			}
			result, trace, err := inv.Invoke(context.Background(), "system", "user", tools.Definition{Name: "record", Parameters: tools.ObjectSchema(map[string]tools.Property{}, "")}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 || result == nil || result.ToolCall == nil || trace == nil || trace.Cost != tc.cost || trace.CostUnknown != tc.unknown {
				t.Fatalf("calls=%d result=%+v trace=%+v", calls.Load(), result, trace)
			}
			summary := ledger.Summary()
			if summary.InvocationCount != 1 || summary.SessionCost != tc.cost || summary.SessionCostUnknown != tc.unknown {
				t.Fatalf("summary=%+v", summary)
			}
		})
	}
}
