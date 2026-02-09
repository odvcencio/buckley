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
	"github.com/odvcencio/fluffyui/state"
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
const maxRecentFiles = 5
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

func updateRecentFiles(current []string, paths []string) []string {
	if len(paths) == 0 {
		return current
	}
	updated := append([]string(nil), current...)
	for i := len(paths) - 1; i >= 0; i-- {
		path := strings.TrimSpace(paths[i])
		if path == "" {
			continue
		}
		for j := 0; j < len(updated); j++ {
			if updated[j] == path {
				updated = append(updated[:j], updated[j+1:]...)
				j--
			}
		}
		updated = append([]string{path}, updated...)
		if len(updated) > maxRecentFiles {
			updated = updated[:maxRecentFiles]
		}
	}
	return updated
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

func getSignal[T any](sig state.Readable[T]) T {
	var zero T
	if sig == nil {
		return zero
	}
	return sig.Get()
}

func setSignal[T any](sig state.Writable[T], value T) bool {
	if sig == nil {
		return false
	}
	sig.Set(value)
	return true
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

func parseExperimentVariants(raw any, defaultStatus string) []buckleywidgets.ExperimentVariant {
	if raw == nil {
		return nil
	}
	switch list := raw.(type) {
	case []map[string]any:
		out := make([]buckleywidgets.ExperimentVariant, 0, len(list))
		for _, entry := range list {
			out = append(out, experimentVariantFromMap(entry, defaultStatus))
		}
		return out
	case []any:
		out := make([]buckleywidgets.ExperimentVariant, 0, len(list))
		for _, item := range list {
			entry, ok := item.(map[string]any)
			if !ok {
				continue
			}
			out = append(out, experimentVariantFromMap(entry, defaultStatus))
		}
		return out
	default:
		return nil
	}
}

func experimentVariantFromMap(entry map[string]any, defaultStatus string) buckleywidgets.ExperimentVariant {
	if entry == nil {
		return buckleywidgets.ExperimentVariant{}
	}
	variant := buckleywidgets.ExperimentVariant{
		ID:      firstNonEmpty(getString(entry, "id"), getString(entry, "variant_id")),
		Name:    firstNonEmpty(getString(entry, "name"), getString(entry, "variant")),
		ModelID: firstNonEmpty(getString(entry, "model"), getString(entry, "model_id")),
		Status:  getString(entry, "status"),
	}
	if variant.Status == "" {
		variant.Status = defaultStatus
	}
	if value, ok := getNumber(entry, "duration_ms"); ok {
		variant.DurationMs = value
	}
	if value, ok := getFloat(entry, "total_cost"); ok {
		variant.TotalCost = value
	}
	if value, ok := getNumber(entry, "prompt_tokens"); ok {
		variant.PromptTokens = value
	}
	if value, ok := getNumber(entry, "completion_tokens"); ok {
		variant.CompletionTokens = value
	}
	return variant
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

func experimentStatusForEvent(event telemetry.Event) string {
	if status := getString(event.Data, "status"); status != "" {
		return status
	}
	switch event.Type {
	case telemetry.EventExperimentStarted:
		return "running"
	case telemetry.EventExperimentCompleted:
		return "completed"
	case telemetry.EventExperimentFailed:
		return "failed"
	default:
		return ""
	}
}

func variantStatusForEvent(event telemetry.Event) string {
	if status := getString(event.Data, "status"); status != "" {
		return status
	}
	switch event.Type {
	case telemetry.EventExperimentVariantStarted:
		return "running"
	case telemetry.EventExperimentVariantCompleted:
		return "completed"
	case telemetry.EventExperimentVariantFailed:
		return "failed"
	default:
		return ""
	}
}

// =============================================================================
// SimpleTelemetryBridge - for Runner (no scheduler dependency)
// =============================================================================

// SimpleTelemetryBridge forwards telemetry events to the TUI without scheduler dependency.
// Used by Runner which doesn't have direct access to the state scheduler.
type SimpleTelemetryBridge struct {
	hub         *telemetry.Hub
	signals     SidebarSignals
	eventCh     <-chan telemetry.Event
	unsubscribe func()
	cancel      context.CancelFunc
	wg          sync.WaitGroup

	// dispatch, when non-nil, schedules a function to run on the UI thread.
	// Signal mutations must happen on the UI thread to avoid data races with
	// the render loop. When nil (e.g. in tests), handleEvent runs inline.
	dispatch func(func())

	mu           sync.Mutex
	touchEntries []touchEntry
}

// NewSimpleTelemetryBridge creates a bridge from telemetry hub to sidebar signals.
func NewSimpleTelemetryBridge(hub *telemetry.Hub, signals SidebarSignals) *SimpleTelemetryBridge {
	bridge := &SimpleTelemetryBridge{hub: hub, signals: signals}
	if hub == nil {
		return bridge
	}
	eventCh, unsub := hub.Subscribe()
	bridge.eventCh = eventCh
	bridge.unsubscribe = unsub
	return bridge
}

// SetDispatch sets a function that schedules work on the UI thread.
// Must be called before Start. When set, all signal mutations from the
// telemetry goroutine are dispatched through this function to ensure
// thread safety with the render loop.
func (b *SimpleTelemetryBridge) SetDispatch(fn func(func())) {
	if b == nil {
		return
	}
	b.dispatch = fn
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
			if b.dispatch != nil {
				// Schedule signal mutations on the UI thread to avoid
				// data races between the telemetry goroutine and the
				// render loop.
				evt := event
				b.dispatch(func() {
					b.handleEvent(evt)
				})
			} else {
				b.handleEvent(event)
			}
		}
	}
}

func (b *SimpleTelemetryBridge) handleEvent(event telemetry.Event) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	state.Batch(func() {
		b.pruneExpiredTouches()
		switch event.Type {
		case telemetry.EventTaskStarted:
			name := event.TaskID
			if data, ok := event.Data["name"].(string); ok {
				name = data
			}
			setSignal(b.signals.CurrentTask, name)
			setSignal(b.signals.TaskProgress, 0)
			updated := append([]buckleywidgets.PlanTask(nil), getSignal(b.signals.PlanTasks)...)
			updated = updateTaskStatus(updated, event.TaskID, buckleywidgets.TaskInProgress)
			setSignal(b.signals.PlanTasks, updated)

		case telemetry.EventTaskCompleted:
			setSignal(b.signals.TaskProgress, 100)
			updated := append([]buckleywidgets.PlanTask(nil), getSignal(b.signals.PlanTasks)...)
			updated = updateTaskStatus(updated, event.TaskID, buckleywidgets.TaskCompleted)
			setSignal(b.signals.PlanTasks, updated)

		case telemetry.EventTaskFailed:
			setSignal(b.signals.TaskProgress, 100)
			updated := append([]buckleywidgets.PlanTask(nil), getSignal(b.signals.PlanTasks)...)
			updated = updateTaskStatus(updated, event.TaskID, buckleywidgets.TaskFailed)
			setSignal(b.signals.PlanTasks, updated)

		case telemetry.EventPlanCreated, telemetry.EventPlanUpdated:
			b.handlePlanUpdate(event)

		case telemetry.EventEditorInline, telemetry.EventEditorPropose, telemetry.EventEditorApply:
			b.handleRecentFiles(event)

		case telemetry.EventToolStarted:
			b.handleToolStarted(event)
			b.handleTouchStarted(event)
			b.handleRecentFiles(event)
		case telemetry.EventToolCompleted, telemetry.EventToolFailed:
			b.handleToolFinished(event)
			b.handleTouchFinished(event)
			b.handleRecentFiles(event)

		case telemetry.EventRLMIteration:
			b.handleRLMIteration(event)
		case telemetry.EventCircuitFailure:
			b.handleCircuitFailure(event)
		case telemetry.EventCircuitStateChange:
			b.handleCircuitStateChange(event)
		case telemetry.EventExperimentStarted:
			b.handleExperimentStarted(event)
		case telemetry.EventExperimentCompleted, telemetry.EventExperimentFailed:
			b.handleExperimentFinished(event)
		case telemetry.EventExperimentVariantStarted, telemetry.EventExperimentVariantCompleted, telemetry.EventExperimentVariantFailed:
			b.handleExperimentVariant(event)
		}
	})
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
		setSignal(b.signals.PlanTasks, updated)
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
		tools := append([]buckleywidgets.RunningTool(nil), getSignal(b.signals.RunningTools)...)
		tools = upsertRunningTool(tools, tool)
		setSignal(b.signals.RunningTools, tools)
	}
	if toolName != "" || desc != "" {
		entry := buckleywidgets.ToolHistoryEntry{
			Name:   firstNonEmpty(toolName, "tool"),
			Status: "running",
			Detail: truncate(desc, 40),
			When:   event.Timestamp,
		}
		history := append([]buckleywidgets.ToolHistoryEntry(nil), getSignal(b.signals.ToolHistory)...)
		history = append(history, entry)
		if len(history) > maxToolHistoryEntries {
			history = history[len(history)-maxToolHistoryEntries:]
		}
		setSignal(b.signals.ToolHistory, history)
	}
}

func (b *SimpleTelemetryBridge) handleToolFinished(event telemetry.Event) {
	status := "completed"
	if event.Type == telemetry.EventToolFailed {
		status = "failed"
	}
	if event.TaskID != "" {
		tools := append([]buckleywidgets.RunningTool(nil), getSignal(b.signals.RunningTools)...)
		if tool, ok := findRunningTool(tools, event.TaskID); ok {
			entry := buckleywidgets.ToolHistoryEntry{
				Name:   firstNonEmpty(tool.Name, "tool"),
				Status: status,
				Detail: truncate(tool.Command, 40),
				When:   event.Timestamp,
			}
			history := append([]buckleywidgets.ToolHistoryEntry(nil), getSignal(b.signals.ToolHistory)...)
			history = append(history, entry)
			if len(history) > maxToolHistoryEntries {
				history = history[len(history)-maxToolHistoryEntries:]
			}
			setSignal(b.signals.ToolHistory, history)
		}
		tools = removeRunningTool(tools, event.TaskID)
		setSignal(b.signals.RunningTools, tools)
	}
}

func (b *SimpleTelemetryBridge) handleRecentFiles(event telemetry.Event) {
	paths := extractPaths(event.Data)
	if len(paths) == 0 {
		return
	}
	current := getSignal(b.signals.RecentFiles)
	updated := updateRecentFiles(current, paths)
	setSignal(b.signals.RecentFiles, updated)
}

func (b *SimpleTelemetryBridge) handleTouchStarted(event telemetry.Event) {
	if b == nil {
		return
	}
	summary, expiresAt := touchSummaryFromEvent(event)
	if strings.TrimSpace(summary.Path) == "" {
		return
	}
	if expiresAt.IsZero() && summary.Operation != "" {
		expiresAt = event.Timestamp.Add(touch.TTLForOperation(summary.Operation))
	}
	id := strings.TrimSpace(event.TaskID)
	if id == "" {
		id = summary.Path + ":" + summary.Operation
	}
	entry := touchEntry{
		id:        id,
		summary:   summary,
		expiresAt: expiresAt,
		startedAt: event.Timestamp,
	}
	updated := false
	for i := range b.touchEntries {
		if b.touchEntries[i].id == id {
			b.touchEntries[i] = entry
			updated = true
			break
		}
	}
	if !updated {
		b.touchEntries = append(b.touchEntries, entry)
	}
	if len(b.touchEntries) > maxTouchEntries {
		sort.Slice(b.touchEntries, func(i, j int) bool {
			return b.touchEntries[i].startedAt.After(b.touchEntries[j].startedAt)
		})
		b.touchEntries = b.touchEntries[:maxTouchEntries]
	}
	setSignal(b.signals.ActiveTouches, summarizeTouches(b.touchEntries, time.Now()))
}

func (b *SimpleTelemetryBridge) handleTouchFinished(event telemetry.Event) {
	if b == nil || strings.TrimSpace(event.TaskID) == "" {
		return
	}
	updated := make([]touchEntry, 0, len(b.touchEntries))
	for _, entry := range b.touchEntries {
		if entry.id != event.TaskID {
			updated = append(updated, entry)
		}
	}
	b.touchEntries = updated
	setSignal(b.signals.ActiveTouches, summarizeTouches(updated, time.Now()))
}

func (b *SimpleTelemetryBridge) handleExperimentStarted(event telemetry.Event) {
	name := firstNonEmpty(getString(event.Data, "name"), getString(event.Data, "experiment"))
	status := experimentStatusForEvent(event)
	variants := parseExperimentVariants(event.Data["variants"], "pending")
	setSignal(b.signals.Experiment, name)
	if status != "" {
		setSignal(b.signals.ExperimentStatus, status)
	}
	setSignal(b.signals.ExperimentVariants, cloneAndSortVariants(variants))
}

func (b *SimpleTelemetryBridge) handleExperimentFinished(event telemetry.Event) {
	name := firstNonEmpty(getString(event.Data, "name"), getString(event.Data, "experiment"))
	status := experimentStatusForEvent(event)
	if name != "" {
		setSignal(b.signals.Experiment, name)
	}
	if status != "" {
		setSignal(b.signals.ExperimentStatus, status)
	}
}

func (b *SimpleTelemetryBridge) handleExperimentVariant(event telemetry.Event) {
	data := event.Data
	name := firstNonEmpty(getString(data, "experiment"), getString(data, "name"))
	if name != "" {
		setSignal(b.signals.Experiment, name)
	}
	current := append([]buckleywidgets.ExperimentVariant(nil), getSignal(b.signals.ExperimentVariants)...)
	status := variantStatusForEvent(event)
	update := experimentVariantFromMap(data, status)
	duration, hasDuration := getNumber(data, "duration_ms")
	cost, hasCost := getFloat(data, "total_cost")
	prompt, hasPrompt := getNumber(data, "prompt_tokens")
	completion, hasCompletion := getNumber(data, "completion_tokens")
	if update.ID == "" && update.Name == "" && update.ModelID == "" {
		return
	}
	updated := false
	for i := range current {
		matchID := update.ID != "" && current[i].ID == update.ID
		matchName := update.Name != "" && current[i].Name == update.Name
		matchModel := update.ModelID != "" && current[i].ModelID == update.ModelID
		if matchID || matchName || matchModel {
			if update.ID != "" {
				current[i].ID = update.ID
			}
			if update.Name != "" {
				current[i].Name = update.Name
			}
			if update.ModelID != "" {
				current[i].ModelID = update.ModelID
			}
			if update.Status != "" {
				current[i].Status = update.Status
			}
			if hasDuration {
				current[i].DurationMs = duration
			}
			if hasCost {
				current[i].TotalCost = cost
			}
			if hasPrompt {
				current[i].PromptTokens = prompt
			}
			if hasCompletion {
				current[i].CompletionTokens = completion
			}
			updated = true
			break
		}
	}
	if !updated {
		if hasDuration {
			update.DurationMs = duration
		}
		if hasCost {
			update.TotalCost = cost
		}
		if hasPrompt {
			update.PromptTokens = prompt
		}
		if hasCompletion {
			update.CompletionTokens = completion
		}
		current = append(current, update)
	}
	setSignal(b.signals.ExperimentVariants, cloneAndSortVariants(current))
	if status == "running" {
		currentStatus := strings.TrimSpace(getSignal(b.signals.ExperimentStatus))
		if currentStatus == "" || currentStatus == "pending" {
			setSignal(b.signals.ExperimentStatus, status)
		}
	}
}

func (b *SimpleTelemetryBridge) handleRLMIteration(event telemetry.Event) {
	status := &buckleywidgets.RLMStatus{
		Iteration:     getInt(event.Data, "iteration"),
		MaxIterations: getInt(event.Data, "max_iterations"),
		TokensUsed:    getInt(event.Data, "tokens_used"),
		Summary:       getString(event.Data, "summary"),
	}
	if ready, ok := getBool(event.Data, "ready"); ok {
		status.Ready = ready
	}
	scratchpad := parseRLMScratchpadEntries(event.Data["scratchpad"])
	if len(scratchpad) > maxRLMScratchpadEntries {
		scratchpad = scratchpad[:maxRLMScratchpadEntries]
	}
	setSignal(b.signals.RLMStatus, status)
	setSignal(b.signals.RLMScratchpad, scratchpad)
}

func (b *SimpleTelemetryBridge) handleCircuitFailure(event telemetry.Event) {
	current := getSignal(b.signals.CircuitStatus)
	next := &buckleywidgets.CircuitStatus{}
	if current != nil {
		*next = *current
	}
	if next.State == "" {
		next.State = "Closed"
	}
	if maxFailures := getInt(event.Data, "max_failures"); maxFailures > 0 {
		next.MaxFailures = maxFailures
	}
	next.ConsecutiveFailures = getInt(event.Data, "consecutive_num")
	next.LastError = getString(event.Data, "error")
	if willOpen, ok := getBool(event.Data, "will_open"); ok && willOpen {
		next.State = "Open"
	}
	setSignal(b.signals.CircuitStatus, next)
}

func (b *SimpleTelemetryBridge) handleCircuitStateChange(event telemetry.Event) {
	toState := getString(event.Data, "to")
	current := getSignal(b.signals.CircuitStatus)
	next := &buckleywidgets.CircuitStatus{}
	if current != nil {
		*next = *current
	}
	next.State = toState
	next.LastError = getString(event.Data, "last_error")
	if toState == "Closed" {
		next.ConsecutiveFailures = 0
		next.LastError = ""
	}
	setSignal(b.signals.CircuitStatus, next)
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
	setSignal(b.signals.ActiveTouches, summarizeTouches(alive, now))
}

// SetPlanTasks allows external code to set plan tasks directly.
func (b *SimpleTelemetryBridge) SetPlanTasks(tasks []buckleywidgets.PlanTask) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	setSignal(b.signals.PlanTasks, tasks)
}
