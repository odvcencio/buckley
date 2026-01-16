package tui

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/buckley/pkg/touch"
	buckleywidgets "github.com/odvcencio/buckley/pkg/ui/widgets/buckley"
)

// TelemetryUIBridge forwards telemetry events to the TUI for sidebar updates.
type TelemetryUIBridge struct {
	hub         *telemetry.Hub
	app         *WidgetApp
	eventCh     <-chan telemetry.Event
	unsubscribe func()
	cancel      context.CancelFunc
	wg          sync.WaitGroup

	// State tracking for sidebar
	mu                 sync.Mutex
	currentTask        string
	taskProgress       int
	planTasks          []buckleywidgets.PlanTask
	runningTools       map[string]buckleywidgets.RunningTool
	activeTouches      map[string]touchEntry
	recentFiles        []string
	experimentID       string
	experiment         string
	experimentStatus   string
	experimentVariants map[string]buckleywidgets.ExperimentVariant
	rlmStatus          *buckleywidgets.RLMStatus
	rlmScratchpad      []buckleywidgets.RLMScratchpadEntry
	circuitStatus      *buckleywidgets.CircuitStatus
}

type touchEntry struct {
	summary   buckleywidgets.TouchSummary
	expiresAt time.Time
	startedAt time.Time
}

const maxTouchEntries = 5
const maxRLMScratchpadEntries = 3

// NewTelemetryUIBridge creates a bridge from telemetry hub to TUI sidebar.
func NewTelemetryUIBridge(hub *telemetry.Hub, app *WidgetApp) *TelemetryUIBridge {
	eventCh, unsub := hub.Subscribe()
	return &TelemetryUIBridge{
		hub:                hub,
		app:                app,
		eventCh:            eventCh,
		unsubscribe:        unsub,
		runningTools:       make(map[string]buckleywidgets.RunningTool),
		activeTouches:      make(map[string]touchEntry),
		experimentVariants: make(map[string]buckleywidgets.ExperimentVariant),
	}
}

// Start begins forwarding telemetry events to the TUI.
func (b *TelemetryUIBridge) Start(ctx context.Context) {
	ctx, b.cancel = context.WithCancel(ctx)
	b.wg.Add(1)
	go b.forwardLoop(ctx)
}

// Stop ceases forwarding and cleans up subscriptions.
func (b *TelemetryUIBridge) Stop() {
	if b.cancel != nil {
		b.cancel()
	}
	if b.unsubscribe != nil {
		b.unsubscribe()
	}
	b.wg.Wait()
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

func (b *TelemetryUIBridge) handleEvent(event telemetry.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch event.Type {
	// Task events
	case telemetry.EventTaskStarted:
		b.handleTaskStarted(event)
	case telemetry.EventTaskCompleted:
		b.handleTaskCompleted(event)
	case telemetry.EventTaskFailed:
		b.handleTaskFailed(event)

	// Plan events
	case telemetry.EventPlanCreated, telemetry.EventPlanUpdated:
		b.handlePlanUpdate(event)

	// Builder events (running tools)
	case telemetry.EventBuilderStarted:
		b.addRunningTool(event.TaskID, "builder", "Building...")
	case telemetry.EventBuilderCompleted, telemetry.EventBuilderFailed:
		b.removeRunningTool(event.TaskID)

	// Shell events (running tools)
	case telemetry.EventShellCommandStarted:
		cmd := ""
		if data, ok := event.Data["command"].(string); ok {
			cmd = data
		}
		b.addRunningTool(event.TaskID, "shell", truncate(cmd, 30))
	case telemetry.EventShellCommandCompleted, telemetry.EventShellCommandFailed:
		b.removeRunningTool(event.TaskID)

	// Research events
	case telemetry.EventResearchStarted:
		query := ""
		if data, ok := event.Data["query"].(string); ok {
			query = data
		}
		b.addRunningTool("research", "research", truncate(query, 30))
	case telemetry.EventResearchCompleted, telemetry.EventResearchFailed:
		b.removeRunningTool("research")

	// Editor events (recent files)
	case telemetry.EventEditorApply, telemetry.EventEditorInline:
		for _, path := range extractPaths(event.Data) {
			b.addRecentFile(path)
		}

	// Tool events (active touches)
	case telemetry.EventToolStarted:
		b.handleToolStarted(event)
	case telemetry.EventToolCompleted, telemetry.EventToolFailed:
		b.handleToolFinished(event)

	// Experiment events
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
	b.currentTask = name
	b.taskProgress = 0
	b.updateTaskStatus(event.TaskID, buckleywidgets.TaskInProgress)
	b.updateSidebar()
}

func (b *TelemetryUIBridge) handleTaskCompleted(event telemetry.Event) {
	b.taskProgress = 100
	b.updateTaskStatus(event.TaskID, buckleywidgets.TaskCompleted)
	b.updateSidebar()

	// Clear current task after a brief delay
	go func() {
		time.Sleep(500 * time.Millisecond)
		b.mu.Lock()
		if b.taskProgress == 100 {
			b.currentTask = ""
			b.taskProgress = 0
		}
		b.mu.Unlock()
		b.updateSidebar()
	}()
}

func (b *TelemetryUIBridge) handleTaskFailed(event telemetry.Event) {
	b.updateTaskStatus(event.TaskID, buckleywidgets.TaskFailed)
	b.currentTask = ""
	b.taskProgress = 0
	b.updateSidebar()
}

func (b *TelemetryUIBridge) handlePlanUpdate(event telemetry.Event) {
	// Extract tasks from plan data if available
	if tasks, ok := event.Data["tasks"].([]any); ok {
		b.planTasks = nil
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
				b.planTasks = append(b.planTasks, pt)
			}
		}
	}
	b.updateSidebar()
}

func (b *TelemetryUIBridge) updateTaskStatus(taskID string, status buckleywidgets.TaskStatus) {
	for i := range b.planTasks {
		if b.planTasks[i].Name == taskID {
			b.planTasks[i].Status = status
			break
		}
	}
}

func (b *TelemetryUIBridge) addRunningTool(id, toolType, desc string) {
	b.runningTools[id] = buckleywidgets.RunningTool{
		ID:      id,
		Name:    toolType,
		Command: desc,
	}
	b.updateSidebar()
}

func (b *TelemetryUIBridge) removeRunningTool(id string) {
	delete(b.runningTools, id)
	b.updateSidebar()
}

func (b *TelemetryUIBridge) addRecentFile(path string) {
	// Remove if already exists
	for i, f := range b.recentFiles {
		if f == path {
			b.recentFiles = append(b.recentFiles[:i], b.recentFiles[i+1:]...)
			break
		}
	}
	// Add to front
	b.recentFiles = append([]string{path}, b.recentFiles...)
	// Cap at 5
	if len(b.recentFiles) > 5 {
		b.recentFiles = b.recentFiles[:5]
	}
	b.updateSidebar()
}

func (b *TelemetryUIBridge) handleToolStarted(event telemetry.Event) {
	summary, expiresAt := touchSummaryFromEvent(event)
	toolName := getString(event.Data, "toolName")
	if summary.Path == "" {
		desc := getString(event.Data, "description")
		if desc == "" {
			desc = getString(event.Data, "command")
		}
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
	b.activeTouches[id] = touchEntry{
		summary:   summary,
		expiresAt: expiresAt,
		startedAt: event.Timestamp,
	}
	if event.TaskID != "" {
		desc := getString(event.Data, "description")
		if desc == "" {
			desc = getString(event.Data, "command")
		}
		b.addRunningTool(event.TaskID, firstNonEmpty(toolName, summary.Operation, "tool"), truncate(desc, 30))
	}
	b.updateSidebar()
}

func (b *TelemetryUIBridge) handleToolFinished(event telemetry.Event) {
	if event.TaskID != "" {
		b.removeRunningTool(event.TaskID)
		delete(b.activeTouches, event.TaskID)
	} else {
		summary, _ := touchSummaryFromEvent(event)
		if summary.Path != "" {
			delete(b.activeTouches, summary.Path+":"+summary.Operation)
		}
	}
	b.updateSidebar()
}

func (b *TelemetryUIBridge) updateSidebar() {
	if b.app == nil {
		return
	}

	// Convert running tools map to slice
	tools := make([]buckleywidgets.RunningTool, 0, len(b.runningTools))
	for _, t := range b.runningTools {
		tools = append(tools, t)
	}

	touches := b.collectTouches()
	experimentVariants := b.collectExperimentVariants()

	// Post updates to app (thread-safe)
	b.app.SetCurrentTask(b.currentTask, b.taskProgress)
	b.app.SetPlanTasks(b.planTasks)
	b.app.SetRunningTools(tools)
	b.app.SetActiveTouches(touches)
	b.app.SetRLMStatus(b.rlmStatus, b.rlmScratchpad)
	b.app.sidebar.SetCircuitStatus(b.circuitStatus)
	b.app.sidebar.SetExperiment(b.experiment, b.experimentStatus, experimentVariants)
	b.app.sidebar.SetRecentFiles(b.recentFiles)
	b.app.Refresh()
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
	b.rlmStatus = status
	b.rlmScratchpad = parseRLMScratchpadEntries(event.Data["scratchpad"])
	if len(b.rlmScratchpad) > maxRLMScratchpadEntries {
		b.rlmScratchpad = b.rlmScratchpad[:maxRLMScratchpadEntries]
	}
	b.updateSidebar()
}

func (b *TelemetryUIBridge) handleCircuitFailure(event telemetry.Event) {
	// Create or update circuit status
	if b.circuitStatus == nil {
		b.circuitStatus = &buckleywidgets.CircuitStatus{
			State:       "Closed",
			MaxFailures: getInt(event.Data, "max_failures"),
		}
	}

	b.circuitStatus.ConsecutiveFailures = getInt(event.Data, "consecutive_num")
	b.circuitStatus.LastError = getString(event.Data, "error")

	if willOpen, ok := getBool(event.Data, "will_open"); ok && willOpen {
		b.circuitStatus.State = "Open"
	}

	b.updateSidebar()
}

func (b *TelemetryUIBridge) handleCircuitStateChange(event telemetry.Event) {
	toState := getString(event.Data, "to")
	if b.circuitStatus == nil {
		b.circuitStatus = &buckleywidgets.CircuitStatus{}
	}

	b.circuitStatus.State = toState
	b.circuitStatus.LastError = getString(event.Data, "last_error")

	// Clear failures when circuit closes
	if toState == "Closed" {
		b.circuitStatus.ConsecutiveFailures = 0
		b.circuitStatus.LastError = ""
	}

	b.updateSidebar()
}

// SetPlanTasks allows external code to set plan tasks directly.
func (b *TelemetryUIBridge) SetPlanTasks(tasks []buckleywidgets.PlanTask) {
	b.mu.Lock()
	b.planTasks = tasks
	b.mu.Unlock()
	b.updateSidebar()
}

// TaskProgress returns the current task progress (thread-safe).
func (b *TelemetryUIBridge) TaskProgress() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.taskProgress
}

// CurrentTask returns the current task name (thread-safe).
func (b *TelemetryUIBridge) CurrentTask() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.currentTask
}

func (b *TelemetryUIBridge) handleExperimentStarted(event telemetry.Event) {
	expID := getString(event.Data, "experiment_id")
	expName := getString(event.Data, "name")
	if expID != "" {
		b.experimentID = expID
	}
	if expName != "" {
		b.experiment = expName
	}
	status := getString(event.Data, "status")
	if status == "" {
		status = "running"
	}
	b.experimentStatus = status
	b.experimentVariants = make(map[string]buckleywidgets.ExperimentVariant)

	if raw, ok := event.Data["variants"]; ok {
		if list, ok := raw.([]any); ok {
			for _, entry := range list {
				if item, ok := entry.(map[string]any); ok {
					variantID := getString(item, "id")
					if variantID == "" {
						continue
					}
					b.experimentVariants[variantID] = buckleywidgets.ExperimentVariant{
						ID:      variantID,
						Name:    getString(item, "name"),
						ModelID: getString(item, "model"),
						Status:  "pending",
					}
				}
			}
		}
	}
	b.updateSidebar()
}

func (b *TelemetryUIBridge) handleExperimentCompleted(event telemetry.Event) {
	expID := getString(event.Data, "experiment_id")
	if expID != "" && b.experimentID != "" && expID != b.experimentID {
		return
	}
	status := getString(event.Data, "status")
	if status == "" {
		if event.Type == telemetry.EventExperimentFailed {
			status = "failed"
		} else {
			status = "completed"
		}
	}
	b.experimentStatus = status
	b.updateSidebar()
}

func (b *TelemetryUIBridge) handleExperimentVariant(event telemetry.Event, status string) {
	expID := getString(event.Data, "experiment_id")
	if expID != "" && b.experimentID != "" && expID != b.experimentID {
		return
	}
	variantID := getString(event.Data, "variant_id")
	if variantID == "" {
		variantID = event.TaskID
	}
	if variantID == "" {
		return
	}
	current := b.experimentVariants[variantID]
	current.ID = variantID
	if current.Name == "" {
		current.Name = getString(event.Data, "variant")
	}
	if current.ModelID == "" {
		current.ModelID = getString(event.Data, "model_id")
	}
	current.Status = status
	if durationMs, ok := getNumber(event.Data, "duration_ms"); ok {
		current.DurationMs = durationMs
	}
	if cost, ok := getFloat(event.Data, "total_cost"); ok {
		current.TotalCost = cost
	}
	if tokens, ok := getNumber(event.Data, "prompt_tokens"); ok {
		current.PromptTokens = tokens
	}
	if tokens, ok := getNumber(event.Data, "completion_tokens"); ok {
		current.CompletionTokens = tokens
	}
	b.experimentVariants[variantID] = current
	b.updateSidebar()
}

func (b *TelemetryUIBridge) collectExperimentVariants() []buckleywidgets.ExperimentVariant {
	if len(b.experimentVariants) == 0 {
		return nil
	}
	var variants []buckleywidgets.ExperimentVariant
	for _, variant := range b.experimentVariants {
		variants = append(variants, variant)
	}
	sort.Slice(variants, func(i, j int) bool {
		return strings.ToLower(variants[i].Name) < strings.ToLower(variants[j].Name)
	})
	return variants
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
	case int32:
		return int(v), true
	case float64:
		return int(v), true
	case float32:
		return int(v), true
	case string:
		if parsed, err := strconv.Atoi(v); err == nil {
			return parsed, true
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
		s := strings.TrimSpace(strings.ToLower(v))
		if s == "true" || s == "1" || s == "yes" {
			return true, true
		}
		if s == "false" || s == "0" || s == "no" {
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
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case int32:
		return float64(v), true
	case string:
		if parsed, err := strconv.ParseFloat(v, 64); err == nil {
			return parsed, true
		}
	}
	return 0, false
}

func parseRLMScratchpadEntries(raw any) []buckleywidgets.RLMScratchpadEntry {
	if raw == nil {
		return nil
	}
	entries := []buckleywidgets.RLMScratchpadEntry{}
	switch list := raw.(type) {
	case []buckleywidgets.RLMScratchpadEntry:
		return append(entries, list...)
	case []map[string]any:
		for _, item := range list {
			entries = append(entries, buckleywidgets.RLMScratchpadEntry{
				Key:     getString(item, "key"),
				Type:    getString(item, "type"),
				Summary: getString(item, "summary"),
			})
		}
	case []any:
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
	}
	if len(entries) == 0 {
		return nil
	}
	return entries
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

func (b *TelemetryUIBridge) collectTouches() []buckleywidgets.TouchSummary {
	now := time.Now()
	touches := make([]touchEntry, 0, len(b.activeTouches))
	for id, entry := range b.activeTouches {
		if !entry.expiresAt.IsZero() && now.After(entry.expiresAt) {
			delete(b.activeTouches, id)
			continue
		}
		touches = append(touches, entry)
	}
	sort.Slice(touches, func(i, j int) bool {
		return touches[i].startedAt.After(touches[j].startedAt)
	})
	if len(touches) > maxTouchEntries {
		touches = touches[:maxTouchEntries]
	}
	out := make([]buckleywidgets.TouchSummary, 0, len(touches))
	for _, entry := range touches {
		out = append(out, entry.summary)
	}
	return out
}
