package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestGenerateDiff_PreservesEvidence(t *testing.T) {
	cases := []struct {
		name, old, next string
	}{
		{"empty", "", ""},
		{"unchanged", "same\ntext", "same\ntext"},
		{"create", "", "new\nfile\n"},
		{"delete", "old\nfile\n", ""},
		{"duplicates", "a\na\nb\n", "a\nb\nb\nb\n"},
		{"reorder", "same\nold\nnew\ntail", "same\nnew\nold\ntail"},
		{"blank-lines", "\n \n\t\n", "\n\n"},
		{"mixed-endings", "old\r\nlast", "new\nlast\n"},
		{"unicode", "界\n😀\n", "界\n🙂"},
		{"binary", "\x00\xffold\n", "\x00\xffnew\n"},
		{"large-evidence", strings.Repeat("a", 256<<10), strings.Repeat("b", 256<<10)},
	}
	for _, lines := range []int{5, 6, 7, 8, 20} {
		cases = append(cases, struct{ name, old, next string }{
			fmt.Sprintf("preview-%d-replacements", lines), strings.Repeat("old\n", lines), strings.Repeat("new\n", lines),
		})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertDiffMetadata(t, generateDiff("fixture.txt", tc.old, tc.next), "fixture.txt", tc.old, tc.next)
		})
	}
}

func TestEditFileTool_ApprovalPreviewRetainsFullEvidence(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "fixture.txt")
	var before, after strings.Builder
	for index := range 20 {
		fmt.Fprintf(&before, "old-%02d\n", index)
		fmt.Fprintf(&after, "new-%02d\n", index)
	}
	old, next := before.String(), after.String()
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := &EditFileTool{ShowDiffPreview: true}
	tool.SetWorkDir(root)
	params := map[string]any{"path": "fixture.txt", "old_string": "old-", "new_string": "new-", "replace_all": true}
	preview, err := tool.Execute(params)
	if err != nil || !preview.Success || !preview.NeedsApproval {
		t.Fatalf("preview failed: %+v %v", preview, err)
	}
	assertDiffMetadata(t, preview.DiffPreview, path, old, next)
	want := preview.DiffPreview
	if preview.Data["old_content"] != old || preview.Data["new_content"] != next {
		t.Fatal("approval preview omitted complete source or diff evidence")
	}
	if !strings.Contains(preview.DiffPreview.Preview, "... (") || !strings.Contains(preview.DiffPreview.UnifiedDiff, "+new-19") {
		t.Fatal("expected a shortened display preview and complete unified diff")
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != old {
		t.Fatalf("approval preview wrote the file: %q %v", got, err)
	}
	tool.ShowDiffPreview = false
	applied, err := tool.Execute(params)
	if err != nil || !applied.Success || !reflect.DeepEqual(applied.DiffPreview, want) {
		t.Fatalf("applied edit changed diff evidence: %+v %v", applied, err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != next {
		t.Fatalf("applied content differs: %q %v", got, err)
	}
}

func TestEditFileTool_NoChangePreservesModificationTime(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params map[string]any
	}{
		{"identical-replacement", map[string]any{"old_string": "old", "new_string": "old"}},
		{"canceling-batch", map[string]any{"edits": []any{
			map[string]any{"old_string": "old", "new_string": "new"},
			map[string]any{"old_string": "new", "new_string": "old"},
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "fixture.txt")
			if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			past := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
			if err := os.Chtimes(path, past, past); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			tool := &EditFileTool{}
			tool.SetWorkDir(root)
			tc.params["path"] = "fixture.txt"
			result, err := tool.Execute(tc.params)
			if err != nil || !result.Success {
				t.Fatalf("no-op edit failed or changed evidence: %+v %v", result, err)
			}
			assertDiffMetadata(t, result.DiffPreview, path, "old\n", "old\n")
			after, err := os.Stat(path)
			if err != nil || !after.ModTime().Equal(before.ModTime()) {
				t.Fatalf("no-op changed modification time: before=%v after=%v error=%v", before, after, err)
			}
			if got, err := os.ReadFile(path); err != nil || string(got) != "old\n" {
				t.Fatalf("no-op changed contents: %q %v", got, err)
			}
		})
	}
}

// Check source retention, line statistics, and display extraction independently.
// GNU patch round trips verify the diff body in diff_roundtrip_test.go.
func assertDiffMetadata(t testing.TB, info *DiffInfo, path, old, next string) {
	t.Helper()
	if info == nil {
		t.Fatal("missing diff evidence")
	}
	if (old == next) != (info.UnifiedDiff == "") {
		t.Fatal("diff does not reflect whether the source changed")
	}
	bags := []map[string]int{{}, {}}
	for index, content := range []string{old, next} {
		for _, line := range strings.Split(content, "\n") {
			if strings.TrimSpace(line) != "" {
				bags[index][line]++
			}
		}
	}
	added, removed := 0, 0
	for line, count := range bags[0] {
		removed += max(0, count-bags[1][line])
	}
	for line, count := range bags[1] {
		added += max(0, count-bags[0][line])
	}
	unified := info.UnifiedDiff
	preview := unified
	lines := strings.Split(unified, "\n")
	if len(lines) > 15 {
		preview = strings.Join(lines[:15], "\n") + fmt.Sprintf("\n... (%d more lines)", len(lines)-15)
	}
	got := *info
	got.UnifiedDiff = ""
	want := DiffInfo{FilePath: path, IsNew: old == "", LinesAdded: added, LinesRemoved: removed,
		OldContent: old, NewContent: next, Preview: preview}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("diff metadata differs: got=%+v want=%+v", got, want)
	}
}

func FuzzGenerateDiff_PreservesEvidence(f *testing.F) {
	for _, pair := range [][2]string{{"", ""}, {"old", "new\n"}, {"a\nb\na\n", "b\na\nb\n"}, {"\n \n", "\t\n"}, {strings.Repeat("a\n", 20), strings.Repeat("b\n", 20)}} {
		f.Add(pair[0], pair[1])
	}
	f.Fuzz(func(t *testing.T, old, next string) {
		if len(old)+len(next) > 4096 {
			t.Skip()
		}
		assertDiffMetadata(t, generateDiff("f", old, next), "f", old, next)
	})
}

var benchmarkEditDiff *DiffInfo

func BenchmarkGenerateDiff(b *testing.B) {
	var before, after strings.Builder
	for index := range 16384 {
		fmt.Fprintf(&before, "old-%06d\n", index)
		fmt.Fprintf(&after, "new-%06d\n", index)
	}
	old := before.String()
	for _, tc := range []struct{ name, next string }{
		{"unchanged", old},
		{"one-line", strings.Replace(old, "old-008192", "new-008192", 1)},
		{"all-lines", after.String()},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				benchmarkEditDiff = generateDiff("fixture.txt", old, tc.next)
			}
		})
	}
}

func BenchmarkEditFileTool_LongFile(b *testing.B) {
	var content strings.Builder
	for index := range 16384 {
		fmt.Fprintf(&content, "old-%06d\n", index)
	}
	old := []byte(content.String())
	root := b.TempDir()
	path := filepath.Join(root, "fixture.txt")
	tool := &EditFileTool{}
	tool.SetWorkDir(root)
	tool.SetMaxFileSizeBytes(int64(len(old)))
	params := map[string]any{"path": "fixture.txt", "old_string": "old-", "new_string": "new-", "replace_all": true}
	b.ReportAllocs()
	for b.Loop() {
		if err := os.WriteFile(path, old, 0o600); err != nil {
			b.Fatal(err)
		}
		result, err := tool.Execute(params)
		if err != nil || !result.Success {
			b.Fatalf("edit failed: %+v %v", result, err)
		}
		benchmarkEditDiff = result.DiffPreview
	}
}
