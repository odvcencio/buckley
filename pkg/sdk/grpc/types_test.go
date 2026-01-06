package grpcsdk

import (
	"testing"

	"github.com/odvcencio/buckley/pkg/orchestrator"
)

func TestPlanRequest(t *testing.T) {
	req := &PlanRequest{
		Feature:     "test-feature",
		Description: "test description",
	}

	if req.Feature != "test-feature" {
		t.Errorf("expected feature 'test-feature', got %q", req.Feature)
	}
	if req.Description != "test description" {
		t.Errorf("expected description 'test description', got %q", req.Description)
	}
}

func TestPlanResponse(t *testing.T) {
	plan := &orchestrator.Plan{ID: "test-plan"}
	resp := &PlanResponse{Plan: plan}

	if resp.Plan.ID != "test-plan" {
		t.Errorf("expected plan ID 'test-plan', got %q", resp.Plan.ID)
	}
}

func TestExecutePlanRequest(t *testing.T) {
	req := &ExecutePlanRequest{PlanID: "test-plan"}

	if req.PlanID != "test-plan" {
		t.Errorf("expected PlanID 'test-plan', got %q", req.PlanID)
	}
}

func TestExecutePlanResponse(t *testing.T) {
	resp := &ExecutePlanResponse{
		PlanID: "test-plan",
		Status: "completed",
	}

	if resp.PlanID != "test-plan" {
		t.Errorf("expected PlanID 'test-plan', got %q", resp.PlanID)
	}
	if resp.Status != "completed" {
		t.Errorf("expected status 'completed', got %q", resp.Status)
	}
}

func TestGetPlanRequest(t *testing.T) {
	req := &GetPlanRequest{PlanID: "test-plan"}

	if req.PlanID != "test-plan" {
		t.Errorf("expected PlanID 'test-plan', got %q", req.PlanID)
	}
}

func TestGetPlanResponse(t *testing.T) {
	plan := &orchestrator.Plan{ID: "test-plan"}
	resp := &GetPlanResponse{Plan: plan}

	if resp.Plan.ID != "test-plan" {
		t.Errorf("expected plan ID 'test-plan', got %q", resp.Plan.ID)
	}
}

func TestListPlansResponse(t *testing.T) {
	plans := []orchestrator.Plan{
		{ID: "plan1"},
		{ID: "plan2"},
	}
	resp := &ListPlansResponse{Plans: plans}

	if len(resp.Plans) != 2 {
		t.Errorf("expected 2 plans, got %d", len(resp.Plans))
	}
	if resp.Plans[0].ID != "plan1" {
		t.Errorf("expected first plan ID 'plan1', got %q", resp.Plans[0].ID)
	}
}
