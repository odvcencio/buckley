package tui

import (
	"fmt"
	"strings"
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

func TestCoalescer_BlockedPublisherKeepsPublicationMemoryBoundedAndOrdered(t *testing.T) {
	var messages []Message
	var messagesMu sync.Mutex
	started := make(chan struct{})
	release := make(chan struct{})
	var blockFirst sync.Once
	c := NewCoalescer(CoalescerConfig{MaxChars: 1, MaxWait: time.Hour}, func(msg Message) {
		blockFirst.Do(func() {
			close(started)
			<-release
		})
		messagesMu.Lock()
		messages = append(messages, msg)
		messagesMu.Unlock()
	})

	publisherDone := make(chan struct{})
	go func() {
		c.AddStream("session", 1, "first")
		close(publisherDone)
	}()
	<-started
	for i := 0; i < coalescerPendingMaxCount*2; i++ {
		c.AddStream("session", 1, "x")
	}
	c.FlushAndPostStream("session", 1, StreamDone{SessionID: "session", Generation: 1})

	snapshot := c.Snapshot()
	if snapshot.PendingCount+snapshot.InFlightCount > coalescerPendingMaxCount {
		t.Fatalf("publication count exceeded cap: %+v", snapshot)
	}
	if snapshot.PendingBytes+snapshot.InFlightBytes > coalescerPendingMaxBytes {
		t.Fatalf("publication bytes exceeded cap: %+v", snapshot)
	}
	if !snapshot.Overloaded || snapshot.Rejected == 0 || snapshot.DiagnosticsQueued != 1 {
		t.Fatalf("publication overload was not observable: %+v", snapshot)
	}

	close(release)
	select {
	case <-publisherDone:
	case <-time.After(time.Second):
		t.Fatal("publisher did not drain after callback release")
	}
	messagesMu.Lock()
	defer messagesMu.Unlock()
	diagnosticIndex, terminalIndex := -1, -1
	for i, msg := range messages {
		switch msg.(type) {
		case streamOverloadMsg:
			diagnosticIndex = i
		case StreamDone, streamBoundaryRejectedMsg:
			terminalIndex = i
		}
	}
	if diagnosticIndex < 0 || terminalIndex <= diagnosticIndex {
		t.Fatalf("publication order diagnostic=%d terminal=%d (messages=%d)", diagnosticIndex, terminalIndex, len(messages))
	}
}

func TestCoalescer_ExactBoundaryCapKeepsDiagnosticAndTerminalRejection(t *testing.T) {
	var messages []Message
	var messagesMu sync.Mutex
	started := make(chan struct{})
	release := make(chan struct{})
	var blockFirst sync.Once
	c := NewCoalescer(DefaultCoalescerConfig(), func(msg Message) {
		blockFirst.Do(func() {
			close(started)
			<-release
		})
		messagesMu.Lock()
		messages = append(messages, msg)
		messagesMu.Unlock()
	})

	publisherDone := make(chan struct{})
	go func() {
		c.FlushAndPostStream("boundary-000", 1, StreamDone{SessionID: "boundary-000", Generation: 1})
		close(publisherDone)
	}()
	<-started
	for i := 1; i < coalescerPendingMaxCount; i++ {
		sessionID := fmt.Sprintf("boundary-%03d", i)
		c.FlushAndPostStream(sessionID, 1, StreamDone{SessionID: sessionID, Generation: 1})
	}
	snapshot := c.Snapshot()
	if snapshot.PendingCount+snapshot.InFlightCount != coalescerPendingMaxCount {
		t.Fatalf("boundary publication count = %+v", snapshot)
	}
	if snapshot.DiagnosticsQueued != 1 || snapshot.RejectedBoundaries != coalescerBoundaryReserve ||
		snapshot.BoundarySignals != coalescerBoundaryMarkerMaxCount || snapshot.Evicted != 0 {
		t.Fatalf("boundary saturation snapshot = %+v", snapshot)
	}

	close(release)
	select {
	case <-publisherDone:
	case <-time.After(time.Second):
		t.Fatal("boundary publisher did not drain")
	}
	messagesMu.Lock()
	defer messagesMu.Unlock()
	if len(messages) != coalescerPendingMaxCount {
		t.Fatalf("published %d messages, want %d", len(messages), coalescerPendingMaxCount)
	}
	diagnosticIndex := -1
	rejections := make(map[streamBoundaryKey]int)
	for i, msg := range messages {
		switch m := msg.(type) {
		case streamOverloadMsg:
			diagnosticIndex = i
		case StreamDone:
			if m.SessionID >= "boundary-239" {
				t.Fatalf("saturated boundary %q was published instead of rejected", m.SessionID)
			}
		case streamBoundaryRejectedMsg:
			if i <= diagnosticIndex {
				t.Fatalf("boundary rejection index %d preceded diagnostic %d", i, diagnosticIndex)
			}
			rejections[streamBoundaryKey{fingerprint: m.Fingerprint, generation: m.Generation}]++
		}
	}
	if diagnosticIndex < 0 || len(rejections) != coalescerBoundaryMarkerMaxCount {
		t.Fatalf("boundary ordering diagnostic=%d correlated=%d", diagnosticIndex, len(rejections))
	}
	for i := coalescerPendingMaxCount - coalescerBoundaryReserve; i < coalescerPendingMaxCount-1; i++ {
		key := streamBoundaryKey{
			fingerprint: streamFingerprint(fmt.Sprintf("boundary-%03d", i)),
			generation:  1,
		}
		if rejections[key] != 1 {
			t.Fatalf("boundary-%03d correlated marker count = %d, want 1", i, rejections[key])
		}
	}
}

func TestCoalescer_DiagnosticLatchRequiresSuccessfulEnqueue(t *testing.T) {
	c := NewCoalescer(DefaultCoalescerConfig(), nil)
	event := coalescerPublication{msg: StreamDone{}, bytes: eventMessageBaseBytes, boundary: true}
	c.mu.Lock()
	for i := 0; i < coalescerPendingMaxCount; i++ {
		c.pending = append(c.pending, event)
		c.pendingBytes += event.bytes
	}
	started := c.enterOverloadLocked()
	diagnosticSet := c.diagnosticSet
	c.mu.Unlock()
	if started || diagnosticSet {
		t.Fatalf("failed diagnostic enqueue started=%v latched=%v", started, diagnosticSet)
	}
	if got := c.Snapshot().DiagnosticsRejected; got != 1 {
		t.Fatalf("diagnostic rejection count = %d, want 1", got)
	}
}

func TestCoalescer_UniqueBelowThresholdBuffersStayBoundedWhilePublisherBlocked(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var blockFirst sync.Once
	c := NewCoalescer(CoalescerConfig{MaxChars: 128, MaxWait: time.Hour}, func(Message) {
		blockFirst.Do(func() {
			close(started)
			<-release
		})
	})
	publisherDone := make(chan struct{})
	go func() {
		c.AddStream("publisher", 1, strings.Repeat("p", 128))
		close(publisherDone)
	}()
	<-started
	for i := 0; i < coalescerBufferMaxCount*2; i++ {
		c.AddStream(fmt.Sprintf("buffer-%03d", i), 1, "x")
	}
	snapshot := c.Snapshot()
	if snapshot.BufferedStreams > coalescerBufferMaxCount || snapshot.BufferBytes > coalescerBufferMaxBytes {
		t.Fatalf("buffer cap exceeded: %+v", snapshot)
	}
	if snapshot.RejectedBuffers == 0 || snapshot.DiagnosticsQueued != 1 {
		t.Fatalf("buffer overload was not observable: %+v", snapshot)
	}
	c.Close()
	afterClose := c.Snapshot()
	if afterClose.BufferedStreams != 0 || afterClose.BufferBytes != 0 || afterClose.PendingCount != 0 {
		t.Fatalf("Close retained coalescer payloads: %+v", afterClose)
	}
	close(release)
	select {
	case <-publisherDone:
	case <-time.After(time.Second):
		t.Fatal("blocked publisher did not return after release")
	}
}
