package rules

import (
	"context"
	"testing"

	"github.com/odvcencio/buckley/pkg/types"
)

func TestArbiterEscalator_DenySubagentFull(t *testing.T) {
	engine := mustNewTestEngine(t)
	adapter := NewEngineAdapter(engine)
	escalator := NewArbiterEscalator(adapter)

	outcome, err := escalator.Decide(context.Background(), types.EscalationRequest{
		ToolName:     "bash",
		CurrentTier:  types.TierWorkspaceWrite,
		RequiredTier: types.TierFullAccess,
		AgentRole:    "subagent",
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if outcome.Granted {
		t.Error("expected deny for subagent requesting full_access")
	}
	if outcome.AuditNote == "" {
		t.Error("expected non-empty audit note")
	}
}

func TestArbiterEscalator_GrantCoordinatorSpawn(t *testing.T) {
	engine := mustNewTestEngine(t)
	adapter := NewEngineAdapter(engine)
	escalator := NewArbiterEscalator(adapter)

	outcome, err := escalator.Decide(context.Background(), types.EscalationRequest{
		ToolName:     "spawn_subagent",
		CurrentTier:  types.TierWorkspaceWrite,
		RequiredTier: types.TierShellExec,
		AgentRole:    "coordinator",
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if !outcome.Granted {
		t.Error("expected grant for coordinator spawn")
	}
	if !outcome.Temporary {
		t.Error("expected temporary grant")
	}
}

func TestArbiterEscalator_DefaultDeny(t *testing.T) {
	engine := mustNewTestEngine(t)
	adapter := NewEngineAdapter(engine)
	escalator := NewArbiterEscalator(adapter)

	outcome, err := escalator.Decide(context.Background(), types.EscalationRequest{
		ToolName:     "bash",
		CurrentTier:  types.TierReadOnly,
		RequiredTier: types.TierShellExec,
		AgentRole:    "unknown_role",
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if outcome.Granted {
		t.Error("expected default deny")
	}
}
