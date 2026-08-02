package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"m31labs.dev/buckley/pkg/acp"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/rules"
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

// TestRunACPLoop_GovernorStopsRepeatingToolLoop locks the migration's new
// safety behavior: a model that repeats the same failing tool call forever
// used to spin until the client cancelled; the shared engine's Governor
// now stops the turn and surfaces the stop message as the turn's answer
// instead of an error.
func TestRunACPLoop_GovernorStopsRepeatingToolLoop(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// Every round: the identical tool call with identical arguments.
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

	conv := conversation.New("session-1")
	conv.AddUserMessage("loop forever")
	collector := &collectingStream{}

	text, err := runACPLoop(context.Background(), cfg, mgr, conv, registry, nil, engine, "gpt-4o", "", "session-1", nil, func(string, ...interface{}) {}, collector.fn)
	if err != nil {
		t.Fatalf("runACPLoop: %v (guard stop must complete the turn, not fail it)", err)
	}
	if !strings.Contains(text, "Buckley stopped the tool loop") {
		t.Fatalf("text = %q, want guard stop message", text)
	}
	// The guard fires on repeats well below the round ceiling; the exact
	// count is the Governor's contract, not this test's. It just must not
	// have run unbounded.
	if probe.callCount() >= 32 {
		t.Fatalf("probe executed %d times, want a guard stop well below the 32-round ceiling", probe.callCount())
	}
}
