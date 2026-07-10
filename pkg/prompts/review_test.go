package prompts

import (
	"strings"
	"testing"
	"time"
)

func TestReviewPromptsRequireEvidenceCoverageAndExactTools(t *testing.T) {
	for name, prompt := range map[string]string{
		"branch": reviewBranchWithToolsDefault(time.Unix(0, 0)),
		"PR":     reviewPRDefault(time.Unix(0, 0)),
	} {
		t.Run(name, func(t *testing.T) {
			for _, want := range []string{
				"read_file",
				"find_files",
				"search_text",
				"## Coverage",
				"## Invariant Audit",
				"## Falsification",
				"**File**: `path/to/changed-file`",
				"**Feedback disposition**",
				"DISPOSITIONED",
				"NONE_SUPPLIED",
				"**Feedback**: `feedback-id-exactly-as-supplied`",
				"ADDRESSED|DISPUTED|UNRESOLVED",
				"PASS|FAIL|PENDING|NOT_RUN|UNAVAILABLE|UNKNOWN",
				"every changed file",
				"ratchet",
				"AGENTS.md",
				"negative",
				"pagination",
				"remote identity",
				"provider/executor enforcement",
				"Strongest plausible failure",
				"already supplied by the sandbox",
				"same applicable toolchain",
				"cover every changed source path",
			} {
				if !strings.Contains(strings.ToLower(prompt), strings.ToLower(want)) {
					t.Errorf("prompt missing %q", want)
				}
			}
		})
	}
}

func TestReviewPromptsMakeApprovalVerificationPolicyExplicit(t *testing.T) {
	branch := reviewBranchWithToolsDefault(time.Unix(0, 0))
	for _, want := range []string{
		"APPROVE requires both Build and Tests to be PASS",
		"focused local verification actually completed",
		"Any FAIL, PENDING, NOT_RUN, UNAVAILABLE, or UNKNOWN state blocks approval",
	} {
		if !strings.Contains(branch, want) {
			t.Errorf("branch prompt missing %q", want)
		}
	}

	pr := reviewPRDefault(time.Unix(0, 0))
	for _, want := range []string{
		"aggregate remote CI status as authoritative",
		"passing (N/N)",
		"Failing, pending, unknown, or absent checks block approval",
		"repeat the exact Feedback ledger entry once for EVERY supplied ID",
	} {
		if !strings.Contains(pr, want) {
			t.Errorf("PR prompt missing %q", want)
		}
	}
}

func TestBranchReviewPromptDoesNotMandateBroadGoSweep(t *testing.T) {
	prompt := reviewBranchWithToolsDefault(time.Unix(0, 0))
	for _, forbidden := range []string{"Run 'go build ./...'", "Run 'go test ./...'"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("review prompt mandates project-unsafe broad gate %q", forbidden)
		}
	}
}
