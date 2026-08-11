package subagent

import (
	"fmt"
	"testing"

	"m31labs.dev/buckley/pkg/agentcoord"
	"m31labs.dev/buckley/pkg/types"
)

type admissionEvaluator func(domain, name string, facts map[string]any) (types.StrategyResult, error)

func (f admissionEvaluator) EvalStrategy(domain, name string, facts map[string]any) (types.StrategyResult, error) {
	return f(domain, name, facts)
}

func TestArbiterAdmissionPolicy_TransfersAllowAndBounds(t *testing.T) {
	policy := NewArbiterAdmissionPolicy(admissionEvaluator(func(domain, name string, facts map[string]any) (types.StrategyResult, error) {
		if domain != "permissions/delegation" || name != "delegation_policy" {
			return types.StrategyResult{}, fmt.Errorf("unexpected strategy %s/%s", domain, name)
		}
		if facts["role"] != "subagent" || facts["tier"] != "heavy" || facts["delegation_depth"] != 1 {
			return types.StrategyResult{}, fmt.Errorf("unexpected facts: %+v", facts)
		}
		return types.StrategyResult{Params: map[string]any{
			"action":          "allow",
			"timeout_seconds": 300.0,
			"max_iterations":  30.0,
		}}, nil
	}))
	decision, err := policy.Admit(t.Context(), agentcoord.TaskSpec{Tier: "reason", DelegationDepth: 1})
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if !decision.Allowed || decision.TimeoutSeconds != 300 || decision.StepCap != 30 {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestArbiterAdmissionPolicy_DenyRetainsReason(t *testing.T) {
	policy := NewArbiterAdmissionPolicy(admissionEvaluator(func(_, _ string, _ map[string]any) (types.StrategyResult, error) {
		return types.StrategyResult{Params: map[string]any{
			"action": "deny",
			"reason": "max delegation depth reached",
		}}, nil
	}))
	decision, err := policy.Admit(t.Context(), agentcoord.TaskSpec{})
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if decision.Allowed || decision.Reason != "max delegation depth reached" {
		t.Fatalf("decision = %+v", decision)
	}
}
