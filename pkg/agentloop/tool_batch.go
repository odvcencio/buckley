package agentloop

import (
	"context"
	"errors"
	"fmt"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/runledger"
)

// executeToolBatch admits one effect at a time unless the request explicitly
// enables parallel tool calls. Each durable receipt reaches the governor before
// another call can start.
func (c *Controller) executeToolBatch(ctx context.Context, calls []model.ToolCall, round int, parallel bool, progress *progressTracker) (*toolRoundState, Decision, error) {
	state := newToolRoundState(calls)
	width := 1
	if parallel {
		width = len(calls)
	}
	var stop Decision
	var batchErr error
	for first := 0; first < len(calls); first += width {
		last := min(first+width, len(calls))
		part := &toolRoundState{
			firstIndex: first, batchSize: len(calls), priorStop: stop.Stop,
			calls: state.calls[first:last], steps: state.steps[first:last],
			outcomes: state.outcomes[first:last], known: state.known[first:last],
			replayed: state.replayed[first:last], records: state.records[first:last],
		}
		if batchErr == nil {
			batchErr = ctx.Err()
		}
		var dispatchErr error
		if stop.Stop || batchErr != nil {
			// Older releases may already have executed this suffix. Restore its
			// actual receipts without starting any new work after the stop.
			replayCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), partialDispatchJournalTimeout)
			dispatchErr = c.restoreStoppedToolRound(replayCtx, part, round)
			cancel()
		} else {
			dispatchErr = c.prepareToolRound(ctx, part, round)
			if dispatchErr == nil {
				dispatchErr = c.dispatchToolRound(ctx, part, round)
			}
			if dispatchErr != nil {
				c.appendUnresolvedToolMessages(part, "No confirmed result is available for this tool. Outcome unknown. Inspect state before retrying; no success or verification is established.")
			}
		}
		decision, observeErr := c.observeToolRound(ctx, part, progress)
		if decision.Stop && !stop.Stop {
			stop = decision
		}
		batchErr = errors.Join(batchErr, dispatchErr, observeErr)
	}
	return state, stop, batchErr
}

func (c *Controller) restoreStoppedToolRound(ctx context.Context, state *toolRoundState, round int) error {
	var restoreErrs []error
	for index, call := range state.calls {
		stepID := StableStepID(c.cfg.RunID, c.cfg.TaskID, c.cfg.TurnID, round, "tool", state.firstIndex+index)
		var step runledger.ExecutionStep
		err := runledger.ErrStepNotFound
		if c.cfg.StepJournal != nil {
			step, err = c.cfg.StepJournal.GetStep(ctx, c.cfg.RunID, stepID)
		}
		content := "Not executed: the harness stopped this tool batch before this call started. No result or verification is established."
		if err == nil && step.Status == runledger.StepCompleted {
			err = c.prepareToolStep(ctx, state, round, index)
			if err == nil {
				err = c.replayToolStep(ctx, state, index)
			}
			if err == nil {
				continue
			}
		} else if err == nil {
			err = runledger.RecoveryErrorForStep(step)
		}
		if !errors.Is(err, runledger.ErrStepNotFound) {
			restoreErrs = append(restoreErrs, fmt.Errorf("agentloop: restore stopped tool %s: %w", stepID, err))
			content = "No confirmed result is available for this tool. Outcome unknown. Inspect state before retrying; no success or verification is established."
		}
		if c.cfg.History != nil {
			c.cfg.History.Append(model.Message{Role: "tool", ToolCallID: call.ID, Name: call.Function.Name, Content: content})
		}
	}
	return errors.Join(restoreErrs...)
}
