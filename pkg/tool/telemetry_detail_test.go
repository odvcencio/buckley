package tool

import (
	"strings"
	"testing"
	"unicode/utf8"

	"m31labs.dev/buckley/v2/pkg/tool/builtin"
)

func TestWithTelemetryArgumentsRedactsSecrets(t *testing.T) {
	metadata := withTelemetryArguments(nil, map[string]any{
		"path":       "pkg/main.go",
		"api_key":    "do-not-log",
		"nested":     map[string]any{"authorization": "Bearer secret"},
		"safe_value": "visible",
	})
	arguments, _ := metadata["arguments"].(string)
	if strings.Contains(arguments, "do-not-log") || strings.Contains(arguments, "Bearer secret") {
		t.Fatalf("secrets leaked into telemetry: %s", arguments)
	}
	if !strings.Contains(arguments, "[REDACTED]") || !strings.Contains(arguments, "visible") {
		t.Fatalf("unexpected telemetry arguments: %s", arguments)
	}
}

func TestWithTelemetryArgumentsStripsToolCallID(t *testing.T) {
	metadata := withTelemetryArguments(nil, map[string]any{
		"path":          "pkg/main.go",
		ToolCallIDParam: "call-123",
	})
	arguments, _ := metadata["arguments"].(string)
	if strings.Contains(arguments, "call-123") || strings.Contains(arguments, ToolCallIDParam) {
		t.Fatalf("tool call ID leaked into telemetry arguments: %s", arguments)
	}
}

// TestWithTelemetryArgumentsRedactsSliceOfMaps guards the []map[string]any
// shape semantic_search formats its results as, which the old recursive-only
// sanitizer passed through unredacted.
func TestWithTelemetryArgumentsRedactsSliceOfMaps(t *testing.T) {
	metadata := withTelemetryArguments(nil, map[string]any{
		"results": []map[string]any{
			{"file": "a.go", "api_key": "sekrit-one"},
			{"file": "b.go", "token": "sekrit-two"},
		},
	})
	arguments, _ := metadata["arguments"].(string)
	if strings.Contains(arguments, "sekrit-one") || strings.Contains(arguments, "sekrit-two") {
		t.Fatalf("secret leaked through []map[string]any: %s", arguments)
	}
}

// TestWithTelemetryArgumentsRedactsMapStringStringAndStructs guards the
// map[string]string and struct shapes the old recursive-only sanitizer also
// passed through unredacted.
func TestWithTelemetryArgumentsRedactsMapStringStringAndStructs(t *testing.T) {
	type nestedSecret struct {
		APIKey string `json:"api_key"`
	}
	metadata := withTelemetryArguments(nil, map[string]any{
		"headers": map[string]string{"authorization": "Bearer leaked-header"},
		"nested":  nestedSecret{APIKey: "struct-secret"},
	})
	arguments, _ := metadata["arguments"].(string)
	if strings.Contains(arguments, "Bearer leaked-header") {
		t.Fatalf("secret leaked through map[string]string: %s", arguments)
	}
	if strings.Contains(arguments, "struct-secret") {
		t.Fatalf("secret leaked through struct: %s", arguments)
	}
}

// TestWithTelemetryArgumentsUsesArgumentLimitNotResultLimit locks in that the
// arguments path is bounded to the 16 KiB argument limit, not the 64 KiB
// result limit, even though both funnel through the same sanitizer.
func TestWithTelemetryArgumentsUsesArgumentLimitNotResultLimit(t *testing.T) {
	big := strings.Repeat("y", maxTelemetryArgumentsBytes+2*1024) // > 16 KiB, well under 64 KiB
	metadata := withTelemetryArguments(nil, map[string]any{"payload": big})
	arguments, _ := metadata["arguments"].(string)
	if len(arguments) > maxTelemetryArgumentsBytes {
		t.Fatalf("arguments length = %d, want <= %d (argument limit)", len(arguments), maxTelemetryArgumentsBytes)
	}
	if !strings.Contains(arguments, "truncated") {
		t.Fatalf("expected payload above the 16 KiB argument limit to be truncated: %s", arguments)
	}
}

func TestTelemetryResultDetailIsBounded(t *testing.T) {
	result := &builtin.Result{Success: true, Data: map[string]any{"content": strings.Repeat("x", maxTelemetryResultBytes*2)}}
	detail := telemetryResultDetail(result)
	if len(detail) > maxTelemetryResultBytes {
		t.Fatalf("detail length = %d, max %d", len(detail), maxTelemetryResultBytes)
	}
	if !strings.Contains(detail, "truncated") {
		t.Fatalf("bounded detail missing truncation marker")
	}
}

func TestTelemetryResultDetailValidUTF8WhenTruncated(t *testing.T) {
	result := &builtin.Result{Success: true, Data: map[string]any{"content": strings.Repeat("中", maxTelemetryResultBytes)}}
	detail := telemetryResultDetail(result)
	if !utf8.ValidString(detail) {
		t.Fatalf("truncated telemetry result is not valid UTF-8")
	}
}

// TestTelemetryResultDetailPreBoundRedactsSecretBeforeGiantField locks in
// that the cheap pre-bound pass ahead of NormalizeAndSanitize doesn't weaken
// redaction: a secret sorted ahead of a multi-megabyte field (json.Marshal
// sorts map keys, so "api_key" < "giant") must still come out redacted, and
// the giant field after it must still be bounded to the result limit.
func TestTelemetryResultDetailPreBoundRedactsSecretBeforeGiantField(t *testing.T) {
	giant := strings.Repeat("z", 5*maxTelemetryResultBytes) // forces the pre-bound path
	result := &builtin.Result{
		Success: true,
		Data: map[string]any{
			"api_key": "do-not-log-this-secret",
			"giant":   giant,
		},
	}
	detail := telemetryResultDetail(result)
	if strings.Contains(detail, "do-not-log-this-secret") {
		t.Fatalf("secret leaked into pre-bounded telemetry detail: %s", detail)
	}
	if !strings.Contains(detail, "[REDACTED]") {
		t.Fatalf("expected redaction marker in pre-bounded telemetry detail: %s", detail)
	}
	if len(detail) > maxTelemetryResultBytes {
		t.Fatalf("detail length = %d, want <= %d", len(detail), maxTelemetryResultBytes)
	}
}

// TestPreBoundIfOversizedLeavesSmallPayloadsUntouched guards against the
// pre-bound pass firing (and paying its walk cost) on payloads that were
// never going to be truncated anyway.
func TestPreBoundIfOversizedLeavesSmallPayloadsUntouched(t *testing.T) {
	small := map[string]any{"path": "pkg/main.go", "count": 3}
	got := preBoundIfOversized(small, maxTelemetryResultBytes)
	gotMap, ok := got.(map[string]any)
	if !ok || gotMap["path"] != "pkg/main.go" || gotMap["count"] != 3 {
		t.Fatalf("small payload was altered: %#v", got)
	}
}

// TestPreBoundIfOversizedTrimsGiantStringField checks the pre-bound pass
// itself: an oversized payload's string values shrink to the per-field
// limit, but its keys are untouched.
func TestPreBoundIfOversizedTrimsGiantStringField(t *testing.T) {
	giant := strings.Repeat("z", 5*maxTelemetryResultBytes)
	payload := map[string]any{"secret_token": "s3cr3t", "giant": giant}
	got := preBoundIfOversized(payload, maxTelemetryResultBytes)
	gotMap, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any, got %#v", got)
	}
	if _, ok := gotMap["secret_token"]; !ok {
		t.Fatalf("pre-bound pass dropped a key it should have left for redaction: %#v", gotMap)
	}
	trimmedGiant, ok := gotMap["giant"].(string)
	if !ok || len(trimmedGiant) > maxTelemetryResultBytes {
		t.Fatalf("giant field not trimmed to the per-field limit: len=%d", len(trimmedGiant))
	}
}
