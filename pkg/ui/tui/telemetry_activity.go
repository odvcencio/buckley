package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/telemetry"
	"m31labs.dev/buckley/pkg/ui/widgets"
)

const maxActivityEntries = 30

func (b *TelemetryUIBridge) handleToolActivity(event telemetry.Event, status widgets.ActivityStatus) {
	id := strings.TrimSpace(event.TaskID)
	if id == "" {
		id = activityFallbackID(event)
	}
	if id == "" {
		return
	}

	record := b.activities[id]
	record.ID = id
	record.Kind = "tool"
	record.Status = status
	record.Title = firstNonEmpty(getString(event.Data, "toolName"), getString(event.Data, "tool"), "tool")
	record.Path = firstNonEmpty(getString(event.Data, "filePath"), getString(event.Data, "path"), getString(event.Data, "file_path"))
	record.Operation = firstNonEmpty(getString(event.Data, "operationType"), getString(event.Data, "operation"))
	record.Summary = toolActivitySummary(event.Data, record)
	if record.StartedAt.IsZero() {
		record.StartedAt = activityTimestamp(event.Timestamp)
	}
	if status != widgets.ActivityRunning {
		record.FinishedAt = activityTimestamp(event.Timestamp)
		// The tool finished, so AppendActivityOutput's streaming builder (if
		// any) is done accumulating and its buffered bytes are no longer
		// needed: record.Detail below fully replaces whatever it held.
		delete(b.detailBuilders, id)
	}
	record.Detail = toolActivityDetail(event.Data)
	b.activities[id] = record
	b.evictCompletedActivities()
}

func (b *TelemetryUIBridge) handleSubagentActivity(event telemetry.Event) {
	id := strings.TrimSpace(event.TaskID)
	if id == "" {
		id = getString(event.Data, "agent_id")
	}
	if id == "" {
		return
	}

	record := b.activities[id]
	record.ID = id
	record.Kind = "subagent"
	label := firstNonEmpty(getString(event.Data, "agent"), getString(event.Data, "provider"), "subagent")
	if modelID := strings.TrimSpace(getString(event.Data, "model")); modelID != "" {
		label += " · " + modelID
	}
	record.Title = "agent:" + label
	record.Status = subagentActivityStatus(event)
	record.Summary = subagentActivitySummary(event.Data, record.Status)
	if record.StartedAt.IsZero() {
		record.StartedAt = activityTimestamp(event.Timestamp)
	}
	if record.Status != widgets.ActivityRunning {
		record.FinishedAt = activityTimestamp(event.Timestamp)
	}
	record.Detail = subagentActivityDetail(event.Data)
	b.activities[id] = record
	b.evictCompletedActivities()
}

// evictCompletedActivities bounds b.activities in place. collectActivities
// already caps what it returns, but the backing map never shrank on its
// own: every finished tool or subagent record (each carrying up to tens of
// kilobytes of Detail) stayed resident for the life of the session. This
// keeps all running records, plus the newest maxActivityEntries completed
// ones, and deletes the rest.
func (b *TelemetryUIBridge) evictCompletedActivities() {
	var completed []widgets.ActivityRecord
	for _, record := range b.activities {
		if record.Status != widgets.ActivityRunning {
			completed = append(completed, record)
		}
	}
	if len(completed) <= maxActivityEntries {
		return
	}
	sort.Slice(completed, func(i, j int) bool {
		return activityRecency(completed[i]).After(activityRecency(completed[j]))
	})
	for _, record := range completed[maxActivityEntries:] {
		delete(b.activities, record.ID)
		delete(b.detailBuilders, record.ID)
	}
}

// activityFlushInterval bounds how often AppendActivityOutput publishes the
// sidebar to the UI: a fast-streaming shell command can call it hundreds of
// times per second, but collectActivities (map copy + sort) and the
// resulting redraw only need to happen often enough to look live.
const activityFlushInterval = 100 * time.Millisecond // ~10 refreshes/sec

// AppendActivityOutput streams incremental shell output into the Detail of
// the running activity identified by taskID, creating a running placeholder
// record if the tool.started event has not arrived yet. It is safe to call
// from any goroutine: the shared activities map is guarded by b.mu, and the
// resulting inspector update flows through WidgetApp.Post (see
// applySetActivities) so the widget tree is only mutated on the UI
// goroutine. The appended text is already bounded by the shell tool's own
// per-stream progress cap (builtin.maxShellProgressBytes via
// WithShellOutputSink), so no further truncation happens here.
//
// Two costs scale with a long-running command's output volume, so both are
// bounded independently of how often the caller calls this function:
//
//   - Accumulating Detail used to be `record.Detail += text`, which copies
//     the entire string-so-far on every call (quadratic total cost). A
//     per-task strings.Builder accumulates in amortized-constant time
//     instead; Builder.String() is a zero-copy view of its buffer, so
//     record.Detail can stay current on every call at no extra cost.
//   - Publishing to the UI (collectActivities' map copy + sort, plus the
//     resulting redraw) is throttled to activityFlushInterval instead of
//     firing on every chunk.
func (b *TelemetryUIBridge) AppendActivityOutput(taskID, text string) {
	if b == nil {
		return
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" || text == "" {
		return
	}

	b.mu.Lock()
	record, ok := b.activities[taskID]
	if !ok {
		record = widgets.ActivityRecord{
			ID:        taskID,
			Kind:      "tool",
			Title:     "run_shell",
			Status:    widgets.ActivityRunning,
			StartedAt: time.Now(),
		}
	}

	builder := b.detailBuilders[taskID]
	if builder == nil {
		builder = &strings.Builder{}
		builder.WriteString(record.Detail)
		b.detailBuilders[taskID] = builder
	}
	builder.WriteString(text)
	record.Detail = builder.String()
	b.activities[taskID] = record

	now := time.Now()
	flush := shouldFlushActivityDetail(b.lastActivityFlush, now)
	var records []widgets.ActivityRecord
	if flush {
		b.lastActivityFlush = now
		if b.app != nil {
			records = b.collectActivities()
		}
	}
	b.mu.Unlock()

	if flush && b.app != nil {
		b.app.SetActivities(records)
	}
}

// shouldFlushActivityDetail reports whether enough time has passed since
// last to publish another sidebar update at now. It's a pure function, kept
// separate from AppendActivityOutput's locking and I/O, so the throttle
// boundary can be tested without wall-clock waits or a running UI.
func shouldFlushActivityDetail(last, now time.Time) bool {
	return last.IsZero() || now.Sub(last) >= activityFlushInterval
}

func activityRecency(record widgets.ActivityRecord) time.Time {
	if !record.FinishedAt.IsZero() {
		return record.FinishedAt
	}
	return record.StartedAt
}

func activityFallbackID(event telemetry.Event) string {
	parts := []string{
		getString(event.Data, "toolName"),
		firstNonEmpty(getString(event.Data, "filePath"), getString(event.Data, "path")),
		getString(event.Data, "operationType"),
	}
	joined := strings.Join(parts, ":")
	if strings.Trim(joined, ": ") == "" {
		return ""
	}
	return joined
}

func activityTimestamp(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now()
	}
	return value
}

func toolActivitySummary(data map[string]any, record widgets.ActivityRecord) string {
	if summary := firstNonEmpty(getString(data, "description"), getString(data, "command")); summary != "" {
		return truncate(summary, 240)
	}
	if record.Path != "" {
		if record.Operation != "" {
			return record.Operation + " " + record.Path
		}
		return record.Path
	}
	return string(record.Status)
}

func toolActivityDetail(data map[string]any) string {
	var sections []string
	if arguments := strings.TrimSpace(getString(data, "arguments")); arguments != "" {
		sections = append(sections, "Arguments:\n"+arguments)
	}
	if result := strings.TrimSpace(getString(data, "result")); result != "" {
		sections = append(sections, "Result:\n"+result)
	}
	if command := strings.TrimSpace(getString(data, "command")); command != "" {
		sections = append(sections, "Command:\n"+command)
	}
	if added, ok := getNumber(data, "addedLines"); ok && added > 0 {
		sections = append(sections, fmt.Sprintf("Added lines: %d", added))
	}
	if removed, ok := getNumber(data, "removedLines"); ok && removed > 0 {
		sections = append(sections, fmt.Sprintf("Removed lines: %d", removed))
	}
	if errText := strings.TrimSpace(getString(data, "error")); errText != "" {
		sections = append(sections, "Error:\n"+errText)
	}
	return strings.Join(sections, "\n\n")
}

func subagentActivityStatus(event telemetry.Event) widgets.ActivityStatus {
	switch event.Type {
	case telemetry.EventSubagentCompleted:
		return widgets.ActivityCompleted
	case telemetry.EventSubagentFailed:
		return widgets.ActivityFailed
	case telemetry.EventSubagentCancelled:
		return widgets.ActivityCancelled
	}
	switch strings.ToLower(getString(event.Data, "state")) {
	case "completed":
		return widgets.ActivityCompleted
	case "failed":
		return widgets.ActivityFailed
	case "cancelled", "canceled":
		return widgets.ActivityCancelled
	default:
		return widgets.ActivityRunning
	}
}

func subagentActivityDetail(data map[string]any) string {
	var sections []string
	if task := strings.TrimSpace(getString(data, "task")); task != "" {
		sections = append(sections, "Task:\n"+task)
	}
	var contract []string
	if persona := strings.TrimSpace(getString(data, "persona")); persona != "" {
		contract = append(contract, "Persona: "+persona)
	}
	if modelID := strings.TrimSpace(getString(data, "model")); modelID != "" {
		contract = append(contract, "Model: "+modelID)
	}
	if tier := strings.TrimSpace(getString(data, "tier")); tier != "" {
		contract = append(contract, "Tier: "+tier)
	}
	if effort := strings.TrimSpace(getString(data, "effort")); effort != "" {
		contract = append(contract, "Effort: "+effort)
	}
	if cap, ok := getNumber(data, "step_cap"); ok && cap > 0 {
		contract = append(contract, fmt.Sprintf("Step cap: %d", cap))
	}
	if count, ok := getNumber(data, "allowed_tool_count"); ok {
		contract = append(contract, fmt.Sprintf("Visible tools: %d", count))
	}
	if isolation := strings.TrimSpace(getString(data, "isolation")); isolation != "" {
		contract = append(contract, "Isolation: "+isolation)
	}
	if schema := strings.TrimSpace(getString(data, "output_schema")); schema != "" {
		contract = append(contract, "Output schema: "+schema)
	}
	if posture := strings.TrimSpace(getString(data, "approval_posture")); posture != "" {
		contract = append(contract, "Approval: "+posture)
	}
	if len(contract) > 0 {
		sections = append(sections, "Execution contract:\n"+strings.Join(contract, "\n"))
	}
	if claims := activityStringList(data, "workspace_claims"); len(claims) > 0 {
		sections = append(sections, "Workspace claims:\n"+strings.Join(claims, "\n"))
	}
	if spec := strings.TrimSpace(getString(data, "spec")); spec != "" {
		sections = append(sections, "Spec: "+spec)
	}
	if pid, ok := getNumber(data, "pid"); ok && pid > 0 {
		sections = append(sections, fmt.Sprintf("PID: %d", pid))
	}
	if output := strings.TrimSpace(getString(data, "output")); output != "" {
		sections = append(sections, "Output:\n"+output)
	}
	if errText := strings.TrimSpace(getString(data, "error")); errText != "" {
		sections = append(sections, "Error:\n"+errText)
	}
	return strings.Join(sections, "\n\n")
}

func subagentActivitySummary(data map[string]any, status widgets.ActivityStatus) string {
	task := strings.TrimSpace(getString(data, "task"))
	state := firstNonEmpty(strings.TrimSpace(getString(data, "state")), string(status))
	if task == "" {
		return state
	}
	return state + " · " + truncate(task, 200)
}

func activityStringList(data map[string]any, key string) []string {
	if data == nil {
		return nil
	}
	value := data[key]
	var raw []string
	switch typed := value.(type) {
	case []string:
		raw = typed
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok {
				raw = append(raw, text)
			}
		}
	}
	if len(raw) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(raw))
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	sort.Strings(result)
	return result
}

func (b *TelemetryUIBridge) collectActivities() []widgets.ActivityRecord {
	if len(b.activities) == 0 {
		return nil
	}
	records := make([]widgets.ActivityRecord, 0, len(b.activities))
	for _, record := range b.activities {
		records = append(records, record)
	}
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].Status == widgets.ActivityRunning && records[j].Status != widgets.ActivityRunning {
			return true
		}
		if records[i].Status != widgets.ActivityRunning && records[j].Status == widgets.ActivityRunning {
			return false
		}
		return records[i].StartedAt.After(records[j].StartedAt)
	})
	if len(records) > maxActivityEntries {
		records = records[:maxActivityEntries]
	}
	return records
}
