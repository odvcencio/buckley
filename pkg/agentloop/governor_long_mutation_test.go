package agentloop

import "testing"

func TestGovernor_SuccessfulRepeatWarningsRetainFailureAndBudgetStops(t *testing.T) {
	for _, kind := range []string{"exact_repeat", "outcome_repeat", "action_cycle"} {
		for _, success := range []bool{false, true} {
			cfg := DefaultConfig()
			cfg.WarnOnSuccessfulRepeats = true
			switch kind {
			case "outcome_repeat":
				cfg.ExactRepeatLimit = 20
				cfg.OutcomeRepeatLimit = 3
				cfg.CycleRepeats = 10
			case "action_cycle":
				cfg.ExactRepeatLimit = 20
				cfg.OutcomeRepeatLimit = 20
			}
			g := New(cfg)
			var got Decision
			for range 3 {
				got = g.Observe("read_file", `{"path":"stable"}`, "same", success)
			}
			if got.Stop == success || (success && got.Nudge == "") {
				t.Fatalf("%s success=%v decision=%+v", kind, success, got)
			}
			if !success && got.Kind != kind {
				t.Fatalf("kind=%s want %s", got.Kind, kind)
			}
		}
	}
	cfg := DefaultConfig()
	cfg.WarnOnSuccessfulRepeats = true
	cfg.MaxToolCalls = 4
	g := New(cfg)
	var got Decision
	for range 4 {
		got = g.Observe("read_file", "{}", "same", true)
	}
	if !got.Stop || got.Kind != "tool_call_limit" {
		t.Fatalf("budget decision=%+v", got)
	}
}
