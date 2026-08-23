package rules

import "testing"

func TestGoalConvergencePolicy(t *testing.T) {
	tests := []struct {
		name           string
		termination    string
		stateChanged   bool
		blockerPresent bool
		wantAction     string
		wantReason     string
	}{
		{name: "exact repeat parks", termination: "exact_repeat", wantAction: "park", wantReason: "exact_repeat_without_state_change"},
		{name: "outcome repeat parks", termination: "outcome_repeat", wantAction: "park", wantReason: "outcome_repeat_without_state_change"},
		{name: "action cycle parks", termination: "action_cycle", wantAction: "park", wantReason: "action_cycle_without_state_change"},
		{name: "read-only budget parks", termination: "read_only_budget", wantAction: "park", wantReason: "read_only_budget_without_state_change"},
		{name: "round limit parks even after state change", termination: "round_limit", stateChanged: true, wantAction: "park", wantReason: "round_limit_terminal"},
		{name: "round limit parks without state change", termination: "round_limit", wantAction: "park", wantReason: "round_limit_terminal"},
		{name: "tool call limit parks even after state change", termination: "tool_call_limit", stateChanged: true, wantAction: "park", wantReason: "tool_call_limit_terminal"},
		{name: "tool call limit parks without state change", termination: "tool_call_limit", wantAction: "park", wantReason: "tool_call_limit_terminal"},
		{name: "state change prevents automatic park", termination: "exact_repeat", stateChanged: true, wantAction: "continue", wantReason: "no_stalled_turn"},
		{name: "state-changing outcome repeat continues", termination: "outcome_repeat", stateChanged: true, wantAction: "continue", wantReason: "no_stalled_turn"},
		{name: "state-changing action cycle continues", termination: "action_cycle", stateChanged: true, wantAction: "continue", wantReason: "no_stalled_turn"},
		{name: "explicit blocker wins", termination: "exact_repeat", blockerPresent: true, wantAction: "continue", wantReason: "explicit_blocker_present"},
		{name: "explicit blocker wins over round limit", termination: "round_limit", stateChanged: true, blockerPresent: true, wantAction: "continue", wantReason: "explicit_blocker_present"},
		{name: "ordinary completion continues", wantAction: "continue", wantReason: "no_stalled_turn"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := mustNewTestEngine(t).EvalStrategy("runtime/goal_convergence", "goal_convergence", map[string]any{
				"termination": map[string]any{"kind": tt.termination},
				"workspace":   map[string]any{"state_changed": tt.stateChanged},
				"turn":        map[string]any{"blocker_present": tt.blockerPresent},
			})
			if err != nil {
				t.Fatalf("EvalStrategy: %v", err)
			}
			if got := result.Params["action"]; got != tt.wantAction {
				t.Errorf("action = %#v, want %q", got, tt.wantAction)
			}
			if got := result.Params["reason_code"]; got != tt.wantReason {
				t.Errorf("reason_code = %#v, want %q", got, tt.wantReason)
			}
		})
	}
}

func TestGoalConvergencePolicyCatalog(t *testing.T) {
	contracts := FactContractsForDomain("runtime/goal_convergence")
	if len(contracts) != 1 {
		t.Fatalf("got %d contracts, want 1", len(contracts))
	}
	for _, key := range []string{"termination.kind", "turn.blocker_present", "workspace.state_changed"} {
		assertFact(t, contracts[0], key)
	}
}
