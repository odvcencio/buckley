package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileMutation_BudgetFailureLeavesFileUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		limit       int64
		tool        interface {
			Execute(map[string]any) (*Result, error)
			SetMaxFileSizeBytes(int64)
			SetWorkDir(string)
		}
		params map[string]any
	}{
		{"edit-expansion", "aaaa", 4, &EditFileTool{}, map[string]any{"old_string": "a", "new_string": "bb", "replace_all": true}},
		{"edit-preview", "aaaa", 4, &EditFileTool{ShowDiffPreview: true}, map[string]any{"old_string": "a", "new_string": "bb", "replace_all": true}},
		{"edit-batch-intermediate", "aaaa", 4, &EditFileTool{}, map[string]any{"edits": []any{map[string]any{"old_string": "aaaa", "new_string": "bbbbbbbb"}, map[string]any{"old_string": "bbbbbbbb", "new_string": "c"}}}},
		{"edit-unicode-insertion", "界", 5, &EditFileTool{}, map[string]any{"old_string": "", "new_string": "界", "replace_all": true}},
		{"insert-expansion", "abc", 4, &InsertTextTool{}, map[string]any{"line": 1, "text": "xx"}},
		{"search-oversized-input", "aaaa", 3, &SearchReplaceTool{}, map[string]any{"search": "a", "replace": "b"}},
		{"search-expansion", "aaaa", 4, &SearchReplaceTool{}, map[string]any{"search": "a", "replace": "bb"}},
		{"search-regex-expansion", "aaaa", 4, &SearchReplaceTool{}, map[string]any{"search": "(a)", "replace": "$1$1", "use_regex": true}},
		{"search-limited-regex-expansion", "aaaa", 4, &SearchReplaceTool{}, map[string]any{"search": "(a)", "replace": "$1$1$1$1$1", "use_regex": true, "max_replacements": 1}},
		{"search-limited-expansion", "aaaa", 4, &SearchReplaceTool{}, map[string]any{"search": "a", "replace": "bb", "max_replacements": 1}},
		{"write-output", "aaaa", 4, &WriteFileTool{}, map[string]any{"content": "bbbbb"}},
		{"delete-oversized-input", "a\nb\nc", 4, &DeleteLinesTool{}, map[string]any{"start_line": 1, "end_line": 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "fixture.txt")
			if err := os.WriteFile(path, []byte(tc.input), 0o640); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			tc.tool.SetWorkDir(root)
			tc.tool.SetMaxFileSizeBytes(tc.limit)
			tc.params["path"] = "fixture.txt"
			result, err := tc.tool.Execute(tc.params)
			if err != nil || result.Success || !strings.Contains(result.Error, "too large") {
				t.Fatalf("budget rejection: result=%+v err=%v", result, err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != tc.input {
				t.Fatalf("rejected mutation changed file: %q %v", got, err)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != before.Mode().Perm() {
				t.Fatalf("rejected mutation changed permissions: %v %v", info, err)
			}
		})
	}
}

func TestFileMutation_DefaultReadAndOutputBudgets(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sparse.txt")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate((8 << 20) + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	tool := &SearchReplaceTool{}
	tool.SetWorkDir(root)
	result, err := tool.Execute(map[string]any{"path": "sparse.txt", "search": "a", "replace": "b"})
	if err != nil || result.Success || !strings.Contains(result.Error, "too large") {
		t.Fatalf("default read cap: success=%v error=%s err=%v", result.Success, result.Error, err)
	}
	tool.SetMaxFileSizeBytes((8 << 20) + 1)
	result, err = tool.Execute(map[string]any{"path": "sparse.txt", "search": "a", "replace": "b"})
	if err != nil || !result.Success || result.Data["replacements"] != 0 {
		t.Fatalf("explicit larger policy rejected: success=%v error=%s err=%v", result.Success, result.Error, err)
	}
	tool.SetMaxFileSizeBytes(0)
	if err := os.WriteFile(path, []byte(strings.Repeat("a", 8192)), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err = tool.Execute(map[string]any{"path": "sparse.txt", "search": "(a)", "replace": strings.Repeat("$1", 2048), "use_regex": true})
	if err != nil || result.Success || !strings.Contains(result.Error, "too large") {
		t.Fatalf("default expansion cap: success=%v error=%s err=%v", result.Success, result.Error, err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() != 8192 {
		t.Fatalf("expansion changed file: %v %v", info, err)
	}
	write := &WriteFileTool{}
	write.SetWorkDir(root)
	result, err = write.Execute(map[string]any{"path": "new.txt", "content": strings.Repeat("x", (8<<20)+1)})
	if err != nil || result.Success || !strings.Contains(result.Error, "too large") {
		t.Fatalf("default write cap: success=%v error=%s err=%v", result.Success, result.Error, err)
	}
	if _, err := os.Stat(filepath.Join(root, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("rejected write created a file: %v", err)
	}
}

func TestSearchReplaceTool_BudgetAllowsFinalShrinkAndExactBoundary(t *testing.T) {
	for _, input := range []string{"baaaa", "aaaab"} {
		t.Run(input, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "fixture.txt")
			if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
				t.Fatal(err)
			}
			tool := &SearchReplaceTool{}
			tool.SetWorkDir(root)
			tool.SetMaxFileSizeBytes(5)
			result, err := tool.Execute(map[string]any{"path": "fixture.txt", "search": "a{4}|b", "replace": "zz", "use_regex": true})
			if err != nil || !result.Success {
				t.Fatalf("valid shrink rejected: %+v %v", result, err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != "zzzz" {
				t.Fatalf("wrong replacement: %q %v", got, err)
			}
		})
	}
	root := t.TempDir()
	path := filepath.Join(root, "exact.txt")
	if err := os.WriteFile(path, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := &EditFileTool{}
	tool.SetWorkDir(root)
	tool.SetMaxFileSizeBytes(4)
	result, err := tool.Execute(map[string]any{"path": "exact.txt", "old_string": "a", "new_string": "bbbb"})
	if err != nil || !result.Success {
		t.Fatalf("exact boundary rejected: %+v %v", result, err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "bbbb" {
		t.Fatalf("exact boundary output: %q %v", got, err)
	}
}

func BenchmarkSearchReplaceTool_FileBudget(b *testing.B) {
	for _, tc := range []struct {
		name, replacement string
		max               int
	}{{"one-match", "b", 1}, {"all-matches", "b", 0}, {"rejected-expansion", strings.Repeat("b", 256), 0}} {
		b.Run(tc.name, func(b *testing.B) {
			root := b.TempDir()
			path := filepath.Join(root, "fixture.txt")
			input := []byte(strings.Repeat("a", 32<<10))
			tool := &SearchReplaceTool{}
			tool.SetWorkDir(root)
			tool.SetMaxFileSizeBytes(32 << 10)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				if err := os.WriteFile(path, input, 0o600); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				_, err := tool.Execute(map[string]any{"path": "fixture.txt", "search": "a", "replace": tc.replacement, "max_replacements": tc.max})
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
