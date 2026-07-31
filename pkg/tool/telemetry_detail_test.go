package tool

import (
	"strings"
	"testing"

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
