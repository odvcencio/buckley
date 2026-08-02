package agentloop

import (
	"context"
	"fmt"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/runledger"
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
	cfg ControllerConfig
}

// RequestBuilder returns the base chat request for one round. Its Messages
// field must carry the full, unprojected working transcript (the caller's
// own conversation store or message slice); Controller replaces Messages
// with the projected result before calling the model. round is the 1-based
// round number the Governor assigned this call (see Governor.Rounds).
type RequestBuilder func(ctx context.Context, round int) (model.ChatRequest, error)

// ModelCaller performs one model turn against an already-projected request.
// useContinuation reports whether Controller resolved a provider
// continuation window for this round (see ControllerConfig.Continuation);
// implementations that do not use continuation can ignore it.
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
}

// ToolDispatcher executes one round's tool calls, in the same order the
// model requested them, and returns one ToolOutcome per call. IDs are
// already backfilled (see BackfillToolCallIDs) by the time Controller calls
// Dispatch. A non-nil error aborts the turn immediately (for example,
// context cancellation while a caller waits on an approval); Controller
// does not retry.
type ToolDispatcher interface {
	Dispatch(ctx context.Context, calls []model.ToolCall) ([]ToolOutcome, error)
}

// ToolDispatcherFunc adapts a function to ToolDispatcher.
type ToolDispatcherFunc func(ctx context.Context, calls []model.ToolCall) ([]ToolOutcome, error)

// Dispatch implements ToolDispatcher.
func (f ToolDispatcherFunc) Dispatch(ctx context.Context, calls []model.ToolCall) ([]ToolOutcome, error) {
	return f(ctx, calls)
}

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
	// choices. Controller does not treat this as an error; callers differ
	// on whether an empty response is a warning or a failure.
	FinishReasonEmptyChoices = "empty_choices"
)

// Result is the outcome of one Controller.Run call.
type Result struct {
	// Message is the final assistant response when the turn ended
	// normally (FinishReason == ""). Its zero value otherwise.
	Message model.Message
	// Usage accumulates every round's reported token usage (model.AddUsage).
	Usage model.Usage
	// Rounds is the number of model rounds executed (Governor.Rounds).
	Rounds int
	// ToolCalls is the number of tool calls dispatched across every round.
	ToolCalls int
	// FinishReason is "" on a normal completion, or one of the
	// FinishReason* constants describing why the turn stopped early.
	FinishReason string
	// GuardDecision is populated when FinishReason == FinishReasonLoopGuard.
	GuardDecision Decision
	// Content is a caller-facing message describing an abnormal stop
	// (loop guard or step cap). Empty on a normal completion or an empty
	// model response, since callers already differ on how to surface those.
	Content string
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

	// BuildRequest is required.
	BuildRequest RequestBuilder
	// CallModel is required.
	CallModel ModelCaller
	// DispatchTools is required whenever the model can return tool calls;
	// Controller errors lazily if a turn needs it and it is unset.
	DispatchTools ToolDispatcher
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

	// RunLedger, when set, receives one event per model request
	// (started/completed/failed), one per tool-dispatch batch, and one per
	// controller stop decision. RunID and SessionID identify the events;
	// TaskID is optional. Recording is best-effort: a ledger write failure
	// never fails the turn.
	RunLedger runledger.Store
	RunID     string
	SessionID string
	TaskID    string
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
func (c *Controller) Run(ctx context.Context) (*Result, error) {
	if c == nil {
		return nil, fmt.Errorf("agentloop: nil controller")
	}
	result := &Result{}

	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}

		govDecision := c.cfg.Governor.BeginRound()
		result.Rounds = c.cfg.Governor.Rounds()
		if c.cfg.StepCap > 0 && result.Rounds > c.cfg.StepCap {
			result.FinishReason = FinishReasonStepCap
			result.Content = fmt.Sprintf("Buckley stopped after the %d-step persona limit.", c.cfg.StepCap)
			c.recordDecision(ctx, "step_cap", result.Content)
			return result, nil
		}
		if govDecision.Stop {
			result.FinishReason = FinishReasonLoopGuard
			result.GuardDecision = govDecision
			result.Content = GuardStopMessage(govDecision.Reason)
			c.recordDecision(ctx, govDecision.Kind, govDecision.Reason)
			return result, nil
		}

		req, err := c.cfg.BuildRequest(ctx, result.Rounds)
		if err != nil {
			return result, fmt.Errorf("agentloop: build request: %w", err)
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

		c.recordEvent(ctx, runledger.EventModelRequestStarted, map[string]any{"model": req.Model, "round": result.Rounds})
		resp, err := c.cfg.CallModel.Call(ctx, req, useContinuation)
		if err != nil {
			c.recordEvent(ctx, runledger.EventModelRequestFailed, map[string]any{"model": req.Model, "error": err.Error()})
			return result, err
		}
		if resp == nil || len(resp.Choices) == 0 {
			result.FinishReason = FinishReasonEmptyChoices
			return result, nil
		}
		result.Usage = model.AddUsage(result.Usage, resp.Usage)
		c.recordEvent(ctx, runledger.EventModelRequestCompleted, map[string]any{
			"model":             req.Model,
			"prompt_tokens":     resp.Usage.PromptTokens,
			"completion_tokens": resp.Usage.CompletionTokens,
		})

		msg := resp.Choices[0].Message
		if len(msg.ToolCalls) == 0 {
			result.Message = msg
			if c.cfg.History != nil {
				c.cfg.History.Append(msg)
			}
			return result, nil
		}

		msg.ToolCalls = BackfillToolCallIDs(msg.ToolCalls)
		if c.cfg.History != nil {
			c.cfg.History.Append(msg)
		}
		if c.cfg.DispatchTools == nil {
			return result, fmt.Errorf("agentloop: model requested tools but no ToolDispatcher is configured")
		}

		c.recordEvent(ctx, runledger.EventToolRequested, map[string]any{"count": len(msg.ToolCalls)})
		outcomes, err := c.cfg.DispatchTools.Dispatch(ctx, msg.ToolCalls)
		if err != nil {
			return result, err
		}
		result.ToolCalls += len(msg.ToolCalls)

		var stopDecision Decision
		for i, call := range msg.ToolCalls {
			outcome := ToolOutcome{}
			if i < len(outcomes) {
				outcome = outcomes[i]
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
		if stopDecision.Stop {
			result.FinishReason = FinishReasonLoopGuard
			result.GuardDecision = stopDecision
			result.Content = GuardStopMessage(stopDecision.Reason)
			c.recordDecision(ctx, stopDecision.Kind, stopDecision.Reason)
			return result, nil
		}
	}
}

func (c *Controller) recordEvent(ctx context.Context, eventType string, payload map[string]any) {
	if c == nil || c.cfg.RunLedger == nil {
		return
	}
	_, _ = c.cfg.RunLedger.Append(ctx, runledger.Event{
		Type:      eventType,
		Timestamp: time.Now(),
		SessionID: c.cfg.SessionID,
		RunID:     c.cfg.RunID,
		TaskID:    c.cfg.TaskID,
		Payload:   payload,
	})
}

func (c *Controller) recordDecision(ctx context.Context, kind, reason string) {
	c.recordEvent(ctx, runledger.EventControllerDecision, map[string]any{
		"kind":   kind,
		"reason": reason,
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
