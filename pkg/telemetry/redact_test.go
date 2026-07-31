package telemetry

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeValueRedactsSensitiveKeys(t *testing.T) {
	value := map[string]any{
		"path":       "pkg/main.go",
		"api_key":    "do-not-log",
		"nested":     map[string]any{"authorization": "Bearer secret"},
		"safe_value": "visible",
	}
	clean := SanitizeValue(value, "", MaxResultBytes)
	out, ok := clean.(map[string]any)
	if !ok {
		t.Fatalf("SanitizeValue did not return a map: %#v", clean)
	}
	if out["api_key"] != "[REDACTED]" {
		t.Fatalf("api_key not redacted: %#v", out["api_key"])
	}
	nested, ok := out["nested"].(map[string]any)
	if !ok || nested["authorization"] != "[REDACTED]" {
		t.Fatalf("nested authorization not redacted: %#v", out["nested"])
	}
	if out["safe_value"] != "visible" {
		t.Fatalf("safe_value unexpectedly altered: %#v", out["safe_value"])
	}
}

func TestNormalizeAndSanitize_SliceOfMaps(t *testing.T) {
	// []map[string]any is the shape semantic_search formats results as; the
	// raw recursive walk (SanitizeValue) doesn't know how to descend into
	// it, but NormalizeAndSanitize should after the JSON round-trip.
	value := []map[string]any{
		{"file": "a.go", "api_key": "sekrit-one"},
		{"file": "b.go", "token": "sekrit-two"},
	}
	clean := NormalizeAndSanitize(value, MaxResultBytes)
	encoded := mustJSONString(t, clean)
	if strings.Contains(encoded, "sekrit-one") || strings.Contains(encoded, "sekrit-two") {
		t.Fatalf("secrets leaked through []map[string]any: %s", encoded)
	}
	if !strings.Contains(encoded, "[REDACTED]") {
		t.Fatalf("expected redaction marker in output: %s", encoded)
	}
}

func TestNormalizeAndSanitize_MapStringString(t *testing.T) {
	value := map[string]string{
		"authorization": "Bearer leaked-token",
		"note":          "safe text",
	}
	clean := NormalizeAndSanitize(value, MaxResultBytes)
	encoded := mustJSONString(t, clean)
	if strings.Contains(encoded, "leaked-token") {
		t.Fatalf("secret leaked through map[string]string: %s", encoded)
	}
	if !strings.Contains(encoded, "safe text") {
		t.Fatalf("safe value unexpectedly redacted: %s", encoded)
	}
}

func TestNormalizeAndSanitize_Struct(t *testing.T) {
	type payload struct {
		APIKey string `json:"api_key"`
		Note   string `json:"note"`
	}
	value := payload{APIKey: "struct-secret", Note: "safe text"}
	clean := NormalizeAndSanitize(value, MaxResultBytes)
	encoded := mustJSONString(t, clean)
	if strings.Contains(encoded, "struct-secret") {
		t.Fatalf("secret leaked through struct: %s", encoded)
	}
	if !strings.Contains(encoded, "safe text") {
		t.Fatalf("safe value unexpectedly redacted: %s", encoded)
	}
}

func TestNormalizeAndSanitize_UnmarshalableValueFallsBackToPlaceholder(t *testing.T) {
	clean := NormalizeAndSanitize(make(chan int), MaxResultBytes)
	text, ok := clean.(string)
	if !ok || !strings.Contains(text, "REDACTED") {
		t.Fatalf("expected redacted placeholder for unmarshalable value, got %#v", clean)
	}
}

func TestBoundText_ThreadsCallerLimit(t *testing.T) {
	content := strings.Repeat("x", MaxResultBytes)
	bounded := BoundText(content, MaxArgumentBytes, "tool arguments")
	if len(bounded) > MaxArgumentBytes {
		t.Fatalf("bounded length = %d, want <= %d", len(bounded), MaxArgumentBytes)
	}
	if !strings.Contains(bounded, "truncated") {
		t.Fatalf("bounded text missing truncation marker: %s", bounded)
	}
}

func TestBoundText_ValidUTF8AndCorrectOmittedCount(t *testing.T) {
	// Multi-byte rune repeated so naive byte-offset slicing would split a
	// character right at the truncation boundary.
	rune3 := "中" // 3-byte UTF-8 character
	content := strings.Repeat(rune3, 200)
	limit := 100 // not a multiple of 3, forces boundary misalignment

	bounded := BoundText(content, limit, "value")
	if !utf8.ValidString(bounded) {
		t.Fatalf("truncated output is not valid UTF-8: %q", bounded)
	}

	// The reported omitted-byte count should equal what was actually cut.
	head, marker, tail := splitOnMarker(t, bounded)
	gotOmitted := extractOmittedCount(t, marker)
	wantOmitted := len(content) - len(head) - len(tail)
	if gotOmitted != wantOmitted {
		t.Fatalf("reported omitted = %d, want %d (marker=%q)", gotOmitted, wantOmitted, marker)
	}
}

func TestBoundText_NoTruncationBelowLimit(t *testing.T) {
	content := "short and sweet"
	if got := BoundText(content, MaxResultBytes, "value"); got != content {
		t.Fatalf("BoundText altered content below the limit: %q", got)
	}
}

func mustJSONString(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(data)
}

func splitOnMarker(t *testing.T, bounded string) (head, marker, tail string) {
	t.Helper()
	start := strings.Index(bounded, "\n... ")
	if start < 0 {
		t.Fatalf("marker not found in %q", bounded)
	}
	relEnd := strings.Index(bounded[start:], " ...\n")
	if relEnd < 0 {
		t.Fatalf("marker end not found in %q", bounded)
	}
	end := start + relEnd + len(" ...\n")
	return bounded[:start], bounded[start:end], bounded[end:]
}

var omittedCountPattern = regexp.MustCompile(`\((\d+) bytes omitted\)`)

func extractOmittedCount(t *testing.T, marker string) int {
	t.Helper()
	matches := omittedCountPattern.FindStringSubmatch(marker)
	if len(matches) != 2 {
		t.Fatalf("could not extract omitted count from marker %q", marker)
	}
	n, err := strconv.Atoi(matches[1])
	if err != nil {
		t.Fatalf("parse omitted count: %v", err)
	}
	return n
}
