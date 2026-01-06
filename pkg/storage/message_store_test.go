package storage

import (
	"path/filepath"
	"testing"
	"time"
)

func TestMessageStoreCRUD(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to init store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	session := &Session{
		ID:         "session-1",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("create session: %v", err)
	}

	msg := &Message{
		SessionID: session.ID,
		Role:      "user",
		Content:   "hello world",
		Timestamp: time.Now(),
		Tokens:    12,
	}
	if err := store.SaveMessage(msg); err != nil {
		t.Fatalf("save message: %v", err)
	}
	if msg.ID == 0 {
		t.Fatalf("expected message ID to be populated")
	}

	records, err := store.GetMessages(session.ID, 10, 0)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(records) != 1 || records[0].Content != "hello world" {
		t.Fatalf("expected to read saved message, got %+v", records)
	}

	replacement := []Message{
		{
			SessionID: session.ID,
			Role:      "user",
			Content:   "first",
			Timestamp: time.Now().Add(1 * time.Minute),
			Tokens:    5,
		},
		{
			SessionID: session.ID,
			Role:      "assistant",
			Content:   "second",
			Timestamp: time.Now().Add(2 * time.Minute),
			Tokens:    8,
		},
	}
	if err := store.ReplaceMessages(session.ID, replacement); err != nil {
		t.Fatalf("replace messages: %v", err)
	}

	records, err = store.GetAllMessages(session.ID)
	if err != nil {
		t.Fatalf("get all messages: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 messages after replace, got %d", len(records))
	}
	if records[1].Content != "second" {
		t.Fatalf("expected chronological ordering, got %+v", records)
	}

	recent, err := store.GetRecentMessagesByRole("assistant", 1)
	if err != nil {
		t.Fatalf("get recent by role: %v", err)
	}
	if len(recent) != 1 || recent[0].Content != "second" {
		t.Fatalf("expected latest assistant message, got %+v", recent)
	}
}
