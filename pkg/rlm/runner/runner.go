// Package runner provides plan execution using the RLM runtime.
//
// The RLM runner uses the coordinator/subagent architecture to execute plan tasks:
//   - Weight-based model routing for cost optimization
//   - Scratchpad for cross-task visibility
//   - Parallel task execution for independent tasks via delegate_batch
package runner

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/bus"
	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/orchestrator"
	"github.com/odvcencio/buckley/pkg/rlm"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/buckley/pkg/tool"
)

// Runner provides plan execution using the RLM runtime.
type Runner struct {
	mu sync.Mutex

	store     *storage.Store
	models    *model.Manager
	registry  *tool.Registry
	cfg       *config.Config
	workflow  *orchestrator.WorkflowManager
	planStore orchestrator.PlanStore
	telemetry *telemetry.Hub
	bus       bus.MessageBus

	runtime     *rlm.Runtime
	currentPlan *orchestrator.Plan
	planner     *orchestrator.Planner
}

// New constructs an RLM runner with the full runtime wired.
func New(store *storage.Store, mgr *model.Manager, registry *tool.Registry, cfg *config.Config, workflow *orchestrator.WorkflowManager, planStore orchestrator.PlanStore) *Runner {
	r := &Runner{
		store:     store,
		models:    mgr,
		registry:  registry,
		cfg:       cfg,
		workflow:  workflow,
		planStore: planStore,
	}

	// Create the planner for plan generation
	if store != nil && mgr != nil && cfg != nil {
		r.planner = orchestrator.NewPlanner(mgr, cfg, store, workflow, planStore)
	}

	// Initialize RLM runtime
	if err := r.initRuntime(); err != nil {
		// Log but continue - runtime will be initialized lazily
		fmt.Printf("Warning: failed to initialize RLM runtime: %v\n", err)
	}

	return r
}

// SetTelemetry configures telemetry for iteration events.
func (r *Runner) SetTelemetry(hub *telemetry.Hub) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.telemetry = hub
}

// SetBus configures the message bus for event broadcasting.
func (r *Runner) SetBus(b bus.MessageBus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bus = b
}

func (r *Runner) initRuntime() error {
	if r.models == nil {
		return fmt.Errorf("model manager required")
	}

	rlmCfg := rlm.DefaultConfig()
	if r.cfg != nil {
		// Apply config overrides if available
		if r.cfg.RLM.Coordinator.MaxIterations > 0 {
			rlmCfg.Coordinator.MaxIterations = r.cfg.RLM.Coordinator.MaxIterations
		}
		if r.cfg.RLM.Coordinator.MaxTokensBudget > 0 {
			rlmCfg.Coordinator.MaxTokensBudget = r.cfg.RLM.Coordinator.MaxTokensBudget
		}
		if r.cfg.RLM.Coordinator.ConfidenceThreshold > 0 {
			rlmCfg.Coordinator.ConfidenceThreshold = r.cfg.RLM.Coordinator.ConfidenceThreshold
		}
		if r.cfg.RLM.SubAgent.MaxConcurrent > 0 {
			rlmCfg.SubAgent.MaxConcurrent = r.cfg.RLM.SubAgent.MaxConcurrent
		}
	}

	runtime, err := rlm.NewRuntime(rlmCfg, rlm.RuntimeDeps{
		Models:    r.models,
		Store:     r.store,
		Registry:  r.registry,
		Bus:       r.bus,
		Telemetry: r.telemetry,
		UseToon:   r.cfg != nil && r.cfg.Encoding.UseToon,
	})
	if err != nil {
		return err
	}

	r.runtime = runtime
	return nil
}

func (r *Runner) ensureRuntime() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.runtime != nil {
		return nil
	}
	return r.initRuntime()
}

// PlanFeature creates a plan for a feature request.
func (r *Runner) PlanFeature(featureName, description string) (*orchestrator.Plan, error) {
	if r.planner == nil {
		return nil, fmt.Errorf("planner not initialized")
	}

	plan, err := r.planner.GeneratePlan(featureName, description)
	if err != nil {
		return nil, err
	}

	// Store the plan
	if r.planStore != nil {
		if err := r.planStore.SavePlan(plan); err != nil {
			return nil, fmt.Errorf("save plan: %w", err)
		}
	}

	r.mu.Lock()
	r.currentPlan = plan
	r.mu.Unlock()

	return plan, nil
}

// LoadPlan loads a plan into the runner.
func (r *Runner) LoadPlan(planID string) (*orchestrator.Plan, error) {
	if r.planStore == nil {
		return nil, fmt.Errorf("plan store not configured")
	}

	plan, err := r.planStore.LoadPlan(planID)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	r.currentPlan = plan
	r.mu.Unlock()

	return plan, nil
}

// GetCurrentPlan returns the currently loaded plan.
func (r *Runner) GetCurrentPlan() *orchestrator.Plan {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.currentPlan
}

// ExecutePlan runs all pending tasks in the current plan using the RLM runtime.
func (r *Runner) ExecutePlan() error {
	r.mu.Lock()
	plan := r.currentPlan
	r.mu.Unlock()

	if plan == nil {
		return fmt.Errorf("no plan loaded")
	}

	if err := r.ensureRuntime(); err != nil {
		return fmt.Errorf("initialize runtime: %w", err)
	}

	// Find pending tasks
	var pendingTasks []orchestrator.Task
	for i := range plan.Tasks {
		if plan.Tasks[i].Status == orchestrator.TaskPending {
			pendingTasks = append(pendingTasks, plan.Tasks[i])
		}
	}

	if len(pendingTasks) == 0 {
		return nil // All tasks complete
	}

	// Group tasks by dependencies for parallel execution
	independent, dependent := r.partitionTasks(pendingTasks)

	// Execute independent tasks in parallel via the RLM coordinator
	if len(independent) > 0 {
		if err := r.executeTaskBatch(plan, independent); err != nil {
			return err
		}
	}

	// Execute dependent tasks sequentially
	for _, task := range dependent {
		if err := r.executeSingleTask(plan, &task); err != nil {
			// Mark task as failed but continue with others
			r.updateTaskStatus(plan, task.ID, orchestrator.TaskFailed)
		}
	}

	// Save updated plan
	if r.planStore != nil {
		if err := r.planStore.SavePlan(plan); err != nil {
			return fmt.Errorf("save plan: %w", err)
		}
	}

	return nil
}

// ExecuteTask runs a single task by ID.
func (r *Runner) ExecuteTask(taskID string) error {
	r.mu.Lock()
	plan := r.currentPlan
	r.mu.Unlock()

	if plan == nil {
		return fmt.Errorf("no plan loaded")
	}

	if err := r.ensureRuntime(); err != nil {
		return fmt.Errorf("initialize runtime: %w", err)
	}

	// Find the task
	var task *orchestrator.Task
	for i := range plan.Tasks {
		if plan.Tasks[i].ID == taskID {
			task = &plan.Tasks[i]
			break
		}
	}

	if task == nil {
		return fmt.Errorf("task not found: %s", taskID)
	}

	if err := r.executeSingleTask(plan, task); err != nil {
		r.updateTaskStatus(plan, taskID, orchestrator.TaskFailed)
		return err
	}

	// Save updated plan
	if r.planStore != nil {
		if err := r.planStore.SavePlan(plan); err != nil {
			return fmt.Errorf("save plan: %w", err)
		}
	}

	return nil
}

// partitionTasks separates tasks into independent (no pending dependencies) and dependent.
func (r *Runner) partitionTasks(tasks []orchestrator.Task) (independent, dependent []orchestrator.Task) {
	r.mu.Lock()
	plan := r.currentPlan
	r.mu.Unlock()

	// Build set of pending task IDs
	pendingIDs := make(map[string]bool)
	for _, t := range tasks {
		pendingIDs[t.ID] = true
	}

	// Also check completed tasks in the plan
	completedIDs := make(map[string]bool)
	if plan != nil {
		for _, t := range plan.Tasks {
			if t.Status == orchestrator.TaskCompleted {
				completedIDs[t.ID] = true
			}
		}
	}

	for _, t := range tasks {
		hasUnmetDeps := false
		for _, depID := range t.Dependencies {
			// Dependency is unmet if it's pending (not yet executed)
			if pendingIDs[depID] && !completedIDs[depID] {
				hasUnmetDeps = true
				break
			}
		}

		if hasUnmetDeps {
			dependent = append(dependent, t)
		} else {
			independent = append(independent, t)
		}
	}

	return independent, dependent
}

// executeTaskBatch runs multiple independent tasks using the RLM coordinator.
func (r *Runner) executeTaskBatch(plan *orchestrator.Plan, tasks []orchestrator.Task) error {
	if len(tasks) == 0 {
		return nil
	}

	// Build a composite task description for the coordinator
	var sb strings.Builder
	sb.WriteString("Execute the following independent tasks in parallel:\n\n")

	for i, task := range tasks {
		sb.WriteString(fmt.Sprintf("## Task %d: %s\n", i+1, task.Title))
		sb.WriteString(fmt.Sprintf("ID: %s\n", task.ID))
		sb.WriteString(fmt.Sprintf("Type: %s\n", task.Type))
		sb.WriteString(fmt.Sprintf("Description: %s\n", task.Description))
		if len(task.Files) > 0 {
			sb.WriteString(fmt.Sprintf("Files: %s\n", strings.Join(task.Files, ", ")))
		}
		if len(task.Verification) > 0 {
			sb.WriteString(fmt.Sprintf("Verification: %s\n", strings.Join(task.Verification, "; ")))
		}
		sb.WriteString("\n")

		// Mark as in progress
		r.updateTaskStatus(plan, task.ID, orchestrator.TaskInProgress)
	}

	sb.WriteString("\nFor each task, use delegate_batch with appropriate weights based on task type:\n")
	sb.WriteString("- implementation tasks: weight=medium or heavy\n")
	sb.WriteString("- analysis tasks: weight=light or medium\n")
	sb.WriteString("- validation tasks: weight=light\n")
	sb.WriteString("\nReport completion status for each task in your final answer.")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	answer, err := r.runtime.Execute(ctx, sb.String())
	if err != nil {
		// Mark all tasks as failed
		for _, task := range tasks {
			r.updateTaskStatus(plan, task.ID, orchestrator.TaskFailed)
		}
		return fmt.Errorf("RLM execution failed: %w", err)
	}

	// Parse answer to determine which tasks succeeded
	// For now, mark all as completed if we got a confident answer
	if answer != nil && answer.Ready && answer.Confidence >= 0.7 {
		for _, task := range tasks {
			r.updateTaskStatus(plan, task.ID, orchestrator.TaskCompleted)
		}
	} else {
		// Mark as failed if confidence is low
		for _, task := range tasks {
			r.updateTaskStatus(plan, task.ID, orchestrator.TaskFailed)
		}
	}

	return nil
}

// executeSingleTask runs a single task using the RLM runtime.
func (r *Runner) executeSingleTask(plan *orchestrator.Plan, task *orchestrator.Task) error {
	r.updateTaskStatus(plan, task.ID, orchestrator.TaskInProgress)

	// Build task prompt with context
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# Task: %s\n\n", task.Title))
	sb.WriteString(fmt.Sprintf("**Type**: %s\n", task.Type))
	sb.WriteString(fmt.Sprintf("**Description**: %s\n\n", task.Description))

	if len(task.Files) > 0 {
		sb.WriteString(fmt.Sprintf("**Target Files**: %s\n\n", strings.Join(task.Files, ", ")))
	}

	if len(task.Verification) > 0 {
		sb.WriteString("**Verification Steps**:\n")
		for _, v := range task.Verification {
			sb.WriteString(fmt.Sprintf("- %s\n", v))
		}
		sb.WriteString("\n")
	}

	// Add weight guidance based on task type
	weight := r.taskTypeToWeight(task.Type)
	sb.WriteString(fmt.Sprintf("Use weight=%s for sub-agent delegation.\n", weight))
	sb.WriteString("Complete the task and verify the verification steps pass.\n")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	answer, err := r.runtime.Execute(ctx, sb.String())
	if err != nil {
		return err
	}

	// Determine success based on answer
	if answer != nil && answer.Ready && answer.Confidence >= 0.7 {
		r.updateTaskStatus(plan, task.ID, orchestrator.TaskCompleted)
		return nil
	}

	return fmt.Errorf("task execution did not reach confidence threshold")
}

// taskTypeToWeight maps orchestrator task types to RLM weights.
func (r *Runner) taskTypeToWeight(taskType orchestrator.TaskType) string {
	switch taskType {
	case orchestrator.TaskTypeImplementation:
		return "medium" // Code changes need balanced model
	case orchestrator.TaskTypeAnalysis:
		return "light" // Analysis can use faster models
	case orchestrator.TaskTypeValidation:
		return "light" // Validation is typically straightforward
	default:
		return "medium"
	}
}

// updateTaskStatus updates a task's status in the plan.
func (r *Runner) updateTaskStatus(plan *orchestrator.Plan, taskID string, status orchestrator.TaskStatus) {
	if plan == nil {
		return
	}

	for i := range plan.Tasks {
		if plan.Tasks[i].ID == taskID {
			plan.Tasks[i].Status = status
			break
		}
	}

	// Emit telemetry event
	if r.telemetry != nil {
		eventType := telemetry.EventTaskStarted
		switch status {
		case orchestrator.TaskCompleted:
			eventType = telemetry.EventTaskCompleted
		case orchestrator.TaskFailed:
			eventType = telemetry.EventTaskFailed
		case orchestrator.TaskInProgress:
			eventType = telemetry.EventTaskStarted
		}
		r.telemetry.Publish(telemetry.Event{
			Type:      eventType,
			SessionID: plan.ID,
			Data: map[string]any{
				"task_id": taskID,
			},
		})
	}
}

// ListPlans returns all saved plans.
func (r *Runner) ListPlans() ([]*orchestrator.Plan, error) {
	if r.planStore == nil {
		return nil, fmt.Errorf("plan store not configured")
	}
	plans, err := r.planStore.ListPlans()
	if err != nil {
		return nil, err
	}
	// Convert []Plan to []*Plan
	result := make([]*orchestrator.Plan, len(plans))
	for i := range plans {
		result[i] = &plans[i]
	}
	return result, nil
}

// ResumeFeature loads a plan by ID and sets it as current.
func (r *Runner) ResumeFeature(planID string) error {
	_, err := r.LoadPlan(planID)
	return err
}
