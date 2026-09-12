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

func TestReadEditContextPreservesWhitespace(t *testing.T) {
	for _, useToon := range []bool{false, true} {
		for _, newline := range []string{"\n", "\r\n"} {
			t.Run(strconv.FormatBool(useToon)+strconv.Quote(newline), func(t *testing.T) {
				SetResultEncoding(useToon)
				t.Cleanup(func() { SetResultEncoding(true) })
				root := t.TempDir()
				path := filepath.Join(root, "sample.go")
				source := strings.Join([]string{"package sample", "func run() {", "\tif true {", "\t\tliteral := \"\\n\"", "\t\tvalue := \"old\"", "\t}", "}", ""}, newline)
				if err := os.WriteFile(path, []byte(source), 0600); err != nil {
					t.Fatal(err)
				}
				reader := &builtin.ReadFileTool{}
				reader.SetWorkDir(root)
				result, err := reader.Execute(map[string]any{"path": "sample.go", "start_line": 3, "end_line": 5})
				if err != nil || result == nil || !result.Success {
					t.Fatalf("read=%+v err=%v", result, err)
				}
				encoded, err := ToModelOutput(result)
				if err != nil {
					t.Fatal(err)
				}
				var content string
				if useToon {
					for _, line := range strings.Split(encoded, "\n") {
						if raw, ok := strings.CutPrefix(strings.TrimSpace(line), "content: "); ok {
							content, err = strconv.Unquote(raw)
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
					if err := json.Unmarshal([]byte(encoded), &payload); err != nil {
						t.Fatal(err)
					}
					content = payload.Data.Content
				}
				want := strings.Join(strings.Split(source, "\n")[2:5], "\n")
				if content != want {
					t.Fatalf("model content=%q, want %q", content, want)
				}
				editor := &builtin.EditFileTool{}
				editor.SetWorkDir(root)
				rejected, err := editor.Execute(map[string]any{"path": "sample.go", "old_string": "\t\t\tvalue := \"old\"", "new_string": "wrong"})
				if err != nil || rejected == nil || rejected.Success {
					t.Fatalf("indentation mismatch accepted: %+v %v", rejected, err)
				}
				unchanged, err := os.ReadFile(path)
				if err != nil || string(unchanged) != source {
					t.Fatal("rejected edit changed source")
				}
				edited, err := editor.Execute(map[string]any{"path": "sample.go", "old_string": `value := "old"`, "new_string": `value := "new"`})
				if err != nil || edited == nil || !edited.Success {
					t.Fatalf("short anchor edit=%+v err=%v", edited, err)
				}
				actual, err := os.ReadFile(path)
				if err != nil || string(actual) != strings.Replace(source, `value := "old"`, `value := "new"`, 1) {
					t.Fatalf("short anchor altered surrounding text: %q err=%v", actual, err)
				}
			})
		}
	}
}
