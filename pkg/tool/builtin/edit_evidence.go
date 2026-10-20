package builtin

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	DefaultAppliedDiffBytes = 4 << 10
	DefaultDiffPreviewBytes = 2 << 10
	DefaultDiffSourceBytes  = 4 << 10
)

func compactAppliedDiff(info *DiffInfo) {
	if len(info.OldContent)+len(info.NewContent) > DefaultDiffSourceBytes {
		info.SourceOmitted = true
		info.OldBytes, info.NewBytes = len(info.OldContent), len(info.NewContent)
		info.OldSHA256 = fmt.Sprintf("%x", mutationDigest(info.OldContent))
		info.NewSHA256 = fmt.Sprintf("%x", mutationDigest(info.NewContent))
		info.OldContent, info.NewContent = "", ""
	}
	if len(info.UnifiedDiff) > DefaultAppliedDiffBytes {
		info.DiffTruncated = true
		info.DiffBytes = len(info.UnifiedDiff)
		info.DiffSHA256 = fmt.Sprintf("%x", mutationDigest(info.UnifiedDiff))
		// An incomplete patch must not be exposed as an applicable unified diff.
		info.UnifiedDiff = ""
	}
	preview := strings.ToValidUTF8(info.Preview, "�")
	if len(preview) > DefaultDiffPreviewBytes {
		marker := fmt.Sprintf("\n... (preview truncated, %d bytes before display clipping)", len(info.Preview))
		keep := DefaultDiffPreviewBytes - len(marker)
		for keep > 0 && !utf8.ValidString(preview[:keep]) {
			keep--
		}
		preview = preview[:keep] + marker
	}
	info.Preview = strings.Clone(preview)
}
