package prompts

import (
	"strings"
	"testing"
	"time"
)

// TestSTE100MarkerPresentAtEachWiringSite asserts the ASD-STE100 marker
// line reaches every prompt site required by decision 0011: commit
// generation, PR generation, and buckley review (branch and PR).
func TestSTE100MarkerPresentAtEachWiringSite(t *testing.T) {
	const marker = "ASD-STE100 profile:"
	now := time.Unix(0, 0)

	cases := map[string]string{
		"commit":        commitDefault(now),
		"pr":            prDefault(now),
		"review-branch": reviewBranchWithToolsDefault(now),
		"review-pr":     reviewPRDefault(now),
	}

	for name, prompt := range cases {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(prompt, marker) {
				t.Fatalf("%s prompt missing STE-100 marker %q", name, marker)
			}
		})
	}
}

// TestSTE100ProseBlockContent asserts the shared prose block encodes the
// operative rules profile from decision 0011 (active voice, sentence
// length caps, noun-cluster limit, abbreviation rule, banned idioms).
func TestSTE100ProseBlockContent(t *testing.T) {
	for _, want := range []string{
		"ASD-STE100 profile:",
		"active voice",
		"imperative mood",
		"25 words",
		"noun clusters of more than three",
		"idioms, slang, or Latin abbreviations",
		"Define an abbreviation at first use",
		"concrete verbs",
	} {
		if !strings.Contains(ste100ProseBlock, want) {
			t.Errorf("ste100ProseBlock missing %q", want)
		}
	}
}

// TestSTE100ReviewTenetContent asserts the review tenet names every
// checked violation category and requires a suggested rewrite per flag.
func TestSTE100ReviewTenetContent(t *testing.T) {
	for _, want := range []string{
		"ASD-STE100 profile:",
		"commit messages, PR titles/descriptions, and added doc or",
		"Passive voice",
		"20 words",
		"25 words",
		"Noun clusters",
		"Inconsistent terminology",
		"undefined at first use",
		"suggested rewrite",
		"MINOR finding",
	} {
		if !strings.Contains(ste100ReviewTenet, want) {
			t.Errorf("ste100ReviewTenet missing %q", want)
		}
	}
}

// TestSTE100MarkerSurvivesResolvePromptWithNoOverride asserts the marker
// reaches the effective prompt returned by CommitPrompt/PRPrompt when no
// override is configured (the common, un-overridden path).
func TestSTE100MarkerSurvivesResolvePromptWithNoOverride(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Force-clear any override env vars a polluted runner environment might
	// have set, so this test always exercises the un-overridden default path.
	t.Setenv("BUCKLEY_PROMPT_COMMIT", "")
	t.Setenv("BUCKLEY_PROMPT_COMMIT_FILE", "")
	t.Setenv("BUCKLEY_PROMPT_PR", "")
	t.Setenv("BUCKLEY_PROMPT_PR_FILE", "")
	now := time.Unix(0, 0)

	if !strings.Contains(CommitPrompt(now), "ASD-STE100 profile:") {
		t.Fatalf("CommitPrompt missing STE-100 marker")
	}
	if !strings.Contains(PRPrompt(now), "ASD-STE100 profile:") {
		t.Fatalf("PRPrompt missing STE-100 marker")
	}
}
