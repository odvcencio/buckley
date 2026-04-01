package bootstrap

import (
	"context"
	"strings"
	"testing"

	"github.com/odvcencio/buckley/pkg/types"
)

type mockPhaseEvaluator struct {
	skipPhases map[string]bool
	denyPhases map[string]string // phase -> reason
}

func (m *mockPhaseEvaluator) EvalStrategy(domain, name string, facts map[string]any) (types.StrategyResult, error) {
	phase, _ := facts["phase"].(string)
	if reason, ok := m.denyPhases[phase]; ok {
		return types.StrategyResult{Params: map[string]any{"action": "deny", "reason": reason}}, nil
	}
	if m.skipPhases[phase] {
		return types.StrategyResult{Params: map[string]any{"action": "skip", "reason": "test skip"}}, nil
	}
	return types.StrategyResult{Params: map[string]any{"action": "allow", "reason": ""}}, nil
}

func TestBootstrapPlan_AllPhasesExecute(t *testing.T) {
	executed := map[Phase]bool{}
	plan := NewBootstrapPlan(nil) // no evaluator = no gating
	plan.SetExecutor(func(ctx context.Context, phase Phase) error {
		executed[phase] = true
		return nil
	})

	if err := plan.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for p := PhaseCliParse; p <= PhaseReady; p++ {
		if !executed[p] {
			t.Errorf("phase %s was not executed", p)
		}
	}
}

func TestBootstrapPlan_SkipsGatedPhase(t *testing.T) {
	eval := &mockPhaseEvaluator{skipPhases: map[string]bool{"gts_init": true}}
	executed := map[Phase]bool{}

	plan := NewBootstrapPlan(eval)
	plan.SetExecutor(func(ctx context.Context, phase Phase) error {
		executed[phase] = true
		return nil
	})

	if err := plan.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if executed[PhaseGTSInit] {
		t.Error("expected gts_init to be skipped")
	}
	if !executed[PhaseConfigLoad] {
		t.Error("expected config_load to be executed")
	}
	if !executed[PhaseReady] {
		t.Error("expected ready to be executed")
	}
}

func TestBootstrapPlan_DeniedPhase(t *testing.T) {
	eval := &mockPhaseEvaluator{
		denyPhases: map[string]string{"mode_route": "daemon not allowed"},
	}
	plan := NewBootstrapPlan(eval)
	plan.SetExecutor(func(ctx context.Context, phase Phase) error { return nil })

	err := plan.Run(context.Background())
	if err == nil {
		t.Fatal("expected error for denied phase")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Errorf("error = %q, expected 'denied'", err.Error())
	}
}

func TestBootstrapPlan_PhaseString(t *testing.T) {
	if PhaseCliParse.String() != "cli_parse" {
		t.Errorf("PhaseCliParse = %q", PhaseCliParse.String())
	}
	if PhaseReady.String() != "ready" {
		t.Errorf("PhaseReady = %q", PhaseReady.String())
	}
}

func TestBootstrapPlan_EarlyPhasesNotGated(t *testing.T) {
	// Phases before and including ArbiterInit should NOT be gated
	eval := &mockPhaseEvaluator{
		denyPhases: map[string]string{
			"cli_parse":    "should not be checked",
			"config_load":  "should not be checked",
			"arbiter_init": "should not be checked",
		},
	}
	executed := map[Phase]bool{}
	plan := NewBootstrapPlan(eval)
	plan.SetExecutor(func(ctx context.Context, phase Phase) error {
		executed[phase] = true
		return nil
	})

	// Should NOT error — early phases aren't gated
	err := plan.Run(context.Background())
	// It will error on auth_resolve or later, but cli_parse/config_load/arbiter_init should execute
	if !executed[PhaseCliParse] {
		t.Error("cli_parse should execute (not gated)")
	}
	if !executed[PhaseConfigLoad] {
		t.Error("config_load should execute (not gated)")
	}
	if !executed[PhaseArbiterInit] {
		t.Error("arbiter_init should execute (not gated)")
	}
	_ = err // we expect it to fail on auth_resolve deny
}
