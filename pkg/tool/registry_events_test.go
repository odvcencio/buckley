package tool

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/telemetry"
	"m31labs.dev/buckley/pkg/tool/builtin"
	"m31labs.dev/buckley/pkg/touch"
)

type panickingPanicStringer struct{}

func (panickingPanicStringer) String() string {
	panic("must not stringify panic metadata")
}

func TestPublishToolEventSanitizesAndBoundsPanicMetadata(t *testing.T) {
	hub := telemetry.NewHub()
	eventCh, unsubscribe := hub.Subscribe()
	t.Cleanup(unsubscribe)
	r := NewEmptyRegistry()
	r.EnableTelemetry(hub, "session-panic-metadata")

	stack := `{"api_key":"stack-secret","frame":"` + strings.Repeat("x", maxTelemetryPanicStackBytes*2) + `"}`
	r.publishToolEventBestEffort(
		telemetry.EventToolFailed,
		"call-panic-metadata",
		"panic_tool",
		touch.RichFields{},
		time.Now(),
		nil,
		nil,
		1,
		map[string]any{
			"panic_stack": stack,
			"panic_value": "panic-value-secret",
		},
	)

	events := drainTelemetryEvents(eventCh)
	if len(events) != 1 {
		t.Fatalf("published %d events, want 1", len(events))
	}
	data := events[0].Data
	sanitizedStack, _ := data["panic_stack"].(string)
	if len(sanitizedStack) > maxTelemetryPanicStackBytes {
		t.Fatalf("panic_stack length = %d, want <= %d", len(sanitizedStack), maxTelemetryPanicStackBytes)
	}
	if strings.Contains(sanitizedStack, "stack-secret") || !strings.Contains(sanitizedStack, "REDACTED") || !strings.Contains(sanitizedStack, "truncated") {
		t.Fatalf("panic_stack was not redacted and bounded: %q", sanitizedStack)
	}
	if got := data["panic_value"]; got != "[REDACTED: panic value]" {
		t.Fatalf("panic_value = %v, want redaction marker", got)
	}
	if got := data["panic_type"]; got != "string" {
		t.Fatalf("panic_type = %v, want string", got)
	}
}

func TestSanitizePanicValueHandlesStructuredHugeAndStringerValues(t *testing.T) {
	tests := []struct {
		name      string
		value     any
		wantType  string
		forbidden string
		truncated bool
	}{
		{
			name:      "structured secret",
			value:     map[string]any{"api_key": "map-secret", "note": "safe"},
			wantType:  "map[string]interface {}",
			forbidden: "map-secret",
		},
		{
			name:      "huge structured value",
			value:     map[string]any{"detail": strings.Repeat("z", maxTelemetryPanicValueBytes*3)},
			wantType:  "map[string]interface {}",
			truncated: true,
		},
		{
			name:     "panicking stringer",
			value:    panickingPanicStringer{},
			wantType: "tool.panickingPanicStringer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, valueType := sanitizePanicValue(tt.value)
			if len(value) > maxTelemetryPanicValueBytes {
				t.Fatalf("panic value length = %d, want <= %d", len(value), maxTelemetryPanicValueBytes)
			}
			if valueType != tt.wantType {
				t.Fatalf("panic type = %q, want %q", valueType, tt.wantType)
			}
			if tt.forbidden != "" && strings.Contains(value, tt.forbidden) {
				t.Fatalf("panic value leaked %q: %s", tt.forbidden, value)
			}
			if tt.truncated && !strings.Contains(value, "truncated") {
				t.Fatalf("panic value was not truncated: %s", value)
			}
		})
	}
}

func TestPublishTelemetryEventProjectsOpaqueToolFields(t *testing.T) {
	hub := telemetry.NewHub()
	eventCh, unsubscribe := hub.Subscribe()
	t.Cleanup(unsubscribe)
	r := NewEmptyRegistry()
	r.EnableTelemetry(hub, "session-safe-fields")

	r.publishToolEventBestEffort(
		telemetry.EventToolFailed,
		"call-safe-fields",
		"run_shell",
		touch.RichFields{
			Command:     "curl -H 'Authorization: Bearer top-secret' https://example.test/" + string([]byte{0xff}) + strings.Repeat("x", 20_000),
			FilePath:    "/workspace/private/top-secret.txt",
			Description: "failed with password=top-secret",
		},
		time.Now(),
		&builtin.Result{
			Success: false,
			Data: map[string]any{
				"stderr": "api_key=nested-secret " + strings.Repeat("中", 20_000),
			},
			Error: "request failed: token=top-secret",
		},
		nil,
		0,
		nil,
	)

	select {
	case event := <-eventCh:
		for _, key := range []string{"command", "filePath", "description", "result", "error"} {
			if value, ok := event.Data[key].(string); ok {
				if !utf8.ValidString(value) {
					t.Fatalf("%s is not valid UTF-8: %q", key, value)
				}
				if len(value) > telemetry.MaxResultBytes {
					t.Fatalf("%s length = %d, want <= %d", key, len(value), telemetry.MaxResultBytes)
				}
			}
		}
		for _, key := range []string{"command", "filePath", "result", "error"} {
			if value, _ := event.Data[key].(string); strings.Contains(value, "top-secret") || strings.Contains(value, "nested-secret") {
				t.Fatalf("opaque field %s leaked secret: %q", key, value)
			}
		}
		if got := event.Data["command_bytes"]; got == nil {
			t.Fatal("command size projection missing")
		}
		if got := event.Data["command_fingerprint"]; got == nil {
			t.Fatal("command fingerprint projection missing")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for safe telemetry event")
	}
}
