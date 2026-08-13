package goalloop

import (
	"context"
	"fmt"
	"testing"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/taskstate"
)

// TestProgressFor_Postures locks the posture policy bundles: interactive
// enforces budgets but arms no fuses, frugal and overnight arm fuses
// with progressively earlier parking, and an explicit controller wins.
func TestProgressFor_Postures(t *testing.T) {
	t.Parallel()
	loop := &Loop{}

	interactive := loop.progressFor(Goal{Posture: PostureInteractive, BudgetUSD: 5})
	if interactive.Mode != agentloop.ModeDynamic || interactive.Fuses.ModelRequests != 0 {
		t.Fatalf("interactive = %+v, want dynamic with no fuses", interactive)
	}

	frugal := loop.progressFor(Goal{Posture: PostureFrugal, BudgetUSD: 5})
	if frugal.Fuses.ModelRequests != 500 || frugal.Thresholds.ParkUncertainty != 0.6 {
		t.Fatalf("frugal = %+v, want armed fuses and 0.6 parking", frugal)
	}

	overnight := loop.progressFor(Goal{Posture: PostureOvernight, BudgetUSD: 5})
	if overnight.Fuses.BudgetUSD != 6 {
		t.Fatalf("overnight dollar fuse = %.2f, want 6.00 (1.2x the ceiling)", overnight.Fuses.BudgetUSD)
	}
	if overnight.Thresholds.ParkUncertainty != 0.5 {
		t.Fatalf("overnight parking = %.2f, want 0.5", overnight.Thresholds.ParkUncertainty)
	}

	explicit := &agentloop.ProgressController{Mode: agentloop.ModeShadow}
	loop.progress = explicit
	if got := loop.progressFor(Goal{Posture: PostureOvernight}); got != explicit {
		t.Fatal("explicit controller did not win over the posture")
	}
}

// TestLoop_SpendTelemetryIsCumulativeAcrossTasks locks G8's core rule:
// budget decisions run on the goal's cumulative ledger spend, not the
// current drive's local total. Task A spends half the budget; task B's
// first turn exhausts it and parks immediately — which can only happen
// if B reads A's spend back from the metric samples.
func TestLoop_SpendTelemetryIsCumulativeAcrossTasks(t *testing.T) {
	t.Parallel()
	engine := &scriptedEngine{outcomes: []TurnOutcome{
		{Rounds: 1, SpentUSD: 0.50, PromptTokens: 100, CompletionTokens: 20, Completed: true, CompletedEvidenceID: "ev_a", Summary: "task a done"},
		{Rounds: 1, SpentUSD: 0.50, PromptTokens: 100, CompletionTokens: 20, StateChanged: true, Summary: "task b working"},
	}}
	loop, ledger := newTestLoop(t, Config{
		Engine: engine,
		Planner: staticPlanner{specs: []TaskSpec{
			{Title: "task a", Priority: 1},
			{Title: "task b", Priority: 2},
		}},
	})
	ctx := context.Background()

	intake, err := loop.Start(ctx, Goal{Statement: "budgeted work", BudgetUSD: 1.00, Posture: PostureOvernight})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	first, err := loop.RunTask(ctx, intake.RunID, intake.Tasks[0].TaskID, intake.Goal, intake.Tasks[0].Spec)
	if err != nil {
		t.Fatalf("RunTask a: %v", err)
	}
	if first.Status != taskstate.StatusCompleted || first.Turns != 1 {
		t.Fatalf("task a = %+v, want completed in one turn", first)
	}

	second, err := loop.RunTask(ctx, intake.RunID, intake.Tasks[1].TaskID, intake.Goal, intake.Tasks[1].Spec)
	if err != nil {
		t.Fatalf("RunTask b: %v", err)
	}
	if second.Status != taskstate.StatusParked || second.Turns != 1 {
		t.Fatalf("task b = %+v, want parked on its first turn (cumulative spend hit the ceiling)", second)
	}

	spent, err := ledger.SumMetric(ctx, intake.RunID, costUSDMetric)
	if err != nil {
		t.Fatalf("SumMetric: %v", err)
	}
	if spent != 1.00 {
		t.Fatalf("cumulative spend = %.2f, want 1.00", spent)
	}
	prompt, _ := ledger.SumMetric(ctx, intake.RunID, promptTokensMetric)
	if prompt != 200 {
		t.Fatalf("prompt tokens = %.0f, want 200", prompt)
	}

	events, err := ledger.ListEvents(ctx, runledger.EventQuery{RunID: intake.RunID})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var sawUpdated, sawExhausted bool
	for _, ev := range events {
		switch ev.Type {
		case runledger.EventBudgetUpdated:
			sawUpdated = true
		case runledger.EventBudgetExhausted:
			sawExhausted = true
			if remaining, _ := ev.Payload["remaining"].(float64); remaining != 0 {
				t.Fatalf("exhausted payload = %+v, want remaining 0", ev.Payload)
			}
		}
	}
	if !sawUpdated || !sawExhausted {
		t.Fatalf("budget events: updated=%v exhausted=%v, want both", sawUpdated, sawExhausted)
	}
}

func TestLoop_RecordTurnSpendRetryIsIdempotent(t *testing.T) {
	loop, ledger := newTestLoop(t, Config{})
	ctx := context.Background()
	intake, err := loop.Start(ctx, Goal{Statement: "retry a durable turn", BudgetUSD: 2})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	taskID := intake.Tasks[0].TaskID
	outcome := TurnOutcome{SpentUSD: 0.75, PromptTokens: 100, CompletionTokens: 25}
	key := fmt.Sprintf("turn:%s:%s:%d:%d", intake.RunID, taskID, 1, 2)

	for attempt := 0; attempt < 2; attempt++ {
		spent, err := loop.recordTurnSpend(ctx, intake.RunID, taskID, intake.Goal, outcome, key)
		if err != nil {
			t.Fatalf("recordTurnSpend attempt %d: %v", attempt+1, err)
		}
		if spent != 0.75 {
			t.Fatalf("attempt %d spend = %.2f, want 0.75", attempt+1, spent)
		}
	}

	events, err := ledger.ListEvents(ctx, runledger.EventQuery{RunID: intake.RunID, Types: []string{runledger.EventBudgetUpdated}})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("budget events = %d, want 1", len(events))
	}
	prompt, err := ledger.SumMetric(ctx, intake.RunID, promptTokensMetric)
	if err != nil || prompt != 100 {
		t.Fatalf("prompt tokens = %.0f, %v; want 100", prompt, err)
	}
}
