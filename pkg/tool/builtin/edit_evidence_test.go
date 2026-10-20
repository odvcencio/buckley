package builtin

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/encoding/toon"
)

func TestEditFileTool_BoundsCompletedEvidenceAndPreservesApproval(t *testing.T) {
	for _, tc := range []struct{ name, source string }{
		{"many lines", "BEFORE\n" + strings.Repeat("unchanged source\n", 65536)},
		{"long line", "BEFORE " + strings.Repeat("界", 300000) + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "source.txt")
			if err := os.WriteFile(path, []byte(tc.source), 0640); err != nil {
				t.Fatal(err)
			}
			tool := &EditFileTool{ShowDiffPreview: true}
			tool.SetWorkDir(dir)
			params := map[string]any{"path": "source.txt", "old_string": "BEFORE", "new_string": "AFTER"}
			preview, err := tool.Execute(params)
			if err != nil || !preview.Success || !preview.NeedsApproval {
				t.Fatalf("preview=%+v err=%v", preview, err)
			}
			next := strings.Replace(tc.source, "BEFORE", "AFTER", 1)
			if preview.DiffPreview.OldContent != tc.source || preview.DiffPreview.NewContent != next || preview.Data["new_content"] != next || preview.DiffPreview.DiffTruncated {
				t.Fatal("approval lost full source/diff evidence")
			}
			before, err := os.ReadFile(path)
			if err != nil || string(before) != tc.source {
				t.Fatal("approval preview changed source")
			}
			fullPatch := preview.DiffPreview.UnifiedDiff
			tool.ShowDiffPreview = false
			applied, err := tool.Execute(params)
			if err != nil || !applied.Success {
				t.Fatalf("applied=%+v err=%v", applied, err)
			}
			info := applied.DiffPreview
			if !info.SourceOmitted || info.OldContent != "" || info.NewContent != "" || info.OldBytes != len(tc.source) || info.NewBytes != len(next) || info.OldSHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(tc.source))) || info.NewSHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(next))) {
				t.Fatal("completed evidence retained source or lost source identity")
			}
			if info.LinesAdded != preview.DiffPreview.LinesAdded || info.LinesRemoved != preview.DiffPreview.LinesRemoved || len(info.Preview) > DefaultDiffPreviewBytes || !utf8.ValidString(info.Preview) {
				t.Fatalf("bad counts or preview: %+v", info)
			}
			if len(fullPatch) > DefaultAppliedDiffBytes {
				if !info.DiffTruncated || info.UnifiedDiff != "" || info.DiffBytes != len(fullPatch) || info.DiffSHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(fullPatch))) || applied.DisplayData["diff_truncated"] != true {
					t.Fatal("oversized patch exposed as complete or lost identity")
				}
			} else if info.DiffTruncated || info.UnifiedDiff != fullPatch {
				t.Fatal("small complete patch changed")
			}
			for _, useToon := range []bool{false, true} {
				wire, err := toon.New(useToon).MarshalString(applied)
				if err != nil || len(wire) > 16<<10 {
					t.Fatalf("completed result not bounded: bytes=%d err=%v", len(wire), err)
				}
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != next {
				t.Fatal("completed edit differs from proposal")
			}
		})
	}
}
