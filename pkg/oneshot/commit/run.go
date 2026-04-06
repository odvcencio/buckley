package commit

import (
	"context"
	"fmt"

	"github.com/odvcencio/buckley/pkg/oneshot"
	"github.com/odvcencio/buckley/pkg/transparency"
)

// Runner executes the commit generation flow.
type Runner struct {
	invoker        *oneshot.DefaultInvoker
	ledger         *transparency.CostLedger
	streamCallback oneshot.StreamCallback
}

// RunnerConfig configures the commit runner.
type RunnerConfig struct {
	Invoker *oneshot.DefaultInvoker
	Ledger  *transparency.CostLedger

	// StreamCallback is called for each streaming chunk (optional).
	// When set, enables streaming mode to show thinking progress.
	StreamCallback oneshot.StreamCallback
}

// NewRunner creates a commit runner.
func NewRunner(cfg RunnerConfig) *Runner {
	return &Runner{
		invoker:        cfg.Invoker,
		ledger:         cfg.Ledger,
		streamCallback: cfg.StreamCallback,
	}
}

// RunResult contains the results of a commit generation.
type RunResult struct {
	// Commit is the generated commit message (if successful)
	Commit *CommitResult

	// Trace contains full transparency data
	Trace *transparency.Trace

	// ContextAudit shows what context was sent
	ContextAudit *transparency.ContextAudit

	// Warnings about potentially unintended files
	Warnings []Warning

	// Error if generation failed
	Error error
}

// Run executes the full commit generation flow.
func (r *Runner) Run(ctx context.Context, opts ContextOptions) (*RunResult, error) {
	result := &RunResult{}

	// 1. Assemble context (transparent)
	commitCtx, audit, err := AssembleContext(opts)
	if err != nil {
		return nil, fmt.Errorf("assemble context: %w", err)
	}
	result.ContextAudit = audit
	result.Warnings = commitCtx.Warnings

	// 2. Build prompt (rich context, no format demands)
	userPrompt := BuildPrompt(commitCtx)
	systemPrompt := SystemPrompt()

	// 3. Invoke model with tool (streaming if callback provided)
	var invokeResult *oneshot.Result
	var trace *transparency.Trace
	if r.streamCallback != nil {
		invokeResult, trace, err = r.invoker.InvokeStream(ctx, systemPrompt, userPrompt, GenerateCommitTool, audit, r.streamCallback)
	} else {
		invokeResult, trace, err = r.invoker.InvokeWithRetry(ctx, systemPrompt, userPrompt, GenerateCommitTool, audit)
	}
	result.Trace = trace

	if err != nil {
		result.Error = err
		return result, nil // Return result with error for transparency
	}

	// 4. Check if we got a tool call
	if !invokeResult.HasToolCall() {
		result.Error = &transparency.NoToolCallError{
			Expected: "generate_commit",
			Got:      "text response",
		}
		return result, nil
	}

	// 5. Unmarshal the structured result - NO PARSING!
	var commit CommitResult
	if err := invokeResult.ToolCall.Unmarshal(&commit); err != nil {
		result.Error = fmt.Errorf("unmarshal tool call: %w", err)
		return result, nil
	}

	// 6. Validate the result
	if err := commit.Validate(); err != nil {
		result.Error = fmt.Errorf("invalid commit: %w", err)
		return result, nil
	}

	result.Commit = &commit
	return result, nil
}

// RunSimple is a convenience method for simple usage.
func (r *Runner) RunSimple(ctx context.Context) (*CommitResult, error) {
	result, err := r.Run(ctx, DefaultContextOptions())
	if err != nil {
		return nil, err
	}
	if result.Error != nil {
		return nil, result.Error
	}
	return result.Commit, nil
}
