package pr

import (
	"context"
	"fmt"
	"strings"

	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/transparency"
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

// maxParseAttempts bounds how many times Run re-invokes the model when the
// response is missing a tool call or its arguments cannot be decoded/validated.
// Tool-call JSON from non-Anthropic models is intermittently malformed, so a
// bounded re-invoke (each with a corrective hint) makes generation reliable
// without masking a persistently broken response.
const maxParseAttempts = 3

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

	// 2. Build prompts (rich context, no format demands)
	systemPrompt := SystemPrompt()
	baseUserPrompt := BuildPrompt(prCtx)

	// 3. Invoke with a bounded parse-retry loop. InvokeWithRetry handles
	// transport/API errors; this loop additionally re-invokes when the model
	// returns text instead of a tool call, or emits tool-call arguments that
	// cannot be decoded or fail validation.
	var lastErr error
	for attempt := 0; attempt < maxParseAttempts; attempt++ {
		userPrompt := baseUserPrompt
		if attempt > 0 && lastErr != nil {
			userPrompt = baseUserPrompt + "\n\nThe previous attempt could not be used: " +
				strings.TrimSpace(lastErr.Error()) + ". Call the " + GeneratePRTool.Name +
				" tool again with valid JSON — arrays must be JSON arrays of strings, " +
				"booleans must be true/false, and numbers must contain no spaces or separators."
		}

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
			return result, nil // transport error: surface immediately for transparency
		}

		if !invokeResult.HasToolCall() {
			lastErr = fmt.Errorf("model returned text instead of calling %s", GeneratePRTool.Name)
			continue
		}

		pr, decodeErr := decodePRResult(invokeResult.ToolCall.Arguments)
		if decodeErr != nil {
			lastErr = fmt.Errorf("decode tool call: %w", decodeErr)
			continue
		}

		if err := pr.Validate(); err != nil {
			lastErr = fmt.Errorf("invalid PR: %w", err)
			continue
		}

		result.PR = &pr
		return result, nil
	}

	result.Error = fmt.Errorf("unmarshal tool call: %w", lastErr)
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
