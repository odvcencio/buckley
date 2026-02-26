package tui

import (
	"github.com/odvcencio/buckley/pkg/execution"
	"github.com/odvcencio/fluffyui/progress"
	"github.com/odvcencio/fluffyui/toast"
)

type strategyProgressReporter struct {
	mgr *progress.ProgressManager
}

func (r strategyProgressReporter) Start(id, title string, mode execution.ProgressMode, total int) {
	if r.mgr == nil {
		return
	}
	progressType := progress.ProgressIndeterminate
	if mode == execution.ProgressModeSteps {
		progressType = progress.ProgressSteps
	}
	r.mgr.Start(id, title, progressType, total)
}

func (r strategyProgressReporter) Update(id string, current int) {
	if r.mgr == nil {
		return
	}
	r.mgr.Update(id, current)
}

func (r strategyProgressReporter) Done(id string) {
	if r.mgr == nil {
		return
	}
	r.mgr.Done(id)
}

type strategyToastNotifier struct {
	mgr *toast.ToastManager
}

func (n strategyToastNotifier) ShowWarning(title, message string) {
	if n.mgr == nil {
		return
	}
	n.mgr.Show(toast.ToastWarning, title, message, toast.DefaultToastDuration)
}

func (n strategyToastNotifier) ShowError(title, message string) {
	if n.mgr == nil {
		return
	}
	n.mgr.Show(toast.ToastError, title, message, toast.DefaultToastDuration)
}

func progressReporterForStrategy(mgr *progress.ProgressManager) execution.ProgressReporter {
	if mgr == nil {
		return nil
	}
	return strategyProgressReporter{mgr: mgr}
}

func toastNotifierForStrategy(mgr *toast.ToastManager) execution.ToastNotifier {
	if mgr == nil {
		return nil
	}
	return strategyToastNotifier{mgr: mgr}
}

func attachStrategyUIHooks(strategy execution.ExecutionStrategy, progressMgr *progress.ProgressManager, toastMgr *toast.ToastManager) {
	if strategy == nil {
		return
	}
	if setter, ok := strategy.(interface {
		SetProgressReporter(execution.ProgressReporter)
	}); ok {
		setter.SetProgressReporter(progressReporterForStrategy(progressMgr))
	}
	if setter, ok := strategy.(interface {
		SetToastNotifier(execution.ToastNotifier)
	}); ok {
		setter.SetToastNotifier(toastNotifierForStrategy(toastMgr))
	}
}
