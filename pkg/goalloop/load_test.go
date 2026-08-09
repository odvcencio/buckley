package goalloop

import (
	"context"
	"testing"
)

// TestLoop_LoadGoalRoundTrip locks LoadGoal as Start's inverse: a
// recorded goal reconstructs its statement, criteria, posture, budget,
// and every task spec purely from the ledger.
func TestLoop_LoadGoalRoundTrip(t *testing.T) {
	t.Parallel()
	loop, _ := newTestLoop(t, Config{
		Planner: staticPlanner{specs: []TaskSpec{
			{Title: "task one", Description: "first", AcceptanceCriteria: []string{"a1"}, Priority: 1, Claims: []string{"pkg/one", "docs/one.md"}},
			{Title: "task two", Priority: 2},
		}},
	})
	ctx := context.Background()

	original := Goal{
		Statement:          "migrate the tests",
		AcceptanceCriteria: []string{"suite green"},
		Constraints:        []string{"no new deps"},
		BudgetUSD:          9.5,
		Posture:            PostureOvernight,
		ApprovalMode:       "safe",
	}
	intake, err := loop.Start(ctx, original)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	goal, specs, err := loop.LoadGoal(ctx, intake.RunID)
	if err != nil {
		t.Fatalf("LoadGoal: %v", err)
	}
	if goal.Statement != original.Statement || goal.Posture != original.Posture || goal.ApprovalMode != original.ApprovalMode {
		t.Fatalf("goal = %+v, want %+v", goal, original)
	}
	if goal.BudgetUSD != 9.5 {
		t.Fatalf("budget = %v, want 9.5", goal.BudgetUSD)
	}
	if len(goal.AcceptanceCriteria) != 1 || goal.AcceptanceCriteria[0] != "suite green" {
		t.Fatalf("criteria = %+v", goal.AcceptanceCriteria)
	}
	if len(goal.Constraints) != 1 || goal.Constraints[0] != "no new deps" {
		t.Fatalf("constraints = %+v", goal.Constraints)
	}
	if len(specs) != 2 {
		t.Fatalf("specs = %+v, want 2", specs)
	}
	one := specs[intake.Tasks[0].TaskID]
	if one.Title != "task one" || one.Description != "first" || one.Priority != 1 || len(one.AcceptanceCriteria) != 1 {
		t.Fatalf("spec one = %+v", one)
	}
	if len(one.Claims) != 2 || one.Claims[0] != "pkg/one" || one.Claims[1] != "docs/one.md" {
		t.Fatalf("spec one claims = %+v, want workspace claims round-tripped", one.Claims)
	}

	if _, _, err := loop.LoadGoal(ctx, "run_missing"); err == nil {
		t.Fatal("LoadGoal on an unknown run did not fail")
	}
}
