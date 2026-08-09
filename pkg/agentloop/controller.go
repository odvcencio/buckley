package agentloop

import (
	"context"
	"fmt"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/evidence"
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
	// EffectClass is an optional caller-provided effect classification such
	// as readonly, modifying, destructive, or control. It is recorded with
	// the durable step event so a future retry policy can be conservative.
	EffectClass string
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
	// outputs instead of executing them again after a restart.
	StepJournal runledger.StepJournal
	RunID       string
	SessionID   string
	TaskID      string
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
	started := time.Now()

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

		stepID := StableStepID(c.cfg.RunID, c.cfg.TaskID, c.cfg.TurnID, result.Rounds, "model", 0)
		inputDigest, err := jsonDigest(req)
		if err != nil {
			return result, fmt.Errorf("agentloop: digest model request: %w", err)
		}
		requestEvidenceID, _, err := c.recordJSONEvidence(ctx, evidence.KindModelRequest, req, stepID, map[string]any{"round": result.Rounds})
		if err != nil {
			return result, err
		}
		step, replay, err := c.beginStep(ctx, stepID, "model", inputDigest)
		if err != nil {
			return result, err
		}
		planned := stepPayload(stepID, step.Attempt, inputDigest)
		planned["model"] = req.Model
		planned["round"] = result.Rounds
		if requestEvidenceID != "" {
			planned["request_evidence_id"] = requestEvidenceID
		}

		var resp *model.ChatResponse
		if replay {
			if step.OutputEvidenceID == "" {
				return result, fmt.Errorf("agentloop: completed model step %s has no response evidence", stepID)
			}
			resp = &model.ChatResponse{}
			if err := c.loadJSONEvidence(ctx, step.OutputEvidenceID, resp); err != nil {
				return result, err
			}
			planned["replayed"] = true
			planned["response_evidence_id"] = step.OutputEvidenceID
			c.recordEventWithEvidence(ctx, runledger.EventModelRequestReplayed, planned, []string{step.OutputEvidenceID})
		} else {
			c.recordEvent(ctx, runledger.EventModelRequestPlanned, planned)
			c.recordEvent(ctx, runledger.EventModelRequestStarted, planned)
			resp, err = c.cfg.CallModel.Call(ctx, req, useContinuation)
			if err != nil {
				c.failStep(ctx, step, err)
				failed := stepPayload(stepID, step.Attempt, inputDigest)
				failed["model"] = req.Model
				failed["round"] = result.Rounds
				failed["error"] = err.Error()
				c.recordEvent(ctx, runledger.EventModelRequestFailed, failed)
				return result, err
			}
			responseEvidenceID, outputDigest, err := c.recordJSONEvidence(ctx, evidence.KindModelResponse, resp, stepID, map[string]any{"round": result.Rounds, "model": req.Model})
			if err != nil {
				c.failStep(ctx, step, err)
				return result, err
			}
			if err := c.completeStep(ctx, step, responseEvidenceID, outputDigest); err != nil {
				c.failStep(ctx, step, err)
				return result, err
			}
			completed := stepPayload(stepID, step.Attempt, inputDigest)
			completed["model"] = req.Model
			completed["round"] = result.Rounds
			completed["prompt_tokens"] = resp.Usage.PromptTokens
			completed["completion_tokens"] = resp.Usage.CompletionTokens
			if responseEvidenceID != "" {
				completed["response_evidence_id"] = responseEvidenceID
			}
			c.recordEventWithEvidence(ctx, runledger.EventModelRequestCompleted, completed, evidenceIDs(responseEvidenceID))
		}
		if resp == nil || len(resp.Choices) == 0 {
			result.FinishReason = FinishReasonEmptyChoices
			return result, nil
		}
		result.Usage = model.AddUsage(result.Usage, resp.Usage)

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
		toolSteps := make([]runledger.ExecutionStep, len(msg.ToolCalls))
		outcomes := make([]ToolOutcome, len(msg.ToolCalls))
		replayedTools := make([]bool, len(msg.ToolCalls))
		pendingCalls := make([]model.ToolCall, 0, len(msg.ToolCalls))
		pendingIndexes := make([]int, 0, len(msg.ToolCalls))
		callRecords := make([]map[string]any, len(msg.ToolCalls))
		for i, call := range msg.ToolCalls {
			toolStepID := StableStepID(c.cfg.RunID, c.cfg.TaskID, c.cfg.TurnID, result.Rounds, "tool", i)
			inputDigest, err := jsonDigest(call)
			if err != nil {
				return result, fmt.Errorf("agentloop: digest tool request: %w", err)
			}
			requestEvidenceID, _, err := c.recordJSONEvidence(ctx, evidence.KindToolRequest, call, toolStepID, map[string]any{"round": result.Rounds, "tool": call.Function.Name})
			if err != nil {
				return result, err
			}
			step, replay, err := c.beginStep(ctx, toolStepID, "tool", inputDigest)
			if err != nil {
				return result, err
			}
			toolSteps[i] = step
			record := stepPayload(toolStepID, step.Attempt, inputDigest)
			record["tool"] = call.Function.Name
			record["call_id"] = call.ID
			if requestEvidenceID != "" {
				record["request_evidence_id"] = requestEvidenceID
			}
			callRecords[i] = record
			if replay {
				if step.OutputEvidenceID == "" {
					return result, fmt.Errorf("agentloop: completed tool step %s has no result evidence", toolStepID)
				}
				if err := c.loadJSONEvidence(ctx, step.OutputEvidenceID, &outcomes[i]); err != nil {
					return result, err
				}
				replayedTools[i] = true
				record["replayed"] = true
				record["output_evidence_id"] = step.OutputEvidenceID
				c.recordEventWithEvidence(ctx, runledger.EventToolReplayed, record, []string{step.OutputEvidenceID})
				continue
			}
			c.recordEvent(ctx, runledger.EventToolStarted, record)
			pendingCalls = append(pendingCalls, call)
			pendingIndexes = append(pendingIndexes, i)
		}

		c.recordEvent(ctx, runledger.EventToolRequested, map[string]any{
			"count":      len(msg.ToolCalls),
			"round":      result.Rounds,
			"call_steps": callRecords,
		})
		if len(pendingCalls) > 0 {
			if c.cfg.DispatchTools == nil {
				return result, fmt.Errorf("agentloop: model requested tools but no ToolDispatcher is configured")
			}
			pendingOutcomes, err := c.cfg.DispatchTools.Dispatch(ctx, pendingCalls)
			if err != nil {
				for _, index := range pendingIndexes {
					c.failStep(ctx, toolSteps[index], err)
					failed := callRecords[index]
					failed["error"] = err.Error()
					c.recordEvent(ctx, runledger.EventToolFailed, failed)
				}
				return result, err
			}
			for position, index := range pendingIndexes {
				if position < len(pendingOutcomes) {
					outcomes[index] = pendingOutcomes[position]
				}
				outputEvidenceID, outputDigest, evidenceErr := c.recordJSONEvidence(ctx, evidence.KindToolResult, outcomes[index], toolSteps[index].StepID, map[string]any{
					"round": result.Rounds,
					"tool":  msg.ToolCalls[index].Function.Name,
				})
				if evidenceErr != nil {
					c.failStep(ctx, toolSteps[index], evidenceErr)
					return result, evidenceErr
				}
				record := callRecords[index]
				record["effect_class"] = outcomes[index].EffectClass
				record["success"] = outcomes[index].Success
				if outputEvidenceID != "" {
					record["output_evidence_id"] = outputEvidenceID
				}
				if outcomes[index].Success {
					if err := c.completeStep(ctx, toolSteps[index], outputEvidenceID, outputDigest); err != nil {
						c.failStep(ctx, toolSteps[index], err)
						return result, err
					}
					c.recordEventWithEvidence(ctx, runledger.EventToolCompleted, record, evidenceIDs(outputEvidenceID))
				} else {
					c.failStep(ctx, toolSteps[index], fmt.Errorf("tool %s returned an unsuccessful result", msg.ToolCalls[index].Function.Name))
					c.recordEventWithEvidence(ctx, runledger.EventToolFailed, record, evidenceIDs(outputEvidenceID))
				}
			}
		}
		result.ToolCalls += len(msg.ToolCalls)

		var stopDecision Decision
		for i, call := range msg.ToolCalls {
			outcome := ToolOutcome{}
			if i < len(outcomes) {
				outcome = outcomes[i]
			}
			if c.cfg.ObserveToolOutcome != nil {
				if err := c.cfg.ObserveToolOutcome(ctx, call, outcome, replayedTools[i]); err != nil {
					return result, fmt.Errorf("agentloop: observe tool %s outcome: %w", call.Function.Name, err)
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
		if stopDecision.Stop {
			result.FinishReason = FinishReasonLoopGuard
			result.GuardDecision = stopDecision
			result.Content = GuardStopMessage(stopDecision.Reason)
			c.recordDecision(ctx, stopDecision.Kind, stopDecision.Reason)
			return result, nil
		}

		if stop := c.consultProgress(ctx, result, resp.Usage, contextWindow, started); stop {
			return result, nil
		}
	}
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
		Repetition: c.cfg.Governor.RepetitionPressure(),
	}
	state.EvidenceNovelty, state.EvidenceObserved = c.cfg.Governor.EvidenceNovelty()
	if contextWindow > 0 && usage.PromptTokens > 0 {
		state.Pressure = float64(usage.PromptTokens) / float64(contextWindow)
	}

	decision := c.cfg.Progress.Decide(state, FuseCounters{
		ModelRequests:  result.Rounds,
		ToolExecutions: result.ToolCalls,
		Elapsed:        time.Since(started),
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
	if c == nil || c.cfg.RunLedger == nil {
		return
	}
	_, _ = c.cfg.RunLedger.Append(ctx, runledger.Event{
		Type:        eventType,
		Timestamp:   time.Now(),
		SessionID:   c.cfg.SessionID,
		RunID:       c.cfg.RunID,
		TaskID:      c.cfg.TaskID,
		EvidenceIDs: evidenceIDs,
		Payload:     payload,
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
