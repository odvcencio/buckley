package conversation

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/storage"
)

func TestLoadWorkingSet_BoundedResumeAndPartialSavePreserveCanonicalHistory(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sid := "paused-large"
	now := time.Now()
	if err := store.CreateSession(&storage.Session{ID: sid, Status: storage.SessionStatusPaused, CreatedAt: now, LastActive: now}); err != nil {
		t.Fatal(err)
	}
	messages := []storage.Message{
		{SessionID: sid, Role: "system", Content: "Keep the system instructions", Timestamp: now},
		{SessionID: sid, Role: "user", Content: "Keep the original task", Timestamp: now},
	}
	for i := range 500 {
		messages = append(messages, storage.Message{SessionID: sid, Role: "assistant", Content: fmt.Sprintf("history-%04d %s", i, strings.Repeat("x", 4096)), Timestamp: now})
	}
	messages = append(messages, storage.Message{SessionID: sid, Role: "assistant", Content: "Latest useful summary", IsSummary: true, Timestamp: now})
	if err := store.ReplaceMessages(sid, messages); err != nil {
		t.Fatal(err)
	}
	const limit = 64 << 10
	c := New(sid)
	if err := c.LoadWorkingSet(store, limit); err != nil {
		t.Fatal(err)
	}
	if c.WorkingSetBytes() > limit || c.historyBeforeID == 0 || len(c.Messages) > 256 {
		t.Fatalf("unbounded view: bytes=%d count=%d boundary=%d", c.WorkingSetBytes(), len(c.Messages), c.historyBeforeID)
	}
	protectedPrefix := c.historyBeforeID
	for _, want := range []string{"Keep the system instructions", "Keep the original task", "Latest useful summary", workingHistoryNotice} {
		found := false
		for _, msg := range c.Messages {
			found = found || msg.Content == want
		}
		if !found {
			t.Fatalf("missing anchor %q", want)
		}
	}
	for i := range 30 {
		c.AddUserMessage(fmt.Sprintf("continued-%d", i))
		c.AddAssistantMessage(strings.Repeat("y", 4096))
		if err := c.SaveAllMessages(store); err != nil {
			t.Fatal(err)
		}
		if _, err := c.BoundWorkingSet(limit); err != nil {
			t.Fatal(err)
		}
		if c.WorkingSetBytes() > limit {
			t.Fatalf("working set grew at %d: %d", i, c.WorkingSetBytes())
		}
	}
	canonical, err := store.GetAllMessages(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(canonical) != len(messages)+60 {
		t.Fatalf("canonical count=%d want=%d", len(canonical), len(messages)+60)
	}
	for i, original := range messages {
		if canonical[i].Content != original.Content || (original.ID <= protectedPrefix && canonical[i].ID != original.ID) {
			t.Fatalf("prefix changed at %d: got ID=%d content=%.80q; want ID=%d content=%.80q (boundary=%d)", i, canonical[i].ID, canonical[i].Content, original.ID, original.Content, protectedPrefix)
		}
	}
	for _, msg := range canonical {
		if msg.Content == workingHistoryNotice {
			t.Fatal("projection marker persisted into canonical history")
		}
	}
	resumed := New(sid)
	if err := resumed.LoadWorkingSet(store, limit); err != nil {
		t.Fatal(err)
	}
	if resumed.WorkingSetBytes() > limit || GetContentAsString(resumed.Messages[len(resumed.Messages)-2].Content) != "continued-29" {
		t.Fatal("bounded resume lost latest turn")
	}
}

func TestBoundWorkingSet_PreservesCompleteToolExchanges(t *testing.T) {
	c := New("tools")
	c.AddUserMessage("original task")
	for i := range 100 {
		id := fmt.Sprintf("call-%d", i)
		c.Messages = append(c.Messages,
			Message{Role: "assistant", ToolCalls: []model.ToolCall{{ID: id, Type: "function", Function: model.FunctionCall{Name: "echo", Arguments: "{}"}}}},
			Message{Role: "tool", ToolCallID: id, Content: strings.Repeat("t", 2048)},
			Message{Role: "assistant", Content: "done"})
	}
	if _, err := c.BoundWorkingSet(16 << 10); err != nil {
		t.Fatal(err)
	}
	calls := map[string]bool{}
	for _, msg := range c.Messages {
		for _, call := range msg.ToolCalls {
			calls[call.ID] = true
		}
		if msg.Role == "tool" && !calls[msg.ToolCallID] {
			t.Fatalf("orphan result %q", msg.ToolCallID)
		}
	}
	if c.WorkingSetBytes() > 16<<10 {
		t.Fatalf("retained %d", c.WorkingSetBytes())
	}
}

func TestBoundWorkingSet_OversizedCurrentMessageReleasesMemoryAndRequiresReload(t *testing.T) {
	c := New("large")
	c.AddUserMessage("task")
	c.AddAssistantMessage(strings.Repeat("x", 1<<20))
	changed, err := c.BoundWorkingSet(64 << 10)
	if !changed || !errors.Is(err, storage.ErrSessionMemoryBudget) {
		t.Fatalf("changed=%v error=%v", changed, err)
	}
	if c.WorkingSetBytes() > 64<<10 {
		t.Fatalf("blocked view retains %d", c.WorkingSetBytes())
	}
	if _, err := c.BoundWorkingSet(64 << 10); !errors.Is(err, storage.ErrSessionMemoryBudget) {
		t.Fatalf("blocked state lost: %v", err)
	}
}

func TestBoundWorkingSet_SystemAnchorDoesNotRetainOrphanToolResult(t *testing.T) {
	c := New("interleaved")
	c.Messages = []Message{
		{Role: "user", Content: "original task", storageID: 1},
		{Role: "assistant", storageID: 2, ToolCalls: []model.ToolCall{{ID: "old-call", Function: model.FunctionCall{Name: "echo", Arguments: strings.Repeat("x", 6000)}}}},
		{Role: "system", Content: "additional instructions", storageID: 3},
		{Role: "tool", ToolCallID: "old-call", Content: strings.Repeat("y", 2048), storageID: 4},
		{Role: "assistant", Content: "recent answer", storageID: 5},
	}
	if _, err := c.BoundWorkingSet(8192); err != nil {
		t.Fatal(err)
	}
	for _, msg := range c.Messages {
		if msg.Role == "tool" {
			t.Fatal("persistent instruction kept a result whose call was trimmed")
		}
	}
	if c.historyBeforeID != 4 {
		t.Fatalf("protected prefix=%d", c.historyBeforeID)
	}
}

func TestBoundWorkingSet_OnlyLatestSystemSummaryIsPinned(t *testing.T) {
	c := New("summaries")
	c.AddSystemMessage("instructions")
	c.AddUserMessage("task")
	for i := range 100 {
		c.Messages = append(c.Messages, Message{Role: "system", IsSummary: true, Content: fmt.Sprintf("summary-%d %s", i, strings.Repeat("x", 2048))})
	}
	if _, err := c.BoundWorkingSet(8192); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(GetContentAsString(c.Messages[len(c.Messages)-1].Content), "summary-99 ") || c.WorkingSetBytes() > 8192 {
		t.Fatal("summaries exhausted the working budget")
	}
}

func TestLoadWorkingSet_RestoresCompactionCountWithoutLoadingOldSummaries(t *testing.T) {
	s, err := storage.New(filepath.Join(t.TempDir(), "summaries.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	now := time.Now()
	if err := s.CreateSession(&storage.Session{ID: "summaries", CreatedAt: now, LastActive: now}); err != nil {
		t.Fatal(err)
	}
	for i := range 100 {
		if err := s.SaveMessage(&storage.Message{SessionID: "summaries", Role: "system", IsSummary: true, Content: fmt.Sprintf("summary-%d %s", i, strings.Repeat("x", 4096)), Timestamp: now}); err != nil {
			t.Fatal(err)
		}
	}
	c := New("summaries")
	if err := c.LoadWorkingSet(s, 16<<10); err != nil {
		t.Fatal(err)
	}
	if c.CompactionCount != 100 || c.WorkingSetBytes() > 16<<10 {
		t.Fatalf("resume: compactions=%d bytes=%d", c.CompactionCount, c.WorkingSetBytes())
	}
	found := false
	for _, msg := range c.Messages {
		found = found || strings.HasPrefix(GetContentAsString(msg.Content), "summary-99 ")
	}
	if !found {
		t.Fatal("resume did not retain the latest summary")
	}
}
