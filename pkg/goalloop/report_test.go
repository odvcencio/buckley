package goalloop

import (
	"context"
	"math"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/taskstate"
)

// TestLoop_ReportRollsUpDurableState locks the morning report: a goal
// with one evidence-linked completion and one parked task with a
// deferred question rolls up into the 7.3 sections — completed items,
// the verification table, the park with its reason, batched questions,
// spend by node, and pending next actions — all from durable state.
func TestLoop_ReportRollsUpDurableState(t *testing.T) {
	t.Parallel()
	engine := &scriptedEngine{outcomes: []TurnOutcome{
		{
			Rounds: 1, SpentUSD: 0.40, Completed: true, CompletedEvidenceID: "ev_a",
			Summary: "ported the store tests",
			Checks: []taskstate.VerificationEntry{
				{Check: "unit tests", Scope: "pkg/storage", Status: taskstate.VerificationPass, Required: true, EvidenceID: "ev_a"},
			},
		},
		{
			Rounds: 1, SpentUSD: 0.20, Summary: "needs credentials",
			Questions:   []taskstate.Question{{Text: "Which DATABASE_URL should integration use?", BlockingTasks: []string{"integration"}}},
			NextActions: []taskstate.NextAction{{Text: "Wire DATABASE_URL and rerun", Kind: "verify"}},
			Blocker:     &taskstate.Blocker{Reason: "needs DATABASE_URL", Needs: "integration env"},
		},
	}}
	loop, _ := newTestLoop(t, Config{
		Engine: engine,
		Planner: staticPlanner{specs: []TaskSpec{
			{Title: "port store tests", Priority: 1},
			{Title: "integration run", Priority: 2},
		}},
	})
	ctx := context.Background()

	intake, err := loop.Start(ctx, Goal{Statement: "migrate storage tests", BudgetUSD: 12})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	for _, task := range intake.Tasks {
		if _, err := loop.RunTask(ctx, intake.RunID, task.TaskID, intake.Goal, task.Spec); err != nil {
			t.Fatalf("RunTask %s: %v", task.TaskID, err)
		}
	}

	report, err := loop.Report(ctx, intake.RunID)
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if report.Status != "partial" {
		t.Fatalf("status = %q, want partial (one completed, one blocked)", report.Status)
	}
	if report.Statement != "migrate storage tests" {
		t.Fatalf("statement = %q", report.Statement)
	}
	if report.BudgetUSD != 12 || math.Abs(report.SpentUSD-0.60) > 1e-9 {
		t.Fatalf("spend = %v / %.2f, want 0.60 / 12.00", report.SpentUSD, report.BudgetUSD)
	}
	if len(report.Completed) != 1 || report.Completed[0].EvidenceID != "ev_a" {
		t.Fatalf("completed = %+v", report.Completed)
	}
	if len(report.Parked) != 1 || report.Parked[0].Reason != "needs DATABASE_URL" {
		t.Fatalf("parked = %+v", report.Parked)
	}
	if len(report.Questions) != 1 || len(report.NextActions) != 1 {
		t.Fatalf("questions = %+v, next actions = %+v, want one each", report.Questions, report.NextActions)
	}

	rendered := RenderReport(report)
	for _, want := range []string{
		"type: buckley-goal-report",
		"status: partial",
		"spend_usd: 0.60 / 12.00",
		"# Goal\nmigrate storage tests",
		"# Completed (evidence-linked)",
		"(`ev_a`)",
		"| unit tests | pkg/storage | pass | `ev_a` |",
		"# Parked",
		"integration run blocked: needs DATABASE_URL — needs: integration env",
		"# Questions for you",
		"1. Which DATABASE_URL should integration use? (blocks integration)",
		"# Spend by node",
		"- port store tests $0.40",
		"- integration run $0.20",
		"# Next actions",
		"1. Wire DATABASE_URL and rerun",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("render missing %q:\n%s", want, rendered)
		}
	}
}
