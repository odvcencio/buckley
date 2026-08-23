package pr

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/draco/buckley/pkg/transparency"
)

// Context contains all information needed for PR generation.
type Context struct {
	// Git information
	Branch      string
	BaseBranch  string
	RepoRoot    string
	RemoteURL   string
	Commits     []CommitInfo
	DiffSummary string
	FullDiff    string

	// Stats
	Stats DiffStats

	// Project context
	AgentsMD string
}

// CommitInfo represents a single commit in the branch.
type CommitInfo struct {
	Hash    string
	Subject string
	Body    string
}

// DiffStats contains diff statistics.
type DiffStats struct {
	Files       int
	Insertions  int
	Deletions   int
	BinaryFiles int
}

// TotalChanges returns insertions + deletions.
func (ds DiffStats) TotalChanges() int {
	return ds.Insertions + ds.Deletions
}

// ContextOptions configures context assembly.
type ContextOptions struct {
	BaseBranch    string
	MaxDiffBytes  int
	MaxDiffTokens int
	IncludeAgents bool
}

// DefaultContextOptions returns sensible defaults.
func DefaultContextOptions() ContextOptions {
	return ContextOptions{
		BaseBranch:    "", // Auto-detect
		MaxDiffBytes:  80_000,
		MaxDiffTokens: 20_000,
		IncludeAgents: true,
	}
}

// AssembleContext gathers all context needed for PR generation.
func AssembleContext(opts ContextOptions) (*Context, *transparency.ContextAudit, error) {
	audit := transparency.NewContextAudit()
	ctx := &Context{}

	// Get repo root
	root, err := gitOutput("rev-parse", "--show-toplevel")
	if err != nil {
		return nil, nil, fmt.Errorf("not in a git repository: %w", err)
	}
	ctx.RepoRoot = strings.TrimSpace(root)

	// Get current branch
	branch, err := gitOutput("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get current branch: %w", err)
	}
	ctx.Branch = strings.TrimSpace(branch)
	audit.Add("branch", estimateTokens(ctx.Branch))

	// Get base branch
	if opts.BaseBranch != "" {
		ctx.BaseBranch = opts.BaseBranch
	} else {
		ctx.BaseBranch = detectBaseBranch()
	}
	audit.Add("base branch", estimateTokens(ctx.BaseBranch))

	// Resolve the ref actually used for the diff/log plumbing below. This
	// prefers the "origin" remote-tracking branch (e.g. "origin/main")
	// over the bare local branch name so a stale local branch (behind
	// origin) can't balloon the diff/commit list with content that's
	// already merged upstream. ctx.BaseBranch itself is left as a plain
	// branch name since callers (e.g. `gh pr create --base`) need that,
	// not a remote-qualified ref.
	diffRef := resolveBaseRef(ctx.BaseBranch)

	// Get remote URL for context
	if remote, err := gitOutput("remote", "get-url", "origin"); err == nil {
		ctx.RemoteURL = strings.TrimSpace(remote)
	}

	// Get commits on this branch (since divergence from base)
	commits, err := getCommitsSinceBase(diffRef)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get commits: %w", err)
	}
	ctx.Commits = commits

	// Build commit summary
	var commitSummary strings.Builder
	for _, c := range commits {
		commitSummary.WriteString(c.Hash[:7])
		commitSummary.WriteString(" ")
		commitSummary.WriteString(c.Subject)
		commitSummary.WriteString("\n")
	}
	audit.Add("commits", estimateTokens(commitSummary.String()))

	// Get diff summary (--stat)
	diffStat, err := gitOutput("diff", "--stat", diffRef+"...HEAD")
	if err == nil {
		ctx.DiffSummary = diffStat
		audit.Add("diff summary", estimateTokens(diffStat))
	}

	// Get full diff. Generated/vendored/lockfile/binary files and large
	// data blobs are filtered out (see classifyDiffFile) and the
	// MaxDiffBytes budget is distributed fairly across the remaining
	// source files (see allocateDiffBudget), so a single file - no
	// matter where it happens to sort alphabetically - can never
	// dominate what the model sees.
	diffResult, err := buildDiffContext(diffRef, opts.MaxDiffBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get diff: %w", err)
	}
	ctx.FullDiff = diffResult.Text
	diffTokens := estimateTokens(diffResult.Text)
	if diffResult.Truncated {
		audit.AddTruncated("full diff", diffTokens, opts.MaxDiffTokens)
	} else {
		audit.Add("full diff", diffTokens)
	}
	if len(diffResult.Omitted) > 0 {
		audit.Add(fmt.Sprintf("omitted files (%d generated/data/binary)", len(diffResult.Omitted)), 0)
	}

	// Get stats
	ctx.Stats = getDiffStats(diffRef)

	// Load AGENTS.md if requested
	if opts.IncludeAgents {
		agentsPath := filepath.Join(ctx.RepoRoot, "AGENTS.md")
		if content, err := readFileLimited(agentsPath, 10_000); err == nil && content != "" {
			ctx.AgentsMD = content
			audit.Add("AGENTS.md", estimateTokens(content))
		}
	}

	return ctx, audit, nil
}

// detectBaseBranch attempts to find the default branch.
//
// Branches confirmed to exist on the "origin" remote are preferred over
// same-named local branches: a local branch can be stale (behind
// origin) without that being visible from its name alone, and diffing
// against a stale local branch makes the current branch look like it
// introduces far more changes than it actually does relative to
// upstream. Local branches are only consulted as a fallback when
// there's no usable "origin" signal (e.g. no remote configured, offline
// clone). See resolveBaseRef, which performs the equivalent
// origin-preferring resolution for the ref actually used to compute the
// diff/commit list.
func detectBaseBranch() string {
	// Try common default branches on origin first - this is the
	// canonical signal for the default branch since remote-tracking
	// refs are what `git fetch` keeps up to date.
	for _, branch := range []string{"main", "master", "develop"} {
		if _, err := gitOutput("rev-parse", "--verify", "origin/"+branch); err == nil {
			return branch
		}
	}
	// Try origin/HEAD, which reflects the remote's configured default branch.
	if ref, err := gitOutput("symbolic-ref", "refs/remotes/origin/HEAD"); err == nil {
		parts := strings.Split(strings.TrimSpace(ref), "/")
		if len(parts) > 0 {
			return parts[len(parts)-1]
		}
	}
	// No usable "origin" signal - fall back to whatever exists locally.
	for _, branch := range []string{"main", "master", "develop"} {
		if _, err := gitOutput("rev-parse", "--verify", branch); err == nil {
			return branch
		}
	}
	return "main" // Default fallback
}

// resolveBaseRef returns the git ref that should actually be used for
// diff/log against a given base branch name. It prefers the "origin"
// remote-tracking ref (e.g. "origin/main") when one exists, since that
// reflects the real upstream default branch; a local branch with the
// same name can be stale (e.g. not fetched/merged recently), which
// would otherwise make the diff include commits that are already
// merged upstream. Falls back to the bare branch name when no matching
// remote-tracking ref exists (no "origin" remote, or an explicit base
// that isn't a branch at all, e.g. a tag or commit SHA).
func resolveBaseRef(baseBranch string) string {
	baseBranch = strings.TrimSpace(baseBranch)
	if baseBranch == "" || strings.HasPrefix(baseBranch, "origin/") {
		return baseBranch
	}
	if _, err := gitOutput("rev-parse", "--verify", "origin/"+baseBranch); err == nil {
		return "origin/" + baseBranch
	}
	return baseBranch
}

// getCommitsSinceBase returns commits on current branch since divergence from base.
func getCommitsSinceBase(baseBranch string) ([]CommitInfo, error) {
	// Get commit log with format: hash<SEP>subject<SEP>body<END>
	format := "%H<SEP>%s<SEP>%b<END>"
	output, err := gitOutput("log", "--format="+format, baseBranch+"..HEAD")
	if err != nil {
		return nil, err
	}
	return ParseCommitLog(output), nil
}

// ParseCommitLog parses the output of 'git log --format="%H<SEP>%s<SEP>%b<END>"'.
// Exported for testing.
func ParseCommitLog(output string) []CommitInfo {
	var commits []CommitInfo
	entries := strings.Split(output, "<END>")
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, "<SEP>", 3)
		if len(parts) < 2 {
			continue
		}
		commit := CommitInfo{
			Hash:    parts[0],
			Subject: parts[1],
		}
		if len(parts) > 2 {
			commit.Body = strings.TrimSpace(parts[2])
		}
		commits = append(commits, commit)
	}
	return commits
}

// getDiffStats extracts diff statistics.
func getDiffStats(baseBranch string) DiffStats {
	output, err := gitOutput("diff", "--numstat", baseBranch+"...HEAD")
	if err != nil {
		return DiffStats{}
	}
	return ParseDiffNumstat(output)
}

// ParseDiffNumstat parses the output of 'git diff --numstat' into DiffStats.
// Exported for testing.
func ParseDiffNumstat(output string) DiffStats {
	var stats DiffStats
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			continue
		}
		stats.Files++

		ins, errIns := strconv.Atoi(parts[0])
		del, errDel := strconv.Atoi(parts[1])
		if errIns != nil || errDel != nil {
			stats.BinaryFiles++
			continue
		}
		stats.Insertions += ins
		stats.Deletions += del
	}
	return stats
}

// estimateTokens provides a rough token estimate.
func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return (len(s) + 3) / 4
}

// Diff filtering and budgeting.
//
// A single `git diff` can mix meaningful source changes with large,
// low-signal content: committed generated/vendored code, lockfiles, and
// large data blobs (fixtures, exported JSON/CSV, etc). Because git
// orders diff hunks alphabetically by path, a single such file sorting
// early can consume most (or all) of a raw byte-prefix budget, starving
// every source file that happens to sort after it - which is exactly
// what happened when a large committed generated JSON file
// (cgo_harness/perf_scan/perf_ratio_budgets.json) dominated a PR body
// generated from a diff that also touched several .go source files
// sorting later alphabetically.
//
// buildDiffContext addresses this in two steps: (1) filter out files
// that are very unlikely to be useful raw context for a PR description
// (classifyDiffFile), noting how many were omitted so the model still
// knows they exist and changed, and (2) distribute the remaining byte
// budget fairly across the surviving files (allocateDiffBudget) so no
// single file - regardless of sort order - can consume the whole
// budget.

const (
	// defaultPerFileDiffCapBytes bounds how many bytes of any single
	// file's diff can be included in the model context, independent of
	// the overall MaxDiffBytes budget. This is what prevents one large
	// (but legitimate source) file from crowding out every other file.
	defaultPerFileDiffCapBytes = 8_000

	// diffRoundRobinChunkBytes is the granularity used to fairly
	// distribute the overall diff budget across multiple files. See
	// allocateDiffBudget.
	diffRoundRobinChunkBytes = 2_000

	// largeDataFileThresholdBytes is the per-file diff size above which
	// a data-extension file (see dataFileExtensions) is treated as a
	// generated/data blob and omitted outright, rather than merely
	// capped. Small data-extension files (e.g. a slim package.json) are
	// left alone since they're commonly legitimate, hand-edited source.
	largeDataFileThresholdBytes = 4_000

	// maxOmittedFilesListed bounds how many omitted file paths are
	// listed by name in the note appended to the diff, so a change that
	// touches thousands of generated files doesn't itself blow the
	// budget.
	maxOmittedFilesListed = 15
)

// dataFileExtensions are extensions commonly used for machine-generated
// or exported data blobs (fixtures, snapshots, budgets/exports, etc.)
// rather than hand-authored source. They're only omitted when "large"
// (largeDataFileThresholdBytes), so small legitimate files with these
// extensions (e.g. a small package.json) are unaffected.
var dataFileExtensions = map[string]bool{
	".json":    true,
	".csv":     true,
	".tsv":     true,
	".jsonl":   true,
	".ndjson":  true,
	".parquet": true,
	".avro":    true,
	".sql":     true,
	".geojson": true,
}

// generatedPathMarkers are path substrings that mark a file as
// vendored, build output, or tool-generated code. These are excluded
// unconditionally (regardless of size) since they're never meaningful
// hand-authored PR content.
var generatedPathMarkers = []string{
	"/vendor/",
	"/node_modules/",
	"/dist/",
	"/build/",
	"/.next/",
	"/target/",
	"/.venv/",
	"/venv/",
	"/third_party/",
	"/generated/",
	"/.vitepress/dist/",
	"/coverage/",
}

// generatedFilenameMarkers are filename substrings that mark a file as
// tool-generated code, independent of its directory.
var generatedFilenameMarkers = []string{
	".pb.go",
	".pb.cc",
	".pb.h",
	"_pb2.py",
	".generated.",
	".min.js",
	".min.css",
	".map",
	"_generated.go",
	".gen.go",
}

// lockfileNames are well-known dependency lockfiles: large,
// machine-written, and never hand-authored.
var lockfileNames = map[string]bool{
	"go.sum":            true,
	"package-lock.json": true,
	"yarn.lock":         true,
	"pnpm-lock.yaml":    true,
	"composer.lock":     true,
	"Gemfile.lock":      true,
	"Cargo.lock":        true,
	"poetry.lock":       true,
	"Pipfile.lock":      true,
	"mix.lock":          true,
	"flake.lock":        true,
	"bun.lockb":         true,
}

// isGeneratedOrVendoredPath reports whether path matches a well-known
// lockfile, vendored/build-output directory, or generated-code filename
// pattern. Matches are excluded unconditionally, regardless of size.
func isGeneratedOrVendoredPath(path string) bool {
	if lockfileNames[filepath.Base(path)] {
		return true
	}

	lower := "/" + strings.ToLower(path)
	for _, marker := range generatedPathMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	for _, marker := range generatedFilenameMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// classifyDiffFile decides whether a file's diff should be omitted from
// the model context outright. omit is true if the path/size matches a
// generated/vendored/lockfile pattern or a large data-blob extension;
// reason is a short human-readable explanation used in the
// omitted-files note.
func classifyDiffFile(path string, sizeBytes int) (omit bool, reason string) {
	if isGeneratedOrVendoredPath(path) {
		return true, "generated/vendored/lockfile"
	}
	ext := strings.ToLower(filepath.Ext(path))
	if dataFileExtensions[ext] && sizeBytes > largeDataFileThresholdBytes {
		return true, "large data file"
	}
	return false, ""
}

// diffFileSegment is one file's worth of raw `git diff` output.
type diffFileSegment struct {
	Path    string
	Content string
	Binary  bool
}

// splitDiffByFile splits the raw output of `git diff` into per-file
// segments, in the order git emitted them (typically alphabetical by
// path). This allows each file's diff to be classified and budgeted
// independently instead of treating the whole diff as one opaque blob
// that's truncated by raw byte prefix.
func splitDiffByFile(raw string) []diffFileSegment {
	if raw == "" {
		return nil
	}

	const header = "diff --git "
	lines := strings.Split(raw, "\n")

	var segments []diffFileSegment
	var cur *diffFileSegment
	var body strings.Builder

	flush := func() {
		if cur != nil {
			cur.Content = body.String()
			segments = append(segments, *cur)
		}
		body.Reset()
		cur = nil
	}

	for _, line := range lines {
		if strings.HasPrefix(line, header) {
			flush()
			cur = &diffFileSegment{Path: parseDiffGitPath(line)}
		}
		if cur == nil {
			// Content preceding the first "diff --git" line shouldn't
			// normally occur in `git diff` output; skip defensively.
			continue
		}
		if body.Len() > 0 {
			body.WriteByte('\n')
		}
		body.WriteString(line)
		if strings.HasPrefix(line, "Binary files ") || strings.HasPrefix(line, "GIT binary patch") {
			cur.Binary = true
		}
	}
	flush()

	return segments
}

// parseDiffGitPath extracts the (post-image) file path from a
// `diff --git a/<path> b/<path>` header line.
func parseDiffGitPath(line string) string {
	rest := strings.TrimPrefix(line, "diff --git ")
	idx := strings.LastIndex(rest, " b/")
	if idx < 0 {
		return strings.TrimSpace(rest)
	}
	path := rest[idx+len(" b/"):]
	return strings.Trim(strings.TrimSpace(path), `"`)
}

// omittedDiffFile records a file whose diff content was left out of the
// model context entirely.
type omittedDiffFile struct {
	Path   string
	Reason string
	Bytes  int
}

// diffContextResult is the output of assembling a filtered,
// budget-bounded diff for the model.
type diffContextResult struct {
	Text      string
	Truncated bool
	Omitted   []omittedDiffFile
}

// buildDiffContext runs `git diff <baseRef>...HEAD` and processes it via
// processDiffContext (see its doc comment for the filtering/budgeting
// behavior).
func buildDiffContext(baseRef string, maxBytes int) (diffContextResult, error) {
	raw, err := gitOutput("diff", baseRef+"...HEAD")
	if err != nil {
		return diffContextResult{}, err
	}
	return processDiffContext(raw, maxBytes), nil
}

// processDiffContext is the pure (no git invocation) core of
// buildDiffContext: it filters generated/vendored/lockfile/binary and
// large data-blob files out of raw (the full output of `git diff`), then
// distributes maxBytes fairly across the remaining files so no single
// file can dominate the result. Split out from buildDiffContext so it
// can be exercised directly against synthetic `git diff` output.
func processDiffContext(raw string, maxBytes int) diffContextResult {
	if raw == "" {
		return diffContextResult{Text: raw}
	}
	if maxBytes <= 0 {
		return diffContextResult{Text: "", Truncated: true}
	}

	segments := splitDiffByFile(raw)
	if len(segments) == 0 {
		// Not parseable as per-file segments (e.g. unexpected diff
		// format) - fall back to the previous raw byte-prefix behavior
		// rather than silently dropping everything.
		if len(raw) > maxBytes {
			return diffContextResult{Text: raw[:maxBytes], Truncated: true}
		}
		return diffContextResult{Text: raw}
	}

	var kept []diffFileSegment
	var omitted []omittedDiffFile
	for _, seg := range segments {
		if seg.Binary {
			omitted = append(omitted, omittedDiffFile{Path: seg.Path, Reason: "binary file", Bytes: len(seg.Content)})
			continue
		}
		if omit, reason := classifyDiffFile(seg.Path, len(seg.Content)); omit {
			omitted = append(omitted, omittedDiffFile{Path: seg.Path, Reason: reason, Bytes: len(seg.Content)})
			continue
		}
		kept = append(kept, seg)
	}

	var note string
	budget := maxBytes
	if len(omitted) > 0 {
		note = formatOmittedNote(omitted)
		budget -= len(note) + 2 // blank-line separator before the note
		if budget < 0 {
			budget = 0
		}
	}

	sizes := make([]int, len(kept))
	for i, seg := range kept {
		sizes[i] = len(seg.Content)
	}
	alloc := allocateDiffBudget(sizes, defaultPerFileDiffCapBytes, diffRoundRobinChunkBytes, budget)

	var b strings.Builder
	truncated := len(omitted) > 0
	for i, seg := range kept {
		grant := alloc[i]
		if grant <= 0 {
			truncated = true
			continue
		}
		content := seg.Content
		if grant < len(content) {
			content = truncateDiffAtLine(content, grant)
			truncated = true
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(content)
	}

	if note != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(note)
	}

	return diffContextResult{Text: b.String(), Truncated: truncated, Omitted: omitted}
}

// allocateDiffBudget fairly distributes totalBudget bytes across
// len(sizes) files using round-robin ("water-filling") allocation: each
// file is first capped at perFileCap, then repeatedly granted up to
// chunkBytes at a time (cycling through files in order) until either
// every file's capped size has been fully granted or the total budget
// is exhausted. This guarantees that when the budget is scarce, it's
// spread across files instead of being consumed entirely by whichever
// file happens to come first.
func allocateDiffBudget(sizes []int, perFileCap, chunkBytes, totalBudget int) []int {
	n := len(sizes)
	alloc := make([]int, n)
	if n == 0 || totalBudget <= 0 {
		return alloc
	}
	if chunkBytes <= 0 {
		chunkBytes = totalBudget
	}

	remaining := make([]int, n)
	for i, s := range sizes {
		want := s
		if perFileCap > 0 && want > perFileCap {
			want = perFileCap
		}
		remaining[i] = want
	}

	budget := totalBudget
	for budget > 0 {
		grantedThisRound := false
		for i := 0; i < n && budget > 0; i++ {
			if remaining[i] <= 0 {
				continue
			}
			grant := chunkBytes
			if grant > remaining[i] {
				grant = remaining[i]
			}
			if grant > budget {
				grant = budget
			}
			alloc[i] += grant
			remaining[i] -= grant
			budget -= grant
			grantedThisRound = true
		}
		if !grantedThisRound {
			break
		}
	}
	return alloc
}

// truncateDiffAtLine truncates content to at most limit bytes, cutting
// at the last newline at or before limit when possible so a file's
// included diff ends on a clean line boundary instead of mid-line.
func truncateDiffAtLine(content string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(content) <= limit {
		return content
	}
	cut := content[:limit]
	if idx := strings.LastIndexByte(cut, '\n'); idx > 0 {
		return cut[:idx]
	}
	return cut
}

// formatOmittedNote renders a short, model-facing note listing files
// that were left out of the diff, so the model knows they exist (and
// changed) without their raw content - often large and low-signal -
// consuming the context budget.
func formatOmittedNote(omitted []omittedDiffFile) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %d generated/data/binary file(s) omitted from diff (lockfiles, vendored/generated code, large data blobs, or binary files):\n", len(omitted))

	for i, f := range omitted {
		if i >= maxOmittedFilesListed {
			fmt.Fprintf(&b, "#   ... and %d more\n", len(omitted)-maxOmittedFilesListed)
			break
		}
		fmt.Fprintf(&b, "#   - %s (%s, %d bytes)\n", f.Path, f.Reason, f.Bytes)
	}
	return strings.TrimRight(b.String(), "\n")
}

// Git helpers

func gitOutput(args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"--no-pager"}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func readFileLimited(path string, maxBytes int) (string, error) {
	cmd := exec.Command("head", "-c", strconv.Itoa(maxBytes), path)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(output), nil
}
