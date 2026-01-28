package orchestrator

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/artifact"
	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/encoding/toon"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/personality"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/buckley/pkg/tool"
)

var issuesCodec = toon.New(true)

var validTaskStages = map[string]struct{}{
	"builder": {},
	"verify":  {},
	"review":  {},
}

type reviewerAgent interface {
	Review(task *Task, builderResult *BuilderResult) (*ReviewResult, error)
	SetPersonaProvider(provider *personality.PersonaProvider)
}

type verifierAgent interface {
	VerifyOutcomes(task *Task, result *VerifyResult) error
}

type Executor struct {
	plan             *Plan
	currentTask      *Task
	ctx              context.Context
	cancel           context.CancelFunc
	store            *storage.Store
	modelClient      ModelClient
	toolRegistry     *tool.Registry
	config           *config.Config
	planner          *Planner
	validator        *Validator
	verifier         verifierAgent
	builder          *BuilderAgent
	reviewer         reviewerAgent
	workflow         *WorkflowManager
	batchCoordinator *BatchCoordinator
	issuesCodec      *toon.Codec

	maxRetries      int
	maxReviewCycles int
	retryCount      int
	retryContext    *RetryContext
	taskPhases      []TaskPhase
}

func (e *Executor) baseContext() context.Context {
	if e == nil || e.ctx == nil {
		return context.Background()
	}
	return e.ctx
}

// SetPersonaProvider updates sub-agents with refreshed persona definitions.
func (e *Executor) SetPersonaProvider(provider *personality.PersonaProvider) {
	if e == nil || e.reviewer == nil {
		return
	}
	e.reviewer.SetPersonaProvider(provider)
}

type taskExecution struct {
	id               int64
	startTime        time.Time
	validationErrors string
}

// RetryContext tracks retry progress to detect dead-end loops
type RetryContext struct {
	Attempt       int
	PreviousError string
	Changed       bool // Did this retry change anything?
	FilesBefore   map[string]bool
}

func NewExecutor(plan *Plan, store *storage.Store, mgr ModelClient, registry *tool.Registry, cfg *config.Config, planner *Planner, workflow *WorkflowManager, batchCoordinator *BatchCoordinator) *Executor {
	phases := resolveTaskPhases(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	var reviewer reviewerAgent
	if rev := NewReviewAgent(plan, cfg, mgr, registry, workflow); rev != nil {
		reviewer = rev
	}
	var verifier verifierAgent
	if v := NewVerifier(registry); v != nil {
		verifier = v
	}
	return &Executor{
		plan:         plan,
		store:        store,
		modelClient:  mgr,
		toolRegistry: registry,
		config:       cfg,
		planner:      planner,
		validator: func() *Validator {
			if workflow != nil {
				return NewValidator(registry, workflow.projectRoot)
			}
			return NewValidator(registry, "")
		}(),
		verifier:         verifier,
		builder:          NewBuilderAgent(plan, cfg, mgr, registry, workflow),
		reviewer:         reviewer,
		workflow:         workflow,
		batchCoordinator: batchCoordinator,
		maxRetries:       cfg.Orchestrator.MaxSelfHealAttempts,
		maxReviewCycles:  cfg.Orchestrator.MaxReviewCycles,
		issuesCodec:      toon.New(cfg.Encoding.UseToon),
		taskPhases:       phases,
		ctx:              ctx,
		cancel:           cancel,
	}
}

// SetContext replaces the executor context (used for external cancellation).
func (e *Executor) SetContext(ctx context.Context) {
	if e == nil || ctx == nil {
		return
	}
	if e.cancel != nil {
		e.cancel()
	}
	e.ctx, e.cancel = context.WithCancel(ctx)
	if e.builder != nil {
		e.builder.SetContext(e.ctx)
	}
	if e.validator != nil {
		e.validator.SetContext(e.ctx)
	}
	if e.verifier != nil {
		if setter, ok := e.verifier.(interface{ SetContext(context.Context) }); ok {
			setter.SetContext(e.ctx)
		}
	}
	if e.reviewer != nil {
		if setter, ok := e.reviewer.(interface{ SetContext(context.Context) }); ok {
			setter.SetContext(e.ctx)
		}
	}
}

// Cancel stops any in-flight execution.
func (e *Executor) Cancel() {
	if e.cancel != nil {
		e.cancel()
	}
}

func (e *Executor) Execute() error {
	for i := range e.plan.Tasks {
		task := &e.plan.Tasks[i]

		if err := e.ctx.Err(); err != nil {
			if e.workflow != nil {
				e.workflow.SendProgress(fmt.Sprintf("⏹️ Execution cancelled: %v", err))
			}
			return err
		}

		// Skip if already completed
		if task.Status == TaskCompleted {
			continue
		}

		// Check dependencies
		if !e.dependenciesMet(task) {
			return fmt.Errorf("task %s has unmet dependencies", task.ID)
		}

		// Execute task
		e.currentTask = task
		if err := e.executeTask(task); err != nil {
			task.Status = TaskFailed
			e.planner.UpdatePlan(e.plan)
			return fmt.Errorf("task %s failed: %w", task.ID, err)
		}

		// Save progress
		if err := e.saveProgress(); err != nil {
			return fmt.Errorf("failed to save progress: %w", err)
		}
	}

	return nil
}

func (e *Executor) executeTask(task *Task) error {
	if err := e.ctx.Err(); err != nil {
		return err
	}
	if e.batchCoordinator != nil && e.batchCoordinator.Enabled() {
		return e.executeTaskInBatch(task)
	}
	return e.executeTaskLocal(task)
}

func (e *Executor) executeTaskLocal(task *Task) error {
	exec := e.beginTaskExecution(task)

	_, validationSummary, err := e.performValidation(task)
	if err != nil {
		return e.failExecution(exec, task, nil, err)
	}
	exec.validationErrors = validationSummary

	var builderResult *BuilderResult
	var verifyResult *VerifyResult

	for _, phase := range e.taskPhases {
		switch phase.Stage {
		case "builder":
			builderResult, err = e.runBuilderPhase(task)
			if err != nil {
				return e.failExecution(exec, task, verifyResult, err)
			}
		case "verify":
			if builderResult == nil {
				builderResult, err = e.runBuilderPhase(task)
				if err != nil {
					return e.failExecution(exec, task, verifyResult, err)
				}
			}
			verifyResult, err = e.runVerificationPhase(task)
			if err != nil {
				return e.failExecution(exec, task, verifyResult, err)
			}
		case "review":
			if builderResult == nil {
				builderResult, err = e.runBuilderPhase(task)
				if err != nil {
					return e.failExecution(exec, task, verifyResult, err)
				}
			}
			if err := e.runReviewPhase(task, builderResult); err != nil {
				return e.failExecution(exec, task, verifyResult, fmt.Errorf("review failed: %w", err))
			}
		}
	}

	return e.completeExecution(exec, task, verifyResult)
}

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
		return nil, err
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
			return verifyResult, err
		}
		// Self-heal succeeded; refresh verification results for downstream reporting.
		verifyResult = &VerifyResult{}
		if err := e.verifier.VerifyOutcomes(task, verifyResult); err != nil {
			e.sendProgress("⚠️ Verification errors after self-heal for %q: %v", task.Title, err)
			return verifyResult, err
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
		return err
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
	return err
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

func (e *Executor) runBuilder(task *Task) (*BuilderResult, error) {
	if e.builder == nil {
		return nil, fmt.Errorf("builder agent not initialized")
	}

	result, err := e.builder.Build(task)
	if err != nil {
		return nil, err
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
		return 0, err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
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

	return err
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
			return err
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
		return "", err
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
		return nil, err
	}

	files, err := e.builder.ApplyImplementation(task, fix, "review_fix")
	if err != nil {
		return nil, err
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
		return "", err
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
		return "", err
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
	if e.workflow.reviewArtifact != nil {
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

func (e *Executor) dependenciesMet(task *Task) bool {
	for _, depID := range task.Dependencies {
		// Find dependency task
		depMet := false
		for _, t := range e.plan.Tasks {
			if t.ID == depID && t.Status == TaskCompleted {
				depMet = true
				break
			}
		}
		if !depMet {
			return false
		}
	}
	return true
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (e *Executor) detectBusinessAmbiguity(task *Task) error {
	if e.workflow == nil || e.config == nil || !e.config.Workflow.PauseOnBusinessAmbiguity {
		return nil
	}

	text := strings.ToLower(strings.TrimSpace(task.Title + " " + task.Description))
	if text == "" {
		return nil
	}

	keywords := []string{"clarify", "unknown", "decide", "unsure", "not sure", "tbd", "???"}
	for _, kw := range keywords {
		if strings.Contains(text, kw) {
			return e.workflow.pauseWorkflow("Business Ambiguity",
				fmt.Sprintf("Task %s may require clarification: \"%s\"", task.ID, snippet(task.Description, 160)))
		}
	}

	if strings.Count(task.Description, "?") >= 2 {
		return e.workflow.pauseWorkflow("Business Ambiguity",
			fmt.Sprintf("Task %s contains unresolved questions: \"%s\"", task.ID, snippet(task.Description, 160)))
	}

	return nil
}

func snippet(text string, limit int) string {
	text = strings.TrimSpace(text)
	if text == "" || len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}

func (e *Executor) saveProgress() error {
	// Save plan updates
	return e.planner.UpdatePlan(e.plan)
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
	return err
}

func (e *Executor) GetProgress() (completed int, total int) {
	total = len(e.plan.Tasks)
	completed = 0
	for _, task := range e.plan.Tasks {
		if task.Status == TaskCompleted {
			completed++
		}
	}
	return completed, total
}

// captureFileState captures the current state of task files for change detection
func (e *Executor) captureFileState(task *Task) map[string]bool {
	state := make(map[string]bool)
	if task == nil {
		return state
	}

	readTool, ok := e.toolRegistry.Get("read_file")
	if !ok {
		return state
	}

	for _, filePath := range task.Files {
		// Try to read the file
		params := map[string]any{
			"path": filePath,
		}
		result, err := readTool.Execute(params)
		if err == nil && result.Success {
			// File exists
			state[filePath] = true
		}
	}

	return state
}

// filesChanged checks if files changed between two states
func (e *Executor) filesChanged(before, after map[string]bool) bool {
	// Check if any new files appeared
	for file := range after {
		if !before[file] {
			return true
		}
	}

	// Check if any files disappeared (less common but possible)
	for file := range before {
		if !after[file] {
			return true
		}
	}

	// For implementation tasks, file existence change indicates progress
	// For analysis tasks, no file changes is expected
	return false
}

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
		return err
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
