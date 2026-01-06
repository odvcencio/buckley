package grpcsdk

import "github.com/odvcencio/buckley/pkg/orchestrator"

// PlanRequest contains inputs for creating a plan.
type PlanRequest struct {
	Feature     string `json:"feature"`
	Description string `json:"description"`
}

// PlanResponse returns a generated plan.
type PlanResponse struct {
	Plan *orchestrator.Plan `json:"plan"`
}

// ExecutePlanRequest requests execution of a plan.
type ExecutePlanRequest struct {
	PlanID string `json:"plan_id"`
}

// ExecutePlanResponse indicates plan execution status.
type ExecutePlanResponse struct {
	PlanID string `json:"plan_id"`
	Status string `json:"status"`
}

// GetPlanRequest loads a persisted plan.
type GetPlanRequest struct {
	PlanID string `json:"plan_id"`
}

// GetPlanResponse returns a plan.
type GetPlanResponse struct {
	Plan *orchestrator.Plan `json:"plan"`
}

// ListPlansResponse lists available plans.
type ListPlansResponse struct {
	Plans []orchestrator.Plan `json:"plans"`
}
