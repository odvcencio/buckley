package execution

import (
	"fmt"
	"strings"
	"sync"

	"github.com/odvcencio/buckley/pkg/rlm"
	"github.com/odvcencio/fluffy-ui/progress"
	"github.com/odvcencio/fluffy-ui/toast"
)

const rlmProgressID = "rlm-iterations"

// RLMStreamAdapter bridges RLM iteration events to execution stream handlers.
type RLMStreamAdapter struct {
	mu             sync.Mutex
	handler        StreamHandler
	toasts         *toast.ToastManager
	progress       *progress.ProgressManager
	progressActive bool
	lastWarning    string
}

// NewRLMStreamAdapter creates a new adapter for RLM iteration events.
func NewRLMStreamAdapter(handler StreamHandler, progressMgr *progress.ProgressManager, toastMgr *toast.ToastManager) *RLMStreamAdapter {
	return &RLMStreamAdapter{
		handler:  handler,
		progress: progressMgr,
		toasts:   toastMgr,
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

// SetProgressManager updates the progress manager.
func (a *RLMStreamAdapter) SetProgressManager(manager *progress.ProgressManager) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.progress = manager
	a.mu.Unlock()
}

// SetToastManager updates the toast manager.
func (a *RLMStreamAdapter) SetToastManager(manager *toast.ToastManager) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.toasts = manager
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

// OnRLMEvent handles iteration events and updates downstream handlers.
func (a *RLMStreamAdapter) OnRLMEvent(event rlm.IterationEvent) {
	if a == nil {
		return
	}

	a.mu.Lock()
	handler := a.handler
	progressMgr := a.progress
	toastMgr := a.toasts
	a.mu.Unlock()

	if handler != nil {
		label := formatIterationLabel(event)
		handler.OnToolStart("rlm_iteration", label)
		if strings.TrimSpace(event.ReasoningTrace) != "" {
			handler.OnReasoning(event.ReasoningTrace)
		}
		handler.OnToolEnd("rlm_iteration", strings.TrimSpace(event.Summary), nil)
	}

	a.updateProgress(progressMgr, event)
	a.handleBudgetWarning(toastMgr, event.BudgetStatus)
}

func (a *RLMStreamAdapter) updateProgress(manager *progress.ProgressManager, event rlm.IterationEvent) {
	if manager == nil {
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
		ptype := progress.ProgressIndeterminate
		if max > 0 {
			ptype = progress.ProgressSteps
		}
		manager.Start(rlmProgressID, "RLM iterations", ptype, max)
	}

	if max > 0 {
		manager.Update(rlmProgressID, event.Iteration)
	}

	if finished {
		manager.Done(rlmProgressID)
		a.mu.Lock()
		a.progressActive = false
		a.mu.Unlock()
	}
}

func (a *RLMStreamAdapter) handleBudgetWarning(manager *toast.ToastManager, status rlm.BudgetStatus) {
	if manager == nil {
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

	level := toast.ToastWarning
	title := "RLM budget warning"
	switch strings.ToLower(warning) {
	case "critical":
		level = toast.ToastError
		title = "RLM budget critical"
	case "low":
		title = "RLM budget low"
	}

	message := buildBudgetMessage(status)
	if strings.TrimSpace(message) == "" {
		message = "budget threshold reached"
	}

	manager.Show(level, title, message, toast.DefaultToastDuration)
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
