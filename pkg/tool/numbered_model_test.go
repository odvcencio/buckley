package tool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestToModelOutput_NumberedRead(t *testing.T) {
	for _, useToon := range []bool{false, true} {
		for _, newline := range []string{"\n", "\r\n"} {
			for _, redacted := range []bool{false, true} {
				t.Run(strconv.FormatBool(useToon)+strconv.Quote(newline)+strconv.FormatBool(redacted), func(t *testing.T) {
					SetResultEncoding(useToon)
					t.Cleanup(func() { SetResultEncoding(true) })
					path := filepath.Join(t.TempDir(), "numbered.txt")
					source := strings.Join([]string{"UNREAD_PRIVATE_CANARY", "2: real source", "3: more source", "after", ""}, newline)
					if err := os.WriteFile(path, []byte(source), 0600); err != nil {
						t.Fatal(err)
					}
					result, err := (&builtin.ReadFileTool{}).Execute(map[string]any{"path": path, "start_line": 2, "end_line": 3, "line_numbers": true})
					if err != nil || result == nil || !result.Success {
						t.Fatalf("read=%+v err=%v", result, err)
					}
					want := strings.Join(strings.Split(source, "\n")[1:3], "\n")
					if redacted {
						result.DisplayData["content"] = "2: [MASKED]\n3: [MASKED]"
						want = "[MASKED]\n[MASKED]"
					}
					display := result.DisplayData["content"]
					if result.DisplayData["line_numbers"] != true {
						t.Fatal("missing display marker")
					}
					wire, err := ToModelOutput(result)
					if err != nil {
						t.Fatal(err)
					}
					var content string
					if useToon {
						for _, line := range strings.Split(wire, "\n") {
							if value, ok := strings.CutPrefix(strings.TrimSpace(line), "content: "); ok {
								content, err = strconv.Unquote(value)
								if err != nil {
									t.Fatal(err)
								}
							}
						}
					} else {
						var payload struct {
							Data struct {
								Content string `json:"content"`
							} `json:"data"`
						}
						if err := json.Unmarshal([]byte(wire), &payload); err != nil {
							t.Fatal(err)
						}
						content = payload.Data.Content
					}
					if content != want {
						t.Fatalf("model content=%q want=%q", content, want)
					}
					if strings.Contains(wire, "UNREAD_PRIVATE_CANARY") || strings.Contains(wire, "line_numbers") {
						t.Fatalf("UI marker or hidden source in model output: %s", wire)
					}
					if result.DisplayData["content"] != display || result.DisplayData["line_numbers"] != true || result.Data["content"] != source {
						t.Fatal("model formatting mutated API/UI data")
					}
					if !redacted {
						edited, err := (&builtin.EditFileTool{}).Execute(map[string]any{"path": path, "old_string": content, "new_string": strings.Replace(content, "real source", "updated source", 1)})
						if err != nil || edited == nil || !edited.Success {
							t.Fatalf("raw model edit=%+v %v", edited, err)
						}
						actual, err := os.ReadFile(path)
						if err != nil || string(actual) != strings.Replace(source, "real source", "updated source", 1) {
							t.Fatalf("raw edit changed unrelated bytes: %q %v", actual, err)
						}
					}
				})
			}
		}
	}
}

func TestToModelOutput_NumberedInvalidDisplay(t *testing.T) {
	SetResultEncoding(false)
	t.Cleanup(func() { SetResultEncoding(true) })
	for _, tc := range []struct {
		name, content string
		start, end    any
	}{
		{"partial-prefix", "2: [MASKED]\n[MASKED]", 2, 3},
		{"count-mismatch", "2: [MASKED]", 2, 3},
		{"plain-mask", "[MASKED]\n[MASKED]", 2, 3},
		{"invalid-start-type", "2: [MASKED]\n3: [MASKED]", "2", 3},
		{"reversed", "2: [MASKED]", 2, 1},
		{"zero-start", "0: [MASKED]", 0, 0},
		{"oversized-range", "2: [MASKED]", 2, 102},
	} {
		t.Run(tc.name, func(t *testing.T) {
			visible := map[string]any{"content": tc.content, "line_numbers": true, "page": map[string]any{"start_line": tc.start, "end_line": tc.end}}
			result := &builtin.Result{Success: true, ShouldAbridge: true, Data: map[string]any{"content": "UNREAD_PRIVATE_CANARY"}, DisplayData: visible}
			wire, err := ToModelOutput(result)
			if err != nil {
				t.Fatal(err)
			}
			var payload struct {
				Data map[string]any `json:"data"`
			}
			if err := json.Unmarshal([]byte(wire), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Data["content"] != tc.content || payload.Data["line_numbers"] != true || visible["content"] != tc.content || visible["line_numbers"] != true || strings.Contains(wire, "UNREAD_PRIVATE_CANARY") {
				t.Fatalf("invalid display was repaired or leaked source: %s", wire)
			}
		})
	}
}
