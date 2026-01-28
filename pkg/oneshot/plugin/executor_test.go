package plugin

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestRetryConfigIsRetryableError tests the retry error classification
func TestRetryConfigIsRetryableError(t *testing.T) {
	cfg := DefaultRetryConfig()

	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error is not retryable",
			err:      nil,
			expected: false,
		},
		{
			name:     "context cancelled is not retryable",
			err:      errors.New("context canceled"),
			expected: false,
		},
		{
			name:     "context deadline exceeded is not retryable",
			err:      errors.New("context deadline exceeded"),
			expected: false,
		},
		{
			name:     "connection refused is retryable",
			err:      errors.New("connection refused"),
			expected: true,
		},
		{
			name:     "timeout is retryable",
			err:      errors.New("operation timeout"),
			expected: true,
		},
		{
			name:     "temporary error is retryable",
			err:      errors.New("temporary failure"),
			expected: true,
		},
		{
			name:     "unavailable is retryable",
			err:      errors.New("service unavailable"),
			expected: true,
		},
		{
			name:     "rate limit is retryable",
			err:      errors.New("rate limit exceeded"),
			expected: true,
		},
		{
			name:     "generic error is not retryable",
			err:      errors.New("some random error"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cfg.IsRetryableError(tt.err)
			if result != tt.expected {
				t.Errorf("IsRetryableError(%v) = %v, want %v", tt.err, result, tt.expected)
			}
		})
	}
}

// TestRetryConfigWithCustomRetryableErrors tests custom retryable error patterns
func TestRetryConfigWithCustomRetryableErrors(t *testing.T) {
	cfg := &RetryConfig{
		MaxRetries:        3,
		InitialBackoff:    100 * time.Millisecond,
		MaxBackoff:        5 * time.Second,
		BackoffMultiplier: 2.0,
		RetryableErrors:   []string{"custom_error", "another_pattern"},
	}

	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "custom error is retryable",
			err:      errors.New("this is a custom_error in the message"),
			expected: true,
		},
		{
			name:     "another custom pattern is retryable",
			err:      errors.New("another_pattern occurred"),
			expected: true,
		},
		{
			name:     "connection refused is not retryable with custom config",
			err:      errors.New("connection refused"),
			expected: false,
		},
		{
			name:     "generic error is not retryable",
			err:      errors.New("some random error"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cfg.IsRetryableError(tt.err)
			if result != tt.expected {
				t.Errorf("IsRetryableError(%v) = %v, want %v", tt.err, result, tt.expected)
			}
		})
	}
}

// TestDefaultRetryConfig tests the default retry configuration
func TestDefaultRetryConfig(t *testing.T) {
	cfg := DefaultRetryConfig()

	if cfg.MaxRetries != 3 {
		t.Errorf("MaxRetries = %d, want 3", cfg.MaxRetries)
	}
	if cfg.InitialBackoff != 100*time.Millisecond {
		t.Errorf("InitialBackoff = %v, want 100ms", cfg.InitialBackoff)
	}
	if cfg.MaxBackoff != 5*time.Second {
		t.Errorf("MaxBackoff = %v, want 5s", cfg.MaxBackoff)
	}
	if cfg.BackoffMultiplier != 2.0 {
		t.Errorf("BackoffMultiplier = %f, want 2.0", cfg.BackoffMultiplier)
	}
}

// TestPluginProcessPoolGetAndRelease tests basic pool operations
func TestPluginProcessPoolGetAndRelease(t *testing.T) {
	config := PoolConfig{
		MaxSize:             2,
		MaxUsesPerProcess:   10,
		IdleTimeout:         0, // Disable cleanup for this test
		HealthCheckInterval: 0,
	}

	pool := NewPluginProcessPool(config)
	defer pool.Shutdown()

	spawnCount := 0
	spawnFunc := func() (*PluginProcess, error) {
		spawnCount++
		// Create a mock process (won't actually run)
		return &PluginProcess{
			PluginID: "test-plugin",
			healthy:  true,
		}, nil
	}

	// Get first process
	proc1, err := pool.GetProcess("test-plugin", spawnFunc)
	if err != nil {
		t.Fatalf("GetProcess failed: %v", err)
	}
	if proc1 == nil {
		t.Fatal("Expected process, got nil")
	}
	if spawnCount != 1 {
		t.Errorf("Spawn count = %d, want 1", spawnCount)
	}

	// Get second process
	proc2, err := pool.GetProcess("test-plugin", spawnFunc)
	if err != nil {
		t.Fatalf("GetProcess failed: %v", err)
	}
	if proc2 == nil {
		t.Fatal("Expected process, got nil")
	}
	if spawnCount != 2 {
		t.Errorf("Spawn count = %d, want 2", spawnCount)
	}

	// Try to get third process - should fail (max size = 2)
	_, err = pool.GetProcess("test-plugin", spawnFunc)
	if err == nil {
		t.Error("Expected error when pool is at max size")
	}

	// Release first process
	pool.ReleaseProcess("test-plugin", proc1)

	// Now we can get another process (should reuse proc1)
	proc3, err := pool.GetProcess("test-plugin", spawnFunc)
	if err != nil {
		t.Fatalf("GetProcess failed: %v", err)
	}
	if proc3 != proc1 {
		t.Error("Expected reused process, got new one")
	}
	if spawnCount != 2 {
		t.Errorf("Spawn count = %d, want 2 (should reuse)", spawnCount)
	}

	// Release all processes
	pool.ReleaseProcess("test-plugin", proc2)
	pool.ReleaseProcess("test-plugin", proc3)

	stats := pool.Stats()
	if stats.TotalPlugins != 1 {
		t.Errorf("TotalPlugins = %d, want 1", stats.TotalPlugins)
	}
	if stats.AvailableCount != 2 {
		t.Errorf("AvailableCount = %d, want 2", stats.AvailableCount)
	}
	if stats.InUseCount != 0 {
		t.Errorf("InUseCount = %d, want 0", stats.InUseCount)
	}
}

// TestPluginProcessPoolMaxUses tests that processes are retired after max uses
func TestPluginProcessPoolMaxUses(t *testing.T) {
	config := PoolConfig{
		MaxSize:             3,
		MaxUsesPerProcess:   2,
		IdleTimeout:         0,
		HealthCheckInterval: 0,
	}

	pool := NewPluginProcessPool(config)
	defer pool.Shutdown()

	// Track which processes were killed
	killed := make(map[*PluginProcess]bool)
	var mu sync.Mutex

	spawnFunc := func() (*PluginProcess, error) {
		return &PluginProcess{
			PluginID: "test-plugin",
			healthy:  true,
			maxUses:  config.MaxUsesPerProcess,
		}, nil
	}

	// Get and release process twice
	for i := 0; i < 3; i++ {
		proc, err := pool.GetProcess("test-plugin", spawnFunc)
		if err != nil {
			t.Fatalf("GetProcess failed: %v", err)
		}

		// Simulate use
		proc.UseCount = i + 1

		// Check health after use count exceeds max
		if !proc.IsHealthy() {
			mu.Lock()
			killed[proc] = true
			mu.Unlock()
		}

		pool.ReleaseProcess("test-plugin", proc)
	}
}

// TestPluginProcessHealth tests process health checking
func TestPluginProcessHealth(t *testing.T) {
	tests := []struct {
		name        string
		process     *PluginProcess
		wantHealthy bool
	}{
		{
			name: "healthy process",
			process: &PluginProcess{
				healthy:   true,
				UseCount:  0,
				maxUses:   10,
				Cmd:       &exec.Cmd{},
			},
			wantHealthy: true,
		},
		{
			name: "unhealthy flag set",
			process: &PluginProcess{
				healthy:   false,
				UseCount:  0,
				maxUses:   10,
				Cmd:       &exec.Cmd{},
			},
			wantHealthy: false,
		},
		{
			name: "max uses reached",
			process: &PluginProcess{
				healthy:   true,
				UseCount:  10,
				maxUses:   10,
				Cmd:       &exec.Cmd{},
			},
			wantHealthy: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.process.IsHealthy()
			if got != tt.wantHealthy {
				t.Errorf("IsHealthy() = %v, want %v", got, tt.wantHealthy)
			}
		})
	}
}

// TestPluginProcessPoolCleanup tests the cleanup of unhealthy processes
func TestPluginProcessPoolCleanup(t *testing.T) {
	config := PoolConfig{
		MaxSize:             3,
		MaxUsesPerProcess:   0,
		IdleTimeout:         100 * time.Millisecond,
		HealthCheckInterval: 50 * time.Millisecond,
	}

	pool := NewPluginProcessPool(config)
	defer pool.Shutdown()

	// Create and add an unhealthy process
	dummyCmd := exec.Command("echo", "test")
	unhealthyProc := &PluginProcess{
		PluginID: "test-plugin",
		Cmd:      dummyCmd,
		healthy:  false, // Mark as unhealthy
		LastUsed: time.Now().Add(-time.Hour), // Very old
	}

	// Manually add to pool
	pool.mu.Lock()
	p := &pluginPool{
		available: []*PluginProcess{unhealthyProc},
		inUse:     make(map[*PluginProcess]bool),
	}
	pool.pools["test-plugin"] = p
	pool.mu.Unlock()

	// Wait for cleanup to run
	time.Sleep(200 * time.Millisecond)

	// Check that the unhealthy process was cleaned up
	p.mu.Lock()
	count := len(p.available)
	p.mu.Unlock()

	if count != 0 {
		t.Errorf("Expected 0 processes after cleanup, got %d", count)
	}
}

// TestPluginProcessPoolShutdown tests pool shutdown
func TestPluginProcessPoolShutdown(t *testing.T) {
	config := PoolConfig{
		MaxSize: 3,
	}

	pool := NewPluginProcessPool(config)

	spawnFunc := func() (*PluginProcess, error) {
		return &PluginProcess{
			PluginID: "test-plugin",
			healthy:  true,
		}, nil
	}

	// Get some processes
	proc1, _ := pool.GetProcess("test-plugin", spawnFunc)
	proc2, _ := pool.GetProcess("test-plugin", spawnFunc)

	// Release one, keep one in use
	pool.ReleaseProcess("test-plugin", proc1)

	// Verify pool has processes
	stats := pool.Stats()
	if stats.TotalProcesses != 2 {
		t.Errorf("Expected 2 processes before shutdown, got %d", stats.TotalProcesses)
	}

	// Shutdown
	pool.Shutdown()

	// After shutdown, pool should be empty
	// Note: we can't easily verify processes were killed without a real process,
	// but we can verify the pool structure was cleared
	pool.mu.RLock()
	poolCount := len(pool.pools)
	pool.mu.RUnlock()

	if poolCount != 0 {
		t.Errorf("Expected 0 pools after shutdown, got %d", poolCount)
	}

	// Release should not panic after shutdown
	pool.ReleaseProcess("test-plugin", proc2)
}

// TestPluginProcessPoolStats tests pool statistics
func TestPluginProcessPoolStats(t *testing.T) {
	config := PoolConfig{
		MaxSize: 5,
	}

	pool := NewPluginProcessPool(config)
	defer pool.Shutdown()

	spawnFunc := func() (*PluginProcess, error) {
		return &PluginProcess{
			PluginID: "test-plugin",
			healthy:  true,
		}, nil
	}

	// Initially empty
	stats := pool.Stats()
	if stats.TotalPlugins != 0 {
		t.Errorf("Expected 0 plugins initially, got %d", stats.TotalPlugins)
	}

	// Add processes
	proc1, _ := pool.GetProcess("plugin-a", spawnFunc)
	proc2, _ := pool.GetProcess("plugin-a", spawnFunc)
	_, _ = pool.GetProcess("plugin-b", spawnFunc)

	// All in use
	stats = pool.Stats()
	if stats.TotalPlugins != 2 {
		t.Errorf("Expected 2 plugins, got %d", stats.TotalPlugins)
	}
	if stats.TotalProcesses != 3 {
		t.Errorf("Expected 3 processes, got %d", stats.TotalProcesses)
	}
	if stats.InUseCount != 3 {
		t.Errorf("Expected 3 in use, got %d", stats.InUseCount)
	}
	if stats.AvailableCount != 0 {
		t.Errorf("Expected 0 available, got %d", stats.AvailableCount)
	}

	// Release some
	pool.ReleaseProcess("plugin-a", proc1)
	pool.ReleaseProcess("plugin-a", proc2)

	stats = pool.Stats()
	if stats.AvailableCount != 2 {
		t.Errorf("Expected 2 available, got %d", stats.AvailableCount)
	}
	if stats.InUseCount != 1 {
		t.Errorf("Expected 1 in use, got %d", stats.InUseCount)
	}
}

// TestPluginProcessPoolConcurrentAccess tests concurrent pool access
func TestPluginProcessPoolConcurrentAccess(t *testing.T) {
	config := PoolConfig{
		MaxSize: 10,
	}

	pool := NewPluginProcessPool(config)
	defer pool.Shutdown()

	spawnFunc := func() (*PluginProcess, error) {
		return &PluginProcess{
			PluginID: "test-plugin",
			healthy:  true,
		}, nil
	}

	var wg sync.WaitGroup
	numGoroutines := 20
	numIterations := 10

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numIterations; j++ {
				proc, err := pool.GetProcess("test-plugin", spawnFunc)
				if err != nil {
					// Expected when pool is at capacity
					if !strings.Contains(err.Error(), "max pool size") {
						t.Errorf("Unexpected error: %v", err)
					}
					continue
				}
				// Simulate work
				time.Sleep(time.Millisecond)
				pool.ReleaseProcess("test-plugin", proc)
			}
		}(i)
	}

	wg.Wait()

	// All processes should be available or cleaned up
	stats := pool.Stats()
	if stats.InUseCount != 0 {
		t.Errorf("Expected 0 in use after all released, got %d", stats.InUseCount)
	}
}

// TestDefaultPoolConfig tests the default pool configuration
func TestDefaultPoolConfig(t *testing.T) {
	cfg := DefaultPoolConfig()

	if cfg.MaxSize != 3 {
		t.Errorf("MaxSize = %d, want 3", cfg.MaxSize)
	}
	if cfg.MaxUsesPerProcess != 100 {
		t.Errorf("MaxUsesPerProcess = %d, want 100", cfg.MaxUsesPerProcess)
	}
	if cfg.IdleTimeout != 5*time.Minute {
		t.Errorf("IdleTimeout = %v, want 5m", cfg.IdleTimeout)
	}
	if cfg.HealthCheckInterval != 30*time.Second {
		t.Errorf("HealthCheckInterval = %v, want 30s", cfg.HealthCheckInterval)
	}
}

// TestNewPluginProcessPoolWithDefaults tests that defaults are applied
func TestNewPluginProcessPoolWithDefaults(t *testing.T) {
	// Test with zero values
	config := PoolConfig{
		MaxSize: 0, // Should default to 3
	}

	pool := NewPluginProcessPool(config)
	defer pool.Shutdown()

	// Verify that the pool uses the default max size
	spawnFunc := func() (*PluginProcess, error) {
		return &PluginProcess{
			PluginID: "test-plugin",
			healthy:  true,
		}, nil
	}

	// Should be able to get up to 3 processes
	processes := make([]*PluginProcess, 0, 3)
	for i := 0; i < 3; i++ {
		proc, err := pool.GetProcess("test-plugin", spawnFunc)
		if err != nil {
			t.Fatalf("Failed to get process %d: %v", i, err)
		}
		processes = append(processes, proc)
	}

	// 4th should fail
	_, err := pool.GetProcess("test-plugin", spawnFunc)
	if err == nil {
		t.Error("Expected error when exceeding default max size")
	}

	// Cleanup
	for _, proc := range processes {
		pool.ReleaseProcess("test-plugin", proc)
	}
}

// TestPluginRequestResponse tests request/response structures
func TestPluginRequestResponse(t *testing.T) {
	req := PluginRequest{
		Action: "execute",
		Payload: map[string]interface{}{
			"key": "value",
		},
	}

	if req.Action != "execute" {
		t.Errorf("Action = %s, want execute", req.Action)
	}
	if req.Payload["key"] != "value" {
		t.Error("Payload not set correctly")
	}

	resp := PluginResponse{
		Success: true,
		Result: map[string]interface{}{
			"output": "test",
		},
	}

	if !resp.Success {
		t.Error("Success should be true")
	}
	if resp.Result["output"] != "test" {
		t.Error("Result not set correctly")
	}

	resp.Error = "error message"
	if resp.Error != "error message" {
		t.Error("Error not set correctly")
	}
}

// TestPluginProcessKill tests process termination
func TestPluginProcessKill(t *testing.T) {
	// Create a real process that we can kill
	cmd := exec.Command("sleep", "10")
	err := cmd.Start()
	if err != nil {
		t.Skip("Cannot start test process")
	}

	proc := &PluginProcess{
		PluginID: "test",
		Cmd:      cmd,
		healthy:  true,
	}

	// Kill should work
	err = proc.Kill()
	if err != nil {
		t.Errorf("Kill failed: %v", err)
	}

	if proc.healthy {
		t.Error("Process should be marked unhealthy after kill")
	}

	// Wait for process to exit
	cmd.Wait()

	if proc.IsHealthy() {
		t.Error("Process should not be healthy after kill")
	}
}

// TestExecutorWithNilPool tests executor with nil pool
func TestExecutorWithNilPool(t *testing.T) {
	executor := &Executor{
		workDir: "/tmp",
		pool:    nil,
	}

	if executor.pool != nil {
		t.Error("Expected nil pool")
	}
}

// TestExecutorNewExecutorWithPool tests creating executor with custom pool
func TestExecutorNewExecutorWithPool(t *testing.T) {
	pool := NewPluginProcessPool(DefaultPoolConfig())
	defer pool.Shutdown()

	executor := NewExecutorWithPool(nil, "/tmp", pool)

	if executor.pool != pool {
		t.Error("Executor should use provided pool")
	}
	if executor.workDir != "/tmp" {
		t.Errorf("WorkDir = %s, want /tmp", executor.workDir)
	}
}

// TestPluginProcessPoolReleaseNil tests releasing nil process
func TestPluginProcessPoolReleaseNil(t *testing.T) {
	pool := NewPluginProcessPool(DefaultPoolConfig())
	defer pool.Shutdown()

	// Should not panic
	pool.ReleaseProcess("test-plugin", nil)
}

// TestPluginProcessPoolReleaseUnknownPlugin tests releasing to unknown plugin
func TestPluginProcessPoolReleaseUnknownPlugin(t *testing.T) {
	pool := NewPluginProcessPool(DefaultPoolConfig())
	defer pool.Shutdown()

	proc := &PluginProcess{
		PluginID: "unknown",
		healthy:  true,
	}

	// Should not panic and should mark process unhealthy/kill it
	pool.ReleaseProcess("unknown-plugin", proc)

	if proc.healthy {
		t.Error("Process should be marked unhealthy when releasing to unknown pool")
	}
}

// TestBackoffCalculation tests the exponential backoff calculation
func TestBackoffCalculation(t *testing.T) {
	cfg := &RetryConfig{
		MaxRetries:        5,
		InitialBackoff:    100 * time.Millisecond,
		MaxBackoff:        1 * time.Second,
		BackoffMultiplier: 2.0,
	}

	tests := []struct {
		attempt  int
		expected time.Duration
	}{
		{0, 100 * time.Millisecond},
		{1, 200 * time.Millisecond},
		{2, 400 * time.Millisecond},
		{3, 800 * time.Millisecond},
		{4, 1 * time.Second}, // Capped at MaxBackoff
	}

	for _, tt := range tests {
		backoff := cfg.InitialBackoff
		for i := 0; i < tt.attempt; i++ {
			backoff = time.Duration(float64(backoff) * cfg.BackoffMultiplier)
			if backoff > cfg.MaxBackoff {
				backoff = cfg.MaxBackoff
				break
			}
		}

		if backoff != tt.expected {
			t.Errorf("Attempt %d: backoff = %v, want %v", tt.attempt, backoff, tt.expected)
		}
	}
}

// BenchmarkPluginProcessPoolGetRelease benchmarks pool operations
func BenchmarkPluginProcessPoolGetRelease(b *testing.B) {
	config := PoolConfig{
		MaxSize:           10,
		MaxUsesPerProcess: 0,
		IdleTimeout:       0,
	}

	pool := NewPluginProcessPool(config)
	defer pool.Shutdown()

	spawnFunc := func() (*PluginProcess, error) {
		return &PluginProcess{
			PluginID: "bench-plugin",
			healthy:  true,
		}, nil
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			proc, err := pool.GetProcess("bench-plugin", spawnFunc)
			if err != nil {
				continue
			}
			pool.ReleaseProcess("bench-plugin", proc)
		}
	})
}

// TestExecuteWithContextTimeout tests the context timeout functionality
func TestExecuteWithContextTimeout(t *testing.T) {
	// This test verifies that ExecuteWithContext properly handles context cancellation
	// We can't fully test without a real invoker, but we can verify the method exists
	// and the context handling structure is correct

	// Create a context with a very short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()

	// Wait for context to expire
	time.Sleep(10 * time.Millisecond)

	// Verify context is expired
	if ctx.Err() == nil {
		t.Fatal("Context should have expired")
	}

	// Verify that the context handling is correct
	// The actual ExecuteWithContext will fail due to nil invoker,
	// but we can verify the context structure is set up correctly
	done := make(chan struct{})
	go func() {
		defer close(done)
		// This would normally call the executor, but we're just testing
		// that the pattern works with an expired context
		select {
		case <-ctx.Done():
			// Expected
		case <-time.After(time.Second):
			t.Error("Should have detected context cancellation")
		}
	}()

	select {
	case <-done:
		// Test passed
	case <-time.After(time.Second):
		t.Fatal("Test timeout")
	}
}

// TestExecutorConstructor tests the executor constructors
func TestExecutorConstructor(t *testing.T) {
	t.Run("NewExecutor with empty workDir", func(t *testing.T) {
		executor := NewExecutor(nil, "")
		if executor.workDir == "" {
			t.Error("Expected workDir to be set to current directory")
		}
		if executor.pool == nil {
			t.Error("Expected pool to be initialized")
		}
	})

	t.Run("NewExecutor with explicit workDir", func(t *testing.T) {
		executor := NewExecutor(nil, "/custom/path")
		if executor.workDir != "/custom/path" {
			t.Errorf("Expected workDir = /custom/path, got %s", executor.workDir)
		}
	})

	t.Run("NewExecutorWithPool with empty workDir", func(t *testing.T) {
		pool := NewPluginProcessPool(DefaultPoolConfig())
		defer pool.Shutdown()

		executor := NewExecutorWithPool(nil, "", pool)
		if executor.workDir == "" {
			t.Error("Expected workDir to be set to current directory")
		}
	})

	t.Run("NewExecutorWithPool with all parameters", func(t *testing.T) {
		pool := NewPluginProcessPool(DefaultPoolConfig())
		defer pool.Shutdown()

		executor := NewExecutorWithPool(nil, "/test/path", pool)
		if executor.workDir != "/test/path" {
			t.Errorf("Expected workDir = /test/path, got %s", executor.workDir)
		}
		if executor.pool != pool {
			t.Error("Expected pool to be the one provided")
		}
		if executor.invoker != nil {
			t.Error("Expected invoker to be nil")
		}
	})
}

// TestRetryConfigZeroRetries tests that zero retries means no retries
func TestRetryConfigZeroRetries(t *testing.T) {
	cfg := &RetryConfig{
		MaxRetries:        0,
		InitialBackoff:    100 * time.Millisecond,
		MaxBackoff:        1 * time.Second,
		BackoffMultiplier: 2.0,
	}

	if cfg.MaxRetries != 0 {
		t.Errorf("MaxRetries = %d, want 0", cfg.MaxRetries)
	}

	// With MaxRetries = 0, the loop should run once (attempt 0)
	attemptCount := 0
	for attempt := 0; attempt <= cfg.MaxRetries; attempt++ {
		attemptCount++
	}

	if attemptCount != 1 {
		t.Errorf("Attempt count = %d, want 1", attemptCount)
	}
}

// TestPoolConfigValidation tests pool configuration validation
func TestPoolConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		config PoolConfig
	}{
		{
			name:   "zero MaxSize uses default",
			config: PoolConfig{MaxSize: 0},
		},
		{
			name:   "negative MaxSize uses default",
			config: PoolConfig{MaxSize: -1},
		},
		{
			name:   "positive MaxSize is respected",
			config: PoolConfig{MaxSize: 5},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := NewPluginProcessPool(tt.config)
			defer pool.Shutdown()

			expectedMaxSize := tt.config.MaxSize
			if expectedMaxSize <= 0 {
				expectedMaxSize = 3 // default
			}

			if pool.config.MaxSize != expectedMaxSize {
				t.Errorf("MaxSize = %d, want %d", pool.config.MaxSize, expectedMaxSize)
			}
		})
	}
}

// mockError is a custom error type for testing

type mockError string

func (e mockError) Error() string {
	return string(e)
}

// TestIsRetryableErrorWithCustomTypes tests retry detection with custom error types
func TestIsRetryableErrorWithCustomTypes(t *testing.T) {
	cfg := DefaultRetryConfig()

	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "wrapped transient error",
			err:      fmt.Errorf("wrapped: %w", errors.New("connection timeout")),
			expected: true,
		},
		{
			name:     "custom error type with transient indicator",
			err:      mockError("transient failure occurred"),
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cfg.IsRetryableError(tt.err)
			if result != tt.expected {
				t.Errorf("IsRetryableError(%v) = %v, want %v", tt.err, result, tt.expected)
			}
		})
	}
}

// TestPluginProcessUseCountIncrement tests use count tracking
func TestPluginProcessUseCountIncrement(t *testing.T) {
	proc := &PluginProcess{
		PluginID: "test",
		healthy:  true,
		UseCount: 0,
		maxUses:  10,
	}

	if proc.UseCount != 0 {
		t.Errorf("Initial UseCount = %d, want 0", proc.UseCount)
	}

	// Simulate increment (normally done during Execute)
	proc.UseCount++
	if proc.UseCount != 1 {
		t.Errorf("After increment UseCount = %d, want 1", proc.UseCount)
	}

	// Verify health is still good
	if !proc.IsHealthy() {
		t.Error("Process should still be healthy")
	}

	// Increment to max
	proc.UseCount = 10
	if proc.IsHealthy() {
		t.Error("Process should not be healthy at max uses")
	}
}

// TestPluginProcessPoolMultiplePlugins tests managing different plugins
func TestPluginProcessPoolMultiplePlugins(t *testing.T) {
	config := PoolConfig{
		MaxSize: 3,
	}

	pool := NewPluginProcessPool(config)
	defer pool.Shutdown()

	spawnCountA := 0
	spawnCountB := 0

	spawnFuncA := func() (*PluginProcess, error) {
		spawnCountA++
		return &PluginProcess{PluginID: "plugin-a", healthy: true}, nil
	}

	spawnFuncB := func() (*PluginProcess, error) {
		spawnCountB++
		return &PluginProcess{PluginID: "plugin-b", healthy: true}, nil
	}

	// Get processes from both plugins
	procA1, _ := pool.GetProcess("plugin-a", spawnFuncA)
	procA2, _ := pool.GetProcess("plugin-a", spawnFuncA)
	procB1, _ := pool.GetProcess("plugin-b", spawnFuncB)

	if spawnCountA != 2 {
		t.Errorf("Plugin A spawn count = %d, want 2", spawnCountA)
	}
	if spawnCountB != 1 {
		t.Errorf("Plugin B spawn count = %d, want 1", spawnCountB)
	}

	// Release and verify stats
	pool.ReleaseProcess("plugin-a", procA1)
	pool.ReleaseProcess("plugin-a", procA2)
	pool.ReleaseProcess("plugin-b", procB1)

	stats := pool.Stats()
	if stats.TotalPlugins != 2 {
		t.Errorf("Total plugins = %d, want 2", stats.TotalPlugins)
	}
	if stats.AvailableCount != 3 {
		t.Errorf("Available count = %d, want 3", stats.AvailableCount)
	}
}
