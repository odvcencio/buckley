package rules

import (
	"log/slog"

	"github.com/odvcencio/buckley/pkg/types"
)

type ArbiterSandboxResolver struct{ evaluator types.RuleEvaluator }

func NewArbiterSandboxResolver(evaluator types.RuleEvaluator) *ArbiterSandboxResolver {
	return &ArbiterSandboxResolver{evaluator: evaluator}
}

func (s *ArbiterSandboxResolver) ForTool(toolName, agentRole string, riskScore float64) types.SandboxLevel {
	facts := SandboxFacts{Tool: toolName, Role: agentRole, RiskScore: riskScore}
	result, err := s.evaluator.EvalStrategy("permissions/sandbox", "sandbox_level", facts.ToMap())
	if err != nil {
		slog.Warn("arbiter sandbox eval failed, falling back to full", "error", err)
		return types.SandboxFull
	}
	return types.ParseSandboxLevel(result.String("level"))
}
