// pkg/ralph/orchestrator.go
package ralph

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// Orchestrator coordinates backend execution based on control configuration.
type Orchestrator struct {
	registry       *BackendRegistry
	config         *ControlConfig
	mu             sync.RWMutex
	currentBackend int // index for round_robin
	iteration      int

	// Context for "when" expression evaluation
	startTime     time.Time
	errorCount    int
	consecErrors  int
	totalCost     float64
	totalTokens   int
	lastCronCheck time.Time
}

// ScheduleAction represents an action to take based on schedule evaluation.
// These actions are triggered by schedule rules in the control configuration.
type ScheduleAction struct {
	// Action is the type of action: "rotate_backend", "next_backend", "pause",
	// "resume", "set_mode", or "set_backend".
	Action string
	// Mode is the new mode for "set_mode" action (sequential, parallel, round_robin).
	Mode string
	// Backend is the backend name for "set_backend" action.
	Backend string
	// Reason provides context for "pause" action.
	Reason string
}

// NewOrchestrator creates a new orchestrator with the given registry and config.
func NewOrchestrator(registry *BackendRegistry, config *ControlConfig) *Orchestrator {
	return &Orchestrator{
		registry:       registry,
		config:         config,
		currentBackend: 0,
		iteration:      0,
		startTime:      time.Now(),
	}
}

// Execute runs the prompt through backend(s) based on current mode.
func (o *Orchestrator) Execute(ctx context.Context, req BackendRequest) ([]*BackendResult, error) {
	if o == nil {
		return nil, fmt.Errorf("orchestrator is nil")
	}

	o.mu.RLock()
	mode := o.config.Mode
	o.mu.RUnlock()

	backends := o.getAvailableBackends()
	if len(backends) == 0 {
		return nil, fmt.Errorf("no available backends")
	}

	switch mode {
	case ModeSequential:
		return o.executeSequential(ctx, req, backends)
	case ModeParallel:
		return o.executeParallel(ctx, req, backends)
	case ModeRoundRobin:
		return o.executeRoundRobin(ctx, req, backends)
	default:
		return nil, fmt.Errorf("unknown mode: %s", mode)
	}
}

// executeSequential runs the first available backend.
func (o *Orchestrator) executeSequential(ctx context.Context, req BackendRequest, backends []Backend) ([]*BackendResult, error) {
	if len(backends) == 0 {
		return nil, fmt.Errorf("no available backends")
	}

	// Use the first available backend
	backend := backends[0]
	result, err := backend.Execute(ctx, req)
	if err != nil {
		return nil, err
	}

	return []*BackendResult{result}, nil
}

// executeParallel runs all available backends concurrently.
func (o *Orchestrator) executeParallel(ctx context.Context, req BackendRequest, backends []Backend) ([]*BackendResult, error) {
	if len(backends) == 0 {
		return nil, fmt.Errorf("no available backends")
	}

	var mu sync.Mutex
	results := make([]*BackendResult, 0, len(backends))

	g, ctx := errgroup.WithContext(ctx)

	for _, b := range backends {
		backend := b // capture for goroutine
		g.Go(func() error {
			result, err := backend.Execute(ctx, req)
			if err != nil {
				// Store error in result rather than failing the group
				result = &BackendResult{
					Backend: backend.Name(),
					Error:   err,
				}
			}

			mu.Lock()
			results = append(results, result)
			mu.Unlock()

			return nil // don't propagate errors to errgroup
		})
	}

	// Wait for all goroutines
	if err := g.Wait(); err != nil {
		return results, err
	}

	// Check if all results have errors
	allFailed := true
	for _, r := range results {
		if r.Error == nil {
			allFailed = false
			break
		}
	}

	if allFailed && len(results) > 0 {
		return results, fmt.Errorf("all backends failed")
	}

	return results, nil
}

// executeRoundRobin rotates through available backends.
func (o *Orchestrator) executeRoundRobin(ctx context.Context, req BackendRequest, backends []Backend) ([]*BackendResult, error) {
	if len(backends) == 0 {
		return nil, fmt.Errorf("no available backends")
	}

	o.mu.Lock()
	// Get current index and advance
	idx := o.currentBackend % len(backends)
	o.currentBackend++
	o.mu.Unlock()

	backend := backends[idx]
	result, err := backend.Execute(ctx, req)
	if err != nil {
		return nil, err
	}

	return []*BackendResult{result}, nil
}

// getAvailableBackends returns backends that are both enabled in config and available.
func (o *Orchestrator) getAvailableBackends() []Backend {
	o.mu.RLock()
	config := o.config
	o.mu.RUnlock()

	if config == nil || config.Backends == nil {
		return nil
	}

	var backends []Backend

	// Get backend names sorted for deterministic ordering
	names := make([]string, 0, len(config.Backends))
	for name := range config.Backends {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		backendConfig := config.Backends[name]

		// Skip disabled backends
		if !backendConfig.Enabled {
			continue
		}

		// Get backend from registry
		backend, ok := o.registry.Get(name)
		if !ok {
			continue
		}

		// Skip unavailable backends
		if !backend.Available() {
			continue
		}

		backends = append(backends, backend)
	}

	return backends
}

// UpdateConfig hot-reloads the configuration.
func (o *Orchestrator) UpdateConfig(config *ControlConfig) {
	if o == nil {
		return
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	o.config = config
}

// EvaluateSchedule checks schedule rules and returns action to take (if any).
func (o *Orchestrator) EvaluateSchedule(lastError error) *ScheduleAction {
	if o == nil {
		return nil
	}

	o.mu.RLock()
	config := o.config
	iteration := o.iteration
	o.mu.RUnlock()

	if config == nil || len(config.Schedule) == 0 {
		return nil
	}

	for _, rule := range config.Schedule {
		if o.triggerMatches(rule.Trigger, iteration, lastError) {
			return &ScheduleAction{
				Action:  rule.Action,
				Mode:    rule.Mode,
				Backend: rule.Backend,
				Reason:  rule.Reason,
			}
		}
	}

	return nil
}

// triggerMatches checks if a trigger condition is satisfied.
func (o *Orchestrator) triggerMatches(trigger ScheduleTrigger, iteration int, lastError error) bool {
	// Check every_iterations trigger
	if trigger.EveryIterations > 0 {
		if iteration > 0 && iteration%trigger.EveryIterations == 0 {
			return true
		}
	}

	// Check on_error trigger
	if trigger.OnError != "" && lastError != nil {
		errMsg := strings.ToLower(lastError.Error())
		triggerPattern := strings.ToLower(trigger.OnError)
		if strings.Contains(errMsg, triggerPattern) {
			return true
		}
	}

	// Check "when" expressions
	if trigger.When != "" {
		o.mu.RLock()
		ctx := WhenContext{
			Iteration:      o.iteration,
			ErrorCount:     o.errorCount,
			ConsecErrors:   o.consecErrors,
			TotalCost:      o.totalCost,
			TotalTokens:    o.totalTokens,
			ElapsedMinutes: int(time.Since(o.startTime).Minutes()),
			HasError:       lastError != nil,
		}
		o.mu.RUnlock()

		if EvalWhen(trigger.When, ctx) {
			return true
		}
	}

	// Check cron expressions
	if trigger.Cron != "" {
		spec, err := ParseCron(trigger.Cron)
		if err == nil {
			now := time.Now()
			// Only trigger once per minute (check if we haven't checked in this minute)
			o.mu.Lock()
			shouldCheck := o.lastCronCheck.Minute() != now.Minute() ||
				o.lastCronCheck.Hour() != now.Hour() ||
				o.lastCronCheck.Day() != now.Day()
			if shouldCheck {
				o.lastCronCheck = now
			}
			o.mu.Unlock()

			if shouldCheck && spec.Matches(now) {
				return true
			}
		}
	}

	return false
}

// RecordError records an error for when-expression evaluation.
func (o *Orchestrator) RecordError(err error) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if err != nil {
		o.errorCount++
		o.consecErrors++
	} else {
		o.consecErrors = 0 // Reset consecutive errors on success
	}
}

// RecordCost records cost and tokens for when-expression evaluation.
func (o *Orchestrator) RecordCost(cost float64, tokens int) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.totalCost += cost
	o.totalTokens += tokens
}

// NextIteration increments the iteration counter.
func (o *Orchestrator) NextIteration() {
	if o == nil {
		return
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	o.iteration++
}
