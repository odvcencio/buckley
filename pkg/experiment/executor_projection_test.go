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
	projectcontext "m31labs.dev/buckley/pkg/context"
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

type countingTool struct {
	name       string
	executions *int
}

func (t countingTool) Name() string { return t.name }

func (t countingTool) Description() string {
	return "counts executions for experiment tool routing tests"
}

func (t countingTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{Type: "object"}
}

func (t countingTool) Execute(params map[string]any) (*builtin.Result, error) {
	if t.executions != nil {
		*t.executions++
	}
	return &builtin.Result{Success: true, Data: map[string]any{"ok": true}}, nil
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
			"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":13}
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

	conversation, err := executor.runConversation(context.Background(), "gpt-4o", registry, "accumulate evidence then finish", "", "", "")
	if err != nil {
		t.Fatalf("runConversation: %v", err)
	}
	if conversation.output != "done" {
		t.Fatalf("output = %q, want %q", conversation.output, "done")
	}
	if conversation.metrics.toolCalls != toolRounds {
		t.Fatalf("toolCalls = %d, want %d", conversation.metrics.toolCalls, toolRounds)
	}

	rawAccumulated := toolRounds * len(blob)
	if len(lastRequestBody) >= rawAccumulated {
		t.Fatalf("final request body (%d bytes) was not bounded below the raw accumulated tool evidence (%d bytes); projection did not run", len(lastRequestBody), rawAccumulated)
	}
}

func TestExperimentExecutor_ToollessRouteOmitsToolsAndRejectsSurpriseCall(t *testing.T) {
	executions := 0
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "application/json")
		switch len(bodies) {
		case 1:
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-surprise",
				"model":"o1-mini",
				"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_surprise","type":"function","function":{"name":"count_tool","arguments":"{}"}}]},"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
			}`)
		case 2:
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-final",
				"model":"o1-mini",
				"choices":[{"index":0,"message":{"role":"assistant","content":"I could not run that tool."},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
			}`)
		default:
			t.Fatalf("unexpected request %d: %s", len(bodies), body)
		}
	}))
	defer server.Close()

	executor := newOpenAIExperimentExecutor(t, server.URL, nil)
	executor.modelManager.RoutingHooks().Register(func(decision *model.RoutingDecision) *model.RoutingDecision {
		if decision != nil && decision.RequestedModel == "alias-o1-mini" {
			decision.SelectedModel = "openai/o1-mini"
		}
		return decision
	})
	registry := tool.NewEmptyRegistry()
	registry.Register(countingTool{name: "count_tool", executions: &executions})

	conversation, err := executor.runConversation(context.Background(), "alias-o1-mini", registry, "try the counter", "", "", "")
	var incomplete *agentloop.IncompleteTurnError
	if !errors.As(err, &incomplete) {
		t.Fatalf("runConversation error = %T %v, want *agentloop.IncompleteTurnError", err, err)
	}
	if incomplete.FinishReason != agentloop.FinishReasonInvalidCompletion || incomplete.Code != "unoffered_tool_call" {
		t.Fatalf("incomplete result = %+v, want invalid unoffered-tool-call completion", incomplete)
	}
	if !strings.Contains(conversation.output, "unexecuted tool call") {
		t.Fatalf("output = %q, want retained public unexecuted-call evidence", conversation.output)
	}
	if executions != 0 {
		t.Fatalf("tool executions = %d, want zero", executions)
	}
	if conversation.metrics.toolCalls != 0 || conversation.metrics.toolSuccesses != 0 || conversation.metrics.toolFailures != 0 {
		t.Fatalf("tool metrics = %+v, want no local execution metrics for surprise call after no-schema request", conversation.metrics)
	}
	if len(conversation.toolOutcomes) != 0 {
		t.Fatalf("tool outcomes = %+v, want no dispatch after an unoffered call", conversation.toolOutcomes)
	}
	if len(bodies) != 1 {
		t.Fatalf("request count = %d, want only the unsafe response-producing request", len(bodies))
	}
	assertRequestOmitsTools(t, 1, bodies[0])
}

func TestExperimentExecutor_UnknownRouteMetadataKeepsToolsEligible(t *testing.T) {
	executions := 0
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "application/json")
		switch len(bodies) {
		case 1:
			var decoded map[string]any
			if err := json.Unmarshal(body, &decoded); err != nil {
				t.Fatalf("decode first request: %v", err)
			}
			if _, ok := decoded["tools"]; !ok {
				t.Fatalf("first request omitted tools for unknown future model: %s", body)
			}
			if decoded["tool_choice"] != "auto" {
				t.Fatalf("tool_choice = %v, want auto in first request: %s", decoded["tool_choice"], body)
			}
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-tool",
				"model":"future-openai-tool-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_count","type":"function","function":{"name":"count_tool","arguments":"{}"}}]},"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
			}`)
		case 2:
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-final",
				"model":"future-openai-tool-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
			}`)
		default:
			t.Fatalf("unexpected request %d: %s", len(bodies), body)
		}
	}))
	defer server.Close()

	executor := newOpenAIExperimentExecutor(t, server.URL, nil)
	registry := tool.NewEmptyRegistry()
	registry.Register(countingTool{name: "count_tool", executions: &executions})

	conversation, err := executor.runConversation(context.Background(), "future-openai-tool-model", registry, "use the counter", "", "", "")
	if err != nil {
		t.Fatalf("runConversation: %v", err)
	}
	if conversation.output != "done" {
		t.Fatalf("output = %q, want done", conversation.output)
	}
	if executions != 1 {
		t.Fatalf("tool executions = %d, want 1", executions)
	}
	if conversation.metrics.toolCalls != 1 || conversation.metrics.toolSuccesses != 1 || conversation.metrics.toolFailures != 0 {
		t.Fatalf("tool metrics = %+v, want one successful execution", conversation.metrics)
	}
}

func TestExperimentExecutor_RouteDriftFailsBeforeProviderInvocation(t *testing.T) {
	providerCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls++
		t.Fatalf("provider should not be invoked after route drift")
	}))
	defer server.Close()

	executor := newOpenAIExperimentExecutor(t, server.URL, nil)
	hookCalls := 0
	executor.modelManager.RoutingHooks().Register(func(decision *model.RoutingDecision) *model.RoutingDecision {
		hookCalls++
		if decision == nil || decision.RequestedModel != "drifty-model" {
			return decision
		}
		if hookCalls == 1 {
			decision.SelectedModel = "openai/gpt-4o"
		} else {
			decision.SelectedModel = "openai/gpt-4o-mini"
		}
		return decision
	})

	_, err := executor.runConversation(context.Background(), "drifty-model", tool.NewEmptyRegistry(), "finish", "", "", "")
	if err == nil || !strings.Contains(err.Error(), "route changed") {
		t.Fatalf("runConversation error = %v, want route changed", err)
	}
	if providerCalls != 0 {
		t.Fatalf("provider calls = %d, want zero", providerCalls)
	}
}

func TestExperimentExecutor_CompatibleCatalogToollessSurpriseDoesNotInjectNoop(t *testing.T) {
	for _, providerID := range []string{"openai_compatible", "litellm"} {
		t.Run(providerID, func(t *testing.T) {
			executions := 0
			var chatBodies []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/model/info":
					// LiteLLM probes this endpoint before using the compatible
					// /models catalog fallback used by this shared fixture.
					if providerID != "litellm" {
						t.Fatalf("unexpected LiteLLM model-info request for %s", providerID)
					}
					http.Error(w, "model info unavailable", http.StatusNotFound)
				case "/models":
					_, _ = io.WriteString(w, `{"data":[{"id":"tool-less-model","name":"tool-less-model","context_length":4096,"supported_parameters":[]}]}`)
				case "/chat/completions":
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Fatalf("read chat request: %v", err)
					}
					chatBodies = append(chatBodies, string(body))
					switch len(chatBodies) {
					case 1:
						_, _ = io.WriteString(w, `{
							"id":"chatcmpl-compatible-surprise",
							"model":"tool-less-model",
							"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_surprise","type":"function","function":{"name":"count_tool","arguments":"{}"}}]},"finish_reason":"tool_calls"}],
							"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
						}`)
					case 2:
						_, _ = io.WriteString(w, `{
							"id":"chatcmpl-compatible-final",
							"model":"tool-less-model",
							"choices":[{"index":0,"message":{"role":"assistant","content":"done without tools"},"finish_reason":"stop"}],
							"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
						}`)
					default:
						t.Fatalf("unexpected chat request %d: %s", len(chatBodies), body)
					}
				default:
					t.Fatalf("unexpected path %s", r.URL.Path)
				}
			}))
			defer server.Close()

			executor := newCompatibleExperimentExecutor(t, providerID, server.URL)
			registry := tool.NewEmptyRegistry()
			registry.Register(countingTool{name: "count_tool", executions: &executions})

			conversation, err := executor.runConversation(context.Background(), providerID+"/tool-less-model", registry, "try the counter", "", "", "")
			var incomplete *agentloop.IncompleteTurnError
			if !errors.As(err, &incomplete) {
				t.Fatalf("runConversation error = %T %v, want *agentloop.IncompleteTurnError", err, err)
			}
			if incomplete.FinishReason != agentloop.FinishReasonInvalidCompletion || incomplete.Code != "unoffered_tool_call" {
				t.Fatalf("incomplete result = %+v, want invalid unoffered-tool-call completion", incomplete)
			}
			if !strings.Contains(conversation.output, "unexecuted tool call") {
				t.Fatalf("output = %q, want retained public unexecuted-call evidence", conversation.output)
			}
			if executions != 0 {
				t.Fatalf("tool executions = %d, want zero", executions)
			}
			if len(conversation.toolOutcomes) != 0 {
				t.Fatalf("tool outcomes = %+v, want no dispatch after an unoffered call", conversation.toolOutcomes)
			}
			if len(chatBodies) != 1 {
				t.Fatalf("chat request count = %d, want only the unsafe response-producing request", len(chatBodies))
			}
			assertRequestOmitsTools(t, 1, chatBodies[0])
			if strings.Contains(chatBodies[0], "_noop") {
				t.Fatalf("request injected noop tool after catalog-confirmed toolless route: %s", chatBodies[0])
			}
		})
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
	conversation, err := executor.runConversation(context.Background(), "gpt-4o", tool.NewEmptyRegistry(), "finish this task", "", "", "")
	if err == nil || !strings.Contains(err.Error(), "max tokens per run exceeded (12 > 11)") {
		t.Fatalf("error = %v, want existing max-token cap failure", err)
	}
	if conversation.output != "done" {
		t.Fatalf("output = %q, want provider response output preserved with admission failure", conversation.output)
	}
	if conversation.metrics.promptTokens != 10 || conversation.metrics.completionTokens != 2 {
		t.Fatalf("metrics = %+v, want charged provider usage preserved", conversation.metrics)
	}
	if !conversation.metrics.usageEvidence || conversation.metrics.usage.Input != 10 || conversation.metrics.usage.Output != 2 || conversation.metrics.usage.ReportedTotal != 12 || !conversation.metrics.usage.UsageEvidencePresent {
		t.Fatalf("usage evidence = %+v observed=%v, want retained provider usage from response+error", conversation.metrics.usage, conversation.metrics.usageEvidence)
	}
	if conversation.metrics.costUnknown {
		t.Fatalf("costUnknown = true, want authoritative split usage to remain known")
	}
	if len(conversation.modelExecutions) != 1 || conversation.modelExecutions[0].ResponseID != "chatcmpl-over-cap" || conversation.modelExecutions[0].ResponseModel != "gpt-4o" {
		t.Fatalf("model executions = %+v, want response identity preserved through token cap failure", conversation.modelExecutions)
	}
}

func TestExperimentExecutor_DelegateIncompleteResultPropagatesToFinalSynthesis(t *testing.T) {
	requestCount := 0
	var finalRequestBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch requestCount {
		case 1:
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-outer-tool",
				"model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_delegate","type":"function","function":{"name":"delegate_task","arguments":"{\"agent_name\":\"helper\",\"task\":\"inspect evidence\"}"}}]},"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13}
			}`)
		case 2:
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-delegate-length",
				"model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","content":"<think>private-delegate-content</think>public delegate draft","reasoning":"message-reasoning-sentinel","reasoning_details":[{"type":"reasoning.text","text":"reasoning-detail-sentinel"}]},"finish_reason":"length"}],
				"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9},
				"error":{"message":"raw-provider-error-sentinel"}
			}`)
		case 3:
			finalRequestBody = append(finalRequestBody[:0], body...)
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-final",
				"model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","content":"final answer from retained delegate evidence"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":8,"completion_tokens":4,"total_tokens":12}
			}`)
		default:
			t.Fatalf("unexpected request %d: %s", requestCount, string(body))
		}
	}))
	defer server.Close()

	executor := newOpenAIExperimentExecutor(t, server.URL, nil)
	executor.projectContext = &projectcontext.ProjectContext{
		SubAgents: map[string]*projectcontext.SubAgentSpec{
			"helper": {Name: "helper", Model: "gpt-4o"},
		},
	}
	task := &parallel.AgentTask{
		ID:      "delegate-incomplete",
		Name:    "delegate incomplete",
		Prompt:  "delegate then synthesize",
		Context: map[string]string{"model_id": "gpt-4o"},
	}
	registry := executor.buildRegistry(task, t.TempDir())

	conversation, err := executor.runConversation(context.Background(), "gpt-4o", registry, task.Prompt, "", "", "")
	if err != nil {
		t.Fatalf("runConversation: %v", err)
	}
	if conversation.output != "final answer from retained delegate evidence" {
		t.Fatalf("output = %q, want final synthesis after failed delegate outcome", conversation.output)
	}
	if requestCount != 3 {
		t.Fatalf("request count = %d, want outer tool, delegate response+error, final synthesis", requestCount)
	}
	finalRequest := string(finalRequestBody)
	if !strings.Contains(finalRequest, "public delegate draft") || !strings.Contains(finalRequest, "delegate task incomplete") {
		t.Fatalf("final request did not include retained delegate failure evidence: %s", string(finalRequestBody))
	}
	for _, forbidden := range []string{"private-delegate-content", "message-reasoning-sentinel", "reasoning-detail-sentinel", "raw-provider-error-sentinel"} {
		if strings.Contains(finalRequest, forbidden) {
			t.Fatalf("final request contains forbidden sentinel %q: %s", forbidden, finalRequest)
		}
	}
	if conversation.metrics.toolCalls != 1 || conversation.metrics.toolSuccesses != 0 || conversation.metrics.toolFailures != 1 {
		t.Fatalf("tool metrics = %+v, want one failed delegate outcome retained", conversation.metrics)
	}
	if conversation.metrics.promptTokens != 25 || conversation.metrics.completionTokens != 9 {
		t.Fatalf("token metrics = %+v, want outer+delegate+final response usage", conversation.metrics)
	}
	if !conversation.metrics.usageEvidence || conversation.metrics.usage.Input != 25 || conversation.metrics.usage.Output != 9 || conversation.metrics.usage.ReportedTotal != 34 {
		t.Fatalf("usage evidence = %+v observed=%v, want run-level delegate usage included", conversation.metrics.usage, conversation.metrics.usageEvidence)
	}
	if !hasResponseID(conversation.modelExecutions, "chatcmpl-delegate-length") {
		t.Fatalf("model executions = %+v, want delegate response identity retained", conversation.modelExecutions)
	}
}

func TestExperimentExecutor_DelegateCapBreachEmitsFailedOutcomeAndStops(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch requestCount {
		case 1:
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-cap-outer-tool",
				"model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_delegate_cap","type":"function","function":{"name":"delegate_task","arguments":"{\"agent_name\":\"helper\",\"task\":\"finish under cap\"}"}}]},"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
			}`)
		case 2:
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-delegate-over-cap",
				"model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","content":"delegate completed public output"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":11,"completion_tokens":1,"total_tokens":12}
			}`)
		default:
			t.Fatalf("unexpected final request after delegate cap breach %d: %s", requestCount, string(body))
		}
	}))
	defer server.Close()

	executor := newOpenAIExperimentExecutor(t, server.URL, func(cfg *config.Config) {
		cfg.Experiment.MaxTokensPerRun = 10
	})
	executor.projectContext = &projectcontext.ProjectContext{
		SubAgents: map[string]*projectcontext.SubAgentSpec{
			"helper": {Name: "helper", Model: "gpt-4o"},
		},
	}
	task := &parallel.AgentTask{
		ID:      "delegate-cap",
		Name:    "delegate cap",
		Prompt:  "delegate then stop on cap",
		Context: map[string]string{"model_id": "gpt-4o"},
	}

	conversation, err := executor.runConversation(context.Background(), "gpt-4o", executor.buildRegistry(task, t.TempDir()), task.Prompt, "", "", "")
	if err == nil || !strings.Contains(err.Error(), "max tokens per run exceeded") {
		t.Fatalf("error = %v, want max token cap failure", err)
	}
	if requestCount != 2 {
		t.Fatalf("request count = %d, want no final model call after cap breach", requestCount)
	}
	if len(conversation.toolOutcomes) != 1 {
		t.Fatalf("tool outcomes = %+v, want one retained delegate outcome", conversation.toolOutcomes)
	}
	outcome := conversation.toolOutcomes[0]
	if outcome.Success {
		t.Fatalf("tool outcome success = true, want false when retained delegate evidence breaches run cap")
	}
	if !strings.Contains(outcome.Content, "delegate completed public output") || !strings.Contains(outcome.Content, "chatcmpl-delegate-over-cap") {
		t.Fatalf("tool outcome content = %q, want structured delegate payload retained", outcome.Content)
	}
	if conversation.metrics.toolCalls != 1 || conversation.metrics.toolSuccesses != 0 || conversation.metrics.toolFailures != 1 {
		t.Fatalf("tool metrics = %+v, want cap-breached delegate counted as failed outcome", conversation.metrics)
	}
	if !conversation.metrics.usageEvidence || conversation.metrics.usage.Input != 12 || conversation.metrics.usage.Output != 2 || conversation.metrics.usage.ReportedTotal != 14 {
		t.Fatalf("usage evidence = %+v observed=%v, want outer+delegate usage retained through cap breach", conversation.metrics.usage, conversation.metrics.usageEvidence)
	}
	if !hasResponseID(conversation.modelExecutions, "chatcmpl-delegate-over-cap") {
		t.Fatalf("model executions = %+v, want over-cap delegate identity retained", conversation.modelExecutions)
	}
}

func hasResponseID(identities []model.ExecutionIdentity, responseID string) bool {
	for _, identity := range identities {
		if identity.ResponseID == responseID {
			return true
		}
	}
	return false
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

func newCompatibleExperimentExecutor(t *testing.T, providerID string, baseURL string) *experimentExecutor {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = false
	cfg.Models.DefaultProvider = providerID
	cfg.Experiment.MaxCostPerRun = 0
	cfg.Experiment.MaxTokensPerRun = 0
	switch providerID {
	case "openai_compatible":
		cfg.Providers.OpenAICompatible.Enabled = true
		cfg.Providers.OpenAICompatible.APIKey = "test-key"
		cfg.Providers.OpenAICompatible.BaseURL = baseURL
	case "litellm":
		cfg.Providers.LiteLLM.Enabled = true
		cfg.Providers.LiteLLM.APIKey = "test-key"
		cfg.Providers.LiteLLM.BaseURL = baseURL
	default:
		t.Fatalf("unsupported provider %q", providerID)
	}
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := mgr.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
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
