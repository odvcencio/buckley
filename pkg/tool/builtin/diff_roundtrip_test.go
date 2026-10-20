package builtin_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestEditFileTool_GeneratedDiffRoundTrips(t *testing.T) {
	requireGNUPatch(t)
	for _, tc := range []struct{ name, old, next string }{
		{"lf", "old\n", "new\n"},
		{"no-final-lf", "old", "new"},
		{"remove-final-lf", "old\n", "new"},
		{"add-final-lf", "old", "new\n"},
		{"crlf", "old\r\n", "new\r\n"},
		{"unterminated-context", "old\nlast", "new\nlast"},
		{"truncate", "old\n", ""},
		{"truncate-unterminated", "old", ""},
		{"multiple-hunks", "old\n" + strings.Repeat("same\n", 12) + "tail", "new\n" + strings.Repeat("same\n", 12) + "TAIL"},
	} {
		for _, limit := range []int64{0, 4096} {
			t.Run(fmt.Sprintf("%s/limit-%d", tc.name, limit), func(t *testing.T) {
				root := t.TempDir()
				path := filepath.Join(root, "f.txt")
				if err := os.WriteFile(path, []byte(tc.old), 0o600); err != nil {
					t.Fatal(err)
				}
				edit := &builtin.EditFileTool{ShowDiffPreview: true}
				edit.SetWorkDir(root)
				result, err := edit.Execute(map[string]any{"path": "f.txt", "old_string": tc.old, "new_string": tc.next})
				if err != nil || !result.Success {
					t.Fatalf("preview: %+v %v", result, err)
				}
				if result.DiffPreview.OldContent != tc.old || result.DiffPreview.NewContent != tc.next {
					t.Fatal("preview lost source evidence")
				}
				// Preview headers use absolute display paths; normalize only the
				// two headers to apply inside the tool's existing workspace fence.
				patch := strings.Replace(result.DiffPreview.UnifiedDiff, "--- "+path+"\n", "--- f.txt\n", 1)
				patch = strings.Replace(patch, "+++ "+path+"\n", "+++ f.txt\n", 1)
				assertGeneratedPatchRoundTrip(t, root, patch, tc.next, limit)
			})
		}
	}
}

func assertGeneratedPatchRoundTrip(t testing.TB, root, patch, want string, limit int64) {
	t.Helper()
	apply := &builtin.PatchFileTool{}
	apply.SetWorkDir(root)
	apply.SetMaxFileSizeBytes(limit)
	applied, err := apply.Execute(map[string]any{"patch": patch, "strip": 0})
	if err != nil || !applied.Success {
		t.Fatalf("apply generated diff: %+v %v\n%s", applied, err, patch)
	}
	got, err := os.ReadFile(filepath.Join(root, "f.txt"))
	if err != nil || !bytes.Equal(got, []byte(want)) {
		t.Fatalf("round trip got=%q want=%q err=%v", got, want, err)
	}
}

func TestGenerateUnifiedDiff_EmptyFiles(t *testing.T) {
	requireGNUPatch(t)
	for _, tc := range []struct {
		name, old, next string
		missing         bool
	}{
		{"create-lf", "", "new\n", true},
		{"create-no-lf", "", "new", true},
		{"fill-empty-file", "", "new", false},
		{"truncate-lf", "old\n", "", false},
		{"truncate-no-lf", "old", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if !tc.missing {
				if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte(tc.old), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			assertGeneratedPatchRoundTrip(t, root, builtin.GenerateUnifiedDiff("f.txt", tc.old, tc.next), tc.next, 4096)
		})
	}
}

func TestGenerateUnifiedDiff_StandardLineTermination(t *testing.T) {
	for _, tc := range []struct{ name, old, next, body string }{
		{"lf", "old\n", "new\n", "@@ -1 +1 @@\n-old\n+new\n"},
		{"no-lf", "old", "new", "@@ -1 +1 @@\n-old\n\\ No newline at end of file\n+new\n\\ No newline at end of file\n"},
		{"create", "", "new", "@@ -0,0 +1 @@\n+new\n\\ No newline at end of file\n"},
		{"truncate", "old", "", "@@ -1 +0,0 @@\n-old\n\\ No newline at end of file\n"},
		{"context", "old\nlast", "new\nlast", "@@ -1,2 +1,2 @@\n-old\n+new\n last\n\\ No newline at end of file\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := "--- f.txt\n+++ f.txt\n" + tc.body
			if got := builtin.GenerateUnifiedDiff("f.txt", tc.old, tc.next); got != want {
				t.Fatalf("got=%q want=%q", got, want)
			}
		})
	}
	for _, content := range []string{"", "same", "same\n"} {
		if got := builtin.GenerateUnifiedDiff("f.txt", content, content); got != "" {
			t.Fatalf("unchanged diff=%q", got)
		}
	}
}

func FuzzGenerateUnifiedDiff_RoundTrip(f *testing.F) {
	requireGNUPatch(f)
	for _, pair := range [][2]string{{"", "new"}, {"old", "new\n"}, {"old\n", "new"}, {"old\nlast", "new\nlast"}, {"\x00old\n", "\x00new"}, {"a\r\n", "b\r\n"}} {
		f.Add(pair[0], pair[1])
	}
	f.Fuzz(func(t *testing.T, old, next string) {
		if len(old)+len(next) > 256 {
			t.Skip()
		}
		patch := builtin.GenerateUnifiedDiff("f.txt", old, next)
		if old == next {
			if patch != "" {
				t.Fatalf("unchanged diff=%q", patch)
			}
			return
		}
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte(old), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("patch", "--fuzz=0", "--batch", "-N", "-s", "-p0", "--no-backup-if-mismatch", "-r", filepath.Join(root, "rejects"))
		cmd.Dir, cmd.Stdin = root, strings.NewReader(patch)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("strict GNU patch failed: %v %s\nold=%q next=%q\n%s", err, output, old, next, patch)
		}
		got, err := os.ReadFile(filepath.Join(root, "f.txt"))
		if err != nil || !bytes.Equal(got, []byte(next)) {
			t.Fatalf("strict round trip got=%q want=%q err=%v", got, next, err)
		}
	})
}
