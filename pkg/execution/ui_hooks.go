package execution

// ProgressMode describes the shape of progress tracking.
type ProgressMode string

const (
	ProgressModeIndeterminate ProgressMode = "indeterminate"
	ProgressModeSteps         ProgressMode = "steps"
)

// ProgressReporter receives progress lifecycle updates from strategy execution.
type ProgressReporter interface {
	Start(id, title string, mode ProgressMode, total int)
	Update(id string, current int)
	Done(id string)
}

// ToastNotifier receives warning/error notifications from strategy execution.
type ToastNotifier interface {
	ShowWarning(title, message string)
	ShowError(title, message string)
}
