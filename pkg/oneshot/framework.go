package oneshot

import (
	"context"
	"fmt"
	"strings"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/rules"
	"m31labs.dev/buckley/pkg/tools"
	"m31labs.dev/buckley/pkg/transparency"
)

const defaultMaxRetries = 3

// Framework provides a single execution engine for all oneshot commands.
// It replaces the duplicated Runner types in commit/, pr/, and rlm/.
//
// The framework routes execution based on which interface a definition implements:
//   - Definition    -> single-tool invoke+retry (commit, PR)
//   - RLMDefinition -> full RLM sub-agent with multi-turn tool access (review)
type Framework struct {
	invoker   ToolInvoker
	rlmRunner RLMExecutor
	engine    *rules.Engine
}

// RLMExecutor runs a multi-turn agent task. Keeping the framework dependent on
// this narrow interface makes validation/retry behavior independently testable.
type RLMExecutor interface {
	Run(ctx context.Context, systemPrompt, task string, allowedTools []string, opts RLMExecutionOpts) (*RLMResult, error)
}

// RLMExecutionOpts is immutable execution metadata shared by every sub-agent
// invocation in one RunRLM call.
type RLMExecutionOpts struct {
	ReviewSnapshot *model.ReviewSnapshot
	MaxIterations  int
}

// ToolInvoker runs a single tool-shaped one-shot model invocation.
type ToolInvoker interface {
	Invoke(ctx context.Context, systemPrompt, userPrompt string, tool tools.Definition, audit *transparency.ContextAudit) (*Result, *transparency.Trace, error)
}

// NewFramework creates a new oneshot framework.
// The invoker is required for Definition-based commands.
// Use WithRLMRunner to enable RLMDefinition-based commands.
func NewFramework(invoker ToolInvoker, engine *rules.Engine) *Framework {
	return &Framework{
		invoker: invoker,
		engine:  engine,
	}
}

// WithRLMRunner returns a copy of the framework with the given RLM runner.
// This enables execution of RLMDefinition-based commands (e.g., review).
func (f *Framework) WithRLMRunner(runner RLMExecutor) *Framework {
	return &Framework{
		invoker:   f.invoker,
		rlmRunner: runner,
		engine:    f.engine,
	}
}

// RunOpts configures a single framework execution.
type RunOpts struct {
	// ContextOpts controls context gathering behavior.
	ContextOpts ContextOpts

	// MaxRetries overrides the default retry count.
	// If zero, uses arbiter strategy or default (3).
	MaxRetries int

	// Guidance is optional extra text appended to the user prompt on retry
	// when the model fails to call the tool.
	Guidance string
}

// RunResult contains the outcome of a framework execution.
type RunResult struct {
	// Value is the unmarshalled result (typed per Definition).
	Value any

	// Trace contains transparency data from the invocation.
	Trace *transparency.Trace

	// ContextAudit shows what context was gathered.
	ContextAudit *transparency.ContextAudit

	// Attempts is the total number of model invocations across the primary and
	// approval-critic phases. Validation retries are included.
	Attempts int

	// PrimaryAttempts is the number of invocations used to obtain a valid
	// primary result.
	PrimaryAttempts int

	// CriticAttempts is the number of independent approval-critic invocations.
	// It is zero when the primary result did not require a critic.
	CriticAttempts int
}

// Run executes a oneshot command using the unified pipeline:
//  1. Build context from the definition's sources
//  2. Build system and user prompts
//  3. Query arbiter for retry config (if engine available)
//  4. Invoke model in a retry loop with validation
//  5. Return the validated, unmarshalled result
func (f *Framework) Run(ctx context.Context, def Definition, opts RunOpts) (*RunResult, error) {
	if f.invoker == nil {
		return nil, fmt.Errorf("invoker is required")
	}

	// 1. Build context from definition's sources
	gathered, err := BuildContext(def.ContextSources(), opts.ContextOpts)
	if err != nil {
		return nil, fmt.Errorf("build context: %w", err)
	}

	// Build a transparency audit from gathered sources
	audit := transparency.NewContextAudit()
	for label, content := range gathered.Sources {
		audit.Add(label, contextEstimateTokens(content))
	}

	// 2. Build prompts
	systemPrompt := def.SystemPrompt()
	baseUserPrompt := def.BuildPrompt(gathered)

	// 3. Determine retry config from arbiter
	maxRetries := f.resolveMaxRetries(def.Name(), opts.MaxRetries)

	// 4. Invoke with retry loop
	tool := def.Tool()
	userPrompt := baseUserPrompt
	var lastTrace *transparency.Trace
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		result, trace, invokeErr := f.invoker.Invoke(ctx, systemPrompt, userPrompt, tool, audit)
		lastTrace = trace

		if invokeErr != nil {
			return nil, fmt.Errorf("invoke failed: %w", invokeErr)
		}

		// Check if model called the tool
		if result == nil || result.ToolCall == nil {
			lastErr = fmt.Errorf("model did not call the %s tool", tool.Name)
			userPrompt = baseUserPrompt + "\n\nIMPORTANT: You MUST call the " + tool.Name + " tool. Do not reply with plain text."
			continue
		}

		// 5. Validate
		if err := def.Validate(result.ToolCall.Arguments); err != nil {
			lastErr = fmt.Errorf("validation: %w", err)
			userPrompt = baseUserPrompt + "\n\nThe previous response failed validation: " + strings.TrimSpace(err.Error()) + ". Fix and call " + tool.Name + " again."
			continue
		}

		// Unmarshal
		value, err := def.Unmarshal(result.ToolCall.Arguments)
		if err != nil {
			lastErr = fmt.Errorf("unmarshal: %w", err)
			userPrompt = baseUserPrompt + "\n\nThe tool call arguments must be valid JSON matching the schema for " + tool.Name + "."
			continue
		}

		return &RunResult{
			Value:        value,
			Trace:        trace,
			ContextAudit: audit,
		}, nil
	}

	// All retries exhausted. Surface the last failure reason: without it,
	// every exhausted run reads identically and the operator is left to
	// guess whether the model skipped the tool call, tripped validation,
	// or emitted malformed JSON.
	if lastErr != nil {
		return &RunResult{
			Trace:        lastTrace,
			ContextAudit: audit,
		}, fmt.Errorf("failed after %d attempts for command %q: last attempt: %w", maxRetries, def.Name(), lastErr)
	}
	return &RunResult{
		Trace:        lastTrace,
		ContextAudit: audit,
	}, fmt.Errorf("failed after %d attempts for command %q", maxRetries, def.Name())
}

// RLMRunOpts configures an RLM framework execution.
type RLMRunOpts struct {
	// UserPrompt is the task prompt sent to the RLM agent.
	UserPrompt string

	// Audit is an optional pre-built context audit for transparency.
	Audit *transparency.ContextAudit

	// MaxRetries overrides the Arbiter oneshot policy when non-zero.
	MaxRetries int

	// SnapshotPolicy captures the exact Git state that native review
	// verification may inspect. It is captured once before the primary pass and
	// reused unchanged for validation retries and the approval critic.
	SnapshotPolicy model.ReviewSnapshotPolicy

	// ReviewSnapshot supplies an already captured descriptor. It is primarily
	// useful to callers that coordinate capture outside the framework and to
	// deterministic integration tests.
	ReviewSnapshot *model.ReviewSnapshot
}

// RunRLM executes an RLM-based oneshot command using the full sub-agent pipeline:
//  1. Validate the RLM runner is configured
//  2. Execute the sub-agent with multi-turn tool access
//  3. Parse the free-form response into typed output
//  4. Retry incomplete or inconsistent results through semantic validation
//  5. Send validated approvals through an independent critic when required
//  6. Return the final result with transparency and attempt counts
func (f *Framework) RunRLM(ctx context.Context, def RLMDefinition, opts RLMRunOpts) (*RunResult, error) {
	if f.rlmRunner == nil {
		return nil, fmt.Errorf("RLM runner is required for command %q (configure with WithRLMRunner)", def.Name())
	}

	// Build audit if not provided
	audit := opts.Audit
	if audit == nil {
		audit = transparency.NewContextAudit()
	}
	if opts.UserPrompt != "" {
		audit.Add("user prompt", contextEstimateTokens(opts.UserPrompt))
	}

	basePrompt := opts.UserPrompt
	maxRetries := f.resolveMaxRetries(def.Name(), opts.MaxRetries)
	result := &RunResult{ContextAudit: audit}
	snapshot := opts.ReviewSnapshot
	if snapshot == nil && opts.SnapshotPolicy.Mode != model.ReviewSnapshotNone {
		var err error
		snapshot, err = model.CaptureReviewSnapshot(ctx, "", opts.SnapshotPolicy)
		if err != nil {
			return result, fmt.Errorf("capture review verification snapshot for %q: %w", def.Name(), err)
		}
	}
	if snapshot != nil && opts.SnapshotPolicy.Mode != model.ReviewSnapshotNone && snapshot.Mode() != opts.SnapshotPolicy.Mode {
		return result, fmt.Errorf(
			"review verification snapshot mode %q does not match policy %q for %q",
			snapshot.Mode(), opts.SnapshotPolicy.Mode, def.Name(),
		)
	}
	if snapshot != nil {
		expectedCommit := strings.TrimSpace(opts.SnapshotPolicy.ExpectedCommit)
		if expectedCommit != "" && !strings.EqualFold(strings.TrimSpace(snapshot.Commit()), expectedCommit) {
			return result, fmt.Errorf(
				"review verification snapshot commit %q does not match expected commit %q for %q",
				snapshot.Commit(), expectedCommit, def.Name(),
			)
		}
	}
	executionOpts := RLMExecutionOpts{ReviewSnapshot: snapshot}
	if budget, ok := def.(RLMExecutionBudget); ok {
		executionOpts.MaxIterations = budget.MaxRLMIterations()
	}

	primary := f.runValidatedRLMPhase(ctx, def, def.SystemPrompt(), basePrompt, maxRetries, "primary", executionOpts)
	result.Attempts = primary.attempts
	result.PrimaryAttempts = primary.attempts
	traceAttempts := append([]transparency.TraceAttempt(nil), primary.traces...)
	result.Trace = transparency.AggregateTraceAttempts(traceAttempts)
	if primary.err != nil {
		return result, primary.err
	}

	criticDef, hasCritic := def.(RLMApprovalCritic)
	if !hasCritic || !criticDef.RequiresApprovalCritic(primary.value) {
		result.Value = primary.value
		return result, nil
	}

	criticPrompt, err := criticDef.BuildApprovalCriticPrompt(basePrompt, primary.value)
	if err != nil {
		return result, fmt.Errorf("build approval critic prompt for %q: %w", def.Name(), err)
	}
	critic := f.runValidatedRLMPhase(
		ctx,
		def,
		criticDef.ApprovalCriticSystemPrompt(),
		criticPrompt,
		maxRetries,
		"approval critic",
		executionOpts,
	)
	result.Attempts += critic.attempts
	result.CriticAttempts = critic.attempts
	traceAttempts = append(traceAttempts, critic.traces...)
	result.Trace = transparency.AggregateTraceAttempts(traceAttempts)
	if critic.err != nil {
		return result, critic.err
	}

	// The primary pass was an approval, so the independent critic is always at
	// least as conservative: its validated result (approval or otherwise) is the
	// authoritative final review.
	result.Value = critic.value
	return result, nil
}

type rlmPhaseResult struct {
	value    any
	traces   []transparency.TraceAttempt
	attempts int
	err      error
}

func (f *Framework) runValidatedRLMPhase(
	ctx context.Context,
	def RLMDefinition,
	systemPrompt string,
	basePrompt string,
	maxRetries int,
	phase string,
	executionOpts RLMExecutionOpts,
) rlmPhaseResult {
	userPrompt := basePrompt
	var result rlmPhaseResult
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		result.attempts++
		rlmResult, err := f.rlmRunner.Run(ctx, systemPrompt, userPrompt, def.AllowedTools(), executionOpts)
		if rlmResult != nil && rlmResult.Trace != nil {
			result.traces = append(result.traces, transparency.TraceAttempt{
				Phase:   phase,
				Attempt: attempt + 1,
				Trace:   rlmResult.Trace,
			})
		}
		if err != nil {
			result.err = fmt.Errorf("RLM %s execution failed for %q: %w", phase, def.Name(), err)
			return result
		}
		if rlmResult == nil {
			lastErr = fmt.Errorf("RLM runner returned no result")
		} else {
			result.value, lastErr = def.ParseResult(rlmResult.Response)
			if lastErr == nil {
				if validator, ok := def.(RLMResultValidator); ok {
					lastErr = validator.ValidateResult(result.value)
				}
			}
			if lastErr == nil {
				if validator, ok := def.(RLMExecutionValidator); ok {
					lastErr = validator.ValidateRLMExecution(result.value, rlmResult)
				}
			}
		}
		if lastErr == nil {
			return result
		}

		userPrompt = basePrompt + "\n\nQUALITY GATE: The previous " + phase + " review was rejected: " +
			strings.TrimSpace(lastErr.Error()) +
			". Re-run the review from the supplied evidence and return a complete, internally consistent review in the required format."
	}

	result.value = nil
	result.err = fmt.Errorf("%s review validation failed after %d attempts for %q: %w", phase, maxRetries, def.Name(), lastErr)
	return result
}

// resolveMaxRetries determines the retry count from opts, arbiter, or defaults.
func (f *Framework) resolveMaxRetries(cmdName string, optsRetries int) int {
	if optsRetries > 0 {
		return optsRetries
	}

	// Try arbiter engine
	if f.engine != nil {
		result, err := f.engine.EvalStrategy("oneshot", "oneshot_policy", map[string]any{
			"command": cmdName,
		})
		if err == nil {
			if v, ok := result.Params["max_retries"]; ok {
				switch n := v.(type) {
				case float64:
					if int(n) > 0 {
						return int(n)
					}
				case int:
					if n > 0 {
						return n
					}
				}
			}
		}
	}

	return defaultMaxRetries
}
