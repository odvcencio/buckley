package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"time"

	"m31labs.dev/buckley/pkg/agentloop/lifecycle"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/durability/modelstep"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/runledger"
)

// Lifecycle aliases keep the controller API ergonomic while the contract
// itself lives in a neutral subpackage so telemetry can consume it without an
// import cycle through pkg/model.
type LifecycleEvent = lifecycle.Event
type LifecycleEventType = lifecycle.EventType
type LifecycleObserver = lifecycle.Observer

const (
	LifecycleTurnStart     = lifecycle.TurnStart
	LifecycleAttemptStart  = lifecycle.AttemptStart
	LifecycleAttemptEnd    = lifecycle.AttemptEnd
	LifecycleStepStart     = lifecycle.StepStart
	LifecycleModelRequest  = lifecycle.ModelRequest
	LifecycleModelResponse = lifecycle.ModelResponse
	LifecycleModelError    = lifecycle.ModelError
	LifecycleToolCall      = lifecycle.ToolCall
	LifecycleToolStart     = lifecycle.ToolStart
	LifecycleToolResult    = lifecycle.ToolResult
	LifecycleToolError     = lifecycle.ToolError
	LifecycleTurnStopping  = lifecycle.TurnStopping
	LifecycleTurnEnd       = lifecycle.TurnEnd
)

// Controller is the shared turn engine (G6): one type that owns a full
// model-and-tool turn loop so callers stop hand-rolling the same seven
// responsibilities (build request, project, call model, backfill tool-call
// IDs, dispatch tools, append history, accumulate usage) with a guard on
// top. It is a thin orchestration layer over pieces callers already own
// (their conversation store, their tool registry, their model client); it
// does not replace pkg/agentloop's Governor, it consults one every round.
//
// Two steps are Controller-owned and identical for every caller: request
// projection (ProjectForContinuation) and tool-call ID backfill
// (BackfillToolCallIDs). Three steps are pluggable hooks because callers
// genuinely differ in how they store history and dispatch tools:
// RequestBuilder, ToolDispatcher, HistorySink.
type Controller struct {
	cfg    ControllerConfig
	runMu  sync.Mutex
	totals controllerTotals
	// lifecycleSeq is protected by runMu because Controller serializes all
	// Run continuations. It gives live consumers a stable order even when a
	// surface calls Run again for a nudge or capability retry.
	lifecycleSeq uint64
	// lifecycleTurnStarted/completed and runAttempts are protected by runMu.
	// A caller-managed logical turn (ACP) can span several Run attempts while
	// retaining one TurnID and exactly one turn/start + turn/end pair.
	lifecycleTurnStarted   bool
	lifecycleTurnCompleted bool
	runAttempts            int
}

// controllerTotals is the turn-lifetime accounting owned by one Controller.
// ACP may call Run repeatedly for nudges or capability recovery; those calls
// are continuations of the same turn and must not reset all-in limits.
type controllerTotals struct {
	usage         model.Usage
	costUSD       float64
	modelRequests int
	toolCalls     int
	progress      ProgressSnapshot
	startedAt     time.Time
}

// RequestBuilder returns the base chat request for one round. Its Messages
// field must carry the full, unprojected working transcript (the caller's
// own conversation store or message slice); Controller replaces Messages
// with the projected result before calling the model. round is the 1-based
// round number the Governor assigned this call (see Governor.Rounds).
type RequestBuilder func(ctx context.Context, round int) (model.ChatRequest, error)

// ModelCaller performs one model turn against an already-projected request.
// A caller may return a non-nil response alongside an error when the provider
// emitted billable material before failing; Controller persists/accounts that
// partial response and refuses to treat it as a fresh retry. useContinuation
// reports whether Controller resolved a provider continuation window for this
// round (see ControllerConfig.Continuation); implementations that do not use
// continuation can ignore it.
type ModelCaller interface {
	Call(ctx context.Context, req model.ChatRequest, useContinuation bool) (*model.ChatResponse, error)
}

// ModelCallerFunc adapts a function to ModelCaller.
type ModelCallerFunc func(ctx context.Context, req model.ChatRequest, useContinuation bool) (*model.ChatResponse, error)

// Call implements ModelCaller.
func (f ModelCallerFunc) Call(ctx context.Context, req model.ChatRequest, useContinuation bool) (*model.ChatResponse, error) {
	return f(ctx, req, useContinuation)
}

// ToolOutcome is one dispatched tool call's result, already formatted as the
// content a tool-role model.Message carries back to the model. Approval,
// posture/permission gating (parked decisions pass straight through as a
// normal, non-error outcome), audit logging, and danger checks are the
// ToolDispatcher's responsibility; Controller treats every outcome as
// opaque content plus a success flag for the Governor.
type ToolOutcome struct {
	Content string
	Success bool
	// Error and Stderr are bounded diagnostic projections for failed-event
	// telemetry. Content remains the model-facing result and the evidence store
	// retains the complete body.
	Error  string
	Stderr string
	// EffectClass is an optional caller-provided effect classification such
	// as readonly, modifying, destructive, or control. It is recorded with
	// the durable step event so a future retry policy can be conservative.
	EffectClass string
	// YieldObserved reports whether the adapter can state an exact result
	// count. A true zero is a successful empty query, not an error.
	YieldObserved bool
	YieldCount    int
	YieldUnit     string
}

// ToolDispatcher executes one round's tool calls, in the same order the
// model requested them, and returns one ToolOutcome per call. IDs are
// already backfilled (see BackfillToolCallIDs) by the time Controller calls
// Dispatch. A non-nil error aborts the turn immediately (for example,
// context cancellation while a caller waits on an approval). When Dispatch
// returns a prefix of outcomes with that error, Controller durably records the
// prefix before failing only the unresolved suffix; a later retry can replay
// completed prefix calls instead of executing their effects again.
type ToolDispatcher interface {
	Dispatch(ctx context.Context, calls []model.ToolCall) ([]ToolOutcome, error)
}

// ToolDispatcherFunc adapts a function to ToolDispatcher.
type ToolDispatcherFunc func(ctx context.Context, calls []model.ToolCall) ([]ToolOutcome, error)

// Dispatch implements ToolDispatcher.
func (f ToolDispatcherFunc) Dispatch(ctx context.Context, calls []model.ToolCall) ([]ToolOutcome, error) {
	return f(ctx, calls)
}

// ToolOutcomeObserver observes a resolved tool outcome after it has either
// been dispatched or loaded from durable evidence. Observers must keep
// replay handling side-effect free; replayed is true when the dispatcher was
// intentionally skipped and the observer is only rehydrating local state.
type ToolOutcomeObserver func(ctx context.Context, call model.ToolCall, outcome ToolOutcome, replayed bool) error

// HistorySink observes every message Controller appends to the working
// transcript: the assistant turn (with or without tool calls) and each tool
// result. Controller does not retain conversation state itself; a nil
// HistorySink is valid when a caller only needs Result.Message from a
// single-shot call.
type HistorySink interface {
	Append(msg model.Message)
}

// HistorySinkFunc adapts a function to HistorySink.
type HistorySinkFunc func(msg model.Message)

// Append implements HistorySink.
func (f HistorySinkFunc) Append(msg model.Message) { f(msg) }

// Finish reasons Controller can report on Result when a turn ends without a
// plain-text completion. The empty string means the turn ended normally
// (the model returned content with no further tool calls).
const (
	// FinishReasonLoopGuard means the Governor stopped the turn (a round
	// or tool-call ceiling, an exact/outcome repeat, or a short cycle).
	// Result.GuardDecision carries the underlying Decision.
	FinishReasonLoopGuard = "loop_guard"
	// FinishReasonStepCap means ControllerConfig.StepCap (a persona-derived
	// iteration budget) stopped the turn before the Governor's own ceiling
	// would have.
	FinishReasonStepCap = "step_cap"
	// FinishReasonEmptyChoices means the model returned a response with no
	// choices.
	FinishReasonEmptyChoices = "empty_choices"
	// FinishReasonInvalidCompletion means the provider returned a no-tool
	// candidate that was empty, unreadable, or explicitly truncated.
	FinishReasonInvalidCompletion = "invalid_completion"
	// FinishReasonModelError means the provider failed after returning a
	// response fragment. The fragment is preserved as an incomplete result;
	// callers must not retry the logical step blindly.
	FinishReasonModelError = "model_error"

	partialDispatchJournalTimeout = 5 * time.Second
	// fallbackCostBoundedOutputTokens is used only when neither the caller nor
	// the model catalog supplies an output/context ceiling. Making the fallback
	// explicit prevents an unpriceable provider default from defeating a child
	// budget; 8K matches Buckley's existing review-agent completion allowance.
	fallbackCostBoundedOutputTokens = 8192
	// Provider framing is not present in ChatRequest's JSON. The byte-based
	// input bound below adds a fixed and per-item reserve for role separators,
	// tool wrappers, and provider control tokens. Catalog context can constrain
	// projection/output planning but never lowers this pricing bound.
	conservativeProviderFramingTokens        = 256
	conservativeProviderFramingTokensPerItem = 16
)

// CompletionStatus distinguishes a real terminal answer from a turn that
// stopped with useful evidence but could not synthesize it into an answer.
// Callers that launch child processes must never project incomplete as
// completed merely because the process itself exited cleanly.
type CompletionStatus string

const (
	CompletionConclusive CompletionStatus = "conclusive"
	CompletionIncomplete CompletionStatus = "incomplete"
)

// Termination describes the harness intervention behind an abnormal stop.
// It remains populated after successful finalization so observability can
// distinguish an ordinary model answer from an evidence-preserving recovery.
type Termination struct {
	Kind                  string
	Reason                string
	FinalizationAttempted bool
	FinalizationError     string
	ProviderError         string
}

// IncompleteTurnError reports that Buckley preserved the turn's evidence but
// could not produce a conclusive terminal answer from it.
type IncompleteTurnError struct {
	FinishReason      string
	Reason            string
	FinalizationError string
	ProviderError     string
}

func (e *IncompleteTurnError) Error() string {
	if e == nil {
		return "agentloop: turn is incomplete"
	}
	reason := strings.TrimSpace(e.Reason)
	if reason == "" {
		reason = "the turn stopped before a conclusive answer was produced"
	}
	message := "agentloop: incomplete turn: " + strings.TrimSuffix(reason, ".")
	if detail := strings.TrimSpace(e.FinalizationError); detail != "" {
		message += "; final synthesis failed: " + detail
	}
	if detail := strings.TrimSpace(e.ProviderError); detail != "" {
		message += "; provider error: " + detail
	}
	return message
}

// costCeilingError marks a request or response that cannot be admitted under
// an explicit all-in cost ceiling. It stays private: callers receive the
// stable IncompleteTurnError contract, while Controller uses this type to
// distinguish an intentional budget stop from provider and persistence errors.
type costCeilingError struct {
	reason string
}

func (e *costCeilingError) Error() string {
	if e == nil || strings.TrimSpace(e.reason) == "" {
		return "explicit child cost ceiling cannot admit another model request"
	}
	return e.reason
}

// Result is the outcome of one Controller.Run call.
type Result struct {
	// Message is the final assistant response when the turn ended
	// normally (FinishReason == ""). Its zero value otherwise.
	Message model.Message
	// Usage accumulates every round's reported token usage (model.AddUsage).
	Usage model.Usage
	// CostUSD accumulates the priced usage for every model request when the
	// caller supplies ControllerConfig.CostForUsage.
	CostUSD float64
	// Rounds is the number of model rounds executed (Governor.Rounds).
	Rounds int
	// ToolCalls is the number of tool calls dispatched across every round.
	ToolCalls int
	// ModelRequests counts every provider request attempted by the controller,
	// including a reserved final synthesis request.
	ModelRequests int
	// FinishReason is "" on a normal completion, or one of the
	// FinishReason* constants describing why the turn stopped early.
	FinishReason string
	// GuardDecision is populated when FinishReason == FinishReasonLoopGuard.
	GuardDecision Decision
	// Content is a caller-facing message describing an abnormal stop
	// (loop guard or step cap). Empty on a normal completion or an empty
	// model response, since callers already differ on how to surface those.
	Content string
	// Partial is true when the provider returned usable material but the
	// logical request failed before producing a conclusive turn. Message and
	// Content then contain the preserved fragment; it must not be treated as a
	// successful assistant answer or retried as a fresh provider call.
	Partial bool
	// Progress is the shared, provider-neutral operation projection for this
	// run. It remains available even when the turn stops before a final model
	// response so callers can report truthful partial work.
	Progress ProgressSnapshot
	// CompletionStatus is conclusive only when Message contains a terminal
	// assistant answer. A guard/cap stop is incomplete unless the shared
	// no-tools finalization pass succeeds.
	CompletionStatus CompletionStatus
	// Termination records why the harness stopped and whether its reserved
	// no-tools finalization request succeeded.
	Termination Termination
}

// RequireConclusive converts Result's explicit completion projection into an
// error suitable for process and run-lifecycle adapters.
func (r *Result) RequireConclusive() error {
	if r != nil && r.CompletionStatus == CompletionConclusive {
		return nil
	}
	if r == nil {
		return &IncompleteTurnError{Reason: "the controller returned no result"}
	}
	return &IncompleteTurnError{
		FinishReason:      r.FinishReason,
		Reason:            r.Termination.Reason,
		FinalizationError: r.Termination.FinalizationError,
		ProviderError:     r.Termination.ProviderError,
	}
}

// DurableStepJournal is the complete step-state port required by a durable
// Controller. Its static shape prevents a partially capable journal from
// reaching an external-effect boundary and failing only at runtime.
type DurableStepJournal interface {
	runledger.FencedStepJournal
}

// ControllerConfig wires one Controller instance.
type ControllerConfig struct {
	// Governor is consulted at the start of every round (BeginRound) and
	// after every dispatched tool call (Observe). Optional; when nil,
	// Controller uses New(DefaultConfig()).
	Governor *Governor

	// StepCap overrides the round ceiling when positive: a persona-derived
	// iteration budget (persona.Persona.StepCap) layered on top of the
	// Governor's own MaxRounds. Zero means no override.
	StepCap int

	// FinalizeOnStop reserves one final model request after a guard or step cap.
	// The request carries the completed tool transcript with tools disabled, so
	// evidence can be synthesized without permitting more actions. Surfaces
	// that map a turn to a process/run lifecycle should enable this.
	FinalizeOnStop bool

	// MaxCostUSD is an explicit all-in, client-side per-turn admission ceiling.
	// Zero leaves spend unbounded. CostForUsage is required when MaxCostUSD is
	// positive. Before a real provider dispatch, Controller prices a conservative
	// request-admission input bound plus its maximum output allowance and clamps
	// that allowance to the affordable remainder. This is not provider payment
	// authorization: upstream invoice and accounting details observed after
	// dispatch can differ. Completed durable steps are replayed without another
	// reservation.
	// A provider response whose reported usage still crosses the ceiling is
	// rejected before its content or tool calls can be accepted.
	MaxCostUSD   float64
	CostForUsage func(model.Usage) (float64, error)
	// NormalizeCostBoundedRequest lets the model adapter map Controller's
	// portable completion allowance onto the provider's actual wire field and
	// reject providers that cannot enforce it. Nil preserves the portable
	// ChatRequest field selected by applyRequestOutputAllowance; callers that
	// route to an adapter with different semantics should supply this hook.
	NormalizeCostBoundedRequest func(model.ChatRequest) (model.ChatRequest, error)
	// MaxModelRequests is an explicit all-in request ceiling. Unlike the
	// Governor's action-round fuse, it includes the reserved final synthesis.
	// Zero leaves request count unbounded.
	MaxModelRequests int

	// BuildRequest is required.
	BuildRequest RequestBuilder
	// CallModel is required.
	CallModel ModelCaller
	// DispatchTools is required whenever the model can return tool calls;
	// Controller errors lazily if a turn needs it and it is unset.
	DispatchTools ToolDispatcher
	// ObserveToolOutcome can rehydrate caller-owned in-memory state for a
	// replayed result without re-executing the tool. It is called for every
	// resolved outcome after durable evidence is available.
	ObserveToolOutcome ToolOutcomeObserver
	// History is optional.
	History HistorySink

	// ContextWindow resolves the model's context length for projection.
	// Optional; nil or a non-positive result falls back to the byte-budget
	// path in conversation.ProjectModelMessagesForRequestPinned.
	ContextWindow func(modelID string) int

	// Continuation, ContinuationEligible, and ProviderID together enable
	// continuation-aware projection (decision 0001): when set, Controller
	// restores the coordinator, pins the represented prefix during
	// projection, and applies the epoch rule (reset and recompile) if the
	// pinned projection still overflows its budget. All three are
	// optional; Continuation nil disables continuation entirely.
	Continuation         *model.ContinuationCoordinator
	ContinuationEligible func(modelID string) bool
	ProviderID           func(modelID string) string

	// Progress, when set, is consulted at the end of every tool round
	// (section 20, shadow-first rollout): the decision and its policy
	// trace are recorded to the RunLedger as a controller.decision event.
	// The engine enforces only stop_safety, and only in dynamic mode;
	// richer routing (verify, replan, park) is the goal loop's job, so a
	// shadow controller can never change engine behavior.
	Progress *ProgressController

	// RunLedger, when set, receives one event per model request
	// (started/completed/failed), one per tool-dispatch batch, and one per
	// controller stop decision. RunID and SessionID identify the events;
	// TaskID is optional. Recording is best-effort: a ledger write failure
	// never fails the turn.
	RunLedger runledger.Store
	// Evidence, when set, captures exact model requests/responses and tool
	// inputs/results. Evidence failures fail the turn because a replayable
	// step must not be acknowledged without its result.
	Evidence evidence.Store
	// StepJournal, when set, records stable logical steps and reuses completed
	// outputs instead of executing them again after a restart. Its composite
	// type statically requires guarded transitions, dispatch marking, and claim
	// fencing; nil explicitly selects non-durable controller execution.
	StepJournal DurableStepJournal
	// LifecycleObserver receives a redacted live projection of controller
	// transitions. It is observe-only and best-effort; exact request/response
	// bodies remain in Evidence and durable RunLedger records.
	LifecycleObserver LifecycleObserver
	// DeferLifecycleTurnEnd makes each Run emit attempt boundaries while the
	// caller owns the logical turn boundary. Such callers must invoke
	// CompleteLifecycleTurn exactly once after accepting or rejecting the
	// overall turn. ACP uses this for nudge/continuation Run calls.
	DeferLifecycleTurnEnd bool
	RunID                 string
	SessionID             string
	TaskID                string
	// TurnID is stable across retries of one goal-loop turn. It changes when
	// the goal loop advances to a new checkpoint generation.
	TurnID string
}

// NewController constructs a Controller. BuildRequest and CallModel are
// required.
func NewController(cfg ControllerConfig) (*Controller, error) {
	if cfg.BuildRequest == nil {
		return nil, fmt.Errorf("agentloop: BuildRequest is required")
	}
	if cfg.CallModel == nil {
		return nil, fmt.Errorf("agentloop: CallModel is required")
	}
	if cfg.MaxCostUSD < 0 || math.IsNaN(cfg.MaxCostUSD) || math.IsInf(cfg.MaxCostUSD, 0) {
		return nil, fmt.Errorf("agentloop: MaxCostUSD must be finite and non-negative")
	}
	if cfg.MaxModelRequests < 0 {
		return nil, fmt.Errorf("agentloop: MaxModelRequests must not be negative")
	}
	if cfg.MaxCostUSD > 0 && cfg.CostForUsage == nil {
		return nil, fmt.Errorf("agentloop: CostForUsage is required when MaxCostUSD is set")
	}
	if cfg.Governor == nil {
		cfg.Governor = New(DefaultConfig())
	}
	return &Controller{cfg: cfg}, nil
}

// Run executes the turn loop to completion: it builds and projects a
// request, calls the model, backfills tool-call IDs, dispatches any tool
// calls, appends history, accumulates usage, and consults the Governor,
// once per round, until the model returns a response with no tool calls, an
// empty response, a fatal error, or the Governor/step cap stops the loop.
func (c *Controller) Run(ctx context.Context) (result *Result, runErr error) {
	if c == nil {
		return nil, fmt.Errorf("agentloop: nil controller")
	}
	// Governor and the cumulative counters below form one turn state machine.
	// Serialize Run calls so a nudge cannot race another continuation and admit
	// both against the same unspent remainder.
	c.runMu.Lock()
	defer c.runMu.Unlock()
	if c.cfg.DeferLifecycleTurnEnd && c.lifecycleTurnCompleted {
		return nil, fmt.Errorf("agentloop: logical turn is already complete")
	}
	if c.totals.startedAt.IsZero() {
		c.totals.startedAt = time.Now()
	}
	runAttempt := 0
	continuation := false
	if c.cfg.DeferLifecycleTurnEnd {
		c.runAttempts++
		runAttempt = c.runAttempts
		continuation = runAttempt > 1
	}
	if !c.cfg.DeferLifecycleTurnEnd {
		c.emitLifecycle(LifecycleEvent{
			Type:      LifecycleTurnStart,
			Timestamp: time.Now().UTC(),
			Status:    string(CompletionIncomplete),
		})
	} else if !c.lifecycleTurnStarted {
		c.emitLifecycle(LifecycleEvent{
			Type:         LifecycleTurnStart,
			Timestamp:    time.Now().UTC(),
			Status:       string(CompletionIncomplete),
			RunAttempt:   runAttempt,
			Continuation: continuation,
		})
		c.lifecycleTurnStarted = true
	}
	if c.cfg.DeferLifecycleTurnEnd {
		c.emitLifecycle(LifecycleEvent{
			Type:         LifecycleAttemptStart,
			Timestamp:    time.Now().UTC(),
			Status:       string(CompletionIncomplete),
			RunAttempt:   runAttempt,
			Continuation: continuation,
		})
	}
	result = &Result{
		Usage:            cloneControllerUsage(c.totals.usage),
		CostUSD:          c.totals.costUSD,
		Rounds:           c.cfg.Governor.Rounds(),
		ToolCalls:        c.totals.toolCalls,
		ModelRequests:    c.totals.modelRequests,
		Progress:         c.totals.progress,
		CompletionStatus: CompletionIncomplete,
	}
	started := c.totals.startedAt
	progress := progressTracker{snapshot: c.totals.progress}
	defer func() {
		result.Progress = progress.Snapshot()
		c.totals.usage = cloneControllerUsage(result.Usage)
		c.totals.costUSD = result.CostUSD
		c.totals.modelRequests = result.ModelRequests
		c.totals.toolCalls = result.ToolCalls
		c.totals.progress = result.Progress
		end := LifecycleEvent{
			Type:         LifecycleTurnEnd,
			Timestamp:    time.Now().UTC(),
			Status:       string(result.CompletionStatus),
			FinishReason: result.FinishReason,
			StopReason:   result.Termination.Kind,
			Error:        lifecycleErrorProjection(result, runErr),
		}
		if c.cfg.DeferLifecycleTurnEnd {
			end.Type = LifecycleAttemptEnd
			end.RunAttempt = runAttempt
			end.Continuation = continuation
		}
		c.emitLifecycle(end)
	}()

	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}

		govDecision := c.cfg.Governor.BeginRound()
		result.Rounds = c.cfg.Governor.Rounds()
		if c.cfg.StepCap > 0 && result.Rounds > c.cfg.StepCap {
			result.FinishReason = FinishReasonStepCap
			result.Content = fmt.Sprintf("Buckley stopped after the %d-step persona limit.", c.cfg.StepCap)
			result.Termination = Termination{Kind: "step_cap", Reason: result.Content}
			c.recordDecision(ctx, "step_cap", result.Content)
			return c.finalizeStoppedTurn(ctx, result, result.Rounds)
		}
		if govDecision.Stop {
			result.FinishReason = FinishReasonLoopGuard
			result.GuardDecision = govDecision
			result.Content = GuardStopMessage(govDecision.Reason)
			result.Termination = Termination{Kind: govDecision.Kind, Reason: govDecision.Reason}
			c.recordDecision(ctx, govDecision.Kind, govDecision.Reason)
			return c.finalizeStoppedTurn(ctx, result, result.Rounds)
		}
		if c.cfg.FinalizeOnStop && c.cfg.MaxModelRequests > 0 && c.cfg.Governor.ToolCalls() > 0 && result.ModelRequests+1 >= c.cfg.MaxModelRequests {
			reason := fmt.Sprintf("one of the %d explicit model requests remains and is reserved for final synthesis", c.cfg.MaxModelRequests)
			result.FinishReason = FinishReasonLoopGuard
			result.GuardDecision = Decision{Stop: true, Kind: "model_request_limit", Reason: reason}
			result.Termination = Termination{Kind: "model_request_limit", Reason: reason}
			c.recordDecision(ctx, "model_request_limit", reason)
			return c.finalizeStoppedTurn(ctx, result, result.Rounds)
		}
		if c.cfg.MaxModelRequests > 0 && result.ModelRequests >= c.cfg.MaxModelRequests {
			reason := fmt.Sprintf("model requests reached the explicit %d-request child limit", c.cfg.MaxModelRequests)
			result.FinishReason = FinishReasonLoopGuard
			result.GuardDecision = Decision{Stop: true, Kind: "model_request_limit", Reason: reason}
			result.Termination = Termination{Kind: "model_request_limit", Reason: reason}
			c.recordDecision(ctx, "model_request_limit", reason)
			return c.finalizeStoppedTurn(ctx, result, result.Rounds)
		}

		roundResult, contextWindow, roundErr := c.executeModelRound(ctx, result, result.Rounds)
		var accountingErr error
		if roundResult != nil && roundResult.response != nil {
			accountingErr = c.addUsageCost(result, roundResult.response.Usage, roundResult.chargedCostUSD, roundResult.costRecorded, roundResult.pricingError)
		}
		providerPartial := roundResult != nil && roundResult.partial
		providerError := ""
		if providerPartial {
			providerError = strings.TrimSpace(roundResult.providerError)
		}
		if accountingErr != nil {
			var ceilingErr *costCeilingError
			if errors.As(accountingErr, &ceilingErr) {
				stopped, stopErr := c.stopForCostLimit(ctx, result, ceilingErr, result.Rounds)
				if providerPartial {
					stopped.Termination.ProviderError = providerError
					if stopped.CompletionStatus != CompletionConclusive {
						c.capturePartialResponse(stopped, roundResult.response)
						stopErr = stopped.RequireConclusive()
					}
				}
				if roundErr != nil && stopErr != nil {
					return stopped, errors.Join(stopErr, roundErr, accountingErr)
				}
				return stopped, stopErr
			}
			result.CompletionStatus = CompletionIncomplete
			result.Termination = Termination{Kind: "cost_accounting", Reason: accountingErr.Error(), ProviderError: providerError}
			if providerPartial {
				c.capturePartialResponse(result, roundResult.response)
				return result, errors.Join(result.RequireConclusive(), roundErr, accountingErr)
			}
			return result, errors.Join(roundErr, accountingErr)
		}
		if providerPartial {
			c.capturePartialResponse(result, roundResult.response)
			result.CompletionStatus = CompletionIncomplete
			reason := "the provider failed after returning a partial response"
			result.FinishReason = FinishReasonModelError
			result.Termination = Termination{Kind: FinishReasonModelError, Reason: reason, ProviderError: providerError}
			c.recordDecision(ctx, FinishReasonModelError, reason)
			return result, errors.Join(result.RequireConclusive(), roundErr)
		}
		if roundErr != nil {
			var ceilingErr *costCeilingError
			if errors.As(roundErr, &ceilingErr) {
				return c.stopForCostLimit(ctx, result, ceilingErr, result.Rounds)
			}
			return result, roundErr
		}
		var resp *model.ChatResponse
		if roundResult != nil {
			resp = roundResult.response
		}
		if resp == nil || len(resp.Choices) == 0 {
			result.FinishReason = FinishReasonEmptyChoices
			result.Termination = Termination{Kind: FinishReasonEmptyChoices, Reason: "the model returned no response choices"}
			if c.cfg.Governor.ToolCalls() > 0 {
				return c.finalizeStoppedTurn(ctx, result, result.Rounds)
			}
			return result, nil
		}

		choice := resp.Choices[0]
		if isTruncatedFinishReason(choice.FinishReason) {
			result.FinishReason = FinishReasonInvalidCompletion
			result.Termination = Termination{Kind: FinishReasonInvalidCompletion, Reason: "model response was truncated at its output limit"}
			if c.cfg.Governor.ToolCalls() > 0 {
				return c.finalizeStoppedTurn(ctx, result, result.Rounds)
			}
			return result, nil
		}

		msg := choice.Message
		if len(msg.ToolCalls) == 0 {
			text, candidateErr := validateTerminalCandidate(choice)
			if candidateErr != nil {
				result.FinishReason = FinishReasonInvalidCompletion
				result.Termination = Termination{Kind: FinishReasonInvalidCompletion, Reason: candidateErr.Error()}
				if c.cfg.Governor.ToolCalls() > 0 {
					return c.finalizeStoppedTurn(ctx, result, result.Rounds)
				}
				return result, nil
			}
			result.Message = msg
			result.Content = text
			result.CompletionStatus = CompletionConclusive
			if c.cfg.History != nil {
				c.cfg.History.Append(msg)
			}
			return result, nil
		}

		msg.ToolCalls = BackfillToolCallIDs(msg.ToolCalls)
		if remaining := c.cfg.Governor.RemainingToolCalls(); len(msg.ToolCalls) > remaining {
			// Preserve a protocol-valid transcript by advertising only calls that
			// can actually run. The complete provider response remains in durable
			// evidence, while omitted calls never reach the dispatcher.
			msg.ToolCalls = msg.ToolCalls[:remaining]
		}
		if len(msg.ToolCalls) == 0 {
			reason := "tool loop has no remaining calls in its harness allowance"
			result.FinishReason = FinishReasonLoopGuard
			result.GuardDecision = Decision{Stop: true, Kind: "tool_call_limit", Reason: reason}
			result.Content = GuardStopMessage(reason)
			result.Termination = Termination{Kind: "tool_call_limit", Reason: reason}
			c.recordDecision(ctx, "tool_call_limit", reason)
			return c.finalizeStoppedTurn(ctx, result, result.Rounds)
		}
		if c.cfg.History != nil {
			c.cfg.History.Append(msg)
		}
		toolRound, err := c.prepareToolRound(ctx, msg.ToolCalls, result.Rounds)
		if err != nil {
			return result, err
		}
		if err := c.dispatchToolRound(ctx, toolRound, result.Rounds); err != nil {
			return result, err
		}
		result.ToolCalls += len(toolRound.calls)

		stopDecision, err := c.observeToolRound(ctx, toolRound, &progress)
		if err != nil {
			return result, err
		}
		result.Progress = progress.Snapshot()
		if stopDecision.Stop {
			result.FinishReason = FinishReasonLoopGuard
			result.GuardDecision = stopDecision
			result.Content = GuardStopMessage(stopDecision.Reason)
			result.Termination = Termination{Kind: stopDecision.Kind, Reason: stopDecision.Reason}
			c.recordDecision(ctx, stopDecision.Kind, stopDecision.Reason)
			return c.finalizeStoppedTurn(ctx, result, result.Rounds)
		}

		if stop := c.consultProgress(ctx, result, resp.Usage, contextWindow, started); stop {
			result.Termination = Termination{Kind: result.GuardDecision.Kind, Reason: result.GuardDecision.Reason}
			return c.finalizeStoppedTurn(ctx, result, result.Rounds)
		}
		if c.cfg.MaxCostUSD > 0 && result.CostUSD >= c.cfg.MaxCostUSD {
			reason := fmt.Sprintf("model spend reached the explicit $%.4f child limit", c.cfg.MaxCostUSD)
			result.FinishReason = FinishReasonLoopGuard
			result.GuardDecision = Decision{Stop: true, Kind: "cost_limit", Reason: reason}
			result.Content = GuardStopMessage(reason)
			result.Termination = Termination{Kind: "cost_limit", Reason: reason}
			c.recordDecision(ctx, "cost_limit", reason)
			return c.finalizeStoppedTurn(ctx, result, result.Rounds)
		}
	}
}

// CompleteLifecycleTurn closes a caller-managed logical turn. It is
// idempotent and emits no duplicate turn/end event. Raw causes remain in
// turnErr for errors.Is/errors.As; the live event receives only a bounded,
// redacted projection.
func (c *Controller) CompleteLifecycleTurn(result *Result, turnErr error) {
	if c == nil || !c.cfg.DeferLifecycleTurnEnd {
		return
	}
	c.runMu.Lock()
	defer c.runMu.Unlock()
	if c.lifecycleTurnCompleted {
		return
	}
	if !c.lifecycleTurnStarted {
		c.emitLifecycle(LifecycleEvent{
			Type:       LifecycleTurnStart,
			Timestamp:  time.Now().UTC(),
			Status:     string(CompletionIncomplete),
			RunAttempt: c.runAttempts,
		})
		c.lifecycleTurnStarted = true
	}
	status := CompletionIncomplete
	finishReason := ""
	stopReason := ""
	if result != nil {
		if turnErr == nil {
			status = result.CompletionStatus
		}
		finishReason = result.FinishReason
		stopReason = result.Termination.Kind
	}
	c.emitLifecycle(LifecycleEvent{
		Type:         LifecycleTurnEnd,
		Timestamp:    time.Now().UTC(),
		Status:       string(status),
		FinishReason: finishReason,
		StopReason:   stopReason,
		Error:        lifecycleErrorProjection(result, turnErr),
		RunAttempt:   c.runAttempts,
		Continuation: c.runAttempts > 1,
	})
	c.lifecycleTurnCompleted = true
}

func lifecycleErrorProjection(result *Result, runErr error) string {
	if result != nil {
		if projection := modelstep.NormalizeErrorText(result.Termination.FinalizationError); projection != "" {
			return projection
		}
	}
	return modelstep.NormalizeError(runErr)
}

func cloneControllerUsage(usage model.Usage) model.Usage {
	if usage.PromptTokensDetails != nil {
		details := *usage.PromptTokensDetails
		usage.PromptTokensDetails = &details
	}
	if usage.CompletionTokenDetails != nil {
		details := *usage.CompletionTokenDetails
		usage.CompletionTokenDetails = &details
	}
	return usage
}

// executeModelRound owns the durable model-step lifecycle. Keeping it outside
// Run makes the turn loop read as the state machine it is, while preserving
// exactly the same request evidence, replay, event, and failure semantics for
// every surface that shares Controller.
func (c *Controller) executeModelRound(ctx context.Context, result *Result, round int) (*modelRoundResult, int, error) {
	req, contextWindow, useContinuation, err := c.projectedModelRequest(ctx, round)
	if err != nil {
		return nil, contextWindow, err
	}
	roundResult, err := c.executePreparedModelRound(ctx, result, req, round, "model", useContinuation)
	return roundResult, contextWindow, err
}

// finalizeStoppedTurn reserves one tools-disabled model request after the
// harness has stopped actions. The completed transcript remains the source of
// truth; finalization can summarize it but can neither dispatch nor invent a
// new tool result.
func (c *Controller) finalizeStoppedTurn(ctx context.Context, result *Result, round int) (*Result, error) {
	if c == nil || result == nil || !c.cfg.FinalizeOnStop {
		return result, nil
	}
	result.Termination.FinalizationAttempted = true
	c.recordDecision(ctx, "finalization_started", result.Termination.Reason)
	if c.cfg.MaxModelRequests > 0 && result.ModelRequests >= c.cfg.MaxModelRequests {
		return c.failFinalization(ctx, result, fmt.Errorf("explicit %d-request child limit left no request for final synthesis", c.cfg.MaxModelRequests))
	}
	if c.cfg.MaxCostUSD > 0 && result.CostUSD >= c.cfg.MaxCostUSD {
		return c.failFinalization(ctx, result, fmt.Errorf("explicit $%.4f child limit left no spend allowance for final synthesis", c.cfg.MaxCostUSD))
	}

	req, err := c.buildFinalizationRequest(ctx, round, result.Termination.Reason)
	if err != nil {
		return c.failFinalization(ctx, result, err)
	}
	roundResult, roundErr := c.executePreparedModelRound(ctx, result, req, round, "finalize", false)
	var response *model.ChatResponse
	if roundResult != nil {
		response = roundResult.response
	}
	var accountingErr error
	if roundResult != nil && response != nil {
		accountingErr = c.addUsageCost(result, response.Usage, roundResult.chargedCostUSD, roundResult.costRecorded, roundResult.pricingError)
	}
	providerPartial := roundResult != nil && roundResult.partial
	providerError := ""
	if providerPartial {
		c.capturePartialResponse(result, response)
		result.CompletionStatus = CompletionIncomplete
		providerError = strings.TrimSpace(roundResult.providerError)
		result.Termination.ProviderError = providerError
	}
	if accountingErr != nil {
		failure := errors.Join(roundErr, accountingErr)
		failed, incompleteErr := c.failFinalization(ctx, result, failure)
		if roundErr != nil {
			return failed, errors.Join(incompleteErr, failure)
		}
		return failed, incompleteErr
	}
	if providerPartial {
		if roundErr == nil {
			if providerError != "" {
				roundErr = errors.New(providerError)
			} else {
				roundErr = fmt.Errorf("the provider failed after returning a partial response")
			}
		}
		failed, incompleteErr := c.failFinalization(ctx, result, roundErr)
		return failed, errors.Join(incompleteErr, roundErr)
	}
	if roundErr != nil {
		failed, incompleteErr := c.failFinalization(ctx, result, roundErr)
		return failed, errors.Join(incompleteErr, roundErr)
	}
	if response == nil {
		return c.failFinalization(ctx, result, fmt.Errorf("model returned no response choices"))
	}

	if len(response.Choices) == 0 {
		return c.failFinalization(ctx, result, fmt.Errorf("model returned no response choices"))
	}
	choice := response.Choices[0]
	message := choice.Message
	if len(message.ToolCalls) > 0 {
		return c.failFinalization(ctx, result, fmt.Errorf("model requested %d tool call(s) while tools were disabled", len(message.ToolCalls)))
	}
	text, err := validateTerminalCandidate(choice)
	if err != nil {
		return c.failFinalization(ctx, result, fmt.Errorf("invalid final synthesis: %w", err))
	}

	if message.Role == "" {
		message.Role = "assistant"
	}
	result.Message = message
	result.Content = text
	result.CompletionStatus = CompletionConclusive
	if c.cfg.History != nil {
		c.cfg.History.Append(message)
	}
	c.recordDecision(ctx, "finalization_completed", result.Termination.Reason)
	return result, nil
}

func validateTerminalCandidate(choice model.Choice) (string, error) {
	if isTruncatedFinishReason(choice.FinishReason) {
		return "", fmt.Errorf("model response was truncated at its output limit")
	}
	text, err := model.ExtractTextContent(choice.Message.Content)
	if err != nil {
		return "", fmt.Errorf("extract model response: %w", err)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("model returned a final response without text")
	}
	return text, nil
}

// capturePartialResponse keeps the last provider material visible even when
// the request cannot complete. It deliberately does not append history or
// dispatch tool calls: a response that arrived with a transport/provider
// error is evidence, not an accepted turn. A usage-only response still gets
// an honest receipt so a billable request can never disappear as a bare error.
func (c *Controller) capturePartialResponse(result *Result, response *model.ChatResponse) {
	if result == nil {
		return
	}
	result.Partial = true
	if response == nil {
		return
	}
	if len(response.Choices) > 0 {
		result.Message = response.Choices[0].Message
		if text, err := model.ExtractTextContent(response.Choices[0].Message.Content); err == nil && strings.TrimSpace(text) != "" {
			result.Content = strings.TrimSpace(text)
			return
		}
		if len(response.Choices[0].Message.ToolCalls) > 0 {
			result.Content = fmt.Sprintf("Partial model response preserved with %d unexecuted tool call(s); the provider request failed before Buckley could safely continue.", len(response.Choices[0].Message.ToolCalls))
			return
		}
	}
	result.Content = fmt.Sprintf("The provider reported billable usage (%d token(s)) but returned no usable answer; Buckley preserved the raw response evidence.", response.Usage.TotalTokens)
}

func isTruncatedFinishReason(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "length", "max_tokens", "max_output_tokens":
		return true
	default:
		return false
	}
}

func (c *Controller) addUsageCost(result *Result, usage model.Usage, chargedCostUSD float64, costRecorded bool, pricingError string) error {
	if result == nil {
		return nil
	}
	result.Usage = model.AddUsage(result.Usage, usage)
	if c == nil || c.cfg.CostForUsage == nil {
		return nil
	}
	if pricingError = strings.TrimSpace(pricingError); pricingError != "" {
		if c.cfg.MaxCostUSD > 0 {
			return &costCeilingError{reason: "original model response could not be priced safely and was rejected: " + pricingError}
		}
		return fmt.Errorf("agentloop: persisted model pricing failure: %s", pricingError)
	}
	if !costRecorded {
		if c.cfg.MaxCostUSD > 0 {
			return &costCeilingError{reason: "durable model response has no original charged cost; refusing to reprice it under an active child ceiling"}
		}
		var err error
		chargedCostUSD, err = c.priceUsage(usage)
		if err != nil {
			return err
		}
	}
	if chargedCostUSD < 0 || math.IsNaN(chargedCostUSD) || math.IsInf(chargedCostUSD, 0) {
		return fmt.Errorf("agentloop: recorded model usage cost must be finite and non-negative")
	}
	result.CostUSD += chargedCostUSD
	if c.cfg.MaxCostUSD > 0 && result.CostUSD > c.cfg.MaxCostUSD {
		return &costCeilingError{reason: fmt.Sprintf(
			"model response reported $%.6f total spend, exceeding the explicit $%.6f child ceiling; its content and tool calls were rejected",
			result.CostUSD,
			c.cfg.MaxCostUSD,
		)}
	}
	return nil
}

func (c *Controller) priceUsage(usage model.Usage) (float64, error) {
	if c == nil || c.cfg.CostForUsage == nil {
		return 0, nil
	}
	// Some provider adapters leave TotalTokens unset. Supplying the derived
	// total makes CostForUsage implementations based on either split or total
	// token accounting agree, without rewriting the provider's reported usage.
	if usage.TotalTokens <= 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	cost, err := c.cfg.CostForUsage(usage)
	if err != nil {
		return 0, fmt.Errorf("agentloop: price model usage: %w", err)
	}
	if cost < 0 || math.IsNaN(cost) || math.IsInf(cost, 0) {
		return 0, fmt.Errorf("agentloop: priced model usage must be finite and non-negative")
	}
	return cost, nil
}

type modelCostReservation struct {
	inputTokens  int
	outputTokens int
	envelopeUSD  float64
	remainingUSD float64
}

type modelRoundResult struct {
	response       *model.ChatResponse
	chargedCostUSD float64
	costRecorded   bool
	pricingError   string
	partial        bool
	providerError  string
}

const (
	modelResponseEvidenceVersion = modelstep.ResponseVersion
	modelResponseCostMetadataKey = "charged_cost_usd"
	blockedModelStepErrorPrefix  = modelstep.BlockedMarkerPrefix
	blockedModelStepErrorRunes   = modelstep.MaxPersistedErrorRunes
)

// modelResponseEvidenceEnvelope keeps the provider response and the original
// usage-based charge calculated for that logical call in one content-addressed body.
// Cost therefore participates in evidence identity instead of being lost when
// the evidence store deduplicates equal response JSON with different metadata.
type modelResponseEvidenceEnvelope = modelstep.ResponseEnvelope

// blockedModelStepRecord is stored in ExecutionStep.Error only when a provider
// returned a response and an error but Buckley could not finish journaling that
// response. The marker makes the already billable logical call fail closed and
// carries enough context to replay its response evidence when that evidence was
// stored before the journal failed. Older records may use StepFailed; the
// terminal fallback uses StepBlocked when writing that failure marker fails.
type blockedModelStepRecord = modelstep.BlockedMarker

// reserveModelRequest converts a provider-defined output default into an
// explicit, priceable allowance and ensures the conservative request-admission
// envelope fits the turn's unspent ceiling. It is deliberately not presented
// as an exact provider invoice. Calls are sequential, so this
// reservation needs no shared mutable balance: observed response spend is
// charged before any subsequent request can reach this boundary.
func (c *Controller) reserveModelRequest(req model.ChatRequest, spentUSD float64) (model.ChatRequest, modelCostReservation, error) {
	if c == nil || c.cfg.MaxCostUSD <= 0 {
		return req, modelCostReservation{}, nil
	}
	if requestContainsUnpricedImageContent(req) {
		return req, modelCostReservation{}, &costCeilingError{reason: "cannot safely admit multimodal image input under an explicit child cost ceiling because no authoritative image-token estimator is available"}
	}
	remainingUSD := c.cfg.MaxCostUSD - spentUSD
	if remainingUSD <= 0 {
		return req, modelCostReservation{}, &costCeilingError{reason: fmt.Sprintf(
			"explicit $%.6f child ceiling has no spend remaining before model dispatch",
			c.cfg.MaxCostUSD,
		)}
	}

	// First normalize an explicit provisional allowance. Provider routing and
	// deterministic wire decorations can change the model, context lookup, and
	// serialized input size; all three must be reflected in admission pricing.
	requestedOutput := explicitRequestOutputAllowance(req)
	usedFallback := requestedOutput <= 0
	if usedFallback {
		requestedOutput = fallbackCostBoundedOutputTokens
	}
	requested, err := c.priceCostBoundedCandidate(req, requestedOutput)
	if err != nil {
		return req, modelCostReservation{}, err
	}
	if usedFallback {
		contextAllowance := defaultCostBoundedOutputAllowance(requested.inputTokens, requested.contextWindow)
		if contextAllowance != requestedOutput {
			requestedOutput = contextAllowance
			requested, err = c.priceCostBoundedCandidate(req, requestedOutput)
			if err != nil {
				return req, modelCostReservation{}, err
			}
		}
	}
	requestedOutput = requested.outputTokens
	if requested.envelopeUSD <= remainingUSD {
		return requested.request, modelCostReservation{
			inputTokens:  requested.inputTokens,
			outputTokens: requested.outputTokens,
			envelopeUSD:  requested.envelopeUSD,
			remainingUSD: remainingUSD,
		}, nil
	}

	// Model pricing is monotone in completion tokens for the Manager-backed
	// pricers used by production. Binary search finds the largest whole-token
	// allowance whose independently normalized and priced envelope remains
	// within the declared remainder. Normalizing every candidate is important:
	// even the decimal width of a wire cap, or provider-added fields derived from
	// it, participates in the conservative input bound.
	low, high := 1, requestedOutput
	var affordable *pricedModelRequest
	for low <= high {
		mid := low + (high-low)/2
		candidate, candidateErr := c.priceCostBoundedCandidate(req, mid)
		if candidateErr != nil {
			return req, modelCostReservation{}, candidateErr
		}
		if candidate.envelopeUSD <= remainingUSD {
			copy := candidate
			affordable = &copy
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	if affordable == nil {
		return req, modelCostReservation{}, &costCeilingError{reason: fmt.Sprintf(
			"explicit $%.6f child ceiling cannot fund the normalized conservative input bound plus even one completion token",
			c.cfg.MaxCostUSD,
		)}
	}
	if affordable.envelopeUSD > remainingUSD {
		return req, modelCostReservation{}, &costCeilingError{reason: "model output allowance could not be clamped safely within the remaining child cost ceiling"}
	}
	return affordable.request, modelCostReservation{
		inputTokens:  affordable.inputTokens,
		outputTokens: affordable.outputTokens,
		envelopeUSD:  affordable.envelopeUSD,
		remainingUSD: remainingUSD,
	}, nil
}

type pricedModelRequest struct {
	request       model.ChatRequest
	inputTokens   int
	outputTokens  int
	contextWindow int
	inputUSD      float64
	envelopeUSD   float64
}

// priceCostBoundedCandidate performs the exact preparation sequence that an
// admitted request relies on: cap, provider normalization, normalized-model
// context lookup, conservative serialized-input bound, and pricing. Callers
// must use the returned request rather than reapplying only the portable cap.
func (c *Controller) priceCostBoundedCandidate(base model.ChatRequest, allowance int) (pricedModelRequest, error) {
	if allowance <= 0 {
		return pricedModelRequest{}, &costCeilingError{reason: "model request has no positive bounded output allowance"}
	}
	normalized, err := c.normalizeCostBoundedOutput(base, allowance)
	if err != nil {
		return pricedModelRequest{}, err
	}
	if requestContainsUnpricedImageContent(normalized) {
		return pricedModelRequest{}, &costCeilingError{reason: "provider normalization introduced multimodal image input that cannot be priced without an authoritative image-token estimator"}
	}
	outputTokens := explicitRequestOutputAllowance(normalized)
	if outputTokens <= 0 {
		return pricedModelRequest{}, &costCeilingError{reason: "provider normalization removed the enforceable output allowance"}
	}
	if outputTokens > allowance {
		return pricedModelRequest{}, &costCeilingError{reason: fmt.Sprintf(
			"provider normalization broadened the output allowance from %d to %d tokens",
			allowance,
			outputTokens,
		)}
	}
	contextWindow := 0
	if c.cfg.ContextWindow != nil {
		contextWindow = c.cfg.ContextWindow(normalized.Model)
	}
	inputTokens, err := conservativeRequestInputTokenBound(normalized)
	if err != nil {
		return pricedModelRequest{}, &costCeilingError{reason: fmt.Sprintf("cannot bound normalized model request input before dispatch: %v", err)}
	}
	price := func(completionTokens int) (float64, error) {
		usage := model.Usage{
			PromptTokens:     inputTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      inputTokens + completionTokens,
		}
		cost, priceErr := c.priceUsage(usage)
		if priceErr != nil {
			return 0, &costCeilingError{reason: fmt.Sprintf("cannot safely price normalized model request before dispatch: %v", priceErr)}
		}
		return cost, nil
	}
	inputUSD, err := price(0)
	if err != nil {
		return pricedModelRequest{}, err
	}
	envelopeUSD, err := price(outputTokens)
	if err != nil {
		return pricedModelRequest{}, err
	}
	if envelopeUSD < inputUSD {
		return pricedModelRequest{}, &costCeilingError{reason: "cannot safely clamp model output because priced cost decreases as completion tokens increase"}
	}
	return pricedModelRequest{
		request:       normalized,
		inputTokens:   inputTokens,
		outputTokens:  outputTokens,
		contextWindow: contextWindow,
		inputUSD:      inputUSD,
		envelopeUSD:   envelopeUSD,
	}, nil
}

func (c *Controller) normalizeCostBoundedOutput(req model.ChatRequest, allowance int) (model.ChatRequest, error) {
	req = applyRequestOutputAllowance(req, allowance)
	if c == nil || c.cfg.NormalizeCostBoundedRequest == nil {
		return req, nil
	}
	normalized, err := c.cfg.NormalizeCostBoundedRequest(req)
	if err != nil {
		return req, &costCeilingError{reason: fmt.Sprintf("provider cannot enforce the cost-bounded output allowance: %v", err)}
	}
	// Manager reapplies the same normalization immediately before provider
	// dispatch. Require idempotence here so the request whose bytes were priced
	// is equivalent to that eventual wire preparation.
	rerenormalized, err := c.cfg.NormalizeCostBoundedRequest(normalized)
	if err != nil {
		return req, &costCeilingError{reason: fmt.Sprintf("provider cost-bound normalization is not replay-safe: %v", err)}
	}
	if !reflect.DeepEqual(normalized, rerenormalized) {
		return req, &costCeilingError{reason: "provider cost-bound normalization is not idempotent; refusing to price a request that can change before dispatch"}
	}
	return normalized, nil
}

// conservativeRequestInputTokenBound treats every serialized UTF-8 byte as
// one input token, then reserves provider framing tokens. This is deliberately
// more conservative than EstimateRequestTokens' byte/4 planning estimate.
// Catalog context is deliberately not a ceiling on this pricing bound: a stale
// or understated catalog value must not make an accepted serialized request
// look cheaper than its own bytes.
func conservativeRequestInputTokenBound(req model.ChatRequest) (int, error) {
	encoded, err := json.Marshal(req)
	if err != nil {
		return 0, err
	}
	items := len(req.Messages) + len(req.Tools) + 1
	bound := len(encoded) + conservativeProviderFramingTokens + items*conservativeProviderFramingTokensPerItem
	bound = max(bound, model.EstimateRequestTokens(req).Total)
	return bound, nil
}

func requestContainsUnpricedImageContent(req model.ChatRequest) bool {
	for _, message := range req.Messages {
		if contentContainsUnpricedImage(message.Content) {
			return true
		}
	}
	return false
}

func contentContainsUnpricedImage(content any) bool {
	return reflectedContentContainsUnpricedImage(reflect.ValueOf(content), make(map[contentVisit]struct{}), 0)
}

type contentVisit struct {
	typ reflect.Type
	ptr uintptr
}

const maxContentInspectionDepth = 128

// reflectedContentContainsUnpricedImage handles the concrete content shapes
// adapters and JSON decoders commonly produce without relying on one exact map
// or slice type. A visited set plus a depth fuse makes caller-owned cyclic
// pointer/map/slice graphs safe to inspect.
func reflectedContentContainsUnpricedImage(value reflect.Value, visited map[contentVisit]struct{}, depth int) bool {
	if !value.IsValid() || depth > maxContentInspectionDepth {
		return false
	}
	if value.Kind() == reflect.Interface {
		if value.IsNil() {
			return false
		}
		return reflectedContentContainsUnpricedImage(value.Elem(), visited, depth+1)
	}
	if value.CanInterface() {
		switch typed := value.Interface().(type) {
		case model.ContentPart:
			return typed.ImageURL != nil || isImageContentType(typed.Type)
		case json.RawMessage:
			return rawJSONContainsUnpricedImage(typed, visited, depth+1)
		case *json.RawMessage:
			if typed == nil {
				return false
			}
			return rawJSONContainsUnpricedImage(*typed, visited, depth+1)
		}
	}

	switch value.Kind() {
	case reflect.Pointer:
		if value.IsNil() || alreadyVisitedContent(value, visited) {
			return false
		}
		return reflectedContentContainsUnpricedImage(value.Elem(), visited, depth+1)
	case reflect.Map:
		if value.IsNil() || alreadyVisitedContent(value, visited) {
			return false
		}
		iterator := value.MapRange()
		for iterator.Next() {
			key := strings.ToLower(strings.TrimSpace(fmt.Sprint(iterator.Key().Interface())))
			nested := iterator.Value()
			if isImageContentKey(key) && reflectedValuePresent(nested) {
				return true
			}
			if key == "type" && reflectedValueIsImageContentType(nested) {
				return true
			}
			if reflectedContentContainsUnpricedImage(nested, visited, depth+1) {
				return true
			}
		}
	case reflect.Slice:
		if value.IsNil() || alreadyVisitedContent(value, visited) {
			return false
		}
		for i := 0; i < value.Len(); i++ {
			if reflectedContentContainsUnpricedImage(value.Index(i), visited, depth+1) {
				return true
			}
		}
	case reflect.Array:
		for i := 0; i < value.Len(); i++ {
			if reflectedContentContainsUnpricedImage(value.Index(i), visited, depth+1) {
				return true
			}
		}
	case reflect.Struct:
		typ := value.Type()
		for i := 0; i < value.NumField(); i++ {
			fieldType := typ.Field(i)
			field := value.Field(i)
			if !field.CanInterface() {
				continue
			}
			name := strings.ToLower(fieldType.Name)
			jsonName := strings.Split(fieldType.Tag.Get("json"), ",")[0]
			if jsonName != "" && jsonName != "-" {
				name = strings.ToLower(jsonName)
			}
			if isImageContentKey(name) && reflectedValuePresent(field) {
				return true
			}
			if name == "type" && reflectedValueIsImageContentType(field) {
				return true
			}
			if reflectedContentContainsUnpricedImage(field, visited, depth+1) {
				return true
			}
		}
	}
	return false
}

func alreadyVisitedContent(value reflect.Value, visited map[contentVisit]struct{}) bool {
	key := contentVisit{typ: value.Type(), ptr: value.Pointer()}
	if key.ptr == 0 {
		return false
	}
	if _, exists := visited[key]; exists {
		return true
	}
	visited[key] = struct{}{}
	return false
}

func rawJSONContainsUnpricedImage(raw json.RawMessage, visited map[contentVisit]struct{}, depth int) bool {
	if len(raw) == 0 {
		return false
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return false
	}
	return reflectedContentContainsUnpricedImage(reflect.ValueOf(decoded), visited, depth+1)
}

func reflectedValuePresent(value reflect.Value) bool {
	for value.IsValid() && value.Kind() == reflect.Interface {
		if value.IsNil() {
			return false
		}
		value = value.Elem()
	}
	if !value.IsValid() {
		return false
	}
	switch value.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice:
		return !value.IsNil()
	case reflect.String:
		return strings.TrimSpace(value.String()) != ""
	default:
		return !value.IsZero()
	}
}

func reflectedValueIsImageContentType(value reflect.Value) bool {
	for value.IsValid() && value.Kind() == reflect.Interface {
		if value.IsNil() {
			return false
		}
		value = value.Elem()
	}
	return value.IsValid() && value.Kind() == reflect.String && isImageContentType(value.String())
}

func isImageContentKey(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "image", "image_url", "input_image":
		return true
	default:
		return false
	}
}

func isImageContentType(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "image", "image_url", "input_image":
		return true
	default:
		return false
	}
}

func requestOutputAllowance(req model.ChatRequest, inputTokens, contextWindow int) int {
	if allowance := explicitRequestOutputAllowance(req); allowance > 0 {
		return allowance
	}
	return defaultCostBoundedOutputAllowance(inputTokens, contextWindow)
}

func explicitRequestOutputAllowance(req model.ChatRequest) int {
	allowance := max(req.MaxTokens, req.MaxCompletionTokens)
	if req.Reasoning != nil {
		allowance = max(allowance, req.Reasoning.MaxTokens)
	}
	return allowance
}

func defaultCostBoundedOutputAllowance(inputTokens, contextWindow int) int {
	if contextWindow > 0 {
		// Use the context-derived remainder when it is useful, but never turn
		// an input pricing upper bound that saturates the context window into a
		// zero-output request. The named fallback becomes an explicit portable
		// cap and the complete input+output admission envelope is still priced;
		// provider adapters with different wire semantics normalize it through
		// ControllerConfig.NormalizeCostBoundedRequest.
		if remaining := contextWindow - inputTokens; remaining > 0 {
			return min(fallbackCostBoundedOutputTokens, remaining)
		}
	}
	return fallbackCostBoundedOutputTokens
}

func applyRequestOutputAllowance(req model.ChatRequest, allowance int) model.ChatRequest {
	allowance = max(1, allowance)
	switch {
	case req.MaxTokens > 0 && req.MaxCompletionTokens > 0:
		req.MaxTokens = min(req.MaxTokens, allowance)
		req.MaxCompletionTokens = min(req.MaxCompletionTokens, allowance)
	case req.MaxTokens > 0:
		req.MaxTokens = min(req.MaxTokens, allowance)
	case req.MaxCompletionTokens > 0:
		req.MaxCompletionTokens = min(req.MaxCompletionTokens, allowance)
	default:
		req.MaxTokens = allowance
	}
	if req.Reasoning != nil && req.Reasoning.MaxTokens > allowance {
		copy := *req.Reasoning
		copy.MaxTokens = allowance
		req.Reasoning = &copy
	}
	return req
}

func (c *Controller) buildFinalizationRequest(ctx context.Context, round int, reason string) (model.ChatRequest, error) {
	req, err := c.cfg.BuildRequest(ctx, round)
	if err != nil {
		return model.ChatRequest{}, fmt.Errorf("agentloop: build finalization request: %w", err)
	}
	req.Tools = nil
	req.ToolChoice = "none"
	req.ParallelToolCalls = nil
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "the harness stopped further tool execution"
	}
	messages := append([]model.Message(nil), req.Messages...)
	req.Messages = append(messages, model.Message{
		// A terminal user message is legal across providers that reject
		// system/developer roles after conversation or tool messages.
		Role: "user",
		Content: "Buckley stopped further tool execution because " + strings.TrimSuffix(reason, ".") + ". " +
			"Do not call tools. Use only the evidence already present in the conversation, state any remaining uncertainty, and return the most useful complete final answer you can.",
	})

	contextWindow := 0
	if c.cfg.ContextWindow != nil {
		contextWindow = c.cfg.ContextWindow(req.Model)
	}
	// Finalization deliberately sends a self-contained projected transcript.
	// Reusing an opaque continuation after a harness intervention could omit the
	// newest tool evidence from providers with stale cursor state.
	req = ProjectForContinuation(req, contextWindow, nil, "", false)
	return req, nil
}

func (c *Controller) failFinalization(ctx context.Context, result *Result, cause error) (*Result, error) {
	if cause == nil {
		cause = fmt.Errorf("unknown finalization failure")
	}
	result.CompletionStatus = CompletionIncomplete
	result.Termination.FinalizationError = modelstep.NormalizeError(cause)
	c.recordDecision(ctx, "finalization_failed", result.Termination.FinalizationError)
	return result, result.RequireConclusive()
}

func (c *Controller) stopForCostLimit(ctx context.Context, result *Result, cause error, round int) (*Result, error) {
	if result == nil {
		result = &Result{CompletionStatus: CompletionIncomplete}
	}
	reason := "explicit child cost ceiling stopped the model request"
	if cause != nil && strings.TrimSpace(cause.Error()) != "" {
		reason = cause.Error()
	}
	result.FinishReason = FinishReasonLoopGuard
	result.GuardDecision = Decision{Stop: true, Kind: "cost_limit", Reason: reason}
	result.Content = GuardStopMessage(reason)
	result.CompletionStatus = CompletionIncomplete
	result.Termination = Termination{Kind: "cost_limit", Reason: reason}
	c.recordDecision(ctx, "cost_limit", reason)
	if c != nil && c.cfg.FinalizeOnStop && c.cfg.Governor.ToolCalls() > 0 {
		return c.finalizeStoppedTurn(ctx, result, round)
	}
	return result, result.RequireConclusive()
}

func (c *Controller) executePreparedModelRound(ctx context.Context, result *Result, req model.ChatRequest, round int, stepKind string, useContinuation bool) (*modelRoundResult, error) {
	stepID := StableStepID(c.cfg.RunID, c.cfg.TaskID, c.cfg.TurnID, round, stepKind, 0)
	// The durable input is the caller's logical request before the execution
	// policy applies a spend-dependent output clamp. This lets a completed step
	// replay without consulting current pricing or creating a second reservation.
	inputDigest, err := jsonDigest(req)
	if err != nil {
		return nil, fmt.Errorf("agentloop: digest model request: %w", err)
	}
	blockedResult, blocked, err := c.replayBlockedModelStep(ctx, stepID, inputDigest, req.Model, round, stepKind)
	if blocked {
		if result != nil {
			result.ModelRequests++
		}
		return blockedResult, err
	}
	if err != nil {
		return nil, err
	}
	step, replay, err := c.beginStep(ctx, stepID, stepKind, inputDigest)
	if err != nil {
		return nil, err
	}
	planned := modelStepPayload(stepID, step.Attempt, inputDigest, req.Model, round, "")
	planned["phase"] = stepKind
	if replay {
		if result != nil {
			// Count the already-terminal logical request against the same all-in
			// request contract, but do not reserve or dispatch it again.
			result.ModelRequests++
		}
		switch step.Status {
		case runledger.StepCompleted:
			roundResult, err := c.replayModelStep(ctx, stepID, step, planned)
			return roundResult, err
		case runledger.StepBlocked:
			return c.replayBlockedExecutionStep(ctx, step, inputDigest, req.Model, round, stepKind)
		default:
			return nil, fmt.Errorf("agentloop: model step %s returned replay for non-terminal status %q", stepID, step.Status)
		}
	}

	req, reservation, err := c.reserveModelRequest(req, resultCostUSD(result))
	if err != nil {
		c.failStep(ctx, step, err)
		return nil, err
	}
	requestEvidenceID, _, err := c.recordJSONEvidence(ctx, evidence.KindModelRequest, req, stepID, map[string]any{"round": round})
	if err != nil {
		c.failStep(ctx, step, err)
		return nil, err
	}
	planned = modelStepPayload(stepID, step.Attempt, inputDigest, req.Model, round, requestEvidenceID)
	planned["phase"] = stepKind
	if c.cfg.MaxCostUSD > 0 {
		planned["estimated_prompt_tokens"] = reservation.inputTokens
		planned["reserved_completion_tokens"] = reservation.outputTokens
		planned["reserved_cost_usd"] = reservation.envelopeUSD
		planned["remaining_cost_usd"] = reservation.remainingUSD
	}
	if result != nil {
		result.ModelRequests++
	}
	return c.callAndRecordModelStep(ctx, req, step, inputDigest, planned, round, stepKind, useContinuation)
}

func resultCostUSD(result *Result) float64 {
	if result == nil {
		return 0
	}
	return result.CostUSD
}

func (c *Controller) projectedModelRequest(ctx context.Context, round int) (model.ChatRequest, int, bool, error) {
	req, err := c.cfg.BuildRequest(ctx, round)
	if err != nil {
		return model.ChatRequest{}, 0, false, fmt.Errorf("agentloop: build request: %w", err)
	}
	contextWindow := 0
	if c.cfg.ContextWindow != nil {
		contextWindow = c.cfg.ContextWindow(req.Model)
	}
	useContinuation := c.cfg.Continuation != nil && c.cfg.ContinuationEligible != nil && c.cfg.ContinuationEligible(req.Model)
	providerID := ""
	if useContinuation && c.cfg.ProviderID != nil {
		providerID = c.cfg.ProviderID(req.Model)
	}
	req = ProjectForContinuation(req, contextWindow, c.cfg.Continuation, providerID, useContinuation)
	return req, contextWindow, useContinuation, nil
}

func modelStepPayload(stepID string, attempt int, inputDigest, modelID string, round int, requestEvidenceID string) map[string]any {
	payload := stepPayload(stepID, attempt, inputDigest)
	payload["model"] = modelID
	payload["round"] = round
	if requestEvidenceID != "" {
		payload["request_evidence_id"] = requestEvidenceID
	}
	return payload
}

func (c *Controller) replayBlockedModelStep(ctx context.Context, stepID, inputDigest, modelID string, round int, stepKind string) (*modelRoundResult, bool, error) {
	if c == nil || c.cfg.StepJournal == nil {
		return nil, false, nil
	}
	step, err := c.cfg.StepJournal.GetStep(ctx, c.cfg.RunID, stepID)
	if errors.Is(err, runledger.ErrStepNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("agentloop: inspect model step %s before retry: %w", stepID, err)
	}
	if step.Status != runledger.StepBlocked {
		return nil, false, nil
	}
	roundResult, replayErr := c.replayBlockedExecutionStep(ctx, step, inputDigest, modelID, round, stepKind)
	return roundResult, true, replayErr
}

func (c *Controller) replayBlockedExecutionStep(ctx context.Context, step runledger.ExecutionStep, inputDigest, modelID string, round int, stepKind string) (*modelRoundResult, error) {
	stepID := step.StepID
	if step.InputDigest != "" && inputDigest != "" && step.InputDigest != inputDigest {
		return nil, fmt.Errorf("agentloop: execution step %s input digest changed", stepID)
	}
	if !strings.HasPrefix(step.Error, blockedModelStepErrorPrefix) {
		return nil, runledger.RecoveryErrorForStep(step)
	}
	marker, err := modelstep.ValidateBlockedStep(step)
	if err != nil {
		return nil, fmt.Errorf("agentloop: validate retry block for model step %s: %w", stepID, err)
	}
	roundResult := &modelRoundResult{
		partial:       true,
		providerError: marker.ProviderError,
	}
	replayErr := errors.Join(errors.New(marker.ProviderError), errors.New(marker.DurabilityError))
	evidenceID := marker.ResponseEvidenceID
	var object *evidence.Object
	if evidenceID != "" {
		if c.cfg.Evidence == nil {
			return roundResult, errors.Join(replayErr, fmt.Errorf("agentloop: evidence store is required to replay %s", evidenceID))
		} else {
			obj, loadErr := c.cfg.Evidence.Get(ctx, evidenceID)
			if loadErr != nil {
				return roundResult, errors.Join(replayErr, fmt.Errorf("agentloop: load retry-blocked evidence %s: %w", evidenceID, loadErr))
			}
			object = &obj
		}
	}
	blocked, err := modelstep.ValidateBlockedReplay(step, object)
	if err != nil {
		return roundResult, errors.Join(replayErr, fmt.Errorf("agentloop: validate retry-blocked model step %s: %w", stepID, err))
	}
	if blocked.Response != nil {
		roundResult.response = blocked.Response.Response
		roundResult.chargedCostUSD = blocked.Response.ChargedCostUSD
		roundResult.costRecorded = blocked.Response.CostRecorded
		roundResult.pricingError = blocked.Response.PricingError
	}
	payload := modelStepPayload(stepID, step.Attempt, inputDigest, modelID, round, "")
	payload["phase"] = stepKind
	payload["replayed"] = true
	payload["retry_blocked"] = true
	payload["provider_error"] = roundResult.providerError
	if evidenceID != "" {
		payload["response_evidence_id"] = evidenceID
	}
	c.recordEventWithEvidence(ctx, runledger.EventModelRequestReplayed, payload, evidenceIDs(evidenceID))
	return roundResult, replayErr
}

func (c *Controller) blockModelStepRetry(ctx context.Context, step runledger.ExecutionStep, providerError string, durabilityErr error, responseEvidenceID, outputDigest string) error {
	if c == nil || c.cfg.StepJournal == nil || providerError == "" {
		return nil
	}
	record := blockedModelStepRecord{
		Version:            modelstep.BlockedMarkerVersion,
		Incomplete:         true,
		ProviderError:      providerError,
		DurabilityError:    modelstep.NormalizeError(durabilityErr),
		ResponseEvidenceID: strings.TrimSpace(responseEvidenceID),
		OutputDigest:       strings.TrimSpace(outputDigest),
	}
	failure, err := modelstep.EncodeBlockedMarker(record)
	if err != nil {
		return fmt.Errorf("agentloop: encode retry block for model step %s: %w", step.StepID, err)
	}
	if err := c.cfg.StepJournal.BlockStep(ctx, step, failure, record.ResponseEvidenceID, record.OutputDigest, time.Now().UTC()); err != nil {
		return fmt.Errorf("agentloop: terminally block model step %s: %w", step.StepID, err)
	}
	return nil
}

func boundedModelStepError(err error) string {
	return modelstep.NormalizeError(err)
}

func (c *Controller) replayModelStep(ctx context.Context, stepID string, step runledger.ExecutionStep, payload map[string]any) (*modelRoundResult, error) {
	if step.OutputEvidenceID == "" {
		return nil, fmt.Errorf("agentloop: completed model step %s has no response evidence", stepID)
	}
	if c == nil || c.cfg.Evidence == nil {
		return nil, fmt.Errorf("agentloop: evidence store is required to replay %s", step.OutputEvidenceID)
	}
	obj, err := c.cfg.Evidence.Get(ctx, step.OutputEvidenceID)
	if err != nil {
		return nil, fmt.Errorf("agentloop: load replay evidence %s: %w", step.OutputEvidenceID, err)
	}
	decoded, err := modelstep.ValidateResponseEvidence(step.OutputEvidenceID, step.OutputDigest, obj)
	if err != nil {
		return nil, fmt.Errorf("agentloop: validate replay evidence %s: %w", step.OutputEvidenceID, err)
	}
	payload["replayed"] = true
	payload["response_evidence_id"] = step.OutputEvidenceID
	if decoded.CostRecorded {
		payload[modelResponseCostMetadataKey] = decoded.ChargedCostUSD
	}
	c.recordEventWithEvidence(ctx, runledger.EventModelRequestReplayed, payload, []string{step.OutputEvidenceID})
	return &modelRoundResult{
		response:       decoded.Response,
		chargedCostUSD: decoded.ChargedCostUSD,
		costRecorded:   decoded.CostRecorded,
		pricingError:   decoded.PricingError,
		partial:        decoded.Partial,
		providerError:  decoded.ProviderError,
	}, nil
}

func (c *Controller) callAndRecordModelStep(ctx context.Context, req model.ChatRequest, step runledger.ExecutionStep, inputDigest string, planned map[string]any, round int, stepKind string, useContinuation bool) (*modelRoundResult, error) {
	c.recordEvent(ctx, runledger.EventModelRequestPlanned, planned)
	c.recordEvent(ctx, runledger.EventModelRequestStarted, planned)
	if err := c.markStepDispatched(ctx, step); err != nil {
		return nil, err
	}
	response, callErr := c.cfg.CallModel.Call(ctx, req, useContinuation)
	persistedProviderError := modelstep.NormalizeError(callErr)
	if callErr != nil && response == nil {
		blockErr := c.blockDispatchedStep(ctx, step, callErr, "", "")
		failed := modelStepPayload(step.StepID, step.Attempt, inputDigest, req.Model, round, "")
		failed["phase"] = stepKind
		failed["error"] = persistedProviderError
		c.recordEvent(ctx, runledger.EventModelRequestFailed, failed)
		return nil, errors.Join(callErr, blockErr)
	}
	if response == nil {
		err := fmt.Errorf("agentloop: model caller returned a nil response")
		blockErr := c.blockDispatchedStep(ctx, step, err, "", "")
		failed := modelStepPayload(step.StepID, step.Attempt, inputDigest, req.Model, round, "")
		failed["phase"] = stepKind
		failed["error"] = err.Error()
		c.recordEvent(ctx, runledger.EventModelRequestFailed, failed)
		return nil, errors.Join(err, blockErr)
	}
	chargedCostUSD := 0.0
	costRecorded := false
	pricingError := ""
	if c.cfg.CostForUsage != nil {
		var priceErr error
		chargedCostUSD, priceErr = c.priceUsage(response.Usage)
		if priceErr != nil {
			// The provider response already exists and may have incurred cost.
			// Persist both it and the pricing failure as a completed logical step
			// so retry fails closed without buying the response again.
			pricingError = modelstep.NormalizeError(priceErr)
			chargedCostUSD = 0
		} else {
			costRecorded = true
		}
	}
	responseEnvelope := modelResponseEvidenceEnvelope{
		Version:        modelResponseEvidenceVersion,
		Response:       response,
		ChargedCostUSD: chargedCostUSD,
		CostRecorded:   costRecorded,
		PricingError:   pricingError,
		Partial:        callErr != nil,
	}
	if callErr != nil {
		responseEnvelope.ProviderError = persistedProviderError
	}
	roundResult := &modelRoundResult{
		response:       response,
		chargedCostUSD: chargedCostUSD,
		costRecorded:   costRecorded,
		pricingError:   pricingError,
		partial:        callErr != nil,
	}
	if callErr != nil {
		// Keep callErr itself raw in the returned error chain for internal
		// classification/retry logic. Result, lifecycle, durable, and ACP-facing
		// projections receive only this bounded/redacted form.
		roundResult.providerError = persistedProviderError
	}
	responseBody, err := modelstep.EncodeResponse(responseEnvelope)
	if err != nil {
		if callErr == nil {
			return roundResult, errors.Join(err, c.blockDispatchedStep(ctx, step, err, "", ""))
		}
		blockErr := c.blockModelStepRetry(ctx, step, persistedProviderError, err, "", "")
		return roundResult, errors.Join(callErr, err, blockErr)
	}
	responseEvidenceID, outputDigest, err := c.recordEvidenceBody(ctx, evidence.KindModelResponse, responseBody, step.StepID, map[string]any{
		"round": round,
		"model": req.Model,
	})
	if err != nil {
		if callErr == nil {
			return roundResult, errors.Join(err, c.blockDispatchedStep(ctx, step, err, "", ""))
		}
		blockErr := c.blockModelStepRetry(ctx, step, persistedProviderError, err, responseEvidenceID, outputDigest)
		return roundResult, errors.Join(callErr, err, blockErr)
	}
	if err := c.completeStep(ctx, step, responseEvidenceID, outputDigest); err != nil {
		if callErr == nil {
			return roundResult, errors.Join(err, c.blockDispatchedStep(ctx, step, err, responseEvidenceID, outputDigest))
		}
		blockErr := c.blockModelStepRetry(ctx, step, persistedProviderError, err, responseEvidenceID, outputDigest)
		return roundResult, errors.Join(callErr, err, blockErr)
	}
	completed := modelStepPayload(step.StepID, step.Attempt, inputDigest, req.Model, round, "")
	completed["phase"] = stepKind
	completed["prompt_tokens"] = response.Usage.PromptTokens
	completed["completion_tokens"] = response.Usage.CompletionTokens
	if costRecorded {
		completed[modelResponseCostMetadataKey] = chargedCostUSD
	}
	if pricingError != "" {
		completed["pricing_error"] = pricingError
	}
	if responseEnvelope.Partial {
		completed["partial"] = true
		completed["provider_error"] = responseEnvelope.ProviderError
	}
	if responseEvidenceID != "" {
		completed["response_evidence_id"] = responseEvidenceID
	}
	eventType := runledger.EventModelRequestCompleted
	if responseEnvelope.Partial {
		eventType = runledger.EventModelRequestFailed
	}
	c.recordEventWithEvidence(ctx, eventType, completed, evidenceIDs(responseEvidenceID))
	return roundResult, callErr
}

// toolRoundState keeps the durable state for one batch of model-requested
// tools. It remains private because callers only need the Controller's turn
// result; keeping the bookkeeping together makes replay and dispatch order
// explicit without leaking a second orchestration API.
type toolRoundState struct {
	calls          []model.ToolCall
	steps          []runledger.ExecutionStep
	outcomes       []ToolOutcome
	replayed       []bool
	pendingCalls   []model.ToolCall
	pendingIndexes []int
	records        []map[string]any
}

func newToolRoundState(calls []model.ToolCall) *toolRoundState {
	return &toolRoundState{
		calls:          calls,
		steps:          make([]runledger.ExecutionStep, len(calls)),
		outcomes:       make([]ToolOutcome, len(calls)),
		replayed:       make([]bool, len(calls)),
		pendingCalls:   make([]model.ToolCall, 0, len(calls)),
		pendingIndexes: make([]int, 0, len(calls)),
		records:        make([]map[string]any, len(calls)),
	}
}

func (c *Controller) prepareToolRound(ctx context.Context, calls []model.ToolCall, round int) (*toolRoundState, error) {
	state := newToolRoundState(calls)
	for index := range state.calls {
		if err := c.prepareToolStep(ctx, state, round, index); err != nil {
			return nil, err
		}
	}
	c.recordEvent(ctx, runledger.EventToolRequested, map[string]any{
		"count":      len(state.calls),
		"round":      round,
		"call_steps": state.records,
	})
	for index := range state.calls {
		if state.replayed[index] {
			if err := c.replayToolStep(ctx, state, index); err != nil {
				return nil, err
			}
			continue
		}
		c.recordEvent(ctx, runledger.EventToolStarted, state.records[index])
	}
	return state, nil
}

func (c *Controller) prepareToolStep(ctx context.Context, state *toolRoundState, round, index int) error {
	call := state.calls[index]
	stepID := StableStepID(c.cfg.RunID, c.cfg.TaskID, c.cfg.TurnID, round, "tool", index)
	inputDigest, err := jsonDigest(call)
	if err != nil {
		return fmt.Errorf("agentloop: digest tool request: %w", err)
	}
	requestEvidenceID, _, err := c.recordJSONEvidence(ctx, evidence.KindToolRequest, call, stepID, map[string]any{"round": round, "tool": call.Function.Name})
	if err != nil {
		return err
	}
	step, replay, err := c.beginStep(ctx, stepID, "tool", inputDigest)
	if err != nil {
		return err
	}
	state.steps[index] = step
	record := stepPayload(stepID, step.Attempt, inputDigest)
	record["tool"] = call.Function.Name
	record["call_id"] = call.ID
	if requestEvidenceID != "" {
		record["request_evidence_id"] = requestEvidenceID
	}
	state.records[index] = record
	if replay {
		if step.Status == runledger.StepBlocked {
			return runledger.RecoveryErrorForStep(step)
		}
		if step.Status != runledger.StepCompleted {
			return fmt.Errorf("agentloop: tool step %s returned replay for terminal status %q", stepID, step.Status)
		}
		state.replayed[index] = true
		return nil
	}
	state.pendingCalls = append(state.pendingCalls, call)
	state.pendingIndexes = append(state.pendingIndexes, index)
	return nil
}

func (c *Controller) replayToolStep(ctx context.Context, state *toolRoundState, index int) error {
	step := state.steps[index]
	if step.OutputEvidenceID == "" {
		return fmt.Errorf("agentloop: completed tool step %s has no result evidence", step.StepID)
	}
	if err := c.loadJSONEvidence(ctx, step.OutputEvidenceID, &state.outcomes[index]); err != nil {
		return err
	}
	state.replayed[index] = true
	record := state.records[index]
	record["replayed"] = true
	record["output_evidence_id"] = step.OutputEvidenceID
	c.recordEventWithEvidence(ctx, runledger.EventToolReplayed, record, []string{step.OutputEvidenceID})
	return nil
}

func (c *Controller) dispatchToolRound(ctx context.Context, state *toolRoundState, round int) error {
	if len(state.pendingCalls) == 0 {
		return nil
	}
	if c.cfg.DispatchTools == nil {
		return fmt.Errorf("agentloop: model requested tools but no ToolDispatcher is configured")
	}
	for _, index := range state.pendingIndexes {
		if err := c.markStepDispatched(ctx, state.steps[index]); err != nil {
			return err
		}
	}
	pendingOutcomes, dispatchErr := c.cfg.DispatchTools.Dispatch(ctx, state.pendingCalls)
	// Once Dispatch reports outcomes, external effects may already exist. Give
	// their receipts a short cleanup window that cannot be cut off by a caller
	// cancellation racing with this handoff.
	journalCtx, journalCancel := context.WithTimeout(context.WithoutCancel(ctx), partialDispatchJournalTimeout)
	defer journalCancel()
	resolved := min(len(pendingOutcomes), len(state.pendingIndexes))
	for position := 0; position < resolved; position++ {
		index := state.pendingIndexes[position]
		state.outcomes[index] = pendingOutcomes[position]
		if err := c.persistToolOutcome(journalCtx, state, round, index); err != nil {
			return errors.Join(err, c.blockPendingToolSteps(journalCtx, state, position+1, err))
		}
	}
	if dispatchErr != nil {
		return errors.Join(dispatchErr, c.blockPendingToolSteps(journalCtx, state, resolved, dispatchErr))
	}
	if len(pendingOutcomes) != len(state.pendingIndexes) {
		err := fmt.Errorf("agentloop: tool dispatcher returned %d outcomes for %d calls", len(pendingOutcomes), len(state.pendingIndexes))
		return errors.Join(err, c.blockPendingToolSteps(journalCtx, state, resolved, err))
	}
	return nil
}

func (c *Controller) blockPendingToolSteps(ctx context.Context, state *toolRoundState, start int, dispatchErr error) error {
	var blockErrors []error
	for _, index := range state.pendingIndexes[start:] {
		blockErrors = append(blockErrors, c.blockDispatchedStep(ctx, state.steps[index], dispatchErr, "", ""))
		failed := state.records[index]
		failed["error"] = modelstep.NormalizeError(dispatchErr)
		c.recordEvent(ctx, runledger.EventToolFailed, failed)
	}
	return errors.Join(blockErrors...)
}

func (c *Controller) persistToolOutcome(ctx context.Context, state *toolRoundState, round, index int) error {
	call := state.calls[index]
	step := state.steps[index]
	outcome := state.outcomes[index]
	outputEvidenceID, outputDigest, err := c.recordJSONEvidence(ctx, evidence.KindToolResult, outcome, step.StepID, map[string]any{
		"round": round,
		"tool":  call.Function.Name,
	})
	if err != nil {
		return errors.Join(err, c.blockDispatchedStep(ctx, step, err, "", ""))
	}
	record := state.records[index]
	record["effect_class"] = outcome.EffectClass
	record["success"] = outcome.Success
	if !outcome.Success {
		errorText, stderr := normalizedToolFailure(outcome)
		record["error"] = errorText
		if stderr != "" {
			record["stderr"] = stderr
		}
	}
	record["yield_observed"] = outcome.YieldObserved
	if outcome.YieldObserved {
		record["yield_count"] = outcome.YieldCount
		record["yield_unit"] = outcome.YieldUnit
	}
	if outputEvidenceID != "" {
		record["output_evidence_id"] = outputEvidenceID
	}
	if err := c.completeStep(ctx, step, outputEvidenceID, outputDigest); err != nil {
		return errors.Join(err, c.blockDispatchedStep(ctx, step, err, outputEvidenceID, outputDigest))
	}
	if outcome.Success {
		c.recordEventWithEvidence(ctx, runledger.EventToolCompleted, record, evidenceIDs(outputEvidenceID))
		return nil
	}
	c.recordEventWithEvidence(ctx, runledger.EventToolFailed, record, evidenceIDs(outputEvidenceID))
	return nil
}

func normalizedToolFailure(outcome ToolOutcome) (string, string) {
	errorText := strings.TrimSpace(outcome.Error)
	stderr := strings.TrimSpace(outcome.Stderr)
	if errorText == "" {
		content := strings.TrimSpace(outcome.Content)
		if strings.HasPrefix(strings.ToLower(content), "error:") {
			errorText = strings.TrimSpace(content[len("error:"):])
		} else {
			for _, line := range strings.Split(content, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(strings.ToLower(line), "error:") {
					errorText = strings.TrimSpace(line[len("error:"):])
					break
				}
			}
		}
	}
	if errorText == "" {
		errorText = "tool reported an unsuccessful result"
	}
	return modelstep.NormalizeErrorText(errorText), modelstep.NormalizeErrorText(stderr)
}

func (c *Controller) observeToolRound(ctx context.Context, state *toolRoundState, progress *progressTracker) (Decision, error) {
	var stopDecision Decision
	for index, call := range state.calls {
		outcome := state.outcomes[index]
		progress.Observe(call.Function.Name, outcome)
		if c.cfg.ObserveToolOutcome != nil {
			if err := c.cfg.ObserveToolOutcome(ctx, call, outcome, state.replayed[index]); err != nil {
				return Decision{}, fmt.Errorf("agentloop: observe tool %s outcome: %w", call.Function.Name, err)
			}
		}
		content := outcome.Content
		decision := c.cfg.Governor.Observe(call.Function.Name, call.Function.Arguments, content, outcome.Success)
		if strings.TrimSpace(decision.Nudge) != "" {
			content += "\n\n" + decision.Nudge
		}
		if c.cfg.History != nil {
			c.cfg.History.Append(model.Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Name:       call.Function.Name,
				Content:    content,
			})
		}
		if decision.Stop && !stopDecision.Stop {
			stopDecision = decision
		}
	}
	return stopDecision, nil
}

// consultProgress runs the section-20 progress policy at the end of one
// tool round, records the decision with its trace, and reports whether
// the turn must stop (an applied stop_safety). Every other decision is
// shadow-recorded only; routing them is goal-loop scope.
func (c *Controller) consultProgress(ctx context.Context, result *Result, usage model.Usage, contextWindow int, started time.Time) bool {
	if c.cfg.Progress == nil {
		return false
	}

	state := ProgressState{
		Repetition:                c.cfg.Governor.RepetitionPressure(),
		ToolCalls:                 result.Progress.ToolCalls,
		SuccessfulToolCalls:       result.Progress.SuccessfulToolCalls,
		FailedToolCalls:           result.Progress.FailedToolCalls,
		YieldObservedCalls:        result.Progress.YieldObservedCalls,
		ZeroYieldCalls:            result.Progress.ZeroYieldCalls,
		ConsecutiveZeroYieldCalls: result.Progress.ConsecutiveZeroYieldCalls,
	}
	if c.cfg.MaxCostUSD > 0 {
		state.BudgetSet = true
		state.BudgetRemaining = (c.cfg.MaxCostUSD - result.CostUSD) / c.cfg.MaxCostUSD
	}
	state.EvidenceNovelty, state.EvidenceObserved = c.cfg.Governor.EvidenceNovelty()
	if contextWindow > 0 && usage.PromptTokens > 0 {
		state.Pressure = float64(usage.PromptTokens) / float64(contextWindow)
	}

	decision := c.cfg.Progress.Decide(state, FuseCounters{
		ModelRequests:  result.ModelRequests,
		ToolExecutions: result.ToolCalls,
		Elapsed:        time.Since(started),
		SpentUSD:       result.CostUSD,
	})

	applied := decision.Apply && decision.Decision == DecideStopSafety
	trace := make([]map[string]any, 0, len(decision.Trace))
	for _, step := range decision.Trace {
		trace = append(trace, map[string]any{"rule": step.Rule, "fired": step.Fired})
	}
	c.recordEvent(ctx, runledger.EventControllerDecision, map[string]any{
		"kind":     "progress_policy",
		"decision": string(decision.Decision),
		"reason":   decision.Reason,
		"mode":     c.cfg.Progress.Mode,
		"applied":  applied,
		"trace":    trace,
		"progress": result.Progress,
	})

	if !applied {
		return false
	}
	result.FinishReason = FinishReasonLoopGuard
	result.GuardDecision = Decision{Stop: true, Kind: "emergency_fuse", Reason: decision.Reason}
	result.Content = GuardStopMessage(decision.Reason)
	return true
}

func (c *Controller) recordEvent(ctx context.Context, eventType string, payload map[string]any) {
	c.recordEventWithEvidence(ctx, eventType, payload, nil)
}

func (c *Controller) recordEventWithEvidence(ctx context.Context, eventType string, payload map[string]any, evidenceIDs []string) {
	if c == nil {
		return
	}
	payload = normalizedDurableEventPayload(payload)
	timestamp := time.Now().UTC()
	c.emitLifecycle(lifecycleEventFromLedger(c, eventType, timestamp, payload, evidenceIDs))
	if c.cfg.RunLedger == nil {
		return
	}
	_, _ = c.cfg.RunLedger.Append(ctx, runledger.Event{
		Type:        eventType,
		Timestamp:   timestamp,
		SessionID:   c.cfg.SessionID,
		RunID:       c.cfg.RunID,
		TaskID:      c.cfg.TaskID,
		EvidenceIDs: evidenceIDs,
		Payload:     payload,
	})
}

func normalizedDurableEventPayload(payload map[string]any) map[string]any {
	if payload == nil {
		return nil
	}
	out := make(map[string]any, len(payload))
	for key, value := range payload {
		out[key] = value
	}
	for _, key := range []string{"error", "stderr", "provider_error", "pricing_error", "reason"} {
		if value, ok := out[key].(string); ok {
			out[key] = modelstep.NormalizeErrorText(value)
		}
	}
	return out
}

func (c *Controller) emitLifecycle(event LifecycleEvent) {
	if c == nil || c.cfg.LifecycleObserver == nil {
		return
	}
	c.lifecycleSeq++
	event.Sequence = c.lifecycleSeq
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	event.RunID = c.cfg.RunID
	event.SessionID = c.cfg.SessionID
	event.TaskID = c.cfg.TaskID
	event.TurnID = c.cfg.TurnID
	event.EvidenceIDs = append([]string(nil), event.EvidenceIDs...)
	observer := c.cfg.LifecycleObserver
	// An observer is a live projection, not a part of the turn's correctness
	// path. Contain both observer panics and accidental mutation of the event
	// value so telemetry cannot alter the next transition.
	func() {
		defer func() { _ = recover() }()
		observer(event)
	}()
}

func lifecycleEventFromLedger(c *Controller, eventType string, timestamp time.Time, payload map[string]any, evidenceIDs []string) LifecycleEvent {
	event := LifecycleEvent{
		Type:        lifecycle.TypeForLedger(eventType),
		Timestamp:   timestamp,
		EvidenceIDs: append([]string(nil), evidenceIDs...),
	}
	if c != nil {
		event.RunID = c.cfg.RunID
		event.SessionID = c.cfg.SessionID
		event.TaskID = c.cfg.TaskID
		event.TurnID = c.cfg.TurnID
	}
	if payload == nil {
		return event
	}
	event.StepID, _ = payloadString(payload, "step_id")
	event.Phase, _ = payloadString(payload, "phase")
	event.ModelID, _ = payloadString(payload, "model")
	event.ToolName, _ = payloadString(payload, "tool")
	event.ToolCallID, _ = payloadString(payload, "call_id")
	if event.ToolName == "" {
		if steps, ok := payload["call_steps"].([]map[string]any); ok && len(steps) > 0 {
			event.ToolName, _ = payloadString(steps[0], "tool")
			event.ToolCallID, _ = payloadString(steps[0], "call_id")
		}
	}
	event.Error, _ = payloadString(payload, "error")
	event.Stderr, _ = payloadString(payload, "stderr")
	if event.Error == "" {
		event.Error, _ = payloadString(payload, "pricing_error")
	}
	if event.Error == "" {
		event.Error, _ = payloadString(payload, "provider_error")
	}
	// Controller decision reasons are intentionally free-form and can embed
	// provider failures or finalization causes. The live lifecycle contract
	// carries only the categorical decision kind; durable ledger records and
	// the normal result/error path retain the reason.
	event.StopReason, _ = payloadString(payload, "kind")
	event.Round = payloadInt(payload, "round")
	event.Attempt = payloadInt(payload, "attempt")
	event.Replayed = payloadBool(payload, "replayed")
	if success, ok := payload["success"].(bool); ok {
		event.Success = &success
	}
	event.Usage.PromptTokens = payloadInt(payload, "prompt_tokens")
	event.Usage.CompletionTokens = payloadInt(payload, "completion_tokens")
	event.Usage.TotalTokens = event.Usage.PromptTokens + event.Usage.CompletionTokens
	if event.CostUSD, _ = payloadFloat(payload, modelResponseCostMetadataKey); event.CostUSD < 0 {
		event.CostUSD = 0
	}
	return event
}

func payloadString(payload map[string]any, key string) (string, bool) {
	value, ok := payload[key].(string)
	return strings.TrimSpace(value), ok
}

func payloadInt(payload map[string]any, key string) int {
	switch value := payload[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func payloadBool(payload map[string]any, key string) bool {
	value, _ := payload[key].(bool)
	return value
}

func payloadFloat(payload map[string]any, key string) (float64, bool) {
	value, ok := payload[key].(float64)
	if ok {
		return value, true
	}
	switch typed := payload[key].(type) {
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	default:
		return 0, false
	}
}

func (c *Controller) recordDecision(ctx context.Context, kind, reason string) {
	c.recordEvent(ctx, runledger.EventControllerDecision, map[string]any{
		"kind":   kind,
		"reason": modelstep.NormalizeErrorText(reason),
	})
}

// BackfillToolCallIDs assigns a stable positional ID ("tool-1", "tool-2",
// ...) to any tool call the provider returned without one. Several callers
// duplicated this exact loop before the shared engine existed; it stays
// exported so pkg/toolrunner and future callers outside the Controller can
// share it too.
func BackfillToolCallIDs(calls []model.ToolCall) []model.ToolCall {
	for i := range calls {
		if calls[i].ID == "" {
			calls[i].ID = fmt.Sprintf("tool-%d", i+1)
		}
	}
	return calls
}

// GuardStopMessage formats a Governor stop Decision.Reason into the prose
// Controller returns as Result.Content. Exported so callers that render
// their own completion text (and pkg/toolrunner, which predates the shared
// engine) produce identical wording.
func GuardStopMessage(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "the harness detected repeated tool activity without new evidence"
	}
	return fmt.Sprintf("Buckley stopped the tool loop because %s. Existing tool results remain available; use a different strategy or a narrower follow-up before continuing.", strings.TrimSuffix(reason, "."))
}

// ProjectForContinuation applies conversation.ProjectModelMessagesForRequestPinned
// to req.Messages, with continuation-aware pin support (decision 0001): when
// useContinuation is set and coordinator is non-nil, it restores the
// coordinator against req.Messages, pins the represented prefix, and -- if
// the pinned projection still overflows its budget -- resets the
// coordinator and recompiles once unpinned (the epoch rule: one full
// recompiled request instead of a mid-turn fingerprint mismatch). It is
// exported so a caller with its own tested request-building path (for
// example, a caller whose continuation wiring predates the shared engine)
// can share this exact logic without routing through Controller.Run.
func ProjectForContinuation(req model.ChatRequest, contextWindow int, coordinator *model.ContinuationCoordinator, providerID string, useContinuation bool) model.ChatRequest {
	rawMessages := req.Messages
	pinnedFromIndex := 0
	if useContinuation && coordinator != nil {
		coordinator.Restore(providerID, req.Model, rawMessages)
		pinnedFromIndex = coordinator.PinnedFromIndex()
	}
	projected, stats := conversation.ProjectModelMessagesForRequestPinned(rawMessages, req, contextWindow, 1, pinnedFromIndex)
	if pinnedFromIndex > 0 && stats.ProjectedTokens > stats.BudgetTokens {
		coordinator.Reset()
		projected, _ = conversation.ProjectModelMessagesForRequestPinned(rawMessages, req, contextWindow, 1, 0)
	}
	req.Messages = projected
	return req
}
