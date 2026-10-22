package tool

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestReadFileModelPages_ByteBoundedRoundTrip(t *testing.T) {
	for _, useToon := range []bool{false, true} {
		for _, streaming := range []bool{false, true} {
			for _, newline := range []string{"\n", "\r\n"} {
				t.Run(fmt.Sprintf("toon=%t/streaming=%t/newline=%q", useToon, streaming, newline), func(t *testing.T) {
					SetResultEncoding(useToon)
					t.Cleanup(func() { SetResultEncoding(true) })
					root := t.TempDir()
					lines := make([]string, 180)
					for i := range lines {
						lines[i] = fmt.Sprintf("line %03d: %s literal \\n", i+1, strings.Repeat("界", 160))
					}
					source := strings.Join(lines, newline)
					if err := os.WriteFile(filepath.Join(root, "wide.txt"), []byte(source), 0600); err != nil {
						t.Fatal(err)
					}
					reader := &builtin.ReadFileTool{}
					reader.SetWorkDir(root)
					if streaming {
						reader.SetMaxFileSizeBytes(32 << 10)
					}
					var chunks []string
					for start := 1; start <= len(lines); {
						result, err := reader.Execute(map[string]any{"path": "wide.txt", "start_line": start, "line_numbers": true})
						if err != nil || result == nil || !result.Success {
							t.Fatalf("read=%+v err=%v", result, err)
						}
						wire, err := ToModelOutput(result)
						if err != nil || len(wire) > DefaultModelOutputBytes || strings.Contains(wire, "output_head") {
							t.Fatalf("page was clipped instead of paginated: bytes=%d err=%v", len(wire), err)
						}
						var content string
						if useToon {
							for _, line := range strings.Split(wire, "\n") {
								if encoded, ok := strings.CutPrefix(strings.TrimSpace(line), "content: "); ok {
									content, err = strconv.Unquote(encoded)
									if err != nil {
										t.Fatal(err)
									}
								}
							}
						} else {
							var payload struct {
								Data struct{ Content string }
							}
							if err := json.Unmarshal([]byte(wire), &payload); err != nil {
								t.Fatal(err)
							}
							content = payload.Data.Content
						}
						page := result.Data["page"].(map[string]any)
						end := page["end_line"].(int)
						if end < start || end >= start+100 || end > len(lines) || content == "" {
							t.Fatalf("invalid page: %+v", page)
						}
						want := strings.Join(lines[start-1:end], newline)
						if newline == "\r\n" && end < len(lines) {
							want += "\r"
						}
						if content != want {
							t.Fatalf("page %d-%d did not preserve source bytes", start, end)
						}
						if newline == "\r\n" && end < len(lines) {
							content = strings.TrimSuffix(content, "\r")
						}
						chunks = append(chunks, content)
						if !streaming && result.Data["content"] != source {
							t.Fatal("model paging changed the full API snapshot")
						}
						if page["has_more"] == false {
							if end != len(lines) {
								t.Fatal("page skipped unread lines")
							}
							break
						}
						next := page["next_start_line"].(int)
						if next != end+1 {
							t.Fatalf("cursor skipped lines: %+v", page)
						}
						start = next
					}
					if strings.Join(chunks, newline) != source || len(chunks) < 3 {
						t.Fatal("model pages lost or duplicated file content")
					}
					t.Logf("model_pages=%d complete_source_preserved=true", len(chunks))
				})
			}
		}
	}
}
