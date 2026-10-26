package commands

import (
	"strings"
	"testing"
)

func TestParseFalsificationConclusion_ExplanatoryProse(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
		want FalsificationConclusion
	}{
		{
			name: "unresolved_issue_117",
			line: "- **Conclusion**: UNRESOLVED — the documentation claims cannot be confirmed or disproved from the supplied diff alone.",
			want: FalsificationUnresolved,
		},
		{
			name: "unresolved_plain_label_and_backticks",
			line: "Conclusion: `unresolved`: the guarantee cannot be proved or disproved without the fixture.",
			want: FalsificationUnresolved,
		},
		{
			name: "proved_explanation",
			line: "- **Conclusion**: PROVED — the counterexample disproved the documented guarantee.",
			want: FalsificationProved,
		},
		{
			name: "disproved_explanation",
			line: "- **Conclusion**: DISPROVED — the focused failure was not reproduced.",
			want: FalsificationDisproved,
		},
		{
			name: "conflicting_approval_status",
			line: "- **Conclusion**: DISPROVED for one route, but UNRESOLVED for another",
		},
		{
			name: "conflicting_lowercase_approval_status",
			line: "- **Conclusion**: DISPROVED for one route, but unresolved for another",
		},
		{
			name: "duplicate_conclusion_lines",
			line: "- **Conclusion**: UNRESOLVED\n- **Conclusion**: DISPROVED",
		},
		{
			name: "invalid_status_suffix",
			line: "- **Conclusion**: UNRESOLVEDness",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseFalsificationConclusion(tc.line); got != tc.want {
				t.Fatalf("parseFalsificationConclusion(%q) = %q, want %q", tc.line, got, tc.want)
			}
		})
	}
}

func TestReviewBranchDef_ExplanatoryConclusionKeepsApprovalGate(t *testing.T) {
	def := ReviewBranchDef{ChangedFiles: []string{"ratchet.go"}}
	base := completeReviewWithCoverage("- **File**: `ratchet.go` — reviewed the exact changed file.\n" +
		"- **Feedback disposition**: `NONE_SUPPLIED` — no prior feedback was supplied.\n" +
		"- **Verification**: focused test passed.")
	for _, tc := range []struct {
		name       string
		conclusion string
		decision   string
	}{
		{
			name:       "unresolved",
			conclusion: "UNRESOLVED — the documentation claims cannot be confirmed or disproved from the supplied diff alone.",
			decision:   "NEEDS DISCUSSION",
		},
		{
			name:       "proved",
			conclusion: "PROVED — the counterexample disproved the documented guarantee.",
			decision:   "REQUEST CHANGES",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			review := strings.Replace(base, "**Conclusion**: DISPROVED.", "**Conclusion**: "+tc.conclusion, 1)
			result, err := def.ParseResult(review)
			if err != nil {
				t.Fatal(err)
			}
			if err := def.ValidateResult(result); err == nil || !strings.Contains(err.Error(), "approval requires a DISPROVED falsification conclusion") {
				t.Fatalf("non-approving conclusion must still block approval, got %v", err)
			}

			review = strings.Replace(review, "## Grade: A", "## Grade: B", 1)
			review = strings.Replace(review, "**Recommendation**: APPROVE", "**Recommendation**: "+tc.decision, 1)
			result, err = def.ParseResult(review)
			if err != nil {
				t.Fatal(err)
			}
			if err := def.ValidateResult(result); err != nil {
				t.Fatalf("explanatory conclusion triggered an unnecessary repair: %v", err)
			}
		})
	}
}
