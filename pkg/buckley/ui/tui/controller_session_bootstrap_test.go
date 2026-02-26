package tui

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/fluffyui/progress"
	"github.com/odvcencio/fluffyui/toast"
)

func TestLoadOrCreateProjectSessions_RequestedSessionMissing_CreatesAndSelects(t *testing.T) {
	t.Parallel()

	store, workDir := newSessionBootstrapTestStore(t)

	now := time.Now()
	for _, id := range []string{"active-a", "active-b"} {
		err := store.CreateSession(&storage.Session{
			ID:          id,
			ProjectPath: workDir,
			CreatedAt:   now,
			LastActive:  now,
			Status:      storage.SessionStatusActive,
		})
		if err != nil {
			t.Fatalf("create session %s: %v", id, err)
		}
	}

	cfg := ControllerConfig{
		Config:       config.DefaultConfig(),
		Store:        store,
		SessionID:    "requested-new",
		ModelManager: nil,
	}

	sessions, currentIdx, err := loadOrCreateProjectSessions(
		cfg,
		context.Background(),
		workDir,
		progress.NewProgressManager(),
		toast.NewToastManager(),
	)
	if err != nil {
		t.Fatalf("loadOrCreateProjectSessions: %v", err)
	}

	if len(sessions) == 0 {
		t.Fatal("expected at least one session")
	}
	if sessions[0].ID != "requested-new" {
		t.Fatalf("expected requested session at index 0, got %s", sessions[0].ID)
	}
	if currentIdx != 0 {
		t.Fatalf("expected currentIdx=0, got %d", currentIdx)
	}

	persisted, err := store.GetSession("requested-new")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if persisted == nil {
		t.Fatal("requested session was not created")
	}
	if persisted.Status != storage.SessionStatusActive {
		t.Fatalf("expected active status, got %s", persisted.Status)
	}
	if persisted.ProjectPath != workDir {
		t.Fatalf("expected project path %s, got %s", workDir, persisted.ProjectPath)
	}
}

func TestLoadOrCreateProjectSessions_RequestedSessionCompleted_ReactivatesAndSelects(t *testing.T) {
	t.Parallel()

	store, workDir := newSessionBootstrapTestStore(t)

	now := time.Now()
	err := store.CreateSession(&storage.Session{
		ID:          "requested-existing",
		ProjectPath: filepath.Join(workDir, "other-project"),
		CreatedAt:   now,
		LastActive:  now,
		Status:      storage.SessionStatusCompleted,
	})
	if err != nil {
		t.Fatalf("create completed session: %v", err)
	}
	err = store.CreateSession(&storage.Session{
		ID:          "active-main",
		ProjectPath: workDir,
		CreatedAt:   now,
		LastActive:  now,
		Status:      storage.SessionStatusActive,
	})
	if err != nil {
		t.Fatalf("create active session: %v", err)
	}

	cfg := ControllerConfig{
		Config:       config.DefaultConfig(),
		Store:        store,
		SessionID:    "requested-existing",
		ModelManager: nil,
	}

	sessions, currentIdx, err := loadOrCreateProjectSessions(
		cfg,
		context.Background(),
		workDir,
		progress.NewProgressManager(),
		toast.NewToastManager(),
	)
	if err != nil {
		t.Fatalf("loadOrCreateProjectSessions: %v", err)
	}

	if len(sessions) == 0 {
		t.Fatal("expected sessions")
	}
	if sessions[0].ID != "requested-existing" {
		t.Fatalf("expected requested session first, got %s", sessions[0].ID)
	}
	if currentIdx != 0 {
		t.Fatalf("expected currentIdx=0, got %d", currentIdx)
	}

	persisted, err := store.GetSession("requested-existing")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if persisted == nil {
		t.Fatal("requested session missing after load")
	}
	if persisted.Status != storage.SessionStatusActive {
		t.Fatalf("expected active status, got %s", persisted.Status)
	}
	if persisted.ProjectPath != workDir {
		t.Fatalf("expected project path %s, got %s", workDir, persisted.ProjectPath)
	}
}

func newSessionBootstrapTestStore(t *testing.T) (*storage.Store, string) {
	t.Helper()
	workDir := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "buckley.db")
	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	return store, workDir
}
