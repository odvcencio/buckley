package rules

import (
	"context"
	"log/slog"

	"github.com/odvcencio/buckley/pkg/types"
)

type ArbiterEscalator struct{ evaluator types.RuleEvaluator }

func NewArbiterEscalator(evaluator types.RuleEvaluator) *ArbiterEscalator {
	return &ArbiterEscalator{evaluator: evaluator}
}

func (a *ArbiterEscalator) Decide(ctx context.Context, req types.EscalationRequest) (types.EscalationOutcome, error) {
	facts := PermissionEscalationFacts{
		ToolName:     req.ToolName,
		CurrentTier:  req.CurrentTier.String(),
		RequiredTier: req.RequiredTier.String(),
		AgentRole:    req.AgentRole,
		Reason:       req.Reason,
	}
	result, err := a.evaluator.EvalStrategy("permissions/escalation", "permission_escalation_policy", facts.ToMap())
	if err != nil {
		slog.Warn("arbiter escalation eval failed, denying", "error", err)
		return types.EscalationOutcome{Granted: false, AuditNote: "arbiter eval failed: " + err.Error()}, nil
	}
	action := result.String("action")
	return types.EscalationOutcome{
		Granted:   action == "grant" || action == "grant_temporary",
		NewTier:   types.ParsePermissionTier(result.String("new_tier")),
		Temporary: action == "grant_temporary",
		AuditNote: result.String("reason"),
	}, nil
}
