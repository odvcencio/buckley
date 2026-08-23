package performance

import (
	"fmt"
	"runtime"
	"sync"
	"time"
)

// Metrics tracks performance metrics
type Metrics struct {
	mu                 sync.RWMutex
	operationDurations map[string][]time.Duration
	operationCounts    map[string]int64
	errors             map[string]int64
	startTime          time.Time
}

// NewMetrics creates a new metrics tracker
func NewMetrics() *Metrics {
	return &Metrics{
		operationDurations: make(map[string][]time.Duration),
		operationCounts:    make(map[string]int64),
		errors:             make(map[string]int64),
		startTime:          time.Now(),
	}
}

// TrackOperation records the duration of an operation
func (m *Metrics) TrackOperation(name string, duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.operationDurations[name] = append(m.operationDurations[name], duration)
	m.operationCounts[name]++
}

// TrackError records an error for an operation
func (m *Metrics) TrackError(operation string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.errors[operation]++
}

// GetStats returns aggregated statistics
func (m *Metrics) GetStats() map[string]OperationStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := make(map[string]OperationStats)

	for name, durations := range m.operationDurations {
		if len(durations) == 0 {
			continue
		}

		total := time.Duration(0)
		min := durations[0]
		max := durations[0]

		for _, d := range durations {
			total += d
			if d < min {
				min = d
			}
			if d > max {
				max = d
			}
		}

		avg := total / time.Duration(len(durations))

		// Calculate percentiles
		p50, p95, p99 := calculatePercentiles(durations)

		stats[name] = OperationStats{
			Count:       m.operationCounts[name],
			Errors:      m.errors[name],
			TotalTime:   total,
			AverageTime: avg,
			MinTime:     min,
			MaxTime:     max,
			P50:         p50,
			P95:         p95,
			P99:         p99,
		}
	}

	return stats
}

// OperationStats holds statistics for an operation
type OperationStats struct {
	Count       int64
	Errors      int64
	TotalTime   time.Duration
	AverageTime time.Duration
	MinTime     time.Duration
	MaxTime     time.Duration
	P50         time.Duration
	P95         time.Duration
	P99         time.Duration
}

// Uptime returns how long the metrics have been tracking
func (m *Metrics) Uptime() time.Duration {
	return time.Since(m.startTime)
}

// Reset clears all metrics
func (m *Metrics) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.operationDurations = make(map[string][]time.Duration)
	m.operationCounts = make(map[string]int64)
	m.errors = make(map[string]int64)
	m.startTime = time.Now()
}

// Timer provides easy duration tracking
type Timer struct {
	start   time.Time
	metrics *Metrics
	name    string
}

// StartTimer creates a new timer
func (m *Metrics) StartTimer(name string) *Timer {
	return &Timer{
		start:   time.Now(),
		metrics: m,
		name:    name,
	}
}

// Stop stops the timer and records the duration
func (t *Timer) Stop() {
	duration := time.Since(t.start)
	t.metrics.TrackOperation(t.name, duration)
}

// StopWithError stops the timer and records an error
func (t *Timer) StopWithError() {
	duration := time.Since(t.start)
	t.metrics.TrackOperation(t.name, duration)
	t.metrics.TrackError(t.name)
}

// calculatePercentiles calculates p50, p95, p99 percentiles
func calculatePercentiles(durations []time.Duration) (time.Duration, time.Duration, time.Duration) {
	if len(durations) == 0 {
		return 0, 0, 0
	}

	// Simple percentile calculation (not fully accurate but fast)
	// For production, use a proper percentile library

	// Sort durations (bubble sort for simplicity)
	sorted := make([]time.Duration, len(durations))
	copy(sorted, durations)

	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[i] > sorted[j] {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	p50Index := int(float64(len(sorted)) * 0.50)
	p95Index := int(float64(len(sorted)) * 0.95)
	p99Index := int(float64(len(sorted)) * 0.99)

	if p50Index >= len(sorted) {
		p50Index = len(sorted) - 1
	}
	if p95Index >= len(sorted) {
		p95Index = len(sorted) - 1
	}
	if p99Index >= len(sorted) {
		p99Index = len(sorted) - 1
	}

	return sorted[p50Index], sorted[p95Index], sorted[p99Index]
}

// MemoryStats returns current memory statistics
func MemoryStats() map[string]any {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	return map[string]any{
		"alloc_mb":       float64(m.Alloc) / 1024 / 1024,
		"total_alloc_mb": float64(m.TotalAlloc) / 1024 / 1024,
		"sys_mb":         float64(m.Sys) / 1024 / 1024,
		"num_gc":         m.NumGC,
		"goroutines":     runtime.NumGoroutine(),
	}
}

// FormatStats formats operation stats for display
func FormatStats(stats map[string]OperationStats) string {
	var result string

	for name, s := range stats {
		errorRate := 0.0
		if s.Count > 0 {
			errorRate = float64(s.Errors) / float64(s.Count) * 100
		}

		result += fmt.Sprintf("%s:\n", name)
		result += fmt.Sprintf("  Count: %d (%.1f%% errors)\n", s.Count, errorRate)
		result += fmt.Sprintf("  Avg: %v, Min: %v, Max: %v\n", s.AverageTime, s.MinTime, s.MaxTime)
		result += fmt.Sprintf("  P50: %v, P95: %v, P99: %v\n", s.P50, s.P95, s.P99)
		result += "\n"
	}

	return result
}
