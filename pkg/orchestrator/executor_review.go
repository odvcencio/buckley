package orchestrator

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/artifact"
	"github.com/odvcencio/buckley/pkg/model"
)

func (e *Executor) review(task *Task, builderResult *BuilderResult) error {
	if e.config.Orchestrator.TrustLevel == "autonomous" || e.reviewer == nil {
		return nil
	}
	if builderResult == nil {
		return fmt.Errorf("builder result missing for review")
	}

	e.ensureReviewInitialized()

	for cycle := 1; cycle <= max(1, e.maxReviewCycles); cycle++ {
		if e.workflow != nil {
			e.workflow.SetActiveAgent("Reviewer")
		}
		result, err := e.reviewer.Review(task, builderResult)
		if err != nil {
			if e.config.Orchestrator.TrustLevel == "balanced" {
				fmt.Printf("Warning: review skipped due to error: %v\n", err)
				return nil
			}
			return fmt.Errorf("reviewing task %s: %w", task.ID, err)
		}
		if result == nil {
			return fmt.Errorf("review result unavailable")
		}

		e.recordReviewArtifact(result, cycle)

		if result.Approved {
			return nil
		}

		if cycle >= e.maxReviewCycles {
			return fmt.Errorf("review blocked after %d cycles: %s", e.maxReviewCycles, result.Summary)
		}

		fixResult, err := e.applyReviewFixes(task, result)
		if err != nil {
			return fmt.Errorf("failed to apply review fixes: %w", err)
		}

		verifyResult := &VerifyResult{}
		if err := e.verifier.VerifyOutcomes(task, verifyResult); err != nil {
			if err := e.handleError(task, err); err != nil {
				return fmt.Errorf("verification failed after review fixes: %w", err)
			}
			// Self-heal succeeded; re-run verification to populate results.
			verifyResult = &VerifyResult{}
			if err := e.verifier.VerifyOutcomes(task, verifyResult); err != nil {
				return fmt.Errorf("verification failed after review fixes: %w", err)
			}
		}
		if !verifyResult.Passed {
			return fmt.Errorf("verification failed after review fixes: %s", strings.Join(verifyResult.Errors, "; "))
		}

		builderResult = fixResult
		if e.workflow != nil {
			e.workflow.ClearPause()
			e.workflow.SetActiveAgent("Execution")
		}
	}

	return nil
}

func (e *Executor) reportValidationStatus(task *Task, result *ValidationResult) {
	if e == nil || result == nil {
		return
	}
	if !result.Valid {
		return
	}
	if len(result.Warnings) > 0 {
		preview := strings.Join(result.Warnings, "; ")
		if len(preview) > 120 {
			preview = preview[:117] + "..."
		}
		e.sendProgress("⚠️ Preconditions passed for %q with warnings: %s", task.Title, preview)
		return
	}
	e.sendProgress("✅ Preconditions satisfied for %q", task.Title)
}

func (e *Executor) sendProgress(format string, args ...any) {
	if e == nil || e.workflow == nil {
		return
	}
	e.workflow.SendProgress(fmt.Sprintf(format, args...))
}

func (e *Executor) handleError(task *Task, err error) error {
	if e.retryCount >= e.maxRetries {
		return fmt.Errorf("max retries exceeded: %w", err)
	}

	// Initialize retry context if this is the first retry
	if e.retryContext == nil {
		e.retryContext = &RetryContext{
			Attempt:       0,
			FilesBefore:   e.captureFileState(task),
			PreviousError: err.Error(),
		}
	}

	e.retryCount++
	e.retryContext.Attempt++

	// Check if we're stuck in a loop with the same error
	currentError := err.Error()
	if e.retryContext.Attempt > 1 && e.retryContext.PreviousError == currentError && !e.retryContext.Changed {
		// Same error and no changes - we're in a dead-end loop
		return fmt.Errorf("retry loop detected: same error '%s' repeated without progress after %d attempts", currentError, e.retryContext.Attempt)
	}

	// Analyze error and generate fix
	fix, fixErr := e.analyzeAndFix(task, err)
	if fixErr != nil {
		return fmt.Errorf("failed to generate fix: %w", fixErr)
	}

	// Apply fix via builder agent for consistent logging
	if e.builder == nil {
		return fmt.Errorf("builder agent not initialized")
	}
	if _, applyErr := e.builder.ApplyImplementation(task, fix, "self_heal"); applyErr != nil {
		return fmt.Errorf("failed to apply fix: %w", applyErr)
	}

	// Check if any files actually changed
	filesAfter := e.captureFileState(task)
	e.retryContext.Changed = e.filesChanged(e.retryContext.FilesBefore, filesAfter)
	e.retryContext.FilesBefore = filesAfter
	e.retryContext.PreviousError = currentError

	// Retry verification
	verifyResult := &VerifyResult{}
	if verifyErr := e.verifier.VerifyOutcomes(task, verifyResult); verifyErr != nil {
		return e.handleError(task, verifyErr)
	}
	if !verifyResult.Passed {
		return e.handleError(task, fmt.Errorf("verification failed after fix"))
	}

	// Success - reset retry state
	e.retryCount = 0
	e.retryContext = nil
	return nil
}

func (e *Executor) analyzeAndFix(task *Task, err error) (string, error) {
	prompt := fmt.Sprintf("The following error occurred while implementing task '%s':\n\n%s\n\nAnalyze the error and provide a fix.",
		task.Title, err.Error())

	req := model.ChatRequest{
		Model: e.config.Models.Execution,
		Messages: []model.Message{
			{
				Role:    "system",
				Content: "You are debugging an implementation. Analyze errors and provide fixes.",
			},
			{
				Role:    "user",
				Content: prompt,
			},
		},
		Temperature: 0.2,
	}

	resp, err := e.modelClient.ChatCompletion(e.baseContext(), req)
	if err != nil {
		return "", fmt.Errorf("analyzing error for task %s: %w", task.Title, err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no response choices from model")
	}

	return model.ExtractTextContent(resp.Choices[0].Message.Content)
}

func (e *Executor) applyReviewFixes(task *Task, review *ReviewResult) (*BuilderResult, error) {
	if e.builder == nil {
		return nil, fmt.Errorf("builder agent not initialized")
	}
	if review == nil {
		return nil, fmt.Errorf("review result missing")
	}

	fix, err := e.generateReviewFix(task, review)
	if err != nil {
		return nil, fmt.Errorf("generating review fix for %q: %w", task.Title, err)
	}

	files, err := e.builder.ApplyImplementation(task, fix, "review_fix")
	if err != nil {
		return nil, fmt.Errorf("applying review fix for %q: %w", task.Title, err)
	}

	now := time.Now()
	return &BuilderResult{
		Implementation: fix,
		Files:          files,
		StartedAt:      now,
		CompletedAt:    now,
	}, nil
}

func (e *Executor) generateReviewFix(task *Task, review *ReviewResult) (string, error) {
	issuesData, err := e.issuesCodec.Marshal(review.Issues)
	if err != nil {
		return "", fmt.Errorf("marshaling review issues for %q: %w", task.Title, err)
	}
	prompt := fmt.Sprintf(
		"Task %q failed review.\n\nReview summary:\n%s\n\nIssues (TOON):\n%s\n\nUpdate the necessary files to resolve every blocking issue. Respond using the same code block format as the builder agent:\n```filepath:path/to/file.go\n<contents>\n```",
		task.Title,
		review.Summary,
		string(issuesData),
	)

	req := model.ChatRequest{
		Model: e.config.Models.Execution,
		Messages: []model.Message{
			{
				Role:    "system",
				Content: "You are assisting with review fixes. Apply the requested changes and respond with complete file contents.",
			},
			{
				Role:    "user",
				Content: prompt,
			},
		},
		Temperature: 0.2,
	}

	resp, err := e.modelClient.ChatCompletion(e.baseContext(), req)
	if err != nil {
		return "", fmt.Errorf("generating review fix for %q: %w", task.Title, err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no response choices from model")
	}

	return model.ExtractTextContent(resp.Choices[0].Message.Content)
}

func (e *Executor) ensureReviewInitialized() {
	if e.workflow == nil {
		return
	}
	e.workflow.stateMu.RLock()
	reviewExists := e.workflow.reviewArtifact != nil
	e.workflow.stateMu.RUnlock()
	if reviewExists {
		return
	}

	planPath := filepath.Join(e.config.Artifacts.PlanningDir, fmt.Sprintf("%s.md", e.plan.ID))
	execPath := ""
	if e.workflow.executionTracker != nil {
		execPath = e.workflow.executionTracker.GetFilePath()
	}
	if err := e.workflow.StartReview(planPath, execPath); err != nil {
		fmt.Printf("Warning: failed to initialize review artifact: %v\n", err)
	}
}

func (e *Executor) recordReviewArtifact(result *ReviewResult, iteration int) {
	if e.workflow == nil || result == nil || result.Artifact == nil {
		return
	}

	art := result.Artifact
	e.workflow.SetValidationStrategy(art.ValidationStrategy)
	for _, vr := range art.ValidationResults {
		e.workflow.AddValidationResult(vr)
	}
	for _, issue := range art.IssuesFound {
		e.workflow.AddIssue(issue)
	}
	for _, improvement := range art.OpportunisticImprovements {
		e.workflow.AddOpportunisticImprovement(improvement)
	}

	iterationRecord := artifact.ReviewIteration{
		Number:      iteration,
		Timestamp:   time.Now(),
		IssuesFound: len(result.Issues),
		Status:      art.Status,
		Notes:       result.Summary,
	}
	e.workflow.AddReviewIteration(iterationRecord)

	if result.Approved && art.Approval != nil {
		if err := e.workflow.ApproveReview(*art.Approval); err != nil {
			fmt.Printf("Warning: failed to finalize review artifact: %v\n", err)
		}
	}
}
