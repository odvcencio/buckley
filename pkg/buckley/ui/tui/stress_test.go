package tui

import (
	"strings"
	"testing"
	"time"
)

// TestCoalescer_HighFrequencyChunks tests coalescing under rapid chunk delivery.
func TestCoalescer_HighFrequencyChunks(t *testing.T) {
	var flushed []string
	cfg := CoalescerConfig{
		MaxWait:  16 * time.Millisecond,
		MaxChars: 4096,
	}
	c := NewCoalescer(cfg)

	// Simulate rapid chunks (like a fast streaming API)
	sessionID := "stress-1"
	for i := 0; i < 1000; i++ {
		c.Add(sessionID, "chunk ")
	}

	// FlushAll to get everything
	c.FlushAll()
	for _, item := range c.Drain() {
		flushed = append(flushed, item.Text)
	}

	// Should have coalesced many chunks
	totalLen := 0
	for _, f := range flushed {
		totalLen += len(f)
	}

	expectedLen := 1000 * len("chunk ")
	if totalLen != expectedLen {
		t.Errorf("expected total length %d, got %d", expectedLen, totalLen)
	}
}

// TestCoalescer_LargeOutput tests handling of large text outputs.
func TestCoalescer_LargeOutput(t *testing.T) {
	var flushed []string
	cfg := CoalescerConfig{
		MaxWait:  16 * time.Millisecond,
		MaxChars: 1024, // Small buffer to force flushes
	}
	c := NewCoalescer(cfg)

	// Send a large chunk (10KB)
	largeText := strings.Repeat("x", 10*1024)
	c.Add("large-1", largeText)

	// Should have flushed multiple times due to MaxBuffer
	c.FlushAll()
	for _, item := range c.Drain() {
		flushed = append(flushed, item.Text)
	}

	totalLen := 0
	for _, f := range flushed {
		totalLen += len(f)
	}

	if totalLen != len(largeText) {
		t.Errorf("expected total length %d, got %d", len(largeText), totalLen)
	}
}

// TestCoalescer_ConcurrentSessions tests multiple simultaneous sessions.
func TestCoalescer_ConcurrentSessions(t *testing.T) {
	sessionData := make(map[string]string)
	c := NewCoalescer(DefaultCoalescerConfig())

	// Simulate 10 concurrent sessions
	numSessions := 10
	chunksPerSession := 100

	for i := 0; i < chunksPerSession; i++ {
		for s := 0; s < numSessions; s++ {
			sessionID := "session-" + intToString(s)
			c.Add(sessionID, "X")
		}
	}

	c.FlushAll()
	for _, item := range c.Drain() {
		sessionData[item.SessionID] += item.Text
	}

	// Verify each session got its data
	for s := 0; s < numSessions; s++ {
		sessionID := "session-" + intToString(s)
		expected := strings.Repeat("X", chunksPerSession)
		if sessionData[sessionID] != expected {
			t.Errorf("session %s: expected %d chars, got %d",
				sessionID, len(expected), len(sessionData[sessionID]))
		}
	}
}

// TestCoalescer_RapidFlush tests behavior under rapid flush calls.
func TestCoalescer_RapidFlush(t *testing.T) {
	flushCount := 0
	totalData := 0
	c := NewCoalescer(DefaultCoalescerConfig())

	// Add data and flush each time
	for i := 0; i < 100; i++ {
		c.Add("rapid", "data")
		c.Flush("rapid") // Explicit flush for this session
		for _, item := range c.Drain() {
			flushCount++
			totalData += len(item.Text)
		}
	}

	// Should have all data flushed
	expectedLen := 100 * len("data")
	if totalData != expectedLen {
		t.Errorf("expected total data %d, got %d", expectedLen, totalData)
	}
	if flushCount != 100 {
		t.Errorf("expected 100 flushes, got %d", flushCount)
	}
}

// TestCoalescer_MemoryPressure tests memory behavior under sustained load.
func TestCoalescer_MemoryPressure(t *testing.T) {
	// Track max buffer size seen
	maxBufferSize := 0
	cfg := CoalescerConfig{
		MaxWait:  16 * time.Millisecond,
		MaxChars: 4096,
	}
	c := NewCoalescer(cfg)

	// Sustained streaming for simulated 1 second (many ticks)
	for tick := 0; tick < 60; tick++ { // 60 ticks = ~1 second at 60fps
		// Add chunks between ticks
		for i := 0; i < 10; i++ {
			c.Add("pressure", strings.Repeat("y", 100))
		}
		c.Tick()
		for _, item := range c.Drain() {
			if len(item.Text) > maxBufferSize {
				maxBufferSize = len(item.Text)
			}
		}
	}

	// Max buffer should never exceed configured limit by much
	// (there's some overhead for the flush mechanism)
	if maxBufferSize > cfg.MaxChars*2 {
		t.Errorf("buffer grew too large: %d (max configured: %d)", maxBufferSize, cfg.MaxChars)
	}
}

// intToString converts int to string without fmt.
func intToString(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}
