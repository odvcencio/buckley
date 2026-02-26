package orchestrator

import (
	"context"
	"fmt"
	"os"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/personality"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/buckley/pkg/tool"
)

// Orchestrator manages the complete feature development workflow
type Orchestrator struct {
	planner          *Planner
	commitGenerator  *CommitGenerator
	prCreator        *PRCreator
	store            *storage.Store
	modelClient      ModelClient
	toolRegistry     *tool.Registry
	config           *config.Config
	workflow         *WorkflowManager
	batchCoordinator *BatchCoordinator

	currentPlan *Plan
	executor    *Executor
	cancelPlan  context.CancelFunc
	ctx         context.Context
	cancel      context.CancelFunc
}

// GetWorkflow returns the workflow manager
func (o *Orchestrator) GetWorkflow() *WorkflowManager {
	if o == nil {
		return nil
	}
	return o.workflow
}

// RefreshPersonaProvider propagates persona updates to planner/executor components.
func (o *Orchestrator) RefreshPersonaProvider(provider *personality.PersonaProvider) {
	if o == nil || provider == nil {
		return
	}
	if o.planner != nil {
		o.planner.SetPersonaProvider(provider)
	}
	if o.executor != nil {
		o.executor.SetPersonaProvider(provider)
	}
}

// NewOrchestrator creates a new orchestrator
func NewOrchestrator(store *storage.Store, mgr ModelClient, registry *tool.Registry, cfg *config.Config, workflow *WorkflowManager, planStore PlanStore) *Orchestrator {
	var batchCoordinator *BatchCoordinator
	if cfg != nil && cfg.Batch.Enabled {
		if bc, err := NewBatchCoordinator(cfg.Batch, workflow); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to initialize batch coordinator: %v\n", err)
		} else {
			batchCoordinator = bc
		}
	}

	baseCtx := context.Background()
	ctx, cancel := context.WithCancel(baseCtx)
	orch := &Orchestrator{
		planner:          NewPlanner(mgr, cfg, store, workflow, planStore),
		commitGenerator:  NewCommitGenerator(mgr, cfg),
		prCreator:        NewPRCreator(mgr, cfg),
		store:            store,
		modelClient:      mgr,
		toolRegistry:     registry,
		config:           cfg,
		workflow:         workflow,
		batchCoordinator: batchCoordinator,
		ctx:              ctx,
		cancel:           cancel,
	}
	orch.setSubcomponentContexts(ctx)
	return orch
}

func (o *Orchestrator) baseContext() context.Context {
	if o == nil || o.ctx == nil {
		return context.Background()
	}
	return o.ctx
}

func (o *Orchestrator) setSubcomponentContexts(ctx context.Context) {
	if ctx == nil {
		return
	}
	if o.planner != nil {
		o.planner.SetContext(ctx)
	}
	if o.commitGenerator != nil {
		o.commitGenerator.SetContext(ctx)
	}
	if o.prCreator != nil {
		o.prCreator.SetContext(ctx)
	}
	if o.executor != nil {
		o.executor.SetContext(ctx)
	}
}

// SetContext updates the base context for orchestrator operations.
func (o *Orchestrator) SetContext(ctx context.Context) {
	if o == nil || ctx == nil {
		return
	}
	if o.cancel != nil {
		o.cancel()
	}
	o.ctx, o.cancel = context.WithCancel(ctx)
	o.setSubcomponentContexts(o.ctx)
}

// PlanFeature creates a feature plan
func (o *Orchestrator) PlanFeature(featureName, description string) (*Plan, error) {
	return o.PlanFeatureWithContext(o.baseContext(), featureName, description)
}

// PlanFeatureWithContext creates a feature plan using the provided context.
func (o *Orchestrator) PlanFeatureWithContext(ctx context.Context, featureName, description string) (*Plan, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if o.planner != nil {
		o.planner.SetContext(ctx)
	}
	plan, err := o.planner.GeneratePlan(featureName, description)
	if err != nil {
		return nil, fmt.Errorf("failed to generate plan: %w", err)
	}

	if o.workflow != nil {
		o.workflow.EnrichPlan(plan)
	}

	// Save plan
	if err := o.planner.SavePlan(plan); err != nil {
		return nil, fmt.Errorf("failed to save plan: %w", err)
	}

	if o.workflow != nil {
		o.workflow.SendProgress(fmt.Sprintf("🗂️ Plan saved to docs/plans/%s.md", plan.ID))
	}

	o.currentPlan = plan
	if o.workflow != nil {
		o.workflow.SetCurrentPlan(plan)
		o.workflow.EmitPlanSnapshot(plan, telemetry.EventPlanCreated)
	}
	return plan, nil
}

// LoadPlan loads an existing plan
func (o *Orchestrator) LoadPlan(planID string) (*Plan, error) {
	plan, err := o.planner.LoadPlan(planID)
	if err != nil {
		return nil, err
	}

	o.currentPlan = plan
	if o.workflow != nil {
		o.workflow.SetCurrentPlan(plan)
		o.workflow.EmitPlanSnapshot(plan, telemetry.EventPlanUpdated)
	}
	return plan, nil
}

// ListPlans returns all available plans
func (o *Orchestrator) ListPlans() ([]Plan, error) {
	return o.planner.ListPlans()
}

// ExecutePlan executes the current plan
func (o *Orchestrator) ExecutePlan() error {
	return o.ExecutePlanWithContext(o.baseContext())
}

// ExecutePlanWithContext executes the current plan with the provided context.
func (o *Orchestrator) ExecutePlanWithContext(ctx context.Context) error {
	if o.currentPlan == nil {
		return fmt.Errorf("no plan loaded")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// Create executor with cancelable context
	if o.cancelPlan != nil {
		o.cancelPlan() // cancel any previous run
	}
	execCtx, cancel := context.WithCancel(ctx)
	o.cancelPlan = cancel
	o.executor = NewExecutor(o.currentPlan, o.store, o.modelClient, o.toolRegistry, o.config, o.planner, o.workflow, o.batchCoordinator)
	o.executor.SetContext(execCtx)

	// Execute all tasks
	if err := o.executor.Execute(); err != nil {
		return fmt.Errorf("execution failed: %w", err)
	}

	return nil
}

// ExecuteTask executes a single task
func (o *Orchestrator) ExecuteTask(taskID string) error {
	return o.ExecuteTaskWithContext(o.baseContext(), taskID)
}

// ExecuteTaskWithContext executes a single task with the provided context.
func (o *Orchestrator) ExecuteTaskWithContext(ctx context.Context, taskID string) error {
	if o.currentPlan == nil {
		return fmt.Errorf("no plan loaded")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// Find task
	var task *Task
	for i := range o.currentPlan.Tasks {
		if o.currentPlan.Tasks[i].ID == taskID {
			task = &o.currentPlan.Tasks[i]
			break
		}
	}

	if task == nil {
		return fmt.Errorf("task %s not found", taskID)
	}

	// Create executor if needed
	if o.executor == nil {
		if o.cancelPlan != nil {
			o.cancelPlan()
		}
		execCtx, cancel := context.WithCancel(ctx)
		o.cancelPlan = cancel
		o.executor = NewExecutor(o.currentPlan, o.store, o.modelClient, o.toolRegistry, o.config, o.planner, o.workflow, o.batchCoordinator)
		o.executor.SetContext(execCtx)
	}

	// Execute task
	return o.executor.executeTask(task)
}

// GenerateCommit generates a commit for a task
func (o *Orchestrator) GenerateCommit(taskID string) (*CommitInfo, error) {
	return o.GenerateCommitWithContext(o.baseContext(), taskID)
}

// GenerateCommitWithContext generates a commit for a task using the provided context.
func (o *Orchestrator) GenerateCommitWithContext(ctx context.Context, taskID string) (*CommitInfo, error) {
	if o.currentPlan == nil {
		return nil, fmt.Errorf("no plan loaded")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// Find task
	var task *Task
	for i := range o.currentPlan.Tasks {
		if o.currentPlan.Tasks[i].ID == taskID {
			task = &o.currentPlan.Tasks[i]
			break
		}
	}

	if task == nil {
		return nil, fmt.Errorf("task %s not found", taskID)
	}
	if o.commitGenerator != nil {
		o.commitGenerator.SetContext(ctx)
	}
	return o.commitGenerator.Generate(task)
}

// Cancel stops the current execution, if any.
func (o *Orchestrator) Cancel() {
	if o.cancelPlan != nil {
		o.cancelPlan()
	}
	if o.executor != nil {
		o.executor.Cancel()
	}
	if o.cancel != nil {
		o.cancel()
	}
}

// CreateCommit creates a commit for a task
func (o *Orchestrator) CreateCommit(taskID string) error {
	return o.CreateCommitWithContext(o.baseContext(), taskID)
}

// CreateCommitWithContext creates a commit for a task using the provided context.
func (o *Orchestrator) CreateCommitWithContext(ctx context.Context, taskID string) error {
	commit, err := o.GenerateCommitWithContext(ctx, taskID)
	if err != nil {
		return err
	}

	return o.commitGenerator.Commit(commit)
}

// CreatePR creates a pull request for the feature
func (o *Orchestrator) CreatePR() (*PRInfo, error) {
	return o.CreatePRWithContext(o.baseContext())
}

// CreatePRWithContext creates a pull request for the feature using the provided context.
func (o *Orchestrator) CreatePRWithContext(ctx context.Context) (*PRInfo, error) {
	if o.currentPlan == nil {
		return nil, fmt.Errorf("no plan loaded")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// Generate PR
	if o.prCreator != nil {
		o.prCreator.SetContext(ctx)
	}
	pr, err := o.prCreator.GeneratePR(o.currentPlan)
	if err != nil {
		return nil, fmt.Errorf("failed to generate PR: %w", err)
	}

	// Create PR
	if err := o.prCreator.CreatePR(pr); err != nil {
		return nil, fmt.Errorf("failed to create PR: %w", err)
	}

	return pr, nil
}

// GetProgress returns execution progress
func (o *Orchestrator) GetProgress() (completed int, total int) {
	if o.executor == nil {
		return 0, 0
	}
	return o.executor.GetProgress()
}

// GetCurrentPlan returns the current plan
func (o *Orchestrator) GetCurrentPlan() *Plan {
	return o.currentPlan
}

// HasPendingTasks returns whether there are tasks still in progress/pending
func (o *Orchestrator) HasPendingTasks() bool {
	if o.currentPlan == nil {
		return false
	}
	for _, task := range o.currentPlan.Tasks {
		if task.Status != TaskCompleted && task.Status != TaskSkipped {
			return true
		}
	}
	return false
}

// GetPlanStatus returns a summary of the plan status
func (o *Orchestrator) GetPlanStatus() string {
	if o.currentPlan == nil {
		return "No active plan"
	}

	completed, total := 0, len(o.currentPlan.Tasks)
	for _, task := range o.currentPlan.Tasks {
		if task.Status == TaskCompleted {
			completed++
		}
	}

	return fmt.Sprintf("Plan: %s (%d/%d tasks completed)", o.currentPlan.FeatureName, completed, total)
}

// ResumeFeature resumes work on a feature
func (o *Orchestrator) ResumeFeature(planID string) error {
	plan, err := o.LoadPlan(planID)
	if err != nil {
		return err
	}

	o.currentPlan = plan
	return nil
}
