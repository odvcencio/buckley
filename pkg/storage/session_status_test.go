package storage

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSetSessionStatusCompleteAndReopen(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "buckley.db")
	store, err := New(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now()
	session := &Session{
		ID:         "session-status",
		CreatedAt:  now,
		LastActive: now,
		Status:     SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("CreateSession error: %v", err)
	}

	if err := store.SetSessionStatus(session.ID, SessionStatusCompleted); err != nil {
		t.Fatalf("SetSessionStatus complete error: %v", err)
	}
	got, err := store.GetSession(session.ID)
	if err != nil {
		t.Fatalf("GetSession error: %v", err)
	}
	if got.Status != SessionStatusCompleted {
		t.Fatalf("expected status completed, got %s", got.Status)
	}
	if got.CompletedAt == nil {
		t.Fatalf("expected completed timestamp to be set")
	}

	if err := store.SetSessionStatus(session.ID, SessionStatusActive); err != nil {
		t.Fatalf("SetSessionStatus reopen error: %v", err)
	}
	got, err = store.GetSession(session.ID)
	if err != nil {
		t.Fatalf("GetSession error: %v", err)
	}
	if got.Status != SessionStatusActive {
		t.Fatalf("expected status active after reopen, got %s", got.Status)
	}
	if got.CompletedAt != nil {
		t.Fatalf("expected completed timestamp cleared after reopen")
	}
}
