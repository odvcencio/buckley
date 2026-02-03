package services

import (
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/buckley/ui/tui/state"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/fluffyui/progress"
	fstate "github.com/odvcencio/fluffyui/state"
	"github.com/odvcencio/fluffyui/toast"
)

// StatusService manages status-related state updates.
type StatusService struct {
	state    *state.AppState
	modelMgr *model.Manager

	overrideMu    sync.Mutex
	overrideTimer *time.Timer
}

// NewStatusService creates a new status service.
func NewStatusService(s *state.AppState, mm *model.Manager) *StatusService {
	return &StatusService{
		state:    s,
		modelMgr: mm,
	}
}

// SetStatus updates the base status text.
func (svc *StatusService) SetStatus(text string) {
	if svc == nil || svc.state == nil {
		return
	}
	svc.state.StatusText.Set(text)
}

// SetStatusOverride temporarily overrides the status text.
func (svc *StatusService) SetStatusOverride(text string, duration time.Duration) {
	if svc == nil || svc.state == nil {
		return
	}
	svc.state.StatusOverride.Set(text)
	if duration <= 0 {
		return
	}

	svc.overrideMu.Lock()
	if svc.overrideTimer != nil {
		svc.overrideTimer.Stop()
	}
	svc.overrideTimer = time.AfterFunc(duration, func() {
		svc.state.StatusOverride.Set("")
	})
	svc.overrideMu.Unlock()
}

// SetMode updates the execution mode display.
func (svc *StatusService) SetMode(mode string) {
	if svc == nil || svc.state == nil {
		return
	}
	svc.state.StatusMode.Set(mode)
}

// SetTokenCount updates token and cost display (cost in cents).
func (svc *StatusService) SetTokenCount(tokens int, costCents float64) {
	if svc == nil || svc.state == nil {
		return
	}
	fstate.Batch(func() {
		svc.state.StatusTokens.Set(tokens)
		svc.state.StatusCost.Set(costCents)
	})
}

// UpdateTokens updates token and cost display from usage.
func (svc *StatusService) UpdateTokens(usage model.Usage, modelID string) {
	if svc == nil || svc.state == nil {
		return
	}
	fstate.Batch(func() {
		svc.state.StatusTokens.Set(usage.TotalTokens)
		if svc.modelMgr == nil {
			return
		}
		if cost, err := svc.modelMgr.CalculateCost(modelID, usage); err == nil {
			svc.state.StatusCost.Set(cost * 100)
		}
	})
}

// SetContextUsage updates context usage display.
func (svc *StatusService) SetContextUsage(used, budget, window int) {
	if svc == nil || svc.state == nil {
		return
	}
	fstate.Batch(func() {
		svc.state.ContextUsed.Set(used)
		svc.state.ContextBudget.Set(budget)
		svc.state.ContextWindow.Set(window)
	})
}

// SetScrollPosition updates the scroll position indicator.
func (svc *StatusService) SetScrollPosition(pos string) {
	if svc == nil || svc.state == nil {
		return
	}
	svc.state.ScrollPos.Set(pos)
}

// SetProgress updates active progress indicators.
func (svc *StatusService) SetProgress(items []progress.Progress) {
	if svc == nil || svc.state == nil {
		return
	}
	if len(items) == 0 {
		svc.state.ProgressItems.Set(nil)
		return
	}
	cloned := append([]progress.Progress(nil), items...)
	svc.state.ProgressItems.Set(cloned)
}

// SetToasts updates active toasts.
func (svc *StatusService) SetToasts(items []*toast.Toast) {
	if svc == nil || svc.state == nil {
		return
	}
	if len(items) == 0 {
		svc.state.Toasts.Set(nil)
		return
	}
	cloned := append([]*toast.Toast(nil), items...)
	svc.state.Toasts.Set(cloned)
}

// SetStreaming updates the streaming indicator.
func (svc *StatusService) SetStreaming(streaming bool) {
	if svc == nil || svc.state == nil {
		return
	}
	svc.state.IsStreaming.Set(streaming)
}
