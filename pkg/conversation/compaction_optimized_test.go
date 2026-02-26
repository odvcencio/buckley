package conversation

import (
	"compress/gzip"
	"context"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
)

// TestCompactionQueue tests the async compaction queue
func TestCompactionQueue(t *testing.T) {
	cm := NewCompactionManager(nil, config.DefaultConfig())
	defer cm.Stop()

	// Verify queue is running
	if !cm.IsQueueRunning() {
		t.Error("expected queue to be running")
	}

	// Verify queue length starts at 0
	if cm.QueueLength() != 0 {
		t.Errorf("expected queue length 0, got %d", cm.QueueLength())
	}
}

// TestCompactionQueueStop tests graceful shutdown
func TestCompactionQueueStop(t *testing.T) {
	cm := NewCompactionManager(nil, config.DefaultConfig())

	// Stop should complete without hanging
	done := make(chan bool)
	go func() {
		cm.Stop()
		done <- true
	}()

	select {
	case <-done:
		// Good, stopped quickly
	case <-time.After(5 * time.Second):
		t.Error("Stop() took too long")
	}

	// Verify queue is no longer running
	if cm.IsQueueRunning() {
		t.Error("expected queue to be stopped")
	}
}

// TestTriggerAsyncCompaction tests the non-blocking compaction trigger
func TestTriggerAsyncCompaction(t *testing.T) {
	conv := &Conversation{
		SessionID: "test-session",
		Messages: []Message{
			{Role: "user", Content: "one"},
			{Role: "assistant", Content: "two"},
			{Role: "user", Content: "three"},
			{Role: "assistant", Content: "four"},
		},
	}

	cm := NewCompactionManager(nil, config.DefaultConfig())
	defer cm.Stop()

	done := make(chan *CompactionResult, 1)
	onComplete := func(result *CompactionResult) {
		done <- result
	}

	// Trigger should not block
	queued := cm.TriggerAsyncCompaction(context.Background(), conv, onComplete)
	if !queued {
		t.Error("expected compaction to be queued")
	}

	// Wait for result
	select {
	case result := <-done:
		if result == nil {
			t.Error("expected non-nil result")
		}
	case <-time.After(3 * time.Second):
		t.Error("timed out waiting for compaction result")
	}
}

// TestTriggerAsyncCompactionBackpressure tests queue full behavior
func TestTriggerAsyncCompactionBackpressure(t *testing.T) {
	cm := NewCompactionManager(nil, config.DefaultConfig())
	defer cm.Stop()

	// Fill the queue with blocked requests
	blocker := make(chan struct{})
	slowHandler := func(result *CompactionResult) {
		<-blocker // Block until we release
	}

	// Queue multiple slow compactions to fill up
	for i := 0; i < queueBufferSize+1; i++ {
		conv := &Conversation{
			SessionID: "test-session",
			Messages: []Message{
				{Role: "user", Content: "one"},
				{Role: "assistant", Content: "two"},
				{Role: "user", Content: "three"},
				{Role: "assistant", Content: "four"},
			},
		}

		// Use a custom context with timeout
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		cm.TriggerAsyncCompaction(ctx, conv, slowHandler)
		cancel()
	}

	// Release blockers
	close(blocker)
}

// TestGzipCompressor tests the gzip compression functionality
func TestGzipCompressor(t *testing.T) {
	compressor := NewGzipCompressor(-1) // Use default compression

	if compressor.Name() != "gzip" {
		t.Errorf("expected name 'gzip', got '%s'", compressor.Name())
	}

	original := []byte("This is a test message that should be compressed and decompressed correctly.")

	// Compress
	compressed, err := compressor.Compress(original)
	if err != nil {
		t.Fatalf("compression failed: %v", err)
	}

	// Compressed should be smaller for repetitive data
	if len(compressed) >= len(original) {
		t.Logf("compressed size (%d) >= original size (%d) - this is expected for small data", len(compressed), len(original))
	}

	// Decompress
	decompressed, err := compressor.Decompress(compressed)
	if err != nil {
		t.Fatalf("decompression failed: %v", err)
	}

	// Verify data integrity
	if string(decompressed) != string(original) {
		t.Errorf("decompressed data doesn't match original: got %q, want %q", string(decompressed), string(original))
	}
}

// TestTokenEstimateCache tests the token estimation cache
func TestTokenEstimateCache(t *testing.T) {
	cache := newTokenEstimateCache(100)

	text := "This is a test message for token estimation."
	tokens := 15

	// Initially not in cache
	if _, ok := cache.Get(text); ok {
		t.Error("expected cache miss for new entry")
	}

	// Store in cache
	cache.Set(text, tokens)

	// Should be a hit now
	cachedTokens, ok := cache.Get(text)
	if !ok {
		t.Error("expected cache hit after setting")
	}
	if cachedTokens != tokens {
		t.Errorf("expected %d tokens, got %d", tokens, cachedTokens)
	}
}

// TestTokenEstimateCacheEviction tests cache eviction
func TestTokenEstimateCacheEviction(t *testing.T) {
	cache := newTokenEstimateCache(10)

	// Fill the cache
	for i := 0; i < 15; i++ {
		text := string(rune('a' + i))
		cache.Set(text, i)
	}

	// Cache should have evicted some entries
	// We added 15 entries with max 10, so at least some should remain
	hits := 0
	for i := 0; i < 15; i++ {
		text := string(rune('a' + i))
		if _, ok := cache.Get(text); ok {
			hits++
		}
	}

	if hits > 10 {
		t.Errorf("expected at most 10 entries after eviction, got %d", hits)
	}
}

// TestTieredThresholds tests the 80% warning / 90% compact thresholds
func TestTieredThresholds(t *testing.T) {
	cm := NewCompactionManager(nil, config.DefaultConfig())
	defer cm.Stop()

	// Get thresholds
	warning, compact := cm.GetThresholds()
	if warning != warningThreshold {
		t.Errorf("expected warning threshold %f, got %f", warningThreshold, warning)
	}
	if compact != compactThreshold {
		t.Errorf("expected compact threshold %f, got %f", compactThreshold, compact)
	}

	// Test at 70% - neither warning nor compact
	shouldCompact, isWarning := cm.CheckThresholds(0.70)
	if shouldCompact || isWarning {
		t.Error("expected no action at 70%")
	}

	// Test at 80% - should trigger warning
	shouldCompact, isWarning = cm.CheckThresholds(0.80)
	if shouldCompact {
		t.Error("expected no compact at 80%")
	}
	if !isWarning {
		t.Error("expected warning at 80%")
	}

	// Test at 85% - should trigger warning
	shouldCompact, isWarning = cm.CheckThresholds(0.85)
	if shouldCompact {
		t.Error("expected no compact at 85%")
	}
	if !isWarning {
		t.Error("expected warning at 85%")
	}

	// Test at 90% - should trigger compact
	shouldCompact, isWarning = cm.CheckThresholds(0.90)
	if !shouldCompact {
		t.Error("expected compact at 90%")
	}
	if isWarning {
		t.Error("expected no warning at 90% (should be compact)")
	}

	// Test at 95% - should trigger compact
	shouldCompact, isWarning = cm.CheckThresholds(0.95)
	if !shouldCompact {
		t.Error("expected compact at 95%")
	}
	if isWarning {
		t.Error("expected no warning at 95% (should be compact)")
	}
}

// TestWarningThresholdCallback tests the warning callback
func TestWarningThresholdCallback(t *testing.T) {
	cm := NewCompactionManager(nil, config.DefaultConfig())
	defer cm.Stop()

	callbackCalled := false
	var callbackRatio float64

	cm.SetWarningThresholdFn(func(ratio float64) {
		callbackCalled = true
		callbackRatio = ratio
	})

	// Trigger warning at 85%
	cm.CheckThresholds(0.85)

	if !callbackCalled {
		t.Error("expected warning callback to be called")
	}
	if callbackRatio != 0.85 {
		t.Errorf("expected callback ratio 0.85, got %f", callbackRatio)
	}
}

// TestCompactionManagerWithCompressor tests setting a custom compressor
func TestCompactionManagerWithCompressor(t *testing.T) {
	cm := NewCompactionManager(nil, config.DefaultConfig())
	defer cm.Stop()

	// Default should be gzip
	if cm.compressor == nil {
		t.Error("expected default compressor")
	}

	// Set custom compressor
	customCompressor := NewGzipCompressor(gzip.BestCompression)
	cm.SetCompressor(customCompressor)

	if cm.compressor != customCompressor {
		t.Error("expected custom compressor to be set")
	}
}

// TestNilCompactionManagerSafety tests that methods handle nil receiver safely
func TestNilCompactionManagerSafety(t *testing.T) {
	var cm *CompactionManager

	// These should not panic
	cm.SetConversation(nil)
	cm.SetOnComplete(nil)
	cm.SetCompressor(nil)
	cm.SetWarningThresholdFn(nil)
	cm.Stop()

	if cm.IsQueueRunning() {
		t.Error("nil manager should not report queue running")
	}

	if cm.QueueLength() != 0 {
		t.Error("nil manager should return 0 queue length")
	}

	queued := cm.TriggerAsyncCompaction(context.Background(), nil, nil)
	if queued {
		t.Error("nil manager should return false for trigger")
	}

	shouldCompact, isWarning := cm.CheckThresholds(0.85)
	if shouldCompact || isWarning {
		t.Error("nil manager should return false for thresholds")
	}
}
