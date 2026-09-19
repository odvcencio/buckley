package prompts

import (
	"strings"
	"testing"
	"time"
)

func TestReviewPromptsRequireEvidenceCoverageAndExactTools(t *testing.T) {
	for name, prompt := range map[string]string{
		"branch": reviewBranchWithToolsDefault(time.Unix(0, 0)),
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

	pr := reviewPRCompactDefault(time.Unix(0, 0))
	for _, want := range []string{
		"## Structural Impact",
		"Use deterministic structural metrics exactly as supplied",
		"not reported",
		"Pending, absent, stale, unavailable, or failing remote CI blocks approval",
		"APPROVE requires Grade A",
		"REQUEST CHANGES requires a proved defect",
		"run_verification: rerun failed or unavailable harness evidence only when a focused sandboxed check can resolve it",
	} {
		if !strings.Contains(pr, want) {
			t.Errorf("PR prompt missing %q", want)
		}
	}
}

func TestReviewPromptsRequireReachabilityBehaviorBindingsVisualsAndRuntimeOwnership(t *testing.T) {
	for name, prompt := range map[string]string{
		"branch": reviewBranchWithToolsDefault(time.Unix(0, 0)),
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

// TestBranchReviewPromptRendersSharedRuleConstants asserts that the branch
// review prompt renders the exported rule constants byte-for-byte, so a
// wording change only ever needs to happen in one place.
func TestBranchReviewPromptRendersSharedRuleConstants(t *testing.T) {
	rules := []string{
		RuleFindingsRequireProvedFalsification,
		RuleDisprovedOrUnresolvedGoesToRemarks,
	}
	branch := reviewBranchWithToolsDefault(time.Unix(0, 0))
	for _, rule := range append(rules, RuleUseHarnessVerificationEvidence) {
		if !strings.Contains(branch, rule) {
			t.Fatalf("branch prompt missing shared rule %q", rule)
		}
	}
}

func TestPRReviewPromptRestrictsMinorFindingsToRealDefects(t *testing.T) {
	prompt := reviewPRCompactDefault(time.Unix(0, 0))
	if !strings.Contains(prompt, "A Finding requires a demonstrated changed behavior, contract, security, data, performance, test, or operational defect") {
		t.Fatal("PR prompt does not restrict findings to demonstrated defects")
	}
	if strings.Contains(prompt, "MINOR: Style, naming, documentation, minor improvements") {
		t.Fatal("PR prompt still classifies style as a general MINOR finding")
	}
	for _, want := range []string{
		"Style, wording, naming, and speculative future concerns belong in Remarks or are omitted",
		"Only PROVED supports a Finding",
		"REQUEST CHANGES requires a proved defect and at least one blocker",
		"Partial or truncated required evidence blocks approval",
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
	words := promptWordCount(prompt)
	t.Logf("branch review prompt footprint: %d bytes, %d words", len(prompt), words)
	const maxBytes = 7_100
	if len(prompt) > maxBytes {
		t.Fatalf("branch review system prompt grew to %d bytes; budget is %d", len(prompt), maxBytes)
	}
	const maxWords = 950
	if words > maxWords {
		t.Fatalf("branch review system prompt grew to %d words; budget is %d", words, maxWords)
	}
}

func TestProjectReviewPromptStaysCompactAndExhaustive(t *testing.T) {
	prompt := reviewProjectDefault(time.Unix(0, 0))
	words := promptWordCount(prompt)
	t.Logf("project review prompt footprint: %d bytes, %d words", len(prompt), words)
	const maxBytes = 2_500
	if len(prompt) > maxBytes {
		t.Fatalf("project review system prompt grew to %d bytes; budget is %d", len(prompt), maxBytes)
	}
	const maxWords = 350
	if words > maxWords {
		t.Fatalf("project review system prompt grew to %d words; budget is %d", words, maxWords)
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

func promptWordCount(prompt string) int {
	return len(strings.Fields(prompt))
}
