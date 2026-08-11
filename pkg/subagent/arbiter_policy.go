package subagent

import (
	"context"
	"fmt"
	"strings"

	"m31labs.dev/buckley/pkg/agentcoord"
	"m31labs.dev/buckley/pkg/types"
)

// ArbiterAdmissionPolicy maps the existing delegation policy strategy onto
// Coordinator's mechanism-only admission contract. Evaluation failures are
// returned to the caller so an unavailable policy engine never becomes an
// implicit authorization grant.
type ArbiterAdmissionPolicy struct {
	evaluator types.RuleEvaluator
}

// NewArbiterAdmissionPolicy returns nil when no evaluator is configured,
// preserving explicit legacy behavior until a caller wires Arbiter in.
func NewArbiterAdmissionPolicy(evaluator types.RuleEvaluator) AdmissionPolicy {
	if evaluator == nil {
		return nil
	}
	return &ArbiterAdmissionPolicy{evaluator: evaluator}
}

// Admit implements AdmissionPolicy using permissions/delegation. The policy
// owns the allow/deny result and maximum timeout/iteration budgets; this
// adapter only supplies normalized facts and transfers the receipt fields.
func (p *ArbiterAdmissionPolicy) Admit(_ context.Context, spec agentcoord.TaskSpec) (AdmissionDecision, error) {
	if p == nil || p.evaluator == nil {
		return AdmissionDecision{}, fmt.Errorf("subagent arbiter admission policy is unavailable")
	}
	result, err := p.evaluator.EvalStrategy("permissions/delegation", "delegation_policy", map[string]any{
		"delegation_depth": spec.DelegationDepth,
		"role":             "subagent",
		"tier":             delegationTier(spec),
	})
	if err != nil {
		return AdmissionDecision{}, fmt.Errorf("evaluate delegation policy: %w", err)
	}
	action := strings.ToLower(strings.TrimSpace(result.String("action")))
	decision := AdmissionDecision{
		Reason:         strings.TrimSpace(result.String("reason")),
		TimeoutSeconds: result.Int("timeout_seconds"),
		StepCap:        result.Int("max_iterations"),
	}
	switch action {
	case "allow":
		decision.Allowed = true
		return decision, nil
	case "deny":
		return decision, nil
	default:
		return AdmissionDecision{}, fmt.Errorf("delegation policy returned unsupported action %q", action)
	}
}

func delegationTier(spec agentcoord.TaskSpec) string {
	if spec.Metadata != nil {
		if tier := strings.ToLower(strings.TrimSpace(spec.Metadata["delegation_tier"])); tier != "" {
			return tier
		}
	}
	switch strings.ToLower(strings.TrimSpace(spec.Tier)) {
	case "reason", "scrutiny", "heavy":
		return "heavy"
	default:
		return "light"
	}
}
