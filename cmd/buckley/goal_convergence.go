package main

import (
	"context"
	"fmt"
	"strings"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/goalloop"
	"m31labs.dev/buckley/pkg/rules"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/taskstate"
)

type goalConvergenceDecision struct {
	Action     string
	ReasonCode string
	Reason     string
}

func evaluateGoalConvergence(engine *rules.Engine, result *agentloop.Result, state *goalTurnState) (goalConvergenceDecision, error) {
	if engine == nil {
		return goalConvergenceDecision{}, fmt.Errorf("goal convergence policy engine is unavailable")
	}
	terminationKind := ""
	if result != nil {
		terminationKind = strings.TrimSpace(result.Termination.Kind)
	}
	stateChanged := state != nil && state.stateChanged
	blockerPresent := state != nil && state.blocker != nil
	decision, err := engine.EvalStrategy("runtime/goal_convergence", "goal_convergence", map[string]any{
		"termination": map[string]any{"kind": terminationKind},
		"workspace":   map[string]any{"state_changed": stateChanged},
		"turn":        map[string]any{"blocker_present": blockerPresent},
	})
	if err != nil {
		return goalConvergenceDecision{}, err
	}
	out := goalConvergenceDecision{
		Action:     strings.TrimSpace(stringParam(decision.Params, "action")),
		ReasonCode: strings.TrimSpace(stringParam(decision.Params, "reason_code")),
		Reason:     strings.TrimSpace(stringParam(decision.Params, "reason")),
	}
	if (out.Action != "continue" && out.Action != "park") || out.ReasonCode == "" || out.Reason == "" {
		return goalConvergenceDecision{}, fmt.Errorf("goal convergence policy returned an invalid decision")
	}
	return out, nil
}

func (e *goalTurnEngine) applyGoalConvergencePolicy(ctx context.Context, task goalloop.TaskContext, result *agentloop.Result, state *goalTurnState) error {
	decision, err := evaluateGoalConvergence(e.policyEngine, result, state)
	if err != nil {
		return fmt.Errorf("goal convergence policy unavailable: %w", err)
	}
	terminationKind := ""
	stateChanged := false
	if result != nil {
		terminationKind = strings.TrimSpace(result.Termination.Kind)
	}
	if state != nil {
		stateChanged = state.stateChanged
	}
	eventID := runledger.StableEventID("goal-convergence-policy", task.RunID, task.TaskID, task.TurnID, terminationKind, decision.Action, decision.ReasonCode)
	if _, err := e.ledger.Append(ctx, runledger.Event{
		ID:     eventID,
		Type:   runledger.EventControllerDecision,
		RunID:  task.RunID,
		TaskID: task.TaskID,
		Payload: map[string]any{
			"kind":             "goal_convergence_policy",
			"turn_id":          task.TurnID,
			"termination_kind": terminationKind,
			"state_changed":    stateChanged,
			"action":           decision.Action,
			"reason_code":      decision.ReasonCode,
			"reason":           decision.Reason,
		},
	}); err != nil {
		return fmt.Errorf("goal convergence policy audit: %w", err)
	}
	if decision.Action == "park" && state != nil && state.blocker == nil {
		state.blocker = &taskstate.Blocker{
			Reason: decision.Reason,
			Needs:  "a materially different strategy or new external evidence",
		}
	}
	return nil
}
