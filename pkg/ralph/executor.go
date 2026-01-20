// pkg/ralph/executor.go
package ralph

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/conversation"
)

// HeadlessRunner defines the interface for executing prompts.
type HeadlessRunner interface {
	ProcessInput(ctx context.Context, input string) error
	State() string
}

// ProgressWriter receives progress updates during execution.
type ProgressWriter interface {
	io.Writer
}

// Executor runs the Ralph iteration loop.
type Executor struct {
	session      *Session
	runner       HeadlessRunner
	orchestrator *Orchestrator
	logger       *Logger
	progress     ProgressWriter
	memoryStore  *MemoryStore
	contextProc  *ContextProcessor
	summaryGen   *SummaryGenerator
	projectCtx   string
	lastBackend  string
	lastModel    string

	mu              sync.Mutex
	promptFileMtime time.Time
	lastError       error
}

// ExecutorOption configures an Executor.
type ExecutorOption func(*Executor)

// WithOrchestrator sets the backend orchestrator for multi-backend execution.
func WithOrchestrator(o *Orchestrator) ExecutorOption {
	return func(e *Executor) {
		e.orchestrator = o
	}
}

// WithProgressWriter sets the writer for progress updates.
func WithProgressWriter(w ProgressWriter) ExecutorOption {
	return func(e *Executor) {
		e.progress = w
	}
}

// WithMemoryStore attaches a session memory store.
func WithMemoryStore(store *MemoryStore) ExecutorOption {
	return func(e *Executor) {
		e.memoryStore = store
	}
}

// WithContextProcessor attaches a context processor for prompt injection.
func WithContextProcessor(processor *ContextProcessor) ExecutorOption {
	return func(e *Executor) {
		e.contextProc = processor
	}
}

// WithSummaryGenerator attaches a summary generator for session memory.
func WithSummaryGenerator(generator *SummaryGenerator) ExecutorOption {
	return func(e *Executor) {
		e.summaryGen = generator
	}
}

// WithProjectContext sets the static project context text.
func WithProjectContext(ctx string) ExecutorOption {
	return func(e *Executor) {
		e.projectCtx = ctx
	}
}

// NewExecutor creates a new executor.
func NewExecutor(session *Session, runner HeadlessRunner, logger *Logger, opts ...ExecutorOption) *Executor {
	e := &Executor{
		session: session,
		runner:  runner,
		logger:  logger,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Run executes the iteration loop until completion.
func (e *Executor) Run(ctx context.Context) error {
	if e == nil || e.session == nil {
		return fmt.Errorf("executor not initialized")
	}

	e.session.Start()

	if err := e.session.TransitionTo(StateRunning); err != nil {
		return fmt.Errorf("transition to running: %w", err)
	}

	if e.logger != nil {
		e.logger.LogSessionStart(e.session.ID, e.session.GetPrompt(), e.session.Sandbox)
	}

	// Set up timeout if configured
	if e.session.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, e.session.Timeout)
		defer cancel()
	}

	// Load initial prompt file mtime if configured
	if e.session.PromptFile != "" {
		if info, err := os.Stat(e.session.PromptFile); err == nil {
			e.mu.Lock()
			e.promptFileMtime = info.ModTime()
			e.mu.Unlock()
		}
	}

	defer func() {
		reason := "completed"
		if ctx.Err() != nil {
			reason = "timeout"
		}
		if e.session.MaxIterations > 0 && e.session.Iteration() >= e.session.MaxIterations {
			reason = "max_iterations"
		}

		e.session.TransitionTo(StateCompleted)
		stats := e.session.Stats()
		if e.logger != nil {
			e.logger.LogSessionEnd(reason, stats.Iteration, stats.TotalCost)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		// Check max iterations
		if e.session.MaxIterations > 0 && e.session.Iteration() >= e.session.MaxIterations {
			return nil
		}

		// Check for prompt file changes
		e.checkPromptReload()

		// Run iteration
		if err := e.runIteration(ctx); err != nil {
			if ctx.Err() != nil {
				return nil // Context cancelled, not an error
			}
			return err
		}
	}
}

func (e *Executor) runIteration(ctx context.Context) error {
	iteration := e.session.IncrementIteration()
	maxIter := e.session.MaxIterations

	if e.logger != nil {
		e.logger.LogIterationStartWithContext(iteration, maxIter, "", "")
	}

	// Print iteration header
	e.writeProgress("\n")
	e.writeProgress("════════════════════════════════════════════════════════════════\n")
	if maxIter > 0 {
		e.writeProgress("  ITERATION %d/%d\n", iteration, maxIter)
	} else {
		e.writeProgress("  ITERATION %d\n", iteration)
	}
	e.writeProgress("════════════════════════════════════════════════════════════════\n")

	// Start live stopwatch
	startTime := time.Now()
	stopwatchCtx, stopStopwatch := context.WithCancel(ctx)
	defer func() {
		stopStopwatch()
		e.clearStopwatchLine()
	}()
	go e.runStopwatch(stopwatchCtx, iteration, startTime)

	basePrompt := e.buildIterationPrompt(iteration)
	cfg := e.currentControlConfig()
	prompt, promptTokens, ctxErr := e.buildContextPrompt(ctx, iteration, basePrompt, cfg)
	if ctxErr != nil {
		e.logInternalError(iteration, "context_processing", ctxErr)
	}

	var err error
	var results []*BackendResult
	var selectedBackend, selectedModel string

	// Use orchestrator if available, otherwise fall back to direct runner
	if e.orchestrator != nil {
		e.orchestrator.NextIteration()

		// Check for schedule actions
		if action := e.orchestrator.EvaluateSchedule(e.lastError); action != nil {
			e.handleScheduleAction(action)
		}

		// Show which backend will be used
		selectedBackend, selectedModel = e.predictNextBackend()
		if selectedBackend != "" {
			e.writeProgress("  Backend: %s", selectedBackend)
			if selectedModel != "" {
				e.writeProgress(" (model: %s)", selectedModel)
			}
			e.writeProgress("\n")
		}

		req := BackendRequest{
			Prompt:       prompt,
			SandboxPath:  e.session.Sandbox,
			Iteration:    iteration,
			SessionID:    e.session.ID,
			SessionFiles: e.session.ModifiedFiles(),
			Context: map[string]any{
				"prompt_tokens": promptTokens,
			},
		}

		for {
			results, err = e.orchestrator.Execute(ctx, req)
			var parked ErrAllBackendsParked
			if err != nil && errors.As(err, &parked) {
				waitFor := defaultRateLimitBackoff
				if !parked.NextAvailable.IsZero() {
					waitFor = time.Until(parked.NextAvailable)
				}
				if waitFor < 0 {
					waitFor = 0
				}
				e.writeProgress("  [PARKED] All backends parked. Waiting %s...\n", waitFor.Round(time.Second))
				if waitErr := waitForDuration(ctx, waitFor); waitErr != nil {
					return nil
				}
				continue
			}
			break
		}

		// Log backend results and update session stats
		e.handleBackendResults(ctx, iteration, prompt, promptTokens, results, cfg)

		// Log comparison for parallel mode
		if len(results) > 1 && e.logger != nil {
			e.logger.LogBackendComparison(iteration, results)
		}
	} else {
		// Direct runner execution
		e.writeProgress("  Backend: direct (no orchestrator)\n")
		err = e.runner.ProcessInput(ctx, prompt)
		if err != nil {
			e.lastError = err
		}
	}

	// Show completion summary
	duration := time.Since(startTime)
	e.writeProgress("────────────────────────────────────────────────────────────────\n")
	e.writeIterationSummary(iteration, duration, results)

	if e.logger != nil {
		sessionStats := e.session.Stats()
		// Build iteration stats from results
		iterStats := IterationStats{
			Duration:           duration,
			SessionTotalTokens: sessionStats.TotalTokens,
			SessionTotalCost:   sessionStats.TotalCost,
			Backend:            selectedBackend,
			Model:              selectedModel,
			Success:            err == nil,
		}
		if err != nil {
			iterStats.Error = err.Error()
		}
		for _, r := range results {
			if r != nil {
				iterStats.TokensIn += r.TokensIn
				iterStats.TokensOut += r.TokensOut
				iterStats.FilesChanged += len(r.FilesChanged)
				if iterStats.Backend == "" && r.Backend != "" {
					iterStats.Backend = r.Backend
				}
				if iterStats.Model == "" && r.Model != "" {
					iterStats.Model = r.Model
				}
			}
		}
		e.logger.LogIterationEndWithStats(iteration, iterStats)
	}

	return err
}

// predictNextBackend returns the backend and model that will likely be used next.
func (e *Executor) predictNextBackend() (string, string) {
	if e.orchestrator == nil {
		return "", ""
	}

	cfg := e.orchestrator.Config()
	if cfg == nil {
		return "", ""
	}

	// Get first active backend from rotation order
	order := cfg.Rotation.Order
	if len(order) == 0 {
		for name := range cfg.Backends {
			order = append(order, name)
		}
		sort.Strings(order)
	}

	activeSet := make(map[string]struct{})
	if len(cfg.Override.ActiveBackends) > 0 {
		for _, name := range cfg.Override.ActiveBackends {
			activeSet[name] = struct{}{}
		}
	}

	for _, name := range order {
		if len(activeSet) > 0 {
			if _, ok := activeSet[name]; !ok {
				continue
			}
		}
		bcfg, ok := cfg.Backends[name]
		if !ok || !bcfg.Enabled {
			continue
		}
		model := bcfg.Models.Default
		if model == "" {
			if v, ok := bcfg.Options["model"]; ok {
				model = v
			}
		}
		return name, model
	}

	return "", ""
}

// writeIterationSummary writes a summary of the iteration results.
func (e *Executor) writeIterationSummary(iteration int, duration time.Duration, results []*BackendResult) {
	e.writeProgress("  Status: ")

	if len(results) == 0 {
		e.writeProgress("completed (no results)\n")
		e.writeProgress("  Duration: %s\n", duration.Round(time.Millisecond))
		return
	}

	// Check for errors
	var hasError bool
	var errMsg string
	for _, r := range results {
		if r != nil && r.Error != nil {
			hasError = true
			errMsg = r.Error.Error()
			break
		}
	}

	if hasError {
		e.writeProgress("ERROR\n")
		e.writeProgress("  Error: %s\n", truncateForDisplay(errMsg, 100))
	} else {
		e.writeProgress("OK\n")
	}

	// Aggregate stats
	var totalTokensIn, totalTokensOut int
	var totalCost float64
	var filesChanged []string
	var backend, model string

	for _, r := range results {
		if r == nil {
			continue
		}
		totalTokensIn += r.TokensIn
		totalTokensOut += r.TokensOut
		if r.Cost > 0 {
			totalCost += r.Cost
		} else {
			totalCost += r.CostEstimate
		}
		filesChanged = append(filesChanged, r.FilesChanged...)
		if backend == "" && r.Backend != "" {
			backend = r.Backend
		}
		if model == "" && r.Model != "" {
			model = r.Model
		}
	}

	e.writeProgress("  Duration: %s\n", duration.Round(time.Millisecond))
	if backend != "" {
		e.writeProgress("  Backend: %s", backend)
		if model != "" {
			e.writeProgress(" (%s)", model)
		}
		e.writeProgress("\n")
	}
	e.writeProgress("  Tokens: %d in / %d out\n", totalTokensIn, totalTokensOut)
	if totalCost > 0 {
		e.writeProgress("  Cost: $%.4f\n", totalCost)
	}
	if len(filesChanged) > 0 {
		e.writeProgress("  Files changed: %d\n", len(filesChanged))
		// Show first few files
		for i, f := range filesChanged {
			if i >= 3 {
				e.writeProgress("    ... and %d more\n", len(filesChanged)-3)
				break
			}
			e.writeProgress("    - %s\n", f)
		}
	}

	// Show session totals
	stats := e.session.Stats()
	e.writeProgress("  Session total: %d tokens, $%.4f\n", stats.TotalTokens, stats.TotalCost)
}

func truncateForDisplay(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// runStopwatch displays a live updating timer for the current iteration.
func (e *Executor) runStopwatch(ctx context.Context, iteration int, startTime time.Time) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			elapsed := time.Since(startTime).Round(time.Second)
			minutes := int(elapsed.Minutes())
			seconds := int(elapsed.Seconds()) % 60
			// Use carriage return to overwrite the line
			e.writeProgress("\rIteration %d [%d:%02d] running...  ", iteration, minutes, seconds)
		}
	}
}

// clearStopwatchLine clears the stopwatch line with carriage return and spaces.
func (e *Executor) clearStopwatchLine() {
	e.writeProgress("\r                                          \r")
}

func (e *Executor) currentControlConfig() *ControlConfig {
	if e == nil || e.orchestrator == nil {
		return nil
	}
	return e.orchestrator.Config()
}

func (e *Executor) buildContextPrompt(ctx context.Context, iteration int, base string, cfg *ControlConfig) (string, int, error) {
	promptTokens := conversation.CountTokens(base)
	if e == nil || e.contextProc == nil || cfg == nil || !cfg.ContextProcessing.Enabled {
		return base, promptTokens, nil
	}

	input := ContextInput{
		Iteration:    iteration,
		BudgetTokens: e.contextBudgetTokens(cfg),
		SessionState: e.buildSessionState(iteration),
		Summaries:    e.buildSummaryContext(ctx, iteration, cfg),
		Project:      e.projectCtx,
	}

	block, err := e.contextProc.BuildContextBlock(ctx, input)
	if err != nil {
		return base, promptTokens, err
	}

	block = strings.TrimSpace(block)
	if block == "" {
		return base, promptTokens, nil
	}

	prompt := fmt.Sprintf("<ralph-context>\n%s\n</ralph-context>\n\n%s", block, base)
	return prompt, conversation.CountTokens(prompt), nil
}

func (e *Executor) buildSessionState(iteration int) string {
	stats := e.session.Stats()
	lines := []string{
		fmt.Sprintf("iteration: %d", iteration),
		fmt.Sprintf("elapsed: %s", stats.Elapsed.Round(time.Second)),
		fmt.Sprintf("total_tokens: %d", stats.TotalTokens),
		fmt.Sprintf("total_cost: $%.4f", stats.TotalCost),
	}

	if strings.TrimSpace(e.lastBackend) != "" {
		lines = append(lines, "backend: "+e.lastBackend)
	}
	if strings.TrimSpace(e.lastModel) != "" {
		lines = append(lines, "model: "+e.lastModel)
	}
	if e.lastError != nil {
		lines = append(lines, "last_error: "+e.lastError.Error())
	}

	files := e.session.ModifiedFiles()
	if len(files) > 0 {
		lines = append(lines, fmt.Sprintf("files_modified: %d", len(files)))
		if len(files) > 10 {
			files = files[len(files)-10:]
		}
		lines = append(lines, "recent_files: "+strings.Join(files, ", "))
	}

	return strings.Join(lines, "\n")
}

func (e *Executor) buildSummaryContext(ctx context.Context, iteration int, cfg *ControlConfig) string {
	if e == nil || e.memoryStore == nil || cfg == nil || !cfg.Memory.Enabled {
		return ""
	}

	summaries, err := e.memoryStore.ListSummaries(ctx, e.session.ID, 0, 3)
	if err != nil {
		e.logInternalError(iteration, "memory_store", err)
		return ""
	}
	if len(summaries) == 0 {
		return ""
	}

	for i, j := 0, len(summaries)-1; i < j; i, j = i+1, j-1 {
		summaries[i], summaries[j] = summaries[j], summaries[i]
	}

	lines := make([]string, 0, len(summaries)*3)
	for _, summary := range summaries {
		lines = append(lines, fmt.Sprintf("Iterations %d-%d:", summary.StartIteration, summary.EndIteration))
		if strings.TrimSpace(summary.Summary) != "" {
			lines = append(lines, summary.Summary)
		}
		if len(summary.KeyDecisions) > 0 {
			lines = append(lines, "Key decisions: "+strings.Join(summary.KeyDecisions, "; "))
		}
		if len(summary.ErrorPatterns) > 0 {
			lines = append(lines, "Error patterns: "+strings.Join(summary.ErrorPatterns, "; "))
		}
		if len(summary.FilesModified) > 0 {
			lines = append(lines, "Files modified: "+strings.Join(summary.FilesModified, ", "))
		}
	}

	return strings.Join(lines, "\n")
}

func (e *Executor) contextBudgetTokens(cfg *ControlConfig) int {
	if e == nil || cfg == nil {
		return 0
	}

	budget := cfg.ContextProcessing.MaxOutputTokens
	if cfg.ContextProcessing.BudgetPct > 0 && strings.TrimSpace(e.lastModel) != "" && e.orchestrator != nil {
		provider := e.orchestrator.ContextProvider()
		if provider != nil {
			if contextLen := provider.ContextLength(e.lastModel); contextLen > 0 {
				budget = (contextLen * cfg.ContextProcessing.BudgetPct) / 100
			}
		}
	}

	if cfg.ContextProcessing.MaxOutputTokens > 0 && (budget == 0 || budget > cfg.ContextProcessing.MaxOutputTokens) {
		budget = cfg.ContextProcessing.MaxOutputTokens
	}
	if budget <= 0 {
		budget = 500
	}

	return budget
}

func (e *Executor) handleBackendResults(ctx context.Context, iteration int, prompt string, promptTokens int, results []*BackendResult, cfg *ControlConfig) {
	var iterationErr error

	for _, result := range results {
		if result == nil {
			continue
		}
		e.normalizeResultTokens(result, promptTokens)

		if strings.TrimSpace(result.Backend) != "" {
			e.lastBackend = result.Backend
		}
		if strings.TrimSpace(result.Model) != "" {
			e.lastModel = result.Model
		}

		if e.logger != nil {
			e.logger.LogBackendResult(iteration, result)
		}

		cost := result.Cost
		if cost == 0 {
			cost = result.CostEstimate
		}
		e.session.AddTokens(result.TokensIn+result.TokensOut, cost)
		for _, file := range result.FilesChanged {
			e.session.AddModifiedFile(file)
		}
		if result.Error != nil {
			iterationErr = result.Error
			e.logInternalErrorWithOutput(iteration, result.Backend, result.Error, result.Output)
		}
	}

	if iterationErr != nil {
		e.lastError = iterationErr
	} else {
		e.lastError = nil
	}

	e.updateMemory(ctx, iteration, prompt, promptTokens, results, cfg)
}

func (e *Executor) normalizeResultTokens(result *BackendResult, promptTokens int) {
	if result == nil {
		return
	}
	if result.TokensIn == 0 && promptTokens > 0 {
		result.TokensIn = promptTokens
	}
	if result.TokensOut == 0 && strings.TrimSpace(result.Output) != "" {
		result.TokensOut = conversation.CountTokens(result.Output)
	}
}

func (e *Executor) updateMemory(ctx context.Context, iteration int, prompt string, promptTokens int, results []*BackendResult, cfg *ControlConfig) {
	if e == nil || e.memoryStore == nil || cfg == nil || !cfg.Memory.Enabled {
		return
	}

	for _, result := range results {
		if result == nil {
			continue
		}
		tokensIn := result.TokensIn
		if tokensIn == 0 && promptTokens > 0 {
			tokensIn = promptTokens
		}
		tokensOut := result.TokensOut
		if tokensOut == 0 && strings.TrimSpace(result.Output) != "" {
			tokensOut = conversation.CountTokens(result.Output)
		}
		cost := result.Cost
		if cost == 0 {
			cost = result.CostEstimate
		}

		turn := &TurnRecord{
			SessionID: e.session.ID,
			Iteration: iteration,
			Timestamp: time.Now(),
			Prompt:    prompt,
			Response:  result.Output,
			Backend:   result.Backend,
			Model:     result.Model,
			TokensIn:  tokensIn,
			TokensOut: tokensOut,
			Cost:      cost,
		}
		if result.Error != nil {
			turn.Error = result.Error.Error()
		}
		if err := e.memoryStore.SaveTurn(ctx, turn); err != nil {
			e.logInternalError(iteration, "memory_store", err)
		}
	}

	if cfg.Memory.MaxRawTurns > 0 {
		if err := e.memoryStore.TrimRawTurns(ctx, e.session.ID, cfg.Memory.MaxRawTurns); err != nil {
			e.logInternalError(iteration, "memory_store", err)
		}
	}

	if cfg.Memory.SummaryInterval > 0 && e.summaryGen != nil && iteration%cfg.Memory.SummaryInterval == 0 {
		start := iteration - cfg.Memory.SummaryInterval + 1
		if start < 1 {
			start = 1
		}
		turns, err := e.memoryStore.GetTurnsInRange(ctx, e.session.ID, start, iteration)
		if err != nil {
			e.logInternalError(iteration, "memory_store", err)
			return
		}
		if len(turns) == 0 {
			return
		}
		summary, err := e.summaryGen.Generate(ctx, SummaryInput{
			SessionID:      e.session.ID,
			StartIteration: start,
			EndIteration:   iteration,
			Turns:          turns,
		})
		if err != nil {
			e.logInternalError(iteration, "summary_generator", err)
			return
		}
		summary.FilesModified = e.filesModifiedForRange(ctx, iteration, start, iteration)
		if err := e.memoryStore.SaveSummary(ctx, summary); err != nil {
			e.logInternalError(iteration, "memory_store", err)
		}
		if cfg.Memory.RetentionDays > 0 {
			if err := e.memoryStore.PruneRetention(ctx, cfg.Memory.RetentionDays); err != nil {
				e.logInternalError(iteration, "memory_store", err)
			}
		}
	}
}

func (e *Executor) filesModifiedForRange(ctx context.Context, iteration int, startIteration int, endIteration int) []string {
	if e == nil || e.memoryStore == nil {
		return nil
	}

	events, err := e.memoryStore.SearchEvents(ctx, EventQuery{
		SessionID:  e.session.ID,
		EventTypes: []string{"file_change"},
		Since:      startIteration,
		Until:      endIteration,
		Limit:      200,
	})
	if err != nil {
		e.logInternalError(iteration, "memory_store", err)
		return nil
	}

	files := make(map[string]struct{})
	for _, evt := range events {
		path := strings.TrimSpace(evt.FilePath)
		if path == "" {
			continue
		}
		files[path] = struct{}{}
	}

	if len(files) == 0 {
		return nil
	}

	out := make([]string, 0, len(files))
	for path := range files {
		out = append(out, path)
	}
	sort.Strings(out)

	return out
}

func (e *Executor) logInternalError(iteration int, backend string, err error) {
	if e == nil || e.logger == nil || err == nil {
		return
	}
	e.logger.LogError(iteration, backend, err)
}

func (e *Executor) logInternalErrorWithOutput(iteration int, backend string, err error, output string) {
	if e == nil || e.logger == nil || err == nil {
		return
	}
	e.logger.LogErrorWithOutput(iteration, backend, err, output)
}

func (e *Executor) handleScheduleAction(action *ScheduleAction) {
	if e.logger != nil {
		e.logger.LogScheduleAction(action, "schedule_trigger")
	}

	switch action.Action {
	case "pause":
		e.writeProgress("  [SCHEDULE] >>> PAUSE: %s\n", action.Reason)
		e.session.TransitionTo(StatePaused)
	case "resume":
		e.writeProgress("  [SCHEDULE] >>> RESUME\n")
		e.session.TransitionTo(StateRunning)
	case "set_mode":
		mode := strings.TrimSpace(action.Mode)
		if mode == "" {
			return
		}
		e.writeProgress("  [SCHEDULE] >>> MODE CHANGE: %s\n", mode)
		e.updateControlConfig(func(cfg *ControlConfig) {
			cfg.Mode = mode
		})
	case "set_backend":
		backend := strings.TrimSpace(action.Backend)
		if backend == "" {
			return
		}
		target := e.applyBackendSelection(func(active []string, _ string) string {
			if indexOfBackend(active, backend) == -1 {
				return ""
			}
			return backend
		}, moveBackendFirst)
		if target != "" {
			e.writeProgress("  [SCHEDULE] >>> BACKEND SWITCH: %s\n", target)
		}
	case "rotate_backend":
		target := e.applyBackendSelection(func(active []string, _ string) string {
			if len(active) == 0 {
				return ""
			}
			return nextBackendAfter(active, active[0])
		}, rotateBackendOrder)
		if target != "" {
			e.writeProgress("  [SCHEDULE] >>> BACKEND ROTATE: %s\n", target)
		}
	case "next_backend":
		current := strings.TrimSpace(e.lastBackend)
		target := e.applyBackendSelection(func(active []string, lastBackend string) string {
			if len(active) == 0 {
				return ""
			}
			use := current
			if use == "" {
				use = strings.TrimSpace(lastBackend)
			}
			return nextBackendAfter(active, use)
		}, rotateBackendOrder)
		if target != "" {
			e.writeProgress("  [SCHEDULE] >>> NEXT BACKEND: %s\n", target)
		}
	}
}

func (e *Executor) updateControlConfig(update func(cfg *ControlConfig)) bool {
	if e == nil || e.orchestrator == nil {
		return false
	}
	o := e.orchestrator
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.config == nil {
		return false
	}
	update(o.config)
	return true
}

func (e *Executor) applyBackendSelection(selectTarget func(active []string, lastBackend string) string, reorder func(order []string, target string) ([]string, bool)) string {
	if e == nil || e.orchestrator == nil {
		return ""
	}
	o := e.orchestrator
	o.mu.Lock()
	defer o.mu.Unlock()

	cfg := o.config
	if cfg == nil {
		return ""
	}

	fullOrder := backendNamesInOrder(cfg)
	if len(fullOrder) == 0 {
		return ""
	}

	activeOrder := activeBackendOrder(cfg, fullOrder)
	if len(activeOrder) == 0 {
		return ""
	}

	target := strings.TrimSpace(selectTarget(activeOrder, o.lastBackend))
	if target == "" {
		return ""
	}
	if indexOfBackend(activeOrder, target) == -1 {
		return ""
	}

	newOrder, ok := reorder(fullOrder, target)
	if !ok {
		return ""
	}

	cfg.Rotation.Order = newOrder
	o.currentBackend = 0
	o.lastRotation = time.Now()
	return target
}

func activeBackendOrder(cfg *ControlConfig, fullOrder []string) []string {
	if cfg == nil || len(fullOrder) == 0 {
		return nil
	}
	if len(cfg.Override.ActiveBackends) == 0 {
		out := make([]string, len(fullOrder))
		copy(out, fullOrder)
		return out
	}

	active := make(map[string]struct{}, len(cfg.Override.ActiveBackends))
	for _, name := range cfg.Override.ActiveBackends {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		active[name] = struct{}{}
	}
	if len(active) == 0 {
		out := make([]string, len(fullOrder))
		copy(out, fullOrder)
		return out
	}

	out := make([]string, 0, len(fullOrder))
	for _, name := range fullOrder {
		if _, ok := active[name]; ok {
			out = append(out, name)
		}
	}
	return out
}

func nextBackendAfter(order []string, current string) string {
	if len(order) == 0 {
		return ""
	}
	idx := indexOfBackend(order, current)
	if idx == -1 {
		return order[0]
	}
	return order[(idx+1)%len(order)]
}

func rotateBackendOrder(order []string, target string) ([]string, bool) {
	if len(order) == 0 {
		return nil, false
	}
	idx := indexOfBackend(order, target)
	if idx == -1 {
		return order, false
	}
	out := make([]string, 0, len(order))
	out = append(out, order[idx:]...)
	out = append(out, order[:idx]...)
	return out, true
}

func moveBackendFirst(order []string, target string) ([]string, bool) {
	if len(order) == 0 {
		return nil, false
	}
	idx := indexOfBackend(order, target)
	if idx == -1 {
		return order, false
	}
	out := make([]string, 0, len(order))
	out = append(out, order[idx])
	out = append(out, order[:idx]...)
	out = append(out, order[idx+1:]...)
	return out, true
}

func indexOfBackend(order []string, name string) int {
	for i, value := range order {
		if value == name {
			return i
		}
	}
	return -1
}

func (e *Executor) writeProgress(format string, args ...any) {
	if e.progress != nil {
		fmt.Fprintf(e.progress, format, args...)
	}
}

func (e *Executor) buildIterationPrompt(iteration int) string {
	base := e.session.GetPrompt()

	if iteration > 1 {
		base = fmt.Sprintf("[Iteration %d] Continue working on the task.\n\nOriginal task:\n%s", iteration, base)
	}

	return base
}

func (e *Executor) checkPromptReload() {
	if e.session.PromptFile == "" {
		return
	}

	info, err := os.Stat(e.session.PromptFile)
	if err != nil {
		return
	}

	e.mu.Lock()
	needsReload := info.ModTime().After(e.promptFileMtime)
	e.mu.Unlock()

	if needsReload {
		content, err := os.ReadFile(e.session.PromptFile)
		if err != nil {
			return
		}

		e.session.SetPrompt(string(content))
		e.mu.Lock()
		e.promptFileMtime = info.ModTime()
		e.mu.Unlock()

		if e.logger != nil {
			e.logger.LogPromptReload(e.session.PromptFile)
		}
	}
}

// Pause pauses the executor.
func (e *Executor) Pause() error {
	if e == nil || e.session == nil {
		return fmt.Errorf("executor not initialized")
	}
	return e.session.TransitionTo(StatePaused)
}

// Resume resumes the executor.
func (e *Executor) Resume() error {
	if e == nil || e.session == nil {
		return fmt.Errorf("executor not initialized")
	}
	return e.session.TransitionTo(StateRunning)
}

func waitForDuration(ctx context.Context, waitFor time.Duration) error {
	if waitFor <= 0 {
		return nil
	}
	timer := time.NewTimer(waitFor)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
