package orchestrator

import (
	"fmt"
	"maps"
	"os"
	"strings"

	"github.com/odvcencio/buckley/pkg/telemetry"
)

// SendProgress sends a progress update to subscribers (non-blocking)
func (w *WorkflowManager) SendProgress(message string) {
	if w == nil {
		return
	}
	w.progressMu.Lock()
	defer w.progressMu.Unlock()
	if w.progressChan == nil {
		return
	}
	select {
	case w.progressChan <- message:
	default:
		// Channel full or no listener - skip
	}
}

func (w *WorkflowManager) emitTelemetry(event telemetry.Event) {
	if w == nil || w.telemetry == nil {
		return
	}
	w.stateMu.RLock()
	sessionID := w.sessionID
	planID := w.planID
	w.stateMu.RUnlock()
	if event.SessionID == "" {
		event.SessionID = sessionID
	}
	if event.PlanID == "" && planID != "" {
		event.PlanID = planID
	}
	w.telemetry.Publish(event)
}

// EmitPlanSnapshot publishes a plan-level telemetry event.
func (w *WorkflowManager) EmitPlanSnapshot(plan *Plan, eventType telemetry.EventType) {
	if plan == nil {
		return
	}
	if eventType == "" {
		eventType = telemetry.EventPlanUpdated
	}
	data := map[string]any{
		"plan":      plan,
		"feature":   plan.FeatureName,
		"taskCount": len(plan.Tasks),
	}
	w.emitTelemetry(telemetry.Event{
		Type:   eventType,
		PlanID: plan.ID,
		Data:   data,
	})
}

// EmitTaskEvent publishes a task-level telemetry event.
func (w *WorkflowManager) EmitTaskEvent(task *Task, eventType telemetry.EventType) {
	if task == nil {
		return
	}
	data := map[string]any{
		"task":   task,
		"title":  task.Title,
		"status": task.Status,
	}
	w.stateMu.RLock()
	pid := w.planID
	w.stateMu.RUnlock()
	w.emitTelemetry(telemetry.Event{
		Type:   eventType,
		PlanID: pid,
		TaskID: task.ID,
		Data:   data,
	})
}

// PublishTelemetry allows external components to emit arbitrary telemetry events.
func (w *WorkflowManager) PublishTelemetry(event telemetry.Event) {
	if w == nil {
		return
	}
	w.emitTelemetry(event)
}

// EmitBuilderEvent emits a builder-related telemetry event.
func (w *WorkflowManager) EmitBuilderEvent(task *Task, eventType telemetry.EventType, details map[string]any) {
	if w == nil {
		return
	}
	data := make(map[string]any, len(details))
	maps.Copy(data, details)
	if task != nil {
		data["taskId"] = task.ID
		data["title"] = task.Title
	}
	w.stateMu.RLock()
	pid := w.planID
	w.stateMu.RUnlock()
	w.emitTelemetry(telemetry.Event{
		Type:   eventType,
		PlanID: pid,
		TaskID: func() string {
			if task != nil {
				return task.ID
			}
			return ""
		}(),
		Data: data,
	})
}

// EmitResearchEvent emits a research-related telemetry event.
func (w *WorkflowManager) EmitResearchEvent(feature string, eventType telemetry.EventType, details map[string]any) {
	if w == nil {
		return
	}
	data := make(map[string]any, len(details))
	maps.Copy(data, details)
	if feature != "" {
		data["feature"] = feature
	}
	w.stateMu.RLock()
	pid := w.planID
	w.stateMu.RUnlock()
	w.emitTelemetry(telemetry.Event{
		Type:   eventType,
		PlanID: pid,
		Data:   data,
	})
}

// GetProgressChan returns the progress channel for subscription
func (w *WorkflowManager) GetProgressChan() <-chan string {
	if w == nil {
		return nil
	}
	w.progressMu.Lock()
	defer w.progressMu.Unlock()
	return w.progressChan
}

// EnableProgressStreaming creates the progress channel
func (w *WorkflowManager) EnableProgressStreaming() {
	if w != nil {
		w.progressMu.Lock()
		if w.progressChan != nil {
			close(w.progressChan)
		}
		w.progressChan = make(chan string, 100) // Buffered to prevent blocking
		w.progressMu.Unlock()
	}
}

// DisableProgressStreaming closes the progress channel
func (w *WorkflowManager) DisableProgressStreaming() {
	if w != nil {
		w.progressMu.Lock()
		if w.progressChan != nil {
			close(w.progressChan)
			w.progressChan = nil
		}
		w.progressMu.Unlock()
	}
}

// Resume clears the pause state, typically after user intervention.
func (w *WorkflowManager) Resume(resolution string) {
	if w == nil {
		return
	}
	resolution = strings.TrimSpace(resolution)
	if resolution == "" {
		resolution = "Manual resume via CLI"
	}
	if w.executionTracker != nil {
		_ = w.executionTracker.ResolvePause("Manual resume", resolution)
	}
	fmt.Fprintf(os.Stderr, "▶️  Workflow resumed: %s\n", resolution)
	w.ClearPause()
}
