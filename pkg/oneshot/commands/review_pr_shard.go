package commands

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"m31labs.dev/buckley/v2/pkg/diffsignal"
)

// DefaultSynthesisFanIn bounds how many shard (or intermediate) reviews one
// synthesis step reduces at once. A synthesis pass over hundreds of shard
// reports would reintroduce the same context-overflow failure one level up;
// reducing in a tree instead keeps each step's input bounded regardless of
// total shard count.
const DefaultSynthesisFanIn = 8

// ShardReview is one shard's completed review: the structured result plus
// which files that shard covered. It is the leaf unit MergeShardedPRReview
// reduces over.
type ShardReview struct {
	ShardIndex int
	Files      []string
	Review     *ParsedReview
}

// BuildPRShardPrompt builds the user prompt for one shard of a fanned-out PR
// review. Every shard sees the PR's identity, CI status, and (for the
// primary shard only) prior review feedback to disposition; every shard
// reviews only its own slice of the diff and must ledger exactly that slice.
//
// Non-primary shards are instructed to report Grade B (never A) and
// Approved: NO regardless of findings, because only the synthesis step (with
// the full changed-file set, CI state, and feedback ledger) can issue a
// merge verdict. This keeps ValidateParsedReview's existing per-pass schema
// checks satisfied without any change to that validation code: each shard's
// ChangedFiles is simply its own Files slice.
func BuildPRShardPrompt(ctx *PRContext, shard diffsignal.Shard, index, total int, primary bool) string {
	ctx = promptPRContext(ctx)
	if ctx == nil || ctx.PR == nil {
		return ""
	}
	var sb strings.Builder

	fmt.Fprintf(&sb, "## Pull Request (shard %d of %d)\n\n", index+1, total)
	fmt.Fprintf(&sb, "- **#%d**: %s\n", ctx.PR.Number, ctx.PR.Title)
	fmt.Fprintf(&sb, "- **Author**: @%s\n", ctx.PR.Author)
	if ctx.PR.Repository != "" {
		fmt.Fprintf(&sb, "- **Repository**: %s\n", qualifiedPRRepository(ctx.PR.Host, ctx.PR.Repository))
	}
	fmt.Fprintf(&sb, "- **Head**: %s @ %s\n", ctx.PR.HeadBranch, displayPRRevision(ctx.PR.HeadSHA))
	fmt.Fprintf(&sb, "- **Base**: %s @ %s\n", ctx.PR.BaseBranch, displayPRRevision(ctx.PR.BaseSHA))
	fmt.Fprintf(&sb, "- **CI Status**: %s\n\n", ctx.PR.CIStatus)

	sb.WriteString("## Shard Scope\n\n")
	fmt.Fprintf(&sb, "This pull request is too large for one review pass and was split into %d shards. ", total)
	sb.WriteString("You are reviewing shard ")
	fmt.Fprintf(&sb, "%d of %d. Review ONLY the files listed below. ", index+1, total)
	sb.WriteString("Your Coverage ledger MUST list exactly these files, with no more and no fewer:\n\n")
	for _, f := range shard.Files {
		fmt.Fprintf(&sb, "- %s\n", f)
	}
	sb.WriteString("\n")

	appendReviewVerificationTargets(&sb, shard.Files, ctx.AgentsMD)

	if !primary {
		sb.WriteString("This is a secondary shard. A separate synthesis step, not you, issues the merge verdict ")
		sb.WriteString("for the whole pull request. Regardless of what you find:\n\n")
		sb.WriteString("- Set **Approved** to `NO` in the Verdict section.\n")
		sb.WriteString("- Use Grade `B` or worse; do not use Grade `A`, because Grade `A` implies a mergeable approval that only synthesis can grant.\n")
		sb.WriteString("- Set the Coverage **Feedback disposition** to `NONE_SUPPLIED`; feedback disposition is handled by the primary shard.\n\n")
	} else if ctx.HasReviewFeedback() {
		sb.WriteString("You are the primary shard. In addition to reviewing your files, disposition every supplied feedback item below ")
		sb.WriteString("using its exact Feedback ID.\n\n")
	}

	appendPRContextCurationNotice(&sb, ctx.ContextCuration)

	if ctx.PR.Body != "" {
		sb.WriteString("## PR Description\n\n")
		sb.WriteString(ctx.PR.Body)
		sb.WriteString("\n\n")
	}

	if len(ctx.Checks) > 0 {
		sb.WriteString("## CI Checks\n\n")
		for _, c := range ctx.Checks {
			status := c.Conclusion
			if status == "" {
				status = c.Status
			}
			fmt.Fprintf(&sb, "- %s: %s\n", c.Name, status)
		}
		sb.WriteString("\n")
	}

	if primary {
		feedbackIDs := ctx.feedbackIDs()
		if len(ctx.Comments) > 0 {
			sb.WriteString("## Top-Level PR Comments\n\n")
			for i, c := range ctx.Comments {
				fmt.Fprintf(&sb, "**@%s** — Feedback ID: `%s`:\n%s\n\n", c.Author, feedbackIDs.Comments[i], c.Body)
			}
		}
		if len(ctx.Reviews) > 0 {
			sb.WriteString("## Submitted Reviews\n\n")
			for i, review := range ctx.Reviews {
				fmt.Fprintf(&sb, "**@%s** — %s — Feedback ID: `%s`:\n%s\n\n",
					review.Author, displayPRValue(review.State), feedbackIDs.Reviews[i], review.Body)
			}
		}
		if len(ctx.InlineComments) > 0 {
			sb.WriteString("## Inline Review Threads\n\n")
			for i, comment := range ctx.InlineComments {
				fmt.Fprintf(&sb, "**@%s** — Feedback ID: `%s`:\n%s\n\n", comment.Author, feedbackIDs.InlineComments[i], comment.Body)
			}
		}
	}

	appendPRContextProviderEvidence(&sb, prContextEvidenceForShard(ctx.ProviderEvidence, shard.Files, primary))

	if ctx.AgentsMD != "" {
		sb.WriteString("## Project Guidelines (applicable AGENTS.md chain)\n\n")
		sb.WriteString(ctx.AgentsMD)
		sb.WriteString("\n\n")
	}

	sb.WriteString("## Diff (this shard only)\n\n")
	sb.WriteString("```diff\n")
	sb.WriteString(shard.Context)
	sb.WriteString("\n```\n")

	return sb.String()
}

// ShardCostProjection is the cost/size estimate a caller logs and checks
// against a budget before fanning out, so an oversized swarm is refused
// with a number rather than silently run.
type ShardCostProjection struct {
	ShardCount              int
	HighSignalBytes         int
	HighSignalFiles         int
	EstimatedTokensPerShard int
	EstimatedTotalTokens    int
	EstimatedTotalCostUSD   float64
}

// ProjectShardCost estimates the token and dollar cost of reviewing every
// shard, before running any of them. costPerMillionTokens should reflect
// the configured review model's blended rate; 0 skips the dollar estimate
// (ShardCostProjection.EstimatedTotalCostUSD stays 0) while still reporting
// shard count and token estimates.
func ProjectShardCost(shards diffsignal.ShardResult, costPerMillionTokens float64) ShardCostProjection {
	return ProjectShardCostWithContext(shards, nil, costPerMillionTokens)
}

// ProjectShardCostWithContext includes the exact projected user prompt for
// every shard. This accounts for shared metadata and curated deterministic
// evidence that the legacy diff-only projection intentionally cannot see.
func ProjectShardCostWithContext(
	shards diffsignal.ShardResult,
	ctx *PRContext,
	costPerMillionTokens float64,
) ShardCostProjection {
	projection := ShardCostProjection{
		ShardCount:      len(shards.Shards),
		HighSignalBytes: shards.HighSignalBytes,
		HighSignalFiles: shards.HighSignalFiles,
	}
	for index, shard := range shards.Shards {
		prompt := shard.Context
		if ctx != nil {
			prompt = BuildPRShardPrompt(ctx, shard, index, len(shards.Shards), index == 0)
		}
		projection.EstimatedTotalTokens += reviewEstimateTokens(prompt)
	}
	if len(shards.Shards) > 0 {
		projection.EstimatedTokensPerShard = projection.EstimatedTotalTokens / len(shards.Shards)
	}
	if costPerMillionTokens > 0 {
		// Reviews read the diff and write a structured report; output tokens
		// are typically much smaller than input, so this multiplies input
		// tokens by 1.25 as a conservative round-trip estimate rather than
		// pretending to model exact output length.
		projection.EstimatedTotalCostUSD = float64(projection.EstimatedTotalTokens) * 1.25 / 1_000_000 * costPerMillionTokens
	}
	return projection
}

// ShardRunFunc runs one shard through a review pass and returns its
// completed result. Callers (the CLI) implement this against the RLM
// framework; RunPRShardsConcurrently is framework-agnostic so it can be
// tested with a fake implementation.
type ShardRunFunc func(ctx context.Context, shard diffsignal.Shard, index int) (*ParsedReview, error)

// RunPRShardsConcurrently runs every shard through run, bounding how many
// run concurrently to concurrency (at least 1). Shard count itself is never
// bounded here: a queue of any length is fully drained, just serialized
// through the worker limit, so wall time (not coverage) scales with PR
// size. The first error cancels the remaining in-flight work and is
// returned; results for shards that completed before the first error are
// discarded (the caller should not synthesize a partial review).
func RunPRShardsConcurrently(ctx context.Context, shards []diffsignal.Shard, concurrency int, run ShardRunFunc) ([]ShardReview, error) {
	if concurrency <= 0 {
		concurrency = 1
	}
	results := make([]ShardReview, len(shards))
	errs := make([]error, len(shards))

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, shard := range shards {
		select {
		case <-runCtx.Done():
			errs[i] = runCtx.Err()
			continue
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(i int, shard diffsignal.Shard) {
			defer wg.Done()
			defer func() { <-sem }()
			review, err := run(runCtx, shard, i)
			if err != nil {
				errs[i] = err
				cancel()
				return
			}
			results[i] = ShardReview{ShardIndex: i, Files: shard.Files, Review: review}
		}(i, shard)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			return nil, fmt.Errorf("shard %d of %d failed: %w", i+1, len(shards), err)
		}
	}
	return results, nil
}

// MergeShardedPRReview reduces every shard's completed review into one final
// review, in a tree bounded by fanIn per node so a synthesis step's input
// never grows unbounded with shard count. lowSignal and allChangedFiles
// supply the honesty guarantee: any changed file not covered by any shard's
// coverage ledger is added as an explicit "not reviewed" entry with its
// classification reason, so the final coverage ledger always unions to
// exactly allChangedFiles — never a sample of it.
func MergeShardedPRReview(shardReviews []ShardReview, lowSignal []diffsignal.FileDiff, allChangedFiles []string, fanIn int) (*ParsedReview, string) {
	if fanIn <= 0 {
		fanIn = DefaultSynthesisFanIn
	}
	prepared := make([]*ParsedReview, 0, len(shardReviews))
	for _, sr := range shardReviews {
		if sr.Review == nil {
			continue
		}
		prepared = append(prepared, prepareShardReviewForMerge(sr.ShardIndex, sr.Review))
	}

	merged := reduceParsedReviews(prepared, fanIn)
	injectNotReviewedEntries(merged, lowSignal, allChangedFiles)
	recomputeBlockersAndSuggestions(merged)
	rendered := renderMergedPRReview(merged)
	merged.RawReview = rendered
	return merged, rendered
}

// prepareShardReviewForMerge renumbers one shard's finding IDs with a
// shard-scoped prefix so they stay globally unique once every shard's
// findings land in the same merged set. This happens exactly once, at the
// leaves, before any reduction: intermediate merges never renumber again.
func prepareShardReviewForMerge(shardIndex int, review *ParsedReview) *ParsedReview {
	clone := *review
	clone.Findings = make([]Finding, len(review.Findings))
	for i, f := range review.Findings {
		f.ID = fmt.Sprintf("S%d-%s", shardIndex+1, f.ID)
		clone.Findings[i] = f
	}
	return &clone
}

// reduceParsedReviews merges reviews in rounds of at most fanIn per node
// until one remains. Each round's output count is ceil(len(reviews)/fanIn),
// so len(reviews) > fanIn always produces at least two rounds (levels).
func reduceParsedReviews(reviews []*ParsedReview, fanIn int) *ParsedReview {
	if len(reviews) == 0 {
		return &ParsedReview{FeedbackDisposition: FeedbackNoneSupplied, FalsificationConclusion: FalsificationDisproved, Grade: GradeA, Approved: true}
	}
	for len(reviews) > 1 {
		var next []*ParsedReview
		for i := 0; i < len(reviews); i += fanIn {
			end := i + fanIn
			if end > len(reviews) {
				end = len(reviews)
			}
			next = append(next, mergeParsedReviews(reviews[i:end]))
		}
		reviews = next
	}
	return reviews[0]
}

// mergeParsedReviews deterministically combines a group of ParsedReviews
// (leaf shard reviews, or intermediate synthesis results) into one. It is
// associative and commutative in every field that matters for correctness
// (coverage-ledger union, finding set, grade, approval), so reducing in any
// grouping or order yields the same final result. No finding is ever
// dropped to save space here: only exact-duplicate findings (same file,
// severity, and title, most often the same shard's report reappearing at a
// higher reduction level) collapse into one.
func mergeParsedReviews(reviews []*ParsedReview) *ParsedReview {
	merged := &ParsedReview{}
	if len(reviews) == 1 {
		clone := *reviews[0]
		clone.Findings = append([]Finding(nil), reviews[0].Findings...)
		clone.CoverageEntries = append([]CoverageEntry(nil), reviews[0].CoverageEntries...)
		clone.FeedbackEntries = append([]FeedbackEntry(nil), reviews[0].FeedbackEntries...)
		clone.Remarks = append([]string(nil), reviews[0].Remarks...)
		return &clone
	}

	var summaries, invariants, falsifications []string
	remarkSeen := map[string]bool{}
	coverageSeen := map[string]CoverageEntry{}
	var coverageOrder []string
	findingSeen := map[string]bool{}
	feedbackSeen := map[string]FeedbackEntry{}
	var feedbackOrder []string
	var buildStates, testStates []string

	worstGrade := GradeA
	allApproved := true
	dispositioned := false
	worstFalsification := FalsificationDisproved

	for _, r := range reviews {
		if r == nil {
			continue
		}
		if gradeRank(r.Grade) > gradeRank(worstGrade) {
			worstGrade = r.Grade
		}
		if !r.Approved {
			allApproved = false
		}
		if falsificationRank(r.FalsificationConclusion) > falsificationRank(worstFalsification) {
			worstFalsification = r.FalsificationConclusion
		}
		if r.FeedbackDisposition == FeedbackDispositioned {
			dispositioned = true
		}
		if strings.TrimSpace(r.Summary) != "" {
			summaries = append(summaries, r.Summary)
		}
		if strings.TrimSpace(r.InvariantAudit) != "" {
			invariants = append(invariants, r.InvariantAudit)
		}
		if strings.TrimSpace(r.Falsification) != "" {
			falsifications = append(falsifications, r.Falsification)
		}
		if strings.TrimSpace(r.BuildStatus) != "" {
			buildStates = append(buildStates, r.BuildStatus)
		}
		if strings.TrimSpace(r.TestStatus) != "" {
			testStates = append(testStates, r.TestStatus)
		}
		for _, remark := range r.Remarks {
			if !remarkSeen[remark] {
				remarkSeen[remark] = true
				merged.Remarks = append(merged.Remarks, remark)
			}
		}
		for _, entry := range r.CoverageEntries {
			key := normalizeCoveragePath(entry.Path)
			if key == "" {
				continue
			}
			if _, exists := coverageSeen[key]; !exists {
				coverageSeen[key] = entry
				coverageOrder = append(coverageOrder, key)
			}
		}
		for _, f := range r.Findings {
			key := findingDedupKey(f)
			if findingSeen[key] {
				continue
			}
			findingSeen[key] = true
			merged.Findings = append(merged.Findings, f)
		}
		for _, entry := range r.FeedbackEntries {
			if entry.ID == "" {
				continue
			}
			if _, exists := feedbackSeen[entry.ID]; !exists {
				feedbackSeen[entry.ID] = entry
				feedbackOrder = append(feedbackOrder, entry.ID)
			}
		}
	}

	for _, key := range coverageOrder {
		merged.CoverageEntries = append(merged.CoverageEntries, coverageSeen[key])
	}
	for _, id := range feedbackOrder {
		merged.FeedbackEntries = append(merged.FeedbackEntries, feedbackSeen[id])
	}

	merged.Grade = worstGrade
	merged.FalsificationConclusion = worstFalsification
	merged.Approved = allApproved && worstFalsification == FalsificationDisproved && !hasBlockingFinding(merged.Findings)
	merged.Summary = strings.Join(summaries, "\n\n")
	merged.InvariantAudit = strings.Join(invariants, "\n\n")
	merged.Falsification = strings.Join(falsifications, "\n\n")
	merged.BuildStatus = worstVerificationText(buildStates)
	merged.TestStatus = worstVerificationText(testStates)
	merged.BuildVerification = parseVerificationState(merged.BuildStatus)
	merged.TestVerification = parseVerificationState(merged.TestStatus)
	if dispositioned {
		merged.FeedbackDisposition = FeedbackDispositioned
	} else {
		merged.FeedbackDisposition = FeedbackNoneSupplied
	}
	return merged
}

// injectNotReviewedEntries adds an explicit, harness-generated coverage
// entry for every changed file no shard's ledger accounted for: low-signal
// files (with their diffsignal.Reason) and, defensively, any file that ended
// up in neither a shard nor the low-signal set (for example, a metadata
// mismatch between the files-list API and the diff source). This is what
// guarantees the final ledger always unions to exactly allChangedFiles.
func injectNotReviewedEntries(merged *ParsedReview, lowSignal []diffsignal.FileDiff, allChangedFiles []string) {
	accounted := make(map[string]bool, len(merged.CoverageEntries))
	for _, entry := range merged.CoverageEntries {
		if key := normalizeCoveragePath(entry.Path); key != "" {
			accounted[key] = true
		}
	}
	reasonByPath := make(map[string]string, len(lowSignal))
	for _, f := range lowSignal {
		if key := normalizeCoveragePath(f.Path); key != "" {
			reasonByPath[key] = string(f.Reason)
		}
	}
	for _, changed := range allChangedFiles {
		key := normalizeCoveragePath(changed)
		if key == "" || accounted[key] {
			continue
		}
		reason := reasonByPath[key]
		if reason == "" {
			reason = "file content was not available to any review pass"
		}
		merged.CoverageEntries = append(merged.CoverageEntries, CoverageEntry{
			Path:     changed,
			Evidence: "not reviewed: " + reason,
		})
		accounted[key] = true
	}
}

// recomputeBlockersAndSuggestions derives the merged Verdict's Blockers and
// Suggestions from the merged Findings rather than trusting any shard's own
// lists, since finding IDs were renumbered during the merge.
func recomputeBlockersAndSuggestions(merged *ParsedReview) {
	merged.Blockers = nil
	merged.Suggestions = nil
	for _, f := range merged.Findings {
		switch f.Severity {
		case SeverityCritical, SeverityMajor:
			merged.Blockers = append(merged.Blockers, f.ID)
		case SeverityMinor:
			merged.Suggestions = append(merged.Suggestions, f.ID)
		}
	}
}

func findingDedupKey(f Finding) string {
	return strings.ToLower(strings.TrimSpace(f.File)) + "\x00" +
		string(f.Severity) + "\x00" +
		strings.ToLower(strings.TrimSpace(f.Title))
}

func gradeRank(g Grade) int {
	switch g {
	case GradeA:
		return 0
	case GradeB:
		return 1
	case GradeC:
		return 2
	case GradeD:
		return 3
	case GradeF:
		return 4
	default:
		// A missing/unparseable grade is treated as the worst grade rather
		// than silently defaulting to a passing one.
		return 4
	}
}

func falsificationRank(c FalsificationConclusion) int {
	switch c {
	case FalsificationDisproved:
		return 0
	case FalsificationUnresolved:
		return 1
	case FalsificationProved:
		return 2
	default:
		return 1
	}
}

func hasBlockingFinding(findings []Finding) bool {
	for _, f := range findings {
		if f.Severity == SeverityCritical || f.Severity == SeverityMajor {
			return true
		}
	}
	return false
}

// verificationRank orders VerificationState from best to worst for
// worst-of aggregation across shards; PASS is the only state better than
// treating the merged status as unresolved.
func verificationRank(state VerificationState) int {
	switch state {
	case VerificationPass:
		return 0
	case VerificationPending:
		return 1
	case VerificationNotRun:
		return 1
	case VerificationUnknown:
		return 2
	case VerificationUnavailable:
		return 3
	case VerificationFail:
		return 4
	default:
		return 4
	}
}

// worstVerificationText returns the raw status text whose parsed
// VerificationState ranks worst among states, defaulting to "PASS" only
// when no shard reported any status at all (nothing to aggregate).
func worstVerificationText(states []string) string {
	if len(states) == 0 {
		return "PASS"
	}
	worst := states[0]
	worstRank := verificationRank(parseVerificationState(states[0]))
	for _, s := range states[1:] {
		if rank := verificationRank(parseVerificationState(s)); rank > worstRank {
			worst = s
			worstRank = rank
		}
	}
	return worst
}

// renderMergedPRReview renders the final merged ParsedReview back to the
// review markdown schema, for display and for posting to GitHub.
func renderMergedPRReview(r *ParsedReview) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "## Grade: %s\n\n", r.Grade)

	sb.WriteString("## Summary\n\n")
	sb.WriteString(strings.TrimSpace(r.Summary))
	sb.WriteString("\n\n")

	sb.WriteString("## Build & Test Status\n\n")
	fmt.Fprintf(&sb, "- Build: %s\n", r.BuildStatus)
	fmt.Fprintf(&sb, "- Tests: %s\n\n", r.TestStatus)

	sb.WriteString("## Coverage\n\n")
	for _, e := range r.CoverageEntries {
		fmt.Fprintf(&sb, "- **File**: `%s` — %s\n", e.Path, e.Evidence)
	}
	fmt.Fprintf(&sb, "- **Feedback disposition**: `%s`\n", r.FeedbackDisposition)
	for _, e := range r.FeedbackEntries {
		fmt.Fprintf(&sb, "- **Feedback**: `%s` `%s` — %s\n", e.ID, e.Status, e.Evidence)
	}
	sb.WriteString("\n")

	sb.WriteString("## Invariant Audit\n\n")
	sb.WriteString(strings.TrimSpace(r.InvariantAudit))
	sb.WriteString("\n\n")

	sb.WriteString("## Falsification\n\n")
	sb.WriteString(strings.TrimSpace(r.Falsification))
	fmt.Fprintf(&sb, "\n- **Conclusion**: %s\n\n", r.FalsificationConclusion)

	sb.WriteString("## Findings\n\n")
	for _, f := range r.Findings {
		fmt.Fprintf(&sb, "### %s: %s %s\n", f.ID, f.Severity, f.Title)
		if f.File != "" {
			fmt.Fprintf(&sb, "- **File**: %s", f.File)
			if f.Line > 0 {
				fmt.Fprintf(&sb, ":%d", f.Line)
			}
			sb.WriteString("\n")
		}
		if f.Evidence != "" {
			fmt.Fprintf(&sb, "- **Evidence**: %s\n", f.Evidence)
		}
		if f.Impact != "" {
			fmt.Fprintf(&sb, "- **Impact**: %s\n", f.Impact)
		}
		if f.Fix != "" {
			fmt.Fprintf(&sb, "- **Fix**: %s\n", f.Fix)
		}
		sb.WriteString("\n")
	}

	if len(r.Remarks) > 0 {
		sb.WriteString("## Remarks\n\n")
		for _, remark := range r.Remarks {
			fmt.Fprintf(&sb, "- %s\n", remark)
		}
		sb.WriteString("\n")
	}

	sb.WriteString("## Verdict\n\n")
	approval := "NO"
	if r.Approved {
		approval = "YES"
	}
	fmt.Fprintf(&sb, "- **Approved**: %s\n", approval)
	if len(r.Blockers) > 0 {
		fmt.Fprintf(&sb, "- **Blockers**: %s\n", strings.Join(r.Blockers, ", "))
	} else {
		sb.WriteString("- **Blockers**: NONE\n")
	}
	if len(r.Suggestions) > 0 {
		fmt.Fprintf(&sb, "- **Suggestions**: %s\n", strings.Join(r.Suggestions, ", "))
	} else {
		sb.WriteString("- **Suggestions**: NONE\n")
	}

	return sb.String()
}
