package commands

import (
	"fmt"
	"strings"

	"m31labs.dev/buckley/pkg/diffsignal"
)

// generatedFilesHeading starts the section a review ends with: a generated
// file never gets a per-file ledger entry, so this section is the record that
// it changed.
const generatedFilesHeading = "## Generated Files"

// ledgerPaths returns the changed paths a review must account for one by one:
// every changed path except the generated ones.
func ledgerPaths(changed, generated []string) []string {
	if len(generated) == 0 {
		return changed
	}
	skip := make(map[string]struct{}, len(generated))
	for _, p := range generated {
		skip[normalizeCoveragePath(p)] = struct{}{}
	}
	out := make([]string, 0, len(changed))
	for _, p := range changed {
		if _, ok := skip[normalizeCoveragePath(p)]; !ok {
			out = append(out, p)
		}
	}
	return out
}

// mergeGeneratedFiles adds the generated files of one diff to a running set,
// keeping the first occurrence of each path, and returns the updated set.
func mergeGeneratedFiles(have []diffsignal.FileDiff, raw string) []diffsignal.FileDiff {
	seen := make(map[string]struct{}, len(have))
	for _, f := range have {
		seen[f.Path] = struct{}{}
	}
	for _, f := range diffsignal.ClassifyDiff(raw) {
		if !f.Generated() {
			continue
		}
		if _, ok := seen[f.Path]; ok {
			continue
		}
		seen[f.Path] = struct{}{}
		have = append(have, f)
	}
	return have
}

// generatedPromptSection tells the model which files are collapsed and that
// the Coverage ledger must not list them. It returns "" without any.
func generatedPromptSection(groups []diffsignal.GeneratedGroup) string {
	if len(groups) == 0 {
		return ""
	}
	files := 0
	for _, g := range groups {
		files += len(g.Files)
	}
	var sb strings.Builder
	sb.WriteString(generatedFilesHeading + "\n\n")
	fmt.Fprintf(&sb, "%d generated files changed, collapsed into %d groups. ", files, len(groups))
	sb.WriteString("They are excluded from per-file review and from the Coverage ledger: do not list them as **File** entries. ")
	sb.WriteString("The host records them and any receipt that covers them after your review.\n\n```\n")
	for _, g := range groups {
		sb.WriteString(g.Line())
		sb.WriteString("\n")
	}
	sb.WriteString("```\n\n")
	return sb.String()
}

// PathCoverage counts how verification evidence treats a set of paths.
type PathCoverage struct {
	Total       int
	Covered     int
	Failing     int
	Unverified  int
	NotRequired int
}

// PathCoverage reports how many of paths a receipt or CI check covers. A nil
// report covers nothing and counts every path as unverified.
func (r *VerificationReport) PathCoverage(paths []string) PathCoverage {
	cov := PathCoverage{Total: len(paths)}
	if r == nil {
		cov.Unverified = len(paths)
		return cov
	}
	set := func(items []EvidenceItem) map[string]bool {
		m := map[string]bool{}
		for _, item := range items {
			for _, p := range item.Paths {
				m[p] = true
			}
		}
		return m
	}
	covered, failing := set(r.Covered), set(r.Failing)
	notRequired := map[string]bool{}
	for _, p := range r.NotRequired {
		notRequired[p] = true
	}
	for _, p := range paths {
		p = normalizeReviewEvidencePath(p)
		switch {
		case failing[p]:
			cov.Failing++
		case covered[p]:
			cov.Covered++
		case notRequired[p]:
			cov.NotRequired++
		default:
			cov.Unverified++
		}
	}
	return cov
}

func (c PathCoverage) String() string {
	switch {
	case c.Failing > 0:
		return fmt.Sprintf("receipt failing for %d of %d", c.Failing, c.Total)
	case c.Covered == c.Total:
		return fmt.Sprintf("covered by a receipt (%d of %d)", c.Covered, c.Total)
	case c.NotRequired == c.Total:
		return "no receipt required"
	case c.Covered == 0:
		return "not covered by any receipt"
	default:
		return fmt.Sprintf("covered by a receipt for %d of %d", c.Covered, c.Total)
	}
}

// GeneratedSummary renders the closing section of a review for collapsed
// generated files: each group's pattern, file count and bytes changed, and
// whether a receipt covers it. It returns "" without any generated files.
func GeneratedSummary(groups []diffsignal.GeneratedGroup, report *VerificationReport) string {
	if len(groups) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(generatedFilesHeading + " (not reviewed per file)\n\n")
	for _, g := range groups {
		files := "files"
		if len(g.Files) == 1 {
			files = "file"
		}
		fmt.Fprintf(&sb, "- `%s`: %d %s, %s changed; %s\n",
			g.Pattern, len(g.Files), files, diffsignal.FormatBytes(g.Bytes), report.PathCoverage(g.Files))
	}
	return sb.String()
}

// AppendGeneratedSummary adds the generated-files section to a rendered
// review once.
func AppendGeneratedSummary(review string, groups []diffsignal.GeneratedGroup, report *VerificationReport) string {
	section := GeneratedSummary(groups, report)
	if section == "" || strings.Contains(review, generatedFilesHeading) {
		return review
	}
	return strings.TrimRight(review, "\n") + "\n\n" + section
}
