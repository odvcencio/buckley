package commands

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCuratePRContext_BoundsProseWithoutMutatingEvidence(t *testing.T) {
	long := strings.Repeat("deterministic context with unicode λ ", 300)
	ctx := &PRContext{
		PR: &PRInfo{
			Number: 42,
			Body:   long,
		},
		Diff:         "diff --git a/a.go b/a.go\n+protected",
		Files:        []string{"a.go"},
		AgentsMD:     long,
		CanopyReview: long,
		Comments: []PRComment{{
			ID:     "comment-1",
			Author: "reviewer",
			Body:   long,
		}},
		Reviews: []PRReview{{
			ID:     "review-1",
			Author: "maintainer",
			Body:   long,
		}},
		ProviderEvidence: []PRContextEvidence{{
			Provider: "hyphae",
			Title:    "Prior decisions",
			Body:     long,
			Files:    []string{"a.go"},
		}},
	}
	originalIDs := ctx.RequiredFeedbackIDs()
	originalBody := ctx.PR.Body

	curated, accounting := CuratePRContext(ctx, 300)

	if accounting.IncludedTokens > accounting.BudgetTokens {
		t.Fatalf("included tokens = %d, budget = %d", accounting.IncludedTokens, accounting.BudgetTokens)
	}
	if accounting.SavedTokens <= 0 {
		t.Fatalf("saved tokens = %d, want positive savings", accounting.SavedTokens)
	}
	if curated.Diff != ctx.Diff || !reflect.DeepEqual(curated.Files, ctx.Files) {
		t.Fatal("curation changed protected diff or file coverage")
	}
	if got := curated.RequiredFeedbackIDs(); !reflect.DeepEqual(got, originalIDs) {
		t.Fatalf("curated feedback IDs = %v, want %v", got, originalIDs)
	}
	if ctx.PR.Body != originalBody || ctx.Comments[0].Body != long || ctx.ProviderEvidence[0].Body != long {
		t.Fatal("curation mutated captured evidence")
	}
	for _, value := range []string{
		curated.PR.Body,
		curated.Comments[0].Body,
		curated.ProviderEvidence[0].Body,
		curated.CanopyReview,
		curated.AgentsMD,
	} {
		if !utf8.ValidString(value) {
			t.Fatalf("projection produced invalid UTF-8: %q", value)
		}
	}
}

func TestAllocatePRSupportingBudgets_ReclaimsUnusedShares(t *testing.T) {
	got := allocatePRSupportingBudgets(
		100,
		[]int{0, 1_000, 1, 0, 0},
		[]int{prFeedbackBudgetParts, prProviderBudgetParts, prDescriptionBudgetParts, prCanopyBudgetParts, prAgentsBudgetParts},
	)
	if got[2] != 1 {
		t.Fatalf("small description allocation = %d, want its full one-token demand", got[2])
	}
	if got[1] != 99 {
		t.Fatalf("provider allocation = %d, want all 99 reclaimed tokens", got[1])
	}
	if sumInts(got) != 100 {
		t.Fatalf("allocated total = %d, want 100", sumInts(got))
	}
}

func TestBuildPRPrompt_UsesStoredProjectionAndPreservesFeedbackIdentity(t *testing.T) {
	long := strings.Repeat("review body ", 1_000)
	ctx := &PRContext{
		PR:       &PRInfo{Number: 7, Title: "Bound prompt context", Body: long},
		Diff:     "diff --git a/a.go b/a.go\n+protected",
		Files:    []string{"a.go"},
		Comments: []PRComment{{ID: "comment-1", Author: "reviewer", Body: long}},
	}
	projected, accounting := CuratePRContext(ctx, 80)
	ctx.promptContext = projected
	ctx.ContextCuration = accounting
	projected.ContextCuration = accounting

	prompt := BuildPRPrompt(ctx)

	if !strings.Contains(prompt, "## Context Projection") {
		t.Fatal("prompt does not disclose supporting-context projection")
	}
	if !strings.Contains(prompt, "top-level-comment:comment-1") {
		t.Fatal("prompt lost protected feedback identity")
	}
	if !strings.Contains(prompt, "+protected") {
		t.Fatal("prompt lost protected diff")
	}
	if strings.Count(prompt, "review body ") >= strings.Count(long, "review body ") {
		t.Fatal("prompt used unprojected review prose")
	}
}

func sumInts(values []int) int {
	total := 0
	for _, value := range values {
		total += value
	}
	return total
}
