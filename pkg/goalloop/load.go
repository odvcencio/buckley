package goalloop

import (
	"context"
	"fmt"
	"time"

	"m31labs.dev/buckley/pkg/runledger"
)

// LoadGoal reconstructs a goal and its task specs from the run's events —
// the inverse of Start, and the entry point for driving a previously
// recorded goal (buckley goal run) or resuming after a restart. It reads
// only task.created payloads, so it works on any machine that has the
// ledger, with no other state.
func (l *Loop) LoadGoal(ctx context.Context, runID string) (Goal, map[string]TaskSpec, error) {
	events, err := l.ledger.ListEvents(ctx, runledger.EventQuery{RunID: runID})
	if err != nil {
		return Goal{}, nil, fmt.Errorf("goalloop: list events: %w", err)
	}

	goal := Goal{}
	specs := map[string]TaskSpec{}
	sawGoal := false
	for _, ev := range events {
		if ev.Type != runledger.EventTaskCreated {
			continue
		}
		switch payloadString(ev.Payload, "kind") {
		case "goal":
			sawGoal = true
			goal.Statement = payloadString(ev.Payload, "statement")
			goal.AcceptanceCriteria = payloadStrings(ev.Payload, "acceptance_criteria")
			goal.Constraints = payloadStrings(ev.Payload, "constraints")
			goal.Posture = payloadString(ev.Payload, "posture")
			goal.ApprovalMode = payloadString(ev.Payload, "approval_mode")
		case "task":
			specs[ev.TaskID] = TaskSpec{
				Title:              payloadString(ev.Payload, "title"),
				Description:        payloadString(ev.Payload, "description"),
				AcceptanceCriteria: payloadStrings(ev.Payload, "acceptance_criteria"),
				Priority:           payloadInt(ev.Payload, "priority"),
				Claims:             payloadStrings(ev.Payload, "claims"),
			}
		}
	}
	if !sawGoal {
		return Goal{}, nil, fmt.Errorf("goalloop: run %s has no goal record", runID)
	}

	if run, err := l.ledger.GetRun(ctx, runID); err == nil && run.Budget != nil {
		switch v := run.Budget["usd"].(type) {
		case float64:
			goal.BudgetUSD = v
		case int:
			goal.BudgetUSD = float64(v)
		}
		if deadline := payloadString(run.Budget, "deadline"); deadline != "" {
			if t, err := time.Parse(time.RFC3339, deadline); err == nil {
				goal.Deadline = t
			}
		}
	}
	return goal, specs, nil
}

func payloadStrings(payload map[string]any, key string) []string {
	if payload == nil {
		return nil
	}
	switch v := payload[key].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
