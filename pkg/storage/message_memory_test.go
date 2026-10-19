package storage

import (
	"path/filepath"
	"testing"
	"time"
)

func TestGetMessages_SmallHistoryDoesNotReserveRequestedLimit(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "messages.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateSession(&Session{ID: "small", CreatedAt: time.Now(), LastActive: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"first", "second"} {
		if err := store.SaveMessage(&Message{SessionID: "small", Role: "user", Content: content, Timestamp: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name string
		read func(string) ([]Message, error)
	}{
		{"all", store.GetAllMessages},
		{"large_limit", func(id string) ([]Message, error) { return store.GetMessages(id, 999999, 0) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages, err := tc.read("small")
			if err != nil {
				t.Fatal(err)
			}
			if len(messages) != 2 || messages[0].Content != "first" || messages[1].Content != "second" {
				t.Fatalf("history lost or reordered: %+v", messages)
			}
			if cap(messages) > 1024 {
				t.Fatalf("tiny history reserved %d message slots", cap(messages))
			}
		})
	}
}
