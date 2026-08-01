package headless

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"m31labs.dev/buckley/v2/pkg/config"
	"m31labs.dev/buckley/v2/pkg/conversation"
	"m31labs.dev/buckley/v2/pkg/model"
	"m31labs.dev/buckley/v2/pkg/storage"
	"m31labs.dev/buckley/v2/pkg/tool"
)

// TestRunner_UsageSumsAcrossRounds closes the usage-summation gap: every
// round published usage as telemetry (see callModel) but nothing ever
// summed it. The shared turn engine (pkg/agentloop.Controller) now
// accumulates model.Usage across every round of a turn, and Runner.Usage
// reports the running total across every turn the session has run.
func TestRunner_UsageSumsAcrossRounds(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "application/json")
		if requestCount == 1 {
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-1",
				"model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"echo_tool","arguments":"{\"text\":\"hi\"}"}}]},"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}
			}`)
			return
		}
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-2",
			"model":"gpt-4o",
			"choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":20,"completion_tokens":7,"total_tokens":27}
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

	store, err := storage.New(t.TempDir() + "/usage.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSession("session-1"); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}

	registry := tool.NewEmptyRegistry()
	registry.Register(fakeEchoTool{})

	runner := &Runner{
		sessionID:     "session-1",
		session:       &storage.Session{ID: "session-1"},
		conv:          conversation.New("session-1"),
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

	if requestCount != 2 {
		t.Fatalf("expected 2 model requests (one per round), got %d", requestCount)
	}

	usage := runner.Usage()
	if usage.PromptTokens != 30 {
		t.Fatalf("PromptTokens = %d, want 30 (10 + 20)", usage.PromptTokens)
	}
	if usage.CompletionTokens != 12 {
		t.Fatalf("CompletionTokens = %d, want 12 (5 + 7)", usage.CompletionTokens)
	}
	if usage.TotalTokens != 42 {
		t.Fatalf("TotalTokens = %d, want 42 (15 + 27)", usage.TotalTokens)
	}
}
