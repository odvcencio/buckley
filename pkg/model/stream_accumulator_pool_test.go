package model

import (
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
