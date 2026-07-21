package storage

import (
	"path/filepath"
	"testing"
	"time"
)

func TestProviderThreadStoreLifecycle(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "provider-threads.db"))
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
	if err := store.SaveProviderThread(session.ID, "codex", "thread-1"); err != nil {
		t.Fatalf("SaveProviderThread: %v", err)
	}
	threadID, err := store.LoadProviderThread(session.ID, "codex")
	if err != nil || threadID != "thread-1" {
		t.Fatalf("LoadProviderThread = %q, %v", threadID, err)
	}
	if err := store.SaveProviderThread(session.ID, "codex", "thread-2"); err != nil {
		t.Fatalf("update provider thread: %v", err)
	}
	threadID, err = store.LoadProviderThread(session.ID, "codex")
	if err != nil || threadID != "thread-2" {
		t.Fatalf("updated LoadProviderThread = %q, %v", threadID, err)
	}
	if err := store.DeleteProviderThread(session.ID, "codex"); err != nil {
		t.Fatalf("DeleteProviderThread: %v", err)
	}
	threadID, err = store.LoadProviderThread(session.ID, "codex")
	if err != nil || threadID != "" {
		t.Fatalf("deleted LoadProviderThread = %q, %v", threadID, err)
	}
}

func TestProviderThreadStoreCascadesWithSession(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "provider-threads-cascade.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.EnsureSession("session-1"); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}
	if err := store.SaveProviderThread("session-1", "codex", "thread-1"); err != nil {
		t.Fatalf("SaveProviderThread: %v", err)
	}
	if err := store.DeleteSession("session-1"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	threadID, err := store.LoadProviderThread("session-1", "codex")
	if err != nil || threadID != "" {
		t.Fatalf("cascaded LoadProviderThread = %q, %v", threadID, err)
	}
}
