// pkg/ralph/backend.go
package ralph

import (
	"context"
	"time"
)

// Backend defines the interface for execution backends.
// Backends can be external CLIs (claude, codex) or internal Buckley.
type Backend interface {
	// Name returns the unique identifier for this backend.
	Name() string

	// Execute runs a prompt through the backend and returns the result.
	Execute(ctx context.Context, req BackendRequest) (*BackendResult, error)

	// Available returns true if the backend is ready to execute.
	// This checks for rate limits, quota availability, etc.
	Available() bool
}

// BackendRequest contains the parameters for a backend execution.
type BackendRequest struct {
	// Prompt is the task description to execute.
	Prompt string

	// Model is the resolved model identifier for this backend execution.
	Model string

	// SandboxPath is the working directory for the execution.
	SandboxPath string

	// Iteration is the current iteration number (1-indexed).
	Iteration int

	// SessionID is the unique identifier for the Ralph session.
	SessionID string

	// Context contains additional metadata for the execution.
	Context map[string]any
}

// BackendResult contains the outcome of a backend execution.
type BackendResult struct {
	// Backend is the name of the backend that produced this result.
	Backend string

	// Model is the model identifier used for this execution.
	Model string

	// Duration is how long the execution took.
	Duration time.Duration

	// TokensIn is the number of input tokens (0 if unavailable).
	TokensIn int

	// TokensOut is the number of output tokens (0 if unavailable).
	TokensOut int

	// Cost is the actual cost in dollars (0 if subscription-based).
	Cost float64

	// CostEstimate is the OpenRouter-based cost estimate.
	CostEstimate float64

	// FilesChanged lists paths of files modified during execution.
	FilesChanged []string

	// TestsPassed is the count of passing tests after execution.
	TestsPassed int

	// TestsFailed is the count of failing tests after execution.
	TestsFailed int

	// Output is the raw output from the backend.
	Output string

	// Error is the error encountered during execution, if any.
	Error error
}
