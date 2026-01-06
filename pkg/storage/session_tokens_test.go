package storage

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSessionTokensLifecycle(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	sessionID := "sess-123"
	token := "my-secret-token"

	// Create a session first (foreign key requirement)
	sess := &Session{
		ID:          sessionID,
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

	// Save token
	if err := store.SaveSessionToken(sessionID, token); err != nil {
		t.Fatalf("failed to save session token: %v", err)
	}

	// Validate correct token
	valid, err := store.ValidateSessionToken(sessionID, token)
	if err != nil {
		t.Fatalf("failed to validate session token: %v", err)
	}
	if !valid {
		t.Errorf("expected token to be valid")
	}

	// Validate incorrect token
	valid, err = store.ValidateSessionToken(sessionID, "wrong-token")
	if err != nil {
		t.Fatalf("failed to validate incorrect token: %v", err)
	}
	if valid {
		t.Errorf("expected incorrect token to be invalid")
	}

	// Validate non-existent session
	valid, err = store.ValidateSessionToken("nonexistent", token)
	if err != nil {
		t.Fatalf("unexpected error for nonexistent session: %v", err)
	}
	if valid {
		t.Errorf("expected nonexistent session token to be invalid")
	}

	// Update token
	newToken := "new-secret-token"
	if err := store.SaveSessionToken(sessionID, newToken); err != nil {
		t.Fatalf("failed to update session token: %v", err)
	}

	// Old token should no longer be valid
	valid, err = store.ValidateSessionToken(sessionID, token)
	if err != nil {
		t.Fatalf("failed to validate old token: %v", err)
	}
	if valid {
		t.Errorf("expected old token to be invalid after update")
	}

	// New token should be valid
	valid, err = store.ValidateSessionToken(sessionID, newToken)
	if err != nil {
		t.Fatalf("failed to validate new token: %v", err)
	}
	if !valid {
		t.Errorf("expected new token to be valid")
	}

	// Delete token
	if err := store.DeleteSessionToken(sessionID); err != nil {
		t.Fatalf("failed to delete session token: %v", err)
	}

	// Token should no longer be valid
	valid, err = store.ValidateSessionToken(sessionID, newToken)
	if err != nil {
		t.Fatalf("failed to validate token after delete: %v", err)
	}
	if valid {
		t.Errorf("expected token to be invalid after deletion")
	}
}

func TestSessionTokensClosedStore(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	_ = store.Close()

	// Test operations on closed store - should return an error
	if err := store.SaveSessionToken("sess-1", "token"); err == nil {
		t.Errorf("expected error for closed store, got nil")
	}

	_, err = store.ValidateSessionToken("sess-1", "token")
	if err == nil {
		t.Errorf("expected error for closed store, got nil")
	}

	if err := store.DeleteSessionToken("sess-1"); err == nil {
		t.Errorf("expected error for closed store, got nil")
	}
}

func TestSessionTokensWhitespace(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	sessionID := "sess-123"
	token := "  my-secret-token  "
	trimmedToken := "my-secret-token"

	// Create a session first (foreign key requirement)
	sess := &Session{
		ID:          sessionID,
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

	// Save token with whitespace
	if err := store.SaveSessionToken(sessionID, token); err != nil {
		t.Fatalf("failed to save session token: %v", err)
	}

	// Validate with trimmed token (hash should match due to trimming in hashSecret)
	valid, err := store.ValidateSessionToken(sessionID, trimmedToken)
	if err != nil {
		t.Fatalf("failed to validate trimmed token: %v", err)
	}
	if !valid {
		t.Errorf("expected trimmed token to be valid (whitespace should be trimmed)")
	}

	// Validate with whitespace token
	valid, err = store.ValidateSessionToken(sessionID, token)
	if err != nil {
		t.Fatalf("failed to validate token with whitespace: %v", err)
	}
	if !valid {
		t.Errorf("expected token with whitespace to be valid (whitespace should be trimmed)")
	}
}
