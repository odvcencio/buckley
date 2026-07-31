package model

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"m31labs.dev/buckley/v2/pkg/config"
)

// fakeContinuationStore is an in-memory ContinuationStore for coordinator tests.
type fakeContinuationStore struct {
	mu    sync.Mutex
	state map[string]string // key: sessionID/providerID/modelID
}

func newFakeContinuationStore() *fakeContinuationStore {
	return &fakeContinuationStore{state: make(map[string]string)}
}

func (f *fakeContinuationStore) key(sessionID, providerID, modelID string) string {
	return sessionID + "/" + providerID + "/" + modelID
}

func (f *fakeContinuationStore) LoadProviderContinuation(sessionID, providerID, modelID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state[f.key(sessionID, providerID, modelID)], nil
}

func (f *fakeContinuationStore) SaveProviderContinuation(sessionID, providerID, modelID, state string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state[f.key(sessionID, providerID, modelID)] = state
	return nil
}

func (f *fakeContinuationStore) DeleteProviderContinuation(sessionID, providerID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	prefix := sessionID + "/" + providerID + "/"
	for k := range f.state {
		if strings.HasPrefix(k, prefix) {
			delete(f.state, k)
		}
	}
	return nil
}

func newContinuationTestManager(t *testing.T, handler http.HandlerFunc) (*Manager, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	provider := NewOpenAIProvider("test-key", server.URL, false)
	provider.httpClient = server.Client()
	manager := &Manager{
		config: &config.Config{
			Models: config.ModelConfig{DefaultProvider: "openai"},
		},
		providers:      map[string]Provider{"openai": provider},
		providerOrder:  []string{"openai"},
		catalog:        map[string]ModelInfo{},
		providerModels: map[string][]string{},
		modelProviders: map[string]string{"gpt-5.4": "openai"},
	}
	return manager, server
}

func openAIContinuationResponseHandler(t *testing.T, text string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatalf("decode request: %v", err)
		}
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
	}
}

func TestContinuationCoordinator_EligibleChecksProviderCapability(t *testing.T) {
	manager, _ := newContinuationTestManager(t, openAIContinuationResponseHandler(t, "ok"))
	cc := NewContinuationCoordinator(manager, nil, "session-1")

	if !cc.Eligible("gpt-5.4") {
		t.Fatal("expected reasoning model to be continuation-eligible")
	}
	if cc.Eligible("gpt-4o") {
		t.Fatal("expected non-reasoning model to be ineligible")
	}
}

func TestContinuationCoordinator_CallAccumulatesHitsAndPersists(t *testing.T) {
	manager, _ := newContinuationTestManager(t, openAIContinuationResponseHandler(t, "done"))
	store := newFakeContinuationStore()
	cc := NewContinuationCoordinator(manager, store, "session-1")

	req := ChatRequest{
		Model: "gpt-5.4",
		Messages: []Message{
			{Role: "system", Content: "system"},
			{Role: "user", Content: "hello"},
		},
	}
	resp, err := cc.Call(context.Background(), req)
	if err != nil {
		t.Fatalf("Call(first) error = %v", err)
	}
	if resp == nil || len(resp.Choices) == 0 {
		t.Fatalf("Call(first) response = %#v", resp)
	}
	if cc.Hit() {
		t.Fatal("first call should not be a hit (no represented prefix yet)")
	}
	if !cc.Active() {
		t.Fatal("expected coordinator to hold continuation state after first call")
	}

	raw, err := store.LoadProviderContinuation("session-1", "openai", "openai/gpt-5.4")
	if err != nil || strings.TrimSpace(raw) == "" {
		t.Fatalf("expected persisted continuation state, got %q, err %v", raw, err)
	}

	// Second turn: append the assistant response plus new user input, mirroring
	// what a tool loop would do.
	req.Messages = append(req.Messages,
		Message{Role: "assistant", Content: "done"},
		Message{Role: "user", Content: "continue"},
	)
	resp2, err := cc.Call(context.Background(), req)
	if err != nil {
		t.Fatalf("Call(second) error = %v", err)
	}
	if resp2 == nil {
		t.Fatal("Call(second) returned nil response")
	}
	if !cc.Hit() {
		t.Fatal("second call should reuse the represented prefix (hit)")
	}
}

func TestContinuationCoordinator_ResetClearsRuntimeAndPersistedState(t *testing.T) {
	manager, _ := newContinuationTestManager(t, openAIContinuationResponseHandler(t, "done"))
	store := newFakeContinuationStore()
	cc := NewContinuationCoordinator(manager, store, "session-1")

	req := ChatRequest{Model: "gpt-5.4", Messages: []Message{{Role: "user", Content: "hi"}}}
	if _, err := cc.Call(context.Background(), req); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !cc.Active() {
		t.Fatal("expected active continuation before reset")
	}

	cc.Reset()

	if cc.Active() {
		t.Fatal("expected Reset to clear runtime cursor state")
	}
	if cc.PinnedFromIndex() != 0 {
		t.Fatalf("PinnedFromIndex after reset = %d, want 0", cc.PinnedFromIndex())
	}
	raw, err := store.LoadProviderContinuation("session-1", "openai", "openai/gpt-5.4")
	if err != nil || raw != "" {
		t.Fatalf("expected persisted state cleared after Reset, got %q, err %v", raw, err)
	}
}

func TestContinuationCoordinator_CallErrorResetsCursor(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"boom"}}`)
	}
	manager, _ := newContinuationTestManager(t, handler)
	store := newFakeContinuationStore()
	cc := NewContinuationCoordinator(manager, store, "session-1")

	req := ChatRequest{Model: "gpt-5.4", Messages: []Message{{Role: "user", Content: "hi"}}}
	if _, err := cc.Call(context.Background(), req); err == nil {
		t.Fatal("expected Call to surface the provider error")
	}
	if cc.Active() {
		t.Fatal("expected a provider error to reset the cursor")
	}
}

func TestContinuationCoordinator_RestoreReDerivesFingerprintsFromTranscript(t *testing.T) {
	manager, _ := newContinuationTestManager(t, openAIContinuationResponseHandler(t, "done"))
	store := newFakeContinuationStore()

	transcript := []Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "hello"},
	}
	persisted := persistedContinuation{
		Continuation: ProviderContinuation{
			ProviderID: "openai",
			ModelID:    "openai/gpt-5.4",
			State:      json.RawMessage(`{"version":1,"items":[]}`),
		},
		RepresentedCount: len(transcript),
	}
	encoded, err := json.Marshal(persisted)
	if err != nil {
		t.Fatalf("marshal persisted continuation: %v", err)
	}
	if err := store.SaveProviderContinuation("session-1", "openai", "openai/gpt-5.4", string(encoded)); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	cc := NewContinuationCoordinator(manager, store, "session-1")
	cc.Restore("openai", "openai/gpt-5.4", transcript)

	if !cc.Active() {
		t.Fatal("expected Restore to seed an active cursor")
	}
	if cc.PinnedFromIndex() != len(transcript) {
		t.Fatalf("PinnedFromIndex = %d, want %d", cc.PinnedFromIndex(), len(transcript))
	}
}

func TestContinuationCoordinator_RestoreSkipsIncompatibleProviderOrModel(t *testing.T) {
	manager, _ := newContinuationTestManager(t, openAIContinuationResponseHandler(t, "done"))
	store := newFakeContinuationStore()

	persisted := persistedContinuation{
		Continuation: ProviderContinuation{
			ProviderID: "openai",
			ModelID:    "openai/gpt-5.4",
			State:      json.RawMessage(`{"version":1,"items":[]}`),
		},
		RepresentedCount: 1,
	}
	encoded, _ := json.Marshal(persisted)
	if err := store.SaveProviderContinuation("session-1", "openai", "openai/gpt-5.4", string(encoded)); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	cc := NewContinuationCoordinator(manager, store, "session-1")
	cc.Restore("openai", "openai/gpt-5.4-mini", []Message{{Role: "user", Content: "hi"}})
	if cc.Active() {
		t.Fatal("expected Restore to skip state scoped to a different model")
	}
}

func TestContinuationCoordinator_RestoreSkipsWhenRepresentedCountExceedsTranscript(t *testing.T) {
	manager, _ := newContinuationTestManager(t, openAIContinuationResponseHandler(t, "done"))
	store := newFakeContinuationStore()

	persisted := persistedContinuation{
		Continuation: ProviderContinuation{
			ProviderID: "openai",
			ModelID:    "openai/gpt-5.4",
			State:      json.RawMessage(`{"version":1,"items":[]}`),
		},
		RepresentedCount: 5,
	}
	encoded, _ := json.Marshal(persisted)
	if err := store.SaveProviderContinuation("session-1", "openai", "openai/gpt-5.4", string(encoded)); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	cc := NewContinuationCoordinator(manager, store, "session-1")
	cc.Restore("openai", "openai/gpt-5.4", []Message{{Role: "user", Content: "hi"}})
	if cc.Active() {
		t.Fatal("expected Restore to skip state whose prefix no longer fits the transcript")
	}
}
