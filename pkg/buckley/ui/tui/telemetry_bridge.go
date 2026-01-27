package tui

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	buckleywidgets "github.com/odvcencio/buckley/pkg/buckley/ui/widgets"
	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/buckley/pkg/touch"
	"github.com/odvcencio/fluffy-ui/state"
)

// TelemetryUIBridge forwards telemetry events to the TUI for sidebar updates.
type TelemetryUIBridge struct {
	hub         *telemetry.Hub
	app         *WidgetApp
	eventCh     <-chan telemetry.Event
	unsubscribe func()
	cancel      context.CancelFunc
	wg          sync.WaitGroup

	currentTask        *state.Signal[string]
	taskProgress       *state.Signal[int]
	planTasks          *state.Signal[[]buckleywidgets.PlanTask]
	runningTools       *state.Signal[[]buckleywidgets.RunningTool]
	toolHistory        *state.Signal[[]buckleywidgets.ToolHistoryEntry]
	touchEntries       *state.Signal[[]touchEntry]
	recentFiles        *state.Signal[[]string]
	experimentID       *state.Signal[string]
	experiment         *state.Signal[string]
	experimentStatus   *state.Signal[string]
	experimentVariants *state.Signal[[]buckleywidgets.ExperimentVariant]
	rlmStatus          *state.Signal[*buckleywidgets.RLMStatus]
	rlmScratchpad      *state.Signal[[]buckleywidgets.RLMScratchpadEntry]
	circuitStatus      *state.Signal[*buckleywidgets.CircuitStatus]

	snapshot      *state.Computed[SidebarSnapshot]
	snapshotUnsub func()
}

type touchEntry struct {
	id        string
	summary   buckleywidgets.TouchSummary
	expiresAt time.Time
	startedAt time.Time
}

const maxTouchEntries = 5
const maxRLMScratchpadEntries = 3
const maxToolHistoryEntries = 20

// NewTelemetryUIBridge creates a bridge from telemetry hub to TUI sidebar.
func NewTelemetryUIBridge(hub *telemetry.Hub, app *WidgetApp) *TelemetryUIBridge {
	if hub == nil {
		return &TelemetryUIBridge{}
	}
	eventCh, unsub := hub.Subscribe()
	b := &TelemetryUIBridge{
		hub:                hub,
		app:                app,
		eventCh:            eventCh,
		unsubscribe:        unsub,
		currentTask:        state.NewSignal(""),
		taskProgress:       state.NewSignal(0),
		planTasks:          state.NewSignal([]buckleywidgets.PlanTask(nil)),
		runningTools:       state.NewSignal([]buckleywidgets.RunningTool(nil)),
		toolHistory:        state.NewSignal([]buckleywidgets.ToolHistoryEntry(nil)),
		touchEntries:       state.NewSignal([]touchEntry(nil)),
		recentFiles:        state.NewSignal([]string(nil)),
		experimentID:       state.NewSignal(""),
		experiment:         state.NewSignal(""),
		experimentStatus:   state.NewSignal(""),
		experimentVariants: state.NewSignal([]buckleywidgets.ExperimentVariant(nil)),
		rlmStatus:          state.NewSignal((*buckleywidgets.RLMStatus)(nil)),
		rlmScratchpad:      state.NewSignal([]buckleywidgets.RLMScratchpadEntry(nil)),
		circuitStatus:      state.NewSignal((*buckleywidgets.CircuitStatus)(nil)),
	}
	b.setupSnapshot()
	return b
}

// Start begins forwarding telemetry events to the TUI.
func (b *TelemetryUIBridge) Start(ctx context.Context) {
	if b == nil || b.hub == nil {
		return
	}
	ctx, b.cancel = context.WithCancel(ctx)
	b.wg.Add(1)
	go b.forwardLoop(ctx)
}

// Stop ceases forwarding and cleans up subscriptions.
func (b *TelemetryUIBridge) Stop() {
	if b == nil {
		return
	}
	if b.cancel != nil {
		b.cancel()
	}
	if b.unsubscribe != nil {
		b.unsubscribe()
	}
	b.wg.Wait()
	if b.snapshotUnsub != nil {
		b.snapshotUnsub()
		b.snapshotUnsub = nil
	}
	if b.snapshot != nil {
		b.snapshot.Stop()
		b.snapshot = nil
	}
}

func (b *TelemetryUIBridge) forwardLoop(ctx context.Context) {
	defer b.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-b.eventCh:
			if !ok {
				return
			}
			b.handleEvent(event)
		}
	}
}

func (b *TelemetryUIBridge) setupSnapshot() {
	if b == nil || b.app == nil {
		return
	}
	b.snapshot = state.NewComputedWithScheduler(b.app.stateScheduler, func() SidebarSnapshot {
		return b.buildSnapshot()
	}, b.currentTask, b.taskProgress, b.planTasks, b.runningTools, b.toolHistory, b.touchEntries, b.recentFiles,
		b.experiment, b.experimentStatus, b.experimentVariants, b.rlmStatus, b.rlmScratchpad, b.circuitStatus)
	b.snapshotUnsub = b.snapshot.SubscribeWithScheduler(b.app.stateScheduler, func() {
		if b.app == nil {
			return
		}
		b.app.Post(SidebarStateMsg{Snapshot: b.snapshot.Get()})
	})
}

func (b *TelemetryUIBridge) buildSnapshot() SidebarSnapshot {
	now := time.Now()
	tools := cloneRunningTools(b.runningTools.Get())
	toolHistory := append([]buckleywidgets.ToolHistoryEntry(nil), b.toolHistory.Get()...)
	activeTouches := summarizeTouches(b.touchEntries.Get(), now)
	recentFiles := append([]string(nil), b.recentFiles.Get()...)
	planTasks := append([]buckleywidgets.PlanTask(nil), b.planTasks.Get()...)
	variants := cloneAndSortVariants(b.experimentVariants.Get())
	rlmScratchpad := append([]buckleywidgets.RLMScratchpadEntry(nil), b.rlmScratchpad.Get()...)

	return SidebarSnapshot{
		CurrentTask:        b.currentTask.Get(),
		TaskProgress:       b.taskProgress.Get(),
		PlanTasks:          planTasks,
		RunningTools:       tools,
		ToolHistory:        toolHistory,
		ActiveTouches:      activeTouches,
		RecentFiles:        recentFiles,
		RLMStatus:          b.rlmStatus.Get(),
		RLMScratchpad:      rlmScratchpad,
		CircuitStatus:      b.circuitStatus.Get(),
		Experiment:         b.experiment.Get(),
		ExperimentStatus:   b.experimentStatus.Get(),
		ExperimentVariants: variants,
	}
}

func (b *TelemetryUIBridge) handleEvent(event telemetry.Event) {
	if b == nil {
		return
	}
	b.pruneExpiredTouches()

	switch event.Type {
	case telemetry.EventTaskStarted:
		b.handleTaskStarted(event)
	case telemetry.EventTaskCompleted:
		b.handleTaskCompleted(event)
	case telemetry.EventTaskFailed:
		b.handleTaskFailed(event)

	case telemetry.EventPlanCreated, telemetry.EventPlanUpdated:
		b.handlePlanUpdate(event)

	case telemetry.EventBuilderStarted:
		b.addRunningTool(event.TaskID, "builder", "Building...")
	case telemetry.EventBuilderCompleted, telemetry.EventBuilderFailed:
		b.removeRunningTool(event.TaskID)

	case telemetry.EventShellCommandStarted:
		cmd := getString(event.Data, "command")
		b.addRunningTool(event.TaskID, "shell", truncate(cmd, 30))
	case telemetry.EventShellCommandCompleted, telemetry.EventShellCommandFailed:
		b.removeRunningTool(event.TaskID)

	case telemetry.EventResearchStarted:
		query := getString(event.Data, "query")
		b.addRunningTool("research", "research", truncate(query, 30))
	case telemetry.EventResearchCompleted, telemetry.EventResearchFailed:
		b.removeRunningTool("research")

	case telemetry.EventEditorApply, telemetry.EventEditorInline:
		for _, path := range extractPaths(event.Data) {
			b.addRecentFile(path)
		}

	case telemetry.EventToolStarted:
		b.handleToolStarted(event)
	case telemetry.EventToolCompleted, telemetry.EventToolFailed:
		b.handleToolFinished(event)

	case telemetry.EventExperimentStarted:
		b.handleExperimentStarted(event)
	case telemetry.EventExperimentCompleted, telemetry.EventExperimentFailed:
		b.handleExperimentCompleted(event)
	case telemetry.EventExperimentVariantStarted:
		b.handleExperimentVariant(event, "running")
	case telemetry.EventExperimentVariantCompleted:
		b.handleExperimentVariant(event, "completed")
	case telemetry.EventExperimentVariantFailed:
		b.handleExperimentVariant(event, "failed")
	case telemetry.EventRLMIteration:
		b.handleRLMIteration(event)
	case telemetry.EventCircuitFailure:
		b.handleCircuitFailure(event)
	case telemetry.EventCircuitStateChange:
		b.handleCircuitStateChange(event)
	}
}

func (b *TelemetryUIBridge) handleTaskStarted(event telemetry.Event) {
	name := event.TaskID
	if data, ok := event.Data["name"].(string); ok {
		name = data
	}
	b.currentTask.Set(name)
	b.taskProgress.Set(0)
	b.planTasks.Update(func(tasks []buckleywidgets.PlanTask) []buckleywidgets.PlanTask {
		return updateTaskStatus(tasks, event.TaskID, buckleywidgets.TaskInProgress)
	})
}

func (b *TelemetryUIBridge) handleTaskCompleted(event telemetry.Event) {
	b.taskProgress.Set(100)
	b.planTasks.Update(func(tasks []buckleywidgets.PlanTask) []buckleywidgets.PlanTask {
		return updateTaskStatus(tasks, event.TaskID, buckleywidgets.TaskCompleted)
	})
	b.scheduleTaskClear(event)
}

func (b *TelemetryUIBridge) handleTaskFailed(event telemetry.Event) {
	b.taskProgress.Set(100)
	b.planTasks.Update(func(tasks []buckleywidgets.PlanTask) []buckleywidgets.PlanTask {
		return updateTaskStatus(tasks, event.TaskID, buckleywidgets.TaskFailed)
	})
	b.scheduleTaskClear(event)
}

func (b *TelemetryUIBridge) scheduleTaskClear(event telemetry.Event) {
	taskName := event.TaskID
	if data, ok := event.Data["name"].(string); ok && strings.TrimSpace(data) != "" {
		taskName = data
	}
	time.AfterFunc(500*time.Millisecond, func() {
		if b.currentTask.Get() != taskName || b.taskProgress.Get() != 100 {
			return
		}
		b.currentTask.Set("")
		b.taskProgress.Set(0)
	})
}

func (b *TelemetryUIBridge) handlePlanUpdate(event telemetry.Event) {
	if tasks, ok := event.Data["tasks"].([]any); ok {
		updated := make([]buckleywidgets.PlanTask, 0, len(tasks))
		for _, t := range tasks {
			if task, ok := t.(map[string]any); ok {
				pt := buckleywidgets.PlanTask{
					Name:   getString(task, "name"),
					Status: buckleywidgets.TaskPending,
				}
				if status := getString(task, "status"); status != "" {
					switch status {
					case "completed", "done":
						pt.Status = buckleywidgets.TaskCompleted
					case "in_progress", "running":
						pt.Status = buckleywidgets.TaskInProgress
					case "failed":
						pt.Status = buckleywidgets.TaskFailed
					}
				}
				updated = append(updated, pt)
			}
		}
		b.planTasks.Set(updated)
	}
}

func (b *TelemetryUIBridge) addRunningTool(id, toolType, desc string) {
	if strings.TrimSpace(id) == "" {
		return
	}
	tool := buckleywidgets.RunningTool{ID: id, Name: toolType, Command: desc}
	b.runningTools.Update(func(tools []buckleywidgets.RunningTool) []buckleywidgets.RunningTool {
		return upsertRunningTool(tools, tool)
	})
}

func (b *TelemetryUIBridge) removeRunningTool(id string) {
	if strings.TrimSpace(id) == "" {
		return
	}
	b.runningTools.Update(func(tools []buckleywidgets.RunningTool) []buckleywidgets.RunningTool {
		return removeRunningTool(tools, id)
	})
}

func (b *TelemetryUIBridge) addRecentFile(path string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	b.recentFiles.Update(func(files []string) []string {
		return updateRecentFiles(files, path)
	})
}

func (b *TelemetryUIBridge) handleToolStarted(event telemetry.Event) {
	summary, expiresAt := touchSummaryFromEvent(event)
	toolName := getString(event.Data, "toolName")
	desc := getString(event.Data, "description")
	if desc == "" {
		desc = getString(event.Data, "command")
	}
	if toolName != "" || desc != "" {
		b.appendToolHistory(firstNonEmpty(toolName, summary.Operation, "tool"), "running", truncate(desc, 40), event.Timestamp)
	}
	if summary.Path == "" {
		if toolName != "" && event.TaskID != "" {
			b.addRunningTool(event.TaskID, toolName, truncate(desc, 30))
		}
		return
	}
	if expiresAt.IsZero() {
		expiresAt = event.Timestamp.Add(touch.TTLForOperation(summary.Operation))
	}
	id := event.TaskID
	if id == "" {
		id = summary.Path + ":" + summary.Operation
	}
	entry := touchEntry{id: id, summary: summary, expiresAt: expiresAt, startedAt: event.Timestamp}
	b.touchEntries.Update(func(entries []touchEntry) []touchEntry {
		return upsertTouchEntry(entries, entry)
	})
	if event.TaskID != "" {
		b.addRunningTool(event.TaskID, firstNonEmpty(toolName, summary.Operation, "tool"), truncate(desc, 30))
	}
}

func (b *TelemetryUIBridge) handleToolFinished(event telemetry.Event) {
	status := "completed"
	if event.Type == telemetry.EventToolFailed {
		status = "failed"
	}
	if b.app != nil {
		cue := audioCueToolComplete
		if status == "failed" {
			cue = audioCueError
		}
		b.app.Post(AudioSFXMsg{Cue: cue})
	}
	if event.TaskID != "" {
		if tool, ok := findRunningTool(b.runningTools.Get(), event.TaskID); ok {
			b.appendToolHistory(firstNonEmpty(tool.Name, "tool"), status, truncate(tool.Command, 40), event.Timestamp)
		}
		b.removeRunningTool(event.TaskID)
		b.touchEntries.Update(func(entries []touchEntry) []touchEntry {
			return removeTouchEntry(entries, event.TaskID, buckleywidgets.TouchSummary{})
		})
		return
	}
	summary, _ := touchSummaryFromEvent(event)
	if summary.Path != "" {
		b.touchEntries.Update(func(entries []touchEntry) []touchEntry {
			return removeTouchEntry(entries, "", summary)
		})
	}
}

func (b *TelemetryUIBridge) appendToolHistory(name, status, detail string, ts time.Time) {
	entry := buckleywidgets.ToolHistoryEntry{
		Name:   strings.TrimSpace(name),
		Status: strings.TrimSpace(status),
		Detail: strings.TrimSpace(detail),
		When:   ts,
	}
	if entry.Name == "" {
		entry.Name = "tool"
	}
	b.toolHistory.Update(func(entries []buckleywidgets.ToolHistoryEntry) []buckleywidgets.ToolHistoryEntry {
		next := append([]buckleywidgets.ToolHistoryEntry(nil), entries...)
		next = append(next, entry)
		if len(next) > maxToolHistoryEntries {
			next = next[len(next)-maxToolHistoryEntries:]
		}
		return next
	})
}

func (b *TelemetryUIBridge) handleRLMIteration(event telemetry.Event) {
	status := &buckleywidgets.RLMStatus{
		Iteration:     getInt(event.Data, "iteration"),
		MaxIterations: getInt(event.Data, "max_iterations"),
		TokensUsed:    getInt(event.Data, "tokens_used"),
		Summary:       getString(event.Data, "summary"),
	}
	if ready, ok := getBool(event.Data, "ready"); ok {
		status.Ready = ready
	}
	b.rlmStatus.Set(status)
	scratchpad := parseRLMScratchpadEntries(event.Data["scratchpad"])
	if len(scratchpad) > maxRLMScratchpadEntries {
		scratchpad = scratchpad[:maxRLMScratchpadEntries]
	}
	b.rlmScratchpad.Set(scratchpad)
}

func (b *TelemetryUIBridge) handleCircuitFailure(event telemetry.Event) {
	status := b.circuitStatus.Get()
	if status == nil {
		status = &buckleywidgets.CircuitStatus{
			State:       "Closed",
			MaxFailures: getInt(event.Data, "max_failures"),
		}
	}
	status.ConsecutiveFailures = getInt(event.Data, "consecutive_num")
	status.LastError = getString(event.Data, "error")
	if willOpen, ok := getBool(event.Data, "will_open"); ok && willOpen {
		status.State = "Open"
	}
	b.circuitStatus.Set(status)
}

func (b *TelemetryUIBridge) handleCircuitStateChange(event telemetry.Event) {
	toState := getString(event.Data, "to")
	status := b.circuitStatus.Get()
	if status == nil {
		status = &buckleywidgets.CircuitStatus{}
	}
	status.State = toState
	status.LastError = getString(event.Data, "last_error")
	if toState == "Closed" {
		status.ConsecutiveFailures = 0
		status.LastError = ""
	}
	b.circuitStatus.Set(status)
}

// SetPlanTasks allows external code to set plan tasks directly.
func (b *TelemetryUIBridge) SetPlanTasks(tasks []buckleywidgets.PlanTask) {
	b.planTasks.Set(tasks)
}

// TaskProgress returns the current task progress.
func (b *TelemetryUIBridge) TaskProgress() int {
	return b.taskProgress.Get()
}

// CurrentTask returns the current task name.
func (b *TelemetryUIBridge) CurrentTask() string {
	return b.currentTask.Get()
}

func (b *TelemetryUIBridge) handleExperimentStarted(event telemetry.Event) {
	expID := getString(event.Data, "experiment_id")
	expName := getString(event.Data, "name")
	if expID != "" {
		b.experimentID.Set(expID)
	}
	if expName != "" {
		b.experiment.Set(expName)
	}
	status := getString(event.Data, "status")
	if status == "" {
		status = "running"
	}
	b.experimentStatus.Set(status)
	variants := make([]buckleywidgets.ExperimentVariant, 0)
	if raw, ok := event.Data["variants"]; ok {
		if list, ok := raw.([]any); ok {
			for _, entry := range list {
				if item, ok := entry.(map[string]any); ok {
					variantID := getString(item, "id")
					if variantID == "" {
						continue
					}
					variants = append(variants, buckleywidgets.ExperimentVariant{
						ID:      variantID,
						Name:    getString(item, "name"),
						ModelID: getString(item, "model"),
						Status:  "pending",
					})
				}
			}
		}
	}
	b.experimentVariants.Set(variants)
}

func (b *TelemetryUIBridge) handleExperimentCompleted(event telemetry.Event) {
	expID := getString(event.Data, "experiment_id")
	if expID != "" {
		current := b.experimentID.Get()
		if current != "" && expID != current {
			return
		}
	}
	status := getString(event.Data, "status")
	if status == "" {
		if event.Type == telemetry.EventExperimentFailed {
			status = "failed"
		} else {
			status = "completed"
		}
	}
	b.experimentStatus.Set(status)
}

func (b *TelemetryUIBridge) handleExperimentVariant(event telemetry.Event, status string) {
	expID := getString(event.Data, "experiment_id")
	if expID != "" {
		current := b.experimentID.Get()
		if current != "" && expID != current {
			return
		}
	}
	variantID := getString(event.Data, "variant_id")
	if variantID == "" {
		variantID = event.TaskID
	}
	if variantID == "" {
		return
	}
	update := buckleywidgets.ExperimentVariant{ID: variantID}
	update.Name = getString(event.Data, "variant")
	update.ModelID = getString(event.Data, "model_id")
	update.Status = status
	if durationMs, ok := getNumber(event.Data, "duration_ms"); ok {
		update.DurationMs = durationMs
	}
	if cost, ok := getFloat(event.Data, "total_cost"); ok {
		update.TotalCost = cost
	}
	if tokens, ok := getNumber(event.Data, "prompt_tokens"); ok {
		update.PromptTokens = tokens
	}
	if tokens, ok := getNumber(event.Data, "completion_tokens"); ok {
		update.CompletionTokens = tokens
	}

	b.experimentVariants.Update(func(variants []buckleywidgets.ExperimentVariant) []buckleywidgets.ExperimentVariant {
		return upsertVariant(variants, update)
	})
}

func (b *TelemetryUIBridge) pruneExpiredTouches() {
	entries := b.touchEntries.Get()
	if len(entries) == 0 {
		return
	}
	now := time.Now()
	alive := make([]touchEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.expiresAt.IsZero() && now.After(entry.expiresAt) {
			continue
		}
		alive = append(alive, entry)
	}
	if len(alive) == len(entries) {
		return
	}
	b.touchEntries.Set(alive)
}

func updateTaskStatus(tasks []buckleywidgets.PlanTask, taskID string, status buckleywidgets.TaskStatus) []buckleywidgets.PlanTask {
	if len(tasks) == 0 {
		return tasks
	}
	updated := append([]buckleywidgets.PlanTask(nil), tasks...)
	for i := range updated {
		if updated[i].Name == taskID {
			updated[i].Status = status
			break
		}
	}
	return updated
}

func upsertRunningTool(tools []buckleywidgets.RunningTool, tool buckleywidgets.RunningTool) []buckleywidgets.RunningTool {
	updated := make([]buckleywidgets.RunningTool, 0, len(tools)+1)
	replaced := false
	for _, existing := range tools {
		if existing.ID == tool.ID {
			updated = append(updated, tool)
			replaced = true
			continue
		}
		updated = append(updated, existing)
	}
	if !replaced {
		updated = append(updated, tool)
	}
	return updated
}

func removeRunningTool(tools []buckleywidgets.RunningTool, id string) []buckleywidgets.RunningTool {
	updated := make([]buckleywidgets.RunningTool, 0, len(tools))
	for _, tool := range tools {
		if tool.ID == id {
			continue
		}
		updated = append(updated, tool)
	}
	return updated
}

func findRunningTool(tools []buckleywidgets.RunningTool, id string) (buckleywidgets.RunningTool, bool) {
	for _, tool := range tools {
		if tool.ID == id {
			return tool, true
		}
	}
	return buckleywidgets.RunningTool{}, false
}

func updateRecentFiles(files []string, path string) []string {
	updated := make([]string, 0, len(files)+1)
	updated = append(updated, path)
	for _, f := range files {
		if f == path {
			continue
		}
		updated = append(updated, f)
	}
	if len(updated) > 5 {
		updated = updated[:5]
	}
	return updated
}

func upsertTouchEntry(entries []touchEntry, entry touchEntry) []touchEntry {
	updated := make([]touchEntry, 0, len(entries)+1)
	replaced := false
	for _, existing := range entries {
		if existing.id == entry.id {
			updated = append(updated, entry)
			replaced = true
			continue
		}
		updated = append(updated, existing)
	}
	if !replaced {
		updated = append(updated, entry)
	}
	return updated
}

func removeTouchEntry(entries []touchEntry, id string, summary buckleywidgets.TouchSummary) []touchEntry {
	updated := make([]touchEntry, 0, len(entries))
	for _, entry := range entries {
		if id != "" {
			if entry.id == id {
				continue
			}
		} else if summary.Path != "" {
			if entry.summary.Path == summary.Path && entry.summary.Operation == summary.Operation {
				continue
			}
		}
		updated = append(updated, entry)
	}
	return updated
}

func summarizeTouches(entries []touchEntry, now time.Time) []buckleywidgets.TouchSummary {
	if len(entries) == 0 {
		return nil
	}
	alive := make([]touchEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.expiresAt.IsZero() && now.After(entry.expiresAt) {
			continue
		}
		alive = append(alive, entry)
	}
	if len(alive) == 0 {
		return nil
	}
	sort.Slice(alive, func(i, j int) bool {
		return alive[i].startedAt.After(alive[j].startedAt)
	})
	if len(alive) > maxTouchEntries {
		alive = alive[:maxTouchEntries]
	}
	out := make([]buckleywidgets.TouchSummary, 0, len(alive))
	for _, entry := range alive {
		out = append(out, entry.summary)
	}
	return out
}

func cloneRunningTools(tools []buckleywidgets.RunningTool) []buckleywidgets.RunningTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]buckleywidgets.RunningTool, len(tools))
	copy(out, tools)
	return out
}

func cloneAndSortVariants(variants []buckleywidgets.ExperimentVariant) []buckleywidgets.ExperimentVariant {
	if len(variants) == 0 {
		return nil
	}
	out := make([]buckleywidgets.ExperimentVariant, len(variants))
	copy(out, variants)
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

func upsertVariant(variants []buckleywidgets.ExperimentVariant, update buckleywidgets.ExperimentVariant) []buckleywidgets.ExperimentVariant {
	updated := make([]buckleywidgets.ExperimentVariant, 0, len(variants)+1)
	replaced := false
	for _, existing := range variants {
		if existing.ID == update.ID {
			merged := existing
			if update.Name != "" {
				merged.Name = update.Name
			}
			if update.ModelID != "" {
				merged.ModelID = update.ModelID
			}
			if update.Status != "" {
				merged.Status = update.Status
			}
			if update.DurationMs != 0 {
				merged.DurationMs = update.DurationMs
			}
			if update.TotalCost != 0 {
				merged.TotalCost = update.TotalCost
			}
			if update.PromptTokens != 0 {
				merged.PromptTokens = update.PromptTokens
			}
			if update.CompletionTokens != 0 {
				merged.CompletionTokens = update.CompletionTokens
			}
			updated = append(updated, merged)
			replaced = true
			continue
		}
		updated = append(updated, existing)
	}
	if !replaced {
		updated = append(updated, update)
	}
	return updated
}

// Helper functions

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

func getString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func extractPaths(data map[string]any) []string {
	if data == nil {
		return nil
	}
	if path := getString(data, "path"); path != "" {
		return []string{path}
	}
	if path := getString(data, "filePath"); path != "" {
		return []string{path}
	}
	if raw, ok := data["files"]; ok {
		switch v := raw.(type) {
		case []string:
			return v
		case []any:
			out := make([]string, 0, len(v))
			for _, item := range v {
				if s, ok := item.(string); ok && s != "" {
					out = append(out, s)
				}
			}
			return out
		}
	}
	return nil
}

func getNumber(m map[string]any, key string) (int, bool) {
	if m == nil {
		return 0, false
	}
	value, ok := m[key]
	if !ok {
		return 0, false
	}
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case json.Number:
		if n, err := strconv.Atoi(string(v)); err == nil {
			return n, true
		}
	}
	return 0, false
}

func getBool(m map[string]any, key string) (bool, bool) {
	if m == nil {
		return false, false
	}
	value, ok := m[key]
	if !ok {
		return false, false
	}
	switch v := value.(type) {
	case bool:
		return v, true
	case string:
		lower := strings.ToLower(strings.TrimSpace(v))
		switch lower {
		case "true", "yes", "1":
			return true, true
		case "false", "no", "0":
			return false, true
		}
	}
	return false, false
}

func getFloat(m map[string]any, key string) (float64, bool) {
	if m == nil {
		return 0, false
	}
	value, ok := m[key]
	if !ok {
		return 0, false
	}
	switch v := value.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		if f, err := strconv.ParseFloat(string(v), 64); err == nil {
			return f, true
		}
	}
	return 0, false
}

func parseRLMScratchpadEntries(raw any) []buckleywidgets.RLMScratchpadEntry {
	if raw == nil {
		return nil
	}
	switch list := raw.(type) {
	case []map[string]any:
		entries := make([]buckleywidgets.RLMScratchpadEntry, 0, len(list))
		for _, entry := range list {
			entries = append(entries, buckleywidgets.RLMScratchpadEntry{
				Key:     getString(entry, "key"),
				Type:    getString(entry, "type"),
				Summary: getString(entry, "summary"),
			})
		}
		return entries
	case []any:
		entries := make([]buckleywidgets.RLMScratchpadEntry, 0, len(list))
		for _, item := range list {
			entry, ok := item.(map[string]any)
			if !ok {
				continue
			}
			entries = append(entries, buckleywidgets.RLMScratchpadEntry{
				Key:     getString(entry, "key"),
				Type:    getString(entry, "type"),
				Summary: getString(entry, "summary"),
			})
		}
		return entries
	default:
		return nil
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func getInt(m map[string]any, key string) int {
	if v, ok := m[key]; ok {
		switch val := v.(type) {
		case int:
			return val
		case int32:
			return int(val)
		case int64:
			return int(val)
		case float64:
			return int(val)
		}
	}
	return 0
}

func touchSummaryFromEvent(event telemetry.Event) (buckleywidgets.TouchSummary, time.Time) {
	data := event.Data
	path := getString(data, "filePath")
	if path == "" {
		path = getString(data, "path")
	}
	if path == "" {
		path = getString(data, "file_path")
	}
	summary := buckleywidgets.TouchSummary{
		Path:      path,
		Operation: getString(data, "operationType"),
		Ranges:    extractTouchRanges(data),
	}
	expiresAt := extractTime(data, "expiresAt")
	return summary, expiresAt
}

func extractTouchRanges(data map[string]any) []buckleywidgets.TouchRange {
	if data == nil {
		return nil
	}
	switch ranges := data["ranges"].(type) {
	case []touch.LineRange:
		out := make([]buckleywidgets.TouchRange, 0, len(ranges))
		for _, r := range ranges {
			out = append(out, buckleywidgets.TouchRange{Start: r.Start, End: r.End})
		}
		return out
	case []any:
		out := make([]buckleywidgets.TouchRange, 0, len(ranges))
		for _, item := range ranges {
			entry, ok := item.(map[string]any)
			if !ok {
				continue
			}
			start := getInt(entry, "start")
			end := getInt(entry, "end")
			if start == 0 && end == 0 {
				continue
			}
			if end == 0 {
				end = start
			}
			out = append(out, buckleywidgets.TouchRange{Start: start, End: end})
		}
		return out
	default:
		return nil
	}
}

func extractTime(data map[string]any, key string) time.Time {
	if data == nil {
		return time.Time{}
	}
	switch v := data[key].(type) {
	case time.Time:
		return v
	case string:
		if parsed, err := time.Parse(time.RFC3339, v); err == nil {
			return parsed
		}
	}
	return time.Time{}
}
