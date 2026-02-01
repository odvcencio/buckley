package tui

import (
	"testing"
	"time"
)

func TestCoalescer_Add(t *testing.T) {
	c := NewCoalescer(CoalescerConfig{MaxChars: 10, MaxWait: 100 * time.Millisecond})

	// Add small chunks - should buffer
	c.Add("session1", "abc")
	c.Add("session1", "def")

	if flushed := c.Drain(); len(flushed) != 0 {
		t.Errorf("expected 0 flushes (buffered), got %d", len(flushed))
	}
}

func TestCoalescer_FlushOnSize(t *testing.T) {
	c := NewCoalescer(CoalescerConfig{MaxChars: 10, MaxWait: 100 * time.Millisecond})

	// Add enough to exceed max chars
	c.Add("session1", "1234567890ab") // 12 chars > 10

	flushed := c.Drain()
	if len(flushed) != 1 {
		t.Fatalf("expected 1 flush event, got %d", len(flushed))
	}

	flush := flushed[0]
	if flush.SessionID != "session1" {
		t.Errorf("expected session1, got %s", flush.SessionID)
	}
	if flush.Text != "1234567890ab" {
		t.Errorf("expected '1234567890ab', got %s", flush.Text)
	}
}

func TestCoalescer_Tick(t *testing.T) {
	c := NewCoalescer(CoalescerConfig{MaxChars: 100, MaxWait: 10 * time.Millisecond})

	// Add content
	c.Add("session1", "hello")

	// Wait for maxWait to elapse
	time.Sleep(15 * time.Millisecond)

	// Tick should flush
	c.Tick()

	flushed := c.Drain()
	if len(flushed) != 1 {
		t.Fatalf("expected 1 flush event after tick, got %d", len(flushed))
	}

	flush := flushed[0]
	if flush.Text != "hello" {
		t.Errorf("expected 'hello', got %s", flush.Text)
	}
}

func TestCoalescer_FlushAll(t *testing.T) {
	c := NewCoalescer(CoalescerConfig{MaxChars: 100, MaxWait: time.Hour})

	// Add to multiple sessions
	c.Add("session1", "hello")
	c.Add("session2", "world")

	// FlushAll should flush both
	c.FlushAll()

	if flushed := c.Drain(); len(flushed) != 2 {
		t.Fatalf("expected 2 flush events, got %d", len(flushed))
	}
}

func TestCoalescer_Clear(t *testing.T) {
	c := NewCoalescer(CoalescerConfig{MaxChars: 100, MaxWait: time.Hour})

	// Add content
	c.Add("session1", "hello")

	// Clear removes the buffer
	c.Clear("session1")

	// FlushAll should produce nothing
	c.FlushAll()

	if flushed := c.Drain(); len(flushed) != 0 {
		t.Errorf("expected 0 flushes after clear, got %d", len(flushed))
	}
}

func TestCoalescer_HasPending(t *testing.T) {
	c := NewCoalescer(DefaultCoalescerConfig())

	if c.HasPending() {
		t.Error("expected no pending initially")
	}

	c.Add("session1", "test")

	if !c.HasPending() {
		t.Error("expected pending after add")
	}

	c.Clear("session1")

	if c.HasPending() {
		t.Error("expected no pending after clear")
	}
}

func TestCoalescer_MultipleSessions(t *testing.T) {
	c := NewCoalescer(CoalescerConfig{MaxChars: 100, MaxWait: time.Hour})

	// Add to different sessions
	c.Add("s1", "aaa")
	c.Add("s2", "bbb")
	c.Add("s1", "ccc")

	// Flush just s1
	c.Flush("s1")

	flushed := c.Drain()
	if len(flushed) != 1 {
		t.Fatalf("expected 1 flush event, got %d", len(flushed))
	}

	flush := flushed[0]
	if flush.SessionID != "s1" || flush.Text != "aaaccc" {
		t.Errorf("unexpected flush: %+v", flush)
	}
}

func TestDefaultCoalescerConfig(t *testing.T) {
	cfg := DefaultCoalescerConfig()
	if cfg.MaxChars != 128 {
		t.Errorf("expected MaxChars=128, got %d", cfg.MaxChars)
	}
	if cfg.MaxWait != 16*time.Millisecond {
		t.Errorf("expected MaxWait=16ms, got %v", cfg.MaxWait)
	}
}

func TestNewCoalescer_ZeroConfig(t *testing.T) {
	// Test that NewCoalescer applies defaults when config has zero values
	c := NewCoalescer(CoalescerConfig{})

	if c.maxChars != 128 {
		t.Errorf("expected default maxChars=128, got %d", c.maxChars)
	}
	if c.maxWait != 16*time.Millisecond {
		t.Errorf("expected default maxWait=16ms, got %v", c.maxWait)
	}
}

func TestNewCoalescer_CustomConfig(t *testing.T) {
	// Test that NewCoalescer preserves custom config values
	cfg := CoalescerConfig{
		MaxChars: 256,
		MaxWait:  50 * time.Millisecond,
	}
	c := NewCoalescer(cfg)

	if c.maxChars != 256 {
		t.Errorf("expected maxChars=256, got %d", c.maxChars)
	}
	if c.maxWait != 50*time.Millisecond {
		t.Errorf("expected maxWait=50ms, got %v", c.maxWait)
	}
}

func TestCoalescer_DrainEmpty(t *testing.T) {
	c := NewCoalescer(DefaultCoalescerConfig())
	if flushed := c.Drain(); len(flushed) != 0 {
		t.Errorf("expected empty drain on new coalescer, got %d", len(flushed))
	}
}

func TestCoalescer_FlushEmpty(t *testing.T) {
	c := NewCoalescer(DefaultCoalescerConfig())

	// Flush non-existent session should be no-op
	c.Flush("nonexistent")

	// FlushAll on empty coalescer should be no-op
	c.FlushAll()

	if flushed := c.Drain(); len(flushed) != 0 {
		t.Errorf("expected 0 flushes for empty coalescer, got %d", len(flushed))
	}
}

func TestCoalescer_ClearNonExistent(t *testing.T) {
	c := NewCoalescer(DefaultCoalescerConfig())

	// Clear non-existent session should not panic
	c.Clear("nonexistent")
}

func TestCoalescer_TickNoContent(t *testing.T) {
	c := NewCoalescer(CoalescerConfig{MaxChars: 100, MaxWait: 1 * time.Millisecond})

	// Tick with no content should not produce messages
	c.Tick()

	if flushed := c.Drain(); len(flushed) != 0 {
		t.Errorf("expected 0 flushes for tick with no content, got %d", len(flushed))
	}
}

func TestCoalescer_TickRecentContent(t *testing.T) {
	// Long max wait to ensure content stays buffered
	c := NewCoalescer(CoalescerConfig{MaxChars: 100, MaxWait: 1 * time.Hour})

	// Add content
	c.Add("session1", "hello")

	// Tick immediately should not flush (content is too recent)
	c.Tick()

	if flushed := c.Drain(); len(flushed) != 0 {
		t.Errorf("expected 0 flushes for tick with recent content, got %d", len(flushed))
	}
}

func TestCoalescer_TickEmptyBuffer(t *testing.T) {
	c := NewCoalescer(CoalescerConfig{MaxChars: 100, MaxWait: 1 * time.Millisecond})

	// Add and then flush
	c.Add("session1", "hello")
	c.Flush("session1")

	// Wait for maxWait to pass
	time.Sleep(5 * time.Millisecond)

	// Tick should not flush again (buffer is now empty)
	beforeCount := len(c.Drain())
	c.Tick()

	if len(c.Drain()) != 0 {
		t.Errorf("expected no additional flushes after tick on empty buffer, had %d before", beforeCount)
	}
}

func TestCoalescer_AddToNewBuffer(t *testing.T) {
	c := NewCoalescer(CoalescerConfig{MaxChars: 5, MaxWait: time.Hour})

	// First add creates buffer
	c.Add("s1", "ab")

	// Second add should append to same buffer
	c.Add("s1", "cd")

	// Not yet at max, so should be buffered
	if flushed := c.Drain(); len(flushed) != 0 {
		t.Errorf("expected 0 flushes before hitting max, got %d", len(flushed))
	}

	// This should trigger flush (total: "abcde" = 5 chars >= 5 maxChars)
	c.Add("s1", "e")

	flushed := c.Drain()
	if len(flushed) != 1 {
		t.Fatalf("expected 1 flush after hitting max, got %d", len(flushed))
	}

	flush := flushed[0]
	if flush.Text != "abcde" {
		t.Errorf("expected 'abcde', got '%s'", flush.Text)
	}
}
