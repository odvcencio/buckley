package sdk

import (
	"context"
	"testing"

	"github.com/odvcencio/buckley/pkg/orchestrator"
)

func TestAgent_Orchestrator(t *testing.T) {
	agent := &Agent{orchestrator: &orchestrator.Orchestrator{}}

	orch := agent.Orchestrator()
	if orch == nil {
		t.Error("Orchestrator returned nil")
	}
}

func TestAgent_Plan_NilOrchestrator(t *testing.T) {
	agent := &Agent{}

	_, err := agent.Plan(context.Background(), "feature", "description")
	if err == nil {
		t.Error("expected error for nil orchestrator")
	}
}

func TestAgent_LoadPlan_NilOrchestrator(t *testing.T) {
	agent := &Agent{}

	_, err := agent.LoadPlan(context.Background(), "plan-id")
	if err == nil {
		t.Error("expected error for nil orchestrator")
	}
}

func TestAgent_ExecutePlan_NilOrchestrator(t *testing.T) {
	agent := &Agent{}

	err := agent.ExecutePlan(context.Background(), "plan-id")
	if err == nil {
		t.Error("expected error for nil orchestrator")
	}
}

func TestAgent_ListPlans_NilOrchestrator(t *testing.T) {
	agent := &Agent{}

	_, err := agent.ListPlans(context.Background())
	if err == nil {
		t.Error("expected error for nil orchestrator")
	}
}

func TestAgent_GetPlan_NilOrchestrator(t *testing.T) {
	agent := &Agent{}

	_, err := agent.GetPlan(context.Background(), "plan-id")
	if err == nil {
		t.Error("expected error for nil orchestrator")
	}
}
