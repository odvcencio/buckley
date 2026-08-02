package rlm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"m31labs.dev/buckley/v2/pkg/config"
	"m31labs.dev/buckley/v2/pkg/model"
	"m31labs.dev/buckley/v2/pkg/tool"
	"m31labs.dev/buckley/v2/pkg/tool/builtin"
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
