package diffsignal

import (
	"fmt"
	"strings"
	"testing"
)

// TestPrioritizeForPR_RanksSourceBeforeDotDirsAndDocs reproduces Important-3:
// plain Prioritize keeps git's alphabetical emission order, so a dotdir
// (.github/), a repo-root doc (CHANGELOG.md), and a build file (Makefile)
// consume a small PR budget before any hand-written source file is even
// considered, because '.' and 'C'/'M' sort ahead of 'p' (pkg/...).
// PrioritizeForPR must rank source first, tests next, docs/config after,
// and dotdirs/scratch last, regardless of emission order.
func TestPrioritizeForPR_RanksSourceBeforeDotDirsAndDocs(t *testing.T) {
	dotdir := sourceFileDiff(".github/workflows/ci.yml", "ci tweak")
	changelog := sourceFileDiff("CHANGELOG.md", "changelog entry")
	makefile := sourceFileDiff("Makefile", "makefile tweak")
	source := sourceFileDiff("pkg/server/handler.go", "the actual hand-written fix")
	test := sourceFileDiff("pkg/server/handler_test.go", "the matching test change")

	// Alphabetical emission order, as git actually produces it.
	raw := dotdir + changelog + makefile + source + test

	// Budget computed to hold exactly the two ranked-first files (source,
	// test) at full fidelity, plus one summary line per demoted file — no
	// slack for a third file to sneak in at full content, and no shortfall
	// that would collapse the three summaries into one "N more" line.
	summaryCost := func(seg string) int {
		fd := Split(seg)[0]
		fd.Reason = ReasonOverBudget
		return len(summaryLine(fd)) + 1
	}
	budget := len(source) + len(test) + len(summaryHeader) + 2 +
		summaryCost(dotdir) + summaryCost(changelog) + summaryCost(makefile)
	res := PrioritizeForPR(strings.TrimSpace(raw), budget)

	if !strings.Contains(res.Context, "the actual hand-written fix") {
		t.Errorf("PrioritizeForPR must keep the hand-written source change at full fidelity even though it sorts alphabetically last:\n%s", res.Context)
	}
	if !strings.Contains(res.Context, "pkg/server/handler.go") {
		t.Errorf("source file path missing from output:\n%s", res.Context)
	}

	for _, demoted := range []string{".github/workflows/ci.yml", "CHANGELOG.md", "Makefile"} {
		if !strings.Contains(res.Context, demoted) {
			t.Errorf("demoted file %q must still appear as a summary line:\n%s", demoted, res.Context)
		}
	}
	if strings.Contains(res.Context, "ci tweak") {
		t.Errorf("dotdir content should have been demoted to a summary line, not kept at full fidelity:\n%s", res.Context)
	}
	if strings.Contains(res.Context, "changelog entry") {
		t.Errorf("docs content should have been demoted to a summary line, not kept at full fidelity:\n%s", res.Context)
	}
}

// TestPrioritizeForPR_SpreadsBudgetAcrossFiles reproduces the "a few files
// eat the whole budget" half of Important-3: Prioritize's MaxFileDiffBytes
// per-file cap (64_000) lets the first same-sized file consume nearly all of
// a PR-scale budget, demoting every later file in the same category to a
// bare summary line with zero hunks. PrioritizeForPR's smaller PRFileDiffCap
// should let several files keep representative (if truncated) hunks
// instead.
func TestPrioritizeForPR_SpreadsBudgetAcrossFiles(t *testing.T) {
	var raw strings.Builder
	var paths []string
	for i := 0; i < 5; i++ {
		path := fmt.Sprintf("pkg/mod%d/file.go", i)
		paths = append(paths, path)
		raw.WriteString(largeSourceDiff(path, 30_000))
	}

	const budget = 40_000

	plain := Prioritize(strings.TrimSpace(raw.String()), budget)
	ranked := PrioritizeForPR(strings.TrimSpace(raw.String()), budget)

	countWithContent := func(ctx string) int {
		n := 0
		for _, p := range paths {
			// A demoted (summary-only) file's only mention of its path is the
			// one-line "[reason: content omitted]" summary; a file that kept
			// hunks additionally has its "GeneratedHelper" body lines nearby.
			if strings.Contains(ctx, p) && !strings.Contains(ctx, p+" | ") {
				n++
			}
		}
		return n
	}

	plainCount := countWithContent(plain.Context)
	rankedCount := countWithContent(ranked.Context)

	if rankedCount <= plainCount {
		t.Errorf("PrioritizeForPR must spread the budget across more files than plain Prioritize: plain=%d ranked=%d\nplain:\n%.500s\nranked:\n%.500s",
			plainCount, rankedCount, plain.Context, ranked.Context)
	}
	if rankedCount < 2 {
		t.Errorf("PrioritizeForPR kept only %d file(s) with content, want at least 2 spread across the %d-byte budget", rankedCount, budget)
	}
}
