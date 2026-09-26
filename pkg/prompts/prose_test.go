package prompts

import (
	"strings"
	"testing"
	"time"
)

func TestProsePrompts_RegisterGuidance(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, kind := range []string{"COMMIT", "PR", "REVIEW", "REVIEW_BRANCH", "REVIEW_PROJECT", "REVIEW_PR"} {
		t.Setenv("BUCKLEY_PROMPT_"+kind, "")
		t.Setenv("BUCKLEY_PROMPT_"+kind+"_FILE", "")
	}
	now := time.Unix(0, 0)
	for _, tc := range []struct{ name, prompt, block string }{
		{"commit", CommitPrompt(now), CommitProseBlock()},
		{"pr", PRPrompt(now), PRProseBlock()},
		{"review", ReviewPrompt(now, nil), ReviewProseBlock()},
		{"branch", ReviewBranchWithToolsPrompt(now), ReviewProseBlock()},
		{"project", ReviewProjectPrompt(now), ReviewProseBlock()},
		{"review-pr", ReviewPRPrompt(now), ReviewProseBlock()},
		{"critic", ReviewApprovalCriticPrompt(ReviewPRPrompt(now)), ReviewProseBlock()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(tc.prompt, tc.block) {
				t.Fatal("missing register guidance")
			}
			if strings.Contains(tc.prompt, "ASD-STE100") {
				t.Fatal("prompt contains superseded standard")
			}
		})
	}
}

func TestPlainLanguageCore_FiveRules(t *testing.T) {
	for _, rule := range []string{"Lead with the point.", "Say it plainly.", "Keep terms stable.", "Show receipts.", "Shape it for scanning."} {
		if !strings.Contains(plainLanguageCore, rule) {
			t.Errorf("missing rule %q", rule)
		}
	}
}

func TestReviewProseBlock_ReaderHarm(t *testing.T) {
	for _, want := range []string{"unclear", "misleading", "unsourced", "inconsistent in its terms", "undefined abbreviation", "No style nitpicks", "suggested rewrite", "**Category**: prose"} {
		if !strings.Contains(ReviewProseBlock(), want) {
			t.Errorf("missing review constraint %q", want)
		}
	}
}

func TestReviewProseBlock_Footprint(t *testing.T) {
	block := ReviewProseBlock()
	if len(block) > 1100 || promptWordCount(block) > 175 {
		t.Fatalf("prose block grew to %d bytes, %d words", len(block), promptWordCount(block))
	}
}
