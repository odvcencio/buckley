package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestGetWorkingSetMessages_OrderScopeAndAdmission(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "references.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, sid := range []string{"owned", "other"} {
		if err := store.CreateSession(&Session{ID: sid, Status: SessionStatusActive, CreatedAt: time.Now(), LastActive: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	first := Message{SessionID: "owned", Role: "user", Content: "I’ll inspect → 日本語 ✓", Timestamp: time.Now()}
	second := Message{SessionID: "owned", Role: "assistant", Content: "Completed evidence", Reasoning: "retained reasoning", Timestamp: time.Now()}
	foreign := Message{SessionID: "other", Role: "user", Content: "other context", Timestamp: time.Now()}
	for _, m := range []*Message{&first, &second, &foreign} {
		if err := store.SaveMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	limit := first.WorkingSetReadBytes() + second.WorkingSetReadBytes()
	rows, err := store.GetWorkingSetMessages(context.Background(), "owned", []int64{second.ID, first.ID}, limit)
	if err != nil || len(rows) != 2 || rows[0].ID != second.ID || rows[1].Content != first.Content {
		t.Fatalf("requested order/content lost: %v, %v", rows, err)
	}
	var sqlBytes int
	if err := store.DB().QueryRow(`SELECT SUM(` + workingSetReadCostSQL + `) FROM messages WHERE session_id = 'owned'`).Scan(&sqlBytes); err != nil || sqlBytes != limit {
		t.Fatalf("byte admission differs for UTF-8: SQL=%d Go=%d err=%v", sqlBytes, limit, err)
	}
	for _, tt := range []struct {
		name  string
		ids   []int64
		limit int
	}{
		{"foreign", []int64{foreign.ID}, limit},
		{"missing", []int64{second.ID + foreign.ID + 100}, limit},
		{"duplicate", []int64{first.ID, first.ID}, limit},
		{"bytes", []int64{first.ID, second.ID}, limit - 1},
		{"count", make([]int64, 257), limit},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := store.GetWorkingSetMessages(context.Background(), "owned", tt.ids, tt.limit)
			if err == nil || got != nil {
				t.Fatal("inadmissible records were materialized")
			}
			if (tt.name == "bytes" || tt.name == "count") && !errors.Is(err, ErrSessionMemoryBudget) {
				t.Fatalf("unexpected budget error: %v", err)
			}
		})
	}
}
