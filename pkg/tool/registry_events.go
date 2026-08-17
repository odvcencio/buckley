package tool

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"m31labs.dev/buckley/pkg/telemetry"
	"m31labs.dev/buckley/pkg/tool/builtin"
	"m31labs.dev/buckley/pkg/touch"
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
	r.publishTelemetryEvent(telemetry.Event{
		Type:      eventType,
		SessionID: r.telemetrySession,
		Data:      payload,
	})
}

func (r *Registry) publishTelemetryEvent(event telemetry.Event) {
	if r == nil || r.telemetryHub == nil {
		return
	}
	// Every Registry publication crosses this boundary, including the shell
	// mission events. Keep raw command/path/output values out of both the hub
	// and custom publishers; callers must never have to remember which event
	// constructor was responsible for redaction.
	event = sanitizeRegistryTelemetryEvent(event)
	defer func() { _ = recover() }()
	if r.telemetryPublish != nil {
		r.telemetryPublish(event)
		return
	}
	r.telemetryHub.Publish(event)
}

func sanitizeRegistryTelemetryEvent(event telemetry.Event) telemetry.Event {
	if len(event.Data) == 0 {
		return event
	}
	data := make(map[string]any, len(event.Data)+8)
	for key, value := range event.Data {
		switch key {
		case "command":
			text := telemetryValueText(value)
			if strings.TrimSpace(text) == "" {
				data[key] = ""
				continue
			}
			data[key] = telemetry.OpaqueTextSummary(text, "command")
			data["command_bytes"] = len(telemetry.RedactCredentials(telemetry.NormalizeText(text)))
			data["command_fingerprint"] = telemetry.FingerprintText(text)
		case "filePath", "path", "file_path":
			text := telemetryValueText(value)
			if strings.TrimSpace(text) == "" {
				data[key] = ""
				continue
			}
			data[key] = telemetry.OpaqueTextSummary(text, "path")
			data[key+"_bytes"] = len(telemetry.RedactCredentials(telemetry.NormalizeText(text)))
			data[key+"_fingerprint"] = telemetry.FingerprintText(text)
		case "stderr_preview", "stdout_preview", "stderr", "stdout":
			text := telemetryValueText(value)
			if strings.TrimSpace(text) == "" {
				data[key] = ""
				continue
			}
			data[key] = telemetry.OpaqueTextSummary(text, strings.TrimSuffix(key, "_preview"))
			data[key+"_bytes"] = len(telemetry.RedactCredentials(telemetry.NormalizeText(text)))
			data[key+"_fingerprint"] = telemetry.FingerprintText(text)
		case "error":
			text := telemetryValueText(value)
			data[key] = telemetry.OpaqueTextSummary(text, "error")
			data["error_bytes"] = len(telemetry.RedactCredentials(telemetry.NormalizeText(text)))
			data["error_fingerprint"] = telemetry.FingerprintText(text)
		case "arguments", "result":
			// Arguments and results are retained as a correlation-safe projection
			// rather than dropping the lifecycle detail altogether. Preserve the
			// redaction marker in the summary so operators can tell a payload was
			// intentionally withheld.
			text := telemetry.SanitizeText(telemetryValueText(value), telemetry.MaxResultBytes)
			data[key] = telemetry.OpaqueTextSummary(text, key) + redactionMarkerSuffix(text)
			data[key+"_bytes"] = len(text)
			data[key+"_fingerprint"] = telemetry.FingerprintText(text)
		case "note":
			text := telemetryValueText(value)
			if strings.TrimSpace(text) == "" {
				data[key] = ""
				continue
			}
			data[key] = telemetry.OpaqueTextSummary(text, "note")
			data["note_bytes"] = len(telemetry.RedactCredentials(telemetry.NormalizeText(text)))
			data["note_fingerprint"] = telemetry.FingerprintText(text)
		case "description":
			data[key] = telemetry.SanitizeText(telemetryValueText(value), telemetry.MaxArgumentBytes)
		default:
			data[key] = telemetry.NormalizeAndSanitize(value, telemetry.MaxResultBytes)
		}
	}
	event.Data = data
	return event
}

func telemetryValueText(value any) (text string) {
	defer func() {
		if recover() != nil {
			text = "[REDACTED: telemetry value unavailable]"
		}
	}()
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		encoded, err := json.Marshal(telemetry.NormalizeAndSanitize(value, telemetry.MaxArgumentBytes))
		if err != nil {
			return "[REDACTED: telemetry value unavailable]"
		}
		return string(encoded)
	}
}

func redactionMarkerSuffix(text string) string {
	if strings.Contains(text, "[REDACTED") {
		return " [REDACTED]"
	}
	return ""
}

func (r *Registry) publishToolEventBestEffort(eventType telemetry.EventType, callID, toolName string, rich touch.RichFields, timestamp time.Time, res *builtin.Result, err error, attempt int, metadata map[string]any) {
	defer func() {
		if recover() == nil {
			return
		}
		r.publishTelemetryEvent(telemetry.Event{
			Type:      eventType,
			SessionID: r.telemetrySession,
			TaskID:    callID,
			Timestamp: timestamp,
			Data: map[string]any{
				"toolName": toolName,
			},
		})
	}()
	r.publishToolEvent(eventType, callID, toolName, rich, timestamp, res, err, attempt, metadata)
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
		if detail := telemetryResultDetail(res); detail != "" {
			payload["result"] = detail
		}
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
		if arguments, ok := metadata["arguments"].(string); ok && strings.TrimSpace(arguments) != "" {
			payload["arguments"] = arguments
		}
		if stack, ok := metadata["panic_stack"].(string); ok && strings.TrimSpace(stack) != "" {
			payload["panic_stack"] = telemetry.SanitizeText(stack, maxTelemetryPanicStackBytes)
		}
		if value, ok := metadata["panic_value"]; ok {
			payload["panic_value"], payload["panic_type"] = sanitizePanicValue(value)
		}
		if truncated, ok := metadata["result_truncated"].(bool); ok && truncated {
			payload["result_truncated"] = true
		}
	}
	r.publishTelemetryEvent(telemetry.Event{
		Type:      eventType,
		SessionID: r.telemetrySession,
		TaskID:    callID,
		Timestamp: timestamp,
		Data:      payload,
	})
}

const (
	maxTelemetryPanicStackBytes = 8 * 1024
	maxTelemetryPanicValueBytes = 1024
	maxTelemetryPanicTypeBytes  = 256
)

func sanitizePanicValue(value any) (string, string) {
	typeName := telemetry.BoundText(fmt.Sprintf("%T", value), maxTelemetryPanicTypeBytes, "panic type")
	switch value.(type) {
	case string, []byte, error, fmt.Stringer:
		return "[REDACTED: panic value]", typeName
	}

	clean := telemetry.NormalizeAndSanitize(value, maxTelemetryPanicValueBytes)
	encoded, err := json.Marshal(clean)
	if err != nil {
		return "[REDACTED: panic value]", typeName
	}
	return telemetry.BoundText(string(encoded), maxTelemetryPanicValueBytes, "panic value"), typeName
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
