package oneshot

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
	"m31labs.dev/buckley/pkg/transparency"
)

func TestAgentRunnerRun_RecoveredTimeoutKeepsCostUnknown(t *testing.T) {
	for _, byok := range []bool{false, true} {
		t.Run(fmt.Sprintf("BYOK=%v", byok), func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "proof.txt"), []byte("captured contract"), 0o600); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Tools    []json.RawMessage `json:"tools"`
					Messages json.RawMessage   `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				switch call := calls.Add(1); call {
				case 1:
					fmt.Fprintf(w, `{"id":"inspected","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"read-proof","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"proof.txt\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"cost":0.001,"is_byok":%v}}`, byok)
				case 2:
					if len(request.Tools) == 0 {
						t.Error("second request must attempt inspection")
					}
					select {
					case <-r.Context().Done():
					case <-time.After(3 * time.Second):
						t.Error("inspection request did not cancel")
					}
				case 3:
					if len(request.Tools) > 0 {
						t.Error("recovery did not enter final synthesis")
					}
					fmt.Fprintf(w, `{"id":"recovered","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"completed from captured contract"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"cost":0.002,"is_byok":%v}}`, byok)
				default:
					t.Errorf("unexpected model request %d", call)
				}
			}))
			t.Cleanup(server.Close)
			registry := tool.NewEmptyRegistry()
			registry.Register(&builtin.ReadFileTool{})
			registry.SetWorkDir(root)
			t.Cleanup(func() { _ = registry.Close() })
			ledger := transparency.NewCostLedger()
			runner := NewAgentRunner(AgentRunnerConfig{Models: newAgentRunnerTestManager(t, server), Registry: registry, ModelID: "gpt-4o", Ledger: ledger})
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			t.Cleanup(cancel)
			result, err := runner.Run(ctx, "Inspect the contract before answering.", "Read proof.txt", []string{"read_file"}, AgentExecutionOpts{MaxIterations: 6, SynthesisLead: 2 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 3 || result.Incomplete || result.Response != "completed from captured contract" {
				t.Fatalf("calls=%d result=%+v", calls.Load(), result)
			}
			if !result.Usage.UsageEvidenceMissing || !result.Trace.CostUnknown || math.Abs(result.Trace.Cost-0.003) > 1e-12 {
				t.Fatalf("usage=%+v trace=%+v", result.Usage, result.Trace)
			}
			summary := ledger.Summary()
			if !summary.SessionCostUnknown || math.Abs(summary.SessionCost-0.003) > 1e-12 {
				t.Fatalf("session summary=%+v", summary)
			}
			if len(result.ToolCalls) != 1 || !result.ToolCalls[0].Success {
				t.Fatalf("inspection evidence lost: %+v", result.ToolCalls)
			}
		})
	}
}

func TestAgentRunnerInvocationCostDetail_PartialProviderCost(t *testing.T) {
	for _, byok := range []bool{false, true} {
		usage := transparency.TokenUsage{ProviderCostKnown: true, ProviderCostIsBYOK: byok, ProviderCostUSD: 0.0042, UsageEvidenceMissing: true}
		cost, unknown, estimated := agentRunnerInvocationCostDetail(nil, "openrouter", "fixture/model", usage)
		if cost != 0.0042 || !unknown || estimated {
			t.Fatalf("BYOK=%v cost=%v unknown=%v estimated=%v", byok, cost, unknown, estimated)
		}
		if cost, unknown, _ := agentRunnerInvocationCostDetail(nil, "codex", "fixture/model", usage); cost != 0 || unknown {
			t.Fatalf("native subscription cost=%v unknown=%v", cost, unknown)
		}
	}
}
