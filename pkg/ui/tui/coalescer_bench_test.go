package tui

import (
	"testing"
	"time"
)

// BenchmarkCoalescer_Add measures chunk addition.
func BenchmarkCoalescer_Add(b *testing.B) {
	c := NewCoalescer(DefaultCoalescerConfig(), func(msg Message) {})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Add("session-1", "chunk ")
	}
}

// BenchmarkCoalescer_Tick measures frame tick processing.
func BenchmarkCoalescer_Tick(b *testing.B) {
	c := NewCoalescer(DefaultCoalescerConfig(), func(msg Message) {})

	// Add some content
	for i := 0; i < 100; i++ {
		c.Add("session-1", "chunk ")
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Tick()
	}
}

// BenchmarkCoalescer_HighThroughput simulates high-throughput streaming.
func BenchmarkCoalescer_HighThroughput(b *testing.B) {
	flushCount := 0
	c := NewCoalescer(CoalescerConfig{
		MaxWait:  16 * time.Millisecond,
		MaxChars: 4096,
	}, func(msg Message) {
		flushCount++
	})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Simulate rapid streaming
		for j := 0; j < 100; j++ {
			c.Add("session-1", "x")
		}
		c.Tick()
	}
	_ = flushCount
}

// BenchmarkCoalescer_MultipleSessions measures multi-session handling.
func BenchmarkCoalescer_MultipleSessions(b *testing.B) {
	c := NewCoalescer(DefaultCoalescerConfig(), func(msg Message) {})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Add to 5 different sessions
		c.Add("session-0", "a")
		c.Add("session-1", "b")
		c.Add("session-2", "c")
		c.Add("session-3", "d")
		c.Add("session-4", "e")
		c.Tick()
	}
}

// BenchmarkCoalescer_FlushAll measures forced flush.
func BenchmarkCoalescer_FlushAll(b *testing.B) {
	c := NewCoalescer(DefaultCoalescerConfig(), func(msg Message) {})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Add content and flush
		for j := 0; j < 10; j++ {
			c.Add("session-1", "chunk ")
		}
		c.FlushAll()
	}
}
