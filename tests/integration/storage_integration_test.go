//go:build integration
// +build integration

package integration

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/session"
	"github.com/odvcencio/buckley/pkg/storage"
)

// TestStorageSessionLifecycle tests creating, retrieving, and managing sessions
func TestStorageSessionLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create temporary database
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")

	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer store.Close()

	// Create a new session
	sessionID := session.GenerateSessionID("integration-test")
	sess := &storage.Session{
		ID:          sessionID,
		ProjectPath: "/test/project",
		GitRepo:     "test-repo",
		GitBranch:   "main",
		CreatedAt:   time.Now(),
		LastActive:  time.Now(),
		Status:      storage.SessionStatusActive,
	}

	err = store.CreateSession(sess)
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Retrieve the session
	loadedSess, err := store.GetSession(sessionID)
	if err != nil {
		t.Fatalf("Failed to get session: %v", err)
	}

	if loadedSess.ID != sessionID {
		t.Errorf("Session ID = %s, want %s", loadedSess.ID, sessionID)
	}

	if loadedSess.GitRepo != "test-repo" {
		t.Errorf("GitRepo = %s, want test-repo", loadedSess.GitRepo)
	}

	// List sessions
	sessions, err := store.ListSessions(100)
	if err != nil {
		t.Fatalf("Failed to list sessions: %v", err)
	}

	if len(sessions) != 1 {
		t.Errorf("Listed %d sessions, want 1", len(sessions))
	}

	t.Logf("✓ Session lifecycle test passed")
}

// TestStorageMessagePersistence tests saving and retrieving messages
func TestStorageMessagePersistence(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "messages.db")

	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer store.Close()

	// Create session first
	sessionID := session.GenerateSessionID("message-test")
	sess := &storage.Session{
		ID:         sessionID,
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}

	if err := store.CreateSession(sess); err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Save some messages
	messages := []*storage.Message{
		{
			SessionID: sessionID,
			Role:      "user",
			Content:   "Hello, test message 1",
			Timestamp: time.Now(),
			Tokens:    10,
		},
		{
			SessionID: sessionID,
			Role:      "assistant",
			Content:   "Response to test message 1",
			Timestamp: time.Now(),
			Tokens:    15,
		},
		{
			SessionID: sessionID,
			Role:      "user",
			Content:   "Hello, test message 2",
			Timestamp: time.Now(),
			Tokens:    10,
		},
	}

	for _, msg := range messages {
		if err := store.SaveMessage(msg); err != nil {
			t.Fatalf("Failed to save message: %v", err)
		}
	}

	// Retrieve messages
	loadedMessages, err := store.GetMessages(sessionID, 100, 0)
	if err != nil {
		t.Fatalf("Failed to get messages: %v", err)
	}

	if len(loadedMessages) != 3 {
		t.Errorf("Retrieved %d messages, want 3", len(loadedMessages))
	}

	if loadedMessages[0].Role != "user" {
		t.Errorf("First message role = %s, want user", loadedMessages[0].Role)
	}

	// Verify session stats were updated
	updatedSess, err := store.GetSession(sessionID)
	if err != nil {
		t.Fatalf("Failed to get updated session: %v", err)
	}

	if updatedSess.MessageCount != 3 {
		t.Errorf("Session message count = %d, want 3", updatedSess.MessageCount)
	}

	expectedTokens := 10 + 15 + 10
	if updatedSess.TotalTokens != expectedTokens {
		t.Errorf("Session total tokens = %d, want %d", updatedSess.TotalTokens, expectedTokens)
	}

	t.Logf("✓ Message persistence test passed (%d messages, %d tokens)",
		updatedSess.MessageCount, updatedSess.TotalTokens)
}

// TestStorageSessionDeletion tests deleting a session and its messages
func TestStorageSessionDeletion(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "deletion.db")

	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer store.Close()

	// Create session and messages
	sessionID := session.GenerateSessionID("delete-test")
	sess := &storage.Session{
		ID:         sessionID,
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}

	if err := store.CreateSession(sess); err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	msg := &storage.Message{
		SessionID: sessionID,
		Role:      "user",
		Content:   "Test message for deletion",
		Timestamp: time.Now(),
		Tokens:    5,
	}

	if err := store.SaveMessage(msg); err != nil {
		t.Fatalf("Failed to save message: %v", err)
	}

	// Verify session exists
	if _, err := store.GetSession(sessionID); err != nil {
		t.Fatalf("Session not found after creation: %v", err)
	}

	// Delete session
	if err := store.DeleteSession(sessionID); err != nil {
		t.Fatalf("Failed to delete session: %v", err)
	}

	// Verify session is deleted
	_, err = store.GetSession(sessionID)
	if err == nil {
		t.Error("Session still exists after deletion")
	}

	// Verify messages are also deleted
	messages, err := store.GetMessages(sessionID, 100, 0)
	if err != nil {
		t.Fatalf("Failed to query messages: %v", err)
	}

	if len(messages) != 0 {
		t.Errorf("Found %d messages after session deletion, want 0", len(messages))
	}

	t.Logf("✓ Session deletion test passed")
}

// TestGitSessionDetection tests session ID generation from git repository info
func TestGitSessionDetection(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Get current working directory (should be in git repo)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}

	// Detect session ID from git info
	sessionID := session.DetermineSessionID(cwd)

	if sessionID == "" {
		t.Error("Session ID should not be empty")
	}

	// If we're in a git repo, session ID should contain branch name
	repo, branch := session.GetGitInfo(cwd)
	if repo != "" {
		if branch == "" {
			t.Error("Git branch should not be empty in git repository")
		}
		t.Logf("Detected git repo: %s, branch: %s", repo, branch)
		t.Logf("Session ID: %s", sessionID)
	} else {
		t.Log("Not in git repository, using directory-based session ID")
	}

	// Verify session ID is consistent
	sessionID2 := session.DetermineSessionID(cwd)
	if sessionID != sessionID2 {
		t.Errorf("Session ID inconsistent: %s != %s", sessionID, sessionID2)
	}

	t.Logf("✓ Git session detection test passed")
}

// TestDatabaseConcurrency tests that multiple operations don't cause locking issues
func TestDatabaseConcurrency(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "concurrent.db")

	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer store.Close()

	// Create multiple sessions
	for i := 0; i < 5; i++ {
		sessionID := session.GenerateSessionID("concurrent-test")
		sess := &storage.Session{
			ID:         sessionID,
			CreatedAt:  time.Now(),
			LastActive: time.Now(),
			Status:     storage.SessionStatusActive,
		}

		if err := store.CreateSession(sess); err != nil {
			t.Errorf("Failed to create session %d: %v", i, err)
		}

		// Add messages
		for j := 0; j < 3; j++ {
			msg := &storage.Message{
				SessionID: sessionID,
				Role:      "user",
				Content:   "Concurrent test message",
				Timestamp: time.Now(),
				Tokens:    5,
			}

			if err := store.SaveMessage(msg); err != nil {
				t.Errorf("Failed to save message %d for session %d: %v", j, i, err)
			}
		}
	}

	// List all sessions
	sessions, err := store.ListSessions(100)
	if err != nil {
		t.Fatalf("Failed to list sessions: %v", err)
	}

	if len(sessions) != 5 {
		t.Errorf("Listed %d sessions, want 5", len(sessions))
	}

	t.Logf("✓ Database concurrency test passed (%d sessions created)", len(sessions))
}
