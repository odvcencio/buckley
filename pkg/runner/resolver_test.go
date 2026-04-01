package runner

import (
	"testing"

	"github.com/odvcencio/buckley/pkg/types"
)

type modeEvaluator struct{}

func (m *modeEvaluator) EvalStrategy(domain, name string, facts map[string]any) (types.StrategyResult, error) {
	hasPrompt, _ := facts["has_prompt"].(bool)
	reqMode, _ := facts["requested_mode"].(string)

	if reqMode == "daemon" {
		env, _ := facts["environment"].(string)
		if env == "ci" {
			return types.StrategyResult{Params: map[string]any{
				"action": "deny", "reason": "daemon not in CI",
			}}, nil
		}
		return types.StrategyResult{Params: map[string]any{
			"action": "allow", "mode": "daemon", "role": "worker",
			"permission_tier": "workspace_write", "max_turns": float64(10),
			"max_cost_usd": 1.0, "session_isolation": true,
			"sandbox_default": "workspace",
		}}, nil
	}

	if hasPrompt {
		return types.StrategyResult{Params: map[string]any{
			"action": "allow", "mode": "oneshot", "role": "worker",
			"permission_tier": "workspace_write", "max_turns": float64(5),
			"max_cost_usd": 2.0, "sandbox_default": "workspace",
		}}, nil
	}

	return types.StrategyResult{Params: map[string]any{
		"action": "allow", "mode": "interactive", "role": "interactive",
		"permission_tier": "full_access", "sandbox_default": "none",
	}}, nil
}

func TestResolveRunnerConfig_OneshotMode(t *testing.T) {
	cfg, err := ResolveRunnerConfig(&modeEvaluator{}, CLIFlags{Prompt: "hello"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.Mode != ModeOneShot {
		t.Errorf("mode = %v, want oneshot", cfg.Mode)
	}
	if cfg.Role != "worker" {
		t.Errorf("role = %q, want worker", cfg.Role)
	}
	if cfg.MaxTurns != 5 {
		t.Errorf("max_turns = %d, want 5", cfg.MaxTurns)
	}
}

func TestResolveRunnerConfig_DaemonMode(t *testing.T) {
	cfg, err := ResolveRunnerConfig(&modeEvaluator{}, CLIFlags{Mode: "daemon", Environment: "server"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.Mode != ModeDaemon {
		t.Errorf("mode = %v, want daemon", cfg.Mode)
	}
	if !cfg.SessionIsolation {
		t.Error("expected session isolation for daemon")
	}
}

func TestResolveRunnerConfig_DaemonDeniedInCI(t *testing.T) {
	_, err := ResolveRunnerConfig(&modeEvaluator{}, CLIFlags{Mode: "daemon", Environment: "ci"})
	if err == nil {
		t.Error("expected deny for daemon in CI")
	}
}

func TestResolveRunnerConfig_InteractiveDefault(t *testing.T) {
	cfg, err := ResolveRunnerConfig(&modeEvaluator{}, CLIFlags{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.Mode != ModeInteractive {
		t.Errorf("mode = %v, want interactive", cfg.Mode)
	}
}
