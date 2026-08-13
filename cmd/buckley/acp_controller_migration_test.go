package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"m31labs.dev/buckley/pkg/skill"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

// acpProbeTool is a minimal registry tool for the Controller-migration
// test: read-only impact (so the permission flow auto-allows it) and a
// recognizable output string the cross-round assertions can find in the
// next round's request body.
type acpProbeTool struct {
	mu    sync.Mutex
	calls int
}

func TestACPToolLoopGovernorUsesConfiguredEmergencyFuse(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.AgentController.EmergencyFuse.ModelRequests = 40
	governor := newACPToolLoopGovernor(cfg)
	for round := 1; round <= 40; round++ {
		if decision := governor.BeginRound(); decision.Stop {
			t.Fatalf("round %d stopped early: %+v", round, decision)
		}
	}
	decision := governor.BeginRound()
	if !decision.Stop || !strings.Contains(decision.Reason, "40-round") {
		t.Fatalf("round 41 decision = %+v, want configured 40-round stop", decision)
	}
}

func TestACPChildGovernor_ZeroCeilingsRemainUnbounded(t *testing.T) {
	cfg := config.DefaultConfig()
	// Remove operator-wide emergency fuses so this test isolates the child
	// contract: zero child ceilings must not reintroduce legacy 32/96 limits.
	cfg.AgentController.EmergencyFuse.ModelRequests = 0
	cfg.AgentController.EmergencyFuse.ToolExecutions = 0
	governor := newACPToolLoopGovernorWithLimits(cfg, acpLoopLimits{ChildContract: true})
	for round := 1; round <= 120; round++ {
		if decision := governor.BeginRound(); decision.Stop {
			t.Fatalf("round %d stopped with zero child ceiling: %+v", round, decision)
		}
		decision := governor.Observe("read_file", fmt.Sprintf(`{"path":"file-%d"}`, round), fmt.Sprintf("evidence-%d", round), true)
		if decision.Stop {
			t.Fatalf("tool %d stopped with zero child ceiling: %+v", round, decision)
		}
	}
}

func TestACPChildGovernor_RetainsOperatorEmergencyFuse(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.AgentController.EmergencyFuse.ModelRequests = 40
	governor := newACPToolLoopGovernorWithLimits(cfg, acpLoopLimits{ChildContract: true})
	for round := 1; round <= 40; round++ {
		if decision := governor.BeginRound(); decision.Stop {
			t.Fatalf("round %d stopped early: %+v", round, decision)
		}
	}
	if decision := governor.BeginRound(); !decision.Stop || decision.Kind != "round_limit" {
		t.Fatalf("global fuse decision = %+v, want round_limit", decision)
	}
}

func TestACPChildGovernor_ZeroCeilingsRetainRepeatSafety(t *testing.T) {
	governor := newACPToolLoopGovernorWithLimits(config.DefaultConfig(), acpLoopLimits{ChildContract: true})
	for call := 1; call <= 3; call++ {
		decision := governor.Observe("read_file", `{"path":"same"}`, "same evidence", true)
		if call < 3 && decision.Stop {
			t.Fatalf("repeat %d stopped early: %+v", call, decision)
		}
		if call == 3 && (!decision.Stop || decision.Kind != "exact_repeat") {
			t.Fatalf("repeat %d decision = %+v, want exact-repeat safety stop", call, decision)
		}
	}
}

func TestACPChildGovernor_EnforcesExplicitCeilings(t *testing.T) {
	governor := newACPToolLoopGovernorWithLimits(config.DefaultConfig(), acpLoopLimits{
		ChildContract:    true,
		MaxModelRequests: 2,
		MaxToolCalls:     2,
	})
	if decision := governor.BeginRound(); decision.Stop {
		t.Fatalf("round 1 stopped early: %+v", decision)
	}
	if decision := governor.BeginRound(); decision.Stop {
		t.Fatalf("round 2 stopped early: %+v", decision)
	}
	if decision := governor.BeginRound(); !decision.Stop || decision.Kind != "round_limit" {
		t.Fatalf("round 3 decision = %+v, want explicit round limit", decision)
	}

	toolGovernor := newACPToolLoopGovernorWithLimits(config.DefaultConfig(), acpLoopLimits{ChildContract: true, MaxToolCalls: 2})
	if decision := toolGovernor.Observe("read_file", `{"path":"one"}`, "one", true); decision.Stop {
		t.Fatalf("tool 1 stopped early: %+v", decision)
	}
	if decision := toolGovernor.Observe("read_file", `{"path":"two"}`, "two", true); !decision.Stop || decision.Kind != "tool_call_limit" {
		t.Fatalf("tool 2 decision = %+v, want explicit tool limit", decision)
	}
}

func TestACPProgressControllerHonorsRolloutMode(t *testing.T) {
	cfg := config.DefaultConfig()
	if got := newACPProgressController(cfg); got != nil {
		t.Fatalf("legacy progress controller = %+v, want nil", got)
	}
	cfg.AgentController.Mode = "dynamic"
	cfg.AgentController.PolicyVersion = "progress-v2"
	cfg.AgentController.EmergencyFuse.ToolExecutions = 73
	got := newACPProgressController(cfg)
	if got == nil || got.Mode != "dynamic" || got.PolicyVersion != "progress-v2" || got.Fuses.ToolExecutions != 73 {
		t.Fatalf("ACP progress controller = %+v", got)
	}
}

func (p *acpProbeTool) Name() string        { return "stub_probe" }
func (p *acpProbeTool) Description() string { return "test probe" }
func (p *acpProbeTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{Type: "object"}
}

func (p *acpProbeTool) Execute(map[string]any) (*builtin.Result, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return &builtin.Result{Success: true, Data: map[string]any{"output": "probe-ok"}}, nil
}

func (p *acpProbeTool) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// TestRunACPLoop_ControllerCrossRoundInvariants is the ACP loop's
// migration acceptance test (the same invariants #130 locked for the
// oneshot and RLM loops): after the shared engine took over the round
// loop, a tool round's assistant tool-call message and its tool result
// must both appear in the next round's request, the tool must actually
// execute, each round must emit its own usage_update, and the final text
// must come back unchanged.
func TestRunACPLoop_ControllerCrossRoundInvariants(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var requestBodies []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requestBodies = append(requestBodies, string(body))
		round := len(requestBodies)
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if round == 1 {
			_, _ = io.WriteString(w, "data: "+
				`{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"stub_probe","arguments":"{}"}}]},"finish_reason":"tool_calls"}],`+
				`"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}}`+
				"\n\n")
		} else {
			_, _ = io.WriteString(w, "data: "+
				`{"id":"chatcmpl-2","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"done: 7"},"finish_reason":"stop"}],`+
				`"usage":{"prompt_tokens":200,"completion_tokens":10,"total_tokens":210}}`+
				"\n\n")
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	t.Cleanup(server.Close)

	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai"

	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	// "gpt-4o" is in the static catalog with SupportsTools true, so the
	// governed tool-turn path runs and needs a real rules engine.
	engine, err := rules.NewDefaultEngine()
	if err != nil {
		t.Fatalf("rules.NewDefaultEngine: %v", err)
	}

	probe := &acpProbeTool{}
	registry := tool.NewEmptyRegistry()
	registry.Register(probe)

	conv := conversation.New("session-1")
	conv.AddUserMessage("use the stub probe")
	collector := &collectingStream{}

	text, err := runACPLoop(context.Background(), cfg, mgr, conv, registry, nil, engine, "gpt-4o", "", "session-1", nil, func(string, ...interface{}) {}, collector.fn)
	if err != nil {
		t.Fatalf("runACPLoop: %v", err)
	}
	if text != "done: 7" {
		t.Fatalf("text = %q, want %q", text, "done: 7")
	}
	if probe.callCount() != 1 {
		t.Fatalf("probe executed %d times, want 1", probe.callCount())
	}

	mu.Lock()
	bodies := append([]string(nil), requestBodies...)
	mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("model requests = %d, want 2", len(bodies))
	}

	// Cross-round transcript invariant: round 2's request carries round
	// 1's assistant tool-call message and the tool result the dispatcher
	// produced, in the transcript itself (not just the tools list).
	second := bodies[1]
	for _, want := range []string{`"tool_calls"`, `"role":"tool"`, `"tool_call_id":"call-1"`, "probe-ok"} {
		if !strings.Contains(second, want) {
			t.Fatalf("second request missing %s:\n%s", want, second)
		}
	}

	// Conversation state: assistant tool-call message, tool response, and
	// the final assistant answer were all persisted, in order.
	msgs := conv.ToModelMessages()
	var sawToolCall, sawToolResult, sawFinal bool
	for _, m := range msgs {
		switch {
		case m.Role == "assistant" && len(m.ToolCalls) > 0:
			sawToolCall = true
		case m.Role == "tool" && m.ToolCallID == "call-1":
			sawToolResult = true
		case m.Role == "assistant" && len(m.ToolCalls) == 0:
			if text, err := model.ExtractTextContent(m.Content); err == nil && text == "done: 7" {
				sawFinal = true
			}
		}
	}
	if !sawToolCall || !sawToolResult || !sawFinal {
		t.Fatalf("conversation missing tool call (%v), tool result (%v), or final answer (%v): %+v", sawToolCall, sawToolResult, sawFinal, msgs)
	}

	// N1 through the engine: each round reports its own usage_update.
	var usageUpdates []uint64
	for _, u := range collector.updates {
		if u.SessionUpdate == acp.SessionUpdateUsageUpdate && u.UsageUsed != nil {
			usageUpdates = append(usageUpdates, *u.UsageUsed)
		}
	}
	if len(usageUpdates) != 2 || usageUpdates[0] != 120 || usageUpdates[1] != 210 {
		t.Fatalf("usage updates = %v, want [120 210]", usageUpdates)
	}
}

func TestRunACPLoop_NoToolsRepairsDeepSeekXMLInvocationMarkup(t *testing.T) {
	t.Parallel()

	const attemptedCall = `<search_text>
<query>reserved synthesis request Tools nil ToolChoice none</query>
<path>/home/draco/work/buckley</path>
</search_text>`
	const finalAnswer = "No tools are available, so I can only answer from the supplied context."

	var mu sync.Mutex
	requestBodies := make([][]byte, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requestBodies = append(requestBodies, append([]byte(nil), body...))
		round := len(requestBodies)
		mu.Unlock()

		content := finalAnswer
		if round == 1 {
			content = attemptedCall
		}
		payload, _ := json.Marshal(map[string]any{
			"id":    fmt.Sprintf("chatcmpl-%d", round),
			"model": "gpt-4o",
			"choices": []any{map[string]any{
				"index":         0,
				"delta":         map[string]any{"content": content},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 40, "completion_tokens": 10, "total_tokens": 50},
		})
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", payload)
	}))
	t.Cleanup(server.Close)

	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	engine, err := rules.NewDefaultEngine()
	if err != nil {
		t.Fatalf("rules.NewDefaultEngine: %v", err)
	}
	conv := conversation.New("session-no-tools-markup")
	conv.AddUserMessage("answer from context only")
	registry := tool.NewRegistry()
	skillState := skill.NewRuntimeState(nil)
	skillState.SetToolFilter([]string{})

	text, err := runACPLoop(
		context.Background(), cfg, mgr, conv, registry, skillState, engine,
		"gpt-4o", "", "session-no-tools-markup", nil, func(string, ...interface{}) {}, nil,
	)
	if err != nil || text != finalAnswer {
		t.Fatalf("runACPLoop = %q, %v, want repaired answer", text, err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requestBodies) != 2 {
		t.Fatalf("provider requests = %d, want one repair continuation", len(requestBodies))
	}
	for i, body := range requestBodies {
		var wire map[string]json.RawMessage
		if err := json.Unmarshal(body, &wire); err != nil {
			t.Fatalf("decode request %d: %v", i+1, err)
		}
		if _, exposed := wire["tools"]; exposed {
			t.Fatalf("request %d exposed tools despite the empty allowlist: %s", i+1, body)
		}
	}
	if !strings.Contains(string(requestBodies[1]), "Tools are unavailable for this turn") {
		t.Fatalf("repair request omitted the no-tools compatibility nudge: %s", requestBodies[1])
	}
}

func TestRunACPLoop_NoToolsRepeatedInvocationMarkupIsIncomplete(t *testing.T) {
	t.Parallel()

	const attemptedCall = `<search_text>
<query>reserved synthesis request Tools nil ToolChoice none</query>
<path>/home/draco/work/buckley</path>
</search_text>`
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		payload, _ := json.Marshal(map[string]any{
			"id":    "chatcmpl-markup",
			"model": "gpt-4o",
			"choices": []any{map[string]any{
				"index":         0,
				"delta":         map[string]any{"content": attemptedCall},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 40, "completion_tokens": 10, "total_tokens": 50},
		})
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", payload)
	}))
	t.Cleanup(server.Close)

	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	engine, err := rules.NewDefaultEngine()
	if err != nil {
		t.Fatalf("rules.NewDefaultEngine: %v", err)
	}
	conv := conversation.New("session-repeated-no-tools-markup")
	conv.AddUserMessage("answer from context only")
	registry := tool.NewRegistry()
	skillState := skill.NewRuntimeState(nil)
	skillState.SetToolFilter([]string{})

	text, err := runACPLoopWithLimits(
		context.Background(), cfg, mgr, conv, registry, skillState, engine,
		"gpt-4o", "", "session-repeated-no-tools-markup", nil,
		func(string, ...interface{}) {}, nil, acpLoopLimits{MaxModelRequests: 2},
	)
	var incomplete *agentloop.IncompleteTurnError
	if !errors.As(err, &incomplete) || !strings.Contains(err.Error(), "invocation markup while no tools were available") {
		t.Fatalf("runACPLoopWithLimits = %q, %v, want explicit incomplete result", text, err)
	}
	if text != attemptedCall {
		t.Fatalf("preserved terminal evidence = %q, want exact attempted invocation", text)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("provider requests = %d, want one repair attempt then incomplete", got)
	}
}

// TestRunACPLoop_GovernorStopsRepeatingToolLoop locks the shared conclusive
// termination behavior: the Governor stops repeated actions, then ACP makes
// exactly one tools-disabled synthesis request over the evidence already in
// the conversation.
func TestRunACPLoop_GovernorStopsRepeatingToolLoop(t *testing.T) {
	t.Parallel()

	var finalizationCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if !strings.Contains(string(body), `"tools"`) {
			finalizationCalls.Add(1)
			_, _ = io.WriteString(w, "data: "+
				`{"id":"chatcmpl-final","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"The probe repeated without new evidence; no blocker was established."},"finish_reason":"stop"}],`+
				`"usage":{"prompt_tokens":140,"completion_tokens":20,"total_tokens":160}}`+
				"\n\n")
		} else {
			// Every work round repeats the identical tool call and arguments.
			_, _ = io.WriteString(w, "data: "+
				`{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"stub_probe","arguments":"{}"}}]},"finish_reason":"tool_calls"}],`+
				`"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}}`+
				"\n\n")
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	t.Cleanup(server.Close)

	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai"

	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	engine, err := rules.NewDefaultEngine()
	if err != nil {
		t.Fatalf("rules.NewDefaultEngine: %v", err)
	}

	probe := &acpProbeTool{}
	registry := tool.NewEmptyRegistry()
	registry.Register(probe)

	conv := conversation.New("session-1")
	conv.AddUserMessage("loop forever")
	collector := &collectingStream{}

	text, err := runACPLoop(context.Background(), cfg, mgr, conv, registry, nil, engine, "gpt-4o", "", "session-1", nil, func(string, ...interface{}) {}, collector.fn)
	if err != nil {
		t.Fatalf("runACPLoop: %v", err)
	}
	if text != "The probe repeated without new evidence; no blocker was established." {
		t.Fatalf("text = %q, want evidence-grounded synthesis", text)
	}
	if finalizationCalls.Load() != 1 {
		t.Fatalf("finalization requests = %d, want 1", finalizationCalls.Load())
	}
	// The guard fires on repeats well below the round ceiling; the exact
	// count is the Governor's contract, not this test's. It just must not
	// have run unbounded.
	if probe.callCount() >= 32 {
		t.Fatalf("probe executed %d times, want the repeat guard to stop well before the emergency ceiling", probe.callCount())
	}
}

func TestRunACPLoop_LiteLLMFinalizationPreservesNoToolIntent(t *testing.T) {
	t.Parallel()

	var (
		mu                 sync.Mutex
		finalizationBodies []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/model/info":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"model_name":"test-model","model_info":{"mode":"chat","max_input_tokens":128000,"input_cost_per_token":0.000001,"output_cost_per_token":0.000002,"supports_function_calling":true}}]}`)
			return
		case "/chat/completions":
		default:
			http.NotFound(w, r)
			return
		}

		body, _ := io.ReadAll(r.Body)
		bodyText := string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.Contains(bodyText, `"tool_choice":"none"`) {
			mu.Lock()
			finalizationBodies = append(finalizationBodies, bodyText)
			mu.Unlock()
			_, _ = io.WriteString(w, "data: "+
				`{"id":"chatcmpl-final","model":"test-model","choices":[{"index":0,"delta":{"content":"LiteLLM final synthesis."},"finish_reason":"stop"}],"usage":{"prompt_tokens":140,"completion_tokens":20,"total_tokens":160}}`+
				"\n\n")
		} else {
			_, _ = io.WriteString(w, "data: "+
				`{"id":"chatcmpl-tool","model":"test-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-repeat","type":"function","function":{"name":"stub_probe","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}}`+
				"\n\n")
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)

	cfg := config.DefaultConfig()
	cfg.Providers.LiteLLM.Enabled = true
	cfg.Providers.LiteLLM.APIKey = "test-key"
	cfg.Providers.LiteLLM.BaseURL = server.URL
	cfg.Models.DefaultProvider = "litellm"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	engine, err := rules.NewDefaultEngine()
	if err != nil {
		t.Fatalf("rules.NewDefaultEngine: %v", err)
	}
	probe := &acpProbeTool{}
	registry := tool.NewEmptyRegistry()
	registry.Register(probe)
	conv := conversation.New("session-litellm-finalize")
	conv.AddUserMessage("keep probing")

	text, err := runACPLoop(
		context.Background(), cfg, mgr, conv, registry, nil, engine,
		"litellm/test-model", "", "session-litellm-finalize", nil,
		func(string, ...interface{}) {}, (&collectingStream{}).fn,
	)
	if err != nil || text != "LiteLLM final synthesis." {
		t.Fatalf("runACPLoop = %q, %v", text, err)
	}
	if probe.callCount() != 3 {
		t.Fatalf("probe executions = %d, want only three pre-guard calls", probe.callCount())
	}
	mu.Lock()
	bodies := append([]string(nil), finalizationBodies...)
	mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("finalization requests = %d, want exactly one", len(bodies))
	}
	if !strings.Contains(bodies[0], `"name":"_noop"`) || !strings.Contains(bodies[0], `"tool_choice":"none"`) {
		t.Fatalf("LiteLLM finalization lost compatibility noop or forced no-tool choice:\n%s", bodies[0])
	}
}

func TestRunACPLoop_FailedGuardFinalizationDoesNotCompleteTurn(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// This deliberately violates the tools-disabled finalization contract.
		// The shared Controller must reject it rather than dispatching or treating
		// a clean HTTP response as a completed child turn.
		_, _ = io.WriteString(w, "data: "+
			`{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"stub_probe","arguments":"{}"}}]},"finish_reason":"tool_calls"}],`+
			`"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}}`+
			"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	t.Cleanup(server.Close)

	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai"

	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	engine, err := rules.NewDefaultEngine()
	if err != nil {
		t.Fatalf("rules.NewDefaultEngine: %v", err)
	}
	probe := &acpProbeTool{}
	registry := tool.NewEmptyRegistry()
	registry.Register(probe)
	conv := conversation.New("session-incomplete")
	conv.AddUserMessage("loop forever")

	text, err := runACPLoop(context.Background(), cfg, mgr, conv, registry, nil, engine, "gpt-4o", "", "session-incomplete", nil, func(string, ...interface{}) {}, (&collectingStream{}).fn)
	var incomplete *agentloop.IncompleteTurnError
	if !errors.As(err, &incomplete) {
		t.Fatalf("runACPLoop error = %v, want IncompleteTurnError", err)
	}
	if !strings.Contains(err.Error(), "tools were disabled") {
		t.Fatalf("error = %v, want finalization contract failure", err)
	}
	if !strings.Contains(text, "Buckley stopped the tool loop") {
		t.Fatalf("partial status = %q, want preserved harness stop context", text)
	}
	if probe.callCount() != 3 {
		t.Fatalf("probe executed %d times, want only the 3 pre-guard work calls", probe.callCount())
	}
}

func TestRunACPLoop_CostBoundUsesOpenAIWireAllowance(t *testing.T) {
	t.Parallel()

	requestBody := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requestBody <- body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+
			`{"id":"chatcmpl-cost","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"bounded"},"finish_reason":"stop"}]}`+
			"\n\ndata: "+
			`{"id":"chatcmpl-cost","model":"gpt-4o","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`+
			"\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)

	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	engine, err := rules.NewDefaultEngine()
	if err != nil {
		t.Fatalf("rules.NewDefaultEngine: %v", err)
	}
	conv := conversation.New("session-cost-bound")
	conv.AddUserMessage("answer briefly")
	collector := &collectingStream{}

	text, err := runACPLoopWithLimits(
		context.Background(), cfg, mgr, conv, tool.NewEmptyRegistry(), nil, engine,
		"gpt-4o", "", "session-cost-bound", nil, func(string, ...interface{}) {},
		collector.fn, acpLoopLimits{MaxCostUSD: 0.25, MaxModelRequests: 1},
	)
	if err != nil || text != "bounded" {
		t.Fatalf("runACPLoopWithLimits = %q, %v", text, err)
	}

	var wire map[string]json.RawMessage
	if err := json.Unmarshal(<-requestBody, &wire); err != nil {
		t.Fatalf("decode provider request: %v", err)
	}
	if _, ok := wire["max_tokens"]; ok {
		t.Fatalf("OpenAI cost-bounded request sent conflicting max_tokens: %s", wire["max_tokens"])
	}
	var allowance int
	if err := json.Unmarshal(wire["max_completion_tokens"], &allowance); err != nil {
		t.Fatalf("decode max_completion_tokens: %v (request %#v)", err, wire)
	}
	if allowance <= 0 {
		t.Fatalf("max_completion_tokens = %d, want positive bounded allowance", allowance)
	}
	var streamOptions struct {
		IncludeUsage bool `json:"include_usage"`
	}
	if err := json.Unmarshal(wire["stream_options"], &streamOptions); err != nil {
		t.Fatalf("decode stream_options: %v (request %#v)", err, wire)
	}
	if !streamOptions.IncludeUsage {
		t.Fatalf("stream_options.include_usage = false, want authoritative usage-only terminal chunk")
	}
	if got := strings.Join(collector.messageChunks(), ""); got != "bounded" {
		t.Fatalf("accepted bounded output = %q, want released exactly once after validation", got)
	}
}

func TestRunACPLoop_CostBoundRejectsGoogleMaxTokensWithoutLeakingPartialAnswer(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"candidates":[{"content":{"role":"model","parts":[{"text":"partial google answer"}]},"finishReason":"MAX_TOKENS"}],
			"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"totalTokenCount":12}
		}`)
	}))
	t.Cleanup(server.Close)

	cfg := config.DefaultConfig()
	cfg.Providers.Google.Enabled = true
	cfg.Providers.Google.APIKey = "test-key"
	cfg.Providers.Google.BaseURL = server.URL
	cfg.Models.DefaultProvider = "google"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	engine, err := rules.NewDefaultEngine()
	if err != nil {
		t.Fatalf("rules.NewDefaultEngine: %v", err)
	}
	conv := conversation.New("session-google-truncated")
	conv.AddUserMessage("answer")
	collector := &collectingStream{}

	text, err := runACPLoopWithLimits(
		context.Background(), cfg, mgr, conv, tool.NewEmptyRegistry(), nil, engine,
		"google/gemini-2.0-flash", "", "session-google-truncated", nil,
		func(string, ...interface{}) {}, collector.fn,
		acpLoopLimits{MaxCostUSD: 0.25, MaxModelRequests: 1},
	)
	var incomplete *agentloop.IncompleteTurnError
	if !errors.As(err, &incomplete) || !strings.Contains(err.Error(), "truncated at its output limit") {
		t.Fatalf("error = %v, want Google MAX_TOKENS rejected as incomplete", err)
	}
	if strings.Contains(text, "partial google answer") {
		t.Fatalf("partial text escaped as conclusive output: %q", text)
	}
	if got := strings.Join(collector.messageChunks(), ""); got != "" {
		t.Fatalf("truncated Google answer leaked to ACP client: %q", got)
	}
}

func TestRunACPLoop_CostBoundRejectsMissingProviderUsage(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+
			`{"id":"chatcmpl-no-usage","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"unpriced","reasoning":"unpriced reasoning"},"finish_reason":"stop"}]}`+
			"\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)

	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	engine, err := rules.NewDefaultEngine()
	if err != nil {
		t.Fatalf("rules.NewDefaultEngine: %v", err)
	}
	conv := conversation.New("session-missing-usage")
	conv.AddUserMessage("answer briefly")

	collector := &collectingStream{}
	_, err = runACPLoopWithLimits(
		context.Background(), cfg, mgr, conv, tool.NewEmptyRegistry(), nil, engine,
		"gpt-4o", "", "session-missing-usage", nil, func(string, ...interface{}) {},
		collector.fn, acpLoopLimits{MaxCostUSD: 0.25, MaxModelRequests: 1},
	)
	if err == nil || !strings.Contains(err.Error(), "provider reported no token counts") {
		t.Fatalf("error = %v, want fail-closed missing-usage pricing error", err)
	}
	if got := strings.Join(collector.messageChunks(), ""); got != "" {
		t.Fatalf("unpriced assistant output leaked to ACP client: %q", got)
	}
	if got := strings.Join(collector.thoughtChunks(), ""); strings.Contains(got, "unpriced reasoning") {
		t.Fatalf("unpriced model reasoning leaked to ACP client: %q", got)
	}
}

func TestRunACPLoop_CostBoundDoesNotLeakOverCeilingResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+
			`{"id":"chatcmpl-over-cost","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"over budget","reasoning":"expensive reasoning"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":1000000,"completion_tokens":1000000,"total_tokens":2000000}}`+
			"\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)

	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	engine, err := rules.NewDefaultEngine()
	if err != nil {
		t.Fatalf("rules.NewDefaultEngine: %v", err)
	}
	conv := conversation.New("session-over-cost")
	conv.AddUserMessage("answer briefly")
	collector := &collectingStream{}

	_, err = runACPLoopWithLimits(
		context.Background(), cfg, mgr, conv, tool.NewEmptyRegistry(), nil, engine,
		"gpt-4o", "", "session-over-cost", nil, func(string, ...interface{}) {},
		collector.fn, acpLoopLimits{MaxCostUSD: 0.25, MaxModelRequests: 1},
	)
	if err == nil || !strings.Contains(err.Error(), "exceeding the explicit") {
		t.Fatalf("error = %v, want over-ceiling response rejection", err)
	}
	if got := strings.Join(collector.messageChunks(), ""); got != "" {
		t.Fatalf("over-ceiling assistant output leaked to ACP client: %q", got)
	}
	if got := strings.Join(collector.thoughtChunks(), ""); strings.Contains(got, "expensive reasoning") {
		t.Fatalf("over-ceiling model reasoning leaked to ACP client: %q", got)
	}
}

func TestRunACPLoop_CostBoundRejectsTruncatedReservedFinalization(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.Contains(string(body), `"tools"`) {
			_, _ = io.WriteString(w, "data: "+
				`{"id":"chatcmpl-tool","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-repeat","type":"function","function":{"name":"stub_probe","arguments":"{}"}}]},"finish_reason":"tool_calls"}],`+
				`"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}}`+
				"\n\n")
		} else {
			_, _ = io.WriteString(w, "data: "+
				`{"id":"chatcmpl-final","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"partial final synthesis"},"finish_reason":"length"}],`+
				`"usage":{"prompt_tokens":140,"completion_tokens":20,"total_tokens":160}}`+
				"\n\n")
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)

	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	engine, err := rules.NewDefaultEngine()
	if err != nil {
		t.Fatalf("rules.NewDefaultEngine: %v", err)
	}
	probe := &acpProbeTool{}
	registry := tool.NewEmptyRegistry()
	registry.Register(probe)
	conv := conversation.New("session-truncated-final")
	conv.AddUserMessage("keep probing")
	collector := &collectingStream{}

	text, err := runACPLoopWithLimits(
		context.Background(), cfg, mgr, conv, registry, nil, engine,
		"gpt-4o", "", "session-truncated-final", nil, func(string, ...interface{}) {},
		collector.fn, acpLoopLimits{MaxCostUSD: 0.25, MaxModelRequests: 4},
	)
	var incomplete *agentloop.IncompleteTurnError
	if !errors.As(err, &incomplete) || !strings.Contains(err.Error(), "truncated at its output limit") {
		t.Fatalf("error = %v, want incomplete truncated finalization", err)
	}
	if !strings.Contains(text, "Buckley stopped the tool loop") {
		t.Fatalf("partial status = %q, want preserved guard context", text)
	}
	if got := strings.Join(collector.messageChunks(), ""); strings.Contains(got, "partial final synthesis") {
		t.Fatalf("truncated final synthesis leaked to ACP client: %q", got)
	}
}
