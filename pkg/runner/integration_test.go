package runner

import (
	"context"
	"testing"

	"github.com/odvcencio/buckley/pkg/rules"
	"github.com/odvcencio/buckley/pkg/types"
)

// mustNewFullEngine creates an engine with all embedded .arb files.
func mustNewFullEngine(t *testing.T) *rules.Engine {
	t.Helper()
	engine, err := rules.NewEngine()
	if err != nil {
		t.Fatalf("creating full engine: %v", err)
	}
	return engine
}

func TestSafetyStack_DaemonCannotEscalate(t *testing.T) {
	engine := mustNewFullEngine(t)
	adapter := rules.NewEngineAdapter(engine)
	escalator := rules.NewArbiterEscalator(adapter)
	sandbox := rules.NewArbiterSandboxResolver(adapter)

	// Daemon worker requesting full_access — must be denied
	outcome, err := escalator.Decide(context.Background(), types.EscalationRequest{
		ToolName:     "bash",
		CurrentTier:  types.TierWorkspaceWrite,
		RequiredTier: types.TierFullAccess,
		AgentRole:    "worker",
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if outcome.Granted {
		t.Error("daemon worker must not escalate to full_access")
	}

	// Sandbox for bash in worker role — must be at least workspace
	level := sandbox.ForTool("bash", "worker", 0)
	if level < types.SandboxWorkspace {
		t.Errorf("bash in worker role has sandbox %v, want at least SandboxWorkspace", level)
	}
}

func TestSafetyStack_SubagentCannotGetFullAccess(t *testing.T) {
	engine := mustNewFullEngine(t)
	adapter := rules.NewEngineAdapter(engine)
	escalator := rules.NewArbiterEscalator(adapter)

	outcome, _ := escalator.Decide(context.Background(), types.EscalationRequest{
		ToolName:     "bash",
		CurrentTier:  types.TierReadOnly,
		RequiredTier: types.TierFullAccess,
		AgentRole:    "subagent",
	})
	if outcome.Granted {
		t.Error("subagent must not escalate to full_access")
	}
}

func TestSafetyStack_CoordinatorCanSpawn(t *testing.T) {
	engine := mustNewFullEngine(t)
	adapter := rules.NewEngineAdapter(engine)
	escalator := rules.NewArbiterEscalator(adapter)

	outcome, _ := escalator.Decide(context.Background(), types.EscalationRequest{
		ToolName:     "spawn_subagent",
		CurrentTier:  types.TierWorkspaceWrite,
		RequiredTier: types.TierShellExec,
		AgentRole:    "coordinator",
	})
	if !outcome.Granted {
		t.Error("coordinator should be granted temporary shell_exec for spawn_subagent")
	}
	if !outcome.Temporary {
		t.Error("coordinator spawn grant should be temporary")
	}
}

func TestSafetyStack_BudgetHaltsExecution(t *testing.T) {
	engine := mustNewFullEngine(t)
	result, err := engine.EvalStrategy("cost/budgets", "cost_policy", map[string]any{
		"session_spend":      10.0,
		"session_budget":     10.0,
		"budget_util":        1.0,
		"current_model_cost": 3.0,
	})
	if err != nil {
		t.Fatalf("EvalStrategy: %v", err)
	}
	if result.Params["action"] != "halt" {
		t.Errorf("action = %v, want halt when budget exhausted", result.Params["action"])
	}
}

func TestSafetyStack_BudgetDowngradesModel(t *testing.T) {
	engine := mustNewFullEngine(t)
	result, err := engine.EvalStrategy("cost/budgets", "cost_policy", map[string]any{
		"session_spend":      9.5,
		"session_budget":     10.0,
		"budget_util":        0.95,
		"current_model_cost": 15.0, // expensive model
	})
	if err != nil {
		t.Fatalf("EvalStrategy: %v", err)
	}
	if result.Params["action"] != "downgrade_model" {
		t.Errorf("action = %v, want downgrade_model", result.Params["action"])
	}
}

func TestSafetyStack_DaemonDeniedInCI(t *testing.T) {
	engine := mustNewFullEngine(t)
	result, err := engine.EvalStrategy("autonomous/modes", "mode_policy", map[string]any{
		"requested_mode": "daemon",
		"environment":    "ci",
		"has_tty":        false,
		"has_prompt":     false,
	})
	if err != nil {
		t.Fatalf("EvalStrategy: %v", err)
	}
	if result.Params["action"] != "deny" {
		t.Errorf("action = %v, want deny for daemon in CI", result.Params["action"])
	}
}

func TestSafetyStack_TimeoutGovernance(t *testing.T) {
	engine := mustNewFullEngine(t)

	tests := []struct {
		name        string
		tool        string
		role        string
		wantSeconds float64
	}{
		{"bash_coordinator", "bash", "coordinator", 60},
		{"bash_subagent", "bash", "subagent", 120},
		{"web_fetch", "web_fetch", "interactive", 15},
		{"spawn_subagent", "spawn_subagent", "coordinator", 300},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := engine.EvalStrategy("runtime/timeouts", "timeout_policy", map[string]any{
				"tool": tt.tool,
				"role": tt.role,
			})
			if err != nil {
				t.Fatalf("EvalStrategy: %v", err)
			}
			got, _ := result.Params["timeout_seconds"].(float64)
			if got != tt.wantSeconds {
				t.Errorf("timeout = %v, want %v", got, tt.wantSeconds)
			}
		})
	}
}

func TestSafetyStack_DelegationDepthLimit(t *testing.T) {
	engine := mustNewFullEngine(t)
	result, err := engine.EvalStrategy("permissions/delegation", "delegation_policy", map[string]any{
		"delegation_depth": 2,
		"role":             "subagent",
		"tier":             "standard",
	})
	if err != nil {
		t.Fatalf("EvalStrategy: %v", err)
	}
	if result.Params["action"] != "deny" {
		t.Errorf("action = %v, want deny at depth 2", result.Params["action"])
	}
}

func TestSafetyStack_InterviewTriggersOnAmbiguousFeature(t *testing.T) {
	engine := mustNewFullEngine(t)
	result, err := engine.EvalStrategy("session/interview", "interview_policy", map[string]any{
		"mode":       "interactive",
		"task_type":  "feature",
		"ambiguity":  0.7,
		"file_count": 3,
	})
	if err != nil {
		t.Fatalf("EvalStrategy: %v", err)
	}
	shouldInterview, _ := result.Params["should_interview"].(bool)
	if !shouldInterview {
		t.Error("expected interview for ambiguous feature request")
	}
}

func TestSafetyStack_IntelligenceSelectsToolsForDebugging(t *testing.T) {
	engine := mustNewFullEngine(t)
	result, err := engine.EvalStrategy("session/intelligence", "intelligence_policy", map[string]any{
		"task_type": "debugging",
		"has_gts":   true,
	})
	if err != nil {
		t.Fatalf("EvalStrategy: %v", err)
	}
	tools, _ := result.Params["tools"].(string)
	if tools == "" {
		t.Error("expected non-empty tool list for debugging with gts")
	}
}
