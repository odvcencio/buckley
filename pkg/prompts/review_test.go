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
				"ADDRESSED|DISPUTED|DISPOSITIONED|UNRESOLVED",
				"PASS|FAIL|NOT_APPLICABLE|PENDING|NOT_RUN|UNAVAILABLE|UNKNOWN",
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
		"APPROVE requires Build PASS plus Tests PASS, or trusted NO_TEST_GATE",
		"same applicable toolchain",
		"cover every changed source path",
		"Any FAIL, PENDING, NOT_RUN, UNAVAILABLE, or UNKNOWN state blocks approval",
		"Documentation-only exception",
		"exact changed claims, links, or diff hunks",
		"Mixed, source, and configuration changes do not qualify",
		"For Go, harness-collected run_verification kind=test",
		"CONFIRMED_PASS",
		"INCONCLUSIVE",
		"**Recommendation**: APPROVE / REQUEST CHANGES / NEEDS DISCUSSION",
		"Use NEEDS DISCUSSION with Blockers NONE",
	} {
		if !strings.Contains(branch, want) {
			t.Errorf("branch prompt missing %q", want)
		}
	}

	pr := reviewPRDefault(time.Unix(0, 0))
	for _, want := range []string{
		"## Structural Impact",
		"exact changed symbol and file counts",
		"Never estimate a structural metric",
		"aggregate remote CI status as authoritative",
		"passing (N/N)",
		"Failing, pending, unknown, or absent checks block approval",
		"Pending, unknown, absent, or stale remote CI is a merge gate, not a proved current failure",
		"use Grade B with NEEDS DISCUSSION",
		"Missing duplicate verification alone requires Grade B",
		"Do not list the condition as a Blocker or Finding",
		"repeat the exact Feedback ledger entry once for EVERY supplied ID",
		"Do not rerun the full suite solely",
		"falsify a concrete risk",
		"do not replace the required remote gate",
		"smallest changed right-side line",
		"exact replacement for the anchored changed lines",
		"CONFIRMED_PASS",
		"INCONCLUSIVE",
		"cannot override that result",
	} {
		if !strings.Contains(pr, want) {
			t.Errorf("PR prompt missing %q", want)
		}
	}
}

func TestReviewPromptsRequireReachabilityBehaviorBindingsVisualsAndRuntimeOwnership(t *testing.T) {
	for name, prompt := range map[string]string{
		"branch": reviewBranchWithToolsDefault(time.Unix(0, 0)),
		"PR":     reviewPRDefault(time.Unix(0, 0)),
	} {
		t.Run(name, func(t *testing.T) {
			for _, want := range []string{
				"derived cache",
				"dispatch gate",
				"optimized path",
				"production",
				"direct helper test",
				"synthetic",
				"generated metadata",
				"smallest valid shape",
				"removes or replaces an implementation path",
				"compare",
				"observable behavior",
				"submit",
				"click",
				"navigation",
				"reset",
				"error",
				"loading",
				"focus",
				"accessibility",
				"framework-generated or convention-shaped state",
				"real producer",
				"serializer",
				"runtime binding",
				"consumer key shape",
				"success",
				"failure",
				"empty/default",
				"redirect/reload",
				"visual",
				"canvas",
				"shader",
				"coordinate space",
				"actual render or mount box",
				"responsive transforms",
				"overflow",
				"initial",
				"settled",
				"pixel or screenshot evidence",
				"metrics alone cannot approve",
				"generated or framework-owned runtime code",
				"app-owned bespoke JavaScript",
				"do not count generated runtime as bespoke app code",
			} {
				if !strings.Contains(strings.ToLower(prompt), strings.ToLower(want)) {
					t.Errorf("prompt missing %q", want)
				}
			}
		})
	}

	critic := ReviewApprovalCriticPrompt("review")
	for _, want := range []string{"unreachable generalized paths", "derived caches", "dispatch gates", "synthetic fixtures"} {
		if !strings.Contains(critic, want) {
			t.Errorf("approval critic prompt missing %q", want)
		}
	}
}

// TestReviewPromptsShareRuleConstants asserts that the PR and branch review
// prompts both render the exported rule constants byte-for-byte, so a
// wording change only ever needs to happen in one place.
func TestReviewPromptsShareRuleConstants(t *testing.T) {
	rules := []string{
		RuleFindingsRequireProvedFalsification,
		RuleDisprovedOrUnresolvedGoesToRemarks,
	}
	pr := reviewPRDefault(time.Unix(0, 0))
	for _, rule := range rules {
		if !strings.Contains(pr, rule) {
			t.Fatalf("PR prompt missing shared rule %q", rule)
		}
	}
	branch := reviewBranchWithToolsDefault(time.Unix(0, 0))
	for _, rule := range append(rules, RuleUseHarnessVerificationEvidence) {
		if !strings.Contains(branch, rule) {
			t.Fatalf("branch prompt missing shared rule %q", rule)
		}
	}
}

func TestPRReviewPromptRestrictsMinorFindingsToRealDefects(t *testing.T) {
	prompt := reviewPRDefault(time.Unix(0, 0))
	if !strings.Contains(prompt, "MINOR: A real non-blocking behavior, validation, test, or operational defect") {
		t.Fatal("PR prompt does not restrict MINOR findings to real defects")
	}
	if strings.Contains(prompt, "MINOR: Style, naming, documentation, minor improvements") {
		t.Fatal("PR prompt still classifies style as a general MINOR finding")
	}
	for _, want := range []string{
		"Never report ASD-STE100",
		"permits clusters of three nouns",
		"more test shapes only because more shapes exist",
		"Write Findings only when Falsification concludes PROVED",
		"Do not invent or paraphrase an identifier",
		"REQUEST CHANGES requires at least one Blocker",
		"current failing input, violated invariant, failing check, or reproducible current behavior",
		"possible rename, regeneration, test drift, or private test-hook change is not a finding",
		"malformed historical fixture",
		"Write no words after the token",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("PR prompt missing %q", want)
		}
	}
}

func TestBranchReviewPromptRequiresProvedFalsificationForFindings(t *testing.T) {
	prompt := reviewBranchWithToolsDefault(time.Unix(0, 0))
	if !strings.Contains(prompt, "Write Findings only when Falsification concludes PROVED") {
		t.Fatal("branch prompt does not require proved falsification for findings")
	}
}

func TestBranchReviewPromptDefinesNoneForEmptyFindings(t *testing.T) {
	prompt := reviewBranchWithToolsDefault(time.Unix(0, 0))
	findingsSection := prompt[strings.Index(prompt, "## Findings"):strings.Index(prompt, "## Remarks")]
	if !strings.Contains(findingsSection, "`None.`") {
		t.Fatalf("Findings section does not define `None.` for the empty case:\n%s", findingsSection)
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
	const maxBytes = 6_750
	if len(prompt) > maxBytes {
		t.Fatalf("branch review system prompt grew to %d bytes; budget is %d", len(prompt), maxBytes)
	}
}

func TestProjectReviewPromptStaysCompactAndExhaustive(t *testing.T) {
	prompt := reviewProjectDefault(time.Unix(0, 0))
	if len(prompt) > 2_500 {
		t.Fatalf("project review system prompt grew to %d bytes", len(prompt))
	}
	for _, want := range []string{"Canopy", "Canopy TOC", "major call sites", "caller/callee", "no per-review tool-call or model-turn cap", "## Evidence Collected", "## Coverage", "## Project Health"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("project review prompt missing %q", want)
		}
	}
	for _, forbidden := range []string{"at most eight", "three highest-risk"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("project review prompt retained hidden sampling limit %q", forbidden)
		}
	}
}
