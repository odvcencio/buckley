package performance

import (
	"strings"
	"testing"
	"time"
)

func TestNewMetrics(t *testing.T) {
	m := NewMetrics()

	if m == nil {
		t.Fatal("NewMetrics should return non-nil")
	}

	if m.operationDurations == nil {
		t.Error("operationDurations should be initialized")
	}

	if m.operationCounts == nil {
		t.Error("operationCounts should be initialized")
	}

	if m.errors == nil {
		t.Error("errors should be initialized")
	}
}

func TestTrackOperation(t *testing.T) {
	m := NewMetrics()

	m.TrackOperation("test_op", 100*time.Millisecond)
	m.TrackOperation("test_op", 200*time.Millisecond)
	m.TrackOperation("test_op", 150*time.Millisecond)

	stats := m.GetStats()

	if len(stats) != 1 {
		t.Errorf("Expected 1 operation in stats, got %d", len(stats))
	}

	opStats, exists := stats["test_op"]
	if !exists {
		t.Fatal("test_op should exist in stats")
	}

	if opStats.Count != 3 {
		t.Errorf("Expected count 3, got %d", opStats.Count)
	}

	if opStats.MinTime != 100*time.Millisecond {
		t.Errorf("Expected min 100ms, got %v", opStats.MinTime)
	}

	if opStats.MaxTime != 200*time.Millisecond {
		t.Errorf("Expected max 200ms, got %v", opStats.MaxTime)
	}

	expectedAvg := 150 * time.Millisecond
	if opStats.AverageTime != expectedAvg {
		t.Errorf("Expected avg %v, got %v", expectedAvg, opStats.AverageTime)
	}
}

func TestTrackError(t *testing.T) {
	m := NewMetrics()

	m.TrackOperation("test_op", 100*time.Millisecond)
	m.TrackError("test_op")
	m.TrackOperation("test_op", 200*time.Millisecond)

	stats := m.GetStats()
	opStats := stats["test_op"]

	if opStats.Errors != 1 {
		t.Errorf("Expected 1 error, got %d", opStats.Errors)
	}

	if opStats.Count != 2 {
		t.Errorf("Expected count 2, got %d", opStats.Count)
	}
}

func TestTimer(t *testing.T) {
	m := NewMetrics()

	timer := m.StartTimer("timer_test")
	time.Sleep(10 * time.Millisecond)
	timer.Stop()

	stats := m.GetStats()
	opStats := stats["timer_test"]

	if opStats.Count != 1 {
		t.Errorf("Expected count 1, got %d", opStats.Count)
	}

	if opStats.AverageTime < 10*time.Millisecond {
		t.Errorf("Expected at least 10ms, got %v", opStats.AverageTime)
	}
}

func TestTimer_WithError(t *testing.T) {
	m := NewMetrics()

	timer := m.StartTimer("error_test")
	time.Sleep(5 * time.Millisecond)
	timer.StopWithError()

	stats := m.GetStats()
	opStats := stats["error_test"]

	if opStats.Count != 1 {
		t.Errorf("Expected count 1, got %d", opStats.Count)
	}

	if opStats.Errors != 1 {
		t.Errorf("Expected 1 error, got %d", opStats.Errors)
	}
}

func TestUptime(t *testing.T) {
	m := NewMetrics()

	time.Sleep(10 * time.Millisecond)

	uptime := m.Uptime()

	if uptime < 10*time.Millisecond {
		t.Errorf("Expected uptime >= 10ms, got %v", uptime)
	}
}

func TestReset(t *testing.T) {
	m := NewMetrics()

	m.TrackOperation("test", 100*time.Millisecond)
	m.TrackError("test")

	stats := m.GetStats()
	if len(stats) != 1 {
		t.Fatal("Should have 1 operation before reset")
	}

	m.Reset()

	stats = m.GetStats()
	if len(stats) != 0 {
		t.Error("Should have 0 operations after reset")
	}
}

func TestGetStats_MultipleOperations(t *testing.T) {
	m := NewMetrics()

	m.TrackOperation("op1", 100*time.Millisecond)
	m.TrackOperation("op2", 200*time.Millisecond)
	m.TrackOperation("op1", 150*time.Millisecond)

	stats := m.GetStats()

	if len(stats) != 2 {
		t.Errorf("Expected 2 operations, got %d", len(stats))
	}

	if _, exists := stats["op1"]; !exists {
		t.Error("op1 should exist in stats")
	}

	if _, exists := stats["op2"]; !exists {
		t.Error("op2 should exist in stats")
	}

	if stats["op1"].Count != 2 {
		t.Errorf("op1 count should be 2, got %d", stats["op1"].Count)
	}

	if stats["op2"].Count != 1 {
		t.Errorf("op2 count should be 1, got %d", stats["op2"].Count)
	}
}

func TestCalculatePercentiles(t *testing.T) {
	durations := []time.Duration{
		10 * time.Millisecond,
		20 * time.Millisecond,
		30 * time.Millisecond,
		40 * time.Millisecond,
		50 * time.Millisecond,
		60 * time.Millisecond,
		70 * time.Millisecond,
		80 * time.Millisecond,
		90 * time.Millisecond,
		100 * time.Millisecond,
	}

	p50, p95, p99 := calculatePercentiles(durations)

	// P50 should be around 50ms (median)
	if p50 < 40*time.Millisecond || p50 > 60*time.Millisecond {
		t.Errorf("P50 should be ~50ms, got %v", p50)
	}

	// P95 should be close to 95ms
	if p95 < 85*time.Millisecond || p95 > 100*time.Millisecond {
		t.Errorf("P95 should be ~95ms, got %v", p95)
	}

	// P99 should be close to 99ms
	if p99 < 90*time.Millisecond || p99 > 100*time.Millisecond {
		t.Errorf("P99 should be ~99ms, got %v", p99)
	}
}

func TestCalculatePercentiles_Empty(t *testing.T) {
	p50, p95, p99 := calculatePercentiles([]time.Duration{})

	if p50 != 0 || p95 != 0 || p99 != 0 {
		t.Error("Empty durations should return 0 for all percentiles")
	}
}

func TestMemoryStats(t *testing.T) {
	stats := MemoryStats()

	expectedKeys := []string{
		"alloc_mb",
		"total_alloc_mb",
		"sys_mb",
		"num_gc",
		"goroutines",
	}

	for _, key := range expectedKeys {
		if _, exists := stats[key]; !exists {
			t.Errorf("MemoryStats should contain key %q", key)
		}
	}

	// Verify types and reasonable values
	if allocMB, ok := stats["alloc_mb"].(float64); ok {
		if allocMB < 0 {
			t.Error("alloc_mb should be non-negative")
		}
	} else {
		t.Error("alloc_mb should be float64")
	}

	if goroutines, ok := stats["goroutines"].(int); ok {
		if goroutines <= 0 {
			t.Error("goroutines should be positive")
		}
	} else {
		t.Error("goroutines should be int")
	}
}

func TestFormatStats(t *testing.T) {
	m := NewMetrics()

	m.TrackOperation("test_op", 100*time.Millisecond)
	m.TrackOperation("test_op", 200*time.Millisecond)
	m.TrackError("test_op")

	stats := m.GetStats()
	formatted := FormatStats(stats)

	if formatted == "" {
		t.Error("FormatStats should return non-empty string")
	}

	// Should contain operation name
	if !strings.Contains(formatted, "test_op") {
		t.Error("FormatStats should contain operation name")
	}

	// Should contain count
	if !strings.Contains(formatted, "Count") {
		t.Error("FormatStats should contain count")
	}

	// Should contain error rate
	if !strings.Contains(formatted, "errors") {
		t.Error("FormatStats should contain error information")
	}

	// Should contain timing info
	if !strings.Contains(formatted, "Avg") {
		t.Error("FormatStats should contain average time")
	}

	// Should contain percentiles
	if !strings.Contains(formatted, "P50") {
		t.Error("FormatStats should contain percentiles")
	}
}

func TestConcurrentTracking(t *testing.T) {
	m := NewMetrics()

	// Simulate concurrent tracking
	done := make(chan bool, 10)

	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				m.TrackOperation("concurrent", 10*time.Millisecond)
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	stats := m.GetStats()
	opStats := stats["concurrent"]

	if opStats.Count != 1000 {
		t.Errorf("Expected count 1000, got %d", opStats.Count)
	}
}

// Benchmark tests

func BenchmarkTrackOperation(b *testing.B) {
	m := NewMetrics()

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		m.TrackOperation("bench", 100*time.Millisecond)
	}
}

func BenchmarkGetStats(b *testing.B) {
	m := NewMetrics()

	// Add some data
	for i := 0; i < 1000; i++ {
		m.TrackOperation("op1", time.Duration(i)*time.Millisecond)
		m.TrackOperation("op2", time.Duration(i)*time.Millisecond)
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		m.GetStats()
	}
}

func BenchmarkTimer(b *testing.B) {
	m := NewMetrics()

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		timer := m.StartTimer("bench")
		timer.Stop()
	}
}

func BenchmarkMemoryStats(b *testing.B) {
	for i := 0; i < b.N; i++ {
		MemoryStats()
	}
}
