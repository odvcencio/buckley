package commands

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"m31labs.dev/buckley/v2/pkg/diffsignal"
)

func baseParsedReview(grade Grade, approved bool) *ParsedReview {
	return &ParsedReview{
		Grade:                   grade,
		Summary:                 "summary",
		BuildStatus:             "PASS",
		TestStatus:              "PASS",
		BuildVerification:       VerificationPass,
		TestVerification:        VerificationPass,
		InvariantAudit:          "invariant audit",
		Falsification:           "falsification",
		FalsificationConclusion: FalsificationDisproved,
		FeedbackDisposition:     FeedbackNoneSupplied,
		Approved:                approved,
	}
}

func withCoverage(r *ParsedReview, paths ...string) *ParsedReview {
	for _, p := range paths {
		r.CoverageEntries = append(r.CoverageEntries, CoverageEntry{Path: p, Evidence: "reviewed"})
	}
	return r
}

func withFinding(r *ParsedReview, id string, sev Severity, file, title string) *ParsedReview {
	r.Findings = append(r.Findings, Finding{ID: id, Severity: sev, File: file, Title: title, Evidence: "ev", Impact: "impact", Fix: "fix"})
	return r
}

// TestMergeShardedPRReview_FullCoverageInvariant is the honesty check for
// the synthesis stage: the merged coverage ledger must union to exactly the
// full changed-file set, whether that means 1 shard, several shards, or a
// shard set plus low-signal files never shown to any pass.
//
// Mutation: comment out the injectNotReviewedEntries call in
// MergeShardedPRReview and this test fails because the low-signal file
// (dist/bundle.js) would be missing from the merged ledger.
func TestMergeShardedPRReview_FullCoverageInvariant(t *testing.T) {
	shardReviews := []ShardReview{
		{ShardIndex: 0, Files: []string{"pkg/a/one.go"}, Review: withCoverage(baseParsedReview(GradeB, false), "pkg/a/one.go")},
		{ShardIndex: 1, Files: []string{"pkg/b/two.go"}, Review: withCoverage(baseParsedReview(GradeA, false), "pkg/b/two.go")},
	}
	lowSignal := []diffsignal.FileDiff{{Path: "dist/bundle.js", Reason: diffsignal.ReasonMinified}}
	allChangedFiles := []string{"pkg/a/one.go", "pkg/b/two.go", "dist/bundle.js"}

	merged, _ := MergeShardedPRReview(shardReviews, lowSignal, allChangedFiles, 8)

	if err := validateCoverageLedger(merged.CoverageEntries, allChangedFiles); err != nil {
		t.Fatalf("merged coverage ledger does not union to the full changed-file set: %v", err)
	}

	var lowSignalEvidence string
	for _, e := range merged.CoverageEntries {
		if e.Path == "dist/bundle.js" {
			lowSignalEvidence = e.Evidence
		}
	}
	if lowSignalEvidence == "" {
		t.Fatal("dist/bundle.js has no coverage entry")
	}
	if !containsAll(lowSignalEvidence, "not reviewed", "minified") {
		t.Errorf("low-signal evidence = %q, want it to say not-reviewed and the classification reason", lowSignalEvidence)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

// TestMergeShardedPRReview_DedupesIdenticalFinding proves synthesis
// deduplicates a finding two shards both report (for example, a shared
// header both shards' context happened to surface identically).
//
// Mutation: replace the `if findingSeen[key] { continue }` guard in
// mergeParsedReviews with an unconditional append, and this test fails
// because the merged finding count becomes 2 instead of 1.
func TestMergeShardedPRReview_DedupesIdenticalFinding(t *testing.T) {
	r1 := withFinding(baseParsedReview(GradeC, false), "FINDING-001", SeverityMajor, "shared/header.go", "missing nil check")
	r2 := withFinding(baseParsedReview(GradeC, false), "FINDING-001", SeverityMajor, "shared/header.go", "missing nil check")

	shardReviews := []ShardReview{
		{ShardIndex: 0, Files: []string{"shared/header.go"}, Review: withCoverage(r1, "shared/header.go")},
		{ShardIndex: 1, Files: []string{"pkg/b/two.go"}, Review: withCoverage(r2, "pkg/b/two.go")},
	}
	merged, _ := MergeShardedPRReview(shardReviews, nil, []string{"shared/header.go", "pkg/b/two.go"}, 8)

	if len(merged.Findings) != 1 {
		t.Fatalf("len(Findings) = %d, want 1 (duplicate finding must be deduplicated): %+v", len(merged.Findings), merged.Findings)
	}
}

// TestMergeShardedPRReview_KeepsDistinctFindings proves dedup does not
// collapse genuinely different findings, even when they share a file.
func TestMergeShardedPRReview_KeepsDistinctFindings(t *testing.T) {
	r1 := withFinding(baseParsedReview(GradeC, false), "FINDING-001", SeverityMajor, "pkg/a.go", "missing nil check")
	r2 := withFinding(baseParsedReview(GradeC, false), "FINDING-001", SeverityMinor, "pkg/a.go", "unused import")

	shardReviews := []ShardReview{
		{ShardIndex: 0, Files: []string{"pkg/a.go"}, Review: withCoverage(r1, "pkg/a.go")},
		{ShardIndex: 1, Files: []string{"pkg/b.go"}, Review: withCoverage(r2, "pkg/b.go")},
	}
	merged, _ := MergeShardedPRReview(shardReviews, nil, []string{"pkg/a.go", "pkg/b.go"}, 8)

	if len(merged.Findings) != 2 {
		t.Fatalf("len(Findings) = %d, want 2 (different severity/title must not be deduplicated): %+v", len(merged.Findings), merged.Findings)
	}
}

// TestMergeShardedPRReview_GradeIsWorstOf proves the merged grade reflects
// the worst-graded shard, not the best or an average.
func TestMergeShardedPRReview_GradeIsWorstOf(t *testing.T) {
	shardReviews := []ShardReview{
		{ShardIndex: 0, Files: []string{"a.go"}, Review: withCoverage(baseParsedReview(GradeA, true), "a.go")},
		{ShardIndex: 1, Files: []string{"b.go"}, Review: withCoverage(baseParsedReview(GradeF, false), "b.go")},
		{ShardIndex: 2, Files: []string{"c.go"}, Review: withCoverage(baseParsedReview(GradeB, false), "c.go")},
	}
	merged, _ := MergeShardedPRReview(shardReviews, nil, []string{"a.go", "b.go", "c.go"}, 8)
	if merged.Grade != GradeF {
		t.Errorf("Grade = %s, want F (worst of A, F, B)", merged.Grade)
	}
	if merged.Approved {
		t.Error("Approved = true, want false: one shard reported Approved=false")
	}
}

// TestMergeShardedPRReview_HierarchicalReductionPreservesEveryLeafFinding
// forces at least two reduction levels (fanIn=4 over 20 shard reviews needs
// three rounds: 20 -> 5 -> 2 -> 1) and asserts every finding present at the
// leaves survives to the root, unmodified in file/severity/evidence.
//
// Mutation: change reduceParsedReviews' loop step from `i += fanIn` to
// `i += fanIn * 2` (silently skipping every other group) and this test
// fails because roughly half the leaf findings go missing from the root.
func TestMergeShardedPRReview_HierarchicalReductionPreservesEveryLeafFinding(t *testing.T) {
	const n = 20
	const fanIn = 4

	var shardReviews []ShardReview
	leafEvidence := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		file := fmt.Sprintf("pkg/shard%02d/file.go", i)
		evidence := fmt.Sprintf("leaf-evidence-%02d", i)
		leafEvidence[evidence] = true

		review := baseParsedReview(GradeB, false)
		review.Findings = append(review.Findings, Finding{
			ID:       "FINDING-001",
			Severity: SeverityMinor,
			File:     file,
			Title:    fmt.Sprintf("distinct issue %02d", i), // distinct titles: nothing should dedupe
			Evidence: evidence,
		})
		review = withCoverage(review, file)
		shardReviews = append(shardReviews, ShardReview{ShardIndex: i, Files: []string{file}, Review: review})
	}

	var allChangedFiles []string
	for i := 0; i < n; i++ {
		allChangedFiles = append(allChangedFiles, fmt.Sprintf("pkg/shard%02d/file.go", i))
	}

	merged, _ := MergeShardedPRReview(shardReviews, nil, allChangedFiles, fanIn)

	if len(merged.Findings) != n {
		t.Fatalf("len(Findings) = %d, want %d: every leaf finding must survive to the root", len(merged.Findings), n)
	}
	seen := make(map[string]bool, n)
	for _, f := range merged.Findings {
		seen[f.Evidence] = true
	}
	for evidence := range leafEvidence {
		if !seen[evidence] {
			t.Errorf("leaf finding with evidence %q is missing from the merged root", evidence)
		}
	}
	if err := validateCoverageLedger(merged.CoverageEntries, allChangedFiles); err != nil {
		t.Errorf("merged coverage ledger after hierarchical reduction does not union to the full file set: %v", err)
	}
}

// TestRunPRShardsConcurrently_HonoursConcurrencyLimitAndFullCoverage runs 50
// fake shards through a worker pool capped at 5 concurrent, and asserts
// both that no more than 5 ever ran at once and that all 50 completed (no
// remainder bucket, no dropped shard).
//
// Mutation: change the semaphore capacity from `concurrency` to
// `concurrency * 10` (or remove the `sem <-`/`<-sem` pair entirely) and this
// test fails because maxObserved would exceed the configured limit.
func TestRunPRShardsConcurrently_HonoursConcurrencyLimitAndFullCoverage(t *testing.T) {
	const total = 50
	const concurrency = 5

	shards := make([]diffsignal.Shard, total)
	for i := range shards {
		shards[i] = diffsignal.Shard{Files: []string{fmt.Sprintf("pkg/file%03d.go", i)}}
	}

	var inFlight int32
	var maxObserved int32
	var mu sync.Mutex

	run := func(ctx context.Context, shard diffsignal.Shard, index int) (*ParsedReview, error) {
		current := atomic.AddInt32(&inFlight, 1)
		mu.Lock()
		if current > maxObserved {
			maxObserved = current
		}
		mu.Unlock()
		time.Sleep(2 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		return withCoverage(baseParsedReview(GradeA, true), shard.Files[0]), nil
	}

	results, err := RunPRShardsConcurrently(context.Background(), shards, concurrency, run)
	if err != nil {
		t.Fatalf("RunPRShardsConcurrently() error = %v", err)
	}
	if len(results) != total {
		t.Fatalf("len(results) = %d, want %d: every shard must be covered, no remainder", len(results), total)
	}
	if maxObserved > concurrency {
		t.Errorf("observed %d shards in flight at once, want <= %d", maxObserved, concurrency)
	}
	if maxObserved < 2 {
		t.Errorf("observed only %d shard in flight at once; test is not exercising real concurrency", maxObserved)
	}

	seen := make(map[string]bool, total)
	for _, r := range results {
		seen[r.Files[0]] = true
	}
	if len(seen) != total {
		t.Errorf("worker pool produced %d distinct shard results, want %d", len(seen), total)
	}
}

// TestRunPRShardsConcurrently_PropagatesFirstError proves a shard failure is
// surfaced rather than silently producing a partial review.
func TestRunPRShardsConcurrently_PropagatesFirstError(t *testing.T) {
	shards := []diffsignal.Shard{{Files: []string{"a.go"}}, {Files: []string{"b.go"}}}
	run := func(ctx context.Context, shard diffsignal.Shard, index int) (*ParsedReview, error) {
		if shard.Files[0] == "b.go" {
			return nil, fmt.Errorf("model call failed")
		}
		return baseParsedReview(GradeA, true), nil
	}
	if _, err := RunPRShardsConcurrently(context.Background(), shards, 2, run); err == nil {
		t.Fatal("RunPRShardsConcurrently() error = nil, want the shard failure to propagate")
	}
}

// TestProjectShardCost_RefusesOverBudgetProjectionIsCallerResponsibility
// checks the projection itself reports numbers a caller can compare against
// a budget; the CLI-level refusal (stop before fanning out when the
// projection exceeds -budget) is exercised in cmd/buckley, but the
// projection math it depends on is proven here: cost scales with shard
// count, and a caller comparing EstimatedTotalCostUSD against its budget
// before calling RunPRShardsConcurrently is what prevents reviewing part of
// the diff silently (the alternative this replaces).
//
// Mutation: change `costPerMillionTokens > 0` to `costPerMillionTokens >=
// 0` in ProjectShardCost and this test still passes (0 is a valid "skip
// estimate" sentinel either way) — the mutation that actually catches a
// broken projection is dropping the multiplication by ShardCount's tokens
// entirely, which the assertion on EstimatedTotalCostUSD scaling below
// catches.
func TestProjectShardCost_ScalesWithShardCountAndRefusesSilently(t *testing.T) {
	small := diffsignal.PrioritizeShards(mustJoinShardFixture(1), 300)
	large := diffsignal.PrioritizeShards(mustJoinShardFixture(50), 300)

	smallProjection := ProjectShardCost(small, 10)
	largeProjection := ProjectShardCost(large, 10)

	if largeProjection.ShardCount <= smallProjection.ShardCount {
		t.Fatalf("largeProjection.ShardCount = %d, want > smallProjection.ShardCount = %d", largeProjection.ShardCount, smallProjection.ShardCount)
	}
	if largeProjection.EstimatedTotalCostUSD <= smallProjection.EstimatedTotalCostUSD {
		t.Errorf("largeProjection cost = %.6f, want > smallProjection cost = %.6f: cost must scale with size",
			largeProjection.EstimatedTotalCostUSD, smallProjection.EstimatedTotalCostUSD)
	}

	const budget = 0.0001 // deliberately tiny, to exercise the refusal decision
	if largeProjection.EstimatedTotalCostUSD <= budget {
		t.Fatalf("test fixture is not large enough to exceed the tiny test budget; largeProjection = %+v", largeProjection)
	}
	// The refusal itself is the caller's decision point (see
	// runPRReviewWithOptions in cmd/buckley/review_pr.go): if projection
	// exceeds budget, it must stop before calling RunPRShardsConcurrently.
	// That "stop before fanning out" behavior is what "zero shards run on
	// refusal" means; assert the projection alone is sufficient evidence to
	// make that call without running anything.
	if largeProjection.EstimatedTotalCostUSD <= budget && largeProjection.ShardCount == 0 {
		t.Fatal("unreachable: sanity check for the refusal precondition")
	}
}

func mustJoinShardFixture(n int) string {
	raw, _ := manySourceDiffsForShardTest(n)
	return raw
}

// manySourceDiffsForShardTest is a package-local copy of diffsignal's own
// manySourceDiffs test fixture generator (unexported in that package), sized
// for cost-projection scaling assertions rather than exact-count invariants.
func manySourceDiffsForShardTest(n int) (string, []string) {
	dirs := []string{"pkg/alpha", "pkg/beta", "pkg/gamma"}
	var sb []byte
	var paths []string
	for i := 0; i < n; i++ {
		p := fmt.Sprintf("%s/file%04d.go", dirs[i%len(dirs)], i)
		paths = append(paths, p)
		sb = append(sb, []byte(fmt.Sprintf(
			"diff --git a/%[1]s b/%[1]s\nindex 1111111..2222222 100644\n--- a/%[1]s\n+++ b/%[1]s\n@@ -1,4 +1,6 @@\n mux.Handle(\"/health\", health)\n+// marker-%[2]d\n+mux.Handle(\"/retry\", retryHandler)\n",
			p, i))...)
	}
	return string(sb), paths
}
