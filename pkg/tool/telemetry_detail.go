package tool

import (
	"encoding/json"
	"fmt"
	"strings"

	"m31labs.dev/buckley/v2/pkg/telemetry"
	"m31labs.dev/buckley/v2/pkg/tool/builtin"
)

// maxTelemetryArgumentsBytes and maxTelemetryResultBytes are aliases for the
// shared limits in pkg/telemetry, kept so existing call sites and tests in
// this package don't need to know about the shared package's constant
// names.
const (
	maxTelemetryArgumentsBytes = telemetry.MaxArgumentBytes
	maxTelemetryResultBytes    = telemetry.MaxResultBytes
)

func withTelemetryArguments(metadata map[string]any, params map[string]any) map[string]any {
	out := make(map[string]any, len(metadata)+1)
	for key, value := range metadata {
		out[key] = value
	}
	clean := telemetry.NormalizeAndSanitize(stripToolCallID(params), maxTelemetryArgumentsBytes)
	if encoded, err := json.MarshalIndent(clean, "", "  "); err == nil {
		out["arguments"] = telemetry.BoundText(string(encoded), maxTelemetryArgumentsBytes, "tool arguments")
	}
	return out
}

// stripToolCallID returns a shallow copy of params without the internal
// tool-call-ID bookkeeping field, which callers inject at the top level and
// which has no business appearing in telemetry.
func stripToolCallID(params map[string]any) map[string]any {
	if params == nil {
		return nil
	}
	if _, ok := params[ToolCallIDParam]; !ok {
		return params
	}
	out := make(map[string]any, len(params))
	for key, value := range params {
		if key == ToolCallIDParam {
			continue
		}
		out[key] = value
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
	clean := telemetry.NormalizeAndSanitize(payload, maxTelemetryResultBytes)
	encoded, err := json.MarshalIndent(clean, "", "  ")
	if err != nil {
		return telemetry.BoundText(fmt.Sprintf("success=%t error=%q", result.Success, result.Error), maxTelemetryResultBytes, "tool result")
	}
	return telemetry.BoundText(string(encoded), maxTelemetryResultBytes, "tool result")
}
