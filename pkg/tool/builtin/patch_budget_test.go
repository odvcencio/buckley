package builtin_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestPatchFileTool_FileSizeBudget(t *testing.T) {
	requireGNUPatch(t)
	patch := func(old, new string) string { return "--- f\n+++ f\n@@ -1 +1 @@\n-" + old + "\n+" + new + "\n" }
	noLF := "--- f\n+++ f\n@@ -1 +1 @@\n-old\n\\ No newline at end of file\n+new\n\\ No newline at end of file\n"
	for _, tc := range []struct {
		name  string
		files map[string]string
		patch string
		limit int64
		want  map[string]string
	}{
		{"input-too-large", map[string]string{"f": "old\n"}, patch("old", "new"), 2, nil},
		{"growth-too-large", map[string]string{"f": "old\n"}, patch("old", "grown"), 4, nil},
		{"exact-limit", map[string]string{"f": "old\n"}, patch("old", "grown"), 6, map[string]string{"f": "grown\n"}},
		{"create-too-large", map[string]string{}, "--- /dev/null\n+++ f\n@@ -0,0 +1 @@\n+long\n", 4, nil},
		{"create-exact", map[string]string{}, "--- /dev/null\n+++ f\n@@ -0,0 +1 @@\n+new\n", 4, map[string]string{"f": "new\n"}},
		{"delete-oversized-input", map[string]string{"f": "old\n"}, "--- f\n+++ /dev/null\n@@ -1 +0,0 @@\n-old\n", 3, nil},
		{"delete-exact", map[string]string{"f": "old\n"}, "--- f\n+++ /dev/null\n@@ -1 +0,0 @@\n-old\n", 4, map[string]string{}},
		{"no-final-newlines", map[string]string{"f": "old"}, noLF, 3, map[string]string{"f": "new"}},
		{"adds-final-newline", map[string]string{"f": "old"}, strings.TrimSuffix(noLF, "\\ No newline at end of file\n"), 3, nil},
		{"unterminated-context-append-too-large", map[string]string{"f": "old"}, "--- f\n+++ f\n@@ -1 +1,2 @@\n old\n\\ No newline at end of file\n+new\n", 7, nil},
		{"unterminated-context-append-exact", map[string]string{"f": "old"}, "--- f\n+++ f\n@@ -1 +1,2 @@\n old\n\\ No newline at end of file\n+new\n", 8, map[string]string{"f": "old\nnew\n"}},
		{"unterminated-context-fuzz-append", map[string]string{"f": "old"}, "--- f\n+++ f\n@@ -1 +1,2 @@\n old\n+new\n", 7, nil},
		{"unterminated-zero-context-append", map[string]string{"f": "old"}, "--- f\n+++ f\n@@ -1,0 +2 @@\n+new\n", 7, nil},
		{"unterminated-prepend-headroom", map[string]string{"f": "old"}, "--- f\n+++ f\n@@ -1 +1,2 @@\n+new\n old\n\\ No newline at end of file\n", 8, map[string]string{"f": "new\nold"}},

		{"unterminated-added-line-before-retained-input", map[string]string{"f": "old\ntail\n"}, "--- f\n+++ f\n@@ -1 +1 @@\n-old\n+newLONG\n\\ No newline at end of file\n", 12, nil},
		{"unterminated-added-line-separator-exact", map[string]string{"f": "old\ntail\n"}, "--- f\n+++ f\n@@ -1 +1 @@\n-old\n+newLONG\n\\ No newline at end of file\n", 13, map[string]string{"f": "newLONG\ntail\n"}},

		{"unicode-bytes", map[string]string{"f": "a\n"}, patch("a", "界"), 3, nil},
		{"crlf-patch", map[string]string{"f": "old\n"}, strings.ReplaceAll(patch("old", "new"), "\n", "\r\n"), 4, map[string]string{"f": "new\n"}},
		{"crlf-content-growth", map[string]string{"f": "old\r\n"}, "--- f\n+++ f\n@@ -1 +1,2 @@\n-old\r\n+a\r\n+b\r\n", 5, nil},
		{"crlf-content-exact", map[string]string{"f": "old\r\n"}, "--- f\n+++ f\n@@ -1 +1,2 @@\n-old\r\n+a\r\n+b\r\n", 6, map[string]string{"f": "a\r\nb\r\n"}},
		{"later-file-too-large", map[string]string{"f": "a\n", "g": "b\n"}, patch("a", "aa") + "--- g\n+++ g\n@@ -1 +1 @@\n-b\n+bbbb\n", 4, nil},
		{"repeated-file-cumulative", map[string]string{"f": "a\nb\n"}, "--- f\n+++ f\n@@ -1 +1 @@\n-a\n+aa\n--- f\n+++ f\n@@ -2 +2 @@\n-b\n+bb\n", 5, nil},
		{"repeated-file-exact", map[string]string{"f": "a\nb\n"}, "--- f\n+++ f\n@@ -1 +1 @@\n-a\n+aa\n--- f\n+++ f\n@@ -2 +2 @@\n-b\n+bb\n", 6, map[string]string{"f": "aa\nbb\n"}},
		{"octal-quoted-path", map[string]string{"f": "old\n"}, "--- \"\\146\"\n+++ \"\\146\"\n@@ -1 +1 @@\n-old\n+new\n", 4, map[string]string{"f": "new\n"}},

		{"quoted-path", map[string]string{"a b": "old\n"}, "--- \"a b\"\n+++ \"a b\"\n@@ -1 +1 @@\n-old\n+new\n", 4, map[string]string{"a b": "new\n"}},
		{"choose-shorter-existing-name", map[string]string{"long-name.txt": "unused!\n", "f": "old\n"}, "--- long-name.txt\n+++ f\n@@ -1 +1 @@\n-old\n+new\n", 4, map[string]string{"long-name.txt": "unused!\n", "f": "new\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("POSIXLY_CORRECT", "")
			if err := os.Unsetenv("POSIXLY_CORRECT"); err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			for name, content := range tc.files {
				if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			tool := &builtin.PatchFileTool{}
			tool.SetWorkDir(root)
			tool.SetMaxFileSizeBytes(tc.limit)
			result, err := tool.Execute(map[string]any{"patch": tc.patch, "strip": 0})
			if err != nil {
				t.Fatal(err)
			}
			want := tc.want
			if want == nil {
				if result.Success || !strings.Contains(result.Error, "too large") {
					t.Fatalf("expected budget rejection: success=%v error=%s", result.Success, result.Error)
				}
				want = tc.files
			} else if !result.Success {
				t.Fatalf("valid patch rejected: %s", result.Error)
			}
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != len(want) {
				t.Fatalf("patch created/deleted unexpected files: %v want=%v", entries, want)
			}
			for name, content := range want {
				got, err := os.ReadFile(filepath.Join(root, name))
				if err != nil || string(got) != content {
					t.Fatalf("%s=%q err=%v want=%q", name, got, err, content)
				}
			}
		})
	}
}

func TestPatchFileTool_PosixFileSizeBudget(t *testing.T) {
	requireGNUPatch(t)
	for _, tc := range []struct {
		name  string
		files map[string]string
		patch string
		want  string
	}{
		{"first-existing-header", map[string]string{"long-name.txt": "old\n", "f": "larger!\n"}, "--- long-name.txt\n+++ f\n@@ -1 +1 @@\n-old\n+new\n", "long-name.txt"},
		{"index-fallback", map[string]string{"f": "old\n"}, "Index: f\n--- absent-old\n+++ absent-new\n@@ -1 +1 @@\n-old\n+new\n", "f"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for name, contents := range tc.files {
				if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			tool := &builtin.PatchFileTool{}
			tool.SetWorkDir(root)
			tool.SetMaxFileSizeBytes(4)
			tool.SetEnv(map[string]string{"POSIXLY_CORRECT": ""})
			result, err := tool.Execute(map[string]any{"patch": tc.patch})
			if err != nil || !result.Success {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			for name, contents := range tc.files {
				if name == tc.want {
					contents = "new\n"
				}
				got, err := os.ReadFile(filepath.Join(root, name))
				if err != nil || string(got) != contents {
					t.Fatalf("%s=%q err=%v want=%q", name, got, err, contents)
				}
			}
		})
	}
}

func TestPatchFileTool_BudgetRejectsExtraPatchCommands(t *testing.T) {
	requireGNUPatch(t)
	for _, extra := range []string{
		"Index: f\n1c1\n< old\n---\n> grown\n",
		"diff --git a/f b/f\n@@ -1 +1 @@\n-new\n+grown\n",
		"diff --git a/f b/f\nold mode 100644\nnew mode 120000\n",
	} {
		t.Run(extra, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "f")
			if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			tool := &builtin.PatchFileTool{}
			tool.SetWorkDir(root)
			tool.SetMaxFileSizeBytes(4)
			result, err := tool.Execute(map[string]any{"patch": "--- f\n+++ f\n@@ -1 +1 @@\n-old\n+new\n" + extra})
			if err != nil || result.Success {
				t.Fatalf("extra commands accepted: %+v %v", result, err)
			}
			if got, err := os.ReadFile(path); err != nil || string(got) != "old\n" {
				t.Fatalf("rejection changed file: %q %v", got, err)
			}
		})
	}
}

func FuzzPatchFileTool_FileSizeBudget(f *testing.F) {
	for _, seed := range []struct {
		old, new string
		flags    byte
	}{
		{"old\n", "new\n", 0}, {"old", "new", 0}, {"a\r\n", "a\r\nb\r\n", 0},
		{"a\n", "a\nb\n", 1}, {"a\r\n", "b\r\n", 1}, {"a\n", "", 0}, {"", "a\n", 0},
		{"old\n", "newLONG", 2}, {"old", "new\n", 4},
	} {
		f.Add(seed.old, seed.new, seed.flags)
	}
	f.Fuzz(func(t *testing.T, old, new string, flags byte) {
		if len(old) > 128 || len(new) > 128 || old == new || strings.ContainsRune(old+new, '\x00') {
			t.Skip()
		}
		requireGNUPatch(t)
		headerLF := "\n"
		if flags&1 != 0 {
			headerLF = "\r\n"
		}
		lines := func(s string) []string {
			if s == "" {
				return nil
			}
			return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
		}
		oldLines, newLines := lines(old), lines(new)
		input := old
		oldPrefix := "-"
		newCount := len(newLines)
		if flags&2 != 0 {
			input += "tail\n"
		}
		if flags&4 != 0 {
			oldPrefix = " "
			newCount += len(oldLines)
		}
		patch := fmt.Sprintf("--- f%s+++ f%s@@ -1,%d +1,%d @@%s", headerLF, headerLF, len(oldLines), newCount, headerLF)
		for _, body := range []struct {
			text   string
			lines  []string
			prefix string
		}{{old, oldLines, oldPrefix}, {new, newLines, "+"}} {
			for _, line := range body.lines {
				patch += body.prefix + line + "\n"
			}
			if body.text != "" && !strings.HasSuffix(body.text, "\n") {
				patch += "\\ No newline at end of file\n"
			}
		}
		root := t.TempDir()
		path := filepath.Join(root, "f")
		if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("patch", "-p0", "-N", "-s", "--batch", "--no-backup-if-mismatch", "-r", filepath.Join(root, "rejects"))
		cmd.Dir, cmd.Stdin = root, strings.NewReader(patch)
		if err := cmd.Run(); err != nil {
			return
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		limit := max(len(input), len(want))
		if limit == 0 {
			return
		}
		for _, cap := range []int{limit + 4, limit, limit - 1} {
			if cap == 0 {
				continue
			}
			if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
				t.Fatal(err)
			}
			tool := &builtin.PatchFileTool{}
			tool.SetWorkDir(root)
			tool.SetMaxFileSizeBytes(int64(cap))
			result, err := tool.Execute(map[string]any{"patch": patch, "strip": 0})
			if err != nil {
				t.Fatal(err)
			}
			expectSuccess := cap >= limit
			// Partial hunks can need separator headroom when GNU uses fuzz.
			if cap == limit && flags&6 != 0 && !result.Success && strings.Contains(result.Error, "too large") {
				expectSuccess = false
			}
			if result.Success != expectSuccess {
				t.Fatalf("cap=%d old=%q oracle=%q patch=%q result=%+v", cap, old, want, patch, result)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			expected := input
			if expectSuccess {
				expected = string(want)
			}
			if string(got) != expected {
				t.Fatalf("cap=%d got=%q want=%q", cap, got, expected)
			}
		}
	})
}

func TestPatchFileTool_RejectsUnsupportedQuotedEscape(t *testing.T) {
	requireGNUPatch(t)
	for _, header := range []string{`"\x66"`, `"\u0066"`} {
		t.Run(header, func(t *testing.T) {
			root := t.TempDir()
			files := map[string]string{"f": "old\n", "fallback": "old\nextra\n"}
			for name, content := range files {
				if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			tool := &builtin.PatchFileTool{}
			tool.SetWorkDir(root)
			tool.SetMaxFileSizeBytes(4)
			patch := "Index: fallback\n--- " + header + "\n+++ " + header + "\n@@ -1 +1 @@\n-old\n+new\n"
			result, err := tool.Execute(map[string]any{"patch": patch})
			if err != nil || result.Success {
				t.Fatalf("unsupported escape accepted: %+v %v", result, err)
			}
			for name, content := range files {
				got, err := os.ReadFile(filepath.Join(root, name))
				if err != nil || string(got) != content {
					t.Fatalf("rejected patch changed %s: %q %v", name, got, err)
				}
			}
		})
	}
}
