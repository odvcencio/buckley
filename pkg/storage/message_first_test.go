package storage

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStore_GetFirstMessageByRole_AppendOrderAndMetadata(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "first-message.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC().Truncate(time.Second)
	for _, id := range []string{"target", "other"} {
		if err := store.CreateSession(&Session{ID: id, Model: "test", CreatedAt: now, LastActive: now}); err != nil {
			t.Fatal(err)
		}
	}
	messages := []Message{
		{SessionID: "other", Role: "user", Content: "other session", Timestamp: now},
		{SessionID: "target", Role: "system", Content: "instructions", Timestamp: now},
		{SessionID: "target", Role: "user", Content: "  original prompt\n  ", ContentJSON: `{"text":"original prompt"}`, ContentType: "text", Timestamp: now.Add(time.Hour), Tokens: 12, IsTruncated: true},
		{SessionID: "target", Role: "assistant", Content: "answer", Timestamp: now},
		{SessionID: "target", Role: "user", Content: "later prompt with an earlier clock", Timestamp: now.Add(-time.Hour)},
	}
	for i := range messages {
		if err := store.SaveMessage(&messages[i]); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		name, session, role string
		want                int
	}{
		{"first user survives clock rollback", "target", "user", 2},
		{"first assistant", "target", "assistant", 3},
		{"system role", "target", "system", 1},
		{"session scope", "other", "user", 0},
		{"missing role", "target", "tool", -1},
		{"missing session", "missing", "user", -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := store.GetFirstMessageByRole(tc.session, tc.role)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want < 0 {
				if got != nil {
					t.Fatalf("message = %+v, want nil", got)
				}
				return
			}
			want := messages[tc.want]
			if got == nil || got.ID != want.ID || got.SessionID != want.SessionID || got.Role != want.Role || got.Content != want.Content || got.ContentJSON != want.ContentJSON || got.IsTruncated != want.IsTruncated || got.Tokens != want.Tokens || !got.Timestamp.Equal(want.Timestamp) {
				t.Fatalf("message = %+v, want %+v", got, want)
			}
		})
	}
}
