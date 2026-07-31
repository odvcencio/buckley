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
