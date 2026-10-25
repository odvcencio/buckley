package agentloop

import (
	"fmt"
	"strings"
	"testing"
)

func TestGovernor_VerificationRepeatIgnoresOutputNoise(t *testing.T) {
	for _, warnOnly := range []bool{false, true} {
		t.Run(fmt.Sprintf("warn_only_%t", warnOnly), func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.WarnOnSuccessfulRepeats = warnOnly
			g := New(cfg)
			for i := 1; i <= 3; i++ {
				outcome := ToolOutcome{Content: fmt.Sprintf("PASS duration=%d cached=%t", i, i > 1), Success: true,
					StateObserved: true, VerificationObserved: true, VerificationPassed: true}
				g.ObserveProgress("readonly", true, true, false)
				g.Observe("run_tests", `{"path":"./render","pattern":"TestWAV"}`, outcome.Content, true)
				d := g.ObserveVerification("run_tests", `{"pattern":"TestWAV","path":"./render"}`, outcome)
				if i == 1 && (d.Stop || d.Nudge != "") {
					t.Fatalf("first verification = %+v", d)
				}
				if i == 2 && (d.Stop || !strings.Contains(d.Nudge, "already passed")) {
					t.Fatalf("second verification = %+v, want warning", d)
				}
				if i == 3 && (d.Stop == warnOnly || !strings.HasPrefix(d.Kind, "verification_repeat")) {
					t.Fatalf("third verification = %+v, warnOnly=%t", d, warnOnly)
				}
			}
			if got := g.RepetitionPressure(); got != 1 {
				t.Fatalf("repetition pressure = %v, want 1", got)
			}
		})
	}
}

func TestGovernor_VerificationRepeatRequiresObservedPassingCheck(t *testing.T) {
	for _, tc := range []struct {
		name    string
		outcome ToolOutcome
	}{
		{"unclassified", ToolOutcome{Success: true, StateObserved: true}},
		{"unobserved", ToolOutcome{Success: true, VerificationObserved: true, VerificationPassed: true}},
		{"observation failed", ToolOutcome{Success: true, StateObserved: true, StateObservationFailed: true, VerificationObserved: true, VerificationPassed: true}},
		{"failed", ToolOutcome{StateObserved: true, VerificationObserved: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := New(DefaultConfig())
			for range 5 {
				if d := g.ObserveVerification("check", `{}`, tc.outcome); d.Stop || d.Nudge != "" {
					t.Fatalf("non-passing or unobserved check = %+v", d)
				}
			}
		})
	}
}

func TestGovernor_VerificationRepeatResetsOnChangeAndFailedCheck(t *testing.T) {
	pass := ToolOutcome{Success: true, StateObserved: true, VerificationObserved: true, VerificationPassed: true}
	for _, reset := range []string{"workspace change", "failed check", "unobserved check", "observation failed"} {
		t.Run(reset, func(t *testing.T) {
			g := New(DefaultConfig())
			for range 2 {
				g.ObserveVerification("run_tests", `{}`, pass)
			}
			if reset == "workspace change" {
				g.ObserveProgress("modifying", true, true, true)
			} else if reset == "failed check" {
				g.ObserveVerification("run_tests", `{}`, ToolOutcome{VerificationObserved: true})
			} else {
				unknown := pass
				unknown.StateObserved = reset != "unobserved check"
				unknown.StateObservationFailed = reset == "observation failed"
				g.ObserveVerification("run_tests", `{}`, unknown)
			}
			if d := g.ObserveVerification("run_tests", `{}`, pass); d.Stop || d.Nudge != "" {
				t.Fatalf("first pass after %s = %+v", reset, d)
			}
		})
	}
	g := New(DefaultConfig())
	for i := range 8 {
		if d := g.ObserveVerification("run_tests", fmt.Sprintf(`{"pattern":"Test%d"}`, i), pass); d.Stop || d.Nudge != "" {
			t.Fatalf("distinct check %d = %+v", i, d)
		}
	}
}
