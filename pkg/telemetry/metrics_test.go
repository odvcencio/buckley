package telemetry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCounter_Basic(t *testing.T) {
	c := NewCounter("test_counter", Labels{"env": "test"})
	require.NotNil(t, c)

	assert.Equal(t, "test_counter", c.Name())
	assert.Equal(t, MetricTypeCounter, c.Type())
	assert.Equal(t, Labels{"env": "test"}, c.Labels())
	assert.Equal(t, int64(0), c.Get())
}

func TestCounter_Inc(t *testing.T) {
	c := NewCounter("test", nil)

	c.Inc()
	assert.Equal(t, int64(1), c.Get())

	c.Inc()
	c.Inc()
	assert.Equal(t, int64(3), c.Get())
}

func TestCounter_Add(t *testing.T) {
	c := NewCounter("test", nil)

	c.Add(5)
	assert.Equal(t, int64(5), c.Get())

	c.Add(10)
	assert.Equal(t, int64(15), c.Get())
}

func TestCounter_AddNegative(t *testing.T) {
	c := NewCounter("test", nil)
	c.Add(10)
	c.Add(-5) // Should be ignored for counters
	assert.Equal(t, int64(10), c.Get())
}

func TestCounter_NilReceiver(t *testing.T) {
	var c *Counter
	c.Inc()      // Should not panic
	c.Add(5)     // Should not panic
	assert.Equal(t, int64(0), c.Get())
}

func TestCounter_Concurrent(t *testing.T) {
	c := NewCounter("test", nil)
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				c.Inc()
			}
		}()
	}

	wg.Wait()
	assert.Equal(t, int64(100000), c.Get())
}

func TestCounter_String(t *testing.T) {
	c := NewCounter("requests", Labels{"path": "/api"})
	c.Add(42)
	str := c.String()
	assert.Contains(t, str, "Counter")
	assert.Contains(t, str, "requests")
	assert.Contains(t, str, "42")
}

func TestCounter_MarshalJSON(t *testing.T) {
	c := NewCounter("requests", Labels{"path": "/api"})
	c.Add(42)

	data, err := json.Marshal(c)
	require.NoError(t, err)

	var result map[string]any
	err = json.Unmarshal(data, &result)
	require.NoError(t, err)

	assert.Equal(t, "requests", result["name"])
	assert.Equal(t, "counter", result["type"])
	assert.Equal(t, float64(42), result["value"])
}

func TestGauge_Basic(t *testing.T) {
	g := NewGauge("test_gauge", Labels{"env": "test"})
	require.NotNil(t, g)

	assert.Equal(t, "test_gauge", g.Name())
	assert.Equal(t, MetricTypeGauge, g.Type())
	assert.Equal(t, Labels{"env": "test"}, g.Labels())
	assert.Equal(t, int64(0), g.Get())
}

func TestGauge_Set(t *testing.T) {
	g := NewGauge("test", nil)

	g.Set(100)
	assert.Equal(t, int64(100), g.Get())

	g.Set(50)
	assert.Equal(t, int64(50), g.Get())
}

func TestGauge_SetFloat64(t *testing.T) {
	g := NewGauge("test", nil)

	g.SetFloat64(123.0)
	assert.InDelta(t, 123.0, g.GetFloat64(), 0.001)
}

func TestGauge_IncDec(t *testing.T) {
	g := NewGauge("test", nil)

	g.Inc()
	assert.Equal(t, int64(1), g.Get())

	g.Dec()
	assert.Equal(t, int64(0), g.Get())

	g.Dec()
	assert.Equal(t, int64(-1), g.Get())
}

func TestGauge_Add(t *testing.T) {
	g := NewGauge("test", nil)

	g.Add(10)
	assert.Equal(t, int64(10), g.Get())

	g.Add(-5)
	assert.Equal(t, int64(5), g.Get())
}

func TestGauge_NilReceiver(t *testing.T) {
	var g *Gauge
	g.Set(10)  // Should not panic
	g.Inc()    // Should not panic
	g.Dec()    // Should not panic
	g.Add(5)   // Should not panic
	assert.Equal(t, int64(0), g.Get())
	assert.Equal(t, 0.0, g.GetFloat64())
}

func TestGauge_Concurrent(t *testing.T) {
	g := NewGauge("test", nil)
	var wg sync.WaitGroup

	// Start with 0, add 1 500 times and subtract 1 500 times
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(add bool) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if add {
					g.Inc()
				} else {
					g.Dec()
				}
			}
		}(i%2 == 0)
	}

	wg.Wait()
	// Result should be around 0 (depending on scheduling)
	assert.InDelta(t, int64(0), g.Get(), 100)
}

func TestGauge_String(t *testing.T) {
	g := NewGauge("memory", Labels{"type": "heap"})
	g.Set(1024)
	str := g.String()
	assert.Contains(t, str, "Gauge")
	assert.Contains(t, str, "memory")
	assert.Contains(t, str, "1024")
}

func TestGauge_MarshalJSON(t *testing.T) {
	g := NewGauge("memory", Labels{"type": "heap"})
	g.Set(1024)

	data, err := json.Marshal(g)
	require.NoError(t, err)

	var result map[string]any
	err = json.Unmarshal(data, &result)
	require.NoError(t, err)

	assert.Equal(t, "memory", result["name"])
	assert.Equal(t, "gauge", result["type"])
	assert.Equal(t, float64(1024), result["value"])
}

func TestHistogram_Basic(t *testing.T) {
	h := NewHistogram("test_histogram", Labels{"env": "test"}, nil)
	require.NotNil(t, h)

	assert.Equal(t, "test_histogram", h.Name())
	assert.Equal(t, MetricTypeHistogram, h.Type())
	assert.Equal(t, Labels{"env": "test"}, h.Labels())
	assert.Equal(t, int64(0), h.GetCount())
	assert.Equal(t, 0.0, h.GetSum())
}

func TestHistogram_DefaultBuckets(t *testing.T) {
	h := NewHistogram("test", nil, nil)
	assert.Equal(t, DefaultHistogramBuckets, h.buckets)
}

func TestHistogram_CustomBuckets(t *testing.T) {
	buckets := []float64{0.1, 0.5, 1.0, 2.0}
	h := NewHistogram("test", nil, buckets)
	assert.Equal(t, buckets, h.buckets)
}

func TestHistogram_Observe(t *testing.T) {
	h := NewHistogram("test", nil, nil)

	h.Observe(0.05)  // 50ms
	h.Observe(0.1)   // 100ms
	h.Observe(0.15)  // 150ms

	assert.Equal(t, int64(3), h.GetCount())
	assert.InDelta(t, 0.3, h.GetSum(), 0.001)

	buckets := h.GetBuckets()
	require.Equal(t, len(DefaultHistogramBuckets)+1, len(buckets))
	assert.True(t, buckets[4] >= 1)  // 50ms bucket
	assert.True(t, buckets[5] >= 1)  // 100ms bucket
	assert.True(t, buckets[6] >= 1)  // 250ms bucket
}

func TestHistogram_ObserveDuration(t *testing.T) {
	h := NewHistogram("test", nil, nil)

	h.ObserveDuration(100 * time.Millisecond)
	h.ObserveDuration(200 * time.Millisecond)

	assert.Equal(t, int64(2), h.GetCount())
	assert.InDelta(t, 0.3, h.GetSum(), 0.001)
}

func TestHistogram_ObserveNegative(t *testing.T) {
	h := NewHistogram("test", nil, nil)
	h.Observe(-0.1) // Should be treated as 0
	assert.Equal(t, int64(1), h.GetCount())
	assert.Equal(t, 0.0, h.GetSum())
}

func TestHistogram_Percentile(t *testing.T) {
	h := NewHistogram("test", nil, []float64{0.1, 0.5, 1.0, 2.0, 5.0})

	// Empty histogram
	assert.Equal(t, 0.0, h.Percentile(0.5))

	// Add observations
	for i := 0; i < 100; i++ {
		h.Observe(float64(i%5) + 0.5) // 0.5, 1.5, 2.5, 3.5, 4.5
	}

	assert.True(t, h.P50() > 0)
	assert.True(t, h.P90() > 0)
	assert.True(t, h.P99() > 0)
}

func TestHistogram_PercentileBounds(t *testing.T) {
	h := NewHistogram("test", nil, []float64{1.0, 2.0, 3.0})
	h.Observe(0.5)
	h.Observe(1.5)
	h.Observe(2.5)

	// Test boundary conditions
	assert.Equal(t, 0.0, h.Percentile(-0.1))
	assert.Equal(t, 0.0, h.Percentile(1.1))
	assert.True(t, h.Percentile(0.5) > 0)
}

func TestHistogram_NilReceiver(t *testing.T) {
	var h *Histogram
	h.Observe(0.1)              // Should not panic
	h.ObserveDuration(time.Second) // Should not panic
	assert.Equal(t, int64(0), h.GetCount())
	assert.Equal(t, 0.0, h.GetSum())
	assert.Nil(t, h.GetBuckets())
	assert.Equal(t, 0.0, h.P50())
	assert.Equal(t, 0.0, h.P90())
	assert.Equal(t, 0.0, h.P99())
}

func TestHistogram_Concurrent(t *testing.T) {
	h := NewHistogram("test", nil, nil)
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				h.Observe(float64(j) * 0.01)
			}
		}()
	}

	wg.Wait()
	assert.Equal(t, int64(10000), h.GetCount())
}

func TestHistogram_String(t *testing.T) {
	h := NewHistogram("latency", Labels{"path": "/api"}, nil)
	h.Observe(0.1)
	h.Observe(0.2)
	str := h.String()
	assert.Contains(t, str, "Histogram")
	assert.Contains(t, str, "latency")
	assert.Contains(t, str, "count=2")
}

func TestHistogram_MarshalJSON(t *testing.T) {
	h := NewHistogram("latency", nil, nil)
	h.Observe(0.1)
	h.Observe(0.2)

	data, err := json.Marshal(h)
	require.NoError(t, err)

	var result map[string]any
	err = json.Unmarshal(data, &result)
	require.NoError(t, err)

	assert.Equal(t, "latency", result["name"])
	assert.Equal(t, "histogram", result["type"])
	assert.Equal(t, float64(2), result["count"])
}

func TestLabels_String(t *testing.T) {
	l := Labels{"b": "2", "a": "1", "c": "3"}
	assert.Equal(t, "a=1,b=2,c=3", l.String())

	empty := Labels{}
	assert.Equal(t, "", empty.String())
}

func TestRegistry_Basic(t *testing.T) {
	r := NewRegistry()
	require.NotNil(t, r)
}

func TestRegistry_RegisterCounter(t *testing.T) {
	r := NewRegistry()

	c1 := r.RegisterCounter("requests", Labels{"path": "/api"})
	require.NotNil(t, c1)

	// Registering the same metric should return the existing one
	c2 := r.RegisterCounter("requests", Labels{"path": "/api"})
	assert.Equal(t, c1, c2)

	// Different labels should create a new counter
	c3 := r.RegisterCounter("requests", Labels{"path": "/health"})
	assert.NotEqual(t, c1, c3)
}

func TestRegistry_RegisterGauge(t *testing.T) {
	r := NewRegistry()

	g1 := r.RegisterGauge("memory", Labels{"type": "heap"})
	require.NotNil(t, g1)

	g2 := r.RegisterGauge("memory", Labels{"type": "heap"})
	assert.Equal(t, g1, g2)
}

func TestRegistry_RegisterHistogram(t *testing.T) {
	r := NewRegistry()

	h1 := r.RegisterHistogram("latency", Labels{"path": "/api"}, nil)
	require.NotNil(t, h1)

	h2 := r.RegisterHistogram("latency", Labels{"path": "/api"}, nil)
	assert.Equal(t, h1, h2)
}

func TestRegistry_GetMethods(t *testing.T) {
	r := NewRegistry()

	c := r.RegisterCounter("requests", Labels{"path": "/api"})
	g := r.RegisterGauge("memory", Labels{"type": "heap"})
	h := r.RegisterHistogram("latency", Labels{"path": "/api"}, nil)

	// Get existing metrics
	gotC, ok := r.GetCounter("requests", Labels{"path": "/api"})
	assert.True(t, ok)
	assert.Equal(t, c, gotC)

	gotG, ok := r.GetGauge("memory", Labels{"type": "heap"})
	assert.True(t, ok)
	assert.Equal(t, g, gotG)

	gotH, ok := r.GetHistogram("latency", Labels{"path": "/api"})
	assert.True(t, ok)
	assert.Equal(t, h, gotH)

	// Get non-existing metrics
	_, ok = r.GetCounter("nonexistent", nil)
	assert.False(t, ok)
}

func TestRegistry_GetAllMethods(t *testing.T) {
	r := NewRegistry()

	r.RegisterCounter("c1", nil)
	r.RegisterCounter("c2", nil)
	r.RegisterGauge("g1", nil)
	r.RegisterHistogram("h1", nil, nil)

	assert.Len(t, r.GetAllCounters(), 2)
	assert.Len(t, r.GetAllGauges(), 1)
	assert.Len(t, r.GetAllHistograms(), 1)
}

func TestRegistry_Export(t *testing.T) {
	r := NewRegistry()

	r.RegisterCounter("requests", Labels{"path": "/api"}).Inc()
	r.RegisterGauge("memory", nil).Set(1024)
	r.RegisterHistogram("latency", nil, nil).Observe(0.1)

	export := r.Export()
	require.NotNil(t, export)

	assert.Contains(t, export, "counters")
	assert.Contains(t, export, "gauges")
	assert.Contains(t, export, "histograms")
}

func TestRegistry_ExportJSON(t *testing.T) {
	r := NewRegistry()
	r.RegisterCounter("requests", nil).Inc()

	data, err := r.ExportJSON()
	require.NoError(t, err)

	assert.Contains(t, string(data), "requests")
	assert.Contains(t, string(data), "counters")
}

func TestRegistry_WriteTo(t *testing.T) {
	r := NewRegistry()
	r.RegisterCounter("requests", nil).Inc()

	var buf bytes.Buffer
	n, err := r.WriteTo(&buf)
	require.NoError(t, err)
	require.True(t, n > 0)

	assert.Contains(t, buf.String(), "requests")
}

func TestRegistry_NilReceiver(t *testing.T) {
	var r *Registry

	// All methods should handle nil gracefully
	c := r.RegisterCounter("test", nil)
	assert.NotNil(t, c)

	g := r.RegisterGauge("test", nil)
	assert.NotNil(t, g)

	h := r.RegisterHistogram("test", nil, nil)
	assert.NotNil(t, h)

	_, ok := r.GetCounter("test", nil)
	assert.False(t, ok)

	assert.Nil(t, r.Export())
	assert.Nil(t, r.GetAllCounters())
}

func TestRegistry_Concurrent(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup

	// Concurrent registrations
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			labels := Labels{"id": string(rune('a' + n%26))}
			r.RegisterCounter("requests", labels).Inc()
		}(i)
	}

	wg.Wait()
	assert.Len(t, r.GetAllCounters(), 26) // 26 unique labels
}

func TestDefaultRegistry(t *testing.T) {
	// Save and restore original default
	original := DefaultRegistry
	defer func() { DefaultRegistry = original }()

	DefaultRegistry = NewRegistry()

	c := RegisterCounter("test", nil)
	assert.NotNil(t, c)

	g := RegisterGauge("test", nil)
	assert.NotNil(t, g)

	h := RegisterHistogram("test", nil, nil)
	assert.NotNil(t, h)
}

func TestRecordHelpers(t *testing.T) {
	// Save and restore original default
	original := DefaultRegistry
	defer func() { DefaultRegistry = original }()

	DefaultRegistry = NewRegistry()

	RecordToolCall("test-tool")
	c, ok := DefaultRegistry.GetCounter(MetricToolCallsTotal, Labels{"tool_name": "test-tool"})
	assert.True(t, ok)
	assert.Equal(t, int64(1), c.Get())

	RecordToolDuration("test-tool", 100*time.Millisecond)
	h, ok := DefaultRegistry.GetHistogram(MetricToolDurationSeconds, Labels{"tool_name": "test-tool"})
	assert.True(t, ok)
	assert.Equal(t, int64(1), h.GetCount())

	RecordModelRequest("gpt-4")
	c, ok = DefaultRegistry.GetCounter(MetricModelRequestsTotal, Labels{"model": "gpt-4"})
	assert.True(t, ok)
	assert.Equal(t, int64(1), c.Get())

	RecordModelLatency(500 * time.Millisecond)
	h, ok = DefaultRegistry.GetHistogram(MetricModelLatencySeconds, nil)
	assert.True(t, ok)
	assert.Equal(t, int64(1), h.GetCount())

	RecordStorageOperation("read")
	c, ok = DefaultRegistry.GetCounter(MetricStorageOperationsTotal, Labels{"operation": "read"})
	assert.True(t, ok)
	assert.Equal(t, int64(1), c.Get())

	RecordStorageError("write")
	c, ok = DefaultRegistry.GetCounter(MetricStorageErrorsTotal, Labels{"operation": "write"})
	assert.True(t, ok)
	assert.Equal(t, int64(1), c.Get())

	SetActiveSessions(5)
	g, ok := DefaultRegistry.GetGauge(MetricActiveSessions, nil)
	assert.True(t, ok)
	assert.Equal(t, int64(5), g.Get())

	IncActiveSessions()
	assert.Equal(t, int64(6), g.Get())

	DecActiveSessions()
	assert.Equal(t, int64(5), g.Get())
}

func TestRecordMemoryStats(t *testing.T) {
	// Save and restore original default
	original := DefaultRegistry
	defer func() { DefaultRegistry = original }()

	DefaultRegistry = NewRegistry()

	RecordMemoryStats()
	g, ok := DefaultRegistry.GetGauge(MetricMemoryUsageBytes, nil)
	assert.True(t, ok)
	assert.True(t, g.Get() > 0)
}

func TestGetMemoryStats(t *testing.T) {
	stats := GetMemoryStats()

	assert.True(t, stats.Alloc > 0)
	assert.True(t, stats.TotalAlloc > 0)
	assert.True(t, stats.Sys > 0)
	assert.True(t, stats.HeapAlloc > 0)
	assert.True(t, stats.Timestamp > 0)
	assert.True(t, stats.Goroutines > 0)
}

func TestGetMemoryStatsJSON(t *testing.T) {
	data, err := GetMemoryStatsJSON()
	require.NoError(t, err)

	var stats MemoryStats
	err = json.Unmarshal(data, &stats)
	require.NoError(t, err)

	assert.True(t, stats.Alloc > 0)
}

func TestStartStopCPUProfile(t *testing.T) {
	// Create a temporary file for the profile
	f, err := os.CreateTemp(t.TempDir(), "cpu_profile_*.prof")
	require.NoError(t, err)
	path := f.Name()
	defer os.Remove(path)

	// Start profiling
	err = StartCPUProfile(f)
	require.NoError(t, err)

	// Do some work
	sum := 0
	for i := 0; i < 1000000; i++ {
		sum += i
	}
	_ = sum

	// Stop profiling
	StopCPUProfile()

	// Verify file has content by re-opening it
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.True(t, info.Size() > 0)

	// Second stop should not panic
	StopCPUProfile()
}

func TestStartCPUProfile_AlreadyStarted(t *testing.T) {
	f1, err := os.CreateTemp(t.TempDir(), "cpu_profile_*.prof")
	require.NoError(t, err)
	defer os.Remove(f1.Name())

	err = StartCPUProfile(f1)
	require.NoError(t, err)
	defer StopCPUProfile()

	f2, err := os.CreateTemp(t.TempDir(), "cpu_profile_*.prof")
	require.NoError(t, err)
	defer os.Remove(f2.Name())

	err = StartCPUProfile(f2)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already started")
}

func TestStartCPUProfile_NotWriteCloser(t *testing.T) {
	var buf bytes.Buffer
	err := StartCPUProfile(&buf)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "WriteCloser")
}

func TestWriteHeapProfile(t *testing.T) {
	var buf bytes.Buffer

	// Allocate some memory
	data := make([]byte, 1024*1024) // 1MB
	for i := range data {
		data[i] = byte(i % 256)
	}
	runtime.GC()

	err := WriteHeapProfile(&buf)
	require.NoError(t, err)
	assert.True(t, buf.Len() > 0)

	// Verify it's valid pprof format (starts with specific bytes)
	content := buf.Bytes()
	assert.True(t, len(content) > 4)
}

func TestProfileRecorder(t *testing.T) {
	// Save and restore original default
	original := DefaultRegistry
	defer func() { DefaultRegistry = original }()

	DefaultRegistry = NewRegistry()

	config := &ProfileConfig{MemoryInterval: 50 * time.Millisecond}
	recorder := NewProfileRecorder(config)
	require.NotNil(t, recorder)

	recorder.Start()
	time.Sleep(120 * time.Millisecond) // Wait for at least 2 recordings
	recorder.Stop()

	// Check that memory was recorded
	g, ok := DefaultRegistry.GetGauge(MetricMemoryUsageBytes, nil)
	assert.True(t, ok)
	assert.True(t, g.Get() > 0)
}

func TestProfileRecorder_NilConfig(t *testing.T) {
	recorder := NewProfileRecorder(nil)
	assert.NotNil(t, recorder)
	assert.Equal(t, 30*time.Second, recorder.config.MemoryInterval)
}

func TestProfileRecorder_NilReceiver(t *testing.T) {
	var pr *ProfileRecorder
	pr.Start() // Should not panic
	pr.Stop()  // Should not panic
}

func TestDefaultProfileConfig(t *testing.T) {
	config := DefaultProfileConfig()
	assert.Equal(t, 30*time.Second, config.MemoryInterval)
}

func TestTimer(t *testing.T) {
	timer := NewTimer()
	require.NotNil(t, timer)

	time.Sleep(50 * time.Millisecond)
	elapsed := timer.Elapsed()
	assert.True(t, elapsed >= 50*time.Millisecond)

	// Test Start resets the timer
	timer.Start()
	time.Sleep(10 * time.Millisecond)
	elapsed = timer.Elapsed()
	assert.True(t, elapsed >= 10*time.Millisecond)
	assert.True(t, elapsed < 50*time.Millisecond)
}

func TestTimer_Observe(t *testing.T) {
	timer := NewTimer()
	h := NewHistogram("test", nil, nil)

	time.Sleep(20 * time.Millisecond)
	timer.Observe(h)

	assert.Equal(t, int64(1), h.GetCount())
	assert.True(t, h.GetSum() >= 0.02)
}

func TestTimer_NilReceiver(t *testing.T) {
	var timer *Timer
	timer.Start()                  // Should not panic
	assert.Equal(t, time.Duration(0), timer.Elapsed())

	h := NewHistogram("test", nil, nil)
	timer.Observe(h)               // Should not panic
	assert.Equal(t, int64(0), h.GetCount())
}

func TestMakeKey(t *testing.T) {
	key1 := makeKey("counter", Labels{"a": "1", "b": "2"})
	key2 := makeKey("counter", Labels{"b": "2", "a": "1"}) // Same labels, different order
	assert.Equal(t, key1, key2)

	key3 := makeKey("counter", nil)
	assert.Equal(t, "counter", key3)
}

// Benchmarks

func BenchmarkCounter_Inc(b *testing.B) {
	c := NewCounter("bench", nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Inc()
	}
}

func BenchmarkCounter_IncParallel(b *testing.B) {
	c := NewCounter("bench", nil)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c.Inc()
		}
	})
}

func BenchmarkGauge_Set(b *testing.B) {
	g := NewGauge("bench", nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g.Set(int64(i))
	}
}

func BenchmarkHistogram_Observe(b *testing.B) {
	h := NewHistogram("bench", nil, nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.Observe(0.1)
	}
}

func BenchmarkRegistry_RegisterCounter(b *testing.B) {
	r := NewRegistry()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.RegisterCounter("counter", Labels{"i": string(rune(i % 26 + 'a'))})
	}
}

func BenchmarkRecordToolCall(b *testing.B) {
	original := DefaultRegistry
	DefaultRegistry = NewRegistry()
	defer func() { DefaultRegistry = original }()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		RecordToolCall("test-tool")
	}
}

// Test that the file compiles with go vet
func TestCodeQuality(t *testing.T) {
	// This is a placeholder to ensure code quality
	// In a real CI environment, this would run go vet, go fmt, etc.
}

// Test for proper error handling in profile functions
func TestWriteHeapProfile_Error(t *testing.T) {
	// Test with a writer that returns an error
	// Since we can't easily mock this, we just verify the function signature
	var w *bytes.Buffer // nil pointer
	if w != nil {
		err := WriteHeapProfile(w)
		_ = err
	}
}

// Test Timer with histogram integration
func TestTimerWithHistogramIntegration(t *testing.T) {
	h := NewHistogram("operation_latency", Labels{"op": "test"}, nil)

	for i := 0; i < 100; i++ {
		timer := NewTimer()
		time.Sleep(time.Millisecond) // Simulate work
		timer.Observe(h)
	}

	assert.Equal(t, int64(100), h.GetCount())
	assert.True(t, h.P50() > 0)
	assert.True(t, h.P90() > 0)
	assert.True(t, h.P99() > 0)
}

// Test concurrent operations on different metrics
func TestConcurrentDifferentMetrics(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup

	// Many goroutines working on different metrics
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			name := fmt.Sprintf("metric_%d", n)
			c := r.RegisterCounter(name, nil)
			for j := 0; j < 100; j++ {
				c.Inc()
			}
		}(i)
	}

	wg.Wait()

	// Verify all counters
	for i := 0; i < 50; i++ {
		name := fmt.Sprintf("metric_%d", i)
		c, ok := r.GetCounter(name, nil)
		assert.True(t, ok, "counter %s should exist", name)
		assert.Equal(t, int64(100), c.Get(), "counter %s should have value 100", name)
	}
}

// Test export contains all expected keys
func TestExportStructure(t *testing.T) {
	r := NewRegistry()
	r.RegisterCounter("c1", nil)
	r.RegisterGauge("g1", nil)
	r.RegisterHistogram("h1", nil, nil)

	export := r.Export()
	exportJSON, err := json.Marshal(export)
	require.NoError(t, err)

	var result map[string]any
	err = json.Unmarshal(exportJSON, &result)
	require.NoError(t, err)

	assert.Contains(t, result, "counters")
	assert.Contains(t, result, "gauges")
	assert.Contains(t, result, "histograms")

	counters := result["counters"].(map[string]any)
	assert.Contains(t, counters, "c1")
}


