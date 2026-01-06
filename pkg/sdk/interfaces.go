package sdk

import (
	"context"

	"github.com/odvcencio/buckley/pkg/orchestrator"
)

// Planner can generate high-level development plans for a feature.
type Planner interface {
	Plan(ctx context.Context, feature, description string) (*orchestrator.Plan, error)
}

// PlanLoader loads existing plans into the orchestrator.
type PlanLoader interface {
	LoadPlan(ctx context.Context, planID string) (*orchestrator.Plan, error)
	GetPlan(ctx context.Context, planID string) (*orchestrator.Plan, error)
	ListPlans(ctx context.Context) ([]orchestrator.Plan, error)
}

// Executor executes a loaded plan or the plan referenced by ID.
type Executor interface {
	ExecutePlan(ctx context.Context, planID string) error
}

// Service is the full SDK surface Buckley exposes today.
type Service interface {
	Planner
	PlanLoader
	Executor
}
