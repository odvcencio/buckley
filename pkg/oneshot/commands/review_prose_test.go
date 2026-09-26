package commands

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/prompts"
)

func TestParseReview_ProseCategories(t *testing.T) {
	for _, category := range []string{"prose", "asd-ste100", "ste100", "ASD-STE100"} {
		t.Run(category, func(t *testing.T) {
			parsed := ParseReview(fmt.Sprintf(`## Findings
### FINDING-001: [MINOR] Setup names the wrong flag
- **Category**: %s
- **File**: docs/setup.md:12
- **Evidence**: The text says "Use --fast"; the command accepts --quick.
- **Impact**: Readers get an unknown-flag error.
- **Fix**: Correct the flag name.
- **Rewrite**: Run the command with --quick.
`, category))
			if len(parsed.Findings) != 1 || parsed.Findings[0].Category != "prose" {
				t.Fatalf("findings = %+v", parsed.Findings)
			}
			if err := validateDemonstratedFindings(parsed.Findings); err != nil {
				t.Fatal(err)
			}
			var stored Finding
			if err := json.Unmarshal([]byte(fmt.Sprintf(`{"Category":%q,"Title":"Historical finding"}`, category)), &stored); err != nil {
				t.Fatal(err)
			}
			if stored.Category != "prose" || stored.Title != "Historical finding" {
				t.Fatalf("ledger finding = %+v", stored)
			}
		})
	}
}

func TestValidateDemonstratedFindings_ProseRequiresEvidenceImpactAndRewrite(t *testing.T) {
	for _, missing := range []string{"evidence", "impact", "rewrite"} {
		t.Run(missing, func(t *testing.T) {
			finding := Finding{ID: "FINDING-001", Category: "prose", Evidence: "The flag is documented as --fast but declared as --quick.", Impact: "Readers get an unknown-flag error.", SuggestedFix: "Run with --quick."}
			switch missing {
			case "evidence":
				finding.Evidence = ""
			case "impact":
				finding.Impact = ""
			case "rewrite":
				finding.SuggestedFix = ""
			}
			if err := validateDemonstratedFindings([]Finding{finding}); err == nil {
				t.Fatal("accepted incomplete prose finding")
			}
		})
	}
}

func TestReviewDefinitions_RegisterGuidance(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, kind := range []string{"REVIEW_BRANCH", "REVIEW_PROJECT", "REVIEW_PR"} {
		t.Setenv("BUCKLEY_PROMPT_"+kind, "")
		t.Setenv("BUCKLEY_PROMPT_"+kind+"_FILE", "")
	}
	for name, prompt := range map[string]string{
		"branch":  (ReviewBranchDef{}).SystemPrompt(),
		"pr":      (ReviewPRDef{}).SystemPrompt(),
		"project": (ReviewProjectDef{}).SystemPrompt(),
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(prompt, prompts.ReviewProseBlock()) || strings.Contains(prompt, "ASD-STE100") {
				t.Fatal("review register missing or superseded standard present")
			}
		})
	}
}

func TestMergeShardedPRReview_PreservesProseRewrite(t *testing.T) {
	review := withCoverage(baseParsedReview(GradeB, false), "docs/setup.md")
	review.FalsificationConclusion = FalsificationProved
	review.Findings = []Finding{{
		ID: "FINDING-001", Category: "prose", Severity: SeverityMinor,
		Title: "Setup names the wrong flag", File: "docs/setup.md", Line: 12,
		Evidence: "The text names --fast; the command accepts --quick.",
		Impact:   "Readers get an unknown-flag error.", Fix: "Correct the flag name.",
		SuggestedFix: "Run with --quick.",
	}}
	merged, rendered := MergeShardedPRReview([]ShardReview{{ShardIndex: 0, Files: []string{"docs/setup.md"}, Review: review}}, nil, []string{"docs/setup.md"}, 8)
	if len(merged.Findings) != 1 || merged.Findings[0].Category != "prose" || merged.Findings[0].SuggestedFix != review.Findings[0].SuggestedFix {
		t.Fatalf("prose finding lost in merged review: %+v", merged.Findings)
	}
	for _, want := range []string{"**Category**: prose", "**Rewrite**: Run with --quick."} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered review dropped %q", want)
		}
	}
	if err := validateDemonstratedFindings(merged.Findings); err != nil {
		t.Fatal(err)
	}
}
