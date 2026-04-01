package types

import "context"

// RuleEvaluator abstracts arbiter strategy evaluation.
type RuleEvaluator interface {
	EvalStrategy(domain, name string, facts map[string]any) (StrategyResult, error)
}

// StrategyResult wraps arbiter strategy output.
type StrategyResult struct {
	Params map[string]any
}

func (r StrategyResult) String(key string) string {
	v, _ := r.Params[key].(string)
	return v
}

func (r StrategyResult) Float(key string) float64 {
	v, _ := r.Params[key].(float64)
	return v
}

func (r StrategyResult) Int(key string) int {
	v, _ := r.Params[key].(float64)
	return int(v)
}

func (r StrategyResult) Bool(key string) bool {
	v, _ := r.Params[key].(bool)
	return v
}

// EscalationRequest is submitted when a tool/subagent needs elevated access.
type EscalationRequest struct {
	ToolName     string
	CurrentTier  PermissionTier
	RequiredTier PermissionTier
	Reason       string
	AgentRole    string
	Context      map[string]any
}

// EscalationOutcome is the arbiter's verdict.
type EscalationOutcome struct {
	Granted   bool
	NewTier   PermissionTier
	Temporary bool
	AuditNote string
}

// PermissionEscalator decides escalation requests.
type PermissionEscalator interface {
	Decide(ctx context.Context, req EscalationRequest) (EscalationOutcome, error)
}

// SandboxResolver determines sandbox level for a tool invocation.
type SandboxResolver interface {
	ForTool(toolName, agentRole string, riskScore float64) SandboxLevel
}
