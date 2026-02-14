package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/telemetry"
)

func (e *Executor) executeTaskInBatch(task *Task) error {
	startTime := time.Now()
	task.Status = TaskInProgress
	e.planner.UpdatePlan(e.plan)
	e.emitTaskEvent(task, telemetry.EventTaskStarted)
	e.sendProgress("🛰️ Executing %s via Kubernetes batch job", task.Title)

	executionID, err := e.recordExecutionStart(task, startTime)
	if err != nil {
		fmt.Printf("Warning: Failed to record execution start: %v\n", err)
	}

	e.sendProgress("🧪 Validating preconditions for %q", task.Title)
	validationResult := e.validator.ValidatePreconditions(task)
	validationErrors := ""
	if !validationResult.Valid {
		validationErrors = strings.Join(validationResult.Errors, "; ")
	}
	e.reportValidationStatus(task, validationResult)
	if !validationResult.Valid {
		task.Status = TaskFailed
		e.recordExecutionComplete(executionID, task, "failed", validationErrors, "", "", startTime, e.retryCount)
		e.emitTaskEvent(task, telemetry.EventTaskFailed)
		return fmt.Errorf("precondition validation failed: %s", validationErrors)
	}

	if err := e.detectBusinessAmbiguity(task); err != nil {
		task.Status = TaskFailed
		e.recordExecutionComplete(executionID, task, "failed", validationErrors, "", "", startTime, e.retryCount)
		e.emitTaskEvent(task, telemetry.EventTaskFailed)
		return fmt.Errorf("business ambiguity in batch task %s: %w", task.ID, err)
	}

	ctx, cancel := context.WithTimeout(e.baseContext(), 2*time.Hour)
	defer cancel()

	result, err := e.batchCoordinator.DispatchTask(ctx, e.plan, task)
	if err != nil {
		task.Status = TaskFailed
		e.recordExecutionComplete(executionID, task, "failed", validationErrors, "", "", startTime, e.retryCount)
		e.emitTaskEvent(task, telemetry.EventTaskFailed)
		return fmt.Errorf("batch execution failed: %w", err)
	}

	// Reload on-disk plan artifacts after containerized execution
	e.reloadPlanFromDisk()
	if updated := e.findTask(task.ID); updated != nil {
		task = updated
	}

	task.Status = TaskCompleted
	e.retryCount = 0
	if err := e.planner.UpdatePlan(e.plan); err != nil {
		fmt.Printf("Warning: failed to persist plan updates: %v\n", err)
	}
	e.recordExecutionComplete(executionID, task, "completed", validationErrors, "", "", startTime, e.retryCount)
	e.emitTaskEvent(task, telemetry.EventTaskCompleted)

	msg := fmt.Sprintf("✅ Batch job %s completed for %s", result.JobName, task.Title)
	if result.RemoteBranch != "" {
		msg = fmt.Sprintf("%s (remote branch: %s)", msg, result.RemoteBranch)
	}
	e.sendProgress("%s", msg)
	return nil
}

func (e *Executor) reloadPlanFromDisk() {
	if e == nil || e.planner == nil || e.plan == nil {
		return
	}
	latest, err := e.planner.LoadPlan(e.plan.ID)
	if err != nil || latest == nil {
		return
	}
	*e.plan = *latest
}

func (e *Executor) findTask(taskID string) *Task {
	if e == nil || e.plan == nil {
		return nil
	}
	for i := range e.plan.Tasks {
		if e.plan.Tasks[i].ID == taskID {
			return &e.plan.Tasks[i]
		}
	}
	return nil
}
