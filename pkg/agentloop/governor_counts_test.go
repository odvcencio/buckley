package agentloop

import (
	"fmt"
	"testing"
)

func TestGovernor_RepeatEvidenceStaysBounded(t *testing.T) {
	g := New(DefaultConfig())
	pass := ToolOutcome{Success: true, StateObserved: true, VerificationObserved: true, VerificationPassed: true}
	for i := range DefaultEvidenceWindow * 3 {
		args := fmt.Sprintf(`{"path":"pkg/p%d"}`, i)
		g.ObserveProgress("readonly", true, true, false)
		if d := g.Observe("run_tests", args, "pass", true); d.Stop {
			t.Fatalf("unique call %d stopped: %+v", i, d)
		}
		if d := g.ObserveVerification("run_tests", args, pass); d.Stop {
			t.Fatalf("unique verification %d stopped: %+v", i, d)
		}
		for _, window := range []*repeatWindow{&g.exactCounts, &g.outcomeCounts, &g.verificationCounts} {
			if len(window.counts) > DefaultEvidenceWindow || len(window.history) > DefaultEvidenceWindow {
				t.Fatalf("unbounded repeat evidence at %d: counts=%d history=%d", i, len(window.counts), len(window.history))
			}
		}
	}
	if g.ToolCalls() != DefaultEvidenceWindow*3 {
		t.Fatalf("tool-call total = %d", g.ToolCalls())
	}
	g.ObserveProgress("modifying", true, true, true)
	for _, window := range []*repeatWindow{&g.exactCounts, &g.outcomeCounts, &g.verificationCounts} {
		if len(window.counts) != 0 || len(window.history) != 0 || window.next != 0 {
			t.Fatalf("workspace change retained evidence: %+v", window)
		}
		for _, entry := range window.history[:cap(window.history)] {
			if entry != nil {
				t.Fatal("workspace change retained a hash through the ring backing array")
			}
		}
	}
	if novelty, observed := g.EvidenceNovelty(); novelty != 0 || observed {
		t.Fatalf("novelty after workspace change = %v, %t", novelty, observed)
	}
	if g.ToolCalls() != DefaultEvidenceWindow*3 {
		t.Fatal("workspace change reset the hard tool-call counter")
	}
}
