// Package runner provides plan execution using Buckley's coordinator–worker runtime.
//
// The runner uses coordinated execution to execute plan tasks:
//   - Weight-based model routing for cost optimization
//   - Scratchpad for cross-task visibility
//   - Parallel task execution for independent tasks via delegate_batch
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"m31labs.dev/buckley/pkg/bus"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/graft"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/orchestrator"
	"m31labs.dev/buckley/pkg/rlm"
	"m31labs.dev/buckley/pkg/rlm/configadapter"
	"m31labs.dev/buckley/pkg/rules"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/telemetry"
	"m31labs.dev/buckley/pkg/tool"
)

// Runner provides plan execution using the coordinator–worker runtime.
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
	engine    *rules.Engine

	runtime          *rlm.Runtime
	currentPlan      *orchestrator.Plan
	planner          *orchestrator.Planner
	graftClient      *graft.Client
	verificationHost taskVerificationHost
}

// Option configures a Runner before its runtime is initialized.
type Option func(*Runner)

// WithRulesEngine supplies the optional Arbiter engine used by coordinated
// execution policy hooks. A nil engine preserves the legacy fail-open behavior.
func WithRulesEngine(engine *rules.Engine) Option {
	return func(r *Runner) {
		r.engine = engine
	}
}

// WithGraftClient supplies the optional Graft client before the runtime is
// initialized.
func WithGraftClient(client *graft.Client) Option {
	return func(r *Runner) {
		r.graftClient = client
	}
}

// New constructs a runner with the full coordinator–worker runtime wired.
func New(store *storage.Store, mgr *model.Manager, registry *tool.Registry, cfg *config.Config, workflow *orchestrator.WorkflowManager, planStore orchestrator.PlanStore, opts ...Option) *Runner {
	r := &Runner{
		store:     store,
		models:    mgr,
		registry:  registry,
		cfg:       cfg,
		workflow:  workflow,
		planStore: planStore,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}

	// Create the planner for plan generation
	if store != nil && mgr != nil && cfg != nil {
		r.planner = orchestrator.NewPlanner(mgr, cfg, store, workflow, planStore)
	}

	// Initialize the coordinator–worker runtime.
	if err := r.initRuntime(); err != nil {
		// Log but continue - runtime will be initialized lazily
		fmt.Printf("Warning: failed to initialize coordinated execution runtime: %v\n", err)
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

// SetGraftClient stores the Graft client for a runtime that has not yet been
// initialized. It remains for legacy callers, but does not rebind an already
// initialized runtime; use WithGraftClient when constructing an eager runner.
func (r *Runner) SetGraftClient(client *graft.Client) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.graftClient = client
}

func (r *Runner) initRuntime() error {
	if r.models == nil {
		return fmt.Errorf("model manager required")
	}

	rlmCfg := runnerRLMConfig(r.cfg)

	runtime, err := rlm.NewRuntime(rlmCfg, rlm.RuntimeDeps{
		Models:      r.models,
		Store:       r.store,
		Registry:    r.registry,
		Bus:         r.bus,
		Telemetry:   r.telemetry,
		UseToon:     r.cfg != nil && r.cfg.Encoding.UseToon,
		Engine:      r.engine,
		GraftClient: r.graftClient,
	})
	if err != nil {
		return err
	}

	r.runtime = runtime
	return nil
}

func runnerRLMConfig(cfg *config.Config) rlm.Config {
	return configadapter.Resolve(cfg)
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

// ExecutePlan runs all pending tasks in the current plan using coordinated execution.
func (r *Runner) ExecutePlan() error {
	r.mu.Lock()
	plan := r.currentPlan
	r.mu.Unlock()

	if plan == nil {
		return fmt.Errorf("no plan loaded")
	}

	if err := validatePlanTaskIDs(plan); err != nil {
		return err
	}

	if err := r.ensureRuntime(); err != nil {
		return fmt.Errorf("initialize runtime: %w", err)
	}

	var runErr error
	for {
		ready, blocked := r.readyPendingTasks(plan)
		if len(ready) == 0 {
			if len(blocked) == 0 {
				runErr = errors.Join(runErr, planTerminalIncompleteError(plan))
				break
			}
			for _, task := range blocked {
				r.updateTaskStatus(plan, task.ID, orchestrator.TaskFailed)
				runErr = errors.Join(runErr, fmt.Errorf("task %s dependencies are not completed", task.ID))
			}
			break
		}
		if err := r.executeTaskBatch(plan, ready); err != nil {
			runErr = errors.Join(runErr, err)
		}
		if !r.hasPendingTasks(plan) {
			break
		}
	}
	runErr = errors.Join(runErr, planTerminalIncompleteError(plan))

	// Save updated plan
	if r.planStore != nil {
		if err := r.planStore.SavePlan(plan); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("save plan: %w", err))
		}
	}

	return runErr
}

// ExecuteTask runs a single task by ID.
func (r *Runner) ExecuteTask(taskID string) error {
	r.mu.Lock()
	plan := r.currentPlan
	r.mu.Unlock()

	if plan == nil {
		return fmt.Errorf("no plan loaded")
	}

	if err := validatePlanTaskIDs(plan); err != nil {
		return err
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
	if !r.taskDependenciesCompleted(plan, *task) {
		return fmt.Errorf("task %s dependencies are not completed", task.ID)
	}

	if err := r.executeSingleTask(plan, task); err != nil {
		return err
	}

	return nil
}

// partitionTasks separates tasks into independent (no pending dependencies) and dependent.
func (r *Runner) partitionTasks(tasks []orchestrator.Task) (independent, dependent []orchestrator.Task) {
	r.mu.Lock()
	plan := r.currentPlan
	r.mu.Unlock()

	for _, t := range tasks {
		if len(t.Dependencies) == 0 || (plan != nil && r.taskDependenciesCompleted(plan, t)) {
			independent = append(independent, t)
		} else {
			dependent = append(dependent, t)
		}
	}

	return independent, dependent
}

func (r *Runner) readyPendingTasks(plan *orchestrator.Plan) (ready, blocked []orchestrator.Task) {
	if plan == nil {
		return nil, nil
	}
	for _, task := range plan.Tasks {
		if task.Status != orchestrator.TaskPending {
			continue
		}
		if r.taskDependenciesCompleted(plan, task) {
			ready = append(ready, task)
		} else {
			blocked = append(blocked, task)
		}
	}
	return ready, blocked
}

func (r *Runner) hasPendingTasks(plan *orchestrator.Plan) bool {
	if plan == nil {
		return false
	}
	for _, task := range plan.Tasks {
		if task.Status == orchestrator.TaskPending {
			return true
		}
	}
	return false
}

// executeTaskBatch runs multiple independent tasks using host-bound dispatch.
func (r *Runner) executeTaskBatch(plan *orchestrator.Plan, tasks []orchestrator.Task) error {
	if len(tasks) == 0 {
		return nil
	}

	subtasks := make([]rlm.SubTask, 0, len(tasks))
	for _, task := range tasks {
		r.updateTaskStatus(plan, task.ID, orchestrator.TaskInProgress)
		subtasks = append(subtasks, r.subTaskForPlanTask(task))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	results, execErr := r.runtime.ExecuteTasks(ctx, rlm.BatchRequest{Tasks: subtasks, Parallel: true})
	statusErr := r.applyTaskResults(ctx, plan, tasks, results)
	var saveErr error
	if r.planStore != nil {
		if err := r.planStore.SavePlan(plan); err != nil {
			saveErr = fmt.Errorf("save plan: %w", err)
		}
	}
	return errors.Join(execErr, statusErr, saveErr)
}

// executeSingleTask runs a single task using coordinated execution.
func (r *Runner) executeSingleTask(plan *orchestrator.Plan, task *orchestrator.Task) error {
	r.updateTaskStatus(plan, task.ID, orchestrator.TaskInProgress)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	results, execErr := r.runtime.ExecuteTasks(ctx, rlm.BatchRequest{
		Tasks:    []rlm.SubTask{r.subTaskForPlanTask(*task)},
		Parallel: false,
	})
	statusErr := r.applyTaskResults(ctx, plan, []orchestrator.Task{*task}, results)
	var saveErr error
	if r.planStore != nil {
		if err := r.planStore.SavePlan(plan); err != nil {
			saveErr = fmt.Errorf("save plan: %w", err)
		}
	}
	return errors.Join(execErr, statusErr, saveErr)
}

func (r *Runner) subTaskForPlanTask(task orchestrator.Task) rlm.SubTask {
	return rlm.SubTask{
		ID:     task.ID,
		Prompt: r.renderTaskPrompt(task),
		Weight: rlm.Weight(r.taskTypeToWeight(task.Type)),
	}
}

func (r *Runner) renderTaskPrompt(task orchestrator.Task) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# Task: %s\n\n", task.Title))
	sb.WriteString(fmt.Sprintf("ID: %s\n", task.ID))
	sb.WriteString(fmt.Sprintf("Type: %s\n", task.Type))
	sb.WriteString(fmt.Sprintf("Description: %s\n\n", task.Description))
	if len(task.Files) > 0 {
		sb.WriteString(fmt.Sprintf("Target Files: %s\n\n", strings.Join(task.Files, ", ")))
	}
	if len(task.Verification) > 0 {
		sb.WriteString("Requested Verification:\n")
		for _, v := range task.Verification {
			sb.WriteString(fmt.Sprintf("- %s\n", v))
		}
		sb.WriteString("\n")
		sb.WriteString("Report verification evidence separately; requested verification is not auto-passed by task execution.\n")
	}
	if len(task.VerificationChecks) > 0 {
		sb.WriteString("Structured Verification Checks:\n")
		for _, check := range task.VerificationChecks {
			sb.WriteString(fmt.Sprintf("- id=%s kind=%s language=%s path=%s pattern=%s timeout_seconds=%d\n",
				check.ID, check.Kind, check.Language, check.Path, check.Pattern, check.TimeoutSeconds))
		}
		sb.WriteString("\nThese checks are host-executed after your task result; do not claim they passed unless the host reports evidence.\n")
	}
	return sb.String()
}

func (r *Runner) applyTaskResults(ctx context.Context, plan *orchestrator.Plan, tasks []orchestrator.Task, results []rlm.BatchResult) error {
	expected := make(map[string]orchestrator.Task, len(tasks))
	for _, task := range tasks {
		expected[task.ID] = task
	}
	byID := make(map[string][]rlm.BatchResult, len(results))
	var runErr error
	for _, result := range results {
		if _, ok := expected[result.TaskID]; !ok {
			runErr = errors.Join(runErr, fmt.Errorf("unexpected task result id: %s", result.TaskID))
			continue
		}
		byID[result.TaskID] = append(byID[result.TaskID], result)
	}
	for _, task := range tasks {
		rows := byID[task.ID]
		switch len(rows) {
		case 0:
			r.updateTaskStatus(plan, task.ID, orchestrator.TaskFailed)
			r.appendTaskExecutionRecord(plan, task, rlm.BatchResult{TaskID: task.ID}, "failed", "not_run", nil, "execution evidence missing")
			runErr = errors.Join(runErr, fmt.Errorf("task %s did not return execution evidence", task.ID))
			continue
		case 1:
		default:
			r.updateTaskStatus(plan, task.ID, orchestrator.TaskFailed)
			for _, row := range rows {
				r.appendTaskExecutionRecord(plan, task, row, "failed", "not_run", nil, "duplicate execution evidence")
			}
			runErr = errors.Join(runErr, fmt.Errorf("task %s returned duplicate execution evidence", task.ID))
			continue
		}
		result := rows[0]
		if strings.TrimSpace(result.Error) != "" {
			r.updateTaskStatus(plan, task.ID, orchestrator.TaskFailed)
			r.appendTaskExecutionRecord(plan, task, result, "failed", "not_run", nil, result.Error)
			runErr = errors.Join(runErr, fmt.Errorf("task %s failed: %s", task.ID, result.Error))
			continue
		}
		if strings.TrimSpace(result.Summary) == "" {
			r.updateTaskStatus(plan, task.ID, orchestrator.TaskFailed)
			r.appendTaskExecutionRecord(plan, task, result, "failed", "not_run", nil, "execution result did not include a public summary")
			runErr = errors.Join(runErr, fmt.Errorf("task %s did not return a meaningful public summary", task.ID))
			continue
		}
		verificationStatus, verificationResults, verificationErr := r.evaluateTaskVerification(ctx, plan, task, result)
		if verificationErr != nil {
			r.updateTaskStatus(plan, task.ID, orchestrator.TaskFailed)
			r.appendTaskExecutionRecord(plan, task, result, "completed", verificationStatus, verificationResults, verificationErr.Error())
			runErr = errors.Join(runErr, fmt.Errorf("task %s verification failed: %w", task.ID, verificationErr))
			continue
		}
		r.appendTaskExecutionRecord(plan, task, result, "completed", verificationStatus, verificationResults, "")
		r.updateTaskStatus(plan, task.ID, orchestrator.TaskCompleted)
	}
	return runErr
}

func (r *Runner) evaluateTaskVerification(ctx context.Context, plan *orchestrator.Plan, task orchestrator.Task, result rlm.BatchResult) (string, []orchestrator.TaskVerificationResult, error) {
	if len(task.VerificationChecks) == 0 {
		if len(task.Verification) > 0 {
			return "unverified", nil, fmt.Errorf("legacy verification criteria are unverified by host-bound execution")
		}
		return "not_requested", nil, nil
	}
	status, results, err := r.runTaskVerificationChecks(ctx, plan, task, r.verificationHost)
	if len(task.Verification) > 0 {
		return "unverified", results, errors.Join(err, fmt.Errorf("legacy verification criteria are unverified by host-bound execution"))
	}
	if err != nil {
		return status, results, err
	}
	return status, results, nil
}

func (r *Runner) appendTaskExecutionRecord(plan *orchestrator.Plan, task orchestrator.Task, result rlm.BatchResult, executionStatus, verificationStatus string, verificationResults []orchestrator.TaskVerificationResult, errText string) {
	if plan == nil {
		return
	}
	runtimeResult := marshalRuntimeTaskResult(result)
	record := orchestrator.TaskExecutionRecord{
		Schema:              orchestrator.TaskExecutionRecordSchemaV1,
		TaskID:              task.ID,
		ExecutionStatus:     executionStatus,
		VerificationStatus:  verificationStatus,
		Summary:             result.Summary,
		Error:               strings.TrimSpace(errText),
		ScratchpadKey:       result.RawKey,
		Model:               result.ModelUsed,
		TokensUsed:          result.TokensUsed,
		RuntimeResult:       runtimeResult,
		VerificationResults: append([]orchestrator.TaskVerificationResult(nil), verificationResults...),
		CreatedAt:           time.Now(),
	}
	for i := range plan.Tasks {
		if plan.Tasks[i].ID == task.ID {
			plan.Tasks[i].ExecutionRecords = append(plan.Tasks[i].ExecutionRecords, record)
			return
		}
	}
}

func marshalRuntimeTaskResult(result rlm.BatchResult) json.RawMessage {
	payload := struct {
		Schema string          `json:"schema"`
		Result rlm.BatchResult `json:"result"`
	}{
		Schema: "buckley.rlm.batch_result.v1",
		Result: result,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	return data
}

func (r *Runner) taskDependenciesCompleted(plan *orchestrator.Plan, task orchestrator.Task) bool {
	if len(task.Dependencies) == 0 {
		return true
	}
	statusByID := make(map[string]orchestrator.TaskStatus, len(plan.Tasks))
	for _, candidate := range plan.Tasks {
		statusByID[candidate.ID] = candidate.Status
	}
	for _, depID := range task.Dependencies {
		if statusByID[depID] != orchestrator.TaskCompleted {
			return false
		}
	}
	return true
}

func planTerminalIncompleteError(plan *orchestrator.Plan) error {
	if plan == nil {
		return nil
	}
	var reasons []string
	for _, task := range plan.Tasks {
		switch task.Status {
		case orchestrator.TaskFailed:
			reasons = append(reasons, fmt.Sprintf("task %s already failed", task.ID))
		case orchestrator.TaskInProgress:
			reasons = append(reasons, fmt.Sprintf("task %s already in progress", task.ID))
		case orchestrator.TaskSkipped:
			reasons = append(reasons, fmt.Sprintf("task %s skipped", task.ID))
		}
	}
	if len(reasons) == 0 {
		return nil
	}
	return fmt.Errorf("plan incomplete: %s", strings.Join(reasons, "; "))
}

func validatePlanTaskIDs(plan *orchestrator.Plan) error {
	seen := make(map[string]struct{}, len(plan.Tasks))
	for i, task := range plan.Tasks {
		id := strings.TrimSpace(task.ID)
		if id == "" {
			return fmt.Errorf("task %d id required", i+1)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("duplicate task id: %s", id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

// taskTypeToWeight maps orchestrator task types to coordinator–worker weights.
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
