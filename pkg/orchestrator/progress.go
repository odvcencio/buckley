package orchestrator

import (
	"fmt"
	"strings"
	"time"
)

// PlanStatus represents the overall status of a plan
type PlanStatus string

const (
	StatusPlanning  PlanStatus = "planning"
	StatusActive    PlanStatus = "active"
	StatusCompleted PlanStatus = "completed"
	StatusPaused    PlanStatus = "paused"
	StatusFailed    PlanStatus = "failed"
	StatusCancelled PlanStatus = "cancelled"
)

// ProgressInfo represents the current progress of a plan
type ProgressInfo struct {
	PlanID          string
	PlanName        string
	Status          PlanStatus
	TotalTasks      int
	CompletedTasks  int
	InProgressTasks int
	FailedTasks     int
	PendingTasks    int
	CurrentPhase    string
	Phases          []PhaseProgress
	StartedAt       time.Time
	Duration        time.Duration
	ETA             time.Duration
}

// PhaseProgress represents progress within a single phase
type PhaseProgress struct {
	Name        string
	Status      string // pending, in_progress, completed, skipped
	StartedAt   time.Time
	CompletedAt time.Time
	Duration    time.Duration
}

// ProgressTracker tracks and visualizes plan execution progress
type ProgressTracker struct {
	plan      *Plan
	startTime time.Time
	phases    []PhaseProgress
}

// NewProgressTracker creates a new progress tracker for a plan
func NewProgressTracker(plan *Plan) *ProgressTracker {
	return &ProgressTracker{
		plan:      plan,
		startTime: time.Now(),
		phases:    make([]PhaseProgress, 0),
	}
}

// GetProgressInfo returns current progress information
func (pt *ProgressTracker) GetProgressInfo() *ProgressInfo {
	if pt.plan == nil {
		return nil
	}

	completed := 0
	inProgress := 0
	failed := 0
	pending := 0

	for _, task := range pt.plan.Tasks {
		switch task.Status {
		case TaskCompleted:
			completed++
		case TaskInProgress:
			inProgress++
		case TaskFailed:
			failed++
		default:
			pending++
		}
	}

	total := len(pt.plan.Tasks)
	duration := time.Since(pt.startTime)

	// Estimate ETA based on current pace
	var eta time.Duration
	if completed > 0 && pending > 0 {
		avgPerTask := duration / time.Duration(completed)
		eta = avgPerTask * time.Duration(pending)
	}

	// Derive status from task states
	status := pt.deriveStatus(completed, inProgress, failed, total)

	return &ProgressInfo{
		PlanID:          pt.plan.ID,
		PlanName:        pt.plan.FeatureName,
		Status:          status,
		TotalTasks:      total,
		CompletedTasks:  completed,
		InProgressTasks: inProgress,
		FailedTasks:     failed,
		PendingTasks:    pending,
		CurrentPhase:    pt.getCurrentPhase(),
		Phases:          pt.phases,
		StartedAt:       pt.startTime,
		Duration:        duration,
		ETA:             eta,
	}
}

func (pt *ProgressTracker) deriveStatus(completed, inProgress, failed, total int) PlanStatus {
	if total == 0 {
		return StatusPlanning
	}
	if completed == total {
		return StatusCompleted
	}
	if failed > 0 && inProgress == 0 {
		return StatusFailed
	}
	if inProgress > 0 {
		return StatusActive
	}
	return StatusPlanning
}

func (pt *ProgressTracker) getCurrentPhase() string {
	// Return the active phase from our tracker
	for i := len(pt.phases) - 1; i >= 0; i-- {
		if pt.phases[i].Status == "in_progress" {
			return pt.phases[i].Name
		}
	}
	return ""
}

// StartPhase marks a phase as started
func (pt *ProgressTracker) StartPhase(name string) {
	pt.phases = append(pt.phases, PhaseProgress{
		Name:      name,
		Status:    "in_progress",
		StartedAt: time.Now(),
	})
}

// CompletePhase marks a phase as completed
func (pt *ProgressTracker) CompletePhase(name string) {
	for i := range pt.phases {
		if pt.phases[i].Name == name && pt.phases[i].Status == "in_progress" {
			pt.phases[i].Status = "completed"
			pt.phases[i].CompletedAt = time.Now()
			pt.phases[i].Duration = pt.phases[i].CompletedAt.Sub(pt.phases[i].StartedAt)
			break
		}
	}
}

// RenderProgressBar creates a visual progress bar
func RenderProgressBar(progress *ProgressInfo, width int) string {
	if progress == nil || progress.TotalTasks == 0 {
		return ""
	}

	if width < 10 {
		width = 40
	}

	barWidth := width - 10 // Leave space for percentage
	completed := float64(progress.CompletedTasks) / float64(progress.TotalTasks)
	filledWidth := int(completed * float64(barWidth))

	var bar strings.Builder
	bar.WriteString("[")

	for i := 0; i < barWidth; i++ {
		if i < filledWidth {
			bar.WriteString("█")
		} else if i == filledWidth && progress.InProgressTasks > 0 {
			bar.WriteString("▓")
		} else {
			bar.WriteString("░")
		}
	}

	bar.WriteString("]")
	bar.WriteString(fmt.Sprintf(" %3d%%", int(completed*100)))

	return bar.String()
}

// RenderCompactProgress renders a compact one-line progress summary
func RenderCompactProgress(progress *ProgressInfo) string {
	if progress == nil {
		return ""
	}

	status := statusEmoji(progress.Status)
	tasks := fmt.Sprintf("%d/%d", progress.CompletedTasks, progress.TotalTasks)

	parts := []string{status, progress.PlanName, tasks}

	if progress.CurrentPhase != "" {
		parts = append(parts, fmt.Sprintf("[%s]", progress.CurrentPhase))
	}

	if progress.FailedTasks > 0 {
		parts = append(parts, fmt.Sprintf("⚠️ %d failed", progress.FailedTasks))
	}

	return strings.Join(parts, " ")
}

func statusEmoji(status PlanStatus) string {
	switch status {
	case StatusPlanning:
		return "📝"
	case StatusActive:
		return "🔄"
	case StatusCompleted:
		return "✅"
	case StatusPaused:
		return "⏸️"
	case StatusFailed:
		return "❌"
	case StatusCancelled:
		return "🚫"
	default:
		return "⏳"
	}
}

// RenderDetailedProgress renders a detailed multi-line progress view
func RenderDetailedProgress(progress *ProgressInfo) string {
	if progress == nil {
		return ""
	}

	var out strings.Builder

	// Header
	out.WriteString(fmt.Sprintf("╭─ %s (%s)\n", progress.PlanName, progress.PlanID))
	out.WriteString("│\n")

	// Progress bar
	bar := RenderProgressBar(progress, 50)
	out.WriteString(fmt.Sprintf("│ %s\n", bar))
	out.WriteString("│\n")

	// Task counts
	out.WriteString(fmt.Sprintf("│ Tasks: %d completed, %d in progress, %d pending",
		progress.CompletedTasks, progress.InProgressTasks, progress.PendingTasks))
	if progress.FailedTasks > 0 {
		out.WriteString(fmt.Sprintf(", %d failed", progress.FailedTasks))
	}
	out.WriteString("\n")

	// Duration and ETA
	if progress.Duration > 0 {
		out.WriteString(fmt.Sprintf("│ Time: %s elapsed", formatDuration(progress.Duration)))
		if progress.ETA > 0 {
			out.WriteString(fmt.Sprintf(", ~%s remaining", formatDuration(progress.ETA)))
		}
		out.WriteString("\n")
	}

	// Current phase
	if progress.CurrentPhase != "" {
		out.WriteString(fmt.Sprintf("│ Phase: %s\n", progress.CurrentPhase))
	}

	// Phase timeline
	if len(progress.Phases) > 0 {
		out.WriteString("│\n")
		out.WriteString("│ Phases:\n")
		for _, phase := range progress.Phases {
			icon := "○"
			switch phase.Status {
			case "completed":
				icon = "●"
			case "in_progress":
				icon = "◐"
			case "skipped":
				icon = "○"
			}
			out.WriteString(fmt.Sprintf("│   %s %s", icon, phase.Name))
			if phase.Duration > 0 {
				out.WriteString(fmt.Sprintf(" (%s)", formatDuration(phase.Duration)))
			}
			out.WriteString("\n")
		}
	}

	out.WriteString("╰─\n")

	return out.String()
}

// RenderTaskList renders a list of tasks with their status
func RenderTaskList(plan *Plan, limit int) string {
	if plan == nil || len(plan.Tasks) == 0 {
		return "No tasks"
	}

	var out strings.Builder
	shown := 0

	for _, task := range plan.Tasks {
		if limit > 0 && shown >= limit {
			remaining := len(plan.Tasks) - shown
			out.WriteString(fmt.Sprintf("  ... and %d more tasks\n", remaining))
			break
		}

		icon := taskStatusIcon(task.Status)
		out.WriteString(fmt.Sprintf("  %s %s\n", icon, task.Description))
		shown++
	}

	return out.String()
}

func taskStatusIcon(status TaskStatus) string {
	switch status {
	case TaskCompleted:
		return "✓"
	case TaskInProgress:
		return "▶"
	case TaskFailed:
		return "✗"
	case TaskSkipped:
		return "○"
	default:
		return "·"
	}
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

// ProgressCallback is a function called when progress changes
type ProgressCallback func(progress *ProgressInfo)

// ProgressObserver observes plan execution and reports progress
type ProgressObserver struct {
	tracker  *ProgressTracker
	callback ProgressCallback
}

// NewProgressObserver creates a new progress observer
func NewProgressObserver(plan *Plan, callback ProgressCallback) *ProgressObserver {
	return &ProgressObserver{
		tracker:  NewProgressTracker(plan),
		callback: callback,
	}
}

// OnTaskUpdate is called when a task status changes
func (po *ProgressObserver) OnTaskUpdate(taskID string, status TaskStatus) {
	if po.callback != nil {
		po.callback(po.tracker.GetProgressInfo())
	}
}

// OnPhaseStart is called when a phase starts
func (po *ProgressObserver) OnPhaseStart(phase string) {
	po.tracker.StartPhase(phase)
	if po.callback != nil {
		po.callback(po.tracker.GetProgressInfo())
	}
}

// OnPhaseComplete is called when a phase completes
func (po *ProgressObserver) OnPhaseComplete(phase string) {
	po.tracker.CompletePhase(phase)
	if po.callback != nil {
		po.callback(po.tracker.GetProgressInfo())
	}
}

// GetProgress returns current progress info
func (po *ProgressObserver) GetProgress() *ProgressInfo {
	return po.tracker.GetProgressInfo()
}
