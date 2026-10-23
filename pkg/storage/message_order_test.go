package storage

import (
	"context"
	"encoding/base64"
	"path/filepath"
	"testing"
	"time"
)

func rollbackMessageStore(t *testing.T) (*Store, string, []Message) {
	t.Helper()
	store, err := New(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sid := "rollback"
	base := time.Date(2026, 10, 23, 12, 0, 0, 123456789, time.UTC)
	if err := store.CreateSession(&Session{ID: sid, Status: SessionStatusPaused, CreatedAt: base, LastActive: base}); err != nil {
		t.Fatal(err)
	}
	messages := []Message{
		{SessionID: sid, Role: "system", Content: "instructions", Timestamp: base.Add(4 * time.Minute)},
		{SessionID: sid, Role: "user", Content: "task", Timestamp: base.Add(5 * time.Minute)},
		{SessionID: sid, Role: "assistant", Content: "calling tool", ToolCalls: `[{"id":"call-1","type":"function","function":{"name":"read_file","arguments":"{}"}}]`, Timestamp: base.Add(6 * time.Minute)},
		{SessionID: sid, Role: "tool", Content: "tool result", ToolCallID: "call-1", Timestamp: base.Add(2 * time.Minute)},
		{SessionID: sid, Role: "assistant", Content: "answer", Timestamp: base.Add(3 * time.Minute)},
	}
	if err := store.ReplaceMessages(sid, messages); err != nil {
		t.Fatal(err)
	}
	return store, sid, messages
}

func requireAppendOrder(t *testing.T, got, want []Message) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("count=%d want=%d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i].ID || got[i].Content != want[i].Content || !got[i].Timestamp.Equal(want[i].Timestamp) {
			t.Fatalf("at %d: got id=%d content=%q timestamp=%s; want id=%d content=%q timestamp=%s", i, got[i].ID, got[i].Content, got[i].Timestamp, want[i].ID, want[i].Content, want[i].Timestamp)
		}
	}
}

func TestMessageReaders_ClockRollbackPreservesAppendOrder(t *testing.T) {
	store, sid, want := rollbackMessageStore(t)
	for _, tc := range []struct {
		name string
		read func() ([]Message, error)
		want []Message
	}{
		{"full", func() ([]Message, error) { return store.GetAllMessages(sid) }, want},
		{"offset", func() ([]Message, error) { return store.GetMessages(sid, 3, 1) }, want[1:4]},
		{"batch", func() ([]Message, error) {
			m, err := store.GetMessagesWithSessions([]string{sid, "missing"})
			return m[sid], err
		}, want},
		{"bounded", func() ([]Message, error) {
			w, err := store.GetMessageWindow(context.Background(), sid, 64<<10)
			return w.Messages, err
		}, want},
		{"prepared", func() ([]Message, error) {
			statements, err := store.PrepareStatements()
			if err != nil {
				return nil, err
			}
			defer statements.Close()
			rows, err := statements.listMessages.Query(sid)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			var messages []Message
			for rows.Next() {
				var m Message
				if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Content, &m.Timestamp, &m.Tokens, &m.IsSummary); err != nil {
					return nil, err
				}
				messages = append(messages, m)
			}
			return messages, rows.Err()
		}, want},
		{"embedding backlog", func() ([]Message, error) { return store.GetMessagesMissingEmbeddings(context.Background(), sid, 20) }, want},
		{"embedded", func() ([]Message, error) {
			for _, m := range want {
				if err := store.SaveMessageEmbedding(context.Background(), m.ID, []byte{1}); err != nil {
					return nil, err
				}
			}
			return store.GetMessagesWithEmbeddings(context.Background(), sid)
		}, want},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.read()
			if err != nil {
				t.Fatal(err)
			}
			requireAppendOrder(t, got, tc.want)
		})
	}
}

func TestMessageCursor_ClockRollbackAndConcurrentAppend(t *testing.T) {
	store, sid, want := rollbackMessageStore(t)
	page, cursor, err := store.GetMessagesWithCursor(sid, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	requireAppendOrder(t, page, want[:2])
	if cursor == nil || cursor.Version != MessageCursorVersion {
		t.Fatalf("missing append-order continuation: %+v", cursor)
	}
	appended := Message{SessionID: sid, Role: "user", Content: "continued after rollback", Timestamp: want[0].Timestamp.Add(-time.Hour)}
	if err := store.SaveMessage(&appended); err != nil {
		t.Fatal(err)
	}
	want = append(want, appended)
	got := append([]Message(nil), page...)
	for n := 0; cursor != nil; n++ {
		if n > len(want) {
			t.Fatal("pagination did not terminate")
		}
		encoded, err := EncodeCursor(cursor)
		if err != nil {
			t.Fatal(err)
		}
		cursor, err = DecodeCursor(encoded)
		if err != nil {
			t.Fatal(err)
		}
		page, next, err := store.GetMessagesWithCursor(sid, cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, page...)
		cursor = next
	}
	requireAppendOrder(t, got, want)
}

func TestMessageCursor_LegacyTimestampContinuation(t *testing.T) {
	store, sid, messages := rollbackMessageStore(t)
	// A pre-upgrade first page emitted the oldest timestamp, which has ID4.
	legacy := &Cursor{ID: messages[3].ID, Timestamp: messages[3].Timestamp}
	encoded, err := EncodeCursor(legacy)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err = DecodeCursor(encoded)
	if err != nil {
		t.Fatal(err)
	}
	got, next, err := store.GetMessagesWithCursor(sid, legacy, 3)
	if err != nil {
		t.Fatal(err)
	}
	requireAppendOrder(t, got, []Message{messages[4], messages[0], messages[1]})
	if next == nil || next.Version != 0 {
		t.Fatalf("legacy continuation changed format: %+v", next)
	}
	got, next, err = store.GetMessagesWithCursor(sid, next, 3)
	if err != nil {
		t.Fatal(err)
	}
	requireAppendOrder(t, got, []Message{messages[2]})
	if next != nil {
		t.Fatal("legacy traversal did not finish")
	}
}

func TestMessageCursor_VersionAndTimestampMetadata(t *testing.T) {
	store, sid, messages := rollbackMessageStore(t)
	cursor := &Cursor{ID: messages[1].ID, Timestamp: messages[1].Timestamp.Add(24 * time.Hour), Version: MessageCursorVersion}
	encoded, err := EncodeCursor(cursor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := base64.URLEncoding.DecodeString(encoded); err == nil {
		t.Fatal("legacy decoder would silently reinterpret an append-order cursor")
	}
	cursor, err = DecodeCursor(encoded)
	if err != nil || cursor.Version != MessageCursorVersion {
		t.Fatalf("cursor=%+v error=%v", cursor, err)
	}
	got, _, err := store.GetMessagesWithCursor(sid, cursor, 2)
	if err != nil {
		t.Fatal(err)
	}
	requireAppendOrder(t, got, messages[2:4])
	for _, version := range []int{-1, MessageCursorVersion + 1} {
		cursor.Version = version
		if _, _, err := store.GetMessagesWithCursor(sid, cursor, 2); err == nil {
			t.Fatalf("unsupported cursor version %d accepted", version)
		}
	}
}

func TestMessageCursor_MismatchedEncodingIsRejected(t *testing.T) {
	for _, raw := range []string{
		`{"id":2,"timestamp":"2026-10-23T12:00:00Z","version":1}`,
		`{"id":2,"timestamp":"2026-10-23T12:00:00Z","version":2}`,
	} {
		if _, err := DecodeCursor(base64.URLEncoding.EncodeToString([]byte(raw))); err == nil {
			t.Fatal("versioned cursor accepted without its ordering prefix")
		}
	}
	legacy := base64.URLEncoding.EncodeToString([]byte(`{"id":2,"timestamp":"2026-10-23T12:00:00Z"}`))
	if _, err := DecodeCursor("id." + legacy); err == nil {
		t.Fatal("append-order prefix accepted a legacy payload")
	}
	if _, err := EncodeCursor(&Cursor{Version: MessageCursorVersion + 1}); err == nil {
		t.Fatal("encoded unsupported cursor version")
	}
}
