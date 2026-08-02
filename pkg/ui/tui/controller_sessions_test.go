package tui

import (
	"path/filepath"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/storage"
)

func newControllerSessionTestConfig(t *testing.T) (ControllerConfig, *storage.Store, string) {
	t.Helper()

	workDir := t.TempDir()
	t.Chdir(workDir)
	t.Setenv("HOME", t.TempDir())

	store, err := storage.New(filepath.Join(t.TempDir(), "buckley.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})

	cfg := ControllerConfig{
		Config: config.DefaultConfig(),
		Store:  store,
	}
	return cfg, store, workDir
}

func createControllerTestSession(t *testing.T, store *storage.Store, id, projectPath, status string, lastActive time.Time) {
	t.Helper()

	if err := store.CreateSession(&storage.Session{
		ID:          id,
		ProjectPath: projectPath,
		CreatedAt:   lastActive.Add(-time.Minute),
		LastActive:  lastActive,
		Status:      status,
	}); err != nil {
		t.Fatalf("CreateSession(%s): %v", id, err)
	}
}

func TestLoadOrCreateControllerSessions_CreatesGeneratedSession(t *testing.T) {
	cfg, store, workDir := newControllerSessionTestConfig(t)

	sessions, current, err := loadOrCreateControllerSessions(cfg, workDir)
	if err != nil {
		t.Fatalf("loadOrCreateControllerSessions: %v", err)
	}
	if current != 0 {
		t.Fatalf("current session index = %d, want 0", current)
	}
	if len(sessions) != 1 {
		t.Fatalf("session count = %d, want 1", len(sessions))
	}
	if sessions[0].ID == "" {
		t.Fatal("generated session ID should not be empty")
	}

	stored, err := store.ListSessions(10)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("stored session count = %d, want 1", len(stored))
	}
	if stored[0].ID != sessions[0].ID {
		t.Fatalf("stored session ID = %q, want %q", stored[0].ID, sessions[0].ID)
	}
	if stored[0].ProjectPath != workDir {
		t.Fatalf("stored project path = %q, want %q", stored[0].ProjectPath, workDir)
	}
}

func TestLoadOrCreateControllerSessions_ResumesActiveProjectSessions(t *testing.T) {
	cfg, store, workDir := newControllerSessionTestConfig(t)
	now := time.Now()
	createControllerTestSession(t, store, "old", workDir, storage.SessionStatusActive, now.Add(-2*time.Hour))
	createControllerTestSession(t, store, "latest", workDir, storage.SessionStatusActive, now.Add(-time.Hour))
	createControllerTestSession(t, store, "completed", workDir, storage.SessionStatusCompleted, now)
	createControllerTestSession(t, store, "other-project", filepath.Join(workDir, "other"), storage.SessionStatusActive, now.Add(time.Hour))
	if err := store.SaveMessage(&storage.Message{
		SessionID: "latest",
		Role:      "user",
		Content:   "hello",
		Timestamp: now,
	}); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}

	sessions, current, err := loadOrCreateControllerSessions(cfg, workDir)
	if err != nil {
		t.Fatalf("loadOrCreateControllerSessions: %v", err)
	}
	if current != 0 {
		t.Fatalf("current session index = %d, want 0", current)
	}
	if len(sessions) != 2 {
		t.Fatalf("session count = %d, want 2", len(sessions))
	}
	if sessions[0].ID != "latest" {
		t.Fatalf("first session ID = %q, want latest", sessions[0].ID)
	}
	if got := len(sessions[0].Conversation.Messages); got != 1 {
		t.Fatalf("loaded message count = %d, want 1", got)
	}
}

func TestLoadOrCreateControllerSessions_PrefersSessionWithHistory(t *testing.T) {
	cfg, store, workDir := newControllerSessionTestConfig(t)
	now := time.Now()
	createControllerTestSession(t, store, "empty-newer", workDir, storage.SessionStatusActive, now)
	createControllerTestSession(t, store, "history", workDir, storage.SessionStatusActive, now.Add(-time.Minute))
	if err := store.SaveMessage(&storage.Message{SessionID: "history", Role: "user", Content: "resume me", Timestamp: now.Add(-time.Minute)}); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}

	sessions, current, err := loadOrCreateControllerSessions(cfg, workDir)
	if err != nil {
		t.Fatalf("loadOrCreateControllerSessions: %v", err)
	}
	if sessions[current].ID != "history" {
		t.Fatalf("current session = %q, want history", sessions[current].ID)
	}
}

func TestLoadOrCreateControllerSessions_SelectsRequestedActiveSession(t *testing.T) {
	cfg, store, workDir := newControllerSessionTestConfig(t)
	cfg.SessionID = "old"
	now := time.Now()
	createControllerTestSession(t, store, "latest", workDir, storage.SessionStatusActive, now)
	createControllerTestSession(t, store, "old", workDir, storage.SessionStatusActive, now.Add(-time.Hour))

	sessions, current, err := loadOrCreateControllerSessions(cfg, workDir)
	if err != nil {
		t.Fatalf("loadOrCreateControllerSessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("session count = %d, want 2", len(sessions))
	}
	if sessions[current].ID != "old" {
		t.Fatalf("current session ID = %q, want old", sessions[current].ID)
	}
}
