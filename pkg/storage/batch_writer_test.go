package storage

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveMessagesBatch(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to init store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	session := &Session{
		ID:         "batch-session",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Test empty batch
	if err := store.SaveMessagesBatch([]*Message{}); err != nil {
		t.Fatalf("empty batch should not error: %v", err)
	}

	// Test single message (should fall back to SaveMessage)
	single := &Message{
		SessionID: session.ID,
		Role:      "user",
		Content:   "single message",
		Timestamp: time.Now(),
		Tokens:    10,
	}
	if err := store.SaveMessagesBatch([]*Message{single}); err != nil {
		t.Fatalf("single message batch: %v", err)
	}
	if single.ID == 0 {
		t.Fatalf("expected single message to have ID assigned")
	}

	// Test batch insert with multiple messages
	messages := []*Message{
		{
			SessionID: session.ID,
			Role:      "user",
			Content:   "first batch message",
			Timestamp: time.Now(),
			Tokens:    5,
		},
		{
			SessionID: session.ID,
			Role:      "assistant",
			Content:   "second batch message",
			Timestamp: time.Now(),
			Tokens:    8,
		},
		{
			SessionID:   session.ID,
			Role:        "user",
			Content:     "third with json",
			ContentJSON: `{"type":"test"}`,
			ContentType: "json",
			Reasoning:   "some reasoning",
			Timestamp:   time.Now(),
			Tokens:      12,
			IsTruncated: true,
		},
	}

	if err := store.SaveMessagesBatch(messages); err != nil {
		t.Fatalf("batch insert: %v", err)
	}

	// Verify all messages have IDs assigned
	for i, msg := range messages {
		if msg.ID == 0 {
			t.Fatalf("message %d should have ID assigned", i)
		}
	}

	// Verify IDs are sequential
	for i := 1; i < len(messages); i++ {
		if messages[i].ID != messages[i-1].ID+1 {
			t.Fatalf("expected sequential IDs, got %d and %d", messages[i-1].ID, messages[i].ID)
		}
	}

	// Verify messages were saved correctly
	saved, err := store.GetAllMessages(session.ID)
	if err != nil {
		t.Fatalf("get all messages: %v", err)
	}
	if len(saved) != 4 { // single + 3 batch
		t.Fatalf("expected 4 messages, got %d", len(saved))
	}

	// Verify the batch message with JSON fields
	lastMsg := saved[len(saved)-1]
	if lastMsg.ContentJSON != `{"type":"test"}` {
		t.Fatalf("expected ContentJSON to be preserved, got %q", lastMsg.ContentJSON)
	}
	if lastMsg.ContentType != "json" {
		t.Fatalf("expected ContentType to be 'json', got %q", lastMsg.ContentType)
	}
	if lastMsg.Reasoning != "some reasoning" {
		t.Fatalf("expected Reasoning to be preserved, got %q", lastMsg.Reasoning)
	}
	if !lastMsg.IsTruncated {
		t.Fatalf("expected IsTruncated to be true")
	}
}

func TestBatchWriter(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to init store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	session := &Session{
		ID:         "writer-session",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Create batch writer with small batch size for testing
	writer := store.NewBatchWriter(3, 500*time.Millisecond)

	// Add messages one by one
	for i := 0; i < 5; i++ {
		msg := &Message{
			SessionID: session.ID,
			Role:      "user",
			Content:   "message " + string(rune('0'+i)),
			Timestamp: time.Now(),
			Tokens:    i + 1,
		}
		if err := writer.Add(msg); err != nil {
			t.Fatalf("add message %d: %v", i, err)
		}
	}

	// First 3 should have been flushed immediately (batch size = 3)
	// Remaining 2 should still be in the batch
	if writer.BatchSize() != 2 {
		t.Fatalf("expected 2 messages in batch, got %d", writer.BatchSize())
	}

	// Close should flush remaining
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	// Verify all messages were saved
	saved, err := store.GetAllMessages(session.ID)
	if err != nil {
		t.Fatalf("get all messages: %v", err)
	}
	if len(saved) != 5 {
		t.Fatalf("expected 5 messages, got %d", len(saved))
	}
}

func TestBatchWriterTimeoutFlush(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to init store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	session := &Session{
		ID:         "timeout-session",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Create batch writer with small timeout
	writer := store.NewBatchWriter(100, 50*time.Millisecond)

	// Add single message
	msg := &Message{
		SessionID: session.ID,
		Role:      "user",
		Content:   "timeout test",
		Timestamp: time.Now(),
		Tokens:    10,
	}
	if err := writer.Add(msg); err != nil {
		t.Fatalf("add message: %v", err)
	}

	// Wait for timeout flush
	time.Sleep(150 * time.Millisecond)

	// Message should have been flushed by timeout
	saved, err := store.GetAllMessages(session.ID)
	if err != nil {
		t.Fatalf("get all messages: %v", err)
	}
	if len(saved) != 1 {
		t.Fatalf("expected 1 message after timeout flush, got %d", len(saved))
	}

	writer.Close()
}

func TestBatchWriterClose(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to init store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	session := &Session{
		ID:         "close-session",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Create batch writer with large batch size
	writer := store.NewBatchWriter(100, time.Minute)

	// Add a few messages
	for i := 0; i < 3; i++ {
		msg := &Message{
			SessionID: session.ID,
			Role:      "user",
			Content:   "close test",
			Timestamp: time.Now(),
			Tokens:    5,
		}
		if err := writer.Add(msg); err != nil {
			t.Fatalf("add message: %v", err)
		}
	}

	// Messages should still be in batch (not flushed yet)
	if writer.BatchSize() != 3 {
		t.Fatalf("expected 3 messages in batch, got %d", writer.BatchSize())
	}

	// Close should flush
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	// After close, writer should reject new messages
	msg := &Message{
		SessionID: session.ID,
		Role:      "user",
		Content:   "after close",
		Timestamp: time.Now(),
		Tokens:    5,
	}
	if err := writer.Add(msg); err != ErrStoreClosed {
		t.Fatalf("expected ErrStoreClosed after close, got %v", err)
	}

	// Verify messages were saved
	saved, err := store.GetAllMessages(session.ID)
	if err != nil {
		t.Fatalf("get all messages: %v", err)
	}
	if len(saved) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(saved))
	}
}

func TestBatchWriterManualFlush(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to init store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	session := &Session{
		ID:         "flush-session",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("create session: %v", err)
	}

	writer := store.NewBatchWriter(100, time.Minute)

	// Add messages
	for i := 0; i < 3; i++ {
		msg := &Message{
			SessionID: session.ID,
			Role:      "user",
			Content:   "manual flush",
			Timestamp: time.Now(),
			Tokens:    5,
		}
		if err := writer.Add(msg); err != nil {
			t.Fatalf("add message: %v", err)
		}
	}

	// Manual flush
	if err := writer.Flush(); err != nil {
		t.Fatalf("manual flush: %v", err)
	}

	// Batch should be empty
	if writer.BatchSize() != 0 {
		t.Fatalf("expected 0 messages in batch after flush, got %d", writer.BatchSize())
	}

	// Verify messages were saved
	saved, err := store.GetAllMessages(session.ID)
	if err != nil {
		t.Fatalf("get all messages: %v", err)
	}
	if len(saved) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(saved))
	}

	writer.Close()
}

func TestReplaceMessagesBulk(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to init store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	session := &Session{
		ID:         "replace-session",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Add some initial messages
	initial := []Message{
		{SessionID: session.ID, Role: "user", Content: "old1", Timestamp: time.Now(), Tokens: 1},
		{SessionID: session.ID, Role: "assistant", Content: "old2", Timestamp: time.Now(), Tokens: 2},
	}
	if err := store.ReplaceMessages(session.ID, initial); err != nil {
		t.Fatalf("initial replace: %v", err)
	}

	// Replace with many messages to test bulk insert path
	replacement := make([]Message, 10)
	totalTokens := 0
	for i := range replacement {
		replacement[i] = Message{
			SessionID: session.ID,
			Role:      "user",
			Content:   "new message " + string(rune('0'+i)),
			Timestamp: time.Now().Add(time.Duration(i) * time.Second),
			Tokens:    i + 1,
		}
		totalTokens += i + 1
	}

	if err := store.ReplaceMessages(session.ID, replacement); err != nil {
		t.Fatalf("bulk replace: %v", err)
	}

	// Verify all messages were replaced
	saved, err := store.GetAllMessages(session.ID)
	if err != nil {
		t.Fatalf("get all messages: %v", err)
	}
	if len(saved) != 10 {
		t.Fatalf("expected 10 messages after replace, got %d", len(saved))
	}

	// Verify content is correct (should be new messages)
	if saved[0].Content != "new message 0" {
		t.Fatalf("expected first message to be 'new message 0', got %q", saved[0].Content)
	}
}

func TestReplaceMessagesSingle(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to init store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	session := &Session{
		ID:         "replace-single-session",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Replace with single message (tests single-message code path)
	replacement := []Message{
		{SessionID: session.ID, Role: "user", Content: "only one", Timestamp: time.Now(), Tokens: 5},
	}
	if err := store.ReplaceMessages(session.ID, replacement); err != nil {
		t.Fatalf("single replace: %v", err)
	}

	saved, err := store.GetAllMessages(session.ID)
	if err != nil {
		t.Fatalf("get all messages: %v", err)
	}
	if len(saved) != 1 {
		t.Fatalf("expected 1 message, got %d", len(saved))
	}
	if saved[0].Content != "only one" {
		t.Fatalf("expected 'only one', got %q", saved[0].Content)
	}
}

func TestReplaceMessagesEmpty(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to init store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	session := &Session{
		ID:         "replace-empty-session",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Add initial messages
	initial := []Message{
		{SessionID: session.ID, Role: "user", Content: "will be deleted", Timestamp: time.Now(), Tokens: 5},
	}
	if err := store.ReplaceMessages(session.ID, initial); err != nil {
		t.Fatalf("initial replace: %v", err)
	}

	// Replace with empty (should delete all)
	if err := store.ReplaceMessages(session.ID, []Message{}); err != nil {
		t.Fatalf("empty replace: %v", err)
	}

	saved, err := store.GetAllMessages(session.ID)
	if err != nil {
		t.Fatalf("get all messages: %v", err)
	}
	if len(saved) != 0 {
		t.Fatalf("expected 0 messages after empty replace, got %d", len(saved))
	}
}

func BenchmarkSaveMessagesBatch(b *testing.B) {
	tmpDir := b.TempDir()
	dbPath := filepath.Join(tmpDir, "bench.db")

	store, err := New(dbPath)
	if err != nil {
		b.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	store.OptimizeDatabase()

	session := &Session{
		ID:          "bench-batch-session",
		ProjectPath: "/test",
		CreatedAt:   time.Now(),
		LastActive:  time.Now(),
	}
	store.CreateSession(session)

	// Prepare messages
	messages := make([]*Message, 100)
	for i := range messages {
		messages[i] = &Message{
			SessionID: session.ID,
			Role:      "user",
			Content:   "benchmark message",
			Timestamp: time.Now(),
			Tokens:    10,
		}
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		// Create new session for each iteration to avoid unique constraints
		sessionID := fmt.Sprintf("bench-session-%d", i)
		s := &Session{
			ID:          sessionID,
			ProjectPath: "/test",
			CreatedAt:   time.Now(),
			LastActive:  time.Now(),
		}
		store.CreateSession(s)

		for _, m := range messages {
			m.SessionID = sessionID
		}

		if err := store.SaveMessagesBatch(messages); err != nil {
			b.Fatal(err)
		}
	}
}
