package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"m31labs.dev/buckley/pkg/reviewvalidate"
)

// reviewGroundLineTolerance absorbs the few-line drift real models exhibit when
// citing locations (calibrated against Kimi K3 review output, where lines drift
// by a handful but files/claims are accurate).
const reviewGroundLineTolerance = 12

// groundReviewFindings deterministically checks every file:line claim in the
// review against the actual repository and appends a grounding report to the
// output. It is the model-agnostic "positioning" layer that makes review output
// from ANY model auditable: a fabricated file, an out-of-range line, or a symbol
// that appears nowhere in the cited file gets flagged, with no extra model call.
// Returns a one-line terminal summary (empty when there is nothing to ground).
func groundReviewFindings(result *reviewCommandResult) string {
	if result == nil || strings.TrimSpace(result.reviewText) == "" {
		return ""
	}
	root, err := os.Getwd()
	if err != nil {
		return ""
	}
	src, err := reviewvalidate.NewRepoFileSource(root)
	if err != nil {
		return ""
	}
	sum := reviewvalidate.GroundReview(result.reviewText, src, reviewGroundLineTolerance)
	if sum.TotalRefs == 0 {
		return ""
	}
	result.reviewText += renderGroundingSection(sum)
	return "Finding grounding: " + sum.String()
}

func renderGroundingSection(sum reviewvalidate.Summary) string {
	var b strings.Builder
	b.WriteString("\n\n---\n## Finding Grounding (deterministic)\n\n")
	fmt.Fprintf(&b, "`%s`\n\n", sum.String())
	fmt.Fprintf(&b, "%.0f%% of located `file:line` claims point at code that exists. ", sum.GroundRatio()*100)

	if sum.SuspectCount() == 0 {
		b.WriteString("No fabricated locations detected.\n")
	} else {
		fmt.Fprintf(&b, "**%d reference(s) look fabricated — verify or drop:**\n\n", sum.SuspectCount())
		seen := map[string]bool{}
		var lines []string
		for _, v := range sum.Verdicts {
			switch v.Status {
			case reviewvalidate.StatusFileMissing, reviewvalidate.StatusLineOutOfRange, reviewvalidate.StatusUngrounded:
				key := string(v.Status) + v.Path + fmt.Sprint(v.Line)
				if seen[key] {
					continue
				}
				seen[key] = true
				lines = append(lines, fmt.Sprintf("- **%s** — `%s:%d`", v.Status, v.Path, v.Line))
			}
		}
		sort.Strings(lines)
		b.WriteString(strings.Join(lines, "\n"))
		b.WriteString("\n")
	}
	b.WriteString("\n_Positioning check only: it confirms cited code exists, not that the claim's logic is correct — a semantically inverted claim about real code can still pass. Pair with a semantic critic for full trust._\n")
	return b.String()
}
