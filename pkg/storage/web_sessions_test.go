package storage

import (
	"path/filepath"
	"testing"
	"time"
)

func TestWebSessionsLifecycle(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	sessionID := "web-sess-123"
	principal := "user@example.com"
	scope := "read:write"
	tokenID := "token-456"
	expires := time.Now().Add(1 * time.Hour)

	// Create auth session
	if err := store.CreateAuthSession(sessionID, principal, scope, tokenID, expires); err != nil {
		t.Fatalf("failed to create auth session: %v", err)
	}

	// Get auth session
	sess, err := store.GetAuthSession(sessionID)
	if err != nil {
		t.Fatalf("failed to get auth session: %v", err)
	}
	if sess == nil {
		t.Fatalf("expected session to exist")
	}
	if sess.ID != sessionID {
		t.Errorf("expected ID=%q, got %q", sessionID, sess.ID)
	}
	if sess.Principal != principal {
		t.Errorf("expected Principal=%q, got %q", principal, sess.Principal)
	}
	if sess.Scope != scope {
		t.Errorf("expected Scope=%q, got %q", scope, sess.Scope)
	}
	if sess.TokenID != tokenID {
		t.Errorf("expected TokenID=%q, got %q", tokenID, sess.TokenID)
	}

	// Touch session
	time.Sleep(10 * time.Millisecond) // Ensure timestamp changes
	if err := store.TouchAuthSession(sessionID); err != nil {
		t.Fatalf("failed to touch auth session: %v", err)
	}

	// Count active sessions
	count, err := store.CountActiveAuthSessions(time.Now())
	if err != nil {
		t.Fatalf("failed to count active sessions: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 active session, got %d", count)
	}

	// Delete session
	if err := store.DeleteAuthSession(sessionID); err != nil {
		t.Fatalf("failed to delete auth session: %v", err)
	}

	// Session should no longer exist
	sess, err = store.GetAuthSession(sessionID)
	if err != nil {
		t.Fatalf("failed to get deleted session: %v", err)
	}
	if sess != nil {
		t.Errorf("expected session to be deleted, got %+v", sess)
	}
}

func TestGetAuthSessionNonexistent(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	sess, err := store.GetAuthSession("nonexistent")
	if err != nil {
		t.Fatalf("unexpected error for nonexistent session: %v", err)
	}
	if sess != nil {
		t.Errorf("expected nil session for nonexistent ID, got %+v", sess)
	}
}

func TestCleanupExpiredAuthSessions(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now()

	// Create expired session
	expiredID := "expired-sess"
	if err := store.CreateAuthSession(expiredID, "user1", "read", "token1", now.Add(-1*time.Hour)); err != nil {
		t.Fatalf("failed to create expired session: %v", err)
	}

	// Create active session
	activeID := "active-sess"
	if err := store.CreateAuthSession(activeID, "user2", "write", "token2", now.Add(1*time.Hour)); err != nil {
		t.Fatalf("failed to create active session: %v", err)
	}

	// Cleanup expired sessions
	deleted, err := store.CleanupExpiredAuthSessions(now)
	if err != nil {
		t.Fatalf("failed to cleanup expired sessions: %v", err)
	}
	if deleted != 1 {
		t.Errorf("expected 1 deleted session, got %d", deleted)
	}

	// Verify expired session is gone
	sess, err := store.GetAuthSession(expiredID)
	if err != nil {
		t.Fatalf("failed to get expired session: %v", err)
	}
	if sess != nil {
		t.Errorf("expected expired session to be deleted, got %+v", sess)
	}

	// Verify active session still exists
	sess, err = store.GetAuthSession(activeID)
	if err != nil {
		t.Fatalf("failed to get active session: %v", err)
	}
	if sess == nil {
		t.Errorf("expected active session to still exist")
	}

	// Count active sessions
	count, err := store.CountActiveAuthSessions(now)
	if err != nil {
		t.Fatalf("failed to count active sessions: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 active session, got %d", count)
	}
}

func TestCountActiveAuthSessions(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now()

	// No sessions initially
	count, err := store.CountActiveAuthSessions(now)
	if err != nil {
		t.Fatalf("failed to count active sessions: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 active sessions, got %d", count)
	}

	// Create multiple sessions with different expiry times
	for i := 0; i < 3; i++ {
		id := "active-" + string(rune('0'+i))
		if err := store.CreateAuthSession(id, "user", "read", "token", now.Add(1*time.Hour)); err != nil {
			t.Fatalf("failed to create session %d: %v", i, err)
		}
	}

	for i := 0; i < 2; i++ {
		id := "expired-" + string(rune('0'+i))
		if err := store.CreateAuthSession(id, "user", "read", "token", now.Add(-1*time.Hour)); err != nil {
			t.Fatalf("failed to create expired session %d: %v", i, err)
		}
	}

	// Count should only include active sessions
	count, err = store.CountActiveAuthSessions(now)
	if err != nil {
		t.Fatalf("failed to count active sessions: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 active sessions, got %d", count)
	}
}

func TestWebSessionsWhitespace(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	sessionID := "sess-123"
	principal := "  user@example.com  "
	scope := "  read:write  "
	tokenID := "  token-456  "
	expires := time.Now().Add(1 * time.Hour)

	// Create session with whitespace
	if err := store.CreateAuthSession(sessionID, principal, scope, tokenID, expires); err != nil {
		t.Fatalf("failed to create auth session: %v", err)
	}

	// Get session and verify whitespace was trimmed
	sess, err := store.GetAuthSession(sessionID)
	if err != nil {
		t.Fatalf("failed to get auth session: %v", err)
	}
	if sess.Principal != "user@example.com" {
		t.Errorf("expected trimmed principal, got %q", sess.Principal)
	}
	if sess.Scope != "read:write" {
		t.Errorf("expected trimmed scope, got %q", sess.Scope)
	}
	if sess.TokenID != "token-456" {
		t.Errorf("expected trimmed tokenID, got %q", sess.TokenID)
	}
}

func TestWebSessionsClosedStore(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	_ = store.Close()

	expires := time.Now().Add(1 * time.Hour)

	// Test operations on closed store - should return an error
	if err := store.CreateAuthSession("id", "principal", "scope", "token", expires); err == nil {
		t.Errorf("expected error for closed store, got nil")
	}

	if err := store.TouchAuthSession("id"); err == nil {
		t.Errorf("expected error for closed store, got nil")
	}

	_, err = store.GetAuthSession("id")
	if err == nil {
		t.Errorf("expected error for closed store, got nil")
	}

	if err := store.DeleteAuthSession("id"); err == nil {
		t.Errorf("expected error for closed store, got nil")
	}

	_, err = store.CleanupExpiredAuthSessions(time.Now())
	if err == nil {
		t.Errorf("expected error for closed store, got nil")
	}

	_, err = store.CountActiveAuthSessions(time.Now())
	if err == nil {
		t.Errorf("expected error for closed store, got nil")
	}
}
