package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFileTool_DensePagePreservesSnapshot(t *testing.T) {
	for _, finalNewline := range []bool{false, true} {
		t.Run(fmt.Sprint(finalNewline), func(t *testing.T) {
			dir := t.TempDir()
			content := strings.Repeat("\n", 65536) + "target\r\nlast"
			if finalNewline {
				content += "\n"
			}
			if err := os.WriteFile(filepath.Join(dir, "source.txt"), []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			reader := &ReadFileTool{}
			reader.SetWorkDir(dir)
			for _, params := range []map[string]any{{"path": "source.txt", "start_line": 65536, "end_line": 65538}, {"path": "source.txt", "anchor": "target"}} {
				result, err := reader.Execute(params)
				if err != nil || !result.Success {
					t.Fatalf("read: %+v, %v", result, err)
				}
				start := 65536
				want := "\ntarget\r\nlast"
				if _, anchor := params["anchor"]; anchor {
					start++
					want = "target\r\nlast"
				}
				page := result.DisplayData["page"].(map[string]any)
				if result.Data["content"] != content || result.DisplayData["content"] != want || page["total_lines"] != 65538 || page["start_line"] != start || page["end_line"] != 65538 || page["has_more"] != false {
					t.Fatalf("page or snapshot changed: page=%v display=%q", page, result.DisplayData["content"])
				}
			}
		})
	}
}

func BenchmarkReadFileTool_ShortPage(b *testing.B) {
	for _, tc := range []struct {
		name, content string
		start, end    int
	}{
		{"ordinary", strings.Repeat("0123456789\n", 16384), 8190, 8195},
		{"newline_dense", strings.Repeat("\n", 131072) + "target\n", 65535, 65539},
	} {
		b.Run(tc.name, func(b *testing.B) {
			dir := b.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "source.txt"), []byte(tc.content), 0600); err != nil {
				b.Fatal(err)
			}
			reader := &ReadFileTool{}
			reader.SetWorkDir(dir)
			params := map[string]any{"path": "source.txt", "start_line": tc.start, "end_line": tc.end}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				result, err := reader.Execute(params)
				if err != nil || !result.Success {
					b.Fatalf("read: %+v, %v", result, err)
				}
			}
		})
	}
}
