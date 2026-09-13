package experiment

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/parallel"
)

func TestExperimentExecutor_CostAdmissionBeforeDispatch(t *testing.T) {
	for _, tc := range []struct {
		name, model, reason string
		budget              float64
	}{
		{"unknown pricing", "openai/unknown-cost-model", "before dispatch", 1},
		{"unaffordable input", "gpt-4o", "cannot fund", 1e-12},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"should not be requested"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
			}))
			defer server.Close()
			executor := newOpenAIExperimentExecutor(t, server.URL, func(cfg *config.Config) { cfg.Experiment.MaxCostPerRun = tc.budget })
			result, err := executor.Execute(context.Background(), &parallel.AgentTask{ID: "admission", Prompt: "read and summarize", Context: map[string]string{"model_id": tc.model, "tools_allowed": "read_file"}}, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if requests.Load() != 0 {
				t.Errorf("provider requests=%d, want zero before cost admission", requests.Load())
			}
			if result.Success || result.Error == nil || !strings.Contains(result.Error.Error(), tc.reason) {
				t.Errorf("result=%+v, want pre-dispatch cost failure", result)
			}
			if result.Metrics["prompt_tokens"] != 0 || result.Metrics["completion_tokens"] != 0 || result.Metrics["tool_calls"] != 0 || result.TotalCost != 0 {
				t.Errorf("unexecuted run acquired usage: %+v", result)
			}
		})
	}
}

func TestExperimentExecutor_CostAdmissionCapsProviderOutput(t *testing.T) {
	var requests atomic.Int32
	var outputCap atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var req struct {
			MaxTokens           int64 `json:"max_tokens"`
			MaxCompletionTokens int64 `json:"max_completion_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if req.MaxTokens != 0 && req.MaxCompletionTokens != 0 {
			t.Error("ambiguous output cap fields")
		}
		outputCap.Store(req.MaxTokens + req.MaxCompletionTokens)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"model":"gpt-4o","choices":[{"message":{"role":"assistant","content":"summary complete"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
	}))
	defer server.Close()
	executor := newOpenAIExperimentExecutor(t, server.URL, func(cfg *config.Config) { cfg.Experiment.MaxCostPerRun = 0.05 })
	result, err := executor.Execute(context.Background(), &parallel.AgentTask{ID: "bounded", Prompt: "summarize the supplied text: hello", Context: map[string]string{"model_id": "gpt-4o", "max_tokens": "100000", "tools_allowed": "missing_tool"}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success || result.Output != "summary complete" {
		t.Fatalf("result=%+v", result)
	}
	if requests.Load() != 1 || outputCap.Load() <= 0 || outputCap.Load() >= 100000 {
		t.Errorf("requests=%d output cap=%d, want one affordable capped request", requests.Load(), outputCap.Load())
	}
	wantCost, err := executor.modelManager.CalculateBoundedCost("gpt-4o", model.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(result.TotalCost-wantCost) > 1e-12 {
		t.Errorf("cost=%v, want actual usage cost=%v", result.TotalCost, wantCost)
	}
}

func TestExperimentExecutor_CostAdmissionDoesNotReportReservationAsMeasured(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"model":"gpt-4o","choices":[{"message":{"role":"assistant","content":"summary complete"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	executor := newOpenAIExperimentExecutor(t, server.URL, func(cfg *config.Config) { cfg.Experiment.MaxCostPerRun = 0.05 })
	result, err := executor.Execute(context.Background(), &parallel.AgentTask{ID: "missing-usage", Prompt: "summarize: hello", Context: map[string]string{"model_id": "gpt-4o", "tools_allowed": "missing_tool"}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success || result.Output != "summary complete" {
		t.Fatalf("result=%+v", result)
	}
	if result.TotalCost != 0 {
		t.Errorf("reported measured cost=%v despite missing usage; reservation is budget evidence only", result.TotalCost)
	}
}

func TestExperimentExecutor_CostAdmissionRetainsOverrunCostWithoutTools(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"model":"gpt-4o","choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"probe.txt\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1000000000,"completion_tokens":5,"total_tokens":1000000005}}`)
	}))
	defer server.Close()
	executor := newOpenAIExperimentExecutor(t, server.URL, func(cfg *config.Config) { cfg.Experiment.MaxCostPerRun = 0.05 })
	result, err := executor.Execute(context.Background(), &parallel.AgentTask{ID: "provider-overrun", Prompt: "read probe.txt", Context: map[string]string{"model_id": "gpt-4o", "tools_allowed": "read_file"}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || result.Error == nil {
		t.Fatalf("accepted overrun: %+v", result)
	}
	if requests.Load() != 1 || result.Metrics["tool_calls"] != 0 {
		t.Errorf("requests=%d tool calls=%d, want one request and zero tool executions", requests.Load(), result.Metrics["tool_calls"])
	}
	wantCost, err := executor.modelManager.CalculateBoundedCost("gpt-4o", model.Usage{PromptTokens: 1000000000, CompletionTokens: 5, TotalTokens: 1000000005})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(result.TotalCost-wantCost) > 1e-9 {
		t.Errorf("cost=%v, want retained overrun=%v", result.TotalCost, wantCost)
	}
}

func TestExperimentExecutor_CostAdmissionAllowsAuthoritativeFreeModel(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/tags":
			io.WriteString(w, `{"models":[{"name":"free-test"}]}`)
		case "/api/chat":
			requests.Add(1)
			io.WriteString(w, `{"model":"free-test","message":{"role":"assistant","content":"summary complete"},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":5}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cfg := config.DefaultConfig()
	cfg.Providers.OpenRouter.Enabled = false
	cfg.Providers.Ollama.Enabled = true
	cfg.Providers.Ollama.BaseURL = server.URL
	cfg.Models.DefaultProvider = "ollama"
	cfg.Experiment.MaxCostPerRun = 1e-12
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	executor := &experimentExecutor{config: cfg, modelManager: mgr}
	result, err := executor.Execute(context.Background(), &parallel.AgentTask{ID: "known-free", Prompt: "summarize: hello", Context: map[string]string{"model_id": "ollama/free-test", "tools_allowed": "missing_tool"}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success || result.Output != "summary complete" || requests.Load() != 1 || result.TotalCost != 0 {
		t.Fatalf("known-free result=%+v requests=%d", result, requests.Load())
	}
}
