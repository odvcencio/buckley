package builtin

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSearchTextTool_BoundedPagesPreserveEveryRecord(t *testing.T) {
	backends := map[string]string{}
	for _, name := range []string{"rg", "grep"} {
		if path, err := exec.LookPath(name); err == nil {
			backends[name] = path
		}
	}
	for name, executable := range backends {
		t.Run(name, func(t *testing.T) {
			bin := t.TempDir()
			if err := os.Symlink(executable, filepath.Join(bin, name)); err != nil {
				t.Skipf("cannot isolate backend: %v", err)
			}
			t.Setenv("PATH", bin)
			root := t.TempDir()
			line := strings.Repeat("界\t", 12000) + "needle" + strings.Repeat("終", 4000)
			if err := os.WriteFile(filepath.Join(root, "long.txt"), []byte(strings.Repeat(line+"\n", 40)), 0o600); err != nil {
				t.Fatal(err)
			}
			tool := &SearchTextTool{}
			tool.SetWorkDir(root)
			for offset := 0; offset < 40; {
				result, err := tool.Execute(map[string]any{"query": "needle", "literal": true, "path": "long.txt", "offset": offset, "limit": 500})
				if err != nil || !result.Success {
					t.Fatalf("page at %d: result=%+v err=%v", offset, result, err)
				}
				records := result.Data["matches"].([]map[string]any)
				encoded, err := json.Marshal(records)
				if err != nil || len(encoded) > DefaultSearchPageBytes || len(records) == 0 {
					t.Fatalf("page at %d: encoded_bytes=%d records=%d error=%v", offset, len(encoded), len(records), err)
				}
				if result.Data["count"] != 40 || result.Data["records"] != 40 {
					t.Fatalf("counts changed while paging: %#v", result.Data)
				}
				for i, record := range records {
					text := record["match"].(string)
					if record["line"] != offset+i+1 || record["text_truncated"] != true || !utf8.ValidString(text) || len(text) > DefaultSearchRecordBytes {
						t.Fatalf("lost record identity or excerpt: line=%v truncated=%v bytes=%d", record["line"], record["text_truncated"], len(text))
					}
					if name == "rg" && (!strings.Contains(text, "needle") || record["column"] != 48001 || record["text_start_column"].(int) <= 1) {
						t.Fatalf("excerpt lost the distant match or original column: %#v", record)
					}
				}
				offset += len(records)
				next, more := result.Data["next_offset"]
				if (offset < 40) != more || (more && next != offset) {
					t.Fatalf("next_offset=%v present=%v, want %d while more=%v", next, more, offset, offset < 40)
				}
			}
		})
	}
}

func BenchmarkSearchTextTool_SmallPage(b *testing.B) {
	for _, tc := range []struct {
		name             string
		lines, lineBytes int
	}{{"many_matches", 20000, 80}, {"long_lines", 100, 64 << 10}} {
		b.Run(tc.name, func(b *testing.B) {
			if !toolExists("rg") {
				b.Skip("ripgrep unavailable")
			}
			root := b.TempDir()
			file, err := os.Create(filepath.Join(root, "data.txt"))
			if err != nil {
				b.Fatal(err)
			}
			writer := bufio.NewWriter(file)
			line := "needle" + strings.Repeat("x", tc.lineBytes-7) + "\n"
			for i := 0; i < tc.lines; i++ {
				if _, err := writer.WriteString(line); err != nil {
					b.Fatal(err)
				}
			}
			if err := writer.Flush(); err != nil {
				b.Fatal(err)
			}
			if err := file.Close(); err != nil {
				b.Fatal(err)
			}
			tool := &SearchTextTool{}
			tool.SetWorkDir(root)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				result, err := tool.Execute(map[string]any{"query": "needle", "path": "data.txt", "limit": 50})
				if err != nil || !result.Success {
					b.Fatal(fmt.Sprintf("search failed: %+v %v", result, err))
				}
				encoded, _ := json.Marshal(result.Data["matches"])
				b.ReportMetric(float64(len(encoded)), "page-bytes")
			}
		})
	}
}

func TestSearchTextTool_EncodedPageBudget(t *testing.T) {
	root := t.TempDir()
	line := strings.Repeat("\x01", 1000) + "needle\n"
	if err := os.WriteFile(filepath.Join(root, "escaped.txt"), []byte(strings.Repeat(line, 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, budget := range []int{DefaultSearchPageBytes, 1024} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			tool := &SearchTextTool{}
			tool.SetWorkDir(root)
			tool.SetMaxOutputBytes(budget)
			for offset := 0; offset < 32; {
				result, err := tool.Execute(map[string]any{"query": "needle", "path": "escaped.txt", "offset": offset, "limit": 500})
				if err != nil || !result.Success {
					t.Fatalf("search at %d: %+v %v", offset, result, err)
				}
				records := result.Data["matches"].([]map[string]any)
				encoded, _ := json.Marshal(records)
				if len(encoded) > budget || len(records) == 0 {
					t.Fatalf("encoded bytes=%d records=%d budget=%d", len(encoded), len(records), budget)
				}
				for i, record := range records {
					if record["line"] != offset+i+1 {
						t.Fatalf("record skipped at offset %d: %#v", offset, record)
					}
				}
				offset += len(records)
				if next, ok := result.Data["next_offset"]; (offset < 32) != ok || (ok && next != offset) {
					t.Fatalf("pagination did not advance: next=%v offset=%d", next, offset)
				}
			}
		})
	}
}

func TestParseSearchLine_PreservesRecordSeparatorsInText(t *testing.T) {
	for _, tc := range []struct {
		line    string
		context bool
	}{
		{"a.go\x1e2\x1e1\x1evalue\x1fkeep\x1ekeep", false},
		{"a.go\x1f2\x1fvalue\x1ekeep\x1fkeep", true},
		{"a.go\x002:value\x1fkeep\x1ekeep", false},
	} {
		record, ok := parseSearchLine([]byte(tc.line))
		if !ok || string(record.path) != "a.go" || record.line != 2 || record.isContext != tc.context || !strings.Contains(string(record.text), "keep") {
			t.Fatalf("record metadata confused with content: %+v valid=%v", record, ok)
		}
	}
}
