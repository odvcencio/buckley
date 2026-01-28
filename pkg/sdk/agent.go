package sdk

import (
	"context"
	"fmt"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/orchestrator"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/tool"
)

// Agent wraps the orchestrator so callers embedding Buckley programmatically can plan/execute features without the TUI.
type Agent struct {
	orchestrator *orchestrator.Orchestrator
}

// NewAgent constructs an SDK agent. Provide the same dependencies you would pass to orchestrator.NewOrchestrator.
func NewAgent(store *storage.Store, modelClient orchestrator.ModelClient, registry *tool.Registry, cfg *config.Config, workflow *orchestrator.WorkflowManager, planStore orchestrator.PlanStore) *Agent {
	orch := orchestrator.NewOrchestrator(store, modelClient, registry, cfg, workflow, planStore)
	return &Agent{orchestrator: orch}
}

// Orchestrator exposes the underlying orchestrator for advanced callers.
func (a *Agent) Orchestrator() *orchestrator.Orchestrator {
	return a.orchestrator
}

// Plan generates a plan for the given feature.
func (a *Agent) Plan(ctx context.Context, feature, description string) (*orchestrator.Plan, error) {
	if a.orchestrator == nil {
		return nil, fmt.Errorf("orchestrator not configured")
	}
	return a.orchestrator.PlanFeatureWithContext(ctx, feature, description)
}

// LoadPlan loads a plan into the orchestrator.
func (a *Agent) LoadPlan(ctx context.Context, planID string) (*orchestrator.Plan, error) {
	if a.orchestrator == nil {
		return nil, fmt.Errorf("orchestrator not configured")
	}
	return a.orchestrator.LoadPlan(planID)
}

// ExecutePlan ensures the referenced plan is loaded and runs it to completion.
func (a *Agent) ExecutePlan(ctx context.Context, planID string) error {
	if a.orchestrator == nil {
		return fmt.Errorf("orchestrator not configured")
	}
	if planID != "" {
		if _, err := a.orchestrator.LoadPlan(planID); err != nil {
			return err
		}
	}
	return a.orchestrator.ExecutePlanWithContext(ctx)
}

// ListPlans returns all persisted plans.
func (a *Agent) ListPlans(ctx context.Context) ([]orchestrator.Plan, error) {
	if a.orchestrator == nil {
		return nil, fmt.Errorf("orchestrator not configured")
	}
	return a.orchestrator.ListPlans()
}

// GetPlan loads a plan by ID.
func (a *Agent) GetPlan(ctx context.Context, planID string) (*orchestrator.Plan, error) {
	return a.LoadPlan(ctx, planID)
}
