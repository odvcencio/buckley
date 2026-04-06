package pr

import (
	"context"
	"fmt"

	"github.com/odvcencio/buckley/pkg/oneshot"
	"github.com/odvcencio/buckley/pkg/transparency"
)

// Runner executes the PR generation flow.
type Runner struct {
	invoker        *oneshot.DefaultInvoker
	ledger         *transparency.CostLedger
	streamCallback oneshot.StreamCallback
}

// RunnerConfig configures the PR runner.
type RunnerConfig struct {
	Invoker *oneshot.DefaultInvoker
	Ledger  *transparency.CostLedger

	// StreamCallback is called for each streaming chunk (optional).
	// When set, enables streaming mode to show thinking progress.
	StreamCallback oneshot.StreamCallback
}

// NewRunner creates a PR runner.
func NewRunner(cfg RunnerConfig) *Runner {
	return &Runner{
		invoker:        cfg.Invoker,
		ledger:         cfg.Ledger,
		streamCallback: cfg.StreamCallback,
	}
}

// RunResult contains the results of a PR generation.
type RunResult struct {
	// PR is the generated pull request (if successful)
	PR *PRResult

	// Context contains the assembled context
	Context *Context

	// Trace contains full transparency data
	Trace *transparency.Trace

	// ContextAudit shows what context was sent
	ContextAudit *transparency.ContextAudit

	// Error if generation failed
	Error error
}

// Run executes the full PR generation flow.
func (r *Runner) Run(ctx context.Context, opts ContextOptions) (*RunResult, error) {
	result := &RunResult{}

	// 1. Assemble context (transparent)
	prCtx, audit, err := AssembleContext(opts)
	if err != nil {
		return nil, fmt.Errorf("assemble context: %w", err)
	}
	result.Context = prCtx
	result.ContextAudit = audit

	// 2. Build prompt (rich context, no format demands)
	userPrompt := BuildPrompt(prCtx)
	systemPrompt := SystemPrompt()

	// 3. Invoke model with tool (streaming if callback provided)
	var invokeResult *oneshot.Result
	var trace *transparency.Trace
	if r.streamCallback != nil {
		invokeResult, trace, err = r.invoker.InvokeStream(ctx, systemPrompt, userPrompt, GeneratePRTool, audit, r.streamCallback)
	} else {
		invokeResult, trace, err = r.invoker.InvokeWithRetry(ctx, systemPrompt, userPrompt, GeneratePRTool, audit)
	}
	result.Trace = trace

	if err != nil {
		result.Error = err
		return result, nil // Return result with error for transparency
	}

	// 4. Check if we got a tool call
	if !invokeResult.HasToolCall() {
		result.Error = &transparency.NoToolCallError{
			Expected: "generate_pull_request",
			Got:      "text response",
		}
		return result, nil
	}

	// 5. Unmarshal the structured result - NO PARSING!
	var pr PRResult
	if err := invokeResult.ToolCall.Unmarshal(&pr); err != nil {
		result.Error = fmt.Errorf("unmarshal tool call: %w", err)
		return result, nil
	}

	// 6. Validate the result
	if err := pr.Validate(); err != nil {
		result.Error = fmt.Errorf("invalid PR: %w", err)
		return result, nil
	}

	result.PR = &pr
	return result, nil
}

// RunSimple is a convenience method for simple usage.
func (r *Runner) RunSimple(ctx context.Context) (*PRResult, error) {
	result, err := r.Run(ctx, DefaultContextOptions())
	if err != nil {
		return nil, err
	}
	if result.Error != nil {
		return nil, result.Error
	}
	return result.PR, nil
}
