package tool

import (
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/buckley/pkg/tool/builtin"
	"github.com/odvcencio/buckley/pkg/touch"
)

func (r *Registry) publishShellEvent(eventType telemetry.EventType, data map[string]any) {
	if r.telemetryHub == nil {
		return
	}
	payload := map[string]any{
		"tool": "run_shell",
	}
	for k, v := range data {
		payload[k] = v
	}
	r.telemetryHub.Publish(telemetry.Event{
		Type:      eventType,
		SessionID: r.telemetrySession,
		Data:      payload,
	})
}

func (r *Registry) publishToolEvent(eventType telemetry.EventType, callID, toolName string, rich touch.RichFields, timestamp time.Time, res *builtin.Result, err error, attempt int, metadata map[string]any) {
	if r.telemetryHub == nil {
		return
	}
	payload := map[string]any{
		"toolName":      toolName,
		"operationType": rich.OperationType,
		"filePath":      rich.FilePath,
		"ranges":        rich.Ranges,
		"command":       rich.Command,
		"addedLines":    rich.AddedLines,
		"removedLines":  rich.RemovedLines,
		"expiresAt":     timestamp.Add(touch.TTLForOperation(rich.OperationType)),
	}
	if rich.Description != "" {
		payload["description"] = rich.Description
	}
	if attempt > 0 {
		payload["attempt"] = attempt
	}
	if res != nil {
		payload["success"] = res.Success
		if strings.TrimSpace(toolName) == "browser_stream" {
			if rawEvents, ok := res.Data["events"]; ok {
				summary := summarizeBrowserEvents(rawEvents, 25)
				if len(summary) > 0 {
					payload["browser_events"] = summary
				}
			}
			if count, ok := res.Data["event_count"]; ok {
				payload["browser_event_count"] = count
			}
		}
	}
	if err != nil {
		payload["error"] = err.Error()
	}
	if metadata != nil {
		if stack, ok := metadata["panic_stack"].(string); ok && strings.TrimSpace(stack) != "" {
			payload["panic_stack"] = stack
		}
		if value, ok := metadata["panic_value"]; ok {
			payload["panic_value"] = fmt.Sprintf("%v", value)
		}
	}
	r.telemetryHub.Publish(telemetry.Event{
		Type:      eventType,
		SessionID: r.telemetrySession,
		TaskID:    callID,
		Timestamp: timestamp,
		Data:      payload,
	})
}

func eventTypeForResult(res *builtin.Result, err error) telemetry.EventType {
	if err != nil || (res != nil && !res.Success) {
		return telemetry.EventToolFailed
	}
	return telemetry.EventToolCompleted
}

func toolCallIDFromParams(params map[string]any) string {
	if params != nil {
		if raw, ok := params[ToolCallIDParam]; ok {
			switch v := raw.(type) {
			case string:
				if strings.TrimSpace(v) != "" {
					return strings.TrimSpace(v)
				}
			case fmt.Stringer:
				if val := strings.TrimSpace(v.String()); val != "" {
					return val
				}
			default:
				if val := strings.TrimSpace(fmt.Sprintf("%v", raw)); val != "" {
					return val
				}
			}
		}
	}
	return ulid.Make().String()
}

func sanitizeShellCommand(params map[string]any) string {
	if params == nil {
		return ""
	}
	if cmd, ok := params["command"].(string); ok {
		return strings.TrimSpace(cmd)
	}
	return ""
}

func truncateForTelemetry(value string) string {
	const limit = 512
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}

func summarizeBrowserEvents(raw any, limit int) []map[string]any {
	if limit <= 0 {
		limit = 10
	}
	out := make([]map[string]any, 0, limit)
	switch events := raw.(type) {
	case []map[string]any:
		for _, event := range events {
			if len(out) >= limit {
				break
			}
			out = append(out, summarizeBrowserEvent(event))
		}
	case []any:
		for _, item := range events {
			if len(out) >= limit {
				break
			}
			event, ok := item.(map[string]any)
			if !ok {
				continue
			}
			out = append(out, summarizeBrowserEvent(event))
		}
	}
	return out
}

func summarizeBrowserEvent(event map[string]any) map[string]any {
	summary := map[string]any{
		"type":          event["type"],
		"state_version": event["state_version"],
		"timestamp":     event["timestamp"],
	}
	if frame, ok := event["frame"].(map[string]any); ok {
		summary["has_frame"] = true
		if width, ok := frame["width"]; ok {
			summary["frame_width"] = width
		}
		if height, ok := frame["height"]; ok {
			summary["frame_height"] = height
		}
		if format, ok := frame["format"]; ok {
			summary["frame_format"] = format
		}
	} else if event["frame"] != nil {
		summary["has_frame"] = true
	}
	if event["dom_diff"] != nil {
		summary["has_dom_diff"] = true
	}
	if event["accessibility_diff"] != nil {
		summary["has_accessibility_diff"] = true
	}
	if event["hit_test"] != nil {
		summary["has_hit_test"] = true
	}
	return summary
}
