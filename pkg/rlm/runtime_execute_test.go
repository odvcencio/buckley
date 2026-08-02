package rlm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"m31labs.dev/buckley/v2/pkg/config"
	"m31labs.dev/buckley/v2/pkg/model"
)

func newCoordinatorTestManager(t *testing.T, server *httptest.Server) *model.Manager {
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
	// NewRuntime requires a non-empty catalog (ModelRouter validates
	// against it); the OpenAI provider's catalog is a static list, so this
	// never touches the test server.
	if err := mgr.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return mgr
}

// TestRuntimeExecute_SecondRequestCarriesToolCallAndResult is the
// cross-round transcript invariant carried over from the pkg/headless,
// pkg/ui/tui, and pkg/rlm SubAgent agentloop.Controller migrations: after
// round one dispatches a tool, round two's request must contain the
// assistant tool-call message and its tool result, or the model loops on
// stale history.
func TestRuntimeExecute_SecondRequestCarriesToolCallAndResult(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) == 1 {
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-1","model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_coord_1","type":"function","function":{"name":"set_answer","arguments":"{\"content\":\"partial\",\"ready\":false,\"confidence\":0.1}"}}]},"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}
			}`)
			return
		}
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-2","model":"gpt-4o",
			"choices":[{"index":0,"message":{"role":"assistant","content":"final answer"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":12,"completion_tokens":4,"total_tokens":16}
		}`)
	}))
	defer server.Close()

	mgr := newCoordinatorTestManager(t, server)
	rt, err := NewRuntime(Config{}, RuntimeDeps{Models: mgr})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}

	answer, err := rt.Execute(context.Background(), "answer the question")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if answer.Content != "final answer" {
		t.Fatalf("Content = %q, want %q", answer.Content, "final answer")
	}
	if !answer.Ready {
		t.Fatal("expected Ready == true")
	}
	if len(bodies) != 2 {
		t.Fatalf("expected 2 model requests, got %d", len(bodies))
	}

	second := bodies[1]
	if !strings.Contains(second, "call_coord_1") {
		t.Fatalf("second request missing assistant tool-call message: %s", second)
	}
	if !strings.Contains(second, `"role":"tool"`) {
		t.Fatalf("second request missing tool result message: %s", second)
	}
}

// TestRuntimeExecute_AccumulatesStateAcrossRounds is the accumulated-state
// invariant: after two set_answer tool rounds (each below the confidence
// threshold, so the loop keeps going) and a final text answer, Answer must
// reflect the sum of every round's token usage and the last tool call's
// confidence, and the final request must still carry both prior tool
// exchanges, not just the most recent one.
func TestRuntimeExecute_AccumulatesStateAcrossRounds(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "application/json")
		switch len(bodies) {
		case 1:
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-1","model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_coord_1","type":"function","function":{"name":"set_answer","arguments":"{\"content\":\"partial one\",\"ready\":false,\"confidence\":0.2}"}}]},"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}
			}`)
		case 2:
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-2","model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_coord_2","type":"function","function":{"name":"set_answer","arguments":"{\"content\":\"partial two\",\"ready\":false,\"confidence\":0.4}"}}]},"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":20,"completion_tokens":6,"total_tokens":26}
			}`)
		default:
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-3","model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","content":"final synthesis"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":30,"completion_tokens":7,"total_tokens":37}
			}`)
		}
	}))
	defer server.Close()

	mgr := newCoordinatorTestManager(t, server)
	rt, err := NewRuntime(Config{}, RuntimeDeps{Models: mgr})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}

	answer, err := rt.Execute(context.Background(), "answer the question")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if answer.Content != "final synthesis" {
		t.Fatalf("Content = %q, want %q", answer.Content, "final synthesis")
	}
	if !answer.Ready {
		t.Fatal("expected Ready == true")
	}
	if len(bodies) != 3 {
		t.Fatalf("expected 3 model requests, got %d", len(bodies))
	}
	if answer.TokensUsed != 78 { // 15 + 26 + 37
		t.Errorf("TokensUsed = %d, want 78", answer.TokensUsed)
	}
	if answer.Confidence != 0.4 {
		t.Errorf("Confidence = %v, want 0.4 (from the last set_answer call)", answer.Confidence)
	}

	final := bodies[2]
	if !strings.Contains(final, "call_coord_1") || !strings.Contains(final, "call_coord_2") {
		t.Fatalf("final request missing accumulated tool calls: %s", final)
	}
}
