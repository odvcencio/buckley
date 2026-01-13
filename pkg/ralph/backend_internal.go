// pkg/ralph/backend_internal.go
package ralph

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// InternalOptions configures the internal Buckley backend.
type InternalOptions struct {
	// ExecutionModel is the model to use for task execution.
	ExecutionModel string

	// PlanningModel is the model to use for planning.
	PlanningModel string

	// ReasoningModel is the model to use for reasoning steps.
	ReasoningModel string

	// ApprovalMode controls tool approval behavior (ask/safe/auto/yolo).
	ApprovalMode string
}

// InternalBackend wraps the headless.Runner as a Backend implementation.
// This allows Ralph to use Buckley itself as an execution backend.
type InternalBackend struct {
	name      string
	runner    HeadlessRunner
	options   InternalOptions
	mu        sync.RWMutex
	available bool
}

// NewInternalBackend creates a new internal Buckley backend.
func NewInternalBackend(name string, runner HeadlessRunner, options InternalOptions) *InternalBackend {
	return &InternalBackend{
		name:      name,
		runner:    runner,
		options:   options,
		available: true,
	}
}

// Name returns the unique identifier for this backend.
func (b *InternalBackend) Name() string {
	return b.name
}

// Execute runs a prompt through the internal Buckley runner.
func (b *InternalBackend) Execute(ctx context.Context, req BackendRequest) (*BackendResult, error) {
	startTime := time.Now()

	result := &BackendResult{
		Backend: b.name,
	}

	// Check if runner is available
	if b.runner == nil {
		result.Duration = time.Since(startTime)
		result.Error = fmt.Errorf("runner not initialized")
		return result, nil
	}

	// Execute the prompt through the runner
	err := b.runner.ProcessInput(ctx, req.Prompt)

	result.Duration = time.Since(startTime)

	if err != nil {
		result.Error = err
	}

	// Note: TokensIn, TokensOut, Cost, CostEstimate are left as 0 for now.
	// These will be enhanced when we add telemetry integration.

	return result, nil
}

// Available returns true if the backend is ready to execute.
func (b *InternalBackend) Available() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.available
}

// SetAvailable sets the availability state of the backend.
// This can be used for rate limiting or maintenance windows.
func (b *InternalBackend) SetAvailable(available bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.available = available
}

// Options returns the configuration options for this backend.
func (b *InternalBackend) Options() InternalOptions {
	return b.options
}
