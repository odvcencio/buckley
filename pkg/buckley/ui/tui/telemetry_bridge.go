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
)

// TelemetryBridge is the common interface for telemetry bridges.
type TelemetryBridge interface {
	Start(ctx context.Context)
	Stop()
	SetPlanTasks(tasks []buckleywidgets.PlanTask)
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

// Helper functions

func getString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	value, ok := m[key]
	if !ok {
		return ""
	}
	switch v := value.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	case json.Number:
		return string(v)
	default:
		return ""
	}
}

func cloneRunningTools(src []buckleywidgets.RunningTool) []buckleywidgets.RunningTool {
	if src == nil {
		return nil
	}
	out := make([]buckleywidgets.RunningTool, len(src))
	copy(out, src)
	return out
}

func upsertRunningTool(tools []buckleywidgets.RunningTool, tool buckleywidgets.RunningTool) []buckleywidgets.RunningTool {
	for i := range tools {
		if tools[i].ID == tool.ID {
			tools[i] = tool
			return tools
		}
	}
	return append(tools, tool)
}

func findRunningTool(tools []buckleywidgets.RunningTool, id string) (buckleywidgets.RunningTool, bool) {
	for _, t := range tools {
		if t.ID == id {
			return t, true
		}
	}
	return buckleywidgets.RunningTool{}, false
}

func removeRunningTool(tools []buckleywidgets.RunningTool, id string) []buckleywidgets.RunningTool {
	out := make([]buckleywidgets.RunningTool, 0, len(tools))
	for _, t := range tools {
		if t.ID != id {
			out = append(out, t)
		}
	}
	return out
}

func updateTaskStatus(tasks []buckleywidgets.PlanTask, taskID string, status buckleywidgets.TaskStatus) []buckleywidgets.PlanTask {
	for i := range tasks {
		if tasks[i].Name == taskID {
			tasks[i].Status = status
			return tasks
		}
	}
	return tasks
}

func summarizeTouches(entries []touchEntry, now time.Time) []buckleywidgets.TouchSummary {
	out := make([]buckleywidgets.TouchSummary, 0, len(entries))
	for _, e := range entries {
		if !e.expiresAt.IsZero() && now.After(e.expiresAt) {
			continue
		}
		out = append(out, e.summary)
	}
	return out
}

func cloneAndSortVariants(src []buckleywidgets.ExperimentVariant) []buckleywidgets.ExperimentVariant {
	if src == nil {
		return nil
	}
	out := make([]buckleywidgets.ExperimentVariant, len(src))
	copy(out, src)
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
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

// =============================================================================
// SimpleTelemetryBridge - for Runner (no scheduler dependency)
// =============================================================================

// TelemetryPoster is the interface for posting telemetry messages to the UI.
type TelemetryPoster interface {
	Post(msg Message)
}

// SimpleTelemetryBridge forwards telemetry events to the TUI without scheduler dependency.
// Used by Runner which doesn't have access to WidgetApp's stateScheduler.
type SimpleTelemetryBridge struct {
	hub         *telemetry.Hub
	poster      TelemetryPoster
	eventCh     <-chan telemetry.Event
	unsubscribe func()
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	mu          sync.Mutex

	// State mirrors (simpler than signal-based approach)
	currentTask        string
	taskProgress       int
	planTasks          []buckleywidgets.PlanTask
	runningTools       []buckleywidgets.RunningTool
	toolHistory        []buckleywidgets.ToolHistoryEntry
	touchEntries       []touchEntry
	recentFiles        []string
	rlmStatus          *buckleywidgets.RLMStatus
	rlmScratchpad      []buckleywidgets.RLMScratchpadEntry
	circuitStatus      *buckleywidgets.CircuitStatus
	experiment         string
	experimentStatus   string
	experimentVariants []buckleywidgets.ExperimentVariant
}

// NewSimpleTelemetryBridge creates a bridge from telemetry hub to any TelemetryPoster.
func NewSimpleTelemetryBridge(hub *telemetry.Hub, poster TelemetryPoster) *SimpleTelemetryBridge {
	if hub == nil || poster == nil {
		return &SimpleTelemetryBridge{}
	}
	eventCh, unsub := hub.Subscribe()
	return &SimpleTelemetryBridge{
		hub:         hub,
		poster:      poster,
		eventCh:     eventCh,
		unsubscribe: unsub,
	}
}

// Start begins forwarding telemetry events to the UI.
func (b *SimpleTelemetryBridge) Start(ctx context.Context) {
	if b == nil || b.hub == nil {
		return
	}
	ctx, b.cancel = context.WithCancel(ctx)
	b.wg.Add(1)
	go b.forwardLoop(ctx)
}

// Stop ceases forwarding and cleans up subscriptions.
func (b *SimpleTelemetryBridge) Stop() {
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
}

func (b *SimpleTelemetryBridge) forwardLoop(ctx context.Context) {
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
			b.postSnapshot()
		}
	}
}

func (b *SimpleTelemetryBridge) postSnapshot() {
	if b.poster == nil {
		return
	}
	b.mu.Lock()
	snapshot := b.buildSnapshot()
	b.mu.Unlock()
	b.poster.Post(SidebarStateMsg{Snapshot: snapshot})
}

func (b *SimpleTelemetryBridge) buildSnapshot() SidebarSnapshot {
	now := time.Now()
	return SidebarSnapshot{
		CurrentTask:        b.currentTask,
		TaskProgress:       b.taskProgress,
		PlanTasks:          append([]buckleywidgets.PlanTask(nil), b.planTasks...),
		RunningTools:       cloneRunningTools(b.runningTools),
		ToolHistory:        append([]buckleywidgets.ToolHistoryEntry(nil), b.toolHistory...),
		ActiveTouches:      summarizeTouches(b.touchEntries, now),
		RecentFiles:        append([]string(nil), b.recentFiles...),
		RLMStatus:          b.rlmStatus,
		RLMScratchpad:      append([]buckleywidgets.RLMScratchpadEntry(nil), b.rlmScratchpad...),
		CircuitStatus:      b.circuitStatus,
		Experiment:         b.experiment,
		ExperimentStatus:   b.experimentStatus,
		ExperimentVariants: cloneAndSortVariants(b.experimentVariants),
	}
}

func (b *SimpleTelemetryBridge) handleEvent(event telemetry.Event) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	b.pruneExpiredTouches()

	switch event.Type {
	case telemetry.EventTaskStarted:
		name := event.TaskID
		if data, ok := event.Data["name"].(string); ok {
			name = data
		}
		b.currentTask = name
		b.taskProgress = 0
		b.planTasks = updateTaskStatus(b.planTasks, event.TaskID, buckleywidgets.TaskInProgress)

	case telemetry.EventTaskCompleted:
		b.taskProgress = 100
		b.planTasks = updateTaskStatus(b.planTasks, event.TaskID, buckleywidgets.TaskCompleted)

	case telemetry.EventTaskFailed:
		b.taskProgress = 100
		b.planTasks = updateTaskStatus(b.planTasks, event.TaskID, buckleywidgets.TaskFailed)

	case telemetry.EventPlanCreated, telemetry.EventPlanUpdated:
		b.handlePlanUpdate(event)

	case telemetry.EventToolStarted:
		b.handleToolStarted(event)
	case telemetry.EventToolCompleted, telemetry.EventToolFailed:
		b.handleToolFinished(event)

	case telemetry.EventRLMIteration:
		b.handleRLMIteration(event)
	case telemetry.EventCircuitFailure:
		b.handleCircuitFailure(event)
	case telemetry.EventCircuitStateChange:
		b.handleCircuitStateChange(event)
	}
}

func (b *SimpleTelemetryBridge) handlePlanUpdate(event telemetry.Event) {
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
		b.planTasks = updated
	}
}

func (b *SimpleTelemetryBridge) handleToolStarted(event telemetry.Event) {
	toolName := getString(event.Data, "toolName")
	desc := getString(event.Data, "description")
	if desc == "" {
		desc = getString(event.Data, "command")
	}
	if toolName != "" && event.TaskID != "" {
		tool := buckleywidgets.RunningTool{ID: event.TaskID, Name: toolName, Command: desc}
		b.runningTools = upsertRunningTool(b.runningTools, tool)
	}
	if toolName != "" || desc != "" {
		entry := buckleywidgets.ToolHistoryEntry{
			Name:   firstNonEmpty(toolName, "tool"),
			Status: "running",
			Detail: truncate(desc, 40),
			When:   event.Timestamp,
		}
		b.toolHistory = append(b.toolHistory, entry)
		if len(b.toolHistory) > maxToolHistoryEntries {
			b.toolHistory = b.toolHistory[len(b.toolHistory)-maxToolHistoryEntries:]
		}
	}
}

func (b *SimpleTelemetryBridge) handleToolFinished(event telemetry.Event) {
	status := "completed"
	if event.Type == telemetry.EventToolFailed {
		status = "failed"
	}
	if event.TaskID != "" {
		if tool, ok := findRunningTool(b.runningTools, event.TaskID); ok {
			entry := buckleywidgets.ToolHistoryEntry{
				Name:   firstNonEmpty(tool.Name, "tool"),
				Status: status,
				Detail: truncate(tool.Command, 40),
				When:   event.Timestamp,
			}
			b.toolHistory = append(b.toolHistory, entry)
			if len(b.toolHistory) > maxToolHistoryEntries {
				b.toolHistory = b.toolHistory[len(b.toolHistory)-maxToolHistoryEntries:]
			}
		}
		b.runningTools = removeRunningTool(b.runningTools, event.TaskID)
	}
}

func (b *SimpleTelemetryBridge) handleRLMIteration(event telemetry.Event) {
	b.rlmStatus = &buckleywidgets.RLMStatus{
		Iteration:     getInt(event.Data, "iteration"),
		MaxIterations: getInt(event.Data, "max_iterations"),
		TokensUsed:    getInt(event.Data, "tokens_used"),
		Summary:       getString(event.Data, "summary"),
	}
	if ready, ok := getBool(event.Data, "ready"); ok {
		b.rlmStatus.Ready = ready
	}
	b.rlmScratchpad = parseRLMScratchpadEntries(event.Data["scratchpad"])
	if len(b.rlmScratchpad) > maxRLMScratchpadEntries {
		b.rlmScratchpad = b.rlmScratchpad[:maxRLMScratchpadEntries]
	}
}

func (b *SimpleTelemetryBridge) handleCircuitFailure(event telemetry.Event) {
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
}

func (b *SimpleTelemetryBridge) handleCircuitStateChange(event telemetry.Event) {
	toState := getString(event.Data, "to")
	if b.circuitStatus == nil {
		b.circuitStatus = &buckleywidgets.CircuitStatus{}
	}
	b.circuitStatus.State = toState
	b.circuitStatus.LastError = getString(event.Data, "last_error")
	if toState == "Closed" {
		b.circuitStatus.ConsecutiveFailures = 0
		b.circuitStatus.LastError = ""
	}
}

func (b *SimpleTelemetryBridge) pruneExpiredTouches() {
	if len(b.touchEntries) == 0 {
		return
	}
	now := time.Now()
	alive := make([]touchEntry, 0, len(b.touchEntries))
	for _, entry := range b.touchEntries {
		if !entry.expiresAt.IsZero() && now.After(entry.expiresAt) {
			continue
		}
		alive = append(alive, entry)
	}
	b.touchEntries = alive
}

// SetPlanTasks allows external code to set plan tasks directly.
func (b *SimpleTelemetryBridge) SetPlanTasks(tasks []buckleywidgets.PlanTask) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.planTasks = tasks
	b.mu.Unlock()
	b.postSnapshot()
}
