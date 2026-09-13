package experiment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/parallel"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

// bigBlobTool always returns a large payload, so a few rounds of tool calls
// accumulate enough conversation history to force compaction.
type bigBlobTool struct{ blob string }

func (t bigBlobTool) Name() string { return "big_tool" }

func (t bigBlobTool) Description() string { return "returns a large blob for projection tests" }

func (t bigBlobTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{Type: "object"}
}

func (t bigBlobTool) Execute(params map[string]any) (*builtin.Result, error) {
	return &builtin.Result{Success: true, Data: map[string]any{"blob": t.blob}}, nil
}

// TestExperimentExecutor_RunConversationProjectsLargeTranscript closes the
// no-projection gap: runConversation used to send messages straight to the
// model with no compaction pass at all. It now routes through the shared
// turn engine (pkg/agentloop.Controller), which projects each round's
// request before sending it, bounding a transcript that accumulates well
// past the model's context window.
func TestExperimentExecutor_RunConversationProjectsLargeTranscript(t *testing.T) {
	const toolRounds = 8
	blob := strings.Repeat("large tool evidence ", 10000) // ~200 KB per round

	requestCount := 0
	var lastRequestBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		lastRequestBody = body

		w.Header().Set("Content-Type", "application/json")
		if requestCount <= toolRounds {
			_, _ = io.WriteString(w, fmt.Sprintf(`{
				"id":"chatcmpl-%d",
				"model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_%d","type":"function","function":{"name":"big_tool","arguments":"{}"}}]},"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}
			}`, requestCount, requestCount))
			return
		}
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-final",
			"model":"gpt-4o",
			"choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}
		}`)
	}))
	defer server.Close()

	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai"
	// Fund the large synthetic transcript so this test isolates projection.
	cfg.Experiment.MaxCostPerRun = 10

	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	executor := &experimentExecutor{config: cfg, modelManager: mgr}
	registry := tool.NewEmptyRegistry()
	registry.Register(bigBlobTool{blob: blob})

	output, metrics, _, err := executor.runConversation(context.Background(), "gpt-4o", registry, "accumulate evidence then finish", "", "", "")
	if err != nil {
		t.Fatalf("runConversation: %v", err)
	}
	if output != "done" {
		t.Fatalf("output = %q, want %q", output, "done")
	}
	if metrics.toolCalls != toolRounds {
		t.Fatalf("toolCalls = %d, want %d", metrics.toolCalls, toolRounds)
	}

	rawAccumulated := toolRounds * len(blob)
	if len(lastRequestBody) >= rawAccumulated {
		t.Fatalf("final request body (%d bytes) was not bounded below the raw accumulated tool evidence (%d bytes); projection did not run", len(lastRequestBody), rawAccumulated)
	}
}

func TestExperimentExecutor_ExecuteRejectsIncompleteCompletions(t *testing.T) {
	tests := []struct {
		name       string
		response   string
		finish     string
		reasonPart string
		wantOutput string
	}{
		{
			name: "truncated terminal answer",
			response: `{
				"id":"chatcmpl-truncated",
				"model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","content":"useful but unfinished"},"finish_reason":"length"}],
				"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}
			}`,
			finish:     agentloop.FinishReasonInvalidCompletion,
			reasonPart: "truncated",
			wantOutput: "useful but unfinished",
		},
		{
			name: "empty terminal answer",
			response: `{
				"id":"chatcmpl-empty",
				"model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","content":"  "},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":10,"completion_tokens":1,"total_tokens":11}
			}`,
			finish:     agentloop.FinishReasonInvalidCompletion,
			reasonPart: "without text",
		},
		{
			name: "unreadable terminal answer",
			response: `{
				"id":"chatcmpl-unreadable",
				"model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","content":{"image":"not text"}},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":10,"completion_tokens":1,"total_tokens":11}
			}`,
			finish:     agentloop.FinishReasonInvalidCompletion,
			reasonPart: "unexpected content format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tt.response)
			}))
			defer server.Close()

			executor := newOpenAIExperimentExecutor(t, server.URL, nil)
			result, err := executor.Execute(context.Background(), &parallel.AgentTask{
				ID:     "incomplete-run",
				Name:   "incomplete run",
				Branch: "experiment/incomplete-run",
				Prompt: "finish this task",
				Context: map[string]string{
					"model_id": "gpt-4o",
				},
			}, t.TempDir())
			// Execute transports task failure through AgentResult rather than its
			// own error return, matching parallel.TaskExecutor's contract.
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if result.Success {
				t.Fatal("Success = true, want the incomplete controller result to fail the experiment")
			}
			if result.Output != tt.wantOutput {
				t.Fatalf("Output = %q, want %q", result.Output, tt.wantOutput)
			}

			var incomplete *agentloop.IncompleteTurnError
			if !errors.As(result.Error, &incomplete) {
				t.Fatalf("Error = %T %v, want *agentloop.IncompleteTurnError", result.Error, result.Error)
			}
			if incomplete.FinishReason != tt.finish {
				t.Fatalf("FinishReason = %q, want %q", incomplete.FinishReason, tt.finish)
			}
			if !strings.Contains(incomplete.Reason, tt.reasonPart) {
				t.Fatalf("Reason = %q, want it to contain %q", incomplete.Reason, tt.reasonPart)
			}
		})
	}
}

func TestExperimentExecutor_ExecutePreservesGuardOutputOnFailure(t *testing.T) {
	requestCount := 0
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, fmt.Sprintf(`{
			"id":"chatcmpl-tool-%d",
			"model":"gpt-4o",
			"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_%d","type":"function","function":{"name":"missing_tool","arguments":"{}"}}]},"finish_reason":"tool_calls"}],
			"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}
		}`, requestCount, requestCount))
	}))
	defer server.Close()

	executor := newOpenAIExperimentExecutor(t, server.URL, nil)
	result, err := executor.Execute(context.Background(), &parallel.AgentTask{
		ID:     "guarded-run",
		Name:   "guarded run",
		Branch: "experiment/guarded-run",
		Prompt: "keep trying the unavailable tool",
		Context: map[string]string{
			"model_id":      "gpt-4o",
			"tools_allowed": "missing_tool",
		},
	}, t.TempDir())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Success {
		t.Fatal("Success = true, want the guard-stopped experiment to fail")
	}
	if strings.TrimSpace(result.Output) == "" {
		t.Fatal("Output is empty, want retained public unexecuted-call evidence")
	}
	if !strings.Contains(result.Output, "unexecuted tool call") {
		t.Fatalf("Output = %q, want retained public unexecuted-call evidence", result.Output)
	}

	var incomplete *agentloop.IncompleteTurnError
	if !errors.As(result.Error, &incomplete) {
		t.Fatalf("Error = %T %v, want *agentloop.IncompleteTurnError", result.Error, result.Error)
	}
	if incomplete.FinishReason != agentloop.FinishReasonInvalidCompletion || incomplete.Code != "unoffered_tool_call" {
		t.Fatalf("incomplete result = %+v, want invalid unoffered-tool-call completion", incomplete)
	}
	if got := result.Metrics["tool_calls"]; got != 0 {
		t.Fatalf("tool_calls = %d, want zero: the filtered registry supplied no schemas, so returned calls must remain control-only", got)
	}
	if requestCount != 1 || len(bodies) != 1 {
		t.Fatalf("provider requests = %d bodies=%d, want only the unsafe response-producing request", requestCount, len(bodies))
	}
	assertRequestOmitsTools(t, 1, bodies[0])
}

func TestExperimentExecutor_RunConversationPreservesTokenCapFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-over-cap",
			"model":"gpt-4o",
			"choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}
		}`)
	}))
	defer server.Close()

	executor := newOpenAIExperimentExecutor(t, server.URL, func(cfg *config.Config) {
		cfg.Experiment.MaxTokensPerRun = 11
	})
	output, metrics, _, err := executor.runConversation(context.Background(), "gpt-4o", tool.NewEmptyRegistry(), "finish this task", "", "", "")
	if err == nil || !strings.Contains(err.Error(), "max tokens per run exceeded (12 > 11)") {
		t.Fatalf("error = %v, want existing max-token cap failure", err)
	}
	if output != "" {
		t.Fatalf("output = %q, want empty output when provider admission fails", output)
	}
	if metrics.promptTokens != 10 || metrics.completionTokens != 2 {
		t.Fatalf("metrics = %+v, want charged provider usage preserved", metrics)
	}
}

func newOpenAIExperimentExecutor(t *testing.T, baseURL string, configure func(*config.Config)) *experimentExecutor {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = baseURL
	cfg.Models.DefaultProvider = "openai"
	cfg.Experiment.MaxCostPerRun = 0
	cfg.Experiment.MaxTokensPerRun = 0
	if configure != nil {
		configure(cfg)
	}

	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return &experimentExecutor{config: cfg, modelManager: mgr}
}

func assertRequestOmitsTools(t *testing.T, index int, body string) {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("decode request %d: %v", index, err)
	}
	if _, ok := decoded["tools"]; ok {
		t.Fatalf("request %d included tools: %s", index, body)
	}
	if _, ok := decoded["tool_choice"]; ok {
		t.Fatalf("request %d included tool_choice: %s", index, body)
	}
}
