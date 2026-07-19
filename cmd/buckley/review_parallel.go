package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/oneshot/commands"
	"m31labs.dev/buckley/pkg/paths"
	"m31labs.dev/buckley/pkg/transparency"
)

// synthesisTimeout is a dedicated deadline for the reduce step so a long,
// expensive bundle phase can never starve it. It is generous because the
// synthesizer reasons over the concatenated bundle reviews (tens of KB) at the
// review reasoning effort; if it still overruns, the bundle reviews are returned
// verbatim rather than lost.
const synthesisTimeout = 15 * time.Minute

// Per-run tool-loop caps. Bundle reviewers are scoped to a file slice, so a
// lower cap bounds cost without much coverage loss; the single/branch path keeps
// the full budget.
const (
	reviewBundleMaxIterations = 18
	reviewSingleMaxIterations = 25
)

// parallelReviewLineThreshold is the changed-line count above which a branch
// review fans out into parallel bundle reviewers instead of one agent. Project
// reviews always fan out (they are inherently large).
const parallelReviewLineThreshold = 1200

// reviewSynthesisDef is the "reduce" step: a tool-less agent that groups and
// deduplicates the parallel bundle reviews into one prioritized report.
//
// IMPORTANT: an EMPTY AllowedTools() would grant ALL tools, not none, so we
// return a sentinel name that matches no registered tool. Combined with running
// it WITHOUT a review snapshot, the subagent ends up with an empty registry and
// tool_choice=none — pure text synthesis.
type reviewSynthesisDef struct{}

func (reviewSynthesisDef) Name() string { return "review-synthesis" }

func (reviewSynthesisDef) SystemPrompt() string {
	return "You are a staff engineer synthesizing several focused sub-reviews of different parts of ONE codebase into a single, coherent project review. " +
		"Each sub-review already covers a distinct slice of the repo. Your job is the REDUCE step: merge and DEDUPLICATE overlapping findings, resolve contradictions, and rank by real impact. " +
		"Preserve concrete file:line evidence from the sub-reviews; never invent findings that are not present in them. " +
		"Produce exactly these sections:\n" +
		"## Project Status\n## Top Action Items (each: [High|Medium|Low] title, why it matters, file paths, effort)\n## Cross-Cutting Risks\n## Quick Wins\n" +
		"Be specific and actionable. This is an advisory review, not a merge gate — do not issue an approval verdict."
}

// AllowedTools returns a sentinel that matches no registered tool so the
// synthesizer runs tool-free (see type doc).
func (reviewSynthesisDef) AllowedTools() []string { return []string{"__synthesis_no_tools__"} }

func (reviewSynthesisDef) ParseResult(response string) (any, error) {
	return &commands.ReviewRLMResult{Review: response, Parsed: commands.ParseReview(response)}, nil
}

// trackedSourceFiles lists the repo's tracked files at HEAD, minus generated /
// data directories that add noise and cost without review value.
func trackedSourceFiles(root string) ([]string, error) {
	out, err := exec.Command("git", "-C", root, "ls-tree", "-r", "--name-only", "HEAD").Output()
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !isNonReviewPath(line) {
			files = append(files, line)
		}
	}
	return files, nil
}

// isNonReviewPath skips files under directories that are generated, vendored, or
// test fixtures — not worth spending review tokens on.
func isNonReviewPath(p string) bool {
	skip := map[string]bool{
		"testdata": true, "vendor": true, "node_modules": true, "corpus_sources": true,
		"corpus": true, "fixtures": true, "dist": true, "build": true, ".git": true,
		"harness_out": true, "tmp": true, ".idea": true, ".vscode": true,
	}
	segs := strings.Split(p, "/")
	for _, s := range segs[:len(segs)-1] { // directory segments only
		if skip[s] {
			return true
		}
	}
	return false
}

// projectBundleCount picks how many bundles to split a project into: ~1 per 250
// files, clamped to [2, 8] to bound cost.
func projectBundleCount(fileCount int) int {
	b := fileCount / 250
	if b < 2 {
		b = 2
	}
	if b > 8 {
		b = 8
	}
	return b
}

// bundlePaths partitions paths into up to maxBundles contiguous, size-balanced
// buckets. Sorting first keeps same-directory files together so each bundle is a
// coherent slice of the tree. Deterministic.
func bundlePaths(paths []string, maxBundles int) [][]string {
	if maxBundles < 1 {
		maxBundles = 1
	}
	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)
	n := len(sorted)
	if n == 0 {
		return nil
	}
	if maxBundles > n {
		maxBundles = n
	}
	bundles := make([][]string, 0, maxBundles)
	base, rem, idx := n/maxBundles, n%maxBundles, 0
	for i := 0; i < maxBundles; i++ {
		size := base
		if i < rem {
			size++
		}
		bundles = append(bundles, sorted[idx:idx+size])
		idx += size
	}
	return bundles
}

// runReviewBundlesParallel runs each bundle prompt through def with bounded
// concurrency, sharing the (immutable) snapshot. Safe: snapshot reviews get a
// fresh per-call registry+workspace, and the cost ledger is mutex-guarded.
func runReviewBundlesParallel(ctx context.Context, framework *oneshot.Framework, def oneshot.RLMDefinition, snapshot *model.ReviewSnapshot, policy model.ReviewSnapshotPolicy, prompts []string, concurrency int) []string {
	if concurrency < 1 {
		concurrency = 1
	}
	results := make([]string, len(prompts))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, p := range prompts {
		wg.Add(1)
		go func(idx int, prompt string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			fwResult, err := framework.RunRLM(ctx, def, oneshot.RLMRunOpts{
				UserPrompt:     prompt,
				SnapshotPolicy: policy,
				ReviewSnapshot: snapshot,
			})
			if err != nil {
				results[idx] = fmt.Sprintf("(bundle %d review failed: %v)", idx+1, err)
				return
			}
			if r, ok := fwResult.Value.(*commands.ReviewRLMResult); ok {
				results[idx] = r.Review
			}
		}(i, p)
	}
	wg.Wait()
	return results
}

// synthesizeBundleReviews runs the tool-less reduce step over the bundle reviews.
func synthesizeBundleReviews(ctx context.Context, framework *oneshot.Framework, reviews []string, audit *transparency.ContextAudit) (*oneshot.RunResult, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Below are %d focused sub-reviews, each covering a different slice of the same project. Merge and deduplicate them into ONE prioritized project review.\n", len(reviews))
	for i, r := range reviews {
		if strings.TrimSpace(r) == "" {
			continue
		}
		fmt.Fprintf(&b, "\n===== SUB-REVIEW %d =====\n%s\n", i+1, r)
	}
	return framework.RunRLM(ctx, reviewSynthesisDef{}, oneshot.RLMRunOpts{
		UserPrompt: b.String(),
		Audit:      audit,
	})
}

// runParallelProjectReview partitions the project into file bundles, reviews
// them in parallel, then synthesizes — the map-reduce path.
func runParallelProjectReview(ctx context.Context, framework *oneshot.Framework, snapshot *model.ReviewSnapshot, policy model.ReviewSnapshotPolicy, projectCtx *commands.ProjectContext, audit *transparency.ContextAudit, concurrency int) (*reviewCommandResult, error) {
	files, err := trackedSourceFiles(projectCtx.RepoRoot)
	if err != nil || len(files) == 0 {
		// Fall back to the single-agent path if we can't enumerate files.
		fwResult, runErr := framework.RunRLM(ctx, commands.ReviewProjectDef{}, oneshot.RLMRunOpts{
			UserPrompt:     commands.BuildProjectPrompt(projectCtx),
			Audit:          audit,
			SnapshotPolicy: policy,
			ReviewSnapshot: snapshot,
		})
		if runErr != nil {
			return nil, runErr
		}
		return reviewResultFromRLM(fwResult, audit), nil
	}

	bundles := bundlePaths(files, projectBundleCount(len(files)))
	prompts := make([]string, len(bundles))
	for i, b := range bundles {
		prompts[i] = buildBundleProjectPrompt(projectCtx, b)
	}
	reviews := runReviewBundlesParallel(ctx, framework, commands.ReviewProjectDef{}, snapshot, policy, prompts, concurrency)

	// Persist the (expensive) bundle reviews to disk immediately, BEFORE the
	// synthesis step, so a synthesis failure/timeout can never discard them.
	partsDir := persistBundleReviews(reviews)

	// Give synthesis its own fresh deadline, detached from the bundle-phase
	// deadline, so a long map phase can't starve the reduce.
	synthCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), synthesisTimeout)
	defer cancel()

	fwResult, err := synthesizeBundleReviews(synthCtx, framework, reviews, audit)
	if err != nil {
		// Synthesis failed, but the bundle reviews succeeded — deliver them
		// concatenated rather than waste the work.
		fmt.Fprintf(os.Stderr, "⚠ synthesis step failed (%v); returning the %d bundle reviews unsynthesized%s\n",
			err, len(reviews), partsDirNote(partsDir))
		return &reviewCommandResult{
			reviewText:   fallbackBundleReport(reviews, partsDir),
			contextAudit: audit,
		}, nil
	}
	return reviewResultFromRLM(fwResult, audit), nil
}

// persistBundleReviews writes each bundle review to <logs>/review-bundles/ so
// the expensive map-phase output survives a later failure. Best-effort; returns
// the directory (empty on failure).
func persistBundleReviews(reviews []string) string {
	dir := filepath.Join(paths.BuckleyLogsBaseDir(), "review-bundles")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	for i, r := range reviews {
		_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("bundle-%02d.md", i+1)), []byte(r), 0o644)
	}
	return dir
}

func partsDirNote(dir string) string {
	if dir == "" {
		return ""
	}
	return " (also saved to " + dir + ")"
}

// fallbackBundleReport concatenates the raw bundle reviews when synthesis fails.
func fallbackBundleReport(reviews []string, partsDir string) string {
	var b strings.Builder
	b.WriteString("# Project Review (unsynthesized bundle reviews)\n\n")
	b.WriteString("_Synthesis did not complete; the individual bundle reviews are included verbatim below")
	b.WriteString(partsDirNote(partsDir))
	b.WriteString("._\n")
	for i, r := range reviews {
		if strings.TrimSpace(r) == "" {
			continue
		}
		fmt.Fprintf(&b, "\n\n---\n## Bundle %d\n\n%s\n", i+1, r)
	}
	return b.String()
}

// buildBundleProjectPrompt scopes a bundle reviewer to a subset of files. It is
// deliberately LEAN: the full project context (whole tree + README) is NOT
// re-sent to every bundle — the agent reads the actual files via tools, so
// repeating heavy context 6× only burns fresh tokens for little value.
func buildBundleProjectPrompt(projectCtx *commands.ProjectContext, files []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are reviewing ONE slice of the project at %s (branch %s, commit %s), in parallel with other reviewers covering other slices.\n",
		projectCtx.RepoRoot, projectCtx.Branch, shortCommit(projectCtx.HeadCommit))
	if mod := firstLines(projectCtx.GoMod, 3); mod != "" {
		fmt.Fprintf(&b, "\ngo.mod (head):\n%s\n", mod)
	}
	b.WriteString("\nReview ONLY the files listed below. Use read_file/find_files/search_text to read them; do NOT review files outside this list. ")
	b.WriteString("You have a LIMITED tool budget: read the most important files first (entry points, largest/most-complex files, anything suspicious), then STOP exploring and WRITE your review — do not try to open every file. ")
	b.WriteString("Deliver concrete issues with file:line evidence, correctness/performance/maintainability risks, and prioritized action items (High/Medium/Low + effort). Find real problems; do not summarize what the code does.\n\nFILES:\n")
	for _, f := range files {
		b.WriteString("- ")
		b.WriteString(f)
		b.WriteString("\n")
	}
	return b.String()
}

func shortCommit(c string) string {
	c = strings.TrimSpace(c)
	if len(c) > 10 {
		return c[:10]
	}
	return c
}

func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
