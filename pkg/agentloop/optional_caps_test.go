package agentloop

import (
	"fmt"
	"strings"
	"testing"
)

// A governor built from the default config has no round or tool-call ceiling:
// 500 distinct calls and 500 rounds all pass. Caps are opt-in.
func TestGovernor_DefaultHasNoRoundOrToolCallCap(t *testing.T) {
	g := New(DefaultConfig())
	for i := 0; i < 500; i++ {
		if d := g.BeginRound(); d.Stop {
			t.Fatalf("round %d stopped: %s", i+1, d.Reason)
		}
		if d := g.Observe("read_file", fmt.Sprintf(`{"path":"f%d"}`, i), fmt.Sprintf("content %d", i), true); d.Stop {
			t.Fatalf("call %d stopped: %s", i+1, d.Reason)
		}
	}
	if g.RemainingToolCalls() <= 1000 {
		t.Fatalf("remaining calls = %d, want unlimited", g.RemainingToolCalls())
	}
}

func TestGovernor_ExplicitCapStopsAndSaysHowToRaiseIt(t *testing.T) {
	g := New(Config{MaxToolCalls: 3})
	var stop Decision
	for i := 0; i < 5 && !stop.Stop; i++ {
		stop = g.Observe("read_file", fmt.Sprintf(`{"path":"f%d"}`, i), fmt.Sprintf("c%d", i), true)
	}
	if !stop.Stop || stop.Kind != "tool_call_limit" {
		t.Fatalf("decision = %+v, want a tool_call_limit stop", stop)
	}
	for _, want := range []string{"3-call harness limit", "--max-tool-calls", "setting 0"} {
		if !strings.Contains(stop.Reason, want) {
			t.Errorf("reason %q lacks %q", stop.Reason, want)
		}
	}

	rounds := New(Config{MaxRounds: 2})
	rounds.BeginRound()
	rounds.BeginRound()
	if d := rounds.BeginRound(); !d.Stop || !strings.Contains(d.Reason, "2-round harness limit") || !strings.Contains(d.Reason, "emergency_fuse") {
		t.Fatalf("round stop = %+v", d)
	}
}

// Repeat detection is a safety property, not a budget: it stays on with no caps.
func TestGovernor_RepeatDetectionStaysOnWithoutCaps(t *testing.T) {
	g := New(DefaultConfig())
	var stop Decision
	for i := 0; i < 10 && !stop.Stop; i++ {
		stop = g.Observe("read_file", `{"path":"same"}`, "same result", true)
	}
	if !stop.Stop {
		t.Fatal("an identical repeated call was never stopped")
	}
}
