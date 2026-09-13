package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFileToolOversizedRangeReturnsBoundedPage(t *testing.T) {
	for _, total := range []int{0, 2, 250} {
		t.Run(fmt.Sprint(total), func(t *testing.T) {
			lines := make([]string, total)
			for i := range lines {
				lines[i] = fmt.Sprintf("line %d", i+1)
			}
			path := filepath.Join(t.TempDir(), "sample.txt")
			if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
				t.Fatal(err)
			}
			tool := &ReadFileTool{}
			start := 1
			var observed []string
			for {
				result, err := tool.Execute(map[string]any{"path": path, "start_line": start, "end_line": start + 199})
				if err != nil {
					t.Fatal(err)
				}
				if !result.Success {
					t.Fatalf("read failed: %s", result.Error)
				}
				if !result.ShouldAbridge {
					t.Fatal("explicit read must expose bounded display content")
				}
				page := result.DisplayData["page"].(map[string]any)
				content := result.DisplayData["content"].(string)
				var got []string
				if content != "" {
					got = strings.Split(content, "\n")
				}
				if len(got) > 100 {
					t.Fatalf("page has %d lines", len(got))
				}
				observed = append(observed, got...)
				if page["has_more"] == false {
					break
				}
				next, ok := page["next_start_line"].(int)
				if !ok || next != start+len(got) || next <= start {
					t.Fatalf("invalid continuation: %#v", page)
				}
				start = next
			}
			if strings.Join(observed, "\n") != strings.Join(lines, "\n") {
				t.Fatalf("paged content differs: %v", observed)
			}
		})
	}
}
