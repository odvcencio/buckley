package headless

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"m31labs.dev/buckley/v2/pkg/config"
	"m31labs.dev/buckley/v2/pkg/conversation"
	"m31labs.dev/buckley/v2/pkg/model"
	"m31labs.dev/buckley/v2/pkg/storage"
	"m31labs.dev/buckley/v2/pkg/tool"
)

// TestRunner_SecondRequestCarriesToolCallAndResult is the cross-round
// transcript invariant: after round one dispatches a tool, round two's
// request must contain the assistant tool-call message and its tool
// result, or the model loops on stale history.
func TestRunner_SecondRequestCarriesToolCallAndResult(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) == 1 {
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-1","model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_hist_1","type":"function","function":{"name":"echo_tool","arguments":"{\"text\":\"hi\"}"}}]},"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
			}`)
			return
		}
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-2","model":"gpt-4o",
			"choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`)
	}))
	defer server.Close()

	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai"

	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	store, err := storage.New(t.TempDir() + "/history.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSession("session-h"); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}

	registry := tool.NewEmptyRegistry()
	registry.Register(fakeEchoTool{})

	runner := &Runner{
		sessionID:     "session-h",
		session:       &storage.Session{ID: "session-h"},
		conv:          conversation.New("session-h"),
		store:         store,
		config:        cfg,
		modelManager:  mgr,
		tools:         registry,
		modelOverride: "gpt-4o",
		approvalChan:  make(chan ApprovalResponse, 1),
	}

	if err := runner.processUserInput("please echo hi"); err != nil {
		t.Fatalf("processUserInput: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("expected 2 model requests, got %d", len(bodies))
	}

	var second map[string]any
	if err := json.Unmarshal([]byte(bodies[1]), &second); err != nil {
		t.Fatalf("decode second request: %v", err)
	}
	raw := bodies[1]
	if !strings.Contains(raw, "call_hist_1") {
		t.Fatalf("second request missing assistant tool-call message: %s", raw)
	}
	if !strings.Contains(raw, `"role":"tool"`) {
		t.Fatalf("second request missing tool result message: %s", raw)
	}

	// Persistence: a fresh conversation loaded from storage must contain
	// exactly one assistant tool-call message, its tool result, and one
	// final assistant message (no duplicates).
	reloaded := conversation.New("session-h")
	if err := reloaded.LoadFromStorage(store); err != nil {
		t.Fatalf("reload conversation: %v", err)
	}
	toolCalls, toolResults, finals := 0, 0, 0
	for _, m := range reloaded.Messages {
		switch {
		case m.Role == "assistant" && len(m.ToolCalls) > 0:
			toolCalls++
		case m.Role == "tool":
			toolResults++
		case m.Role == "assistant":
			finals++
		}
	}
	if toolCalls != 1 || toolResults != 1 || finals != 1 {
		t.Fatalf("persisted transcript wrong: toolCalls=%d toolResults=%d finals=%d", toolCalls, toolResults, finals)
	}
}
