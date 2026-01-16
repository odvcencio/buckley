package tui

import (
	"sync"
	"testing"
	"time"
)

func TestCoalescer_Add(t *testing.T) {
	var received []Message
	var mu sync.Mutex

	post := func(msg Message) {
		mu.Lock()
		received = append(received, msg)
		mu.Unlock()
	}

	c := NewCoalescer(CoalescerConfig{MaxChars: 10, MaxWait: 100 * time.Millisecond}, post)

	// Add small chunks - should buffer
	c.Add("session1", "abc")
	c.Add("session1", "def")

	mu.Lock()
	if len(received) != 0 {
		t.Errorf("expected 0 messages (buffered), got %d", len(received))
	}
	mu.Unlock()
}

func TestCoalescer_FlushOnSize(t *testing.T) {
	var received []Message
	var mu sync.Mutex

	post := func(msg Message) {
		mu.Lock()
		received = append(received, msg)
		mu.Unlock()
	}

	c := NewCoalescer(CoalescerConfig{MaxChars: 10, MaxWait: 100 * time.Millisecond}, post)

	// Add enough to exceed max chars
	c.Add("session1", "1234567890ab") // 12 chars > 10

	mu.Lock()
	if len(received) != 1 {
		t.Fatalf("expected 1 flush message, got %d", len(received))
	}

	flush, ok := received[0].(StreamFlush)
	if !ok {
		t.Fatalf("expected StreamFlush, got %T", received[0])
	}
	if flush.SessionID != "session1" {
		t.Errorf("expected session1, got %s", flush.SessionID)
	}
	if flush.Text != "1234567890ab" {
		t.Errorf("expected '1234567890ab', got %s", flush.Text)
	}
	mu.Unlock()
}

func TestCoalescer_Tick(t *testing.T) {
	var received []Message
	var mu sync.Mutex

	post := func(msg Message) {
		mu.Lock()
		received = append(received, msg)
		mu.Unlock()
	}

	c := NewCoalescer(CoalescerConfig{MaxChars: 100, MaxWait: 10 * time.Millisecond}, post)

	// Add content
	c.Add("session1", "hello")

	// Wait for maxWait to elapse
	time.Sleep(15 * time.Millisecond)

	// Tick should flush
	c.Tick()

	mu.Lock()
	if len(received) != 1 {
		t.Fatalf("expected 1 flush message after tick, got %d", len(received))
	}

	flush := received[0].(StreamFlush)
	if flush.Text != "hello" {
		t.Errorf("expected 'hello', got %s", flush.Text)
	}
	mu.Unlock()
}

func TestCoalescer_FlushAll(t *testing.T) {
	var received []Message
	var mu sync.Mutex

	post := func(msg Message) {
		mu.Lock()
		received = append(received, msg)
		mu.Unlock()
	}

	c := NewCoalescer(CoalescerConfig{MaxChars: 100, MaxWait: time.Hour}, post)

	// Add to multiple sessions
	c.Add("session1", "hello")
	c.Add("session2", "world")

	// FlushAll should flush both
	c.FlushAll()

	mu.Lock()
	if len(received) != 2 {
		t.Fatalf("expected 2 flush messages, got %d", len(received))
	}
	mu.Unlock()
}

func TestCoalescer_Clear(t *testing.T) {
	var received []Message

	post := func(msg Message) {
		received = append(received, msg)
	}

	c := NewCoalescer(CoalescerConfig{MaxChars: 100, MaxWait: time.Hour}, post)

	// Add content
	c.Add("session1", "hello")

	// Clear removes the buffer
	c.Clear("session1")

	// FlushAll should produce nothing
	c.FlushAll()

	if len(received) != 0 {
		t.Errorf("expected 0 messages after clear, got %d", len(received))
	}
}

func TestCoalescer_HasPending(t *testing.T) {
	c := NewCoalescer(DefaultCoalescerConfig(), nil)

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
	var received []Message
	var mu sync.Mutex

	post := func(msg Message) {
		mu.Lock()
		received = append(received, msg)
		mu.Unlock()
	}

	c := NewCoalescer(CoalescerConfig{MaxChars: 100, MaxWait: time.Hour}, post)

	// Add to different sessions
	c.Add("s1", "aaa")
	c.Add("s2", "bbb")
	c.Add("s1", "ccc")

	// Flush just s1
	c.Flush("s1")

	mu.Lock()
	if len(received) != 1 {
		t.Fatalf("expected 1 message, got %d", len(received))
	}

	flush := received[0].(StreamFlush)
	if flush.SessionID != "s1" || flush.Text != "aaaccc" {
		t.Errorf("unexpected flush: %+v", flush)
	}
	mu.Unlock()
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
	c := NewCoalescer(CoalescerConfig{}, nil)

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
	c := NewCoalescer(cfg, nil)

	if c.maxChars != 256 {
		t.Errorf("expected maxChars=256, got %d", c.maxChars)
	}
	if c.maxWait != 50*time.Millisecond {
		t.Errorf("expected maxWait=50ms, got %v", c.maxWait)
	}
}

func TestCoalescer_NilPost(t *testing.T) {
	// Test that Coalescer handles nil post function gracefully
	c := NewCoalescer(DefaultCoalescerConfig(), nil)

	// Add content
	c.Add("session1", "hello")

	// Should not panic when flushing with nil post function
	c.Flush("session1")
	c.FlushAll()

	// HasPending should still work
	if c.HasPending() {
		t.Error("expected no pending after flush with nil post")
	}
}

func TestCoalescer_FlushEmpty(t *testing.T) {
	var received []Message
	post := func(msg Message) {
		received = append(received, msg)
	}

	c := NewCoalescer(DefaultCoalescerConfig(), post)

	// Flush non-existent session should be no-op
	c.Flush("nonexistent")

	if len(received) != 0 {
		t.Errorf("expected 0 messages for flushing non-existent session, got %d", len(received))
	}

	// FlushAll on empty coalescer should be no-op
	c.FlushAll()

	if len(received) != 0 {
		t.Errorf("expected 0 messages for FlushAll on empty, got %d", len(received))
	}
}

func TestCoalescer_ClearNonExistent(t *testing.T) {
	c := NewCoalescer(DefaultCoalescerConfig(), nil)

	// Clear non-existent session should not panic
	c.Clear("nonexistent")
}

func TestCoalescer_TickNoContent(t *testing.T) {
	var received []Message
	post := func(msg Message) {
		received = append(received, msg)
	}

	c := NewCoalescer(CoalescerConfig{MaxChars: 100, MaxWait: 1 * time.Millisecond}, post)

	// Tick with no content should not produce messages
	c.Tick()

	if len(received) != 0 {
		t.Errorf("expected 0 messages for tick with no content, got %d", len(received))
	}
}

func TestCoalescer_TickRecentContent(t *testing.T) {
	var received []Message
	var mu sync.Mutex

	post := func(msg Message) {
		mu.Lock()
		received = append(received, msg)
		mu.Unlock()
	}

	// Long max wait to ensure content stays buffered
	c := NewCoalescer(CoalescerConfig{MaxChars: 100, MaxWait: 1 * time.Hour}, post)

	// Add content
	c.Add("session1", "hello")

	// Tick immediately should not flush (content is too recent)
	c.Tick()

	mu.Lock()
	count := len(received)
	mu.Unlock()

	if count != 0 {
		t.Errorf("expected 0 messages for tick with recent content, got %d", count)
	}
}

func TestCoalescer_TickEmptyBuffer(t *testing.T) {
	var received []Message
	post := func(msg Message) {
		received = append(received, msg)
	}

	c := NewCoalescer(CoalescerConfig{MaxChars: 100, MaxWait: 1 * time.Millisecond}, post)

	// Add and then flush
	c.Add("session1", "hello")
	c.Flush("session1")

	// Wait for maxWait to pass
	time.Sleep(5 * time.Millisecond)

	// Tick should not flush again (buffer is now empty)
	beforeCount := len(received)
	c.Tick()

	if len(received) != beforeCount {
		t.Errorf("expected no additional messages after tick on empty buffer, got %d (was %d)", len(received), beforeCount)
	}
}

func TestCoalescer_AddToNewBuffer(t *testing.T) {
	var received []Message
	post := func(msg Message) {
		received = append(received, msg)
	}

	c := NewCoalescer(CoalescerConfig{MaxChars: 5, MaxWait: time.Hour}, post)

	// First add creates buffer
	c.Add("s1", "ab")

	// Second add should append to same buffer
	c.Add("s1", "cd")

	// Not yet at max, so should be buffered
	if len(received) != 0 {
		t.Errorf("expected 0 messages before hitting max, got %d", len(received))
	}

	// This should trigger flush (total: "abcde" = 5 chars >= 5 maxChars)
	c.Add("s1", "e")

	if len(received) != 1 {
		t.Fatalf("expected 1 message after hitting max, got %d", len(received))
	}

	flush := received[0].(StreamFlush)
	if flush.Text != "abcde" {
		t.Errorf("expected 'abcde', got '%s'", flush.Text)
	}
}
