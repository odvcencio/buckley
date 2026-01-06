package experiment

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/odvcencio/buckley/pkg/config"
	projectcontext "github.com/odvcencio/buckley/pkg/context"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/notify"
	"github.com/odvcencio/buckley/pkg/parallel"
	"github.com/odvcencio/buckley/pkg/telemetry"
)

// RunnerConfig controls experiment execution behavior.
type RunnerConfig struct {
	MaxConcurrent  int
	DefaultTimeout time.Duration
	CleanupOnDone  bool
}

// Dependencies bundles the shared dependencies for the runner.
type Dependencies struct {
	Config         *config.Config
	ModelManager   *model.Manager
	ProjectContext *projectcontext.ProjectContext
	Telemetry      *telemetry.Hub
	Notify         *notify.Manager
	Worktree       parallel.WorktreeManager
	Store          *Store
}

// Runner executes experiments across multiple variants.
type Runner struct {
	cfg          RunnerConfig
	modelManager *model.Manager
	projectCtx   *projectcontext.ProjectContext
	telemetry    *telemetry.Hub
	notify       *notify.Manager
	parallel     *parallel.Orchestrator
	store        *Store
}

// NewRunner constructs a runner with the required dependencies.
func NewRunner(cfg RunnerConfig, deps Dependencies) (*Runner, error) {
	if deps.Config == nil {
		return nil, errors.New("config is required")
	}
	if deps.ModelManager == nil {
		return nil, errors.New("model manager is required")
	}
	if deps.Worktree == nil {
		return nil, errors.New("worktree manager is required")
	}

	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 4
	}
	if cfg.DefaultTimeout <= 0 {
		cfg.DefaultTimeout = 30 * time.Minute
	}

	executor := &experimentExecutor{
		config:         deps.Config,
		modelManager:   deps.ModelManager,
		projectContext: deps.ProjectContext,
		telemetry:      deps.Telemetry,
	}
	parallelCfg := parallel.Config{
		MaxAgents:       cfg.MaxConcurrent,
		TaskQueueSize:   100,
		ResultQueueSize: 100,
	}

	return &Runner{
		cfg:          cfg,
		modelManager: deps.ModelManager,
		projectCtx:   deps.ProjectContext,
		telemetry:    deps.Telemetry,
		notify:       deps.Notify,
		parallel:     parallel.NewOrchestrator(deps.Worktree, executor, parallelCfg),
		store:        deps.Store,
	}, nil
}

// RunExperiment executes all variants and returns their results.
func (r *Runner) RunExperiment(ctx context.Context, exp *Experiment) ([]*parallel.AgentResult, error) {
	if exp == nil {
		return nil, errors.New("experiment is nil")
	}
	if len(exp.Variants) == 0 {
		return nil, errors.New("experiment has no variants")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if exp.ID == "" {
		exp.ID = ulid.Make().String()
	}

	for i := range exp.Variants {
		variant := &exp.Variants[i]
		if variant.ID == "" {
			variant.ID = ulid.Make().String()
		}
	}

	timeout := exp.Task.Timeout
	if timeout <= 0 {
		timeout = r.cfg.DefaultTimeout
	}

	runIDs := make(map[string]string)
	startTimes := make(map[string]time.Time)

	if r.store != nil {
		stored, err := r.store.GetExperiment(exp.ID)
		if err != nil {
			return nil, err
		}
		if stored == nil {
			if err := r.store.CreateExperiment(exp); err != nil {
				return nil, err
			}
		}
		if err := r.store.UpdateExperimentStatus(exp.ID, ExperimentRunning, nil); err != nil {
			return nil, err
		}
	}
	exp.Status = ExperimentRunning

	r.publishExperimentStart(exp)
	r.notifyExperimentStart(ctx, exp)

	r.parallel.Start()
	defer r.parallel.Stop()

	for i := range exp.Variants {
		variant := &exp.Variants[i]
		contextValues := copyContext(exp.Task.Context)
		task := &parallel.AgentTask{
			ID:          variant.ID,
			Name:        variant.Name,
			Description: exp.Task.Prompt,
			Branch:      fmt.Sprintf("experiment/%s/%s", exp.ID, variant.ID),
			Prompt:      exp.Task.Prompt,
			Context:     contextValues,
		}
		contextValues["model_id"] = variant.ModelID
		contextValues["provider"] = variant.ProviderID
		contextValues["timeout"] = strconv.FormatInt(timeout.Milliseconds(), 10)
		contextValues["experiment"] = exp.ID
		if exp.Task.WorkingDir != "" {
			contextValues["working_dir"] = exp.Task.WorkingDir
		}
		if variant.SystemPrompt != nil {
			contextValues["system_prompt"] = strings.TrimSpace(*variant.SystemPrompt)
		}
		if variant.Temperature != nil {
			contextValues["temperature"] = fmt.Sprintf("%g", *variant.Temperature)
		}
		if variant.MaxTokens != nil {
			contextValues["max_tokens"] = strconv.Itoa(*variant.MaxTokens)
		}
		if len(variant.ToolsAllowed) > 0 {
			task.Context["tools_allowed"] = joinTools(variant.ToolsAllowed)
		}

		// Set file scope for conflict detection (variant overrides task)
		files := variant.Files
		if len(files) == 0 {
			files = exp.Task.Files
		}
		if len(files) > 0 {
			contextValues["files"] = strings.Join(files, ",")
		}
		scope := variant.Scope
		if len(scope) == 0 {
			scope = exp.Task.Scope
		}
		if len(scope) > 0 {
			contextValues["scope"] = strings.Join(scope, ",")
		}

		if r.store != nil {
			runID := ulid.Make().String()
			runIDs[variant.ID] = runID
			startTime := time.Now()
			startTimes[variant.ID] = startTime
			run := Run{
				ID:           runID,
				ExperimentID: exp.ID,
				VariantID:    variant.ID,
				Branch:       task.Branch,
				Status:       RunRunning,
				StartedAt:    startTime,
			}
			if err := r.store.SaveRun(&run); err != nil {
				return nil, err
			}
		}

		r.publishVariantEvent(telemetry.EventExperimentVariantStarted, exp, variant, nil)
		r.notifyVariantStart(ctx, exp, variant)
		if err := r.parallel.Submit(task); err != nil {
			return nil, err
		}
	}

	results := make([]*parallel.AgentResult, 0, len(exp.Variants))
	hadFailure := false
	for len(results) < len(exp.Variants) {
		select {
		case <-ctx.Done():
			if r.store != nil {
				_ = r.store.UpdateExperimentStatus(exp.ID, ExperimentCancelled, nil)
			}
			return results, ctx.Err()
		case result, ok := <-r.parallel.Results():
			if !ok {
				return results, errors.New("experiment runner stopped early")
			}
			if result == nil || !result.Success {
				hadFailure = true
			}
			if result != nil {
				if r.store != nil {
					if err := r.persistResult(exp, result, runIDs, startTimes); err != nil {
						return results, err
					}
				}
				r.publishVariantResult(exp, result)
				r.notifyVariantResult(ctx, exp, result)
			}
			results = append(results, result)
		}
	}

	if r.cfg.CleanupOnDone {
		_ = r.parallel.Cleanup()
	}

	finalStatus := ExperimentCompleted
	if hadFailure {
		finalStatus = ExperimentFailed
	}
	exp.Status = finalStatus
	if r.store != nil {
		if err := r.store.UpdateExperimentStatus(exp.ID, finalStatus, nil); err != nil {
			return results, err
		}
	}
	r.publishExperimentEnd(exp)
	r.notifyExperimentEnd(ctx, exp, results)

	return results, nil
}

func joinTools(values []string) string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return strings.Join(out, ",")
}

func copyContext(input map[string]string) map[string]string {
	if len(input) == 0 {
		return make(map[string]string)
	}
	out := make(map[string]string, len(input))
	for k, v := range input {
		out[k] = v
	}
	return out
}

func (r *Runner) persistResult(exp *Experiment, result *parallel.AgentResult, runIDs map[string]string, startTimes map[string]time.Time) error {
	runID := runIDs[result.TaskID]
	if runID == "" {
		runID = ulid.Make().String()
		runIDs[result.TaskID] = runID
	}

	startedAt := startTimes[result.TaskID]
	if startedAt.IsZero() {
		startedAt = time.Now().Add(-result.Duration)
	}
	completedAt := time.Now()
	status := RunCompleted
	if !result.Success {
		status = RunFailed
	}

	var errText *string
	if result.Error != nil {
		text := result.Error.Error()
		errText = &text
	}

	metrics := RunMetrics{
		DurationMs:       result.Duration.Milliseconds(),
		PromptTokens:     metricValue(result.Metrics, "prompt_tokens"),
		CompletionTokens: metricValue(result.Metrics, "completion_tokens"),
		TotalCost:        result.TotalCost,
		ToolCalls:        metricValue(result.Metrics, "tool_calls"),
		ToolSuccesses:    metricValue(result.Metrics, "tool_successes"),
		ToolFailures:     metricValue(result.Metrics, "tool_failures"),
		FilesModified:    metricValue(result.Metrics, "files_modified"),
		LinesChanged:     metricValue(result.Metrics, "lines_changed"),
	}

	run := Run{
		ID:           runID,
		ExperimentID: exp.ID,
		VariantID:    result.TaskID,
		Branch:       result.Branch,
		Status:       status,
		Output:       result.Output,
		Files:        result.Files,
		Metrics:      metrics,
		Error:        errText,
		StartedAt:    startedAt,
		CompletedAt:  &completedAt,
	}

	if err := r.store.SaveRun(&run); err != nil {
		return err
	}

	if len(exp.Criteria) > 0 && strings.TrimSpace(result.WorktreePath) != "" {
		evalTimeout := exp.Task.Timeout
		if evalTimeout <= 0 {
			evalTimeout = r.cfg.DefaultTimeout
		}
		evalCtx, cancel := context.WithTimeout(context.Background(), evalTimeout)
		defer cancel()

		evals := EvaluateCriteria(evalCtx, result.WorktreePath, exp.Task.WorkingDir, result.Output, exp.Criteria)
		if len(evals) > 0 {
			if err := r.store.ReplaceEvaluations(runID, evals); err != nil {
				return err
			}
		}
	}

	return nil
}

func metricValue(metrics map[string]int, key string) int {
	if metrics == nil {
		return 0
	}
	return metrics[key]
}

func (r *Runner) publishExperimentStart(exp *Experiment) {
	if r == nil || r.telemetry == nil || exp == nil {
		return
	}
	var variants []map[string]any
	for i := range exp.Variants {
		variant := exp.Variants[i]
		variants = append(variants, map[string]any{
			"id":    variant.ID,
			"name":  variantName(&variant),
			"model": variant.ModelID,
		})
	}
	r.telemetry.Publish(telemetry.Event{
		Type: telemetry.EventExperimentStarted,
		Data: map[string]any{
			"experiment_id": exp.ID,
			"name":          exp.Name,
			"status":        string(ExperimentRunning),
			"variants":      variants,
		},
	})
}

func (r *Runner) publishVariantEvent(eventType telemetry.EventType, exp *Experiment, variant *Variant, extra map[string]any) {
	if r == nil || r.telemetry == nil || exp == nil || variant == nil {
		return
	}
	data := map[string]any{
		"experiment_id": exp.ID,
		"experiment":    exp.Name,
		"variant_id":    variant.ID,
		"variant":       variantName(variant),
		"model_id":      variant.ModelID,
	}
	for k, v := range extra {
		data[k] = v
	}
	r.telemetry.Publish(telemetry.Event{
		Type:   eventType,
		TaskID: variant.ID,
		Data:   data,
	})
}

func (r *Runner) publishVariantResult(exp *Experiment, result *parallel.AgentResult) {
	if r == nil || r.telemetry == nil || exp == nil || result == nil {
		return
	}
	variant := findVariant(exp, result.TaskID)
	if variant == nil {
		return
	}
	status := telemetry.EventExperimentVariantCompleted
	if !result.Success {
		status = telemetry.EventExperimentVariantFailed
	}
	extra := map[string]any{
		"status":            map[bool]string{true: "completed", false: "failed"}[result.Success],
		"duration_ms":       result.Duration.Milliseconds(),
		"total_cost":        result.TotalCost,
		"prompt_tokens":     metricValue(result.Metrics, "prompt_tokens"),
		"completion_tokens": metricValue(result.Metrics, "completion_tokens"),
	}
	r.publishVariantEvent(status, exp, variant, extra)
}

func (r *Runner) publishExperimentEnd(exp *Experiment) {
	if r == nil || r.telemetry == nil || exp == nil {
		return
	}
	eventType := telemetry.EventExperimentCompleted
	if exp.Status == ExperimentFailed {
		eventType = telemetry.EventExperimentFailed
	}
	r.telemetry.Publish(telemetry.Event{
		Type: eventType,
		Data: map[string]any{
			"experiment_id": exp.ID,
			"name":          exp.Name,
			"status":        string(exp.Status),
		},
	})
}

func findVariant(exp *Experiment, id string) *Variant {
	if exp == nil {
		return nil
	}
	for i := range exp.Variants {
		if exp.Variants[i].ID == id {
			return &exp.Variants[i]
		}
	}
	return nil
}

// Notification methods for external channels (Telegram, Slack)

func (r *Runner) notifyExperimentStart(ctx context.Context, exp *Experiment) {
	if r == nil || r.notify == nil || exp == nil {
		return
	}
	variantNames := make([]string, 0, len(exp.Variants))
	for i := range exp.Variants {
		variantNames = append(variantNames, variantName(&exp.Variants[i]))
	}
	message := fmt.Sprintf("Running %d variants: %s", len(exp.Variants), strings.Join(variantNames, ", "))
	_ = r.notify.NotifyProgress(ctx, exp.ID, fmt.Sprintf("Experiment: %s", exp.Name), message)
}

func (r *Runner) notifyVariantStart(ctx context.Context, exp *Experiment, variant *Variant) {
	if r == nil || r.notify == nil || exp == nil || variant == nil {
		return
	}
	message := fmt.Sprintf("Starting variant %s (model: %s)", variantName(variant), variant.ModelID)
	_ = r.notify.NotifyProgress(ctx, exp.ID, exp.Name, message)
}

func (r *Runner) notifyVariantResult(ctx context.Context, exp *Experiment, result *parallel.AgentResult) {
	if r == nil || r.notify == nil || exp == nil || result == nil {
		return
	}
	variant := findVariant(exp, result.TaskID)
	if variant == nil {
		return
	}
	if result.Success {
		message := fmt.Sprintf("Variant %s completed in %s (cost: $%.4f)",
			variantName(variant), result.Duration.Round(time.Second), result.TotalCost)
		_ = r.notify.NotifyProgress(ctx, exp.ID, exp.Name, message)
	} else {
		errMsg := "unknown error"
		if result.Error != nil {
			errMsg = result.Error.Error()
		}
		_ = r.notify.NotifyError(ctx, exp.ID, fmt.Sprintf("%s: %s failed", exp.Name, variantName(variant)), fmt.Errorf("%s", errMsg))
	}
}

func (r *Runner) notifyExperimentEnd(ctx context.Context, exp *Experiment, results []*parallel.AgentResult) {
	if r == nil || r.notify == nil || exp == nil {
		return
	}
	succeeded := 0
	failed := 0
	var totalCost float64
	for _, result := range results {
		if result == nil {
			continue
		}
		if result.Success {
			succeeded++
		} else {
			failed++
		}
		totalCost += result.TotalCost
	}
	title := fmt.Sprintf("Experiment: %s", exp.Name)
	message := fmt.Sprintf("Completed: %d succeeded, %d failed (total cost: $%.4f)", succeeded, failed, totalCost)
	if failed > 0 {
		_ = r.notify.NotifyComplete(ctx, exp.ID, title, message)
	} else {
		_ = r.notify.NotifyComplete(ctx, exp.ID, title, message)
	}
}

// CoordinatedRunner wraps Runner with conflict-aware scheduling.
type CoordinatedRunner struct {
	*Runner
	coordinator *parallel.Coordinator
	repoPath    string
}

// NewCoordinatedRunner creates a runner with conflict-aware coordination.
func NewCoordinatedRunner(cfg RunnerConfig, deps Dependencies, repoPath string) (*CoordinatedRunner, error) {
	runner, err := NewRunner(cfg, deps)
	if err != nil {
		return nil, err
	}

	executor := &experimentExecutor{
		config:         deps.Config,
		modelManager:   deps.ModelManager,
		projectContext: deps.ProjectContext,
		telemetry:      deps.Telemetry,
	}

	coordCfg := parallel.DefaultCoordinatorConfig(repoPath)
	coordCfg.MaxAgents = cfg.MaxConcurrent
	coordinator := parallel.NewCoordinator(deps.Worktree, executor, coordCfg)

	return &CoordinatedRunner{
		Runner:      runner,
		coordinator: coordinator,
		repoPath:    repoPath,
	}, nil
}

// PreviewExecution returns a preview of how tasks would be scheduled.
func (r *CoordinatedRunner) PreviewExecution(exp *Experiment) *parallel.ExecutionPreview {
	tasks := r.buildTasks(exp)
	return r.coordinator.PreviewExecution(tasks)
}

// RunCoordinated executes the experiment with conflict-aware scheduling.
// Tasks with overlapping file scopes are automatically serialized into waves.
func (r *CoordinatedRunner) RunCoordinated(ctx context.Context, exp *Experiment, targetBranch string) (*parallel.ExecutionReport, error) {
	if exp == nil {
		return nil, errors.New("experiment is nil")
	}
	if len(exp.Variants) == 0 {
		return nil, errors.New("experiment has no variants")
	}

	tasks := r.buildTasks(exp)
	return r.coordinator.ExecuteParallel(ctx, tasks, targetBranch)
}

// buildTasks converts experiment variants to parallel agent tasks.
func (r *CoordinatedRunner) buildTasks(exp *Experiment) []*parallel.AgentTask {
	tasks := make([]*parallel.AgentTask, 0, len(exp.Variants))
	timeout := exp.Task.Timeout
	if timeout <= 0 {
		timeout = r.cfg.DefaultTimeout
	}

	for i := range exp.Variants {
		variant := &exp.Variants[i]
		contextValues := copyContext(exp.Task.Context)
		task := &parallel.AgentTask{
			ID:          variant.ID,
			Name:        variant.Name,
			Description: exp.Task.Prompt,
			Branch:      fmt.Sprintf("experiment/%s/%s", exp.ID, variant.ID),
			Prompt:      exp.Task.Prompt,
			Context:     contextValues,
		}
		contextValues["model_id"] = variant.ModelID
		contextValues["provider"] = variant.ProviderID
		contextValues["timeout"] = strconv.FormatInt(timeout.Milliseconds(), 10)
		contextValues["experiment"] = exp.ID

		if exp.Task.WorkingDir != "" {
			contextValues["working_dir"] = exp.Task.WorkingDir
		}
		if variant.SystemPrompt != nil {
			contextValues["system_prompt"] = strings.TrimSpace(*variant.SystemPrompt)
		}
		if variant.Temperature != nil {
			contextValues["temperature"] = fmt.Sprintf("%g", *variant.Temperature)
		}
		if variant.MaxTokens != nil {
			contextValues["max_tokens"] = strconv.Itoa(*variant.MaxTokens)
		}
		if len(variant.ToolsAllowed) > 0 {
			contextValues["tools_allowed"] = joinTools(variant.ToolsAllowed)
		}

		// Set file scope for conflict detection (variant overrides task)
		files := variant.Files
		if len(files) == 0 {
			files = exp.Task.Files
		}
		if len(files) > 0 {
			contextValues["files"] = strings.Join(files, ",")
		}
		scope := variant.Scope
		if len(scope) == 0 {
			scope = exp.Task.Scope
		}
		if len(scope) > 0 {
			contextValues["scope"] = strings.Join(scope, ",")
		}

		tasks = append(tasks, task)
	}

	return tasks
}

// SetConflictHandler sets a callback for conflict events.
func (r *CoordinatedRunner) SetConflictHandler(fn func(parallel.ConflictEvent)) {
	r.coordinator.SetConflictHandler(fn)
}

// SetPartitionHandler sets a callback for partition events.
func (r *CoordinatedRunner) SetPartitionHandler(fn func(parallel.PartitionEvent)) {
	r.coordinator.SetPartitionHandler(fn)
}

// SetMergeHandler sets a callback for merge events.
func (r *CoordinatedRunner) SetMergeHandler(fn func(parallel.MergeEvent)) {
	r.coordinator.SetMergeHandler(fn)
}
