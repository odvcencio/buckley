package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"m31labs.dev/buckley/pkg/acp"
	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/rules"
	"m31labs.dev/buckley/pkg/tool"
)

func TestACPModelCanUseTools_OffersUnknownRoute(t *testing.T) {
	unknownCfg, unknownMgr, unknownModel := newACPReasoningTestManager(t)
	_ = unknownCfg
	unknownRoute, err := unknownMgr.ResolveModelRoute(unknownModel)
	if err != nil {
		t.Fatalf("ResolveModelRoute unknown: %v", err)
	}
	if !acpModelCanUseTools(tool.NewEmptyRegistry(), unknownMgr, unknownRoute) {
		t.Fatal("unknown route should remain tool-eligible")
	}

}

func TestRejectACPUnexpectedToolCall_DoesNotExecute(t *testing.T) {
	state := &acpLoopState{}
	collector := &collectingStream{}
	outcome := rejectACPUnexpectedToolCall(collector.fn, model.ToolCall{
		ID: "call-surprise",
		Function: model.FunctionCall{
			Name:      "write_file",
			Arguments: `{"path":"unsafe"}`,
		},
	}, 1, 1, state, t.TempDir())
	if outcome.Success || outcome.EffectClass != "control" {
		t.Fatalf("outcome = %+v, want failed control", outcome)
	}
	if state.toolsExecuted {
		t.Fatal("unexpected tool call marked toolsExecuted")
	}
	if len(collector.updates) < 2 {
		t.Fatalf("updates = %d, want visible start and failed control outcome", len(collector.updates))
	}
}

type acpToolOfferWireRequest struct {
	Model             string           `json:"model"`
	Messages          []model.Message  `json:"messages"`
	Tools             []map[string]any `json:"tools"`
	ToolChoice        string           `json:"tool_choice"`
	ParallelToolCalls *bool            `json:"parallel_tool_calls"`
	fields            map[string]bool
}

func readACPToolOfferWireRequest(t *testing.T, r *http.Request) acpToolOfferWireRequest {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		t.Fatalf("decode provider request: %v", err)
	}
	payload, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal captured provider request: %v", err)
	}
	var request acpToolOfferWireRequest
	if err := json.Unmarshal(payload, &request); err != nil {
		t.Fatalf("decode captured provider request: %v", err)
	}
	request.fields = make(map[string]bool, len(raw))
	for field := range raw {
		request.fields[field] = true
	}
	return request
}

func (r acpToolOfferWireRequest) hasField(field string) bool {
	return r.fields[field]
}

func (r acpToolOfferWireRequest) hasToolHistory() bool {
	for _, message := range r.Messages {
		if message.Role == "tool" || len(message.ToolCalls) > 0 {
			return true
		}
	}
	return false
}

func assertACPToolOfferWireOmitsTools(t *testing.T, ordinal int, request acpToolOfferWireRequest) {
	t.Helper()
	for _, field := range []string{"tools", "tool_choice", "parallel_tool_calls"} {
		if request.hasField(field) {
			t.Fatalf("wire request %d unexpectedly includes %q: %+v", ordinal, field, request)
		}
	}
	for _, definition := range request.Tools {
		function, _ := definition["function"].(map[string]any)
		if function["name"] == "_noop" {
			t.Fatalf("wire request %d unexpectedly includes compatible _noop tool: %+v", ordinal, request)
		}
	}
}

func TestRunACPLoop_RouteBoundCapableAliasUsesSelectedContextAndParallelPolicy(t *testing.T) {
	var (
		mu       sync.Mutex
		requests []acpToolOfferWireRequest
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"alias-capable"},{"id":"selected-capable"}]}`)
			return
		case "/chat/completions":
			var request acpToolOfferWireRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode provider request: %v", err)
				return
			}
			mu.Lock()
			requests = append(requests, request)
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: "+
				`{"id":"route-capable","model":"selected-capable","choices":[{"index":0,"delta":{"content":"selected route answer"},"finish_reason":"stop"}]}`+
				"\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			return
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	cfg := config.DefaultConfig()
	cfg.Providers.OpenAICompatible.Enabled = true
	cfg.Providers.OpenAICompatible.APIKey = "test-key"
	cfg.Providers.OpenAICompatible.BaseURL = server.URL
	cfg.Providers.OpenAICompatible.Models = []string{"alias-capable", "selected-capable"}
	cfg.Providers.OpenAICompatible.SupportedParameters = map[string][]string{
		"alias-capable":    {"tools"},
		"selected-capable": {"tools", "parallel_tool_calls"},
	}
	cfg.Providers.OpenAICompatible.ContextLengths = map[string]int{
		"alias-capable":    128 * 1024,
		"selected-capable": 8 * 1024,
	}
	cfg.Models.DefaultProvider = "openai_compatible"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	selectedRoute := model.ModelRoute{RequestedModel: "alias-capable", SelectedModel: "openai_compatible/selected-capable", ProviderID: "openai_compatible"}
	if contextWindow, err := mgr.GetContextLengthForRoute(selectedRoute); err != nil || contextWindow != 8*1024 {
		t.Fatalf("selected route context = %d, %v, want 8192", contextWindow, err)
	}
	var hookCalls atomic.Int32
	mgr.RoutingHooks().Register(func(decision *model.RoutingDecision) *model.RoutingDecision {
		if decision != nil && decision.RequestedModel == "alias-capable" {
			hookCalls.Add(1)
			decision.SelectedModel = "openai_compatible/selected-capable"
		}
		return decision
	})
	engine, err := rules.NewDefaultEngine()
	if err != nil {
		t.Fatalf("rules.NewDefaultEngine: %v", err)
	}
	registry := tool.NewEmptyRegistry()
	registry.Register(&acpStateTestTool{name: "write_file", metadata: tool.ToolMetadata{Impact: tool.ImpactModifying}})
	conv := conversation.New("route-capable")
	conv.AddUserMessage(strings.Repeat("obsolete alias-context evidence ", 2_500))
	conv.AddAssistantMessage("intermediate context retained for projection")
	conv.AddUserMessage("most recent route-bound evidence")

	text, err := runACPLoop(context.Background(), cfg, mgr, conv, registry, nil, engine, "alias-capable", "", "route-capable", nil, func(string, ...interface{}) {}, nil)
	if err != nil {
		t.Fatalf("runACPLoop: %v", err)
	}
	if text != "selected route answer" {
		t.Fatalf("text = %q, want selected route answer", text)
	}

	mu.Lock()
	captured := append([]acpToolOfferWireRequest(nil), requests...)
	mu.Unlock()
	if len(captured) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(captured))
	}
	req := captured[0]
	if req.Model != "selected-capable" {
		t.Fatalf("wire model = %q, want selected-capable", req.Model)
	}
	if len(req.Tools) != 1 || req.ToolChoice != "auto" {
		t.Fatalf("wire tools = %d, choice=%q, want selected-route schema offer", len(req.Tools), req.ToolChoice)
	}
	if req.ParallelToolCalls == nil || *req.ParallelToolCalls {
		t.Fatalf("wire parallel_tool_calls = %v, want false from selected route", req.ParallelToolCalls)
	}
	estimate := model.EstimateRequestTokens(model.ChatRequest{
		Model:             req.Model,
		Messages:          req.Messages,
		Tools:             req.Tools,
		ToolChoice:        req.ToolChoice,
		ParallelToolCalls: req.ParallelToolCalls,
	})
	if estimate.Total > 8*1024 {
		t.Fatalf("wire request estimate = %d, want selected route's 8192-token context bound", estimate.Total)
	}
	if calls := hookCalls.Load(); calls != 2 {
		t.Fatalf("routing hook calls = %d, want resolve plus route-locked dispatch only", calls)
	}
}

func TestRunACPLoop_RouteBoundToollessAliasStopsUnofferedToolCallWithoutSideEffects(t *testing.T) {
	var (
		mu       sync.Mutex
		requests []acpToolOfferWireRequest
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"selected-toolless","supported_parameters":[]}]}`)
			return
		case "/chat/completions":
			request := readACPToolOfferWireRequest(t, r)
			mu.Lock()
			requests = append(requests, request)
			round := len(requests)
			mu.Unlock()
			if round != 1 {
				t.Errorf("unexpected provider retry request %d", round)
				http.Error(w, "unexpected retry", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: "+
				`{"id":"unexpected-tool","model":"selected-toolless","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-surprise","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"unsafe\"}"}}]},"finish_reason":"tool_calls"}]}`+
				"\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			return
		default:
			http.NotFound(w, r)
			return
		}
	}))
	t.Cleanup(server.Close)

	cfg := config.DefaultConfig()
	cfg.Providers.OpenAICompatible.Enabled = true
	cfg.Providers.OpenAICompatible.APIKey = "test-key"
	cfg.Providers.OpenAICompatible.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai_compatible"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	mgr.RoutingHooks().Register(func(decision *model.RoutingDecision) *model.RoutingDecision {
		if decision != nil && decision.RequestedModel == "toolless-alias" {
			decision.SelectedModel = "openai_compatible/selected-toolless"
		}
		return decision
	})
	engine, err := rules.NewDefaultEngine()
	if err != nil {
		t.Fatalf("rules.NewDefaultEngine: %v", err)
	}
	var executions atomic.Int32
	registry := tool.NewEmptyRegistry()
	registry.Register(&acpStateTestTool{
		name:     "write_file",
		metadata: tool.ToolMetadata{Impact: tool.ImpactModifying},
		execute: func() error {
			executions.Add(1)
			return nil
		},
	})
	agent, client := startFakeACPAgentForPermissionTests(t)
	var permissionRequests atomic.Int32
	go func() {
		for {
			message, err := client.ReadMessage()
			if err != nil {
				return
			}
			var request acp.Request
			if json.Unmarshal(message, &request) == nil && request.Method == "session/request_permission" {
				permissionRequests.Add(1)
			}
		}
	}()
	conv := conversation.New("route-toolless")
	conv.AddUserMessage("try the unavailable write")
	collector := &collectingStream{}

	text, err := runACPLoop(context.Background(), cfg, mgr, conv, registry, nil, engine, "toolless-alias", t.TempDir(), "route-toolless", agent, func(string, ...interface{}) {}, collector.fn)
	if err == nil {
		t.Fatal("runACPLoop unexpectedly completed after an unoffered structured call")
	}
	var incomplete *agentloop.IncompleteTurnError
	if !errors.As(err, &incomplete) {
		t.Fatalf("runACPLoop error = %T %v, want IncompleteTurnError", err, err)
	}
	if incomplete.FinishReason != agentloop.FinishReasonInvalidCompletion || incomplete.Code != "unoffered_tool_call" {
		t.Fatalf("incomplete error = %+v, want invalid_completion/unoffered_tool_call", incomplete)
	}
	if !strings.Contains(incomplete.Reason, "response-producing request did not offer tools") {
		t.Fatalf("incomplete reason = %q, want explicit unoffered-call reason", incomplete.Reason)
	}
	if !strings.Contains(text, "Model response preserved with 1 unexecuted tool call(s)") || !strings.Contains(text, "response-producing request did not offer tools") {
		t.Fatalf("incomplete status = %q, want preserved unoffered-call evidence", text)
	}
	if got := executions.Load(); got != 0 {
		t.Fatalf("unexpected tool executions = %d, want 0", got)
	}
	if got := permissionRequests.Load(); got != 0 {
		t.Fatalf("permission requests = %d, want 0", got)
	}

	mu.Lock()
	captured := append([]acpToolOfferWireRequest(nil), requests...)
	mu.Unlock()
	if len(captured) != 1 {
		t.Fatalf("provider requests = %d, want initial request only", len(captured))
	}
	for i, req := range captured {
		if req.Model != "selected-toolless" {
			t.Fatalf("wire request %d model = %q, want selected-toolless", i+1, req.Model)
		}
		assertACPToolOfferWireOmitsTools(t, i+1, req)
	}
	if history := conv.ToModelMessages(); len(history) != 1 || history[0].Role != "user" {
		t.Fatalf("conversation history = %+v, want only the original user message", history)
	}
	for _, update := range collector.updates {
		if update.ToolCallID == "call-surprise" {
			t.Fatalf("unexpected ACP dispatcher update for unoffered call: %+v", update)
		}
	}
}

func TestRunACPLoop_RouteBoundToollessSafeRetryRetainsNoToolWireShape(t *testing.T) {
	var (
		mu       sync.Mutex
		requests []acpToolOfferWireRequest
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"retry-toolless","supported_parameters":[]}]}`)
		case "/chat/completions":
			request := readACPToolOfferWireRequest(t, r)
			mu.Lock()
			requests = append(requests, request)
			ordinal := len(requests)
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			if ordinal == 1 {
				// No terminal event makes this a safe, no-tools retry candidate.
				acpSSEChunk(t, w, "interrupted first attempt", "", "")
				return
			}
			acpSSEChunk(t, w, "safe retry answer", "", "stop")
			writeACPDone(w)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	cfg := config.DefaultConfig()
	cfg.Providers.OpenAICompatible.Enabled = true
	cfg.Providers.OpenAICompatible.APIKey = "test-key"
	cfg.Providers.OpenAICompatible.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai_compatible"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	var hookCalls atomic.Int32
	mgr.RoutingHooks().Register(func(decision *model.RoutingDecision) *model.RoutingDecision {
		if decision != nil && decision.RequestedModel == "retry-alias" {
			hookCalls.Add(1)
			decision.SelectedModel = "openai_compatible/retry-toolless"
		}
		return decision
	})
	engine, err := rules.NewDefaultEngine()
	if err != nil {
		t.Fatalf("rules.NewDefaultEngine: %v", err)
	}
	conv := conversation.New("route-safe-retry")
	conv.AddUserMessage("answer without using a tool")

	text, err := runACPLoop(context.Background(), cfg, mgr, conv, tool.NewEmptyRegistry(), nil, engine, "retry-alias", "", "route-safe-retry", nil, func(string, ...interface{}) {}, nil)
	if err != nil {
		t.Fatalf("runACPLoop: %v", err)
	}
	if text != "safe retry answer" {
		t.Fatalf("text = %q, want safe retry answer", text)
	}
	if calls := hookCalls.Load(); calls != 3 {
		t.Fatalf("routing hook calls = %d, want prompt route plus two pinned stream attempts", calls)
	}

	mu.Lock()
	captured := append([]acpToolOfferWireRequest(nil), requests...)
	mu.Unlock()
	if len(captured) != 2 {
		t.Fatalf("provider requests = %d, want initial plus safe retry", len(captured))
	}
	for i, request := range captured {
		if request.Model != "retry-toolless" {
			t.Fatalf("wire request %d model = %q, want retry-toolless", i+1, request.Model)
		}
		assertACPToolOfferWireOmitsTools(t, i+1, request)
	}
}

func TestRunACPLoop_RouteDriftBeforeInitialDispatchStopsProviderIO(t *testing.T) {
	var providerRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"initial-toolless","supported_parameters":[]}]}`)
		case "/chat/completions":
			providerRequests.Add(1)
			t.Errorf("provider received request after initial route drift")
			http.Error(w, "unexpected provider dispatch", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	cfg := config.DefaultConfig()
	cfg.Providers.OpenAICompatible.Enabled = true
	cfg.Providers.OpenAICompatible.APIKey = "test-key"
	cfg.Providers.OpenAICompatible.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai_compatible"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	var hookCalls atomic.Int32
	mgr.RoutingHooks().Register(func(decision *model.RoutingDecision) *model.RoutingDecision {
		if decision == nil || decision.RequestedModel != "initial-drift-alias" {
			return decision
		}
		if hookCalls.Add(1) == 1 {
			decision.SelectedModel = "openai_compatible/initial-toolless"
		} else {
			decision.SelectedModel = "openai_compatible/changed-before-initial-dispatch"
		}
		return decision
	})
	engine, err := rules.NewDefaultEngine()
	if err != nil {
		t.Fatalf("rules.NewDefaultEngine: %v", err)
	}
	conv := conversation.New("route-initial-drift")
	conv.AddUserMessage("answer without using a tool")

	_, err = runACPLoop(context.Background(), cfg, mgr, conv, tool.NewEmptyRegistry(), nil, engine, "initial-drift-alias", "", "route-initial-drift", nil, func(string, ...interface{}) {}, nil)
	if err == nil || !strings.Contains(err.Error(), "model route changed before dispatch") {
		t.Fatalf("runACPLoop error = %v, want route-drift failure", err)
	}
	if calls := hookCalls.Load(); calls != 2 {
		t.Fatalf("routing hook calls = %d, want prompt route then initial dispatch check", calls)
	}
	if calls := providerRequests.Load(); calls != 0 {
		t.Fatalf("provider requests after initial route drift = %d, want 0", calls)
	}
}

func TestRunACPLoop_RouteDriftBeforeSafeRetryStopsSecondProviderIO(t *testing.T) {
	var (
		mu       sync.Mutex
		requests []acpToolOfferWireRequest
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"retry-drift-toolless","supported_parameters":[]}]}`)
		case "/chat/completions":
			request := readACPToolOfferWireRequest(t, r)
			mu.Lock()
			requests = append(requests, request)
			ordinal := len(requests)
			mu.Unlock()
			if ordinal != 1 {
				t.Errorf("provider received retry request after route drift")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			acpSSEChunk(t, w, "inconclusive first attempt", "", "")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	cfg := config.DefaultConfig()
	cfg.Providers.OpenAICompatible.Enabled = true
	cfg.Providers.OpenAICompatible.APIKey = "test-key"
	cfg.Providers.OpenAICompatible.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai_compatible"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	var hookCalls atomic.Int32
	mgr.RoutingHooks().Register(func(decision *model.RoutingDecision) *model.RoutingDecision {
		if decision == nil || decision.RequestedModel != "retry-drift-alias" {
			return decision
		}
		if hookCalls.Add(1) <= 2 {
			decision.SelectedModel = "openai_compatible/retry-drift-toolless"
		} else {
			decision.SelectedModel = "openai_compatible/changed-before-safe-retry"
		}
		return decision
	})
	engine, err := rules.NewDefaultEngine()
	if err != nil {
		t.Fatalf("rules.NewDefaultEngine: %v", err)
	}
	conv := conversation.New("route-retry-drift")
	conv.AddUserMessage("answer without using a tool")

	_, err = runACPLoop(context.Background(), cfg, mgr, conv, tool.NewEmptyRegistry(), nil, engine, "retry-drift-alias", "", "route-retry-drift", nil, func(string, ...interface{}) {}, nil)
	if err == nil || !strings.Contains(err.Error(), "model route changed before dispatch") {
		t.Fatalf("runACPLoop error = %v, want safe-retry route-drift failure", err)
	}
	if calls := hookCalls.Load(); calls != 3 {
		t.Fatalf("routing hook calls = %d, want prompt route plus two stream attempts", calls)
	}

	mu.Lock()
	captured := append([]acpToolOfferWireRequest(nil), requests...)
	mu.Unlock()
	if len(captured) != 1 {
		t.Fatalf("provider requests = %d, want first attempt only after retry drift", len(captured))
	}
	if captured[0].Model != "retry-drift-toolless" {
		t.Fatalf("first wire model = %q, want retry-drift-toolless", captured[0].Model)
	}
	assertACPToolOfferWireOmitsTools(t, 1, captured[0])
}

// TestRunACPLoop_RouteBoundToolRiskRetriesIncompleteStreamWithoutObservedToolCall
// covers C2: a tool-bearing request is no longer blanket-excluded from the
// whole-turn safe retry. Every attempt here streams plain content and never
// emits a tool-call delta, so acpStreamRetryCandidate's double-apply guard
// (ObservedToolDelta/ToolCalls) never trips, and the retry is expected;
// both attempts hit the exhausted-retry-budget ceiling identically, so the
// turn still ends in a (combined) error.
func TestRunACPLoop_RouteBoundToolRiskRetriesIncompleteStreamWithoutObservedToolCall(t *testing.T) {
	var providerRequests atomic.Int32
	var captured acpToolOfferWireRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"retry-capable","supported_parameters":["tools"]}]}`)
		case "/chat/completions":
			captured = readACPToolOfferWireRequest(t, r)
			providerRequests.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			acpSSEChunk(t, w, "toolful incomplete attempt", "", "")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	cfg := config.DefaultConfig()
	cfg.Providers.OpenAICompatible.Enabled = true
	cfg.Providers.OpenAICompatible.APIKey = "test-key"
	cfg.Providers.OpenAICompatible.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai_compatible"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	mgr.RoutingHooks().Register(func(decision *model.RoutingDecision) *model.RoutingDecision {
		if decision != nil && decision.RequestedModel == "retry-capable-alias" {
			decision.SelectedModel = "openai_compatible/retry-capable"
		}
		return decision
	})
	engine, err := rules.NewDefaultEngine()
	if err != nil {
		t.Fatalf("rules.NewDefaultEngine: %v", err)
	}
	registry := tool.NewEmptyRegistry()
	registry.Register(&acpStateTestTool{name: "write_file", metadata: tool.ToolMetadata{Impact: tool.ImpactModifying}})
	conv := conversation.New("route-tool-risk")
	conv.AddUserMessage("write the file")

	_, err = runACPLoop(context.Background(), cfg, mgr, conv, registry, nil, engine, "retry-capable-alias", "", "route-tool-risk", nil, func(string, ...interface{}) {}, nil)
	if err == nil {
		t.Fatal("tool-capable incomplete stream unexpectedly succeeded")
	}
	if calls := providerRequests.Load(); calls != 2 {
		t.Fatalf("provider requests = %d, want exactly one retry for a tool-bearing request with no observed tool call", calls)
	}
	if !captured.hasField("tools") || captured.ToolChoice != "auto" {
		t.Fatalf("tool-risk wire request did not carry its schema offer: %+v", captured)
	}
}

func TestRunACPLoop_UnknownRouteKeepsToolSchemasOnWire(t *testing.T) {
	var request acpToolOfferWireRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[]}`)
		case "/chat/completions":
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode provider request: %v", err)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: "+
				`{"id":"unknown-route","model":"future-unknown","choices":[{"index":0,"delta":{"content":"unknown route answer"},"finish_reason":"stop"}]}`+
				"\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	cfg := config.DefaultConfig()
	cfg.Providers.OpenAICompatible.Enabled = true
	cfg.Providers.OpenAICompatible.APIKey = "test-key"
	cfg.Providers.OpenAICompatible.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai_compatible"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	engine, err := rules.NewDefaultEngine()
	if err != nil {
		t.Fatalf("rules.NewDefaultEngine: %v", err)
	}
	registry := tool.NewEmptyRegistry()
	registry.Register(&acpStateTestTool{name: "write_file", metadata: tool.ToolMetadata{Impact: tool.ImpactModifying}})
	conv := conversation.New("route-unknown")
	conv.AddUserMessage("answer without known capability metadata")

	text, err := runACPLoop(context.Background(), cfg, mgr, conv, registry, nil, engine, "future-unknown", "", "route-unknown", nil, func(string, ...interface{}) {}, nil)
	if err != nil {
		t.Fatalf("runACPLoop: %v", err)
	}
	if text != "unknown route answer" {
		t.Fatalf("text = %q, want unknown route answer", text)
	}
	if len(request.Tools) != 1 || request.ToolChoice != "auto" {
		t.Fatalf("unknown route wire tools = %d, choice=%q, want schema offer", len(request.Tools), request.ToolChoice)
	}
}
