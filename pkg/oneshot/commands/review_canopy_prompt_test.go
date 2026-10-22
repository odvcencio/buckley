package commands

import (
	"encoding/json"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/encoding/toon"
)

func TestCompactCanopyPrompt_PreservesEvidenceAndLabelsFormat(t *testing.T) {
	rows := make([]map[string]any, 12)
	for i := range rows {
		rows[i] = map[string]any{"file": "path,with comma.go", "name": "München", "start_line": i + 1}
	}
	raw, err := json.Marshal(map[string]any{
		"symbols":  rows,
		"coverage": map[string]any{"complete": false, "count": 0, "unknown": nil},
		"warning":  "omitted rows; inspect source\nthen verify",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{false, true} {
		encoded, compact := compactCanopyPrompt(string(raw), toon.New(enabled))
		if compact != enabled {
			t.Fatalf("enabled=%v compact=%v", enabled, compact)
		}
		if !enabled && encoded != string(raw) {
			t.Fatal("JSON-only mode changed evidence")
		}
		if enabled {
			if len(encoded) >= len(raw) {
				t.Fatal("compact representation grew")
			}
			for _, want := range []string{"symbols[12]{file,name,start_line}", `"path,with comma.go",München,1`, "complete: false", "count: 0", "unknown: null", `omitted rows; inspect source\nthen verify`} {
				if !strings.Contains(encoded, want) {
					t.Errorf("compact evidence missing %q: %s", want, encoded)
				}
			}
		}
		prompt := BuildProjectPrompt(&ProjectContext{CanopySummary: encoded, CanopySummaryToon: compact})
		fence := "```json\n"
		if compact {
			fence = "```text\n"
		}
		if !strings.Contains(prompt, fence+encoded+"\n```") {
			t.Fatal("project prompt mislabeled or rewrote evidence")
		}
	}
}

func TestCompactCanopyPrompt_JSONFallback(t *testing.T) {
	for _, summary := range []string{
		"not JSON", "null", "{}", strings.Repeat(`{"a":`, 12) + "1" + strings.Repeat("}", 12),
		`{"data":[{"name":"f","parameters":{"x":1}},{"name":"g","parameters":{"x":2}}]}`,
		`{"rows":[{"count":9007199254740993},{"count":9007199254740993}]}`,
		`{"rows":[{"count":-9007199254740993},{"count":-9007199254740993}]}`,
		`{"rows":[{"nested":[9007199254740993]},{"nested":[9007199254740993]}]}`,
		`{"padding":"` + strings.Repeat("x", maxCanopyProjectBytes) + `"}`,
	} {
		encoded, compact := compactCanopyPrompt(summary, toon.New(true))
		if compact || encoded != summary {
			t.Fatalf("fallback changed evidence: %q -> %q compact=%v", summary, encoded, compact)
		}
	}
}
