package storage

import (
	"path/filepath"
	"testing"
	"time"
)

func TestProviderContinuationStoreLifecycle(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "provider-continuations.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	session := &Session{
		ID:         "session-1",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	state, err := store.LoadProviderContinuation(session.ID, "openai", "openai/gpt-5.4")
	if err != nil || state != "" {
		t.Fatalf("LoadProviderContinuation (missing) = %q, %v", state, err)
	}

	if err := store.SaveProviderContinuation(session.ID, "openai", "openai/gpt-5.4", `{"version":1,"items":[]}`); err != nil {
		t.Fatalf("SaveProviderContinuation: %v", err)
	}
	state, err = store.LoadProviderContinuation(session.ID, "openai", "openai/gpt-5.4")
	if err != nil || state != `{"version":1,"items":[]}` {
		t.Fatalf("LoadProviderContinuation = %q, %v", state, err)
	}

	if err := store.SaveProviderContinuation(session.ID, "openai", "openai/gpt-5.4", `{"version":1,"items":["a"]}`); err != nil {
		t.Fatalf("update provider continuation: %v", err)
	}
	state, err = store.LoadProviderContinuation(session.ID, "openai", "openai/gpt-5.4")
	if err != nil || state != `{"version":1,"items":["a"]}` {
		t.Fatalf("updated LoadProviderContinuation = %q, %v", state, err)
	}

	if err := store.DeleteProviderContinuation(session.ID, "openai"); err != nil {
		t.Fatalf("DeleteProviderContinuation: %v", err)
	}
	state, err = store.LoadProviderContinuation(session.ID, "openai", "openai/gpt-5.4")
	if err != nil || state != "" {
		t.Fatalf("deleted LoadProviderContinuation = %q, %v", state, err)
	}
}

func TestProviderContinuationStoreKeyedByModel(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "provider-continuations-model.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.EnsureSession("session-1"); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}
	if err := store.SaveProviderContinuation("session-1", "openai", "openai/gpt-5.4", `{"a":1}`); err != nil {
		t.Fatalf("SaveProviderContinuation (model a): %v", err)
	}
	if err := store.SaveProviderContinuation("session-1", "openai", "openai/gpt-5.4-mini", `{"b":2}`); err != nil {
		t.Fatalf("SaveProviderContinuation (model b): %v", err)
	}

	stateA, err := store.LoadProviderContinuation("session-1", "openai", "openai/gpt-5.4")
	if err != nil || stateA != `{"a":1}` {
		t.Fatalf("LoadProviderContinuation (model a) = %q, %v", stateA, err)
	}
	stateB, err := store.LoadProviderContinuation("session-1", "openai", "openai/gpt-5.4-mini")
	if err != nil || stateB != `{"b":2}` {
		t.Fatalf("LoadProviderContinuation (model b) = %q, %v", stateB, err)
	}

	// Deleting by session/provider invalidates every model's continuation,
	// since a provider change forces a fresh window regardless of model.
	if err := store.DeleteProviderContinuation("session-1", "openai"); err != nil {
		t.Fatalf("DeleteProviderContinuation: %v", err)
	}
	if stateA, _ := store.LoadProviderContinuation("session-1", "openai", "openai/gpt-5.4"); stateA != "" {
		t.Fatalf("expected model a continuation cleared, got %q", stateA)
	}
	if stateB, _ := store.LoadProviderContinuation("session-1", "openai", "openai/gpt-5.4-mini"); stateB != "" {
		t.Fatalf("expected model b continuation cleared, got %q", stateB)
	}
}

func TestProviderContinuationStoreCascadesWithSession(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "provider-continuations-cascade.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.EnsureSession("session-1"); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}
	if err := store.SaveProviderContinuation("session-1", "openai", "openai/gpt-5.4", `{"a":1}`); err != nil {
		t.Fatalf("SaveProviderContinuation: %v", err)
	}
	if err := store.DeleteSession("session-1"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	state, err := store.LoadProviderContinuation("session-1", "openai", "openai/gpt-5.4")
	if err != nil || state != "" {
		t.Fatalf("cascaded LoadProviderContinuation = %q, %v", state, err)
	}
}
