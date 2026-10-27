package agentloop

import (
	"fmt"
	"testing"
)

func TestGovernor_EvidenceNoveltyUsesRecentObservations(t *testing.T) {
	const window = 4096
	cfg := DefaultConfig()
	cfg.WarnOnSuccessfulRepeats = true
	g := New(cfg)
	for range window {
		g.Observe("read_file", `{}`, "old evidence", true)
	}
	if novelty, observed := g.EvidenceNovelty(); !observed || novelty >= 0.01 {
		t.Fatalf("repeated evidence novelty = %v, observed=%t", novelty, observed)
	}
	for i := range window {
		g.ObserveProgress("readonly", true, true, false)
		g.Observe("read_file", fmt.Sprintf(`{"path":"file%d"}`, i), "new evidence", true)
	}
	if novelty, observed := g.EvidenceNovelty(); !observed || novelty != 1 {
		t.Fatalf("fresh recent evidence novelty = %v, observed=%t; want 1", novelty, observed)
	}
}

func TestGovernor_RepeatDetectionAcrossWindowRollover(t *testing.T) {
	for _, kind := range []string{"exact_repeat", "outcome_repeat", "action_cycle"} {
		t.Run(kind, func(t *testing.T) {
			cfg := DefaultConfig()
			if kind == "outcome_repeat" {
				cfg.OutcomeRepeatLimit = 3
			}
			if kind == "action_cycle" {
				cfg.ExactRepeatLimit, cfg.OutcomeRepeatLimit = 10, 10
			}
			g := New(cfg)
			for i := range 4095 {
				g.Observe("read_file", fmt.Sprintf(`{"path":"old%d"}`, i), "old", true)
			}
			calls := 3
			if kind == "action_cycle" {
				calls = 6
			}
			for i := range calls {
				args, result, success := `{"path":"target"}`, "same result", true
				if kind == "outcome_repeat" {
					result = fmt.Sprintf(`{"error_kind":"file_not_found","attempt":%d}`, i)
					success = false
				}
				if kind == "action_cycle" {
					args = fmt.Sprintf(`{"path":"target%d"}`, i%2)
				}
				d := g.Observe("read_file", args, result, success)
				if i < calls-1 && d.Stop {
					t.Fatalf("early stop at call %d: %+v", i+1, d)
				}
				if i == calls-1 && (!d.Stop || d.Kind != kind) {
					t.Fatalf("last decision = %+v, want %s", d, kind)
				}
			}
		})
	}
}

func TestGovernor_VerificationResetSurvivesOldObservationExpiry(t *testing.T) {
	pass := ToolOutcome{Success: true, StateObserved: true, VerificationObserved: true, VerificationPassed: true}
	for _, reset := range []string{"failed", "unobserved", "observation_failed"} {
		t.Run(reset, func(t *testing.T) {
			g := New(DefaultConfig())
			for i := range 4095 {
				g.ObserveVerification("run_tests", fmt.Sprintf(`{"pattern":"Old%d"}`, i), pass)
			}
			for range 2 {
				g.ObserveVerification("run_tests", `{}`, pass)
			}
			invalid := pass
			switch reset {
			case "failed":
				invalid.Success, invalid.VerificationPassed = false, false
			case "unobserved":
				invalid.StateObserved = false
			case "observation_failed":
				invalid.StateObservationFailed = true
			}
			g.ObserveVerification("run_tests", `{}`, invalid)
			if d := g.ObserveVerification("run_tests", `{}`, pass); d.Stop || d.Nudge != "" {
				t.Fatalf("first new pass after reset = %+v", d)
			}
			for i := range 4093 {
				g.ObserveVerification("run_tests", fmt.Sprintf(`{"pattern":"New%d"}`, i), pass)
			}
			if d := g.ObserveVerification("run_tests", `{}`, pass); d.Stop || d.Kind != "verification_repeat_warning" {
				t.Fatalf("second new pass while old record expires = %+v", d)
			}
			if d := g.ObserveVerification("run_tests", `{}`, pass); !d.Stop || d.Kind != "verification_repeat" {
				t.Fatalf("third new pass while old record expires = %+v", d)
			}
		})
	}
}

func TestGovernor_LongTurnKeepsConfiguredFuses(t *testing.T) {
	for _, fuse := range []string{"tool_call_limit", "round_limit"} {
		t.Run(fuse, func(t *testing.T) {
			const limit = 4113
			cfg := DefaultConfig()
			if fuse == "tool_call_limit" {
				cfg.MaxToolCalls = limit
			} else {
				cfg.MaxRounds = limit - 1
			}
			g := New(cfg)
			for i := range limit {
				d := g.BeginRound()
				if fuse == "tool_call_limit" {
					d = g.Observe("read_file", fmt.Sprintf(`{"path":"f%d"}`, i), "evidence", true)
				}
				if i < limit-1 && d.Stop {
					t.Fatalf("early fuse at %d: %+v", i+1, d)
				}
				if i == limit-1 && (!d.Stop || d.Kind != fuse) {
					t.Fatalf("last decision = %+v, want %s", d, fuse)
				}
			}
		})
	}
}

func TestGovernor_LargeConfiguredRepeatThresholdRemainsReachable(t *testing.T) {
	const limit = 4113
	for _, kind := range []string{"verification_repeat", "outcome_repeat"} {
		t.Run(kind, func(t *testing.T) {
			cfg := DefaultConfig()
			if kind == "verification_repeat" {
				cfg.ExactRepeatLimit = limit
			} else {
				cfg.OutcomeRepeatLimit = limit
			}
			g := New(cfg)
			pass := ToolOutcome{Success: true, StateObserved: true, VerificationObserved: true, VerificationPassed: true}
			for i := range limit {
				var d Decision
				if kind == "verification_repeat" {
					d = g.ObserveVerification("run_tests", `{}`, pass)
				} else {
					d = g.Observe("read_file", `{}`, fmt.Sprintf(`{"error_kind":"file_not_found","attempt":%d}`, i), false)
				}
				if i < limit-1 && d.Stop {
					t.Fatalf("early repeat at %d: %+v", i+1, d)
				}
				if i == limit-1 && (!d.Stop || d.Kind != kind) {
					t.Fatalf("last decision = %+v, want %s", d, kind)
				}
			}
		})
	}
}
