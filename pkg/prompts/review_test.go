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
		"same applicable toolchain",
		"cover every changed source path",
		"Any FAIL, PENDING, NOT_RUN, UNAVAILABLE, or UNKNOWN state blocks approval",
		"Documentation-only exception",
		"exact changed claims, links, or diff hunks",
		"Mixed, source, and configuration changes do not qualify",
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
		"Do not rerun the full suite solely",
		"falsify a concrete risk",
		"do not replace the required remote gate",
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

func TestBranchReviewPromptStaysCompact(t *testing.T) {
	prompt := reviewBranchWithToolsDefault(time.Unix(0, 0))
	const maxBytes = 6_000
	if len(prompt) > maxBytes {
		t.Fatalf("branch review system prompt grew to %d bytes; budget is %d", len(prompt), maxBytes)
	}
}

func TestProjectReviewPromptStaysCompactAndBounded(t *testing.T) {
	prompt := reviewProjectDefault(time.Unix(0, 0))
	if len(prompt) > 2_500 {
		t.Fatalf("project review system prompt grew to %d bytes", len(prompt))
	}
	for _, want := range []string{"Canopy", "at most eight", "three highest-risk", "## Project Health"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("project review prompt missing %q", want)
		}
	}
}
