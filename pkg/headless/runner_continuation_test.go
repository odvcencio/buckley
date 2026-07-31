package headless

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"m31labs.dev/buckley/v2/pkg/config"
	"m31labs.dev/buckley/v2/pkg/conversation"
	"m31labs.dev/buckley/v2/pkg/model"
	"m31labs.dev/buckley/v2/pkg/storage"
)

func newContinuationTestConfig(baseURL string) *config.Config {
	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = baseURL
	cfg.Models.DefaultProvider = "openai"
	cfg.Models.ProviderContinuation = true
	return cfg
}

func openAIContinuationTestServer(t *testing.T, requests *[]map[string]any, text string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		*requests = append(*requests, decoded)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"resp_1",
			"model":"gpt-5.4",
			"status":"completed",
			"output":[{
				"id":"msg_1",
				"type":"message",
				"role":"assistant",
				"status":"completed",
				"content":[{"type":"output_text","text":"`+text+`"}]
			}]
		}`)
	}))
}

func TestRunner_ContinuationEligibleRequiresFlagAndProviderSupport(t *testing.T) {
	cfg := newContinuationTestConfig("")
	cfg.Models.ProviderContinuation = false
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	runner := &Runner{config: cfg, modelManager: mgr}

	if runner.continuationEligible("gpt-5.4") {
		t.Fatal("expected continuation to require the opt-in flag")
	}

	cfg.Models.ProviderContinuation = true
	if !runner.continuationEligible("gpt-5.4") {
		t.Fatal("expected a continuation-capable model to be eligible once the flag is on")
	}
	if runner.continuationEligible("gpt-4o") {
		t.Fatal("expected a non-reasoning model to remain ineligible")
	}
}

func TestRunner_ContinuationHitsOnSecondTurnAndPersists(t *testing.T) {
	var requests []map[string]any
	server := openAIContinuationTestServer(t, &requests, "ok")
	defer server.Close()

	cfg := newContinuationTestConfig(server.URL)
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	store, err := storage.New(t.TempDir() + "/continuation.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSession("session-1"); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}

	conv := conversation.New("session-1")
	conv.AddUserMessage("hello")

	runner := &Runner{
		sessionID:     "session-1",
		config:        cfg,
		modelManager:  mgr,
		conv:          conv,
		store:         store,
		modelOverride: "gpt-5.4",
	}

	req, useContinuation := runner.buildChatRequest()
	if !useContinuation {
		t.Fatal("expected continuation to be used for a reasoning-capable model")
	}

	resp, err := runner.callModel(context.Background(), req, useContinuation)
	if err != nil {
		t.Fatalf("callModel(first) error = %v", err)
	}
	if runner.continuation.Hit() {
		t.Fatal("first turn should not be a hit: no represented prefix yet")
	}
	if !runner.continuation.Active() {
		t.Fatal("expected an active continuation after the first turn")
	}
	if len(requests) != 1 {
		t.Fatalf("expected exactly one request so far, got %d", len(requests))
	}

	text, _ := model.ExtractTextContent(resp.Choices[0].Message.Content)
	conv.AddAssistantMessageWithReasoningDetails(text, resp.Choices[0].Message.Reasoning, resp.Choices[0].Message.ReasoningDetails)
	conv.AddUserMessage("continue")

	req2, useContinuation2 := runner.buildChatRequest()
	if !useContinuation2 {
		t.Fatal("expected continuation to still be used on the second turn")
	}
	resp2, err := runner.callModel(context.Background(), req2, useContinuation2)
	if err != nil {
		t.Fatalf("callModel(second) error = %v", err)
	}
	if !runner.continuation.Hit() {
		t.Fatal("expected the second turn to reuse the represented prefix (hit)")
	}
	if len(requests) != 2 {
		t.Fatalf("expected two requests total, got %d", len(requests))
	}

	// Persistence: a fresh coordinator restored for the same session should
	// come back active without another round trip.
	finalTranscript := append(append([]model.Message(nil), req2.Messages...), resp2.Choices[0].Message)
	restored := model.NewContinuationCoordinator(mgr, store, "session-1")
	restored.Restore("openai", "openai/gpt-5.4", finalTranscript)
	if !restored.Active() {
		t.Fatal("expected restored coordinator to be active from persisted state")
	}
}

func TestRunner_ContinuationErrorFallsBackToNormalChatCompletion(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/responses" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"message":"boom"}}`)
			return
		}
		// Normal chat/completions fallback path.
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-1",
			"model":"gpt-5.4",
			"choices":[{"index":0,"message":{"role":"assistant","content":"fallback ok"},"finish_reason":"stop"}]
		}`)
	}))
	defer server.Close()

	cfg := newContinuationTestConfig(server.URL)
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	conv := conversation.New("session-1")
	conv.AddUserMessage("hello")

	runner := &Runner{
		sessionID:     "session-1",
		config:        cfg,
		modelManager:  mgr,
		conv:          conv,
		modelOverride: "gpt-5.4",
	}

	req, useContinuation := runner.buildChatRequest()
	if !useContinuation {
		t.Fatal("expected continuation to be attempted")
	}

	resp, err := runner.callModel(context.Background(), req, useContinuation)
	if err != nil {
		t.Fatalf("expected the normal-path fallback to succeed, got error: %v", err)
	}
	if resp == nil || len(resp.Choices) == 0 || resp.Choices[0].Message.Content != "fallback ok" {
		t.Fatalf("expected fallback response content, got %#v", resp)
	}
	if runner.continuation.Active() {
		t.Fatal("expected the cursor to be reset after a continuation error")
	}
}
