package commands

import (
	"strings"
	"testing"
)

func TestParseReviewStatusMarkdown(t *testing.T) {
	for _, tc := range []struct {
		name, lines, build, tests string
	}{
		{"plain", "- Build: PASS\n- Tests: UNAVAILABLE", "PASS", "UNAVAILABLE"},
		{"bold bullets", "- **Build**: PASS — named check\n- **Tests**: UNAVAILABLE — no test gate", "PASS — named check", "UNAVAILABLE — no test gate"},
		{"bold unbulleted", "**Build**: FAIL\n**Tests**: PASS", "FAIL", "PASS"},
		{"bold asterisk bullets", "* **Build**: PASS\n* **Tests**: PASS", "PASS", "PASS"},
		{"other labels", "- Rebuild: PASS\n- Unit Tests: PASS", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseReview("## CI Status\n" + tc.lines)
			if got.BuildStatus != tc.build || got.TestStatus != tc.tests {
				t.Fatalf("build/test status = %q/%q, want %q/%q", got.BuildStatus, got.TestStatus, tc.build, tc.tests)
			}
		})
	}
}

func TestParseReviewStatusRejectsDuplicateLabels(t *testing.T) {
	for _, tc := range []struct {
		name, lines string
	}{
		{"conflicting build", "- Build: FAIL\n- **Build**: PASS\n- Tests: PASS"},
		{"same build", "- Build: PASS\n- Build: PASS\n- Tests: PASS"},
		{"conflicting tests", "- Build: PASS\n- Tests: FAIL\n- **Tests**: PASS"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseReview("## CI Status\n" + tc.lines)
			if strings.Contains(tc.name, "build") && got.BuildStatus != "" || strings.Contains(tc.name, "tests") && got.TestStatus != "" {
				t.Fatalf("duplicate status was accepted: build=%q tests=%q", got.BuildStatus, got.TestStatus)
			}
		})
	}
}

func TestReviewBranchAcceptsBoldStatusLabels(t *testing.T) {
	coverage := "- **File**: `ratchet.go` — reviewed the exact changed file.\n" +
		"- **Feedback disposition**: `NONE_SUPPLIED` — no prior feedback was supplied.\n" +
		"- **Verification**: focused verification passed."
	review := completeReviewWithCoverage(coverage)
	review = strings.Replace(review, "- Build: PASS", "- **Build**: PASS", 1)
	review = strings.Replace(review, "- Tests: PASS", "- **Tests**: PASS", 1)
	def := ReviewBranchDef{ChangedFiles: []string{"ratchet.go"}}
	result, err := def.ParseResult(review)
	if err != nil {
		t.Fatal(err)
	}
	if err := def.ValidateResult(result); err != nil {
		t.Fatalf("complete review with bold status labels was rejected: %v", err)
	}

	conflicting := strings.Replace(review, "- **Build**: PASS", "- Build: FAIL\n- **Build**: PASS", 1)
	result, err = def.ParseResult(conflicting)
	if err != nil {
		t.Fatal(err)
	}
	if err := def.ValidateResult(result); err == nil {
		t.Fatal("conflicting build statuses produced an approval")
	}
}
