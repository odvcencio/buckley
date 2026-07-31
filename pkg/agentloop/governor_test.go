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

func TestGovernorDetectsShortActionCycle(t *testing.T) {
	config := DefaultConfig()
	config.ExactRepeatLimit = 20
	config.OutcomeRepeatLimit = 20
	governor := New(config)

	actions := []struct {
		name string
		args string
	}{
		{"read_file", `{"path":"a.go"}`},
		{"search_text", `{"pattern":"missing"}`},
		{"read_file", `{"path":"a.go"}`},
		{"search_text", `{"pattern":"missing"}`},
		{"read_file", `{"path":"a.go"}`},
		{"search_text", `{"pattern":"missing"}`},
	}
	var decision Decision
	for i, action := range actions {
		decision = governor.Observe(action.name, action.args, "result "+string(rune('a'+i)), true)
	}
	if !decision.Stop || decision.Kind != "action_cycle" {
		t.Fatalf("decision = %+v, want action-cycle stop", decision)
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
