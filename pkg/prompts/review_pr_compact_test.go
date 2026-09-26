package prompts

import (
	"strings"
	"testing"
	"time"
)

func TestCompactPRReviewPrompt_PreservesMachineContract(t *testing.T) {
	prompt := reviewPRCompactDefault(time.Unix(0, 0))
	for _, want := range []string{
		"backend-neutral",
		"Deterministic provider and Canopy evidence",
		"Disposition every supplied Feedback ID exactly once",
		"every changed file",
		"## Structural Impact",
		"## Coverage",
		"## Invariant Audit",
		"## Falsification",
		"## Findings",
		"## Verdict",
		"APPROVE requires Grade A",
		"REQUEST CHANGES requires a proved defect",
		"production routing",
		"derived caches",
		"bounds/ratchets",
		"empty and failure paths",
		"serialization pairs",
		"cleanup",
		"CI triggers",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("compact PR prompt missing %q", want)
		}
	}
}

func TestCompactPRReviewPrompt_DefinesNoneForEmptyFindings(t *testing.T) {
	prompt := reviewPRCompactDefault(time.Unix(0, 0))
	findingsSection := prompt[strings.Index(prompt, "## Findings"):strings.Index(prompt, "## Remarks")]
	if !strings.Contains(findingsSection, "`None.`") {
		t.Fatalf("Findings section does not define `None.` for the empty case:\n%s", findingsSection)
	}
}

func TestReviewPRPromptUsesCompactDefaultAndOverride(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BUCKLEY_PROMPT_REVIEW_PR", "")
	t.Setenv("BUCKLEY_PROMPT_REVIEW_PR_FILE", "")
	now := time.Unix(0, 0)

	defaultPrompt := reviewPRCompactDefault(now)
	if got := ReviewPRPrompt(now); got != defaultPrompt {
		t.Fatalf("ReviewPRPrompt() did not use compact default")
	}

	t.Setenv("BUCKLEY_PROMPT_REVIEW_PR", "{{DEFAULT_PROMPT}}\n\nExtra PR review guidance at {{CURRENT_TIME}}.")
	got := ReviewPRPrompt(now)
	if !strings.HasPrefix(got, defaultPrompt) {
		t.Fatalf("ReviewPRPrompt() override did not preserve the compact default")
	}
	if !strings.Contains(got, "Extra PR review guidance at "+now.Format(time.RFC3339)+".") {
		t.Fatalf("ReviewPRPrompt() did not apply review-pr override placeholders; got %q", got)
	}
}

func TestCompactPRReviewPrompt_StaysWithinFootprintBudget(t *testing.T) {
	prompt := reviewPRCompactDefault(time.Unix(0, 0))
	words := promptWordCount(prompt)
	t.Logf("compact PR review prompt footprint: %d bytes, %d words", len(prompt), words)
	maxBytes := 4_700 + len(ReviewProseBlock())
	if len(prompt) > maxBytes {
		t.Fatalf("compact PR review prompt grew to %d bytes; budget is %d", len(prompt), maxBytes)
	}
	maxWords := 650 + promptWordCount(ReviewProseBlock())
	if words > maxWords {
		t.Fatalf("compact PR review prompt grew to %d words; budget is %d", words, maxWords)
	}
}
