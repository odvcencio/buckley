package agentloop

import (
	"strings"
	"testing"
)

func TestGovernorWarnsThenStopsExactRepeat(t *testing.T) {
	governor := New(DefaultConfig())

	first := governor.Observe("read_file", `{"path":"a.go"}`, "same result", true)
	if first.Stop || first.Nudge != "" {
		t.Fatalf("first observation = %+v, want no intervention", first)
	}
	second := governor.Observe("read_file", `{ "path" : "a.go" }`, "same   result", true)
	if second.Stop || !strings.Contains(second.Nudge, "exact action") {
		t.Fatalf("second observation = %+v, want warning", second)
	}
	third := governor.Observe("read_file", `{"path":"a.go"}`, "same result", true)
	if !third.Stop || third.Kind != "exact_repeat" {
		t.Fatalf("third observation = %+v, want exact-repeat stop", third)
	}
}

func TestGovernorDetectsShortActionEvidenceCycle(t *testing.T) {
	config := DefaultConfig()
	config.ExactRepeatLimit = 20
	config.OutcomeRepeatLimit = 20
	governor := New(config)

	actions := []struct {
		name   string
		args   string
		result string
	}{
		{"read_file", `{"path":"a.go"}`, "a unchanged"},
		{"search_text", `{"pattern":"missing"}`, "no matches"},
		{"read_file", `{"path":"a.go"}`, "a unchanged"},
		{"search_text", `{"pattern":"missing"}`, "no matches"},
		{"read_file", `{"path":"a.go"}`, "a unchanged"},
		{"search_text", `{"pattern":"missing"}`, "no matches"},
	}
	var decision Decision
	for _, action := range actions {
		decision = governor.Observe(action.name, action.args, action.result, true)
	}
	if !decision.Stop || decision.Kind != "action_cycle" {
		t.Fatalf("decision = %+v, want action-cycle stop", decision)
	}
}

func TestGovernorAllowsRepeatedPollingWhenEvidenceChanges(t *testing.T) {
	config := DefaultConfig()
	config.ExactRepeatLimit = 20
	config.OutcomeRepeatLimit = 20
	governor := New(config)

	for _, result := range []string{"queued", "starting", "running 10%", "running 50%", "running 90%", "complete"} {
		decision := governor.Observe("job_status", `{"id":"build-1"}`, result, true)
		if decision.Stop {
			t.Fatalf("progressing poll %q stopped: %+v", result, decision)
		}
	}
}

func TestGovernorDetectsRepeatedOutcomeWithChangingArguments(t *testing.T) {
	config := DefaultConfig()
	config.ExactRepeatLimit = 20
	config.CycleRepeats = 10
	config.OutcomeRepeatLimit = 4
	governor := New(config)

	var decision Decision
	for _, path := range []string{"a.go", "b.go", "c.go", "d.go"} {
		decision = governor.Observe("read_file", `{"path":"`+path+`"}`, "not found", false)
	}
	if !decision.Stop || decision.Kind != "outcome_repeat" {
		t.Fatalf("decision = %+v, want outcome-repeat stop", decision)
	}
}

func TestGovernorAllowsSuccessfulEmptySearchesWithChangingArguments(t *testing.T) {
	config := DefaultConfig()
	config.ExactRepeatLimit = 20
	config.CycleRepeats = 10
	config.OutcomeRepeatLimit = 4
	governor := New(config)

	for _, pattern := range []string{"MeshVertices", "drawElements", "drawIndexed", "setIndexBuffer", "retainedGeometry"} {
		decision := governor.Observe("search_text", `{"pattern":"`+pattern+`"}`, "no matches", true)
		if decision.Stop || decision.Nudge != "" {
			t.Fatalf("successful search %q = %+v, want no intervention", pattern, decision)
		}
	}
}

func TestGovernorEnforcesRoundAndToolLimits(t *testing.T) {
	governor := New(Config{
		MaxRounds:          2,
		MaxToolCalls:       2,
		ExactRepeatLimit:   10,
		OutcomeRepeatLimit: 10,
		CycleMaxLength:     2,
		CycleRepeats:       10,
	})

	if decision := governor.BeginRound(); decision.Stop {
		t.Fatalf("first round stopped: %+v", decision)
	}
	if decision := governor.BeginRound(); decision.Stop {
		t.Fatalf("second round stopped: %+v", decision)
	}
	if decision := governor.BeginRound(); !decision.Stop || decision.Kind != "round_limit" {
		t.Fatalf("third round = %+v, want round-limit stop", decision)
	}

	if decision := governor.Observe("one", `{}`, "one", true); decision.Stop {
		t.Fatalf("first tool stopped: %+v", decision)
	}
	if decision := governor.Observe("two", `{}`, "two", true); !decision.Stop || decision.Kind != "tool_call_limit" {
		t.Fatalf("second tool = %+v, want tool-call-limit stop", decision)
	}
}

func TestGovernorCanonicalizesJSONEvidence(t *testing.T) {
	governor := New(DefaultConfig())
	_ = governor.Observe("tool", `{"b":2,"a":1}`, `{"ok":true,"items":[1,2]}`, true)
	decision := governor.Observe("tool", `{"a":1,"b":2}`, `{ "items" : [1,2], "ok" : true }`, true)
	if decision.Nudge == "" {
		t.Fatalf("decision = %+v, want canonical repeat warning", decision)
	}
}

func TestGovernorBoundsReadOnlyDiscoveryAndResetsAfterChange(t *testing.T) {
	config := DefaultConfig()
	config.ReadOnlyWarningAt = 2
	config.MaxReadOnlyCalls = 4
	governor := New(config)

	if got := governor.ObserveEffect("readonly", true); got.Stop || got.Nudge != "" {
		t.Fatalf("first read = %+v, want no intervention", got)
	}
	if got := governor.ObserveEffect("readonly", true); got.Stop || got.Kind != "read_only_budget_warning" {
		t.Fatalf("second read = %+v, want warning", got)
	}
	if got := governor.ObserveEffect("modifying", true); got.Stop || got.Nudge != "" {
		t.Fatalf("modifying action = %+v, want reset", got)
	}
	for index := 1; index <= 4; index++ {
		got := governor.ObserveEffect("readonly", true)
		if index < 4 && got.Stop {
			t.Fatalf("read %d stopped early: %+v", index, got)
		}
		if index == 4 && (!got.Stop || got.Kind != "read_only_budget") {
			t.Fatalf("read %d = %+v, want read-only budget stop", index, got)
		}
	}
}

func TestGovernorFailedModificationDoesNotResetReadOnlyBudget(t *testing.T) {
	config := DefaultConfig()
	config.ReadOnlyWarningAt = 2
	config.MaxReadOnlyCalls = 3
	governor := New(config)

	_ = governor.ObserveEffect("readonly", true)
	if got := governor.ObserveEffect("modifying", false); got.Stop || got.Kind != "read_only_budget_warning" {
		t.Fatalf("failed modification = %+v, want no-progress warning", got)
	}
	if got := governor.ObserveEffect("readonly", true); !got.Stop || got.Kind != "read_only_budget" {
		t.Fatalf("next read = %+v, want read-only budget stop", got)
	}
}

func TestGovernorObservedNoOpModificationDoesNotResetReadOnlyBudget(t *testing.T) {
	config := DefaultConfig()
	config.ReadOnlyWarningAt = 2
	config.MaxReadOnlyCalls = 3
	governor := New(config)

	_ = governor.ObserveProgress("readonly", true, false, false)
	if got := governor.ObserveProgress("destructive", true, true, false); got.Stop || got.Kind != "read_only_budget_warning" {
		t.Fatalf("observed no-op shell = %+v, want no-progress warning", got)
	}
	if got := governor.ObserveProgress("modifying", true, true, true); got.Stop || got.Nudge != "" {
		t.Fatalf("observed edit = %+v, want reset", got)
	}
	for index := 1; index <= 3; index++ {
		got := governor.ObserveProgress("destructive", true, true, false)
		if index < 3 && got.Stop {
			t.Fatalf("no-op %d stopped early: %+v", index, got)
		}
		if index == 3 && (!got.Stop || got.Kind != "read_only_budget") {
			t.Fatalf("no-op %d = %+v, want read-only budget stop", index, got)
		}
	}
}

func TestGovernorEscalatesFromCreativeCheckpointToActionBoundary(t *testing.T) {
	config := DefaultConfig()
	config.ReadOnlyWarningAt = 2
	config.ReadOnlyActionAt = 4
	config.MaxReadOnlyCalls = 6
	governor := New(config)

	for count := 1; count <= 5; count++ {
		got := governor.ObserveProgress("readonly", true, false, false)
		switch count {
		case 2:
			if got.Kind != "read_only_budget_warning" || !strings.Contains(got.Nudge, "creative latitude") {
				t.Fatalf("checkpoint = %+v", got)
			}
		case 4:
			if got.Kind != "read_only_action_required" || !strings.Contains(got.Nudge, "next tool work") {
				t.Fatalf("action boundary = %+v", got)
			}
		default:
			if got.Stop {
				t.Fatalf("count %d stopped early: %+v", count, got)
			}
		}
	}
	if !governor.ActionRequired() {
		t.Fatal("action boundary did not narrow the capability phase")
	}
	if got := governor.ObserveProgress("modifying", true, true, true); got.Stop {
		t.Fatalf("observed change stopped: %+v", got)
	}
	if governor.ActionRequired() {
		t.Fatal("observed change did not restore discovery capability")
	}
	for count := 1; count <= 5; count++ {
		_ = governor.ObserveProgress("readonly", true, false, false)
	}
	if got := governor.ObserveProgress("readonly", true, false, false); !got.Stop || got.Kind != "read_only_budget" {
		t.Fatalf("limit = %+v, want deterministic stop", got)
	}
}
