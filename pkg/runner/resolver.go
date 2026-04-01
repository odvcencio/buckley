package runner

import (
	"fmt"

	"github.com/odvcencio/buckley/pkg/types"
)

// ResolveRunnerConfig evaluates autonomous/modes.arb to determine runner configuration.
func ResolveRunnerConfig(evaluator types.RuleEvaluator, flags CLIFlags) (*RunnerConfig, error) {
	facts := map[string]any{
		"requested_mode": flags.Mode,
		"has_tty":        flags.Prompt == "" && flags.Mode == "",
		"has_prompt":     flags.Prompt != "",
		"environment":    flags.Environment,
		"output_format":  flags.OutputFormat,
	}

	result, err := evaluator.EvalStrategy("autonomous/modes", "mode_policy", facts)
	if err != nil {
		return nil, fmt.Errorf("mode resolution failed: %w", err)
	}
	if result.String("action") == "deny" {
		return nil, fmt.Errorf("mode denied: %s", result.String("reason"))
	}

	return &RunnerConfig{
		Mode:             parseMode(result.String("mode")),
		Role:             result.String("role"),
		PermissionTier:   types.ParsePermissionTier(result.String("permission_tier")),
		MaxTurns:         result.Int("max_turns"),
		MaxCostUSD:       result.Float("max_cost_usd"),
		SessionIsolation: result.Bool("session_isolation"),
		SandboxDefault:   types.ParseSandboxLevel(result.String("sandbox_default")),
	}, nil
}
