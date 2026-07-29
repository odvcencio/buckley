package oneshot

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"m31labs.dev/buckley/v2/pkg/model"
	"m31labs.dev/buckley/v2/pkg/rules"
	"m31labs.dev/buckley/v2/pkg/tools"
	"m31labs.dev/buckley/v2/pkg/transparency"
)

const defaultMaxRetries = 3

const (
	evidenceRepairMaxIterations      = 8
	evidenceRepairMaxToolCalls       = 8
	evidenceRepairExplorationTimeout = 2 * time.Minute
	evidenceRepairSynthesisLead      = 30 * time.Second
	textRepairReasoningMaxTokens     = 512
)

// Framework provides a single execution engine for all oneshot commands.
// It replaces the duplicated Runner types in commit/, pr/, and rlm/.
//
// The framework routes execution based on which interface a definition implements:
//   - Definition    -> single-tool invoke+retry (commit, PR)
//   - RLMDefinition -> full RLM sub-agent with multi-turn tool access (review)
type Framework struct {
	invoker              ToolInvoker
	rlmRunner            RLMExecutor
	approvalCriticRunner RLMExecutor
	engine               *rules.Engine
}

// RLMExecutor runs a multi-turn agent task. Keeping the framework dependent on
// this narrow interface makes validation/retry behavior independently testable.
type RLMExecutor interface {
	Run(ctx context.Context, systemPrompt, task string, allowedTools []string, opts RLMExecutionOpts) (*RLMResult, error)
}

// RLMExecutionOpts is immutable execution metadata shared by every sub-agent
// invocation in one RunRLM call.
type RLMExecutionOpts struct {
	ReviewSnapshot       *model.ReviewSnapshot
	MaxIterations        int
	MaxToolCalls         int
	MaxVerificationCalls int
	MaxCostUSD           float64
	ExplorationTimeout   time.Duration
	SynthesisLead        time.Duration
	VerificationTimeout  time.Duration
	ModelID              string
	ReasoningEffort      string
	ReasoningMaxTokens   int
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
		invoker:              f.invoker,
		rlmRunner:            runner,
		approvalCriticRunner: f.approvalCriticRunner,
		engine:               f.engine,
	}
}

// WithApprovalCriticRunner uses a separately priced model for the independent
// approval gate while retaining the primary reviewer for all other outcomes.
func (f *Framework) WithApprovalCriticRunner(runner RLMExecutor) *Framework {
	return &Framework{
		invoker:              f.invoker,
		rlmRunner:            f.rlmRunner,
		approvalCriticRunner: runner,
		engine:               f.engine,
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

	// Incomplete reports that Value contains only salvaged work from an
	// interrupted RLM run. Callers may persist or display it, but must not treat
	// it as a validated result or approval.
	Incomplete bool

	// IncompleteReason records why validation could not finish.
	IncompleteReason string
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

	// MaxIterations overrides the definition's per-agent turn budget when
	// non-zero.
	MaxIterations int

	// MaxToolCalls bounds inspection and verification calls in one review pass.
	MaxToolCalls int

	// MaxVerificationCalls bounds expensive verification calls in one review
	// pass. Zero leaves verification bounded only by MaxToolCalls.
	MaxVerificationCalls int

	// MaxCostUSD is a hard best-effort cost ceiling shared by validation
	// retries and the approval critic. Zero leaves cost unconstrained.
	MaxCostUSD float64

	// ApprovalCriticReserveUSD protects enough of MaxCostUSD for an
	// independent approval critic. It is unused for non-approval results.
	ApprovalCriticReserveUSD float64

	// ApprovalCriticReserve protects command time for an independent approval
	// critic. It is unused when the review does not enable a critic.
	ApprovalCriticReserve time.Duration

	// CriticMaxIterations bounds the approval critic's model turns.
	CriticMaxIterations int

	// CriticMaxToolCalls bounds the approval critic's inspection calls.
	CriticMaxToolCalls int

	// CriticExplorationTimeout caps approval-critic evidence collection.
	CriticExplorationTimeout time.Duration

	// CriticSynthesisLead reserves approval-critic final response time.
	CriticSynthesisLead time.Duration

	// SynthesisLead reserves command time for a complete final review.
	SynthesisLead time.Duration

	// ExplorationTimeout caps evidence collection before final synthesis.
	ExplorationTimeout time.Duration

	// VerificationTimeout caps each snapshot verification command.
	VerificationTimeout time.Duration

	// ReasoningEffort overrides the runner default for this review plan.
	ReasoningEffort string

	// ReasoningMaxTokens bounds hidden reasoning for each model turn.
	ReasoningMaxTokens int

	// ModelID overrides the primary runner model for this review plan.
	// A separately configured approval critic keeps its own model.
	ModelID string

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
	executionOpts := RLMExecutionOpts{
		ReviewSnapshot:       snapshot,
		MaxToolCalls:         opts.MaxToolCalls,
		MaxVerificationCalls: opts.MaxVerificationCalls,
		ExplorationTimeout:   opts.ExplorationTimeout,
		SynthesisLead:        opts.SynthesisLead,
		VerificationTimeout:  opts.VerificationTimeout,
		ModelID:              opts.ModelID,
		ReasoningEffort:      opts.ReasoningEffort,
		ReasoningMaxTokens:   opts.ReasoningMaxTokens,
	}
	if opts.MaxIterations > 0 {
		executionOpts.MaxIterations = opts.MaxIterations
	} else if budget, ok := def.(RLMExecutionBudget); ok {
		executionOpts.MaxIterations = budget.MaxRLMIterations()
	}

	primaryBudget := opts.MaxCostUSD
	if primaryBudget > 0 && opts.ApprovalCriticReserveUSD > 0 {
		primaryBudget -= opts.ApprovalCriticReserveUSD
		if primaryBudget <= 0 {
			return result, fmt.Errorf("approval critic reserve $%.4f leaves no primary review budget for %q", opts.ApprovalCriticReserveUSD, def.Name())
		}
	}
	executionOpts.MaxCostUSD = primaryBudget
	primaryCtx, cancelPrimary := contextWithReservedTime(ctx, opts.ApprovalCriticReserve)
	primary := f.runValidatedRLMPhase(primaryCtx, f.rlmRunner, def, def.SystemPrompt(), basePrompt, maxRetries, "primary", executionOpts)
	cancelPrimary()
	result.Attempts = primary.attempts
	result.PrimaryAttempts = primary.attempts
	traceAttempts := append([]transparency.TraceAttempt(nil), primary.traces...)
	result.Trace = transparency.AggregateTraceAttempts(traceAttempts)
	if primary.err != nil {
		result.Value = primary.value
		result.Incomplete = true
		result.IncompleteReason = primary.err.Error()
		return result, primary.err
	}

	result.Value = primary.value
	criticDef, hasCritic := def.(RLMApprovalCritic)
	if !hasCritic || !criticDef.RequiresApprovalCritic(primary.value) {
		result.Value = primary.value
		return result, nil
	}

	criticPrompt, err := criticDef.BuildApprovalCriticPrompt(basePrompt, primary.value)
	if err != nil {
		result.Incomplete = true
		result.IncompleteReason = err.Error()
		return result, fmt.Errorf("build approval critic prompt for %q: %w", def.Name(), err)
	}
	criticExecutionOpts := executionOpts
	if opts.CriticMaxIterations > 0 {
		criticExecutionOpts.MaxIterations = opts.CriticMaxIterations
	}
	if opts.CriticMaxToolCalls > 0 {
		criticExecutionOpts.MaxToolCalls = opts.CriticMaxToolCalls
	}
	if opts.CriticExplorationTimeout > 0 {
		criticExecutionOpts.ExplorationTimeout = opts.CriticExplorationTimeout
	}
	if opts.CriticSynthesisLead > 0 {
		criticExecutionOpts.SynthesisLead = opts.CriticSynthesisLead
	}
	if opts.MaxCostUSD > 0 {
		criticExecutionOpts.MaxCostUSD = opts.MaxCostUSD - primary.cost
		if criticExecutionOpts.MaxCostUSD <= 0 {
			result.Incomplete = true
			result.IncompleteReason = "review cost budget exhausted before approval critic"
			return result, fmt.Errorf("%s for %q", result.IncompleteReason, def.Name())
		}
	}
	criticRunner := f.approvalCriticRunner
	if criticRunner == nil {
		criticRunner = f.rlmRunner
	} else {
		criticExecutionOpts.ModelID = ""
	}
	critic := f.runValidatedRLMPhase(
		ctx,
		criticRunner,
		def,
		criticDef.ApprovalCriticSystemPrompt(),
		criticPrompt,
		maxRetries,
		"approval critic",
		criticExecutionOpts,
	)
	result.Attempts += critic.attempts
	result.CriticAttempts = critic.attempts
	traceAttempts = append(traceAttempts, critic.traces...)
	result.Trace = transparency.AggregateTraceAttempts(traceAttempts)
	if critic.err != nil {
		if hasRLMValue(critic.value) {
			result.Value = critic.value
		}
		result.Incomplete = true
		result.IncompleteReason = critic.err.Error()
		return result, critic.err
	}

	// The primary pass was an approval, so the independent critic is always at
	// least as conservative: its validated result (approval or otherwise) is the
	// authoritative final review.
	result.Value = critic.value
	return result, nil
}

func hasRLMValue(value any) bool {
	if value == nil {
		return false
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return !reflected.IsNil()
	default:
		return true
	}
}

func contextWithReservedTime(ctx context.Context, reserve time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if reserve <= 0 {
		return context.WithCancel(ctx)
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return context.WithCancel(ctx)
	}
	return context.WithDeadline(ctx, deadline.Add(-reserve))
}

type rlmPhaseResult struct {
	value    any
	traces   []transparency.TraceAttempt
	attempts int
	cost     float64
	err      error
}

type rlmValidationRetryMode uint8

const (
	rlmValidationRetryFull rlmValidationRetryMode = iota
	rlmValidationRetryText
	rlmValidationRetryEvidence
	rlmValidationRetryClean
)

func (f *Framework) runValidatedRLMPhase(
	ctx context.Context,
	runner RLMExecutor,
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
	retryMode := rlmValidationRetryFull
	attemptLimit := maxRetries
	cleanRepairUsed := false

	for attempt := 0; attempt < attemptLimit; attempt++ {
		traceIndex := -1
		attemptOpts := executionOpts
		if attempt > 0 {
			switch retryMode {
			case rlmValidationRetryText, rlmValidationRetryClean:
				attemptOpts.MaxIterations = 1
				attemptOpts.MaxToolCalls = 0
				attemptOpts.ExplorationTimeout = 0
				attemptOpts.ReasoningMaxTokens = boundedPositiveLimit(
					attemptOpts.ReasoningMaxTokens,
					textRepairReasoningMaxTokens,
				)
			case rlmValidationRetryEvidence:
				attemptOpts.MaxIterations = boundedPositiveLimit(attemptOpts.MaxIterations, evidenceRepairMaxIterations)
				attemptOpts.MaxToolCalls = boundedPositiveLimit(attemptOpts.MaxToolCalls, evidenceRepairMaxToolCalls)
				attemptOpts.ExplorationTimeout = boundedPositiveDuration(
					attemptOpts.ExplorationTimeout,
					evidenceRepairExplorationTimeout,
				)
				attemptOpts.SynthesisLead = boundedPositiveDuration(
					attemptOpts.SynthesisLead,
					evidenceRepairSynthesisLead,
				)
			}
		}
		if executionOpts.MaxCostUSD > 0 {
			attemptOpts.MaxCostUSD = executionOpts.MaxCostUSD - result.cost
			if attemptOpts.MaxCostUSD <= 0 {
				result.err = fmt.Errorf("RLM %s cost budget exhausted for %q after %d attempts", phase, def.Name(), result.attempts)
				return result
			}
		}
		result.attempts++
		rlmResult, err := runner.Run(ctx, systemPrompt, userPrompt, def.AllowedTools(), attemptOpts)
		if rlmResult != nil && rlmResult.Trace != nil {
			result.cost += rlmResult.Trace.Cost
			result.traces = append(result.traces, transparency.TraceAttempt{
				Phase:   phase,
				Attempt: attempt + 1,
				Trace:   rlmResult.Trace,
			})
			traceIndex = len(result.traces) - 1
		}
		if err != nil {
			if rlmResult != nil && strings.TrimSpace(rlmResult.Response) != "" {
				// Preserve parseable partial work for callers that explicitly
				// handle incomplete results. Keep an earlier rejected response
				// when the new attempt contains only an execution salvage.
				if result.value == nil || !rlmResult.Incomplete {
					result.value, _ = def.ParseResult(rlmResult.Response)
				}
			}
			result.err = fmt.Errorf("RLM %s execution failed for %q: %w", phase, def.Name(), err)
			return result
		}
		if rlmResult == nil {
			lastErr = fmt.Errorf("RLM runner returned no result")
			retryMode = rlmValidationRetryFull
		} else if incompleteErr := incompleteRLMOutputError(rlmResult); incompleteErr != nil {
			// Preserve the rejected value for diagnostics, but never validate or
			// accept an incomplete provider response.
			result.value, _ = def.ParseResult(rlmResult.Response)
			lastErr = incompleteErr
			retryMode = rlmValidationRetryClean
			if cleanRepairUsed {
				result.err = rlmValidationFailure(phase, def.Name(), result.attempts, lastErr)
				return result
			}
			cleanRepairUsed = true
			if requiredAttempts := attempt + 3; attemptLimit < requiredAttempts {
				attemptLimit = requiredAttempts
			}
		} else {
			result.value, lastErr = def.ParseResult(rlmResult.Response)
			if lastErr != nil {
				retryMode = rlmValidationRetryText
			}
			if lastErr == nil {
				if validator, ok := def.(RLMResultValidator); ok {
					lastErr = validator.ValidateResult(result.value)
					if lastErr != nil {
						retryMode = rlmValidationRetryText
						if IsRLMExecutionEvidenceRequired(lastErr) {
							retryMode = rlmValidationRetryEvidence
						}
					}
				}
			}
			if lastErr == nil {
				if validator, ok := def.(RLMExecutionValidator); ok {
					lastErr = validator.ValidateRLMExecution(result.value, rlmResult)
					if lastErr != nil {
						retryMode = rlmValidationRetryText
						if IsRLMExecutionEvidenceRequired(lastErr) {
							retryMode = rlmValidationRetryEvidence
						}
					}
				}
			}
		}
		if lastErr == nil {
			return result
		}
		if traceIndex >= 0 {
			result.traces[traceIndex].ValidationError = strings.TrimSpace(lastErr.Error())
		}

		userPrompt = buildRLMValidationRetryPrompt(
			basePrompt,
			rlmResult,
			phase,
			lastErr,
			retryMode,
		)
	}

	result.err = rlmValidationFailure(phase, def.Name(), result.attempts, lastErr)
	return result
}

func rlmValidationFailure(phase, definition string, attempts int, err error) error {
	return fmt.Errorf("%s review validation failed after %d attempts for %q: %w", phase, attempts, definition, err)
}

func incompleteRLMOutputError(result *RLMResult) error {
	if result == nil {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(result.FinishReason)) {
	case "length", "max_tokens":
		return fmt.Errorf("provider stopped the response at its token limit")
	case "tool_call", "tool_calls":
		return fmt.Errorf("provider stopped the final response to request a tool")
	}
	if hasUnclosedToolCallMarkup(result.Response) {
		return fmt.Errorf("provider returned unfinished tool-call markup")
	}
	if endsWithToolCallMarkup(result.Response) {
		return fmt.Errorf("provider returned a tool call as final text")
	}
	return nil
}

func hasUnclosedToolCallMarkup(response string) bool {
	for _, delimiters := range [][2]string{
		{"<tool_call>", "</tool_call>"},
		{"<|tool_call_begin|>", "<|tool_call_end|>"},
		{"<|tool_calls_section_begin|>", "<|tool_calls_section_end|>"},
	} {
		if strings.Count(response, delimiters[0]) > strings.Count(response, delimiters[1]) {
			return true
		}
	}
	return false
}

func endsWithToolCallMarkup(response string) bool {
	response = strings.TrimSpace(response)
	for _, closing := range []string{
		"</tool_call>",
		"<|tool_call_end|>",
		"<|tool_calls_section_end|>",
	} {
		if strings.HasSuffix(response, closing) {
			return true
		}
	}
	return false
}

func buildRLMValidationRetryPrompt(
	basePrompt string,
	previous *RLMResult,
	phase string,
	validationErr error,
	retryMode rlmValidationRetryMode,
) string {
	rejection := "QUALITY GATE: The previous " + phase + " review was rejected: " +
		strings.TrimSpace(validationErr.Error()) + ". "
	if retryMode == rlmValidationRetryText && previous != nil && strings.TrimSpace(previous.Response) != "" {
		return basePrompt + "\n\n" + rejection +
			"First apply every exact correction named in the rejection, then repair format or schema issues. " +
			"If one finding ID appears in both Blockers and Suggestions, preserve the finding and its evidence; remove it only from the list that conflicts with its severity (CRITICAL or MAJOR means Blockers, MINOR means Suggestions). " +
			"If coverage ledger paths differ, preserve valid File entries, add every exact missing path, remove every exact unexpected path, and reconcile the final ledger against the rejection. " +
			"Self-check the final review against the rejection before returning. " +
			"Repair the prior review without new tool calls. Preserve judgments and evidence that satisfy the gate. " +
			"Treat Falsification, Findings, Remarks, Grade, and Verdict as one coupled outcome. " +
			"If the conclusion is DISPROVED or UNRESOLVED, replace Findings with `None.` and remove every finding ID from Blockers and Suggestions. " +
			"Move a non-defect observation to Remarks. " +
			"If a current defect is demonstrated, make that defect the strongest plausible failure, use conclusion PROVED, and keep a non-approval verdict. " +
			"If verification is PENDING, NOT_RUN, UNAVAILABLE, or UNKNOWN, use Grade B and a non-approval NEEDS DISCUSSION verdict unless independent evidence proves a defect. " +
			"For that verdict, write `- **Recommendation**: NEEDS DISCUSSION` and `- **Blockers**: NONE`. Never put NEEDS DISCUSSION in Blockers. " +
			"Never return findings with a DISPROVED or UNRESOLVED conclusion. " +
			"Return one complete review in the required format.\n\nPRIOR REVIEW:\n" +
			previous.Response
	}
	if retryMode == rlmValidationRetryEvidence && previous != nil && strings.TrimSpace(previous.Response) != "" {
		return basePrompt + "\n\n" + rejection +
			"Gather only the missing evidence with the available tools. Run each required verification before synthesis. " +
			"Do not repeat inspection unless new evidence contradicts the prior review. " +
			"Return one complete review in the required format.\n\nPRIOR REVIEW:\n" +
			previous.Response
	}
	if retryMode == rlmValidationRetryClean && previous != nil && strings.TrimSpace(previous.Response) != "" {
		rejected := sanitizeCleanRepairResponse(previous.Response)
		return basePrompt + "\n\n" + rejection +
			"Complete one clean repair with no tool calls. Use only the supplied evidence. " +
			"Do not emit tool-call markup, tool-call JSON, progress text, or a plan. " +
			"Start with the required review format. Return the complete final review.\n\nREJECTED RESPONSE:\n" +
			rejected
	}
	return basePrompt + "\n\n" + rejection +
		"Re-run the review from the supplied evidence and return a complete, internally consistent review in the required format."
}

func sanitizeCleanRepairResponse(response string) string {
	for _, delimiters := range [][2]string{
		{"<tool_call>", "</tool_call>"},
		{"<|tool_call_begin|>", "<|tool_call_end|>"},
		{"<|tool_calls_section_begin|>", "<|tool_calls_section_end|>"},
	} {
		response = removeDelimitedToolCallBlocks(response, delimiters[0], delimiters[1])
	}
	response = strings.TrimSpace(response)
	if response == "" {
		return "[The rejected tool request was removed. Reconstruct the review from the supplied evidence.]"
	}
	return response
}

func removeDelimitedToolCallBlocks(response, opening, closing string) string {
	for {
		start := strings.Index(response, opening)
		if start < 0 {
			return response
		}
		afterOpening := response[start+len(opening):]
		end := strings.Index(afterOpening, closing)
		if end < 0 {
			return response[:start]
		}
		response = response[:start] + afterOpening[end+len(closing):]
	}
}

func boundedPositiveLimit(value, limit int) int {
	if value <= 0 || value > limit {
		return limit
	}
	return value
}

func boundedPositiveDuration(value, limit time.Duration) time.Duration {
	if value <= 0 || value > limit {
		return limit
	}
	return value
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
