package orchestrator

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/gts"
	"github.com/odvcencio/buckley/pkg/personality"
	"github.com/odvcencio/buckley/pkg/rules"
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
	engine           *rules.Engine
	gtsPipeline      *gts.Pipeline

	currentPlan *Plan
	executor    *Executor
	cancelPlan  context.CancelFunc
}

// GetWorkflow returns the workflow manager
func (o *Orchestrator) GetWorkflow() *WorkflowManager {
	if o == nil {
		return nil
	}
	return o.workflow
}

// GTSPipeline returns the GTS context pipeline, if configured.
func (o *Orchestrator) GTSPipeline() *gts.Pipeline {
	if o == nil {
		return nil
	}
	return o.gtsPipeline
}

// enrichWithGTS runs the GTS context pipeline and formats the results as a
// markdown section suitable for appending to an LLM prompt. Returns an empty
// string when the pipeline is nil or enrichment fails.
func (o *Orchestrator) enrichWithGTS(ctx context.Context, taskType string, files []string) string {
	if o == nil || o.gtsPipeline == nil {
		return ""
	}

	facts := rules.GTSFacts{
		TaskType:  taskType,
		FileCount: len(files),
	}

	enrichment, err := o.gtsPipeline.Enrich(ctx, facts, files)
	if err != nil || enrichment == nil {
		return ""
	}

	return formatEnrichment(enrichment)
}

// formatEnrichment converts a ContextEnrichment into a markdown section.
func formatEnrichment(e *gts.ContextEnrichment) string {
	if e == nil {
		return ""
	}

	var b strings.Builder

	if len(e.Map) > 0 {
		b.WriteString("## Code Intelligence\n\n")
		b.WriteString("### File Structure\n")
		for _, m := range e.Map {
			b.WriteString(fmt.Sprintf("- **%s**", m.File))
			if len(m.Symbols) > 0 {
				names := make([]string, 0, len(m.Symbols))
				for _, s := range m.Symbols {
					names = append(names, fmt.Sprintf("%s (%s)", s.Name, s.Kind))
				}
				b.WriteString(": " + strings.Join(names, ", "))
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if e.Scope != nil && len(e.Scope.InScope) > 0 {
		b.WriteString("### Scope\n")
		b.WriteString(fmt.Sprintf("At %s:%d:\n", e.Scope.File, e.Scope.Line))
		for _, s := range e.Scope.InScope {
			b.WriteString(fmt.Sprintf("- %s (%s) at %s:%d\n", s.Name, s.Kind, s.File, s.Line))
		}
		b.WriteString("\n")
	}

	if e.Callgraph != nil && len(e.Callgraph.Edges) > 0 {
		b.WriteString("### Call Graph\n")
		b.WriteString(fmt.Sprintf("Root: %s (depth %d)\n", e.Callgraph.Root, e.Callgraph.Depth))
		for _, edge := range e.Callgraph.Edges {
			b.WriteString(fmt.Sprintf("- %s -> %s\n", edge.Caller, edge.Callee))
		}
		b.WriteString("\n")
	}

	if len(e.DeadCode) > 0 {
		b.WriteString("### Dead Code\n")
		for _, s := range e.DeadCode {
			b.WriteString(fmt.Sprintf("- %s (%s) at %s:%d\n", s.Name, s.Kind, s.File, s.Line))
		}
		b.WriteString("\n")
	}

	if len(e.Impact) > 0 {
		b.WriteString("### Impact\n")
		for _, s := range e.Impact {
			b.WriteString(fmt.Sprintf("- %s (%s) at %s:%d\n", s.Name, s.Kind, s.File, s.Line))
		}
		b.WriteString("\n")
	}

	if e.Degraded && b.Len() > 0 {
		b.WriteString("*Note: Code intelligence results may be incomplete (degraded mode).*\n\n")
	}

	return b.String()
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
func NewOrchestrator(store *storage.Store, mgr ModelClient, registry *tool.Registry, cfg *config.Config, workflow *WorkflowManager, planStore PlanStore, engine *rules.Engine, pipeline *gts.Pipeline) *Orchestrator {
	var batchCoordinator *BatchCoordinator
	if cfg != nil && cfg.Batch.Enabled {
		if bc, err := NewBatchCoordinator(cfg.Batch, workflow); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to initialize batch coordinator: %v\n", err)
		} else {
			batchCoordinator = bc
		}
	}

	return &Orchestrator{
		planner:          NewPlanner(mgr, cfg, store, workflow, planStore),
		commitGenerator:  NewCommitGenerator(mgr, cfg),
		prCreator:        NewPRCreator(mgr, cfg),
		store:            store,
		modelClient:      mgr,
		toolRegistry:     registry,
		config:           cfg,
		workflow:         workflow,
		batchCoordinator: batchCoordinator,
		engine:           engine,
		gtsPipeline:      pipeline,
	}
}

// PlanFeature creates a feature plan
func (o *Orchestrator) PlanFeature(featureName, description string) (*Plan, error) {
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
	if o.currentPlan == nil {
		return fmt.Errorf("no plan loaded")
	}

	// Create executor with cancelable context
	ctx := context.Background()
	if o.cancelPlan != nil {
		o.cancelPlan() // cancel any previous run
	}
	ctx, cancel := context.WithCancel(ctx)
	o.cancelPlan = cancel
	o.executor = NewExecutor(o.currentPlan, o.store, o.modelClient, o.toolRegistry, o.config, o.planner, o.workflow, o.batchCoordinator, o.engine)
	o.executor.SetContext(ctx)
	if o.gtsPipeline != nil && o.executor.builder != nil {
		o.executor.builder.SetEnricher(o.enrichWithGTS)
	}

	// Execute all tasks
	if err := o.executor.Execute(); err != nil {
		return fmt.Errorf("execution failed: %w", err)
	}

	return nil
}

// ExecuteTask executes a single task
func (o *Orchestrator) ExecuteTask(taskID string) error {
	if o.currentPlan == nil {
		return fmt.Errorf("no plan loaded")
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
		ctx := context.Background()
		if o.cancelPlan != nil {
			o.cancelPlan()
		}
		ctx, cancel := context.WithCancel(ctx)
		o.cancelPlan = cancel
		o.executor = NewExecutor(o.currentPlan, o.store, o.modelClient, o.toolRegistry, o.config, o.planner, o.workflow, o.batchCoordinator, o.engine)
		o.executor.SetContext(ctx)
		if o.gtsPipeline != nil && o.executor.builder != nil {
			o.executor.builder.SetEnricher(o.enrichWithGTS)
		}
	}

	// Execute task
	return o.executor.executeTask(task)
}

// GenerateCommit generates a commit for a task
func (o *Orchestrator) GenerateCommit(taskID string) (*CommitInfo, error) {
	if o.currentPlan == nil {
		return nil, fmt.Errorf("no plan loaded")
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
}

// CreateCommit creates a commit for a task
func (o *Orchestrator) CreateCommit(taskID string) error {
	commit, err := o.GenerateCommit(taskID)
	if err != nil {
		return err
	}

	return o.commitGenerator.Commit(commit)
}

// CreatePR creates a pull request for the feature
func (o *Orchestrator) CreatePR() (*PRInfo, error) {
	if o.currentPlan == nil {
		return nil, fmt.Errorf("no plan loaded")
	}

	// Generate PR
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
