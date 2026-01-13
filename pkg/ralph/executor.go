// pkg/ralph/executor.go
package ralph

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
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

	if e.logger != nil {
		e.logger.LogIterationStart(iteration)
	}

	// Progress feedback
	e.writeProgress("Iteration %d started...\n", iteration)

	prompt := e.buildIterationPrompt()
	startTime := time.Now()

	var err error
	var results []*BackendResult

	// Use orchestrator if available, otherwise fall back to direct runner
	if e.orchestrator != nil {
		e.orchestrator.NextIteration()

		// Check for schedule actions
		if action := e.orchestrator.EvaluateSchedule(e.lastError); action != nil {
			e.handleScheduleAction(action)
		}

		req := BackendRequest{
			Prompt:      prompt,
			SandboxPath: e.session.Sandbox,
			Iteration:   iteration,
			SessionID:   e.session.ID,
		}

		results, err = e.orchestrator.Execute(ctx, req)

		// Log backend results and update session stats
		for _, result := range results {
			if result == nil {
				continue
			}
			if e.logger != nil {
				e.logger.LogBackendResult(iteration, result)
			}
			// Update session stats from result
			e.session.AddTokens(result.TokensIn+result.TokensOut, result.Cost)
			for _, file := range result.FilesChanged {
				e.session.AddModifiedFile(file)
			}
			if result.Error != nil {
				e.lastError = result.Error
			}
		}

		// Log comparison for parallel mode
		if len(results) > 1 && e.logger != nil {
			e.logger.LogBackendComparison(iteration, results)
		}
	} else {
		// Direct runner execution
		err = e.runner.ProcessInput(ctx, prompt)
		if err != nil {
			e.lastError = err
		}
	}

	duration := time.Since(startTime)
	e.writeProgress("Iteration %d completed in %s\n", iteration, duration.Round(time.Millisecond))

	if e.logger != nil {
		stats := e.session.Stats()
		e.logger.LogIterationEnd(iteration, stats.TotalTokens, stats.TotalCost)
	}

	return err
}

func (e *Executor) handleScheduleAction(action *ScheduleAction) {
	if e.logger != nil {
		e.logger.LogScheduleAction(action, "schedule_trigger")
	}

	switch action.Action {
	case "pause":
		e.writeProgress("Schedule triggered pause: %s\n", action.Reason)
		e.session.TransitionTo(StatePaused)
	case "set_mode":
		e.writeProgress("Schedule switching mode to: %s\n", action.Mode)
		if e.orchestrator != nil && e.orchestrator.config != nil {
			e.orchestrator.config.Mode = action.Mode
		}
	}
}

func (e *Executor) writeProgress(format string, args ...any) {
	if e.progress != nil {
		fmt.Fprintf(e.progress, format, args...)
	}
}

func (e *Executor) buildIterationPrompt() string {
	base := e.session.GetPrompt()

	iteration := e.session.Iteration()
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
