package rlm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

// fakeReadTool is a minimal read-only tool for SubAgent.Execute integration
// tests: it always succeeds and echoes a fixed body.
type fakeReadTool struct {
	name string
	body string
}

func (f fakeReadTool) Name() string        { return f.name }
func (f fakeReadTool) Description() string { return "test tool" }
func (f fakeReadTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{
		Type: "object",
		Properties: map[string]builtin.PropertySchema{
			"path": {Type: "string"},
		},
	}
}
func (f fakeReadTool) Execute(params map[string]any) (*builtin.Result, error) {
	return &builtin.Result{Success: true, Data: map[string]any{"content": f.body}}, nil
}

type countingReadTool struct {
	fakeReadTool
	calls int
}

func (f *countingReadTool) Execute(params map[string]any) (*builtin.Result, error) {
	f.calls++
	return f.fakeReadTool.Execute(params)
}

func TestSubAgentExecuteTools_RetainsToolBudgetRejection(t *testing.T) {
	registry := tool.NewEmptyRegistry()
	readTool := &countingReadTool{fakeReadTool: fakeReadTool{name: "read_file", body: "should not execute"}}
	registry.Register(readTool)
	agent := &SubAgent{maxToolCalls: 1}
	result := &SubAgentResult{ToolCalls: []SubAgentToolCall{{
		ID:      "call-prev",
		Name:    "read_file",
		Result:  "prior evidence",
		Success: true,
	}}}

	calls, err := agent.executeTools(context.Background(), []model.ToolCall{{
		ID: "call-budget", Type: "function", Function: model.FunctionCall{Name: "read_file", Arguments: `{"path":"next.go"}`},
	}}, registry, map[string]struct{}{"read_file": {}}, result)
	if err != nil {
		t.Fatalf("executeTools returned error: %v", err)
	}
	if readTool.calls != 0 {
		t.Fatalf("tool executions = %d, want budget rejection before registry execution", readTool.calls)
	}
	if len(calls) != 1 || calls[0].Success || !strings.Contains(calls[0].Result, "tool call budget exhausted") {
		t.Fatalf("returned calls = %+v, want one failed budget outcome", calls)
	}
	if len(result.ToolCalls) != 2 {
		t.Fatalf("retained ToolCalls = %+v, want prior call plus budget rejection", result.ToolCalls)
	}
	if !reflect.DeepEqual(result.ToolCalls[1], calls[0]) {
		t.Fatalf("retained budget call = %+v, want identical returned outcome %+v", result.ToolCalls[1], calls[0])
	}
}

func TestSubAgentExecuteTools_RetainsVerificationBudgetRejection(t *testing.T) {
	registry := tool.NewEmptyRegistry()
	verifyTool := &countingReadTool{fakeReadTool: fakeReadTool{name: "run_verification", body: "should not execute"}}
	registry.Register(verifyTool)
	agent := &SubAgent{maxVerificationCalls: 1}
	result := &SubAgentResult{ToolCalls: []SubAgentToolCall{{
		ID:      "call-verify-prev",
		Name:    "run_verification",
		Result:  "prior verification evidence",
		Success: true,
	}}}

	calls, err := agent.executeTools(context.Background(), []model.ToolCall{{
		ID: "call-verify-budget", Type: "function", Function: model.FunctionCall{Name: "run_verification", Arguments: `{"command":"go test ./pkg/rlm"}`},
	}}, registry, map[string]struct{}{"run_verification": {}}, result)
	if err != nil {
		t.Fatalf("executeTools returned error: %v", err)
	}
	if verifyTool.calls != 0 {
		t.Fatalf("tool executions = %d, want verification budget rejection before registry execution", verifyTool.calls)
	}
	if len(calls) != 1 || calls[0].Success || !strings.Contains(calls[0].Result, "verification budget exhausted") {
		t.Fatalf("returned calls = %+v, want one failed verification-budget outcome", calls)
	}
	if len(result.ToolCalls) != 2 {
		t.Fatalf("retained ToolCalls = %+v, want prior call plus verification-budget rejection", result.ToolCalls)
	}
	if !reflect.DeepEqual(result.ToolCalls[1], calls[0]) {
		t.Fatalf("retained verification call = %+v, want identical returned outcome %+v", result.ToolCalls[1], calls[0])
	}
}

func newSubAgentTestManager(t *testing.T, server *httptest.Server) *model.Manager {
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

// TestSubAgentExecute_SecondRequestCarriesToolCallAndResult is the
// cross-round transcript invariant carried over from the pkg/headless and
// pkg/ui/tui agentloop.Controller migrations (see
// TestRunner_SecondRequestCarriesToolCallAndResult and
// TestRunToolLoop_SecondRequestCarriesToolCallAndResult): after round one
// dispatches a tool, round two's request must contain the assistant
// tool-call message and its tool result, or the model loops on stale
// history. This is the sharpest check that Execute's History sink lands the
// tool exchange in `messages` before the next round's BuildRequest reads it.
func TestSubAgentExecute_SecondRequestCarriesToolCallAndResult(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) == 1 {
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-1","model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_sub_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"main.go\"}"}}]},"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}
			}`)
			return
		}
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-2","model":"gpt-4o",
			"choices":[{"index":0,"message":{"role":"assistant","content":"done reading"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":12,"completion_tokens":4,"total_tokens":16}
		}`)
	}))
	defer server.Close()

	mgr := newSubAgentTestManager(t, server)
	registry := tool.NewEmptyRegistry()
	registry.Register(fakeReadTool{name: "read_file", body: "package main"})

	agent, err := NewSubAgent(SubAgentConfig{
		ID:            "test-agent",
		Model:         "gpt-4o",
		MaxIterations: 10,
		AllowedTools:  []string{"read_file"},
	}, SubAgentDeps{Models: mgr, Registry: registry})
	if err != nil {
		t.Fatalf("NewSubAgent: %v", err)
	}

	result, err := agent.Execute(context.Background(), "read main.go")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Summary != "done reading" {
		t.Fatalf("Summary = %q, want %q", result.Summary, "done reading")
	}
	if len(result.ModelExecutions) != 2 {
		t.Fatalf("ModelExecutions = %+v, want two model responses", result.ModelExecutions)
	}
	if got := result.ModelExecutions[0]; got.RequestedModel != "gpt-4o" || got.ProviderID != "openai" || got.ResponseID != "chatcmpl-1" || got.ResponseModel != "gpt-4o" {
		t.Fatalf("first identity = %+v, want stamped first response", got)
	}
	if got := result.ModelExecutions[1]; got.ResponseID != "chatcmpl-2" || got.ProviderID != "openai" {
		t.Fatalf("second identity = %+v, want stamped final response", got)
	}
	if raw := string(result.Raw); !strings.Contains(raw, `"model_executions"`) || strings.Contains(raw, "private-reasoning-sentinel") {
		t.Fatalf("raw model execution projection = %s", raw)
	}
	if len(bodies) != 2 {
		t.Fatalf("expected 2 model requests, got %d", len(bodies))
	}

	var second map[string]any
	if err := json.Unmarshal([]byte(bodies[1]), &second); err != nil {
		t.Fatalf("decode second request: %v", err)
	}
	raw := bodies[1]
	if !strings.Contains(raw, "call_sub_1") {
		t.Fatalf("second request missing assistant tool-call message: %s", raw)
	}
	if !strings.Contains(raw, `"role":"tool"`) {
		t.Fatalf("second request missing tool result message: %s", raw)
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].Name != "read_file" {
		t.Fatalf("result.ToolCalls = %+v, want one read_file call", result.ToolCalls)
	}
}

func TestSubAgentExecute_ToollessModelOmitsToolsAndDoesNotExecuteAllowedTool(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-toolless-subagent","model":"o1-mini",
			"choices":[{"index":0,"message":{"role":"assistant","content":"direct sub-agent summary"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":8,"completion_tokens":5,"total_tokens":13}
		}`)
	}))
	defer server.Close()

	mgr := newSubAgentTestManager(t, server)
	registry := tool.NewEmptyRegistry()
	readTool := &countingReadTool{fakeReadTool: fakeReadTool{name: "read_file", body: "should not execute"}}
	registry.Register(readTool)
	agent, err := NewSubAgent(SubAgentConfig{
		ID:            "toolless-agent",
		Model:         "openai/o1-mini",
		MaxIterations: 3,
		AllowedTools:  []string{"read_file"},
	}, SubAgentDeps{Models: mgr, Registry: registry})
	if err != nil {
		t.Fatalf("NewSubAgent: %v", err)
	}

	result, err := agent.Execute(context.Background(), "summarize without tools")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Summary != "direct sub-agent summary" {
		t.Fatalf("Summary = %q, want direct sub-agent summary", result.Summary)
	}
	if readTool.calls != 0 {
		t.Fatalf("tool executions = %d, want none for catalog-confirmed toolless model", readTool.calls)
	}
	if _, ok := body["tools"]; ok {
		t.Fatalf("request included tools for catalog-confirmed toolless subagent: %v", body["tools"])
	}
	if _, ok := body["tool_choice"]; ok {
		t.Fatalf("request included tool_choice for catalog-confirmed toolless subagent: %v", body["tool_choice"])
	}
	messages, ok := body["messages"].([]any)
	if !ok || len(messages) == 0 {
		t.Fatalf("messages = %#v, want request messages", body["messages"])
	}
	system, ok := messages[0].(map[string]any)
	if !ok {
		t.Fatalf("first message = %#v, want object", messages[0])
	}
	if content, _ := system["content"].(string); !strings.Contains(content, "Never claim you read files") {
		t.Fatalf("system prompt = %q, want honest no-tool instruction", content)
	}
	if result.TokensUsed != 13 || result.InputTokens != 8 || result.OutputTokens != 5 {
		t.Fatalf("tokens = %d/%d/%d, want retained usage", result.TokensUsed, result.InputTokens, result.OutputTokens)
	}
}

func TestSubAgentExecute_TruncatedResponsePreservesDraftAndIdentity(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-truncated","model":"gpt-4o",
			"choices":[{"index":0,"message":{"role":"assistant","content":"public partial draft","reasoning":"private-reasoning-sentinel","reasoning_details":[{"type":"reasoning.text","text":"private-reasoning-sentinel"}]},"finish_reason":"length"}],
			"usage":{"prompt_tokens":7,"completion_tokens":11,"total_tokens":18}
		}`)
	}))
	defer server.Close()

	agent, err := NewSubAgent(SubAgentConfig{
		ID:            "test-agent",
		Model:         "gpt-4o",
		MaxIterations: 1,
	}, SubAgentDeps{Models: newSubAgentTestManager(t, server), Registry: tool.NewEmptyRegistry()})
	if err != nil {
		t.Fatalf("NewSubAgent: %v", err)
	}
	result, err := agent.Execute(context.Background(), "draft")
	if err == nil {
		t.Fatal("Execute() error = nil, want incomplete truncated response")
	}
	if calls != 1 {
		t.Fatalf("model calls = %d, want one retained truncated response", calls)
	}
	if result == nil || result.Summary != "public partial draft" {
		t.Fatalf("result summary = %#v, want public partial draft", result)
	}
	if result.TokensUsed != 18 || result.InputTokens != 7 || result.OutputTokens != 11 {
		t.Fatalf("tokens = %d/%d/%d, want retained partial usage", result.TokensUsed, result.InputTokens, result.OutputTokens)
	}
	if len(result.ModelExecutions) != 1 || result.ModelExecutions[0].ResponseID != "chatcmpl-truncated" {
		t.Fatalf("ModelExecutions = %+v, want truncated response identity", result.ModelExecutions)
	}
	if raw := string(result.Raw); strings.Contains(raw, "private-reasoning-sentinel") || strings.Contains(result.Summary, "private-reasoning-sentinel") {
		t.Fatalf("private reasoning leaked through summary/raw: summary=%q raw=%s", result.Summary, raw)
	}
}

type partialDeadlineModelClient struct {
	calls    int
	sawTools bool
	resp     *model.ChatResponse
}

func (c *partialDeadlineModelClient) ChatCompletion(_ context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	c.calls++
	c.sawTools = len(req.Tools) > 0
	return c.resp, context.DeadlineExceeded
}

func (c *partialDeadlineModelClient) GetContextLength(string) (int, error) { return 8192, nil }

func (c *partialDeadlineModelClient) ProviderIDForModel(string) string { return "fake-provider" }

func (c *partialDeadlineModelClient) GetPricing(string) (*model.ModelPricing, error) {
	return nil, fmt.Errorf("no pricing")
}

func (c *partialDeadlineModelClient) CalculateCostFromTokens(string, int, int) (float64, error) {
	return 0, fmt.Errorf("no pricing")
}

func TestSubAgentExecute_PartialDeadlineKeepsDraftIdentityAndDoesNotRetry(t *testing.T) {
	private := "private-reasoning-sentinel"
	fake := &partialDeadlineModelClient{
		resp: &model.ChatResponse{
			ID:    "partial-response-id",
			Model: "provider-model",
			Choices: []model.Choice{{
				Message: model.Message{
					Role:             "assistant",
					Content:          "public partial draft",
					Reasoning:        private,
					ReasoningDetails: []model.ReasoningDetail{{Type: "reasoning.text", Text: private}},
				},
				FinishReason: "stop",
			}},
			Usage:        model.Usage{PromptTokens: 3, CompletionTokens: 5, TotalTokens: 8},
			UsagePresent: true,
			ExecutionIdentity: &model.ExecutionIdentity{
				RequestedModel: "alias/model",
				SelectedModel:  "provider/model",
				ProviderID:     "fake-provider",
				ResponseModel:  "provider-model",
				ResponseID:     "partial-response-id",
			},
		},
	}
	registry := tool.NewEmptyRegistry()
	readTool := &countingReadTool{fakeReadTool: fakeReadTool{name: "read_file", body: "not reached"}}
	registry.Register(readTool)
	agent, err := NewSubAgent(SubAgentConfig{
		ID:            "test-agent",
		Model:         "alias/model",
		MaxIterations: 5,
		AllowedTools:  []string{"read_file"},
	}, SubAgentDeps{Models: &model.Manager{}, Registry: registry})
	if err != nil {
		t.Fatalf("NewSubAgent: %v", err)
	}
	agent.client = fake
	result, err := agent.Execute(context.Background(), "partial")
	if err == nil {
		t.Fatal("Execute() error = nil, want partial deadline error")
	}
	if fake.calls != 1 {
		t.Fatalf("model calls = %d, want no retry after partial deadline response", fake.calls)
	}
	if !fake.sawTools {
		t.Fatal("fake model request had no tools; test did not exercise exploration deadline branch")
	}
	if readTool.calls != 0 {
		t.Fatalf("tool executions = %d, want partial provider response to stop before dispatch", readTool.calls)
	}
	if result.Summary != "public partial draft" {
		t.Fatalf("Summary = %q, want public partial draft", result.Summary)
	}
	if result.TokensUsed != 8 || result.InputTokens != 3 || result.OutputTokens != 5 {
		t.Fatalf("tokens = %d/%d/%d, want retained partial usage", result.TokensUsed, result.InputTokens, result.OutputTokens)
	}
	if len(result.ModelExecutions) != 1 || result.ModelExecutions[0].ResponseID != "partial-response-id" {
		t.Fatalf("ModelExecutions = %+v, want partial response identity", result.ModelExecutions)
	}
	if raw := string(result.Raw); !strings.Contains(raw, "partial-response-id") || strings.Contains(raw, private) || strings.Contains(result.Summary, private) {
		t.Fatalf("Raw = %s, want retained identity without private sentinel", raw)
	}
}

func TestSubAgentExplorationDeadlineRetryPredicateKeepsLegacyNilResponseRetry(t *testing.T) {
	ctx := context.Background()
	if !shouldRetrySubAgentExplorationDeadline(ctx, true, context.DeadlineExceeded, nil) {
		t.Fatal("nil-response exploration deadline should keep the legacy retry path")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if shouldRetrySubAgentExplorationDeadline(cancelled, true, context.DeadlineExceeded, nil) {
		t.Fatal("outer context cancellation should not retry")
	}
}

type scriptedSubAgentModelClient struct {
	calls int
	steps []scriptedSubAgentModelStep
}

type scriptedSubAgentModelStep struct {
	resp *model.ChatResponse
	err  error
}

func (c *scriptedSubAgentModelClient) ChatCompletion(context.Context, model.ChatRequest) (*model.ChatResponse, error) {
	if c.calls >= len(c.steps) {
		c.calls++
		return nil, fmt.Errorf("unexpected model call")
	}
	step := c.steps[c.calls]
	c.calls++
	return step.resp, step.err
}

func (c *scriptedSubAgentModelClient) GetContextLength(string) (int, error) { return 8192, nil }

func (c *scriptedSubAgentModelClient) ProviderIDForModel(string) string { return "fake-provider" }

func (c *scriptedSubAgentModelClient) GetPricing(string) (*model.ModelPricing, error) {
	return nil, fmt.Errorf("no pricing")
}

func (c *scriptedSubAgentModelClient) CalculateCostFromTokens(string, int, int) (float64, error) {
	return 0, fmt.Errorf("no pricing")
}

type routedContextSubAgentModelClient struct {
	scriptedSubAgentModelClient
	route                model.ModelRoute
	capabilityState      model.CapabilityState
	contextLength        int
	contextLengthErr     error
	legacyContextLength  int
	requests             []model.ChatRequest
	routeCompletionCalls int
}

func (c *routedContextSubAgentModelClient) ResolveModelRoute(string) (model.ModelRoute, error) {
	return c.route, nil
}

func (c *routedContextSubAgentModelClient) ResolveParameterCapabilityForRoute(model.ModelRoute, string) model.CapabilityResolution {
	state := c.capabilityState
	if state == "" {
		state = model.CapabilitySupported
	}
	return model.CapabilityResolution{State: state}
}

func (c *routedContextSubAgentModelClient) GetContextLengthForRoute(model.ModelRoute) (int, error) {
	return c.contextLength, c.contextLengthErr
}

func (c *routedContextSubAgentModelClient) GetContextLength(string) (int, error) {
	if c.legacyContextLength > 0 {
		return c.legacyContextLength, nil
	}
	return c.scriptedSubAgentModelClient.GetContextLength("")
}

func (c *routedContextSubAgentModelClient) ChatCompletionForRoute(ctx context.Context, req model.ChatRequest, route model.ModelRoute) (*model.ChatResponse, error) {
	c.routeCompletionCalls++
	c.requests = append(c.requests, req)
	return c.scriptedSubAgentModelClient.ChatCompletion(ctx, req)
}

func TestSubAgentExecute_RouteToollessRequestCarriesCatalogMarker(t *testing.T) {
	fake := &routedContextSubAgentModelClient{
		route:           model.ModelRoute{RequestedModel: "alias/model", SelectedModel: "selected/toolless", ProviderID: "selected"},
		capabilityState: model.CapabilityNotAdvertised,
		contextLength:   4096,
		scriptedSubAgentModelClient: scriptedSubAgentModelClient{steps: []scriptedSubAgentModelStep{
			{resp: &model.ChatResponse{Choices: []model.Choice{{Message: model.Message{Role: "assistant", Content: "direct no-tool summary"}, FinishReason: "stop"}}}},
		}},
	}
	registry := tool.NewEmptyRegistry()
	readTool := &countingReadTool{fakeReadTool: fakeReadTool{name: "read_file", body: "not reached"}}
	registry.Register(readTool)
	agent, err := NewSubAgent(SubAgentConfig{
		ID:            "route-toolless-agent",
		Model:         "alias/model",
		MaxIterations: 3,
		AllowedTools:  []string{"read_file"},
	}, SubAgentDeps{Models: &model.Manager{}, Registry: registry})
	if err != nil {
		t.Fatalf("NewSubAgent: %v", err)
	}
	agent.client = fake

	result, err := agent.Execute(context.Background(), "summarize without tools")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Summary != "direct no-tool summary" {
		t.Fatalf("Summary = %q, want direct no-tool summary", result.Summary)
	}
	if readTool.calls != 0 {
		t.Fatalf("tool executions = %d, want none for route-derived toolless subagent", readTool.calls)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("requests = %d, want one", len(fake.requests))
	}
	req := fake.requests[0]
	if len(req.Tools) != 0 || req.ToolChoice != "" {
		t.Fatalf("request tools=%v tool_choice=%q, want omitted", req.Tools, req.ToolChoice)
	}
	if !req.ToolsCatalogConfirmedUnavailable {
		t.Fatal("route-derived toolless request missing catalog-confirmed marker")
	}
}

func TestSubAgentExecute_CompactsWithRouteBoundContextLength(t *testing.T) {
	fake := &routedContextSubAgentModelClient{
		route:               model.ModelRoute{RequestedModel: "alias/model", SelectedModel: "selected/provider-model", ProviderID: "selected"},
		contextLength:       4096,
		legacyContextLength: 200_000,
		scriptedSubAgentModelClient: scriptedSubAgentModelClient{steps: []scriptedSubAgentModelStep{
			{resp: &model.ChatResponse{Choices: []model.Choice{{
				Message: model.Message{Role: "assistant", ToolCalls: []model.ToolCall{{
					ID: "call_read", Type: "function", Function: model.FunctionCall{Name: "read_file", Arguments: `{"path":"a.go"}`},
				}}},
				FinishReason: "tool_calls",
			}}}},
			{resp: &model.ChatResponse{Choices: []model.Choice{{Message: model.Message{Role: "assistant", Content: "done"}, FinishReason: "stop"}}}},
		}},
	}
	registry := tool.NewEmptyRegistry()
	registry.Register(fakeReadTool{name: "read_file", body: "package a"})
	agent, err := NewSubAgent(SubAgentConfig{
		ID:            "route-context-agent",
		Model:         "alias/model",
		MaxIterations: 5,
		AllowedTools:  []string{"read_file"},
	}, SubAgentDeps{Models: &model.Manager{}, Registry: registry})
	if err != nil {
		t.Fatalf("NewSubAgent: %v", err)
	}
	agent.client = fake

	result, err := agent.Execute(context.Background(), strings.Repeat("route-bound-context ", 2500))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Summary != "done" {
		t.Fatalf("Summary = %q, want done", result.Summary)
	}
	if fake.routeCompletionCalls != 2 {
		t.Fatalf("route completion calls = %d, want 2", fake.routeCompletionCalls)
	}
	if len(fake.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(fake.requests))
	}
	second := fmt.Sprint(fake.requests[1].Messages)
	if !strings.Contains(second, "user message compacted") {
		t.Fatalf("second request was not compacted with selected route context: %s", second)
	}
}

func TestSubAgentContextLengthFallsBackWhenRouteMetadataUnavailable(t *testing.T) {
	fake := &routedContextSubAgentModelClient{
		route:               model.ModelRoute{RequestedModel: "alias/model", SelectedModel: "selected/provider-model", ProviderID: "selected"},
		contextLengthErr:    fmt.Errorf("metadata unavailable"),
		legacyContextLength: 12345,
	}
	agent := &SubAgent{model: "alias/model", client: fake}
	if got := agent.contextLength(fake.route, true); got != 12345 {
		t.Fatalf("contextLength fallback = %d, want legacy context length", got)
	}
}

func TestSubAgentExecute_ReplacesCumulativeIdentityAcrossExplorationRetry(t *testing.T) {
	firstIdentity := model.ExecutionIdentity{RequestedModel: "alias/model", SelectedModel: "provider/model", ProviderID: "fake-provider", ResponseModel: "provider-model", ResponseID: "first-tool-response"}
	finalIdentity := model.ExecutionIdentity{RequestedModel: "alias/model", SelectedModel: "provider/model", ProviderID: "fake-provider", ResponseModel: "provider-model", ResponseID: "final-response"}
	fake := &scriptedSubAgentModelClient{steps: []scriptedSubAgentModelStep{
		{resp: &model.ChatResponse{
			Choices: []model.Choice{{
				Message: model.Message{Role: "assistant", ToolCalls: []model.ToolCall{{
					ID: "call_read", Type: "function", Function: model.FunctionCall{Name: "read_file", Arguments: `{"path":"a.go"}`},
				}}},
				FinishReason: "tool_calls",
			}},
			Usage:             model.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
			UsagePresent:      true,
			ExecutionIdentity: &firstIdentity,
		}},
		{err: context.DeadlineExceeded},
		{resp: &model.ChatResponse{
			Choices:           []model.Choice{{Message: model.Message{Role: "assistant", Content: "final answer"}, FinishReason: "stop"}},
			Usage:             model.Usage{PromptTokens: 12, CompletionTokens: 4, TotalTokens: 16},
			ExecutionIdentity: &finalIdentity,
		}},
	}}
	registry := tool.NewEmptyRegistry()
	readTool := &countingReadTool{fakeReadTool: fakeReadTool{name: "read_file", body: "package a"}}
	registry.Register(readTool)
	agent, err := NewSubAgent(SubAgentConfig{
		ID:            "test-agent",
		Model:         "alias/model",
		MaxIterations: 5,
		AllowedTools:  []string{"read_file"},
	}, SubAgentDeps{Models: &model.Manager{}, Registry: registry})
	if err != nil {
		t.Fatalf("NewSubAgent: %v", err)
	}
	agent.client = fake
	result, err := agent.Execute(context.Background(), "read then answer")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if fake.calls != 3 {
		t.Fatalf("model calls = %d, want first response, nil-response retry, final response", fake.calls)
	}
	if readTool.calls != 1 {
		t.Fatalf("tool executions = %d, want one successful tool call", readTool.calls)
	}
	if result.Summary != "final answer" {
		t.Fatalf("Summary = %q, want final answer", result.Summary)
	}
	if result.TokensUsed != 31 || result.InputTokens != 22 || result.OutputTokens != 9 {
		t.Fatalf("tokens = %d/%d/%d, want first+final usage once", result.TokensUsed, result.InputTokens, result.OutputTokens)
	}
	if got := result.ModelExecutions; len(got) != 2 || got[0] != firstIdentity || got[1] != finalIdentity {
		t.Fatalf("ModelExecutions = %+v, want first and final identities without duplicate retry snapshot", got)
	}
}

// TestSubAgentExecute_AccumulatesTokensAndToolCallsAcrossRounds is the
// accumulated-state invariant: after two tool rounds and a final answer,
// SubAgentResult must reflect every round's token usage and every
// dispatched tool call, not just the last round's, and the final request
// must still carry both prior tool exchanges.
func TestSubAgentExecute_AccumulatesTokensAndToolCallsAcrossRounds(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "application/json")
		switch len(bodies) {
		case 1:
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-1","model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_a","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a.go\"}"}}]},"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}
			}`)
		case 2:
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-2","model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_b","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"b.go\"}"}}]},"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":20,"completion_tokens":6,"total_tokens":26}
			}`)
		default:
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-3","model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","content":"both files read"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":30,"completion_tokens":7,"total_tokens":37}
			}`)
		}
	}))
	defer server.Close()

	mgr := newSubAgentTestManager(t, server)
	registry := tool.NewEmptyRegistry()
	registry.Register(fakeReadTool{name: "read_file", body: "ok"})

	agent, err := NewSubAgent(SubAgentConfig{
		ID:            "test-agent",
		Model:         "gpt-4o",
		MaxIterations: 10,
		AllowedTools:  []string{"read_file"},
	}, SubAgentDeps{Models: mgr, Registry: registry})
	if err != nil {
		t.Fatalf("NewSubAgent: %v", err)
	}

	result, err := agent.Execute(context.Background(), "read both files")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Summary != "both files read" {
		t.Fatalf("Summary = %q, want %q", result.Summary, "both files read")
	}
	if len(bodies) != 3 {
		t.Fatalf("expected 3 model requests, got %d", len(bodies))
	}
	if len(result.ToolCalls) != 2 {
		t.Fatalf("result.ToolCalls = %+v, want 2 calls", result.ToolCalls)
	}
	if result.InputTokens != 60 { // 10 + 20 + 30
		t.Errorf("InputTokens = %d, want 60", result.InputTokens)
	}
	if result.OutputTokens != 18 { // 5 + 6 + 7
		t.Errorf("OutputTokens = %d, want 18", result.OutputTokens)
	}
	if result.TokensUsed != 78 { // 15 + 26 + 37
		t.Errorf("TokensUsed = %d, want 78", result.TokensUsed)
	}

	// The final request must still carry both prior tool exchanges.
	final := bodies[2]
	if !strings.Contains(final, "call_a") || !strings.Contains(final, "call_b") {
		t.Fatalf("final request missing accumulated tool calls: %s", final)
	}
}

func TestSubAgentExecute_GuardFinalizesAndPreservesTermination(t *testing.T) {
	const guardedToolCalls = subAgentGovernorCycleRepeats
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) <= guardedToolCalls {
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-tool","model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_repeat","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"main.go\"}"}}]},"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}
			}`)
			return
		}
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-final","model":"gpt-4o",
			"choices":[{"index":0,"message":{"role":"assistant","content":"synthesized repeated evidence"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":30,"completion_tokens":10,"total_tokens":40}
		}`)
	}))
	defer server.Close()

	mgr := newSubAgentTestManager(t, server)
	registry := tool.NewEmptyRegistry()
	registry.Register(fakeReadTool{name: "read_file", body: "same evidence"})
	agent, err := NewSubAgent(SubAgentConfig{
		ID: "guard-agent", Model: "gpt-4o", MaxIterations: 20, AllowedTools: []string{"read_file"},
	}, SubAgentDeps{Models: mgr, Registry: registry})
	if err != nil {
		t.Fatalf("NewSubAgent: %v", err)
	}

	result, err := agent.Execute(context.Background(), "inspect main.go")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Summary != "synthesized repeated evidence" {
		t.Fatalf("Summary = %q, want final synthesis", result.Summary)
	}
	if result.TerminationKind != "action_cycle" || !strings.Contains(result.TerminationReason, "repeating 1-step cycle") {
		t.Fatalf("termination = %q/%q", result.TerminationKind, result.TerminationReason)
	}
	if !result.FinalizationAttempted || result.FinalizationError != "" {
		t.Fatalf("finalization metadata = tried %v error %q", result.FinalizationAttempted, result.FinalizationError)
	}
	if len(result.ToolCalls) != guardedToolCalls || len(bodies) != guardedToolCalls+1 {
		t.Fatalf("tool/model calls = %d/%d, want %d/%d", len(result.ToolCalls), len(bodies), guardedToolCalls, guardedToolCalls+1)
	}
	if result.TokensUsed != guardedToolCalls*15+40 {
		t.Fatalf("TokensUsed = %d, want finalization usage included", result.TokensUsed)
	}
	if !strings.Contains(string(result.Raw), `"termination_kind":"action_cycle"`) {
		t.Fatalf("raw result omitted termination: %s", result.Raw)
	}
	if !strings.Contains(bodies[len(bodies)-1], "stopped further tool execution") {
		t.Fatalf("finalization request omitted stop context: %s", bodies[len(bodies)-1])
	}
}
