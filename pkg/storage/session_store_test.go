package storage

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSessionStoreLifecycle(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "session.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	sess := &Session{
		ID:          "sess-123",
		ProjectPath: "/project",
		GitRepo:     "repo",
		GitBranch:   "main",
		CreatedAt:   time.Now(),
		LastActive:  time.Now(),
		Status:      SessionStatusActive,
	}
	if err := store.CreateSession(sess); err != nil {
		t.Fatalf("create session: %v", err)
	}

	fetched, err := store.GetSession("sess-123")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if fetched == nil || fetched.ID != "sess-123" {
		t.Fatalf("expected session to exist, got %+v", fetched)
	}

	// EnsureSession should be a no-op if session already exists.
	if err := store.EnsureSession("sess-123"); err != nil {
		t.Fatalf("ensure existing session: %v", err)
	}

	// List sessions should return our session.
	list, err := store.ListSessions(10)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(list) != 1 || list[0].ID != "sess-123" {
		t.Fatalf("expected session in list, got %+v", list)
	}

	// Update stats and verify SetSessionStatus toggles completion.
	if err := store.SetSessionStatus("sess-123", SessionStatusCompleted); err != nil {
		t.Fatalf("set session status: %v", err)
	}
	fetched, _ = store.GetSession("sess-123")
	if fetched == nil || fetched.Status != SessionStatusCompleted || fetched.CompletedAt == nil {
		t.Fatalf("expected completed session, got %+v", fetched)
	}

	// Update activity
	if err := store.UpdateSessionActivity("sess-123"); err != nil {
		t.Fatalf("update activity: %v", err)
	}

	// Delete session and verify removal.
	if err := store.DeleteSession("sess-123"); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	fetched, err = store.GetSession("sess-123")
	if err != nil {
		t.Fatalf("get session after delete: %v", err)
	}
	if fetched != nil {
		t.Fatalf("expected session to be deleted, got %+v", fetched)
	}
}

func TestListSessionsByRepo(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Create sessions with different repos
	sess1 := &Session{
		ID:          "sess-1",
		ProjectPath: "/project/repo1",
		GitRepo:     "repo1",
		GitBranch:   "main",
		CreatedAt:   time.Now(),
		LastActive:  time.Now(),
		Status:      SessionStatusActive,
	}
	if err := store.CreateSession(sess1); err != nil {
		t.Fatalf("failed to create session 1: %v", err)
	}

	sess2 := &Session{
		ID:          "sess-2",
		ProjectPath: "/project/repo1",
		GitRepo:     "repo1",
		GitBranch:   "dev",
		CreatedAt:   time.Now(),
		LastActive:  time.Now(),
		Status:      SessionStatusActive,
	}
	if err := store.CreateSession(sess2); err != nil {
		t.Fatalf("failed to create session 2: %v", err)
	}

	sess3 := &Session{
		ID:          "sess-3",
		ProjectPath: "/project/repo2",
		GitRepo:     "repo2",
		GitBranch:   "main",
		CreatedAt:   time.Now(),
		LastActive:  time.Now(),
		Status:      SessionStatusActive,
	}
	if err := store.CreateSession(sess3); err != nil {
		t.Fatalf("failed to create session 3: %v", err)
	}

	// List sessions by repo
	sessions, err := store.ListSessionsByRepo("repo1")
	if err != nil {
		t.Fatalf("failed to list sessions by repo: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions for repo1, got %d", len(sessions))
	}

	// List sessions by project path
	sessions, err = store.ListSessionsByRepo("/project/repo2")
	if err != nil {
		t.Fatalf("failed to list sessions by project path: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session for /project/repo2, got %d", len(sessions))
	}
	if sessions[0].ID != "sess-3" {
		t.Errorf("expected sess-3, got %s", sessions[0].ID)
	}

	// Empty repo should return empty list
	sessions, err = store.ListSessionsByRepo("")
	if err != nil {
		t.Fatalf("failed to list sessions with empty repo: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected empty list for empty repo, got %d sessions", len(sessions))
	}

	// Whitespace repo should return empty list
	sessions, err = store.ListSessionsByRepo("   ")
	if err != nil {
		t.Fatalf("failed to list sessions with whitespace repo: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected empty list for whitespace repo, got %d sessions", len(sessions))
	}

	// Nonexistent repo should return empty list
	sessions, err = store.ListSessionsByRepo("nonexistent")
	if err != nil {
		t.Fatalf("failed to list sessions for nonexistent repo: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected empty list for nonexistent repo, got %d sessions", len(sessions))
	}
}

func TestUpdateSessionStats(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	sess := &Session{
		ID:          "sess-123",
		ProjectPath: "/project",
		GitRepo:     "repo",
		GitBranch:   "main",
		CreatedAt:   time.Now(),
		LastActive:  time.Now(),
		Status:      SessionStatusActive,
	}
	if err := store.CreateSession(sess); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	// Update stats
	if err := store.UpdateSessionStats("sess-123", 10, 5000, 0.05); err != nil {
		t.Fatalf("failed to update session stats: %v", err)
	}

	// Verify stats were updated
	fetched, err := store.GetSession("sess-123")
	if err != nil {
		t.Fatalf("failed to get session: %v", err)
	}
	if fetched.MessageCount != 10 {
		t.Errorf("expected MessageCount=10, got %d", fetched.MessageCount)
	}
	if fetched.TotalTokens != 5000 {
		t.Errorf("expected TotalTokens=5000, got %d", fetched.TotalTokens)
	}
	if fetched.TotalCost != 0.05 {
		t.Errorf("expected TotalCost=0.05, got %f", fetched.TotalCost)
	}

	// Update stats again with different values
	if err := store.UpdateSessionStats("sess-123", 25, 12000, 0.15); err != nil {
		t.Fatalf("failed to update session stats again: %v", err)
	}

	// Verify new stats
	fetched, err = store.GetSession("sess-123")
	if err != nil {
		t.Fatalf("failed to get session after second update: %v", err)
	}
	if fetched.MessageCount != 25 {
		t.Errorf("expected MessageCount=25, got %d", fetched.MessageCount)
	}
	if fetched.TotalTokens != 12000 {
		t.Errorf("expected TotalTokens=12000, got %d", fetched.TotalTokens)
	}
	if fetched.TotalCost != 0.15 {
		t.Errorf("expected TotalCost=0.15, got %f", fetched.TotalCost)
	}
}
