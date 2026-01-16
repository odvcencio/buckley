// pkg/ralph/backend_internal.go
package ralph

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/conversation"
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

type headlessModelOverrider interface {
	SetModelOverride(modelID string)
}

type headlessOutputProvider interface {
	WaitForIdle(ctx context.Context) error
	LatestAssistantMessageID(ctx context.Context) (int64, error)
	LatestAssistantMessage(ctx context.Context, afterID int64) (string, int, int64, error)
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
		Model:   req.Model,
	}

	// Check if runner is available
	if b.runner == nil {
		result.Duration = time.Since(startTime)
		result.Error = fmt.Errorf("runner not initialized")
		return result, nil
	}

	if overrider, ok := b.runner.(headlessModelOverrider); ok {
		overrider.SetModelOverride(req.Model)
	}

	var beforeID int64
	if outputProvider, ok := b.runner.(headlessOutputProvider); ok {
		if id, err := outputProvider.LatestAssistantMessageID(ctx); err == nil {
			beforeID = id
		}
	}

	// Execute the prompt through the runner
	err := b.runner.ProcessInput(ctx, req.Prompt)

	if outputProvider, ok := b.runner.(headlessOutputProvider); ok {
		if waitErr := outputProvider.WaitForIdle(ctx); waitErr != nil && err == nil {
			err = waitErr
		}
		content, tokensOut, _, msgErr := outputProvider.LatestAssistantMessage(ctx, beforeID)
		if msgErr != nil && err == nil {
			err = msgErr
		}
		result.Output = content
		result.TokensOut = tokensOut
		if result.TokensOut == 0 && strings.TrimSpace(result.Output) != "" {
			result.TokensOut = conversation.CountTokens(result.Output)
		}
	}

	result.Duration = time.Since(startTime)

	if err != nil {
		result.Error = err
	}

	result.TokensIn = conversation.CountTokens(req.Prompt)

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
