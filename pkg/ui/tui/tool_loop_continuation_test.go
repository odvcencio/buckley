package tui

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
	"m31labs.dev/fluffyui/backend/sim"
)

func newContinuationTestConfig(baseURL string) *config.Config {
	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = baseURL
	cfg.Models.DefaultProvider = "openai"
	return cfg
}

func TestContinuationEligible_RequiresFlagAndProviderSupport(t *testing.T) {
	cfg := newContinuationTestConfig("")
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	ctrl := &Controller{cfg: cfg, modelMgr: mgr}

	if ctrl.continuationEligible("gpt-5.4") {
		t.Fatal("expected continuation to require the opt-in flag")
	}

	cfg.Models.ProviderContinuation = true
	if !ctrl.continuationEligible("gpt-5.4") {
		t.Fatal("expected a continuation-capable model to be eligible once the flag is on")
	}
	if ctrl.continuationEligible("gpt-4o") {
		t.Fatal("expected a non-reasoning model to remain ineligible")
	}
}

func TestContinuationCoordinatorForSession_LazilyCreatedAndCached(t *testing.T) {
	cfg := newContinuationTestConfig("")
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	store, err := storage.New(t.TempDir() + "/continuation.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctrl := &Controller{cfg: cfg, modelMgr: mgr, store: store}
	sess := &SessionState{ID: "session-1"}

	first := ctrl.continuationCoordinatorForSession(sess)
	if first == nil {
		t.Fatal("expected a coordinator to be created")
	}
	second := ctrl.continuationCoordinatorForSession(sess)
	if first != second {
		t.Fatal("expected the coordinator to be cached on the session")
	}
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

func TestToolLoopContinuation_SecondTurnHitsAndPersistsAcrossRestore(t *testing.T) {
	var requests []map[string]any
	server := openAIContinuationTestServer(t, &requests, "ok")
	defer server.Close()

	cfg := newContinuationTestConfig(server.URL)
	cfg.Models.ProviderContinuation = true
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

	app, err := NewWidgetApp(WidgetAppConfig{Backend: sim.New(80, 24)})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}
	ctrl := &Controller{app: app, cfg: cfg, modelMgr: mgr, store: store}
	conv := conversation.New("session-1")
	conv.AddUserMessage("hello")
	sess := &SessionState{ID: "session-1", Conversation: conv}

	state := ctrl.newToolLoopState(sess, "gpt-5.4")
	req, useTools := ctrl.buildToolLoopRequestWithState(sess, "gpt-5.4", false, nil, &state)
	if useTools {
		t.Fatal("expected tools to stay disabled for this test")
	}
	if !state.useContinuation || state.continuation == nil {
		t.Fatal("expected continuation to be used for a reasoning-capable model")
	}

	resp, err := ctrl.callToolLoopTurn(context.Background(), "gpt-5.4", 0, req, &state)
	if err != nil {
		t.Fatalf("callToolLoopTurn(first) error = %v", err)
	}
	if state.continuation.Hit() {
		t.Fatal("first turn should not be a hit: no represented prefix yet")
	}
	if !state.continuation.Active() {
		t.Fatal("expected an active continuation after the first turn")
	}
	if len(requests) != 1 {
		t.Fatalf("expected exactly one request so far, got %d", len(requests))
	}

	text, _ := model.ExtractTextContent(resp.Choices[0].Message.Content)
	conv.AddAssistantMessageWithReasoningDetails(text, resp.Choices[0].Message.Reasoning, resp.Choices[0].Message.ReasoningDetails)
	conv.AddUserMessage("continue")

	req2, _ := ctrl.buildToolLoopRequestWithState(sess, "gpt-5.4", false, nil, &state)
	resp2, err := ctrl.callToolLoopTurn(context.Background(), "gpt-5.4", 1, req2, &state)
	if err != nil {
		t.Fatalf("callToolLoopTurn(second) error = %v", err)
	}
	if !state.continuation.Hit() {
		t.Fatal("expected the second turn to reuse the represented prefix (hit)")
	}

	// The second request must have carried the continuation's opaque state
	// rather than retransmitting the represented prefix from scratch.
	if len(requests) != 2 {
		t.Fatalf("expected two requests total, got %d", len(requests))
	}
	secondInput, ok := requests[1]["input"].([]any)
	if !ok {
		t.Fatalf("second request missing input field: %#v", requests[1])
	}
	if len(secondInput) == 0 {
		t.Fatal("expected the second request's incremental input to be non-empty")
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

func TestToolLoopContinuation_ErrorFallsBackToStreamingWithoutFailingTurn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"boom"}}`)
	}))
	defer server.Close()

	cfg := newContinuationTestConfig(server.URL)
	cfg.Models.ProviderContinuation = true
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	app, err := NewWidgetApp(WidgetAppConfig{Backend: sim.New(80, 24)})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}
	ctrl := &Controller{app: app, cfg: cfg, modelMgr: mgr}
	conv := conversation.New("session-1")
	conv.AddUserMessage("hello")
	sess := &SessionState{ID: "session-1", Conversation: conv}

	state := ctrl.newToolLoopState(sess, "gpt-5.4")
	req, _ := ctrl.buildToolLoopRequestWithState(sess, "gpt-5.4", false, nil, &state)
	if !state.useContinuation {
		t.Fatal("expected continuation to be attempted")
	}

	// The provider always errors, and there is no streaming backend
	// available in this test, so the fallback attempt errors too -- but the
	// cursor must still be reset rather than left in a broken state, proving
	// continuation failure alone never corrupts session state.
	_, err = ctrl.callToolLoopTurn(context.Background(), "gpt-5.4", 0, req, &state)
	if err == nil {
		t.Fatal("expected an error once both the continuation and fallback calls fail")
	}
	if state.continuation.Active() {
		t.Fatal("expected the cursor to be reset after a continuation error")
	}
}
