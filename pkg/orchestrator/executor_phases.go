package orchestrator

import (
	"fmt"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/telemetry"
)

func (e *Executor) beginTaskExecution(task *Task) *taskExecution {
	startTime := time.Now()
	task.Status = TaskInProgress
	e.planner.UpdatePlan(e.plan)
	e.emitTaskEvent(task, telemetry.EventTaskStarted)
	e.sendProgress("▶️ Task %s: %s", task.ID, task.Title)

	executionID, err := e.recordExecutionStart(task, startTime)
	if err != nil {
		fmt.Printf("Warning: Failed to record execution start: %v\n", err)
	}
	return &taskExecution{id: executionID, startTime: startTime}
}

func (e *Executor) performValidation(task *Task) (*ValidationResult, string, error) {
	e.sendProgress("🧪 Validating preconditions for %q", task.Title)
	result := e.validator.ValidatePreconditions(task)
	e.reportValidationStatus(task, result)

	if !result.Valid {
		e.sendProgress("❌ Preconditions failed for %q", task.Title)
		return result, strings.Join(result.Errors, "; "), buildValidationError(result)
	}

	if err := e.detectBusinessAmbiguity(task); err != nil {
		e.sendProgress("⚠️ Business ambiguity detected for %q: %v", task.Title, err)
		return result, "", err
	}

	return result, "", nil
}

func buildValidationError(result *ValidationResult) error {
	var errMsg strings.Builder
	errMsg.WriteString("precondition validation failed:\n")
	for _, err := range result.Errors {
		errMsg.WriteString(fmt.Sprintf("  - %s\n", err))
	}
	for _, warn := range result.Warnings {
		errMsg.WriteString(fmt.Sprintf("  - WARNING: %s\n", warn))
	}
	return fmt.Errorf("%s", errMsg.String())
}

func (e *Executor) runBuilderPhase(task *Task) (*BuilderResult, error) {
	e.sendProgress("→ Builder Agent: %s", task.Title)
	result, err := e.runBuilder(task)
	if err != nil {
		e.sendProgress("❌ Builder agent failed for %q: %v", task.Title, err)
		return nil, fmt.Errorf("builder phase for %q: %w", task.Title, err)
	}
	e.sendProgress("✅ Builder agent completed %q (%d file(s))", task.Title, len(result.Files))
	return result, nil
}

func (e *Executor) runVerificationPhase(task *Task) (*VerifyResult, error) {
	e.sendProgress("🔍 Verifying outcomes for %q", task.Title)
	verifyResult := &VerifyResult{}
	if err := e.verifier.VerifyOutcomes(task, verifyResult); err != nil {
		e.sendProgress("⚠️ Verification errors for %q: %v", task.Title, err)
		if err := e.handleError(task, err); err != nil {
			return verifyResult, fmt.Errorf("self-heal for %q: %w", task.Title, err)
		}
		// Self-heal succeeded; refresh verification results for downstream reporting.
		verifyResult = &VerifyResult{}
		if err := e.verifier.VerifyOutcomes(task, verifyResult); err != nil {
			e.sendProgress("⚠️ Verification errors after self-heal for %q: %v", task.Title, err)
			return verifyResult, fmt.Errorf("verification after self-heal for %q: %w", task.Title, err)
		}
	}
	if !verifyResult.Passed {
		e.sendProgress("❌ Verification failed for %q", task.Title)
		return verifyResult, fmt.Errorf("%s", formatVerificationFailure(verifyResult))
	}
	e.sendProgress("✅ Verification passed for %q", task.Title)
	return verifyResult, nil
}

func formatVerificationFailure(result *VerifyResult) string {
	var errMsg strings.Builder
	errMsg.WriteString("verification failed:\n")
	for _, err := range result.Errors {
		errMsg.WriteString(fmt.Sprintf("  - %s\n", err))
	}
	for _, warn := range result.Warnings {
		errMsg.WriteString(fmt.Sprintf("  - WARNING: %s\n", warn))
	}
	return errMsg.String()
}

func (e *Executor) runReviewPhase(task *Task, builderResult *BuilderResult) error {
	if err := e.review(task, builderResult); err != nil {
		e.sendProgress("⚠️ Review agent blocked progress on %q: %v", task.Title, err)
		return fmt.Errorf("review phase for %q: %w", task.Title, err)
	}
	e.sendProgress("📬 Review agent completed for %q", task.Title)
	return nil
}

func (e *Executor) failExecution(exec *taskExecution, task *Task, verifyResult *VerifyResult, err error) error {
	var verificationSummary string
	var artifacts string
	if verifyResult != nil {
		verificationSummary = formatVerificationResults(verifyResult)
		artifacts = formatArtifacts(verifyResult)
	}
	if exec == nil {
		exec = &taskExecution{startTime: time.Now()}
	}
	e.recordExecutionComplete(exec.id, task, "failed", exec.validationErrors, verificationSummary, artifacts, exec.startTime, e.retryCount)
	e.emitTaskEvent(task, telemetry.EventTaskFailed)
	return fmt.Errorf("task %s execution failed: %w", task.ID, err)
}

func (e *Executor) completeExecution(exec *taskExecution, task *Task, verifyResult *VerifyResult) error {
	task.Status = TaskCompleted
	e.planner.UpdatePlan(e.plan)

	var verificationSummary string
	var artifacts string
	if verifyResult != nil {
		verificationSummary = formatVerificationResults(verifyResult)
		artifacts = formatArtifacts(verifyResult)
	}

	e.recordExecutionComplete(exec.id, task, "completed", exec.validationErrors, verificationSummary, artifacts, exec.startTime, e.retryCount)
	e.emitTaskEvent(task, telemetry.EventTaskCompleted)
	e.sendProgress("🏁 Task %s completed", task.ID)
	return nil
}
