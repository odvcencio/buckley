package goalloop

import (
	"context"
	"fmt"
	"time"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/runledger"
)

// Posture names (design 6.1). A posture is a named policy bundle, not a
// number: it decides the controller mode, which fuses are armed, and how
// aggressively the loop parks instead of burning budget.
const (
	PostureInteractive = "interactive"
	PostureFrugal      = "frugal"
	PostureOvernight   = "overnight"
)

// metric names the goal loop writes per turn (design 6.5). The ledger's
// samples are the spend source of truth; SumMetric(costUSDMetric) is what
// budget governance reads back.
const (
	costUSDMetric          = "cost_usd"
	promptTokensMetric     = "prompt_tokens"
	completionTokensMetric = "completion_tokens"
)

// overnightFuses are the section 20.8 emergency fuses: runaway
// protection far above any sane task budget, never a task budget.
var overnightFuses = agentloop.Fuses{
	ModelRequests:  500,
	ToolExecutions: 2000,
	WallTime:       6 * time.Hour,
}

// budgetFuseHeadroom sets the dollar emergency fuse above the goal's own
// ceiling, so the normal exhaustion path is the controller's
// park-not-burn rule (checkpoint and park with an explanation) and the
// fuse only fires if parking somehow failed to stop spend.
const budgetFuseHeadroom = 1.2

// progressFor returns the controller a drive runs under: the explicitly
// configured one when the caller supplied it, otherwise the goal's
// posture expanded into a policy bundle:
//
//   - interactive (and any unknown posture): dynamic mode with no
//     emergency fuses. The user is present to interrupt, so no runaway
//     fuses — but a budget the user set on the goal is still enforced
//     ("user budgets only", design 6.1).
//   - frugal: dynamic with the standard fuses armed and earlier parking.
//   - overnight: dynamic, fuses armed including the dollar fuse above
//     the goal ceiling, and the most aggressive park-not-burn.
func (l *Loop) progressFor(goal Goal) *agentloop.ProgressController {
	if l.progress != nil {
		return l.progress
	}
	switch goal.Posture {
	case PostureFrugal:
		return &agentloop.ProgressController{
			Mode:       agentloop.ModeDynamic,
			Fuses:      fusesFor(goal),
			Thresholds: agentloop.ProgressThresholds{ParkUncertainty: 0.6},
		}
	case PostureOvernight:
		return &agentloop.ProgressController{
			Mode:       agentloop.ModeDynamic,
			Fuses:      fusesFor(goal),
			Thresholds: agentloop.ProgressThresholds{ParkUncertainty: 0.5},
		}
	default:
		return &agentloop.ProgressController{Mode: agentloop.ModeDynamic}
	}
}

func fusesFor(goal Goal) agentloop.Fuses {
	fuses := overnightFuses
	if goal.BudgetUSD > 0 {
		fuses.BudgetUSD = goal.BudgetUSD * budgetFuseHeadroom
	}
	return fuses
}

// recordTurnSpend writes one turn's spend telemetry (design 6.5): metric
// samples with task dimensions, plus the budget.updated event stream
// when the goal carries a budget, and budget.warning/exhausted at the
// 80% and 100% marks. It returns the goal's cumulative spend after this
// turn. Metric writes are the budget's source of truth, so a write
// failure is an error, not best-effort.
func (l *Loop) recordTurnSpend(ctx context.Context, runID, taskID string, goal Goal, outcome TurnOutcome, idempotencyPrefix string) (float64, error) {
	samples := []runledger.AgentMetricSample{
		{RunID: runID, TaskID: taskID, IdempotencyKey: idempotencyPrefix + ":" + costUSDMetric, MetricName: costUSDMetric, Value: outcome.SpentUSD, Unit: "usd"},
		{RunID: runID, TaskID: taskID, IdempotencyKey: idempotencyPrefix + ":" + promptTokensMetric, MetricName: promptTokensMetric, Value: float64(outcome.PromptTokens), Unit: "tokens"},
		{RunID: runID, TaskID: taskID, IdempotencyKey: idempotencyPrefix + ":" + completionTokensMetric, MetricName: completionTokensMetric, Value: float64(outcome.CompletionTokens), Unit: "tokens"},
	}
	for _, sample := range samples {
		if sample.Value == 0 {
			continue
		}
		if _, err := l.ledger.RecordMetricSample(ctx, sample); err != nil {
			return 0, fmt.Errorf("goalloop: record %s: %w", sample.MetricName, err)
		}
	}

	spent, err := l.ledger.SumMetric(ctx, runID, costUSDMetric)
	if err != nil {
		return 0, fmt.Errorf("goalloop: read spend: %w", err)
	}
	if goal.BudgetUSD <= 0 {
		return spent, nil
	}

	eventType := runledger.EventBudgetUpdated
	switch {
	case spent >= goal.BudgetUSD:
		eventType = runledger.EventBudgetExhausted
	case spent >= goal.BudgetUSD*0.8:
		eventType = runledger.EventBudgetWarning
	}
	_, _ = l.ledger.Append(ctx, runledger.Event{
		ID:        runledger.StableEventID("goal-budget-turn", idempotencyPrefix),
		Type:      eventType,
		Timestamp: time.Now().UTC(),
		SessionID: l.sessionID,
		RunID:     runID,
		TaskID:    taskID,
		Payload: map[string]any{
			"spent_usd":  spent,
			"budget_usd": goal.BudgetUSD,
			"remaining":  goal.BudgetUSD - spent,
		},
	})
	return spent, nil
}
