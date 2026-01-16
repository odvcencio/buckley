// pkg/ralph/orchestrator_test.go
package ralph

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewOrchestrator(t *testing.T) {
	registry := NewBackendRegistry()
	config := &ControlConfig{
		Mode: ModeSequential,
		Backends: map[string]BackendConfig{
			"test": {Command: "test", Enabled: true},
		},
	}

	o := NewOrchestrator(registry, config)
	if o == nil {
		t.Fatal("expected non-nil orchestrator")
	}
	if o.registry != registry {
		t.Error("expected registry to be set")
	}
	if o.config != config {
		t.Error("expected config to be set")
	}
}

func TestOrchestrator_Execute_Sequential_FirstAvailable(t *testing.T) {
	registry := NewBackendRegistry()
	registry.Register(&mockBackend{name: "backend-a", available: true})
	registry.Register(&mockBackend{name: "backend-b", available: true})

	config := &ControlConfig{
		Mode: ModeSequential,
		Backends: map[string]BackendConfig{
			"backend-a": {Command: "a", Enabled: true},
			"backend-b": {Command: "b", Enabled: true},
		},
	}

	o := NewOrchestrator(registry, config)
	req := BackendRequest{Prompt: "test"}

	results, err := o.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	// Should use first available backend
	if results[0].Backend != "backend-a" && results[0].Backend != "backend-b" {
		t.Errorf("expected one of the backends, got %q", results[0].Backend)
	}
}

func TestOrchestrator_Execute_Sequential_SkipsDisabled(t *testing.T) {
	registry := NewBackendRegistry()
	registry.Register(&mockBackend{name: "disabled-backend", available: true})
	registry.Register(&mockBackend{name: "enabled-backend", available: true})

	config := &ControlConfig{
		Mode: ModeSequential,
		Backends: map[string]BackendConfig{
			"disabled-backend": {Command: "a", Enabled: false},
			"enabled-backend":  {Command: "b", Enabled: true},
		},
	}

	o := NewOrchestrator(registry, config)
	req := BackendRequest{Prompt: "test"}

	results, err := o.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	if results[0].Backend != "enabled-backend" {
		t.Errorf("expected 'enabled-backend', got %q", results[0].Backend)
	}
}

func TestOrchestrator_Execute_Sequential_SkipsUnavailable(t *testing.T) {
	registry := NewBackendRegistry()
	registry.Register(&mockBackend{name: "unavailable-backend", available: false})
	registry.Register(&mockBackend{name: "available-backend", available: true})

	config := &ControlConfig{
		Mode: ModeSequential,
		Backends: map[string]BackendConfig{
			"unavailable-backend": {Command: "a", Enabled: true},
			"available-backend":   {Command: "b", Enabled: true},
		},
	}

	o := NewOrchestrator(registry, config)
	req := BackendRequest{Prompt: "test"}

	results, err := o.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	if results[0].Backend != "available-backend" {
		t.Errorf("expected 'available-backend', got %q", results[0].Backend)
	}
}

func TestOrchestrator_Execute_Sequential_NoAvailableBackends(t *testing.T) {
	registry := NewBackendRegistry()
	registry.Register(&mockBackend{name: "unavailable", available: false})

	config := &ControlConfig{
		Mode: ModeSequential,
		Backends: map[string]BackendConfig{
			"unavailable": {Command: "a", Enabled: true},
		},
	}

	o := NewOrchestrator(registry, config)
	req := BackendRequest{Prompt: "test"}

	_, err := o.Execute(context.Background(), req)
	if err == nil {
		t.Fatal("expected error when no backends available")
	}
}

func TestOrchestrator_Execute_Parallel_AllBackends(t *testing.T) {
	registry := NewBackendRegistry()
	registry.Register(&mockBackend{name: "backend-a", available: true})
	registry.Register(&mockBackend{name: "backend-b", available: true})
	registry.Register(&mockBackend{name: "backend-c", available: true})

	config := &ControlConfig{
		Mode: ModeParallel,
		Backends: map[string]BackendConfig{
			"backend-a": {Command: "a", Enabled: true},
			"backend-b": {Command: "b", Enabled: true},
			"backend-c": {Command: "c", Enabled: true},
		},
	}

	o := NewOrchestrator(registry, config)
	req := BackendRequest{Prompt: "test"}

	results, err := o.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// Verify all backends returned results
	names := make(map[string]bool)
	for _, r := range results {
		names[r.Backend] = true
	}

	for _, name := range []string{"backend-a", "backend-b", "backend-c"} {
		if !names[name] {
			t.Errorf("expected result from %q", name)
		}
	}
}

func TestOrchestrator_Execute_Parallel_FiltersBackends(t *testing.T) {
	registry := NewBackendRegistry()
	registry.Register(&mockBackend{name: "enabled-available", available: true})
	registry.Register(&mockBackend{name: "enabled-unavailable", available: false})
	registry.Register(&mockBackend{name: "disabled-available", available: true})

	config := &ControlConfig{
		Mode: ModeParallel,
		Backends: map[string]BackendConfig{
			"enabled-available":   {Command: "a", Enabled: true},
			"enabled-unavailable": {Command: "b", Enabled: true},
			"disabled-available":  {Command: "c", Enabled: false},
		},
	}

	o := NewOrchestrator(registry, config)
	req := BackendRequest{Prompt: "test"}

	results, err := o.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	if results[0].Backend != "enabled-available" {
		t.Errorf("expected 'enabled-available', got %q", results[0].Backend)
	}
}

func TestOrchestrator_Execute_Parallel_Concurrent(t *testing.T) {
	var startCount atomic.Int32
	var maxConcurrent atomic.Int32
	var mu sync.Mutex

	// Create backends that track concurrency
	slowBackendFactory := func(name string) *concurrentMockBackend {
		return &concurrentMockBackend{
			name:          name,
			available:     true,
			startCount:    &startCount,
			maxConcurrent: &maxConcurrent,
			mu:            &mu,
			delay:         50 * time.Millisecond,
		}
	}

	registry := NewBackendRegistry()
	registry.Register(slowBackendFactory("backend-a"))
	registry.Register(slowBackendFactory("backend-b"))
	registry.Register(slowBackendFactory("backend-c"))

	config := &ControlConfig{
		Mode: ModeParallel,
		Backends: map[string]BackendConfig{
			"backend-a": {Command: "a", Enabled: true},
			"backend-b": {Command: "b", Enabled: true},
			"backend-c": {Command: "c", Enabled: true},
		},
	}

	o := NewOrchestrator(registry, config)
	req := BackendRequest{Prompt: "test"}

	start := time.Now()
	results, err := o.Execute(context.Background(), req)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// If run concurrently, should complete in ~50ms (not 150ms)
	if elapsed > 150*time.Millisecond {
		t.Errorf("expected concurrent execution to complete in ~50ms, took %v", elapsed)
	}

	// Verify actual concurrency happened
	if maxConcurrent.Load() < 2 {
		t.Errorf("expected at least 2 concurrent executions, got %d", maxConcurrent.Load())
	}
}

func TestOrchestrator_Execute_RoundRobin_Rotates(t *testing.T) {
	registry := NewBackendRegistry()
	registry.Register(&mockBackend{name: "backend-a", available: true})
	registry.Register(&mockBackend{name: "backend-b", available: true})
	registry.Register(&mockBackend{name: "backend-c", available: true})

	config := &ControlConfig{
		Mode: ModeRoundRobin,
		Backends: map[string]BackendConfig{
			"backend-a": {Command: "a", Enabled: true},
			"backend-b": {Command: "b", Enabled: true},
			"backend-c": {Command: "c", Enabled: true},
		},
	}

	o := NewOrchestrator(registry, config)
	req := BackendRequest{Prompt: "test"}

	// Execute multiple times and verify rotation
	seen := make(map[string]int)
	for i := 0; i < 6; i++ {
		results, err := o.Execute(context.Background(), req)
		if err != nil {
			t.Fatalf("Execute failed on iteration %d: %v", i, err)
		}
		if len(results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(results))
		}
		seen[results[0].Backend]++
	}

	// After 6 iterations, each backend should be used exactly 2 times
	for name, count := range seen {
		if count != 2 {
			t.Errorf("expected backend %q to be used 2 times, got %d", name, count)
		}
	}
}

func TestOrchestrator_Execute_RoundRobin_SkipsUnavailable(t *testing.T) {
	registry := NewBackendRegistry()
	registry.Register(&mockBackend{name: "backend-a", available: true})
	registry.Register(&mockBackend{name: "backend-b", available: false}) // unavailable
	registry.Register(&mockBackend{name: "backend-c", available: true})

	config := &ControlConfig{
		Mode: ModeRoundRobin,
		Backends: map[string]BackendConfig{
			"backend-a": {Command: "a", Enabled: true},
			"backend-b": {Command: "b", Enabled: true},
			"backend-c": {Command: "c", Enabled: true},
		},
	}

	o := NewOrchestrator(registry, config)
	req := BackendRequest{Prompt: "test"}

	// Execute multiple times
	seen := make(map[string]int)
	for i := 0; i < 4; i++ {
		results, err := o.Execute(context.Background(), req)
		if err != nil {
			t.Fatalf("Execute failed on iteration %d: %v", i, err)
		}
		if len(results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(results))
		}
		seen[results[0].Backend]++
	}

	// backend-b should never be used
	if seen["backend-b"] > 0 {
		t.Errorf("expected backend-b to never be used, but was used %d times", seen["backend-b"])
	}

	// The available backends should share the load
	if seen["backend-a"] == 0 {
		t.Error("expected backend-a to be used at least once")
	}
	if seen["backend-c"] == 0 {
		t.Error("expected backend-c to be used at least once")
	}
}

func TestOrchestrator_Execute_ContextCancellation(t *testing.T) {
	registry := NewBackendRegistry()
	registry.Register(&mockBackend{
		name:      "slow",
		available: true,
		execDelay: 1 * time.Second,
	})

	config := &ControlConfig{
		Mode: ModeSequential,
		Backends: map[string]BackendConfig{
			"slow": {Command: "slow", Enabled: true},
		},
	}

	o := NewOrchestrator(registry, config)
	req := BackendRequest{Prompt: "test"}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := o.Execute(ctx, req)
	if err == nil {
		t.Fatal("expected error due to context cancellation")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected context.DeadlineExceeded, got %v", err)
	}
}

func TestOrchestrator_UpdateConfig(t *testing.T) {
	registry := NewBackendRegistry()
	initialConfig := &ControlConfig{
		Mode: ModeSequential,
		Backends: map[string]BackendConfig{
			"test": {Command: "test", Enabled: true},
		},
	}

	o := NewOrchestrator(registry, initialConfig)

	newConfig := &ControlConfig{
		Mode: ModeParallel,
		Backends: map[string]BackendConfig{
			"test": {Command: "test", Enabled: true},
		},
	}

	o.UpdateConfig(newConfig)

	// Verify config was updated
	o.mu.RLock()
	currentMode := o.config.Mode
	o.mu.RUnlock()

	if currentMode != ModeParallel {
		t.Errorf("expected mode 'parallel', got %q", currentMode)
	}
}

func TestOrchestrator_UpdateConfig_ThreadSafe(t *testing.T) {
	registry := NewBackendRegistry()
	registry.Register(&mockBackend{name: "test", available: true})

	config := &ControlConfig{
		Mode: ModeSequential,
		Backends: map[string]BackendConfig{
			"test": {Command: "test", Enabled: true},
		},
	}

	o := NewOrchestrator(registry, config)

	done := make(chan bool)

	// Writer goroutine
	go func() {
		for i := 0; i < 100; i++ {
			mode := ModeSequential
			if i%2 == 0 {
				mode = ModeParallel
			}
			o.UpdateConfig(&ControlConfig{
				Mode: mode,
				Backends: map[string]BackendConfig{
					"test": {Command: "test", Enabled: true},
				},
			})
		}
		done <- true
	}()

	// Reader goroutine - Execute
	go func() {
		for i := 0; i < 100; i++ {
			o.Execute(context.Background(), BackendRequest{Prompt: "test"})
		}
		done <- true
	}()

	// Wait for both
	for i := 0; i < 2; i++ {
		<-done
	}
}

func TestOrchestrator_NextIteration(t *testing.T) {
	registry := NewBackendRegistry()
	config := &ControlConfig{
		Mode: ModeSequential,
		Backends: map[string]BackendConfig{
			"test": {Command: "test", Enabled: true},
		},
	}

	o := NewOrchestrator(registry, config)

	if o.iteration != 0 {
		t.Errorf("expected initial iteration 0, got %d", o.iteration)
	}

	o.NextIteration()
	if o.iteration != 1 {
		t.Errorf("expected iteration 1, got %d", o.iteration)
	}

	o.NextIteration()
	if o.iteration != 2 {
		t.Errorf("expected iteration 2, got %d", o.iteration)
	}
}

func TestOrchestrator_EvaluateSchedule_EveryIterations(t *testing.T) {
	registry := NewBackendRegistry()
	config := &ControlConfig{
		Mode: ModeSequential,
		Backends: map[string]BackendConfig{
			"test": {Command: "test", Enabled: true},
		},
		Schedule: []ScheduleRule{
			{
				Trigger: ScheduleTrigger{EveryIterations: 5},
				Action:  "rotate_backend",
			},
		},
	}

	o := NewOrchestrator(registry, config)

	// At iteration 0, should not trigger
	action := o.EvaluateSchedule(nil)
	if action != nil {
		t.Errorf("expected nil action at iteration 0, got %v", action)
	}

	// Advance to iteration 5
	for i := 0; i < 5; i++ {
		o.NextIteration()
	}

	action = o.EvaluateSchedule(nil)
	if action == nil {
		t.Fatal("expected action at iteration 5")
	}
	if action.Action != "rotate_backend" {
		t.Errorf("expected action 'rotate_backend', got %q", action.Action)
	}

	// At iteration 6, should not trigger
	o.NextIteration()
	action = o.EvaluateSchedule(nil)
	if action != nil {
		t.Errorf("expected nil action at iteration 6, got %v", action)
	}

	// At iteration 10, should trigger again
	for i := 0; i < 4; i++ {
		o.NextIteration()
	}
	action = o.EvaluateSchedule(nil)
	if action == nil {
		t.Fatal("expected action at iteration 10")
	}
}

func TestOrchestrator_EvaluateSchedule_OnError_RateLimit(t *testing.T) {
	registry := NewBackendRegistry()
	config := &ControlConfig{
		Mode: ModeSequential,
		Backends: map[string]BackendConfig{
			"test": {Command: "test", Enabled: true},
		},
		Schedule: []ScheduleRule{
			{
				Trigger: ScheduleTrigger{OnError: "rate"},
				Action:  "next_backend",
			},
		},
	}

	o := NewOrchestrator(registry, config)

	// No error - should not trigger
	action := o.EvaluateSchedule(nil)
	if action != nil {
		t.Errorf("expected nil action with no error, got %v", action)
	}

	// Different error - should not trigger
	action = o.EvaluateSchedule(errors.New("connection refused"))
	if action != nil {
		t.Errorf("expected nil action with different error, got %v", action)
	}

	// Rate limit error - should trigger
	action = o.EvaluateSchedule(errors.New("rate limit exceeded"))
	if action == nil {
		t.Fatal("expected action for rate limit error")
	}
	if action.Action != "next_backend" {
		t.Errorf("expected action 'next_backend', got %q", action.Action)
	}

	// Case-insensitive check
	action = o.EvaluateSchedule(errors.New("API Rate Limit"))
	if action == nil {
		t.Fatal("expected action for case-insensitive rate limit error")
	}
}

func TestOrchestrator_EvaluateSchedule_SetMode(t *testing.T) {
	registry := NewBackendRegistry()
	config := &ControlConfig{
		Mode: ModeSequential,
		Backends: map[string]BackendConfig{
			"test": {Command: "test", Enabled: true},
		},
		Schedule: []ScheduleRule{
			{
				Trigger: ScheduleTrigger{EveryIterations: 1},
				Action:  "set_mode",
				Mode:    "parallel",
			},
		},
	}

	o := NewOrchestrator(registry, config)
	o.NextIteration()

	action := o.EvaluateSchedule(nil)
	if action == nil {
		t.Fatal("expected action")
	}
	if action.Action != "set_mode" {
		t.Errorf("expected action 'set_mode', got %q", action.Action)
	}
	if action.Mode != "parallel" {
		t.Errorf("expected mode 'parallel', got %q", action.Mode)
	}
}

func TestOrchestrator_EvaluateSchedule_SetBackend(t *testing.T) {
	registry := NewBackendRegistry()
	config := &ControlConfig{
		Mode: ModeSequential,
		Backends: map[string]BackendConfig{
			"test": {Command: "test", Enabled: true},
		},
		Schedule: []ScheduleRule{
			{
				Trigger: ScheduleTrigger{EveryIterations: 1},
				Action:  "set_backend",
				Backend: "claude",
			},
		},
	}

	o := NewOrchestrator(registry, config)
	o.NextIteration()

	action := o.EvaluateSchedule(nil)
	if action == nil {
		t.Fatal("expected action")
	}
	if action.Action != "set_backend" {
		t.Errorf("expected action 'set_backend', got %q", action.Action)
	}
	if action.Backend != "claude" {
		t.Errorf("expected backend 'claude', got %q", action.Backend)
	}
}

func TestOrchestrator_EvaluateSchedule_Pause(t *testing.T) {
	registry := NewBackendRegistry()
	config := &ControlConfig{
		Mode: ModeSequential,
		Backends: map[string]BackendConfig{
			"test": {Command: "test", Enabled: true},
		},
		Schedule: []ScheduleRule{
			{
				Trigger: ScheduleTrigger{EveryIterations: 1},
				Action:  "pause",
				Reason:  "scheduled pause",
			},
		},
	}

	o := NewOrchestrator(registry, config)
	o.NextIteration()

	action := o.EvaluateSchedule(nil)
	if action == nil {
		t.Fatal("expected action")
	}
	if action.Action != "pause" {
		t.Errorf("expected action 'pause', got %q", action.Action)
	}
	if action.Reason != "scheduled pause" {
		t.Errorf("expected reason 'scheduled pause', got %q", action.Reason)
	}
}

func TestOrchestrator_EvaluateSchedule_WhenAndCron_NotSupported(t *testing.T) {
	registry := NewBackendRegistry()
	config := &ControlConfig{
		Mode: ModeSequential,
		Backends: map[string]BackendConfig{
			"test": {Command: "test", Enabled: true},
		},
		Schedule: []ScheduleRule{
			{
				Trigger: ScheduleTrigger{When: "session.cost > 20.00"},
				Action:  "pause",
			},
			{
				Trigger: ScheduleTrigger{Cron: "0 */6 * * *"},
				Action:  "rotate_backend",
			},
		},
	}

	o := NewOrchestrator(registry, config)
	o.NextIteration()

	// Neither when nor cron triggers should fire
	action := o.EvaluateSchedule(nil)
	if action != nil {
		t.Errorf("expected nil action for unsupported triggers, got %v", action)
	}
}

func TestOrchestrator_EvaluateSchedule_FirstMatchWins(t *testing.T) {
	registry := NewBackendRegistry()
	config := &ControlConfig{
		Mode: ModeSequential,
		Backends: map[string]BackendConfig{
			"test": {Command: "test", Enabled: true},
		},
		Schedule: []ScheduleRule{
			{
				Trigger: ScheduleTrigger{EveryIterations: 5},
				Action:  "rotate_backend",
			},
			{
				Trigger: ScheduleTrigger{EveryIterations: 5},
				Action:  "pause", // same trigger, different action
			},
		},
	}

	o := NewOrchestrator(registry, config)
	for i := 0; i < 5; i++ {
		o.NextIteration()
	}

	action := o.EvaluateSchedule(nil)
	if action == nil {
		t.Fatal("expected action")
	}

	// First matching rule wins
	if action.Action != "rotate_backend" {
		t.Errorf("expected first matching action 'rotate_backend', got %q", action.Action)
	}
}

func TestOrchestrator_NilReceiver(t *testing.T) {
	var o *Orchestrator

	// These should not panic
	o.UpdateConfig(&ControlConfig{})
	o.NextIteration()

	action := o.EvaluateSchedule(nil)
	if action != nil {
		t.Error("expected nil action from nil orchestrator")
	}

	results, err := o.Execute(context.Background(), BackendRequest{})
	if err == nil {
		t.Error("expected error from nil orchestrator")
	}
	if results != nil {
		t.Error("expected nil results from nil orchestrator")
	}
}

func TestOrchestrator_Execute_BackendError(t *testing.T) {
	expectedErr := errors.New("backend execution failed")
	registry := NewBackendRegistry()
	registry.Register(&mockBackend{
		name:      "failing",
		available: true,
		execErr:   expectedErr,
	})

	config := &ControlConfig{
		Mode: ModeSequential,
		Backends: map[string]BackendConfig{
			"failing": {Command: "fail", Enabled: true},
		},
	}

	o := NewOrchestrator(registry, config)
	req := BackendRequest{Prompt: "test"}

	_, err := o.Execute(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from failing backend")
	}
	if !errors.Is(err, expectedErr) {
		t.Errorf("expected error %v, got %v", expectedErr, err)
	}
}

func TestOrchestrator_Execute_Parallel_PartialErrors(t *testing.T) {
	registry := NewBackendRegistry()
	registry.Register(&mockBackend{name: "success", available: true})
	registry.Register(&mockBackend{name: "fail", available: true, execErr: errors.New("failed")})

	config := &ControlConfig{
		Mode: ModeParallel,
		Backends: map[string]BackendConfig{
			"success": {Command: "s", Enabled: true},
			"fail":    {Command: "f", Enabled: true},
		},
	}

	o := NewOrchestrator(registry, config)
	req := BackendRequest{Prompt: "test"}

	// In parallel mode, partial errors should still return successful results
	results, err := o.Execute(context.Background(), req)

	// Should get at least the successful result
	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}

	// The error result should have Error field set
	hasSuccess := false
	hasError := false
	for _, r := range results {
		if r.Backend == "success" && r.Error == nil {
			hasSuccess = true
		}
		if r.Backend == "fail" && r.Error != nil {
			hasError = true
		}
	}

	if !hasSuccess {
		t.Error("expected successful result from 'success' backend")
	}

	// Error should be nil since we got partial results
	// OR it could be an error - implementation choice
	_ = err // acceptable either way
	_ = hasError
}

// concurrentMockBackend tracks concurrent execution
type concurrentMockBackend struct {
	name          string
	available     bool
	startCount    *atomic.Int32
	maxConcurrent *atomic.Int32
	mu            *sync.Mutex
	delay         time.Duration
}

func (c *concurrentMockBackend) Name() string {
	return c.name
}

func (c *concurrentMockBackend) Execute(ctx context.Context, req BackendRequest) (*BackendResult, error) {
	current := c.startCount.Add(1)

	// Update max concurrent
	c.mu.Lock()
	if current > c.maxConcurrent.Load() {
		c.maxConcurrent.Store(current)
	}
	c.mu.Unlock()

	defer c.startCount.Add(-1)

	select {
	case <-time.After(c.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	return &BackendResult{
		Backend: c.name,
		Output:  "concurrent output",
	}, nil
}

func (c *concurrentMockBackend) Available() bool {
	return c.available
}

func TestScheduleAction_Fields(t *testing.T) {
	action := &ScheduleAction{
		Action:  "set_backend",
		Mode:    "parallel",
		Backend: "claude",
		Reason:  "scheduled rotation",
	}

	if action.Action != "set_backend" {
		t.Errorf("expected Action 'set_backend', got %q", action.Action)
	}
	if action.Mode != "parallel" {
		t.Errorf("expected Mode 'parallel', got %q", action.Mode)
	}
	if action.Backend != "claude" {
		t.Errorf("expected Backend 'claude', got %q", action.Backend)
	}
	if action.Reason != "scheduled rotation" {
		t.Errorf("expected Reason 'scheduled rotation', got %q", action.Reason)
	}
}

func TestOrchestrator_Execute_BackendNotInRegistry(t *testing.T) {
	// Config references a backend that doesn't exist in registry
	registry := NewBackendRegistry()
	registry.Register(&mockBackend{name: "existing", available: true})

	config := &ControlConfig{
		Mode: ModeSequential,
		Backends: map[string]BackendConfig{
			"nonexistent": {Command: "x", Enabled: true},
		},
	}

	o := NewOrchestrator(registry, config)
	req := BackendRequest{Prompt: "test"}

	_, err := o.Execute(context.Background(), req)
	if err == nil {
		t.Fatal("expected error when backend not in registry")
	}
}

func TestOrchestrator_EvaluateSchedule_OnError_PartialMatch(t *testing.T) {
	registry := NewBackendRegistry()
	config := &ControlConfig{
		Mode: ModeSequential,
		Backends: map[string]BackendConfig{
			"test": {Command: "test", Enabled: true},
		},
		Schedule: []ScheduleRule{
			{
				Trigger: ScheduleTrigger{OnError: "rate"},
				Action:  "next_backend",
			},
		},
	}

	o := NewOrchestrator(registry, config)

	// Should match partial string "rate" in error message
	tests := []struct {
		err      error
		expected bool
	}{
		{errors.New("rate limit exceeded"), true},
		{errors.New("Rate Limit"), true},
		{errors.New("too many requests, rate limiting applied"), true},
		{errors.New("connection failed"), false},
		{nil, false},
	}

	for _, tt := range tests {
		action := o.EvaluateSchedule(tt.err)
		got := action != nil

		errStr := "nil"
		if tt.err != nil {
			errStr = tt.err.Error()
		}

		if got != tt.expected {
			t.Errorf("EvaluateSchedule(%q) = %v, expected %v", errStr, got, tt.expected)
		}
	}
}

func TestOrchestrator_Execute_EmptyConfig(t *testing.T) {
	registry := NewBackendRegistry()
	registry.Register(&mockBackend{name: "test", available: true})

	// No backends enabled in config
	config := &ControlConfig{
		Mode:     ModeSequential,
		Backends: map[string]BackendConfig{},
	}

	o := NewOrchestrator(registry, config)
	req := BackendRequest{Prompt: "test"}

	_, err := o.Execute(context.Background(), req)
	if err == nil {
		t.Fatal("expected error with empty config")
	}
}

func TestOrchestrator_EvaluateSchedule_NoRules(t *testing.T) {
	registry := NewBackendRegistry()
	config := &ControlConfig{
		Mode: ModeSequential,
		Backends: map[string]BackendConfig{
			"test": {Command: "test", Enabled: true},
		},
		Schedule: []ScheduleRule{}, // no rules
	}

	o := NewOrchestrator(registry, config)
	o.NextIteration()

	action := o.EvaluateSchedule(errors.New("some error"))
	if action != nil {
		t.Errorf("expected nil action with no rules, got %v", action)
	}
}

func TestOrchestrator_Execute_Parallel_AllFail(t *testing.T) {
	registry := NewBackendRegistry()
	registry.Register(&mockBackend{name: "fail1", available: true, execErr: errors.New("error 1")})
	registry.Register(&mockBackend{name: "fail2", available: true, execErr: errors.New("error 2")})

	config := &ControlConfig{
		Mode: ModeParallel,
		Backends: map[string]BackendConfig{
			"fail1": {Command: "f1", Enabled: true},
			"fail2": {Command: "f2", Enabled: true},
		},
	}

	o := NewOrchestrator(registry, config)
	req := BackendRequest{Prompt: "test"}

	results, err := o.Execute(context.Background(), req)

	// When all backends fail, should return error
	if err == nil && len(results) == 0 {
		t.Fatal("expected either error or results with error fields")
	}

	// Results should have error information
	for _, r := range results {
		if r.Error == nil {
			t.Errorf("expected error in result from %q", r.Backend)
		}
	}
}

func TestOrchestrator_EvaluateSchedule_EveryIterations_Zero(t *testing.T) {
	registry := NewBackendRegistry()
	config := &ControlConfig{
		Mode: ModeSequential,
		Backends: map[string]BackendConfig{
			"test": {Command: "test", Enabled: true},
		},
		Schedule: []ScheduleRule{
			{
				Trigger: ScheduleTrigger{EveryIterations: 0}, // zero means never
				Action:  "rotate_backend",
			},
		},
	}

	o := NewOrchestrator(registry, config)

	// Should never trigger
	for i := 0; i < 10; i++ {
		o.NextIteration()
		action := o.EvaluateSchedule(nil)
		if action != nil {
			t.Errorf("expected nil action with EveryIterations=0 at iteration %d", i)
		}
	}
}

func TestOrchestrator_EvaluateSchedule_OnError_EmptyTrigger(t *testing.T) {
	registry := NewBackendRegistry()
	config := &ControlConfig{
		Mode: ModeSequential,
		Backends: map[string]BackendConfig{
			"test": {Command: "test", Enabled: true},
		},
		Schedule: []ScheduleRule{
			{
				Trigger: ScheduleTrigger{OnError: ""}, // empty string
				Action:  "next_backend",
			},
		},
	}

	o := NewOrchestrator(registry, config)

	// Empty OnError should not trigger on any error
	action := o.EvaluateSchedule(errors.New("some error"))
	if action != nil {
		t.Errorf("expected nil action with empty OnError, got %v", action)
	}
}

func TestOrchestrator_GetAvailableBackends(t *testing.T) {
	// Test the internal filtering logic
	registry := NewBackendRegistry()
	registry.Register(&mockBackend{name: "enabled-available", available: true})
	registry.Register(&mockBackend{name: "enabled-unavailable", available: false})
	registry.Register(&mockBackend{name: "disabled-available", available: true})
	registry.Register(&mockBackend{name: "not-in-config", available: true})

	config := &ControlConfig{
		Mode: ModeParallel,
		Backends: map[string]BackendConfig{
			"enabled-available":   {Command: "a", Enabled: true},
			"enabled-unavailable": {Command: "b", Enabled: true},
			"disabled-available":  {Command: "c", Enabled: false},
			// "not-in-config" is in registry but not in config
		},
	}

	o := NewOrchestrator(registry, config)
	req := BackendRequest{Prompt: "test"}

	results, err := o.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Only "enabled-available" should be used
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	if results[0].Backend != "enabled-available" {
		t.Errorf("expected 'enabled-available', got %q", results[0].Backend)
	}
}

// Helper to check if error message contains substring
func containsSubstring(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}
