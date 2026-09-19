package builtin

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSearchTextToolGrepFallbackExtendedRegex(t *testing.T) {
	grep, err := exec.LookPath("grep")
	if err != nil {
		t.Skip("grep is required for fallback coverage")
	}
	bin := t.TempDir()
	if err := os.Symlink(grep, filepath.Join(bin, "grep")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "sample.txt"), []byte("alpha one\nbeta two\nalphaaab three\nfour alpha beta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &SearchTextTool{}
	tool.SetWorkDir(root)
	for _, tc := range []struct {
		name, query string
		lines       []int
		texts       []string
	}{
		{"alternation", "alpha|beta", []int{1, 2, 3, 4}, []string{"alpha one", "beta two", "alphaaab three", "four alpha beta"}},
		{"grouping", "(alpha|beta) (one|two)", []int{1, 2}, []string{"alpha one", "beta two"}},
		{"quantifier", "alpha+b", []int{3}, []string{"alphaaab three"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tool.Execute(map[string]any{"query": tc.query, "path": "."})
			if err != nil {
				t.Fatal(err)
			}
			if !result.Success {
				t.Fatalf("search failed: %s", result.Error)
			}
			if result.Data["tool"] != "grep" || result.Data["count"] != len(tc.lines) {
				t.Fatalf("expected grep with %d matches, got %#v", len(tc.lines), result.Data)
			}
			matches := result.Data["matches"].([]map[string]any)
			if len(matches) != len(tc.lines) {
				t.Fatalf("records = %d, want %d", len(matches), len(tc.lines))
			}
			for i, m := range matches {
				if m["line"] != tc.lines[i] || m["match"] != tc.texts[i] {
					t.Errorf("match %d = %#v, want line %d, text %q", i, m, tc.lines[i], tc.texts[i])
				}
			}
		})
	}
}
