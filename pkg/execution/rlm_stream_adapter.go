package execution

import (
	"fmt"
	"strings"
	"sync"

	"github.com/odvcencio/buckley/pkg/rlm"
)

const rlmProgressID = "rlm-iterations"

// RLMStreamAdapter bridges RLM iteration events to execution stream handlers.
type RLMStreamAdapter struct {
	mu             sync.Mutex
	handler        StreamHandler
	toasts         ToastNotifier
	progress       ProgressReporter
	progressActive bool
	lastWarning    string
}

// NewRLMStreamAdapter creates a new adapter for RLM iteration events.
func NewRLMStreamAdapter(handler StreamHandler, progressReporter ProgressReporter, toastNotifier ToastNotifier) *RLMStreamAdapter {
	return &RLMStreamAdapter{
		handler:  handler,
		progress: progressReporter,
		toasts:   toastNotifier,
	}
}

// SetHandler updates the stream handler.
func (a *RLMStreamAdapter) SetHandler(handler StreamHandler) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.handler = handler
	a.mu.Unlock()
}

// SetProgressReporter updates the progress reporter.
func (a *RLMStreamAdapter) SetProgressReporter(reporter ProgressReporter) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.progress = reporter
	a.mu.Unlock()
}

// SetToastNotifier updates the toast notifier.
func (a *RLMStreamAdapter) SetToastNotifier(notifier ToastNotifier) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.toasts = notifier
	a.mu.Unlock()
}

// OnComplete forwards execution completion to the stream handler.
func (a *RLMStreamAdapter) OnComplete(result *ExecutionResult) {
	if a == nil {
		return
	}
	a.mu.Lock()
	handler := a.handler
	a.mu.Unlock()
	if handler != nil {
		handler.OnComplete(result)
	}
}

// OnReasoningEnd forwards reasoning completion to the stream handler.
func (a *RLMStreamAdapter) OnReasoningEnd() {
	if a == nil {
		return
	}
	a.mu.Lock()
	handler := a.handler
	a.mu.Unlock()
	if handler != nil {
		handler.OnReasoningEnd()
	}
}

// OnRLMEvent handles iteration events and updates downstream handlers.
func (a *RLMStreamAdapter) OnRLMEvent(event rlm.IterationEvent) {
	if a == nil {
		return
	}

	a.mu.Lock()
	handler := a.handler
	progressReporter := a.progress
	toastNotifier := a.toasts
	a.mu.Unlock()

	if handler != nil {
		label := formatIterationLabel(event)
		handler.OnToolStart("rlm_iteration", label)
		if strings.TrimSpace(event.ReasoningTrace) != "" {
			handler.OnReasoning(event.ReasoningTrace)
		}
		handler.OnToolEnd("rlm_iteration", strings.TrimSpace(event.Summary), nil)
	}

	a.updateProgress(progressReporter, event)
	a.handleBudgetWarning(toastNotifier, event.BudgetStatus)
}

func (a *RLMStreamAdapter) updateProgress(reporter ProgressReporter, event rlm.IterationEvent) {
	if reporter == nil {
		return
	}

	max := event.MaxIterations
	if max < 0 {
		max = 0
	}
	finished := event.Ready
	if max > 0 && event.Iteration >= max {
		finished = true
	}

	start := false
	a.mu.Lock()
	if !a.progressActive {
		a.progressActive = true
		start = true
	}
	a.mu.Unlock()

	if start {
		mode := ProgressModeIndeterminate
		if max > 0 {
			mode = ProgressModeSteps
		}
		reporter.Start(rlmProgressID, "RLM iterations", mode, max)
	}

	if max > 0 {
		reporter.Update(rlmProgressID, event.Iteration)
	}

	if finished {
		reporter.Done(rlmProgressID)
		a.mu.Lock()
		a.progressActive = false
		a.mu.Unlock()
	}
}

func (a *RLMStreamAdapter) handleBudgetWarning(notifier ToastNotifier, status rlm.BudgetStatus) {
	if notifier == nil {
		return
	}
	warning := strings.TrimSpace(status.Warning)
	if warning == "" {
		a.mu.Lock()
		a.lastWarning = ""
		a.mu.Unlock()
		return
	}
	a.mu.Lock()
	if warning == a.lastWarning {
		a.mu.Unlock()
		return
	}
	a.lastWarning = warning
	a.mu.Unlock()

	title := "RLM budget warning"
	switch strings.ToLower(warning) {
	case "critical":
		title = "RLM budget critical"
	case "low":
		title = "RLM budget low"
	}

	message := buildBudgetMessage(status)
	if strings.TrimSpace(message) == "" {
		message = "budget threshold reached"
	}
	if strings.EqualFold(warning, "critical") {
		notifier.ShowError(title, message)
		return
	}
	notifier.ShowWarning(title, message)
}

func buildBudgetMessage(status rlm.BudgetStatus) string {
	var parts []string
	if status.TokensPercent > 0 {
		parts = append(parts, fmt.Sprintf("tokens %.0f%%", status.TokensPercent))
	}
	if status.WallTimePercent > 0 {
		parts = append(parts, fmt.Sprintf("time %.0f%%", status.WallTimePercent))
	}
	if status.TokensRemaining > 0 {
		parts = append(parts, fmt.Sprintf("%d tokens remaining", status.TokensRemaining))
	}
	if len(parts) == 0 {
		return strings.TrimSpace(status.Warning)
	}
	return strings.Join(parts, ", ")
}

func formatIterationLabel(event rlm.IterationEvent) string {
	if event.MaxIterations > 0 {
		return fmt.Sprintf("iteration %d/%d", event.Iteration, event.MaxIterations)
	}
	return fmt.Sprintf("iteration %d", event.Iteration)
}
