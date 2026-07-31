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

// preBoundThresholdMultiplier caps how large a raw telemetry payload may
// grow, relative to the final byte limit, before preBoundIfOversized starts
// trimming its oversized string fields. telemetry.NormalizeAndSanitize
// already bounds every string value it visits, so trimming early only skips
// redundant work on payloads far bigger than anything the final truncation
// keeps -- it changes nothing observable in the result.
const preBoundThresholdMultiplier = 4

func withTelemetryArguments(metadata map[string]any, params map[string]any) map[string]any {
	out := make(map[string]any, len(metadata)+1)
	for key, value := range metadata {
		out[key] = value
	}
	trimmed := preBoundIfOversized(stripToolCallID(params), maxTelemetryArgumentsBytes)
	clean := telemetry.NormalizeAndSanitize(trimmed, maxTelemetryArgumentsBytes)
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
	trimmed := preBoundIfOversized(payload, maxTelemetryResultBytes)
	clean := telemetry.NormalizeAndSanitize(trimmed, maxTelemetryResultBytes)
	encoded, err := json.MarshalIndent(clean, "", "  ")
	if err != nil {
		return telemetry.BoundText(fmt.Sprintf("success=%t error=%q", result.Success, result.Error), maxTelemetryResultBytes, "tool result")
	}
	return telemetry.BoundText(string(encoded), maxTelemetryResultBytes, "tool result")
}

// preBoundIfOversized cheaply shrinks value's string fields before the
// expensive normalize/redact/encode pass in withTelemetryArguments and
// telemetryResultDetail, but only when the raw payload is dramatically
// bigger than what the final byte limit keeps. Below that threshold the
// per-string bounding telemetry.NormalizeAndSanitize already does is cheap
// enough on its own, and pre-walking the value here would be pure overhead.
//
// Pre-bounding only ever shortens string values; it never touches map keys
// or drops fields, so key-based redaction in the normalize/sanitize pass
// that follows still sees every field name it needs to.
func preBoundIfOversized(value any, limit int) any {
	if value == nil || limit <= 0 {
		return value
	}
	if approxByteSize(value) <= limit*preBoundThresholdMultiplier {
		return value
	}
	return preBoundOversizedStrings(value, limit)
}

// approxByteSize estimates, without doing a full JSON marshal, how many
// bytes value would take up on the wire. It only descends into the
// container shapes preBoundOversizedStrings (and telemetry.SanitizeValue)
// know how to trim directly: map[string]any, []any, and []string. Other
// shapes are counted as a small fixed guess, since seeing inside them
// requires the JSON round-trip telemetry.NormalizeAndSanitize already does;
// underestimating those is safe because it only means preBoundIfOversized
// skips the cheap pre-trim and falls through to the full, correct path.
func approxByteSize(value any) int {
	switch typed := value.(type) {
	case nil:
		return 4
	case map[string]any:
		total := 0
		for key, v := range typed {
			total += len(key) + approxByteSize(v)
		}
		return total
	case []any:
		total := 0
		for _, v := range typed {
			total += approxByteSize(v)
		}
		return total
	case []string:
		total := 0
		for _, s := range typed {
			total += len(s)
		}
		return total
	case string:
		return len(typed)
	default:
		return 32
	}
}

// preBoundOversizedStrings recursively caps string values inside value to at
// most perFieldLimit bytes, walking the same container shapes
// approxByteSize understands. It leaves map keys, non-string leaves, and any
// shape it doesn't recognize untouched: key-based redaction and full
// correctness still happen afterward in telemetry.NormalizeAndSanitize, so
// this pass only needs to shrink payload size cheaply, not exactly.
func preBoundOversizedStrings(value any, perFieldLimit int) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, v := range typed {
			out[key] = preBoundOversizedStrings(v, perFieldLimit)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, v := range typed {
			out[i] = preBoundOversizedStrings(v, perFieldLimit)
		}
		return out
	case []string:
		out := make([]string, len(typed))
		for i, s := range typed {
			out[i] = telemetry.BoundText(s, perFieldLimit, "value")
		}
		return out
	case string:
		return telemetry.BoundText(typed, perFieldLimit, "value")
	default:
		return typed
	}
}
