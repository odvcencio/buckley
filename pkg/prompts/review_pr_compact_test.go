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

func TestCompactPRReviewPrompt_StaysWithinTokenEnvelope(t *testing.T) {
	prompt := reviewPRCompactDefault(time.Unix(0, 0))
	const maxBytes = 7_000
	if len(prompt) > maxBytes {
		t.Fatalf("compact PR review prompt grew to %d bytes; budget is %d", len(prompt), maxBytes)
	}
}
