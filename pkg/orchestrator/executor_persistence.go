package orchestrator

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/telemetry"
)

func (e *Executor) runBuilder(task *Task) (*BuilderResult, error) {
	if e.builder == nil {
		return nil, fmt.Errorf("builder agent not initialized")
	}

	result, err := e.builder.Build(task)
	if err != nil {
		return nil, fmt.Errorf("building task %s: %w", task.ID, err)
	}

	return result, nil
}

func (e *Executor) recordExecutionStart(task *Task, startTime time.Time) (int64, error) {
	if e.store == nil || e.store.DB() == nil {
		return 0, fmt.Errorf("store not initialized")
	}

	ctx, cancel := context.WithTimeout(e.baseContext(), 5*time.Second)
	defer cancel()

	result, err := e.store.DB().ExecContext(ctx, `
		INSERT INTO executions (plan_id, task_id, status, started_at, retry_count)
		VALUES (?, ?, 'running', ?, ?)
	`, e.plan.ID, task.ID, startTime, e.retryCount)

	if err != nil {
		return 0, fmt.Errorf("inserting execution record for task %s: %w", task.ID, err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("retrieving execution id for task %s: %w", task.ID, err)
	}

	return id, nil
}

func (e *Executor) recordExecutionComplete(executionID int64, task *Task, status, validationErrors, verificationResults, artifacts string, startTime time.Time, retryCount int) error {
	if e.store == nil || e.store.DB() == nil || executionID == 0 {
		return nil // Silently skip if we couldn't start the recording
	}

	ctx, cancel := context.WithTimeout(e.baseContext(), 5*time.Second)
	defer cancel()

	executionTime := time.Since(startTime).Milliseconds()

	_, err := e.store.DB().ExecContext(ctx, `
		UPDATE executions
		SET status = ?,
			validation_errors = ?,
			verification_results = ?,
			artifacts = ?,
			completed_at = ?,
			execution_time_ms = ?,
			retry_count = ?
		WHERE id = ?
	`, status, validationErrors, verificationResults, artifacts, time.Now(), executionTime, retryCount, executionID)

	if err != nil {
		return fmt.Errorf("updating execution record %d: %w", executionID, err)
	}
	return nil
}

func (e *Executor) emitTaskEvent(task *Task, eventType telemetry.EventType) {
	if e == nil || e.workflow == nil {
		return
	}
	e.workflow.EmitTaskEvent(task, eventType)
}

func formatVerificationResults(result *VerifyResult) string {
	if result == nil {
		return ""
	}
	return fmt.Sprintf("passed: %v, errors: %d, warnings: %d", result.Passed, len(result.Errors), len(result.Warnings))
}

func formatArtifacts(result *VerifyResult) string {
	if result == nil || len(result.Artifacts) == 0 {
		return ""
	}
	var artifactDescriptions []string
	for _, artifact := range result.Artifacts {
		artifactDescriptions = append(artifactDescriptions, fmt.Sprintf("%s:%s:%s", artifact.ID, artifact.Type, artifact.Path))
	}
	return strings.Join(artifactDescriptions, ";")
}

// ExecutionContext captures execution metadata for historical tracking.
type ExecutionContext struct {
	PlanID              string
	TaskID              string
	SessionID           string
	Status              string
	ValidationErrors    string
	VerificationResults string
	Artifacts           string
}

// RecordExecutionContext persists execution metadata for later analysis.
func (e *Executor) RecordExecutionContext(ctx ExecutionContext) error {
	if e.store == nil || e.store.DB() == nil {
		return fmt.Errorf("store not initialized")
	}

	var sessionID sql.NullString
	if ctx.SessionID != "" {
		sessionID = sql.NullString{String: ctx.SessionID, Valid: true}
	}

	now := time.Now()
	_, err := e.store.DB().Exec(
		`INSERT INTO executions (
			plan_id, task_id, session_id, status,
			validation_errors, verification_results, artifacts,
			started_at, completed_at, execution_time_ms, retry_count
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ctx.PlanID,
		ctx.TaskID,
		sessionID,
		ctx.Status,
		ctx.ValidationErrors,
		ctx.VerificationResults,
		ctx.Artifacts,
		now,
		now,
		0,
		0,
	)
	if err != nil {
		return fmt.Errorf("recording execution context for task %s: %w", ctx.TaskID, err)
	}
	return nil
}
