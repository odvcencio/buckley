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

// TestStreamAccumulator_ResetPreservesBufferCapacity proves Reset truncates
// the content/reasoning buffers (buf[:0]) instead of discarding them, so
// their allocated capacity survives across Acquire/Release cycles instead of
// being reallocated from scratch on every reuse.
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
