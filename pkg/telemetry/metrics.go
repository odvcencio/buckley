package telemetry

import (
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"runtime/pprof"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// MetricType identifies the kind of metric.
type MetricType string

const (
	MetricTypeCounter   MetricType = "counter"
	MetricTypeGauge     MetricType = "gauge"
	MetricTypeHistogram MetricType = "histogram"
)

// Metric is the common interface for all metric types.
type Metric interface {
	Name() string
	Type() MetricType
	String() string
}

// Labels represents a set of dimensional labels for metrics.
type Labels map[string]string

// String returns a string representation of labels for map keys.
func (l Labels) String() string {
	if len(l) == 0 {
		return ""
	}
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	result := ""
	for i, k := range keys {
		if i > 0 {
			result += ","
		}
		result += fmt.Sprintf("%s=%s", k, l[k])
	}
	return result
}

// Counter is a monotonically increasing metric.
type Counter struct {
	name   string
	labels Labels
	value  atomic.Int64
}

// NewCounter creates a new counter with the given name and labels.
func NewCounter(name string, labels Labels) *Counter {
	if labels == nil {
		labels = Labels{}
	}
	return &Counter{
		name:   name,
		labels: labels,
	}
}

// Name returns the metric name.
func (c *Counter) Name() string {
	return c.name
}

// Type returns the metric type.
func (c *Counter) Type() MetricType {
	return MetricTypeCounter
}

// Labels returns the metric labels.
func (c *Counter) Labels() Labels {
	return c.labels
}

// Inc increments the counter by 1.
func (c *Counter) Inc() {
	if c == nil {
		return
	}
	c.value.Add(1)
}

// Add adds the given value to the counter.
func (c *Counter) Add(delta int64) {
	if c == nil {
		return
	}
	if delta < 0 {
		return // counters don't decrease
	}
	c.value.Add(delta)
}

// Get returns the current value.
func (c *Counter) Get() int64 {
	if c == nil {
		return 0
	}
	return c.value.Load()
}

// String returns a human-readable representation.
func (c *Counter) String() string {
	if c == nil {
		return "Counter<nil>"
	}
	return fmt.Sprintf("Counter{name=%s, labels=%s, value=%d}", c.name, c.labels.String(), c.Get())
}

// MarshalJSON implements json.Marshaler.
func (c *Counter) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{
		"name":   c.name,
		"type":   c.Type(),
		"labels": c.labels,
		"value":  c.Get(),
	})
}

// Gauge is a metric that can go up and down.
type Gauge struct {
	name   string
	labels Labels
	value  atomic.Int64
}

// NewGauge creates a new gauge with the given name and labels.
func NewGauge(name string, labels Labels) *Gauge {
	if labels == nil {
		labels = Labels{}
	}
	return &Gauge{
		name:   name,
		labels: labels,
	}
}

// Name returns the metric name.
func (g *Gauge) Name() string {
	return g.name
}

// Type returns the metric type.
func (g *Gauge) Type() MetricType {
	return MetricTypeGauge
}

// Labels returns the metric labels.
func (g *Gauge) Labels() Labels {
	return g.labels
}

// Set sets the gauge to the given value.
func (g *Gauge) Set(value int64) {
	if g == nil {
		return
	}
	g.value.Store(value)
}

// SetFloat64 sets the gauge to the given float64 value (stored as nanoseconds for time values).
func (g *Gauge) SetFloat64(value float64) {
	if g == nil {
		return
	}
	g.value.Store(int64(value))
}

// Inc increments the gauge by 1.
func (g *Gauge) Inc() {
	if g == nil {
		return
	}
	g.value.Add(1)
}

// Dec decrements the gauge by 1.
func (g *Gauge) Dec() {
	if g == nil {
		return
	}
	g.value.Add(-1)
}

// Add adds the given value to the gauge.
func (g *Gauge) Add(delta int64) {
	if g == nil {
		return
	}
	g.value.Add(delta)
}

// Get returns the current value.
func (g *Gauge) Get() int64 {
	if g == nil {
		return 0
	}
	return g.value.Load()
}

// GetFloat64 returns the current value as float64.
func (g *Gauge) GetFloat64() float64 {
	if g == nil {
		return 0
	}
	return float64(g.value.Load())
}

// String returns a human-readable representation.
func (g *Gauge) String() string {
	if g == nil {
		return "Gauge<nil>"
	}
	return fmt.Sprintf("Gauge{name=%s, labels=%s, value=%d}", g.name, g.labels.String(), g.Get())
}

// MarshalJSON implements json.Marshaler.
func (g *Gauge) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{
		"name":   g.name,
		"type":   g.Type(),
		"labels": g.labels,
		"value":  g.Get(),
	})
}

// DefaultHistogramBuckets are the default latency buckets in milliseconds.
// Buckets: 1ms, 5ms, 10ms, 25ms, 50ms, 100ms, 250ms, 500ms, 1s, 2.5s, 5s, 10s
var DefaultHistogramBuckets = []float64{
	0.001,  // 1ms
	0.005,  // 5ms
	0.01,   // 10ms
	0.025,  // 25ms
	0.05,   // 50ms
	0.1,    // 100ms
	0.25,   // 250ms
	0.5,    // 500ms
	1.0,    // 1s
	2.5,    // 2.5s
	5.0,    // 5s
	10.0,   // 10s
}

// Histogram is a metric that samples observations and counts them in buckets.
type Histogram struct {
	name    string
	labels  Labels
	buckets []float64
	counts  []atomic.Int64
	sum     atomic.Int64
	count   atomic.Int64
}

// NewHistogram creates a new histogram with the given name, labels, and buckets.
// If buckets is nil, DefaultHistogramBuckets is used.
func NewHistogram(name string, labels Labels, buckets []float64) *Histogram {
	if labels == nil {
		labels = Labels{}
	}
	if buckets == nil {
		buckets = DefaultHistogramBuckets
	}
	h := &Histogram{
		name:    name,
		labels:  labels,
		buckets: buckets,
		counts:  make([]atomic.Int64, len(buckets)+1), // +1 for +Inf bucket
	}
	return h
}

// Name returns the metric name.
func (h *Histogram) Name() string {
	return h.name
}

// Type returns the metric type.
func (h *Histogram) Type() MetricType {
	return MetricTypeHistogram
}

// Labels returns the metric labels.
func (h *Histogram) Labels() Labels {
	return h.labels
}

// Observe records a value in the histogram.
// Value should be in seconds (float64).
func (h *Histogram) Observe(value float64) {
	if h == nil {
		return
	}
	if value < 0 {
		value = 0
	}

	// Find the bucket and increment
	for i, bucket := range h.buckets {
		if value <= bucket {
			h.counts[i].Add(1)
			break
		}
		// If we're at the last bucket, increment the +Inf bucket
		if i == len(h.buckets)-1 {
			h.counts[len(h.buckets)].Add(1)
		}
	}

	// Update sum and count (store seconds as nanoseconds for atomic operations)
	h.sum.Add(int64(value * 1e9))
	h.count.Add(1)
}

// ObserveDuration records a duration observation.
func (h *Histogram) ObserveDuration(duration time.Duration) {
	if h == nil {
		return
	}
	h.Observe(duration.Seconds())
}

// GetCount returns the total number of observations.
func (h *Histogram) GetCount() int64 {
	if h == nil {
		return 0
	}
	return h.count.Load()
}

// GetSum returns the sum of all observed values (in seconds).
func (h *Histogram) GetSum() float64 {
	if h == nil {
		return 0
	}
	return float64(h.sum.Load()) / 1e9
}

// GetBuckets returns the bucket counts.
func (h *Histogram) GetBuckets() []int64 {
	if h == nil {
		return nil
	}
	result := make([]int64, len(h.counts))
	for i := range h.counts {
		result[i] = h.counts[i].Load()
	}
	return result
}

// Percentile returns the estimated value at the given percentile (0-1).
// Returns 0 if no observations have been recorded.
func (h *Histogram) Percentile(p float64) float64 {
	if h == nil || p < 0 || p > 1 {
		return 0
	}

	count := h.GetCount()
	if count == 0 {
		return 0
	}

	target := int64(float64(count) * p)
	if target == 0 {
		target = 1
	}

	cumulative := int64(0)
	for i := range h.buckets {
		cumulative += h.counts[i].Load()
		if cumulative >= target {
			return h.buckets[i]
		}
	}

	// Return the upper bound of the last bucket if we didn't find it
	if len(h.buckets) > 0 {
		return h.buckets[len(h.buckets)-1]
	}
	return 0
}

// P50 returns the 50th percentile (median).
func (h *Histogram) P50() float64 {
	return h.Percentile(0.5)
}

// P90 returns the 90th percentile.
func (h *Histogram) P90() float64 {
	return h.Percentile(0.9)
}

// P99 returns the 99th percentile.
func (h *Histogram) P99() float64 {
	return h.Percentile(0.99)
}

// String returns a human-readable representation.
func (h *Histogram) String() string {
	if h == nil {
		return "Histogram<nil>"
	}
	return fmt.Sprintf("Histogram{name=%s, labels=%s, count=%d, sum=%.3f, p50=%.3f, p90=%.3f, p99=%.3f}",
		h.name, h.labels.String(), h.GetCount(), h.GetSum(), h.P50(), h.P90(), h.P99())
}

// MarshalJSON implements json.Marshaler.
func (h *Histogram) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{
		"name":    h.name,
		"type":    h.Type(),
		"labels":  h.labels,
		"count":   h.GetCount(),
		"sum":     h.GetSum(),
		"buckets": h.GetBuckets(),
		"p50":     h.P50(),
		"p90":     h.P90(),
		"p99":     h.P99(),
	})
}

// Registry manages all metrics.
type Registry struct {
	mu         sync.RWMutex
	counters   map[string]*Counter
	gauges     map[string]*Gauge
	histograms map[string]*Histogram
}

// NewRegistry creates a new metric registry.
func NewRegistry() *Registry {
	return &Registry{
		counters:   make(map[string]*Counter),
		gauges:     make(map[string]*Gauge),
		histograms: make(map[string]*Histogram),
	}
}

// makeKey creates a unique key for a metric with labels.
func makeKey(name string, labels Labels) string {
	if len(labels) == 0 {
		return name
	}
	return name + "{" + labels.String() + "}"
}

// RegisterCounter registers a counter metric.
func (r *Registry) RegisterCounter(name string, labels Labels) *Counter {
	if r == nil {
		return NewCounter(name, labels)
	}
	key := makeKey(name, labels)
	r.mu.Lock()
	defer r.mu.Unlock()

	if c, ok := r.counters[key]; ok {
		return c
	}
	c := NewCounter(name, labels)
	r.counters[key] = c
	return c
}

// RegisterGauge registers a gauge metric.
func (r *Registry) RegisterGauge(name string, labels Labels) *Gauge {
	if r == nil {
		return NewGauge(name, labels)
	}
	key := makeKey(name, labels)
	r.mu.Lock()
	defer r.mu.Unlock()

	if g, ok := r.gauges[key]; ok {
		return g
	}
	g := NewGauge(name, labels)
	r.gauges[key] = g
	return g
}

// RegisterHistogram registers a histogram metric.
func (r *Registry) RegisterHistogram(name string, labels Labels, buckets []float64) *Histogram {
	if r == nil {
		return NewHistogram(name, labels, buckets)
	}
	key := makeKey(name, labels)
	r.mu.Lock()
	defer r.mu.Unlock()

	if h, ok := r.histograms[key]; ok {
		return h
	}
	h := NewHistogram(name, labels, buckets)
	r.histograms[key] = h
	return h
}

// GetCounter retrieves a counter by name and labels.
func (r *Registry) GetCounter(name string, labels Labels) (*Counter, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.counters[makeKey(name, labels)]
	return c, ok
}

// GetGauge retrieves a gauge by name and labels.
func (r *Registry) GetGauge(name string, labels Labels) (*Gauge, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	g, ok := r.gauges[makeKey(name, labels)]
	return g, ok
}

// GetHistogram retrieves a histogram by name and labels.
func (r *Registry) GetHistogram(name string, labels Labels) (*Histogram, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.histograms[makeKey(name, labels)]
	return h, ok
}

// GetAllCounters returns all registered counters.
func (r *Registry) GetAllCounters() []*Counter {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*Counter, 0, len(r.counters))
	for _, c := range r.counters {
		result = append(result, c)
	}
	return result
}

// GetAllGauges returns all registered gauges.
func (r *Registry) GetAllGauges() []*Gauge {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*Gauge, 0, len(r.gauges))
	for _, g := range r.gauges {
		result = append(result, g)
	}
	return result
}

// GetAllHistograms returns all registered histograms.
func (r *Registry) GetAllHistograms() []*Histogram {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*Histogram, 0, len(r.histograms))
	for _, h := range r.histograms {
		result = append(result, h)
	}
	return result
}

// Export exports all metrics as a map suitable for JSON serialization.
func (r *Registry) Export() map[string]any {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	export := make(map[string]any)
	export["counters"] = r.counters
	export["gauges"] = r.gauges
	export["histograms"] = r.histograms
	return export
}

// ExportJSON exports all metrics as JSON.
func (r *Registry) ExportJSON() ([]byte, error) {
	export := r.Export()
	if export == nil {
		return []byte("null"), nil
	}
	return json.MarshalIndent(export, "", "  ")
}

// WriteTo writes all metrics to the given writer, implementing io.WriterTo.
func (r *Registry) WriteTo(w io.Writer) (int64, error) {
	data, err := r.ExportJSON()
	if err != nil {
		return 0, fmt.Errorf("exporting metrics: %w", err)
	}
	n, err := w.Write(data)
	return int64(n), err
}

// DefaultRegistry is the default global registry.
var DefaultRegistry = NewRegistry()

// Exported functions for the default registry.

// RegisterCounter registers a counter in the default registry.
func RegisterCounter(name string, labels Labels) *Counter {
	return DefaultRegistry.RegisterCounter(name, labels)
}

// RegisterGauge registers a gauge in the default registry.
func RegisterGauge(name string, labels Labels) *Gauge {
	return DefaultRegistry.RegisterGauge(name, labels)
}

// RegisterHistogram registers a histogram in the default registry.
func RegisterHistogram(name string, labels Labels, buckets []float64) *Histogram {
	return DefaultRegistry.RegisterHistogram(name, labels, buckets)
}

// Predefined metric names for Buckley.
const (
	MetricToolCallsTotal         = "tool_calls_total"
	MetricToolDurationSeconds    = "tool_duration_seconds"
	MetricModelRequestsTotal     = "model_requests_total"
	MetricModelLatencySeconds    = "model_latency_seconds"
	MetricStorageOperationsTotal = "storage_operations_total"
	MetricStorageErrorsTotal     = "storage_errors_total"
	MetricActiveSessions         = "active_sessions"
	MetricMemoryUsageBytes       = "memory_usage_bytes"
)

// Helper functions for predefined metrics.

// RecordToolCall records a tool call.
func RecordToolCall(toolName string) {
	DefaultRegistry.RegisterCounter(MetricToolCallsTotal, Labels{"tool_name": toolName}).Inc()
}

// RecordToolDuration records the duration of a tool execution.
func RecordToolDuration(toolName string, duration time.Duration) {
	DefaultRegistry.RegisterHistogram(MetricToolDurationSeconds, Labels{"tool_name": toolName}, nil).ObserveDuration(duration)
}

// RecordModelRequest records a model request.
func RecordModelRequest(model string) {
	DefaultRegistry.RegisterCounter(MetricModelRequestsTotal, Labels{"model": model}).Inc()
}

// RecordModelLatency records the latency of a model request.
func RecordModelLatency(duration time.Duration) {
	DefaultRegistry.RegisterHistogram(MetricModelLatencySeconds, nil, nil).ObserveDuration(duration)
}

// RecordStorageOperation records a storage operation.
func RecordStorageOperation(operation string) {
	DefaultRegistry.RegisterCounter(MetricStorageOperationsTotal, Labels{"operation": operation}).Inc()
}

// RecordStorageError records a storage error.
func RecordStorageError(operation string) {
	DefaultRegistry.RegisterCounter(MetricStorageErrorsTotal, Labels{"operation": operation}).Inc()
}

// SetActiveSessions sets the number of active sessions.
func SetActiveSessions(count int64) {
	DefaultRegistry.RegisterGauge(MetricActiveSessions, nil).Set(count)
}

// IncActiveSessions increments the active sessions count.
func IncActiveSessions() {
	DefaultRegistry.RegisterGauge(MetricActiveSessions, nil).Inc()
}

// DecActiveSessions decrements the active sessions count.
func DecActiveSessions() {
	DefaultRegistry.RegisterGauge(MetricActiveSessions, nil).Dec()
}

// RecordMemoryStats records current memory statistics.
func RecordMemoryStats() {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	DefaultRegistry.RegisterGauge(MetricMemoryUsageBytes, nil).Set(int64(m.Alloc))
}

// Profiling support.

// cpuProfileWriter is the current CPU profile writer, if any.
var (
	cpuProfileMu     sync.Mutex
	cpuProfileWriter io.WriteCloser
)

// StartCPUProfile starts CPU profiling and writes to the given writer.
// Returns an error if profiling is already started.
func StartCPUProfile(w io.Writer) error {
	cpuProfileMu.Lock()
	defer cpuProfileMu.Unlock()

	if cpuProfileWriter != nil {
		return fmt.Errorf("cpu profiling already started")
	}

	wc, ok := w.(io.WriteCloser)
	if !ok {
		return fmt.Errorf("writer must implement io.WriteCloser")
	}

	cpuProfileWriter = wc
	if err := pprof.StartCPUProfile(w); err != nil {
		cpuProfileWriter = nil
		return fmt.Errorf("starting cpu profile: %w", err)
	}
	return nil
}

// StopCPUProfile stops the current CPU profiling.
func StopCPUProfile() {
	cpuProfileMu.Lock()
	defer cpuProfileMu.Unlock()

	if cpuProfileWriter != nil {
		pprof.StopCPUProfile()
		cpuProfileWriter.Close()
		cpuProfileWriter = nil
	}
}

// WriteHeapProfile writes the current heap profile to the given writer.
func WriteHeapProfile(w io.Writer) error {
	if err := pprof.WriteHeapProfile(w); err != nil {
		return fmt.Errorf("writing heap profile: %w", err)
	}
	return nil
}

// MemoryStats holds key memory statistics.
type MemoryStats struct {
	Alloc         uint64 `json:"alloc"`
	TotalAlloc    uint64 `json:"total_alloc"`
	Sys           uint64 `json:"sys"`
	NumGC         uint32 `json:"num_gc"`
	HeapAlloc     uint64 `json:"heap_alloc"`
	HeapSys       uint64 `json:"heap_sys"`
	HeapIdle      uint64 `json:"heap_idle"`
	HeapInuse     uint64 `json:"heap_inuse"`
	HeapReleased  uint64 `json:"heap_released"`
	HeapObjects   uint64 `json:"heap_objects"`
	StackInuse    uint64 `json:"stack_inuse"`
	StackSys      uint64 `json:"stack_sys"`
	Goroutines    int    `json:"goroutines"`
	Timestamp     int64  `json:"timestamp"`
}

// GetMemoryStats returns current memory statistics.
func GetMemoryStats() MemoryStats {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	return MemoryStats{
		Alloc:        m.Alloc,
		TotalAlloc:   m.TotalAlloc,
		Sys:          m.Sys,
		NumGC:        m.NumGC,
		HeapAlloc:    m.HeapAlloc,
		HeapSys:      m.HeapSys,
		HeapIdle:     m.HeapIdle,
		HeapInuse:    m.HeapInuse,
		HeapReleased: m.HeapReleased,
		HeapObjects:  m.HeapObjects,
		StackInuse:   m.StackInuse,
		StackSys:     m.StackSys,
		Goroutines:   runtime.NumGoroutine(),
		Timestamp:    time.Now().Unix(),
	}
}

// GetMemoryStatsJSON returns memory statistics as JSON.
func GetMemoryStatsJSON() ([]byte, error) {
	stats := GetMemoryStats()
	return json.Marshal(stats)
}

// ProfileConfig holds configuration for continuous profiling.
type ProfileConfig struct {
	MemoryInterval time.Duration // Interval for recording memory stats
}

// DefaultProfileConfig returns default profiling configuration.
func DefaultProfileConfig() *ProfileConfig {
	return &ProfileConfig{
		MemoryInterval: 30 * time.Second,
	}
}

// ProfileRecorder handles continuous profiling.
type ProfileRecorder struct {
	config *ProfileConfig
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// NewProfileRecorder creates a new profile recorder.
func NewProfileRecorder(config *ProfileConfig) *ProfileRecorder {
	if config == nil {
		config = DefaultProfileConfig()
	}
	return &ProfileRecorder{
		config: config,
		stopCh: make(chan struct{}),
	}
}

// Start begins continuous profiling.
func (pr *ProfileRecorder) Start() {
	if pr == nil {
		return
	}
	pr.wg.Add(1)
	go pr.recordLoop()
}

// Stop stops continuous profiling.
func (pr *ProfileRecorder) Stop() {
	if pr == nil {
		return
	}
	close(pr.stopCh)
	pr.wg.Wait()
}

func (pr *ProfileRecorder) recordLoop() {
	defer pr.wg.Done()

	ticker := time.NewTicker(pr.config.MemoryInterval)
	defer ticker.Stop()

	// Record immediately on start
	RecordMemoryStats()

	for {
		select {
		case <-pr.stopCh:
			return
		case <-ticker.C:
			RecordMemoryStats()
		}
	}
}

// Timer is a helper for timing operations.
type Timer struct {
	start time.Time
}

// NewTimer creates a new timer.
func NewTimer() *Timer {
	return &Timer{start: time.Now()}
}

// Start resets and starts the timer.
func (t *Timer) Start() {
	if t == nil {
		return
	}
	t.start = time.Now()
}

// Elapsed returns the elapsed time.
func (t *Timer) Elapsed() time.Duration {
	if t == nil {
		return 0
	}
	return time.Since(t.start)
}

// Observe records the elapsed time in a histogram.
func (t *Timer) Observe(h *Histogram) {
	if t == nil || h == nil {
		return
	}
	h.ObserveDuration(t.Elapsed())
}
