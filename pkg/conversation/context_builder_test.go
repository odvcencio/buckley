package conversation

import (
	"context"
	"testing"
)

type stubTokenCounter struct{}

func (stubTokenCounter) Count(text string) int {
	return len(text)
}

type stubCompactor struct {
	should    bool
	called    int
	lastMode  string
	lastUsage float64
}

func (s *stubCompactor) ShouldAutoCompact(mode string, usageRatio float64) bool {
	s.lastMode = mode
	s.lastUsage = usageRatio
	return s.should
}

func (s *stubCompactor) CompactAsync(_ context.Context) {
	s.called++
}

func TestContextBuilder_BuildMessages_TrimsToBudget(t *testing.T) {
	conv := &Conversation{
		Messages: []Message{
			{Role: "user", Content: "one", Tokens: 10},
			{Role: "assistant", Content: "two", Tokens: 10},
			{Role: "user", Content: "three", Tokens: 10},
			{Role: "assistant", Content: "four", Tokens: 10},
		},
	}

	builder := &ContextBuilder{
		tokenCounter: stubTokenCounter{},
	}

	trimmed := builder.BuildMessages(conv, 28, "classic")
	if len(trimmed) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(trimmed))
	}
	if trimmed[0].Content != "three" || trimmed[1].Content != "four" {
		t.Fatalf("unexpected trimmed messages: %#v", trimmed)
	}
}

func TestContextBuilder_BuildMessages_TriggersCompaction(t *testing.T) {
	conv := &Conversation{
		Messages: []Message{
			{Role: "user", Content: "hello", Tokens: 10},
		},
	}
	compactor := &stubCompactor{should: true}
	builder := &ContextBuilder{
		tokenCounter: stubTokenCounter{},
		compactor:    compactor,
	}

	builder.BuildMessages(conv, 5, "classic")
	if compactor.called != 1 {
		t.Fatalf("expected compaction to be triggered once, got %d", compactor.called)
	}
	if compactor.lastMode != "classic" {
		t.Fatalf("expected mode classic, got %s", compactor.lastMode)
	}
	if compactor.lastUsage <= 0 {
		t.Fatalf("expected positive usage ratio, got %.2f", compactor.lastUsage)
	}
}
