package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/mock/gomock"
	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	orchmocks "m31labs.dev/buckley/pkg/orchestrator/mocks"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

type builderRouteCaptureClient struct {
	*model.Manager
	requests []model.ChatRequest
	routes   []model.ModelRoute
}

func (c *builderRouteCaptureClient) ChatCompletionForRoute(ctx context.Context, req model.ChatRequest, route model.ModelRoute) (*model.ChatResponse, error) {
	c.requests = append(c.requests, req)
	c.routes = append(c.routes, route)
	return c.Manager.ChatCompletionForRoute(ctx, req, route)
}

type builderRouteProbeTool struct {
	name  string
	calls int
}

func (t *builderRouteProbeTool) Name() string { return t.name }

func (t *builderRouteProbeTool) Description() string { return "route-binding probe" }

func (t *builderRouteProbeTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{Type: "object"}
}

func (t *builderRouteProbeTool) Execute(map[string]any) (*builtin.Result, error) {
	t.calls++
	return &builtin.Result{Success: true, Data: map[string]any{"ok": true}}, nil
}

func newBuilderRouteBindingManager(t *testing.T, server *httptest.Server, hook model.RoutingHook) *model.Manager {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Models.Execution = "builder-alias"
	cfg.Models.DefaultProvider = "openai"
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = server.URL
	cfg.Providers.ModelRouting["builder-alias"] = "openai"

	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	mgr.RoutingHooks().Register(hook)
	return mgr
}

func newBuilderRouteBindingAgent(cfg *config.Config, client ModelClient, probe *builderRouteProbeTool) *BuilderAgent {
	registry := tool.NewEmptyRegistry()
	registry.Register(probe)
	agent := NewBuilderAgent(&Plan{ID: "builder-route-binding", FeatureName: "Route binding"}, cfg, client, registry, nil)
	agent.SetResolver(model.NewResolver(nil, model.ResolverConfig{Execution: cfg.Models.Execution}, client))
	return agent
}

func TestBuilderRouteBinding_ToollessRouteOmitsToolsAndRejectsSurpriseCall(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-toolless-builder","model":"o1-mini","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call-surprise","type":"function","function":{"name":"modify_probe","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`)
	}))
	defer server.Close()

	hookCalls := 0
	mgr := newBuilderRouteBindingManager(t, server, func(decision *model.RoutingDecision) *model.RoutingDecision {
		hookCalls++
		decision.SelectedModel = "openai/o1-mini"
		return decision
	})
	client := &builderRouteCaptureClient{Manager: mgr}
	probe := &builderRouteProbeTool{name: "modify_probe"}
	agent := newBuilderRouteBindingAgent(builderRouteBindingConfig(), client, probe)

	_, err := agent.generateImplementation(&Task{ID: "task-toolless", Title: "toolless", Description: "do not modify"})
	var incomplete *agentloop.IncompleteTurnError
	if !errors.As(err, &incomplete) || incomplete.Code != "unoffered_tool_call" {
		t.Fatalf("error = %v, want unoffered_tool_call incomplete result", err)
	}
	if probe.calls != 0 {
		t.Fatalf("modifying tool executions = %d, want zero", probe.calls)
	}
	if len(client.requests) != 1 || !client.requests[0].ToolsCatalogConfirmedUnavailable {
		t.Fatalf("pre-wire requests = %+v, want one catalog-toolless marker", client.requests)
	}
	if req := client.requests[0]; req.Model != "builder-alias" || req.Route != (model.ModelRoute{RequestedModel: "builder-alias", SelectedModel: "openai/o1-mini", ProviderID: "openai"}) {
		t.Fatalf("pre-wire route request = %+v, want pinned builder alias route", req)
	}
	if _, ok := body["tools"]; ok {
		t.Fatalf("catalog-toolless request included tools: %#v", body["tools"])
	}
	if _, ok := body["tool_choice"]; ok {
		t.Fatalf("catalog-toolless request included tool_choice: %#v", body["tool_choice"])
	}
	if body["model"] != "o1-mini" {
		t.Fatalf("wire model = %#v, want selected o1-mini", body["model"])
	}
	if got := countBuilderSystemInstruction(body, builderToollessInstruction); got != 1 {
		t.Fatalf("honest no-tools instruction count = %d, want 1", got)
	}
	if hookCalls != 2 {
		t.Fatalf("routing hook calls = %d, want one route resolution and one dispatch check", hookCalls)
	}
}

func TestBuilderRouteBinding_ToollessUnsupportedToolsErrorDoesNotRetry(t *testing.T) {
	requests := 0
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeBuilderUnsupportedToolsError(w)
	}))
	defer server.Close()

	mgr := newBuilderRouteBindingManager(t, server, func(decision *model.RoutingDecision) *model.RoutingDecision {
		decision.SelectedModel = "openai/o1-mini"
		return decision
	})
	client := &builderRouteCaptureClient{Manager: mgr}
	probe := &builderRouteProbeTool{name: "modify_probe"}
	agent := newBuilderRouteBindingAgent(builderRouteBindingConfig(), client, probe)

	_, err := agent.generateImplementation(&Task{ID: "task-toolless-error", Title: "toolless error", Description: "return the provider error"})
	if err == nil || !strings.Contains(err.Error(), "does not support tools") {
		t.Fatalf("error = %v, want original unsupported-tools failure", err)
	}
	if requests != 1 {
		t.Fatalf("provider requests = %d, want one with no incompatible retry", requests)
	}
	if len(client.requests) != 1 || !client.requests[0].ToolsCatalogConfirmedUnavailable {
		t.Fatalf("pre-wire requests = %+v, want one catalog-toolless request", client.requests)
	}
	if _, ok := body["tools"]; ok {
		t.Fatalf("catalog-toolless error request included tools: %#v", body["tools"])
	}
	if _, ok := body["tool_choice"]; ok {
		t.Fatalf("catalog-toolless error request included tool_choice: %#v", body["tool_choice"])
	}
	if probe.calls != 0 {
		t.Fatalf("modifying tool executions = %d, want zero", probe.calls)
	}
}

func TestBuilderRouteBinding_HookDriftStopsBeforeProviderDispatch(t *testing.T) {
	providerCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	hookCalls := 0
	mgr := newBuilderRouteBindingManager(t, server, func(decision *model.RoutingDecision) *model.RoutingDecision {
		hookCalls++
		if hookCalls == 1 {
			decision.SelectedModel = "openai/o1-mini"
		} else {
			decision.SelectedModel = "openai/gpt-4o"
		}
		return decision
	})
	probe := &builderRouteProbeTool{name: "modify_probe"}
	agent := newBuilderRouteBindingAgent(builderRouteBindingConfig(), mgr, probe)

	_, err := agent.generateImplementation(&Task{ID: "task-drift", Title: "drift", Description: "no provider call"})
	if err == nil || !strings.Contains(err.Error(), "model route changed before dispatch") {
		t.Fatalf("error = %v, want route drift before dispatch", err)
	}
	if providerCalls != 0 {
		t.Fatalf("provider requests = %d, want zero after route drift", providerCalls)
	}
	if hookCalls != 2 {
		t.Fatalf("routing hook calls = %d, want route resolution plus dispatch check", hookCalls)
	}
}

func TestBuilderRouteBinding_CapableRouteOffersToolsAndExecutesOnce(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make(map[string]any)
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) == 1 {
			_, _ = io.WriteString(w, `{"id":"chatcmpl-capable-1","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call-allowed","type":"function","function":{"name":"modify_probe","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"chatcmpl-capable-2","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}}`)
	}))
	defer server.Close()

	mgr := newBuilderRouteBindingManager(t, server, func(decision *model.RoutingDecision) *model.RoutingDecision {
		decision.SelectedModel = "openai/gpt-4o"
		return decision
	})
	probe := &builderRouteProbeTool{name: "modify_probe"}
	agent := newBuilderRouteBindingAgent(builderRouteBindingConfig(), mgr, probe)

	out, err := agent.generateImplementation(&Task{ID: "task-capable", Title: "capable", Description: "execute exactly once"})
	if err != nil {
		t.Fatalf("generateImplementation: %v", err)
	}
	if out != "done" {
		t.Fatalf("output = %q, want done", out)
	}
	if probe.calls != 1 {
		t.Fatalf("modifying tool executions = %d, want one", probe.calls)
	}
	if len(bodies) != 2 {
		t.Fatalf("provider requests = %d, want two", len(bodies))
	}
	if _, ok := bodies[0]["tools"]; !ok {
		t.Fatalf("capable route omitted tool schemas")
	}
	if bodies[0]["tool_choice"] != "auto" {
		t.Fatalf("capable route tool_choice = %#v, want auto", bodies[0]["tool_choice"])
	}
	if got := countBuilderSystemInstruction(bodies[0], builderToollessInstruction); got != 0 {
		t.Fatalf("capable route honest no-tools instruction count = %d, want zero", got)
	}
}

func TestBuilderRouteBinding_CapableUnsupportedToolsRetryPinsRouteAndRejectsSurpriseCall(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make(map[string]any)
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		bodies = append(bodies, body)
		if len(bodies) == 1 {
			writeBuilderUnsupportedToolsError(w)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-capable-retry","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call-retry-surprise","type":"function","function":{"name":"modify_probe","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`)
	}))
	defer server.Close()

	hookCalls := 0
	mgr := newBuilderRouteBindingManager(t, server, func(decision *model.RoutingDecision) *model.RoutingDecision {
		hookCalls++
		decision.SelectedModel = "openai/gpt-4o"
		return decision
	})
	client := &builderRouteCaptureClient{Manager: mgr}
	probe := &builderRouteProbeTool{name: "modify_probe"}
	agent := newBuilderRouteBindingAgent(builderRouteBindingConfig(), client, probe)

	_, err := agent.generateImplementation(&Task{ID: "task-capable-retry", Title: "capable retry", Description: "retry without tools"})
	var incomplete *agentloop.IncompleteTurnError
	if !errors.As(err, &incomplete) || incomplete.Code != "unoffered_tool_call" {
		t.Fatalf("error = %v, want unoffered_tool_call from retry response", err)
	}
	if probe.calls != 0 {
		t.Fatalf("modifying tool executions = %d, want Controller rejection before execute", probe.calls)
	}
	if len(bodies) != 2 {
		t.Fatalf("provider requests = %d, want initial schema request and one retry", len(bodies))
	}
	if _, ok := bodies[0]["tools"]; !ok || bodies[0]["tool_choice"] != "auto" {
		t.Fatalf("initial capable request = %#v, want schemas and tool_choice:auto", bodies[0])
	}
	if _, ok := bodies[1]["tools"]; ok {
		t.Fatalf("retry wire request included schemas: %#v", bodies[1])
	}
	wantRoute := model.ModelRoute{RequestedModel: "builder-alias", SelectedModel: "openai/gpt-4o", ProviderID: "openai"}
	if len(client.requests) != 2 || len(client.routes) != 2 {
		t.Fatalf("captured routed dispatches requests=%d routes=%d, want two", len(client.requests), len(client.routes))
	}
	for i := range client.requests {
		if client.requests[i].Model != wantRoute.RequestedModel || client.requests[i].Route != wantRoute || client.routes[i] != wantRoute {
			t.Fatalf("routed dispatch %d request=%+v route=%+v, want pinned %+v", i, client.requests[i], client.routes[i], wantRoute)
		}
	}
	if len(client.requests[1].Tools) != 0 || client.requests[1].ToolChoice != "none" {
		t.Fatalf("pre-wire retry = %+v, want no schemas and tool_choice:none", client.requests[1])
	}
	if hookCalls != 3 {
		t.Fatalf("routing hook calls = %d, want resolution plus initial/retry dispatch checks", hookCalls)
	}
}

func TestBuilderRouteBinding_CapableUnsupportedToolsRetryDriftStopsBeforeSecondProviderRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests != 1 {
			t.Fatalf("unexpected retry provider request %d", requests)
		}
		writeBuilderUnsupportedToolsError(w)
	}))
	defer server.Close()

	hookCalls := 0
	mgr := newBuilderRouteBindingManager(t, server, func(decision *model.RoutingDecision) *model.RoutingDecision {
		hookCalls++
		if hookCalls <= 2 {
			decision.SelectedModel = "openai/gpt-4o"
		} else {
			decision.SelectedModel = "openai/o1-mini"
		}
		return decision
	})
	client := &builderRouteCaptureClient{Manager: mgr}
	probe := &builderRouteProbeTool{name: "modify_probe"}
	agent := newBuilderRouteBindingAgent(builderRouteBindingConfig(), client, probe)

	_, err := agent.generateImplementation(&Task{ID: "task-capable-retry-drift", Title: "capable retry drift", Description: "stop before retry I/O"})
	if err == nil || !strings.Contains(err.Error(), "model route changed before dispatch") {
		t.Fatalf("error = %v, want retry route drift failure", err)
	}
	if requests != 1 {
		t.Fatalf("provider requests = %d, want first request only", requests)
	}
	if len(client.requests) != 2 || len(client.routes) != 2 {
		t.Fatalf("captured routed dispatches requests=%d routes=%d, want initial and stopped retry", len(client.requests), len(client.routes))
	}
	if client.requests[1].ToolChoice != "none" || len(client.requests[1].Tools) != 0 {
		t.Fatalf("retry before route check = %+v, want no schemas and tool_choice:none", client.requests[1])
	}
	if client.routes[0] != client.routes[1] || client.routes[0].SelectedModel != "openai/gpt-4o" {
		t.Fatalf("retry route = %+v, want original pinned route %+v", client.routes[1], client.routes[0])
	}
	if hookCalls != 3 {
		t.Fatalf("routing hook calls = %d, want resolution plus initial/retry dispatch checks", hookCalls)
	}
}

func TestBuilderRouteBinding_UnknownRouteRemainsToolEligible(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-unknown-builder","model":"future-unlisted","choices":[{"index":0,"message":{"role":"assistant","content":"unknown remains eligible"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`)
	}))
	defer server.Close()

	mgr := newBuilderRouteBindingManager(t, server, func(decision *model.RoutingDecision) *model.RoutingDecision {
		decision.SelectedModel = "openai/future-unlisted"
		return decision
	})
	client := &builderRouteCaptureClient{Manager: mgr}
	probe := &builderRouteProbeTool{name: "modify_probe"}
	agent := newBuilderRouteBindingAgent(builderRouteBindingConfig(), client, probe)

	out, err := agent.generateImplementation(&Task{ID: "task-unknown", Title: "unknown", Description: "keep tools eligible"})
	if err != nil {
		t.Fatalf("generateImplementation: %v", err)
	}
	if out != "unknown remains eligible" {
		t.Fatalf("output = %q", out)
	}
	if len(client.requests) != 1 || client.requests[0].ToolsCatalogConfirmedUnavailable {
		t.Fatalf("pre-wire requests = %+v, want no catalog-negative marker", client.requests)
	}
	if _, ok := body["tools"]; !ok {
		t.Fatalf("unknown route omitted tool schemas")
	}
	if body["tool_choice"] != "auto" {
		t.Fatalf("unknown route tool_choice = %#v, want auto", body["tool_choice"])
	}
	if got := countBuilderSystemInstruction(body, builderToollessInstruction); got != 0 {
		t.Fatalf("unknown route honest no-tools instruction count = %d, want zero", got)
	}
}

func TestBuilderRouteBinding_LegacyMockFallbackUsesGenericDispatch(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	client := orchmocks.NewMockModelClient(ctrl)
	client.EXPECT().SupportsReasoning("legacy-alias").Return(false)
	client.EXPECT().ChatCompletion(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
		if req.Model != "legacy-alias" {
			t.Fatalf("generic request model = %q, want legacy-alias", req.Model)
		}
		if req.Route != (model.ModelRoute{}) {
			t.Fatalf("generic fallback unexpectedly attached a route: %+v", req.Route)
		}
		return &model.ChatResponse{Choices: []model.Choice{{Message: model.Message{Role: "assistant", Content: "legacy done"}}}}, nil
	})

	cfg := config.DefaultConfig()
	cfg.Models.Execution = "legacy-alias"
	probe := &builderRouteProbeTool{name: "modify_probe"}
	agent := newBuilderRouteBindingAgent(cfg, client, probe)
	out, err := agent.generateImplementation(&Task{ID: "task-legacy", Title: "legacy", Description: "generic fallback"})
	if err != nil {
		t.Fatalf("generateImplementation: %v", err)
	}
	if out != "legacy done" {
		t.Fatalf("output = %q, want legacy done", out)
	}
}

func countBuilderSystemInstruction(body map[string]any, instruction string) int {
	messages, _ := body["messages"].([]any)
	count := 0
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		if message["role"] == "system" && message["content"] == instruction {
			count++
		}
	}
	return count
}

func writeBuilderUnsupportedToolsError(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = io.WriteString(w, `{"error":{"message":"provider does not support tools"}}`)
}

func builderRouteBindingConfig() *config.Config {
	return &config.Config{
		Models:   config.ModelConfig{Execution: "builder-alias"},
		Encoding: config.EncodingConfig{UseToon: false},
	}
}
