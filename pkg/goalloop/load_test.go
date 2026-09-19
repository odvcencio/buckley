package goalloop

import (
	"context"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/runledger"
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
		ModelRequest: GoalModelRequest{
			PolicyVersion:            GoalModelPolicyVersionV1,
			Policy:                   "strict_zdr",
			PolicyAction:             "allow",
			PolicyReasonCode:         "zdr_enforced",
			Model:                    "stealth/ox-alpha",
			ReasoningEffort:          "max",
			RetentionMode:            GoalRetentionZDR,
			OpenRouterZDR:            true,
			OpenRouterDataCollection: "deny",
		},
		Posture:       PostureOvernight,
		ApprovalMode:  "safe",
		WorkspaceRoot: t.TempDir(),
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
	if goal.WorkspaceRoot != original.WorkspaceRoot {
		t.Fatalf("workspace root = %q, want %q", goal.WorkspaceRoot, original.WorkspaceRoot)
	}
	if goal.ModelRequest != original.ModelRequest {
		t.Fatalf("model request = %+v, want %+v", goal.ModelRequest, original.ModelRequest)
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

func TestGoalModelRequestValidate(t *testing.T) {
	t.Parallel()
	valid := Goal{
		Statement:     "probe",
		WorkspaceRoot: t.TempDir(),
		ModelRequest: GoalModelRequest{
			PolicyVersion:            GoalModelPolicyVersionV1,
			Policy:                   "strict_zdr",
			PolicyAction:             "allow",
			PolicyReasonCode:         "zdr_enforced",
			Model:                    "stealth/ox-alpha",
			ReasoningEffort:          "max",
			RetentionMode:            GoalRetentionZDR,
			OpenRouterZDR:            true,
			OpenRouterDataCollection: "deny",
		},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid contract rejected: %v", err)
	}
	rootless := valid
	rootless.WorkspaceRoot = ""
	if err := rootless.Validate(); err == nil || !strings.Contains(err.Error(), "workspace root") {
		t.Fatalf("rootless v1 Validate error = %v", err)
	}

	tests := []struct {
		name    string
		request GoalModelRequest
	}{
		{name: "privacy without exact model", request: GoalModelRequest{OpenRouterZDR: true}},
		{name: "unknown reasoning", request: GoalModelRequest{Model: "stealth/ox-alpha", ReasoningEffort: "ultra"}},
		{name: "noncanonical reasoning", request: GoalModelRequest{Model: "stealth/ox-alpha", ReasoningEffort: "MAX"}},
		{name: "weakening collection policy", request: GoalModelRequest{Model: "stealth/ox-alpha", OpenRouterDataCollection: "allow"}},
		{name: "model control", request: GoalModelRequest{Model: "stealth/ox-alpha\nother"}},
		{name: "mode without version", request: GoalModelRequest{Model: "stealth/ox-alpha", RetentionMode: GoalRetentionZDR, OpenRouterZDR: true}},
		{name: "unqualified OpenRouter model", request: GoalModelRequest{PolicyVersion: GoalModelPolicyVersionV1, Policy: "strict_zdr", PolicyAction: "allow", PolicyReasonCode: "zdr_enforced", Model: "ox-alpha", RetentionMode: GoalRetentionZDR, OpenRouterZDR: true}},
		{name: "non zdr missing data deny", request: GoalModelRequest{Model: "stealth/ox-alpha", RetentionMode: GoalRetentionNonZDR}},
		{name: "v1 missing decision", request: GoalModelRequest{PolicyVersion: GoalModelPolicyVersionV1, Model: "stealth/ox-alpha", RetentionMode: GoalRetentionZDR, OpenRouterZDR: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			goal := Goal{Statement: "probe", ModelRequest: tt.request}
			if err := goal.Validate(); err == nil {
				t.Fatalf("Validate(%+v) error = nil", tt.request)
			}
		})
	}
}

func TestLoop_LoadGoalRejectsMalformedPolicyTypesWithoutWeakening(t *testing.T) {
	for _, tt := range []struct {
		name  string
		key   string
		value any
	}{
		{name: "zdr string", key: "openrouter_zdr", value: "false"},
		{name: "retention number", key: "retention_mode", value: 1},
		{name: "license digest boolean", key: "workspace_license_sha256", value: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			loop, ledger := newTestLoop(t, Config{})
			run, err := ledger.StartRun(context.Background(), runledger.AgentRun{SessionID: "sess-goal"})
			if err != nil {
				t.Fatalf("StartRun: %v", err)
			}
			payload := map[string]any{
				"kind":                     "goal",
				"statement":                "malformed policy",
				"model_policy_version":     GoalModelPolicyVersionV1,
				"model_policy":             "strict_zdr",
				"model_policy_action":      "allow",
				"model_policy_reason_code": "zdr_enforced",
				"model":                    "stealth/ox-alpha",
				"retention_mode":           GoalRetentionZDR,
				"openrouter_zdr":           true,
				"workspace_root":           t.TempDir(),
			}
			payload[tt.key] = tt.value
			if _, err := ledger.Append(context.Background(), runledger.Event{
				RunID: run.RunID, TaskID: "goal-1", Type: runledger.EventTaskCreated, Payload: payload,
			}); err != nil {
				t.Fatalf("Append: %v", err)
			}
			if _, _, err := loop.LoadGoal(context.Background(), run.RunID); err == nil {
				t.Fatal("LoadGoal accepted malformed policy field")
			}
		})
	}
}

func TestLoop_LoadGoalRejectsMissingOrMalformedV1WorkspaceRoot(t *testing.T) {
	canonicalRoot := t.TempDir()
	for _, tt := range []struct {
		name      string
		setRoot   bool
		rootValue any
	}{
		{name: "missing"},
		{name: "empty", setRoot: true, rootValue: ""},
		{name: "wrong type", setRoot: true, rootValue: 42},
		{name: "noncanonical", setRoot: true, rootValue: canonicalRoot + "/."},
	} {
		t.Run(tt.name, func(t *testing.T) {
			loop, ledger := newTestLoop(t, Config{})
			run, err := ledger.StartRun(context.Background(), runledger.AgentRun{SessionID: "sess-goal"})
			if err != nil {
				t.Fatalf("StartRun: %v", err)
			}
			payload := map[string]any{
				"kind": "goal", "statement": "malformed workspace",
				"model_policy_version": GoalModelPolicyVersionV1,
				"model_policy":         "strict_zdr", "model_policy_action": "allow", "model_policy_reason_code": "zdr_enforced",
				"model": "stealth/ox-alpha", "retention_mode": GoalRetentionZDR, "openrouter_zdr": true,
			}
			if tt.setRoot {
				payload["workspace_root"] = tt.rootValue
			}
			if _, err := ledger.Append(context.Background(), runledger.Event{
				RunID: run.RunID, TaskID: "goal-1", Type: runledger.EventTaskCreated, Payload: payload,
			}); err != nil {
				t.Fatalf("Append: %v", err)
			}
			if _, _, err := loop.LoadGoal(context.Background(), run.RunID); err == nil || !strings.Contains(err.Error(), "workspace") {
				t.Fatalf("LoadGoal error = %v", err)
			}
		})
	}
}
