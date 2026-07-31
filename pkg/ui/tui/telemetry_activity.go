package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"m31labs.dev/buckley/v2/pkg/telemetry"
	"m31labs.dev/buckley/v2/pkg/ui/widgets"
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
	}
	record.Detail = toolActivityDetail(event.Data)
	b.activities[id] = record
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
	record.Title = "agent:" + label
	record.Status = subagentActivityStatus(event)
	record.Summary = firstNonEmpty(getString(event.Data, "task"), getString(event.Data, "state"), string(record.Status))
	if record.StartedAt.IsZero() {
		record.StartedAt = activityTimestamp(event.Timestamp)
	}
	if record.Status != widgets.ActivityRunning {
		record.FinishedAt = activityTimestamp(event.Timestamp)
	}
	record.Detail = subagentActivityDetail(event.Data)
	b.activities[id] = record
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
