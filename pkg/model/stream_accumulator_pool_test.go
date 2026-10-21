package model

import (
	"strings"
	"testing"
)

func TestAcquireStreamAccumulator_Basic(t *testing.T) {
	a := AcquireStreamAccumulator()
	if a == nil {
		t.Fatal("expected non-nil accumulator")
	}

	// Should be reset
	if a.Content() != "" {
		t.Errorf("expected empty content, got %q", a.Content())
	}
	if a.Reasoning() != "" {
		t.Errorf("expected empty reasoning, got %q", a.Reasoning())
	}
	if a.HasToolCalls() {
		t.Error("expected no tool calls")
	}
	if a.Usage() != nil {
		t.Error("expected nil usage")
	}
}

func TestReleaseStreamAccumulator_Basic(t *testing.T) {
	a := AcquireStreamAccumulator()

	// Add some content
	a.Add(StreamChunk{
		Choices: []StreamChoice{
			{
				Delta: MessageDelta{
					Role:    "assistant",
					Content: "Hello world",
				},
			},
		},
	})

	if a.Content() != "Hello world" {
		t.Errorf("expected 'Hello world', got %q", a.Content())
	}

	// Release
	ReleaseStreamAccumulator(a)

	// Acquire again - should be reset
	a2 := AcquireStreamAccumulator()
	if a2.Content() != "" {
		t.Errorf("expected empty content after release, got %q", a2.Content())
	}
}

func TestReleaseStreamAccumulator_Nil(t *testing.T) {
	// Should not panic
	ReleaseStreamAccumulator(nil)
}

// Ordinary response buffers stay reusable across turns.
func TestStreamAccumulator_ResetPreservesBufferCapacity(t *testing.T) {
	a := &StreamAccumulator{}
	large := strings.Repeat("x", 4096)
	a.Add(StreamChunk{Choices: []StreamChoice{{Delta: MessageDelta{Content: large, Reasoning: large}}}})

	contentCap := cap(a.content)
	reasoningCap := cap(a.reasoning)
	if contentCap < len(large) || reasoningCap < len(large) {
		t.Fatalf("expected buffers to grow to fit content, content cap=%d reasoning cap=%d", contentCap, reasoningCap)
	}

	a.Reset()

	if len(a.content) != 0 || len(a.reasoning) != 0 {
		t.Fatalf("expected buffers to be empty after Reset, content len=%d reasoning len=%d", len(a.content), len(a.reasoning))
	}
	if cap(a.content) != contentCap || cap(a.reasoning) != reasoningCap {
		t.Fatalf("expected Reset to preserve buffer capacity: content cap %d -> %d, reasoning cap %d -> %d",
			contentCap, cap(a.content), reasoningCap, cap(a.reasoning))
	}
}

func TestStreamAccumulator_PoolReuse(t *testing.T) {
	// Acquire, use, release
	a1 := AcquireStreamAccumulator()
	a1.Add(StreamChunk{
		Choices: []StreamChoice{
			{
				Delta: MessageDelta{
					Content: "test content",
				},
			},
		},
	})
	ReleaseStreamAccumulator(a1)

	// Acquire again - might get the same instance
	a2 := AcquireStreamAccumulator()
	if a2.Content() != "" {
		t.Errorf("expected reset accumulator, got content: %q", a2.Content())
	}

	// Verify it works normally
	a2.Add(StreamChunk{
		Choices: []StreamChoice{
			{
				Delta: MessageDelta{
					Content: "new content",
				},
			},
		},
	})
	if a2.Content() != "new content" {
		t.Errorf("expected 'new content', got %q", a2.Content())
	}
}

func TestStreamAccumulator_ResetBoundsRetainedCapacity(t *testing.T) {
	for _, tc := range []struct {
		name               string
		content, reasoning int
	}{
		{"small", 4096, 4096},
		{"at_limit", 64 << 10, 64 << 10},
		{"both_large", 1 << 20, 1 << 20},
		{"large_text", 1 << 20, 4096},
		{"large_reasoning", 4096, 1 << 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			acc := NewStreamAccumulator()
			text, reasoning := strings.Repeat("x", tc.content), strings.Repeat("r", tc.reasoning)
			acc.Add(StreamChunk{Choices: []StreamChoice{{Delta: MessageDelta{Content: text, Reasoning: reasoning}}}})
			message := acc.Message()
			textCap, reasoningCap := cap(acc.content), cap(acc.reasoning)
			acc.Reset()
			if cap(acc.content) > 64<<10 || cap(acc.reasoning) > 64<<10 {
				t.Fatal("reset retained oversized response storage")
			}
			if textCap <= 64<<10 && cap(acc.content) != textCap || reasoningCap <= 64<<10 && cap(acc.reasoning) != reasoningCap {
				t.Fatal("reset discarded reusable ordinary response storage")
			}
			acc.Add(StreamChunk{Choices: []StreamChoice{{Delta: MessageDelta{Content: "next", Reasoning: "new"}}}})
			if message.Content != text || message.Reasoning != reasoning || acc.Content() != "next" || acc.Reasoning() != "new" {
				t.Fatal("reset/reuse changed the returned message or next response")
			}
		})
	}
}
