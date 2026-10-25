package conversation

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/storage"
)

func newWorkingReferenceStore(t *testing.T) *storage.Store {
	t.Helper()
	s, err := storage.New(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.CreateSession(&storage.Session{ID: "reference", Status: storage.SessionStatusActive, CreatedAt: time.Now(), LastActive: time.Now()}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestWorkingSetReference_PreservesProviderWireAndBookkeeping(t *testing.T) {
	store := newWorkingReferenceStore(t)
	sig := "opaque signature"
	now := time.Now().UTC()
	c := New("reference")
	c.Messages = []Message{
		{Role: "user", Content: []model.ContentPart{{Type: "text", Text: "Inspect 日本語 → ✓"}, {Type: "image_url", ImageURL: &model.ImageURL{URL: "data:image/png;base64,AAAA", Detail: "low"}}}, Timestamp: now, Tokens: 10},
		{Role: "assistant", Content: "Checking evidence", Reasoning: "I’ll inspect safely", ReasoningDetails: []model.ReasoningDetail{{Type: "reasoning.encrypted", ID: "r1", Data: "opaque", Signature: &sig, Extra: map[string]json.RawMessage{"vendor": json.RawMessage(`{"keep":true}`)}}}, ToolCalls: []model.ToolCall{{ID: "completed", Type: "function", Function: model.FunctionCall{Name: "read_file", Arguments: `{"path":"a.go"}`}}}, Timestamp: now, Tokens: 12},
		{Role: "tool", Name: "read_file", ToolCallID: "completed", Content: "Completed evidence ✓", Timestamp: now, Tokens: 5},
		{Role: "assistant", Content: "A partial draft survives", IsTruncated: true, Timestamp: now, Tokens: 6},
	}
	c.UpdateTokenCount()
	c.CompactionCount = 7
	if err := c.SaveAllMessages(store); err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(c.ToModelMessages())
	if err != nil {
		t.Fatal(err)
	}
	tokens := c.TokenCount
	canonical, err := store.GetAllMessages(c.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := c.ReleaseWorkingSet(store)
	if err != nil || ref == nil || len(c.Messages) != 0 || c.WorkingSetBytes() != 0 {
		t.Fatalf("release failed: reference=%v err=%v", ref, err)
	}
	after, err := store.GetAllMessages(c.SessionID)
	if err != nil || !reflect.DeepEqual(canonical, after) {
		t.Fatal("release changed canonical transcript or receipts")
	}
	if err := c.RestoreWorkingSet(store, ref); err != nil {
		t.Fatal(err)
	}
	afterWire, err := json.Marshal(c.ToModelMessages())
	if err != nil || !bytes.Equal(before, afterWire) || c.TokenCount != tokens || c.CompactionCount != 7 {
		t.Fatal("restore changed provider-visible continuation prefix or bookkeeping")
	}
}

func TestWorkingSetReference_PreservesPartialWindowAndNotice(t *testing.T) {
	store := newWorkingReferenceStore(t)
	c := New("reference")
	c.AddUserMessage("Original task")
	for range 60 {
		c.AddAssistantMessage(strings.Repeat("stored evidence ", 80))
	}
	if err := c.SaveAllMessages(store); err != nil {
		t.Fatal(err)
	}
	if err := c.LoadWorkingSet(store, 8<<10); err != nil {
		t.Fatal(err)
	}
	before, tokens, prefix := c.ToModelMessages(), c.TokenCount, c.historyBeforeID
	if prefix == 0 {
		t.Fatal("fixture did not create a partial working window")
	}
	ref, err := c.ReleaseWorkingSet(store)
	if err != nil || ref == nil {
		t.Fatalf("release: %v", err)
	}
	if err := c.RestoreWorkingSet(store, ref); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, c.ToModelMessages()) || c.TokenCount != tokens || c.historyBeforeID != prefix {
		t.Fatal("partial window order, notice, tokens or protected prefix changed")
	}
}

func TestWorkingSetReference_PinsUnsavedAndRejectsChangedStorage(t *testing.T) {
	store := newWorkingReferenceStore(t)
	c := New("reference")
	c.AddUserMessage("Original task ✓")
	if ref, err := c.ReleaseWorkingSet(store); err != nil || ref != nil || len(c.Messages) != 1 {
		t.Fatal("unsaved input was released")
	}
	if err := c.SaveAllMessages(store); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec("UPDATE messages SET content = 'changed' WHERE session_id = 'reference'"); err != nil {
		t.Fatal(err)
	}
	if ref, err := c.ReleaseWorkingSet(store); err == nil || ref != nil || len(c.Messages) != 1 {
		t.Fatal("release discarded a view storage could not reproduce")
	}
	if _, err := store.DB().Exec("UPDATE messages SET content = ? WHERE session_id = 'reference'", "Original task ✓"); err != nil {
		t.Fatal(err)
	}
	ref, err := c.ReleaseWorkingSet(store)
	if err != nil || ref == nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := store.DB().Exec("UPDATE messages SET content = 'changed' WHERE session_id = 'reference'"); err != nil {
		t.Fatal(err)
	}
	if err := c.RestoreWorkingSet(store, ref); !errors.Is(err, ErrWorkingSetChanged) || len(c.Messages) != 0 {
		t.Fatalf("changed storage was accepted: %v", err)
	}
	if _, err := store.DB().Exec("UPDATE messages SET content = ? WHERE session_id = 'reference'", "Original task ✓"); err != nil {
		t.Fatal(err)
	}
	if err := c.RestoreWorkingSet(store, ref); err != nil || GetContentAsString(c.Messages[0].Content) != "Original task ✓" {
		t.Fatalf("failed restore was not retryable: %v", err)
	}
	if err := c.RestoreWorkingSet(store, ref); err == nil {
		t.Fatal("stale reference overwrote an already resident view")
	}
}
