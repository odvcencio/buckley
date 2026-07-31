package tool

import (
	"encoding/json"
	"fmt"
	"strings"

	"m31labs.dev/buckley/v2/pkg/tool/builtin"
)

const (
	maxTelemetryArgumentsBytes = 16 * 1024
	maxTelemetryResultBytes    = 64 * 1024
)

func withTelemetryArguments(metadata map[string]any, params map[string]any) map[string]any {
	out := make(map[string]any, len(metadata)+1)
	for key, value := range metadata {
		out[key] = value
	}
	clean := sanitizeTelemetryValue(params, "")
	if encoded, err := json.MarshalIndent(clean, "", "  "); err == nil {
		out["arguments"] = boundTelemetryText(string(encoded), maxTelemetryArgumentsBytes, "tool arguments")
	}
	return out
}

func telemetryResultDetail(result *builtin.Result) string {
	if result == nil {
		return ""
	}
	payload := map[string]any{
		"success": result.Success,
	}
	if len(result.Data) > 0 {
		payload["data"] = result.Data
	}
	if len(result.DisplayData) > 0 {
		payload["display_data"] = result.DisplayData
	}
	if strings.TrimSpace(result.Error) != "" {
		payload["error"] = result.Error
	}
	if result.DiffPreview != nil {
		payload["diff"] = map[string]any{
			"file_path":     result.DiffPreview.FilePath,
			"is_new":        result.DiffPreview.IsNew,
			"is_delete":     result.DiffPreview.IsDelete,
			"lines_added":   result.DiffPreview.LinesAdded,
			"lines_removed": result.DiffPreview.LinesRemoved,
			"unified_diff":  result.DiffPreview.UnifiedDiff,
		}
	}
	clean := sanitizeTelemetryValue(payload, "")
	encoded, err := json.MarshalIndent(clean, "", "  ")
	if err != nil {
		return boundTelemetryText(fmt.Sprintf("success=%t error=%q", result.Success, result.Error), maxTelemetryResultBytes, "tool result")
	}
	return boundTelemetryText(string(encoded), maxTelemetryResultBytes, "tool result")
}

func sanitizeTelemetryValue(value any, key string) any {
	if sensitiveTelemetryKey(key) {
		return "[REDACTED]"
	}
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for childKey, childValue := range typed {
			if childKey == ToolCallIDParam {
				continue
			}
			out[childKey] = sanitizeTelemetryValue(childValue, childKey)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i := range typed {
			out[i] = sanitizeTelemetryValue(typed[i], key)
		}
		return out
	case []string:
		out := make([]string, len(typed))
		copy(out, typed)
		return out
	case string:
		return boundTelemetryText(typed, maxTelemetryResultBytes, "value")
	default:
		return typed
	}
}

func sensitiveTelemetryKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		return false
	}
	for _, fragment := range []string{
		"password", "passwd", "secret", "token", "api_key", "apikey",
		"authorization", "credential", "cookie", "private_key", "access_key",
	} {
		if strings.Contains(key, fragment) {
			return true
		}
	}
	return false
}

func boundTelemetryText(content string, limit int, label string) string {
	content = strings.TrimSpace(content)
	if limit <= 0 || len(content) <= limit {
		return content
	}
	marker := fmt.Sprintf("\n... %s truncated (%d bytes omitted) ...\n", label, len(content)-limit)
	if len(marker) >= limit {
		return marker[:limit]
	}
	available := limit - len(marker)
	head := available * 2 / 3
	tail := available - head
	return content[:head] + marker + content[len(content)-tail:]
}
