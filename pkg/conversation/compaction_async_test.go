package conversation

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
)

func TestCompactionManager_CompactAsync_Fallback(t *testing.T) {
	conv := &Conversation{
		SessionID: "s1",
		Messages: []Message{
			{Role: "user", Content: "one"},
			{Role: "assistant", Content: "two"},
			{Role: "user", Content: "three"},
			{Role: "assistant", Content: "four"},
		},
	}

	cm := NewCompactionManager(nil, config.DefaultConfig())
	cm.SetConversation(conv)

	done := make(chan *CompactionResult, 1)
	cm.SetOnComplete(func(result *CompactionResult) {
		done <- result
	})

	cm.CompactAsync(context.Background())

	select {
	case result := <-done:
		if result == nil {
			t.Fatal("expected compaction result")
		}
		if len(conv.Messages) == 0 {
			t.Fatal("expected compacted messages")
		}
		summary, ok := conv.Messages[0].Content.(string)
		if !ok {
			t.Fatalf("expected summary content to be string, got %T", conv.Messages[0].Content)
		}
		if !strings.Contains(summary, "[Earlier context summarized]") {
			t.Fatalf("expected fallback marker in summary, got %q", summary)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for compaction")
	}
}
