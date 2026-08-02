package commands

import (
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/diffsignal"
)

func TestBuildPRShardPrompt_ScopesDeterministicEvidence(t *testing.T) {
	ctx := &PRContext{
		PR:    &PRInfo{Number: 42, Title: "Scoped evidence"},
		Files: []string{"pkg/api/a.go", "pkg/storage/b.go"},
		ProviderEvidence: []PRContextEvidence{
			{Provider: "hyphae", Title: "Global", Body: "primary only"},
			{Provider: "canopy", Title: "All", Body: "shared", AllShards: true},
			{Provider: "canopy", Title: "API", Body: "api consumer", Files: []string{"pkg/api/a.go"}},
			{Provider: "canopy", Title: "Storage", Body: "storage consumer", Files: []string{"pkg/storage/b.go"}},
		},
	}
	shards := []diffsignal.Shard{
		{Context: "api diff", Files: []string{"pkg/api/a.go"}},
		{Context: "storage diff", Files: []string{"pkg/storage/b.go"}},
	}

	primary := BuildPRShardPrompt(ctx, shards[0], 0, 2, true)
	for _, want := range []string{"Global", "All", "API"} {
		if !strings.Contains(primary, "Deterministic Evidence: "+want) {
			t.Errorf("primary prompt missing %q evidence", want)
		}
	}
	if strings.Contains(primary, "Deterministic Evidence: Storage") {
		t.Fatal("primary prompt included storage-scoped evidence")
	}

	secondary := BuildPRShardPrompt(ctx, shards[1], 1, 2, false)
	for _, want := range []string{"All", "Storage"} {
		if !strings.Contains(secondary, "Deterministic Evidence: "+want) {
			t.Errorf("secondary prompt missing %q evidence", want)
		}
	}
	if strings.Contains(secondary, "Deterministic Evidence: Global") ||
		strings.Contains(secondary, "Deterministic Evidence: API") {
		t.Fatal("secondary prompt included primary-only or API-scoped evidence")
	}
}

func TestBuildPRShardPrompt_IncludesRequiredVerificationTargets(t *testing.T) {
	ctx := &PRContext{
		PR:    &PRInfo{Number: 77, Title: "Shard verification targets"},
		Files: []string{"pkg/api/a.go", "pkg/storage/b.go"},
	}
	shard := diffsignal.Shard{Context: "api diff", Files: []string{"pkg/api/a.go"}}

	prompt := BuildPRShardPrompt(ctx, shard, 0, 2, true)
	if !strings.Contains(prompt, "## Required Local Verification Targets") {
		t.Fatalf("shard prompt missing verification targets section:\n%s", prompt)
	}
	if !strings.Contains(prompt, "go: pkg/api") {
		t.Fatalf("shard prompt missing shard-scoped target:\n%s", prompt)
	}
	if strings.Contains(prompt, "go: pkg/storage") {
		t.Fatal("shard prompt included target outside shard's own files")
	}
}

func TestProjectShardCostWithContext_IncludesProjectedSharedEvidence(t *testing.T) {
	shards := diffsignal.ShardResult{
		Shards: []diffsignal.Shard{
			{Context: "diff one", Files: []string{"a.go"}},
			{Context: "diff two", Files: []string{"b.go"}},
		},
	}
	ctx := &PRContext{
		PR:    &PRInfo{Number: 42, Title: "Cost projected prompt"},
		Files: []string{"a.go", "b.go"},
		ProviderEvidence: []PRContextEvidence{{
			Provider:  "canopy",
			Title:     "Shared consumers",
			Body:      strings.Repeat("consumer evidence ", 40),
			AllShards: true,
		}},
	}

	legacy := ProjectShardCost(shards, 10)
	withContext := ProjectShardCostWithContext(shards, ctx, 10)

	wantTokens := 0
	for index, shard := range shards.Shards {
		wantTokens += reviewEstimateTokens(BuildPRShardPrompt(ctx, shard, index, len(shards.Shards), index == 0))
	}
	if withContext.EstimatedTotalTokens != wantTokens {
		t.Fatalf("estimated tokens = %d, want exact projected prompt total %d", withContext.EstimatedTotalTokens, wantTokens)
	}
	if withContext.EstimatedTotalTokens <= legacy.EstimatedTotalTokens {
		t.Fatalf("context-aware tokens = %d, want > diff-only %d", withContext.EstimatedTotalTokens, legacy.EstimatedTotalTokens)
	}
	if withContext.EstimatedTotalCostUSD <= legacy.EstimatedTotalCostUSD {
		t.Fatal("context-aware cost did not include shared evidence")
	}
}
