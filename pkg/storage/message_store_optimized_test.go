package storage

import (
	"path/filepath"
	"testing"
	"time"
)

func TestCursorPagination(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to init store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	// Create a session
	session := &Session{
		ID:         "cursor-test-session",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Create 10 messages with distinct timestamps
	for i := 0; i < 10; i++ {
		msg := &Message{
			SessionID: session.ID,
			Role:      "user",
			Content:   "message " + string(rune('0'+i)),
			Timestamp: time.Now().Add(time.Duration(i) * time.Second),
			Tokens:    i + 1,
		}
		if err := store.SaveMessage(msg); err != nil {
			t.Fatalf("save message %d: %v", i, err)
		}
	}

	// Test cursor pagination with limit of 3
	page1, cursor1, err := store.GetMessagesWithCursor(session.ID, nil, 3)
	if err != nil {
		t.Fatalf("get first page: %v", err)
	}
	if len(page1) != 3 {
		t.Errorf("expected 3 messages in first page, got %d", len(page1))
	}
	if cursor1 == nil {
		t.Fatal("expected cursor for next page")
	}
	if page1[0].Content != "message 0" {
		t.Errorf("expected first message to be 'message 0', got %q", page1[0].Content)
	}

	// Get second page
	page2, cursor2, err := store.GetMessagesWithCursor(session.ID, cursor1, 3)
	if err != nil {
		t.Fatalf("get second page: %v", err)
	}
	if len(page2) != 3 {
		t.Errorf("expected 3 messages in second page, got %d", len(page2))
	}
	if cursor2 == nil {
		t.Fatal("expected cursor for third page")
	}
	if page2[0].Content != "message 3" {
		t.Errorf("expected first message of page 2 to be 'message 3', got %q", page2[0].Content)
	}

	// Get third page (should have 3 messages)
	page3, cursor3, err := store.GetMessagesWithCursor(session.ID, cursor2, 3)
	if err != nil {
		t.Fatalf("get third page: %v", err)
	}
	if len(page3) != 3 {
		t.Errorf("expected 3 messages in third page, got %d", len(page3))
	}
	if cursor3 == nil {
		t.Fatal("expected cursor for fourth page")
	}

	// Get fourth page (should have 1 message, no cursor)
	page4, cursor4, err := store.GetMessagesWithCursor(session.ID, cursor3, 3)
	if err != nil {
		t.Fatalf("get fourth page: %v", err)
	}
	if len(page4) != 1 {
		t.Errorf("expected 1 message in fourth page, got %d", len(page4))
	}
	if cursor4 != nil {
		t.Error("expected no cursor after last page")
	}

	// Verify total messages
	allMessages, err := store.GetAllMessages(session.ID)
	if err != nil {
		t.Fatalf("get all messages: %v", err)
	}
	if len(allMessages) != 10 {
		t.Errorf("expected 10 total messages, got %d", len(allMessages))
	}
}

func TestCursorEncodeDecode(t *testing.T) {
	original := &Cursor{
		ID:        12345,
		Timestamp: time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
	}

	encoded := EncodeCursor(original)
	if encoded == "" {
		t.Fatal("expected non-empty encoded cursor")
	}

	decoded, err := DecodeCursor(encoded)
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}

	if decoded.ID != original.ID {
		t.Errorf("expected ID %d, got %d", original.ID, decoded.ID)
	}
	if !decoded.Timestamp.Equal(original.Timestamp) {
		t.Errorf("expected timestamp %v, got %v", original.Timestamp, decoded.Timestamp)
	}

	// Test empty cursor
	emptyEncoded := EncodeCursor(nil)
	if emptyEncoded != "" {
		t.Errorf("expected empty string for nil cursor, got %q", emptyEncoded)
	}

	// Test decoding empty string
	emptyDecoded, err := DecodeCursor("")
	if err != nil {
		t.Fatalf("decode empty cursor: %v", err)
	}
	if emptyDecoded != nil {
		t.Errorf("expected nil for empty cursor, got %v", emptyDecoded)
	}

	// Test decoding invalid string
	_, err = DecodeCursor("invalid-base64!!!")
	if err == nil {
		t.Error("expected error decoding invalid cursor")
	}
}

func TestGetMessagesWithSessions(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to init store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	// Create 3 sessions
	sessions := []*Session{
		{ID: "session-1", CreatedAt: time.Now(), LastActive: time.Now(), Status: SessionStatusActive},
		{ID: "session-2", CreatedAt: time.Now(), LastActive: time.Now(), Status: SessionStatusActive},
		{ID: "session-3", CreatedAt: time.Now(), LastActive: time.Now(), Status: SessionStatusActive},
	}
	for _, s := range sessions {
		if err := store.CreateSession(s); err != nil {
			t.Fatalf("create session %s: %v", s.ID, err)
		}
	}

	// Add messages to sessions 1 and 2, leave session 3 empty
	for i := 0; i < 3; i++ {
		msg := &Message{
			SessionID: "session-1",
			Role:      "user",
			Content:   "s1-msg-" + string(rune('0'+i)),
			Timestamp: time.Now().Add(time.Duration(i) * time.Second),
			Tokens:    i + 1,
		}
		if err := store.SaveMessage(msg); err != nil {
			t.Fatalf("save message: %v", err)
		}
	}

	for i := 0; i < 2; i++ {
		msg := &Message{
			SessionID: "session-2",
			Role:      "assistant",
			Content:   "s2-msg-" + string(rune('0'+i)),
			Timestamp: time.Now().Add(time.Duration(i) * time.Second),
			Tokens:    i + 10,
		}
		if err := store.SaveMessage(msg); err != nil {
			t.Fatalf("save message: %v", err)
		}
	}

	// Test batch loading
	result, err := store.GetMessagesWithSessions([]string{"session-1", "session-2", "session-3"})
	if err != nil {
		t.Fatalf("get messages with sessions: %v", err)
	}

	if len(result) != 3 {
		t.Errorf("expected 3 sessions in result, got %d", len(result))
	}

	if len(result["session-1"]) != 3 {
		t.Errorf("expected 3 messages for session-1, got %d", len(result["session-1"]))
	}
	if len(result["session-2"]) != 2 {
		t.Errorf("expected 2 messages for session-2, got %d", len(result["session-2"]))
	}
	if len(result["session-3"]) != 0 {
		t.Errorf("expected 0 messages for session-3, got %d", len(result["session-3"]))
	}

	// Verify ordering (should be by timestamp ASC)
	if result["session-1"][0].Content != "s1-msg-0" {
		t.Errorf("expected first message to be 's1-msg-0', got %q", result["session-1"][0].Content)
	}

	// Test empty session IDs
	emptyResult, err := store.GetMessagesWithSessions([]string{})
	if err != nil {
		t.Fatalf("get messages with empty sessions: %v", err)
	}
	if len(emptyResult) != 0 {
		t.Errorf("expected empty result, got %d sessions", len(emptyResult))
	}

	// Test non-existent session
	singleResult, err := store.GetMessagesWithSessions([]string{"non-existent"})
	if err != nil {
		t.Fatalf("get messages for non-existent session: %v", err)
	}
	if len(singleResult["non-existent"]) != 0 {
		t.Errorf("expected empty result for non-existent session, got %d messages", len(singleResult["non-existent"]))
	}
}

func TestGetSessionStats(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to init store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	// Create a session
	session := &Session{
		ID:         "stats-test-session",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Test stats for empty session
	stats, err := store.GetSessionStats(session.ID)
	if err != nil {
		t.Fatalf("get stats for empty session: %v", err)
	}
	if stats.MessageCount != 0 {
		t.Errorf("expected 0 messages, got %d", stats.MessageCount)
	}
	if stats.TotalTokens != 0 {
		t.Errorf("expected 0 tokens, got %d", stats.TotalTokens)
	}

	// Add messages of different roles
	baseTime := time.Now()
	messages := []struct {
		role   string
		tokens int
		delay  time.Duration
	}{
		{"user", 10, 0},
		{"assistant", 25, 1 * time.Second},
		{"user", 15, 2 * time.Second},
		{"assistant", 30, 3 * time.Second},
		{"system", 5, 4 * time.Second},
	}

	for _, m := range messages {
		msg := &Message{
			SessionID: session.ID,
			Role:      m.role,
			Content:   "test",
			Timestamp: baseTime.Add(m.delay),
			Tokens:    m.tokens,
		}
		if err := store.SaveMessage(msg); err != nil {
			t.Fatalf("save message: %v", err)
		}
	}

	// Get stats
	stats, err = store.GetSessionStats(session.ID)
	if err != nil {
		t.Fatalf("get session stats: %v", err)
	}

	if stats.SessionID != session.ID {
		t.Errorf("expected session ID %s, got %s", session.ID, stats.SessionID)
	}
	if stats.MessageCount != 5 {
		t.Errorf("expected 5 messages, got %d", stats.MessageCount)
	}
	expectedTokens := 10 + 25 + 15 + 30 + 5
	if stats.TotalTokens != expectedTokens {
		t.Errorf("expected %d tokens, got %d", expectedTokens, stats.TotalTokens)
	}

	// Check role counts
	if stats.RoleCounts["user"] != 2 {
		t.Errorf("expected 2 user messages, got %d", stats.RoleCounts["user"])
	}
	if stats.RoleCounts["assistant"] != 2 {
		t.Errorf("expected 2 assistant messages, got %d", stats.RoleCounts["assistant"])
	}
	if stats.RoleCounts["system"] != 1 {
		t.Errorf("expected 1 system message, got %d", stats.RoleCounts["system"])
	}

	// Check timestamps
	if stats.FirstMessage.IsZero() {
		t.Error("expected non-zero first message time")
	}
	if stats.LastMessage.IsZero() {
		t.Error("expected non-zero last message time")
	}
	if !stats.FirstMessage.Equal(baseTime) {
		t.Errorf("expected first message at %v, got %v", baseTime, stats.FirstMessage)
	}
}

func TestStatementCache(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to init store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	// Test that getStmt returns a statement
	query := "SELECT 1"
	stmt1, err := store.getStmt(query)
	if err != nil {
		t.Fatalf("get stmt first time: %v", err)
	}
	if stmt1 == nil {
		t.Fatal("expected non-nil statement")
	}

	// Test that getStmt returns the same cached statement
	stmt2, err := store.getStmt(query)
	if err != nil {
		t.Fatalf("get stmt second time: %v", err)
	}
	if stmt1 != stmt2 {
		t.Error("expected same cached statement")
	}

	// Test clearStmtCache
	store.clearStmtCache()

	// After clearing, should get a new statement
	stmt3, err := store.getStmt(query)
	if err != nil {
		t.Fatalf("get stmt after clear: %v", err)
	}
	if stmt3 == stmt1 {
		t.Error("expected different statement after cache clear")
	}

	// Test with different queries
	query2 := "SELECT 2"
	stmt4, err := store.getStmt(query2)
	if err != nil {
		t.Fatalf("get stmt for different query: %v", err)
	}
	if stmt4 == stmt1 {
		t.Error("expected different statements for different queries")
	}
}

func TestGetMessagesWithCursorLimits(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to init store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	session := &Session{
		ID:         "limit-test",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Add a few messages
	for i := 0; i < 5; i++ {
		msg := &Message{
			SessionID: session.ID,
			Role:      "user",
			Content:   "msg",
			Timestamp: time.Now().Add(time.Duration(i) * time.Second),
			Tokens:    1,
		}
		if err := store.SaveMessage(msg); err != nil {
			t.Fatalf("save message: %v", err)
		}
	}

	// Test with zero limit (should default to 100)
	msgs, cursor, err := store.GetMessagesWithCursor(session.ID, nil, 0)
	if err != nil {
		t.Fatalf("get messages with zero limit: %v", err)
	}
	if len(msgs) != 5 {
		t.Errorf("expected 5 messages, got %d", len(msgs))
	}
	if cursor != nil {
		t.Error("expected no cursor when all messages fit")
	}

	// Test with very large limit (should cap at 1000)
	// We can't easily test the cap without creating 1001 messages,
	// but we can verify the function doesn't error
	msgs, _, err = store.GetMessagesWithCursor(session.ID, nil, 10000)
	if err != nil {
		t.Fatalf("get messages with large limit: %v", err)
	}
	if len(msgs) != 5 {
		t.Errorf("expected 5 messages, got %d", len(msgs))
	}
}
