package runner

import (
	"context"

	"github.com/odvcencio/buckley/pkg/orchestrator"
	"github.com/odvcencio/buckley/pkg/types"
)

// OneShotResult is the structured output of a oneshot execution.
type OneShotResult struct {
	Message    string                       `json:"message"`
	Model      string                       `json:"model,omitempty"`
	Iterations int                          `json:"iterations"`
	ToolUses   []orchestrator.ToolUseRecord `json:"tool_uses,omitempty"`
	Audit      []orchestrator.AuditEntry    `json:"audit,omitempty"`
}

// RuntimeDeps holds all dependencies needed by runners.
type RuntimeDeps struct {
	Api       orchestrator.ApiClient
	Tools     orchestrator.ToolExecutor
	Escalator types.PermissionEscalator
	Sandbox   types.SandboxResolver
	Evaluator types.RuleEvaluator
}

// RunOneShot executes a single prompt and returns structured results.
func RunOneShot(ctx context.Context, cfg *RunnerConfig, prompt string, deps *RuntimeDeps) (*OneShotResult, error) {
	loop := orchestrator.NewRuntimeLoop(deps.Api, deps.Tools, deps.Escalator, deps.Sandbox, deps.Evaluator)
	loop.SetMaxIterations(cfg.MaxTurns)
	loop.SetRole(cfg.Role)

	summary, err := loop.RunTurn(ctx, prompt)
	if err != nil {
		// Return partial result on budget/iteration errors
		if summary != nil {
			return &OneShotResult{
				Message:    summary.FinalText(),
				Iterations: summary.Iterations,
				ToolUses:   summary.ToolUses,
				Audit:      summary.AuditTrail,
			}, err
		}
		return nil, err
	}

	return &OneShotResult{
		Message:    summary.FinalText(),
		Iterations: summary.Iterations,
		ToolUses:   summary.ToolUses,
		Audit:      summary.AuditTrail,
	}, nil
}
