package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"m31labs.dev/buckley/pkg/diffsignal"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/transparency"
)

const (
	ReviewScopeBranch   = "branch"
	ReviewScopeChanges  = "changes"
	ReviewScopeWorktree = "worktree"
)

// BranchContext contains context for branch review.
type BranchContext struct {
	RepoRoot          string
	Branch            string
	BaseBranch        string
	HeadCommit        string
	BaseCommit        string
	Scope             string
	IncludesUnstaged  bool
	IncludesUntracked bool
	Files             []FileChange
	Stats             DiffStats
	Diff              string
	Unstaged          string
	DiffTruncated     bool
	UnstagedTruncated bool
	ContextIncomplete bool
	RecentLog         string
	AgentsMD          string
}

// ProjectContext contains context for project review.
type ProjectContext struct {
	RepoRoot          string
	Branch            string
	HeadCommit        string
	Tree              string
	GoMod             string
	PackageJSON       string
	ReadmeMD          string
	AgentsMD          string
	RecentLog         string
	ContextIncomplete bool
}

// FileChange represents a file change.
type FileChange struct {
	Status  string
	Path    string
	OldPath string
}

// DiffStats contains diff statistics.
type DiffStats struct {
	Files      int
	Insertions int
	Deletions  int
}

// TotalChanges returns total lines changed.
func (ds DiffStats) TotalChanges() int {
	return ds.Insertions + ds.Deletions
}

// BranchContextOptions configures branch context assembly.
type BranchContextOptions struct {
	MaxDiffBytes    int
	IncludeUnstaged bool
	// UntrackedPaths explicitly allowlists repository-relative untracked text
	// paths for model input. Callers must obtain clear user authorization.
	UntrackedPaths []string
	IncludeAgents  bool
	BaseBranch     string
	Scope          string
	// CapturedUntracked is immutable worktree evidence supplied by the review
	// snapshot. When UntrackedPaths is non-empty, nil asks direct callers to
	// capture it; non-nil (even empty) prevents a second live read from
	// diverging from verification.
	CapturedUntracked []model.ReviewUntrackedFile
}

// DefaultBranchContextOptions returns sensible defaults.
func DefaultBranchContextOptions() BranchContextOptions {
	return BranchContextOptions{
		MaxDiffBytes:    diffsignal.ReviewDiffBudget,
		IncludeUnstaged: true,
		IncludeAgents:   true,
		BaseBranch:      "",
		Scope:           ReviewScopeWorktree,
	}
}

// ProjectContextOptions configures project context assembly.
type ProjectContextOptions struct {
	MaxTreeDepth  int
	IncludeAgents bool
}

// DefaultProjectContextOptions returns sensible defaults.
func DefaultProjectContextOptions() ProjectContextOptions {
	return ProjectContextOptions{
		MaxTreeDepth:  3,
		IncludeAgents: true,
	}
}

// AssembleBranchContext gathers context for branch review.
func AssembleBranchContext(opts BranchContextOptions) (*BranchContext, *transparency.ContextAudit, error) {
	audit := transparency.NewContextAudit()
	ctx := &BranchContext{}

	root, err := reviewGitOutput("rev-parse", "--show-toplevel")
	if err != nil {
		return nil, nil, fmt.Errorf("not in a git repository: %w", err)
	}
	ctx.RepoRoot = strings.TrimSpace(root)

	branch, _ := reviewGitOutputAt(ctx.RepoRoot, "rev-parse", "--abbrev-ref", "HEAD")
	ctx.Branch = strings.TrimSpace(branch)
	audit.Add("branch", reviewEstimateTokens(ctx.Branch))

	ctx.HeadCommit, err = resolveReviewCommit(ctx.RepoRoot, "HEAD")
	if err != nil {
		return nil, nil, fmt.Errorf("resolve review HEAD: %w", err)
	}
	audit.Add("head commit", reviewEstimateTokens(ctx.HeadCommit))

	ctx.Scope = normalizeReviewScope(opts.Scope)
	ctx.IncludesUnstaged = opts.IncludeUnstaged
	if len(opts.UntrackedPaths) > 0 && (!opts.IncludeUnstaged || ctx.Scope != ReviewScopeWorktree) {
		return nil, nil, fmt.Errorf("untracked review input requires worktree scope with unstaged changes enabled")
	}
	audit.Add("review scope", reviewEstimateTokens(ctx.Scope))

	ctx.BaseBranch = opts.BaseBranch
	if ctx.BaseBranch == "" && ctx.Scope != ReviewScopeChanges {
		ctx.BaseBranch = detectBaseBranch(ctx.RepoRoot)
	}
	if ctx.Scope == ReviewScopeChanges {
		ctx.BaseBranch = "(local changes)"
		ctx.BaseCommit = ctx.HeadCommit
	} else {
		ctx.BaseCommit, err = resolveReviewCommit(ctx.RepoRoot, ctx.BaseBranch)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve review base %q: %w", ctx.BaseBranch, err)
		}
	}
	if ctx.BaseBranch != "" {
		audit.Add("base branch", reviewEstimateTokens(ctx.BaseBranch))
	}
	if ctx.BaseCommit != "" {
		audit.Add("base commit", reviewEstimateTokens(ctx.BaseCommit))
	}

	if ctx.Scope == ReviewScopeChanges {
		nameStatus, err := localNameStatus(ctx.RepoRoot, opts.IncludeUnstaged)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get local changed files: %w", err)
		}
		ctx.Files = parseNameStatus(nameStatus)
		audit.Add("changed files", reviewEstimateTokens(nameStatus))

		diff, rawTruncated, err := localDiff(ctx.RepoRoot, opts.MaxDiffBytes, opts.IncludeUnstaged)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get local diff: %w", err)
		}
		diffRes := diffsignal.Prioritize(diff, opts.MaxDiffBytes)
		ctx.Diff = diffRes.Context
		ctx.DiffTruncated = rawTruncated || diffRes.Truncated
		diffTokens := reviewEstimateTokens(ctx.Diff)
		if ctx.DiffTruncated {
			audit.AddTruncated("local changes", diffTokens, opts.MaxDiffBytes/4)
		} else {
			audit.Add("local changes", diffTokens)
		}

		ctx.Stats = getLocalDiffStats(ctx.RepoRoot, opts.IncludeUnstaged)
	} else {
		nameStatus, err := reviewGitOutputAt(ctx.RepoRoot, "diff", "--name-status", ctx.BaseCommit+"..."+ctx.HeadCommit, "--")
		if err != nil {
			nameStatus, err = reviewGitOutputAt(ctx.RepoRoot, "diff", "--name-status", ctx.BaseCommit, ctx.HeadCommit, "--")
			if err != nil {
				return nil, nil, fmt.Errorf("failed to get changed files: %w", err)
			}
		}
		ctx.Files = parseNameStatus(nameStatus)
		audit.Add("changed files", reviewEstimateTokens(nameStatus))

		diff, rawTruncated, err := reviewGitOutputLimitedAt(ctx.RepoRoot, diffsignal.MaxParseBytes, "diff", ctx.BaseCommit+"..."+ctx.HeadCommit, "--")
		if err != nil {
			diff, rawTruncated, err = reviewGitOutputLimitedAt(ctx.RepoRoot, diffsignal.MaxParseBytes, "diff", ctx.BaseCommit, ctx.HeadCommit, "--")
		}
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get diff: %w", err)
		}
		diffRes := diffsignal.Prioritize(diff, opts.MaxDiffBytes)
		ctx.Diff = diffRes.Context
		ctx.DiffTruncated = rawTruncated || diffRes.Truncated
		diffTokens := reviewEstimateTokens(ctx.Diff)
		if ctx.DiffTruncated {
			audit.AddTruncated("git diff", diffTokens, opts.MaxDiffBytes/4)
		} else {
			audit.Add("git diff", diffTokens)
		}

		ctx.Stats = getDiffStats(ctx.RepoRoot, ctx.BaseCommit, ctx.HeadCommit)
		if ctx.Stats.Files == 0 {
			ctx.Stats.Files = len(ctx.Files)
		}

		if ctx.Scope == ReviewScopeWorktree {
			// Worktree review always includes staged changes. Untracked text is
			// included only after a separate, explicit caller opt-in.
			var untracked []model.ReviewUntrackedFile
			if len(opts.UntrackedPaths) > 0 {
				if opts.CapturedUntracked != nil {
					untracked = opts.CapturedUntracked
					if err := validateCapturedReviewUntracked(opts.UntrackedPaths, untracked); err != nil {
						return nil, nil, err
					}
				} else {
					untracked, err = model.CaptureReviewUntrackedFiles(context.Background(), ctx.RepoRoot, opts.UntrackedPaths)
					if err != nil {
						return nil, nil, fmt.Errorf("capture reviewable untracked files: %w", err)
					}
				}
				ctx.IncludesUntracked = true
			}
			localStatus, err := localNameStatus(ctx.RepoRoot, opts.IncludeUnstaged)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to get worktree changed files: %w", err)
			}
			localStatus = appendReviewUntrackedNameStatus(localStatus, untracked)
			if strings.TrimSpace(localStatus) != "" {
				ctx.Files = mergeFileChanges(ctx.Files, parseNameStatus(localStatus))
			}
			localStats := getLocalDiffStats(ctx.RepoRoot, opts.IncludeUnstaged)
			for _, file := range untracked {
				localStats.Files++
				localStats.Insertions += file.Insertions
			}
			ctx.Stats.Files = len(ctx.Files)
			ctx.Stats.Insertions += localStats.Insertions
			ctx.Stats.Deletions += localStats.Deletions
			localChanges, localRawTrunc, err := localDiff(ctx.RepoRoot, 0, opts.IncludeUnstaged)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to get worktree diff: %w", err)
			}
			localChanges = appendReviewUntrackedDiff(localChanges, untracked)
			if strings.TrimSpace(localChanges) != "" {
				// Reserve space for the truncation marker so appending it never
				// pushes ctx.Unstaged past MaxDiffBytes (marker is 16 bytes).
				const truncMarker = "\n... (truncated)"
				unstagedBudget := opts.MaxDiffBytes
				if unstagedBudget > len(truncMarker) {
					unstagedBudget -= len(truncMarker)
				}
				unstagedRes := diffsignal.Prioritize(localChanges, unstagedBudget)
				ctx.Unstaged = unstagedRes.Context
				ctx.UnstagedTruncated = localRawTrunc || unstagedRes.Truncated
				if ctx.UnstagedTruncated {
					ctx.Unstaged += truncMarker
					audit.AddTruncated("worktree changes", reviewEstimateTokens(ctx.Unstaged), opts.MaxDiffBytes/4)
				} else {
					audit.Add("worktree changes", reviewEstimateTokens(ctx.Unstaged))
				}
			}
		}
	}

	logArgs := []string{"log", "--oneline", "-20", ctx.HeadCommit}
	if ctx.Scope != ReviewScopeChanges {
		logArgs = []string{"log", "--oneline", "-20", ctx.BaseCommit + ".." + ctx.HeadCommit}
	}
	log, _ := reviewGitOutputAt(ctx.RepoRoot, logArgs...)
	if strings.TrimSpace(log) != "" {
		ctx.RecentLog = log
		audit.Add("recent commits", reviewEstimateTokens(log))
	}

	if opts.IncludeAgents {
		ctx.AgentsMD, ctx.ContextIncomplete = assembleReviewAgentsContext(
			nestedBranchAgentsCandidates(ctx.Files),
			func(path string, maxBytes int) (string, bool, error) {
				return readBranchSnapshotFile(ctx, opts.IncludeUnstaged, path, maxBytes)
			},
			audit,
		)
	}

	return ctx, audit, nil
}

// AssembleProjectContext gathers context for project review.
func AssembleProjectContext(opts ProjectContextOptions) (*ProjectContext, *transparency.ContextAudit, error) {
	audit := transparency.NewContextAudit()
	ctx := &ProjectContext{}

	root, err := reviewGitOutput("rev-parse", "--show-toplevel")
	if err != nil {
		return nil, nil, fmt.Errorf("not in a git repository: %w", err)
	}
	ctx.RepoRoot = strings.TrimSpace(root)

	branch, _ := reviewGitOutputAt(ctx.RepoRoot, "rev-parse", "--abbrev-ref", "HEAD")
	ctx.Branch = strings.TrimSpace(branch)
	audit.Add("branch", reviewEstimateTokens(ctx.Branch))

	ctx.HeadCommit, err = resolveReviewCommit(ctx.RepoRoot, "HEAD")
	if err != nil {
		return nil, nil, fmt.Errorf("resolve project review HEAD: %w", err)
	}
	audit.Add("head commit", reviewEstimateTokens(ctx.HeadCommit))

	trackedFiles, err := trackedProjectFiles(ctx.RepoRoot)
	if err != nil {
		return nil, nil, fmt.Errorf("enumerate tracked project files: %w", err)
	}
	tracked := make(map[string]struct{}, len(trackedFiles))
	for _, path := range trackedFiles {
		tracked[path] = struct{}{}
	}

	tree := buildTrackedTree(trackedFiles, opts.MaxTreeDepth)
	if tree != "" {
		ctx.Tree = tree
		audit.Add("directory tree", reviewEstimateTokens(tree))
	}

	if content, truncated, readErr := readTrackedProjectFile(ctx.RepoRoot, tracked, "go.mod", 5_000); readErr != nil {
		ctx.ContextIncomplete = true
		audit.Add("go.mod (unavailable)", 0)
	} else if truncated {
		ctx.GoMod = content
		ctx.ContextIncomplete = true
		audit.AddTruncated("go.mod", reviewEstimateTokens(content), reviewEstimateTokens(content)+1)
	} else if content != "" {
		ctx.GoMod = content
		audit.Add("go.mod", reviewEstimateTokens(content))
	}

	if content, truncated, readErr := readTrackedProjectFile(ctx.RepoRoot, tracked, "package.json", 5_000); readErr != nil {
		ctx.ContextIncomplete = true
		audit.Add("package.json (unavailable)", 0)
	} else if truncated {
		ctx.PackageJSON = content
		ctx.ContextIncomplete = true
		audit.AddTruncated("package.json", reviewEstimateTokens(content), reviewEstimateTokens(content)+1)
	} else if content != "" {
		ctx.PackageJSON = content
		audit.Add("package.json", reviewEstimateTokens(content))
	}

	if content, truncated, readErr := readTrackedProjectFile(ctx.RepoRoot, tracked, "README.md", 20_000); readErr != nil {
		ctx.ContextIncomplete = true
		audit.Add("README.md (unavailable)", 0)
	} else if truncated {
		ctx.ReadmeMD = content
		ctx.ContextIncomplete = true
		audit.AddTruncated("README.md", reviewEstimateTokens(content), reviewEstimateTokens(content)+1)
	} else if content != "" {
		ctx.ReadmeMD = content
		audit.Add("README.md", reviewEstimateTokens(content))
	}

	if opts.IncludeAgents {
		agents, incomplete := assembleReviewAgentsContext(
			nestedProjectAgentsCandidates(trackedFiles),
			func(path string, maxBytes int) (string, bool, error) {
				return readTrackedProjectFile(ctx.RepoRoot, tracked, path, maxBytes)
			},
			audit,
		)
		ctx.AgentsMD = agents
		ctx.ContextIncomplete = ctx.ContextIncomplete || incomplete
	}

	log, _ := reviewGitOutputAt(ctx.RepoRoot, "log", "--oneline", "-30", ctx.HeadCommit)
	if strings.TrimSpace(log) != "" {
		ctx.RecentLog = log
		audit.Add("recent commits", reviewEstimateTokens(log))
	}

	return ctx, audit, nil
}

// BuildBranchPrompt builds the user prompt for branch review.
func BuildBranchPrompt(ctx *BranchContext) string {
	var sb strings.Builder

	sb.WriteString("## Repository Information\n\n")
	sb.WriteString("- **Root**: `.` (captured repository root; use repository-relative paths)\n")
	sb.WriteString(fmt.Sprintf("- **Branch**: %s\n", ctx.Branch))
	sb.WriteString(fmt.Sprintf("- **Head Commit**: `%s` (immutable)\n", ctx.HeadCommit))
	sb.WriteString(fmt.Sprintf("- **Base Ref**: %s (display only; do not re-resolve)\n", ctx.BaseBranch))
	sb.WriteString(fmt.Sprintf("- **Base Commit**: `%s` (immutable)\n", ctx.BaseCommit))
	sb.WriteString(fmt.Sprintf("- **Review Scope**: %s\n", defaultIfEmpty(ctx.Scope, ReviewScopeWorktree)))
	sb.WriteString(fmt.Sprintf("- **Diff Completeness**: %s\n", reviewCompleteness(ctx.DiffTruncated || ctx.UnstagedTruncated)))
	if ctx.ContextIncomplete {
		sb.WriteString("- **Context Completeness**: INCOMPLETE — project guidance was unavailable or truncated; do not approve\n")
	}
	sb.WriteString("- **Verification Identity**: use only the captured commit content and supplied diff; never resolve live branch refs\n")
	if ctx.IncludesUntracked {
		sb.WriteString("- **Included Local State**: explicitly opted-in filtered untracked text files (contents may still contain secrets)\n")
		sb.WriteString("- **Excluded Local State**: ignored, known secret-like, binary, symlink/non-regular untracked files; untracked AGENTS.md; and Git index-hidden assume-unchanged/skip-worktree edits\n")
	} else {
		sb.WriteString("- **Excluded Local State**: untracked files and Git index-hidden assume-unchanged/skip-worktree edits\n")
	}
	sb.WriteString("\n")

	if ctx.RecentLog != "" {
		sb.WriteString("## Commits on this Branch\n\n")
		sb.WriteString("```\n")
		sb.WriteString(ctx.RecentLog)
		sb.WriteString("\n```\n\n")
	}

	sb.WriteString("## Files Changed\n\n")
	sb.WriteString(fmt.Sprintf("**Summary**: %d files, +%d/-%d lines\n\n",
		ctx.Stats.Files, ctx.Stats.Insertions, ctx.Stats.Deletions))

	sb.WriteString("```\n")
	for _, f := range ctx.Files {
		sb.WriteString(fmt.Sprintf("%s\t%s\n", f.Status, f.Path))
	}
	sb.WriteString("```\n\n")

	sb.WriteString("## Full Diff\n\n")
	sb.WriteString("```diff\n")
	sb.WriteString(ctx.Diff)
	sb.WriteString("\n```\n\n")

	if ctx.Unstaged != "" {
		if ctx.IncludesUnstaged {
			sb.WriteString("## Worktree Changes (staged and unstaged)\n\n")
		} else {
			sb.WriteString("## Worktree Changes (staged only; unstaged excluded)\n\n")
		}
		sb.WriteString("```diff\n")
		sb.WriteString(ctx.Unstaged)
		sb.WriteString("\n```\n\n")
	}

	if ctx.AgentsMD != "" {
		sb.WriteString("## Project Guidelines (applicable AGENTS.md chain)\n\n")
		sb.WriteString(ctx.AgentsMD)
		sb.WriteString("\n\n")
	}

	return sb.String()
}

func reviewCompleteness(truncated bool) string {
	if truncated {
		return "TRUNCATED — do not approve without retrieving the omitted hunks"
	}
	return "COMPLETE"
}

// BuildProjectPrompt builds the user prompt for project review.
func BuildProjectPrompt(ctx *ProjectContext) string {
	var sb strings.Builder

	sb.WriteString("## Repository Information\n\n")
	sb.WriteString("- **Root**: `.` (captured repository root; use repository-relative paths)\n")
	sb.WriteString(fmt.Sprintf("- **Branch**: %s\n", ctx.Branch))
	sb.WriteString(fmt.Sprintf("- **Head Commit**: `%s` (immutable)\n", ctx.HeadCommit))
	sb.WriteString("- **Snapshot**: Git-visible tracked files only; untracked and index-hidden paths are intentionally excluded\n")
	if ctx.ContextIncomplete {
		sb.WriteString("- **Context Completeness**: INCOMPLETE — tracked metadata was unavailable or truncated\n")
	}
	sb.WriteString("\n")

	if ctx.Tree != "" {
		sb.WriteString("## Project Structure\n\n")
		sb.WriteString("```\n")
		sb.WriteString(ctx.Tree)
		sb.WriteString("\n```\n\n")
	}

	if ctx.GoMod != "" {
		sb.WriteString("## go.mod\n\n")
		sb.WriteString("```\n")
		sb.WriteString(ctx.GoMod)
		sb.WriteString("\n```\n\n")
	}

	if ctx.PackageJSON != "" {
		sb.WriteString("## package.json\n\n")
		sb.WriteString("```json\n")
		sb.WriteString(ctx.PackageJSON)
		sb.WriteString("\n```\n\n")
	}

	if ctx.ReadmeMD != "" {
		sb.WriteString("## README.md\n\n")
		sb.WriteString(ctx.ReadmeMD)
		sb.WriteString("\n\n")
	}

	if ctx.AgentsMD != "" {
		sb.WriteString("## Project Guidelines (applicable AGENTS.md chain)\n\n")
		sb.WriteString(ctx.AgentsMD)
		sb.WriteString("\n\n")
	}

	if ctx.RecentLog != "" {
		sb.WriteString("## Recent Git History\n\n")
		sb.WriteString("```\n")
		sb.WriteString(ctx.RecentLog)
		sb.WriteString("\n```\n\n")
	}

	return sb.String()
}

// detectBaseBranch tries to find main or master branch.
func detectBaseBranch(root string) string {
	if _, err := resolveReviewCommit(root, "origin/main"); err == nil {
		return "origin/main"
	}
	if _, err := resolveReviewCommit(root, "origin/master"); err == nil {
		return "origin/master"
	}
	if _, err := resolveReviewCommit(root, "main"); err == nil {
		return "main"
	}
	if _, err := resolveReviewCommit(root, "master"); err == nil {
		return "master"
	}
	return "main"
}

func parseNameStatus(output string) []FileChange {
	var changes []FileChange
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			continue
		}
		change := FileChange{
			Status: parts[0][:1],
			Path:   parts[len(parts)-1],
		}
		if (change.Status == "R" || change.Status == "C") && len(parts) >= 3 {
			change.OldPath = parts[1]
			change.Path = parts[2]
		}
		changes = append(changes, change)
	}
	return changes
}

func mergeFileChanges(base, extra []FileChange) []FileChange {
	if len(extra) == 0 {
		return base
	}
	seen := make(map[string]struct{}, len(base)+len(extra))
	merged := make([]FileChange, 0, len(base)+len(extra))
	for _, change := range append(base, extra...) {
		key := change.Status + "\x00" + change.OldPath + "\x00" + change.Path
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, change)
	}
	return merged
}

func getDiffStats(root, baseCommit, headCommit string) DiffStats {
	output, err := reviewGitOutputAt(root, "diff", "--numstat", baseCommit+"..."+headCommit, "--")
	if err != nil {
		output, _ = reviewGitOutputAt(root, "diff", "--numstat", baseCommit, headCommit, "--")
	}
	if output == "" {
		return DiffStats{}
	}

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
		if errIns == nil && errDel == nil {
			stats.Insertions += ins
			stats.Deletions += del
		}
	}
	return stats
}

func getLocalDiffStats(root string, includeUnstaged bool) DiffStats {
	if includeUnstaged {
		return parseDiffStats(localNumstat(root, "HEAD"))
	}
	return parseDiffStats(localNumstat(root, "--cached"))
}

func localNumstat(root string, args ...string) string {
	fullArgs := append([]string{"diff", "--numstat"}, args...)
	fullArgs = append(fullArgs, "--")
	output, _ := reviewGitOutputAt(root, fullArgs...)
	return output
}

func parseDiffStats(output string) DiffStats {
	if output == "" {
		return DiffStats{}
	}

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
		if errIns == nil && errDel == nil {
			stats.Insertions += ins
			stats.Deletions += del
		}
	}
	return stats
}

func localNameStatus(root string, includeUnstaged bool) (string, error) {
	args := []string{"diff", "--name-status"}
	if includeUnstaged {
		args = append(args, "HEAD")
	} else {
		args = append(args, "--cached")
	}
	args = append(args, "--")
	return reviewGitOutputAt(root, args...)
}

func appendReviewUntrackedNameStatus(status string, files []model.ReviewUntrackedFile) string {
	if len(files) == 0 {
		return status
	}
	var result strings.Builder
	result.WriteString(strings.TrimSpace(status))
	for _, file := range files {
		if result.Len() > 0 {
			result.WriteByte('\n')
		}
		result.WriteString("A\t")
		result.WriteString(file.Path)
	}
	return result.String()
}

func validateCapturedReviewUntracked(allowlistedPaths []string, files []model.ReviewUntrackedFile) error {
	allowed := make(map[string]struct{}, len(allowlistedPaths))
	for _, raw := range allowlistedPaths {
		path := filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(raw))))
		if path == "." || filepath.IsAbs(path) || path == ".." || strings.HasPrefix(path, "../") {
			return fmt.Errorf("unsafe captured untracked allowlist path %q", raw)
		}
		allowed[path] = struct{}{}
	}
	for _, file := range files {
		if _, ok := allowed[file.Path]; !ok {
			return fmt.Errorf("captured untracked review evidence %q was not explicitly allowlisted", file.Path)
		}
		delete(allowed, file.Path)
	}
	if len(allowed) > 0 {
		missing := make([]string, 0, len(allowed))
		for path := range allowed {
			missing = append(missing, path)
		}
		sort.Strings(missing)
		return fmt.Errorf("captured untracked review evidence is missing allowlisted paths: %s", strings.Join(missing, ", "))
	}
	return nil
}

func appendReviewUntrackedDiff(diff string, files []model.ReviewUntrackedFile) string {
	if len(files) == 0 {
		return diff
	}
	var result strings.Builder
	result.Grow(len(diff))
	result.WriteString(strings.TrimSpace(diff))
	for _, file := range files {
		if result.Len() > 0 {
			result.WriteByte('\n')
		}
		result.Write(file.Patch)
	}
	return result.String()
}

func localDiff(root string, maxBytes int, includeUnstaged bool) (string, bool, error) {
	args := []string{"diff"}
	if includeUnstaged {
		args = append(args, "HEAD")
	} else {
		args = append(args, "--cached")
	}
	args = append(args, "--")
	diff, truncated, err := reviewGitOutputLimitedAt(root, diffsignal.MaxParseBytes, args...)
	if err != nil {
		return "", false, err
	}
	if maxBytes > 0 && len(diff) > maxBytes {
		diff = diff[:maxBytes]
		truncated = true
	}
	return diff, truncated, nil
}

func normalizeReviewScope(scope string) string {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "", ReviewScopeWorktree:
		return ReviewScopeWorktree
	case ReviewScopeBranch, "commits":
		return ReviewScopeBranch
	case ReviewScopeChanges, "change", "local", "working-tree":
		return ReviewScopeChanges
	default:
		return ReviewScopeWorktree
	}
}

func defaultIfEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// RevalidateBranchContext verifies that the named refs used to assemble the
// review still resolve to the immutable commits recorded in the context.
// Evidence commands themselves use only those commits, so movement after this
// check cannot silently change the supplied diff.
func RevalidateBranchContext(ctx *BranchContext) error {
	if ctx == nil {
		return fmt.Errorf("branch review context is required")
	}
	if strings.TrimSpace(ctx.RepoRoot) == "" || strings.TrimSpace(ctx.HeadCommit) == "" {
		return fmt.Errorf("branch review context is missing repository identity")
	}

	root, err := reviewGitOutputAt(ctx.RepoRoot, "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("revalidate review repository: %w", err)
	}
	if filepath.Clean(strings.TrimSpace(root)) != filepath.Clean(ctx.RepoRoot) {
		return fmt.Errorf("review repository changed while context was assembled")
	}

	head, err := resolveReviewCommit(ctx.RepoRoot, "HEAD")
	if err != nil {
		return fmt.Errorf("revalidate review HEAD: %w", err)
	}
	if head != ctx.HeadCommit {
		return fmt.Errorf("review HEAD moved from %s to %s while context was assembled", ctx.HeadCommit, head)
	}

	if ctx.Scope == ReviewScopeChanges {
		return nil
	}
	if strings.TrimSpace(ctx.BaseBranch) == "" || strings.TrimSpace(ctx.BaseCommit) == "" {
		return fmt.Errorf("branch review context is missing base identity")
	}
	base, err := resolveReviewCommit(ctx.RepoRoot, ctx.BaseBranch)
	if err != nil {
		return fmt.Errorf("revalidate review base %q: %w", ctx.BaseBranch, err)
	}
	if base != ctx.BaseCommit {
		return fmt.Errorf("review base %q moved from %s to %s while context was assembled", ctx.BaseBranch, ctx.BaseCommit, base)
	}
	return nil
}

func resolveReviewCommit(root, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.HasPrefix(ref, "-") {
		return "", fmt.Errorf("invalid Git revision %q", ref)
	}
	commit, err := reviewGitOutputAt(root, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	commit = strings.TrimSpace(commit)
	if commit == "" {
		return "", fmt.Errorf("Git revision %q resolved to an empty commit", ref)
	}
	return commit, nil
}

func trackedProjectFiles(root string) ([]string, error) {
	output, err := reviewGitBytesAt(root, "ls-tree", "-r", "--name-only", "-z", "HEAD", "--")
	if err != nil {
		return nil, err
	}

	seen := reviewNullPathSet(output)
	deleted, err := reviewGitBytesAt(root, "diff", "--no-renames", "--name-only", "-z", "--diff-filter=D", "HEAD", "--")
	if err != nil {
		return nil, err
	}
	for path := range reviewNullPathSet(deleted) {
		delete(seen, path)
	}
	changed, err := reviewGitBytesAt(root, "diff", "--no-renames", "--name-only", "-z", "--diff-filter=ACMRTUXB", "HEAD", "--")
	if err != nil {
		return nil, err
	}
	for path := range reviewNullPathSet(changed) {
		seen[path] = struct{}{}
	}

	files := make([]string, 0, len(seen))
	for path := range seen {
		files = append(files, path)
	}
	sort.Strings(files)
	return files, nil
}

func reviewNullPathSet(output []byte) map[string]struct{} {
	paths := make(map[string]struct{})
	for _, raw := range strings.Split(string(output), "\x00") {
		path := filepath.ToSlash(filepath.Clean(raw))
		if path == "" || path == "." || filepath.IsAbs(path) || path == ".." || strings.HasPrefix(path, "../") {
			continue
		}
		paths[path] = struct{}{}
	}
	return paths
}

type trackedTreeEntry struct {
	path  string
	isDir bool
}

func buildTrackedTree(files []string, maxDepth int) string {
	if maxDepth < 1 {
		return "."
	}

	entries := make(map[string]bool)
	for _, path := range files {
		parts := strings.Split(filepath.ToSlash(path), "/")
		limit := len(parts)
		if limit > maxDepth {
			limit = maxDepth
		}
		for depth := 1; depth <= limit; depth++ {
			entryPath := strings.Join(parts[:depth], "/")
			isDir := depth < len(parts)
			entries[entryPath] = entries[entryPath] || isDir
		}
	}

	ordered := make([]trackedTreeEntry, 0, len(entries))
	for path, isDir := range entries {
		ordered = append(ordered, trackedTreeEntry{path: path, isDir: isDir})
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].path < ordered[j].path })

	var sb strings.Builder
	sb.WriteString(".")
	for _, entry := range ordered {
		depth := strings.Count(entry.path, "/")
		name := filepath.Base(entry.path)
		if entry.isDir {
			name += "/"
		}
		sb.WriteByte('\n')
		sb.WriteString(strings.Repeat("  ", depth))
		sb.WriteString(name)
	}
	return sb.String()
}

func readTrackedProjectFile(root string, tracked map[string]struct{}, path string, maxBytes int) (string, bool, error) {
	path = filepath.ToSlash(filepath.Clean(path))
	if _, ok := tracked[path]; !ok {
		return "", false, nil
	}
	return readTrackedWorktreeSnapshotFile(root, path, maxBytes)
}

type reviewSnapshotFileReader func(path string, maxBytes int) (string, bool, error)

const (
	reviewAgentsPerFileLimit     = 10_000
	reviewNestedAgentsTotalLimit = 40_000
)

// assembleReviewAgentsContext reads the root instructions plus every nested
// instruction file that can govern the reviewed paths. The caller supplies a
// reader bound to the same Git snapshot as the rest of the review evidence.
func assembleReviewAgentsContext(candidates []string, read reviewSnapshotFileReader, audit *transparency.ContextAudit) (string, bool) {
	var content strings.Builder
	incomplete := false

	root, truncated, err := read("AGENTS.md", reviewAgentsPerFileLimit)
	switch {
	case err != nil && !os.IsNotExist(err):
		incomplete = true
		audit.Add("AGENTS.md (unavailable)", 0)
	case truncated:
		content.WriteString(root)
		incomplete = true
		audit.AddTruncated("AGENTS.md", reviewEstimateTokens(root), reviewEstimateTokens(root)+1)
	case root != "":
		content.WriteString(root)
		audit.Add("AGENTS.md", reviewEstimateTokens(root))
	}

	remaining := reviewNestedAgentsTotalLimit
	for _, candidate := range candidates {
		limit := reviewAgentsPerFileLimit
		if limit > remaining {
			limit = remaining
		}
		nested, truncated, err := read(candidate, limit)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			incomplete = true
			audit.Add(candidate+" (unavailable)", 0)
			continue
		}
		if nested == "" && !truncated {
			continue
		}

		if content.Len() > 0 {
			content.WriteString("\n\n")
		}
		content.WriteString("### ")
		content.WriteString(candidate)
		content.WriteString("\n\n")
		content.WriteString(nested)
		remaining -= len(nested)

		if truncated {
			incomplete = true
			audit.AddTruncated(candidate, reviewEstimateTokens(nested), reviewEstimateTokens(nested)+1)
		} else {
			audit.Add(candidate, reviewEstimateTokens(nested))
		}
	}

	return content.String(), incomplete
}

func nestedBranchAgentsCandidates(files []FileChange) []string {
	seen := make(map[string]struct{})
	for _, file := range files {
		addNestedAgentsCandidates(seen, file.Path)
		addNestedAgentsCandidates(seen, file.OldPath)
	}
	return sortedNestedAgentsCandidates(seen)
}

func nestedProjectAgentsCandidates(files []string) []string {
	seen := make(map[string]struct{})
	for _, file := range files {
		file = pathpkg.Clean(strings.TrimSpace(strings.ReplaceAll(file, "\\", "/")))
		if file == "AGENTS.md" || pathpkg.Base(file) != "AGENTS.md" || !validReviewRepoPath(file) {
			continue
		}
		seen[file] = struct{}{}
	}
	return sortedNestedAgentsCandidates(seen)
}

func addNestedAgentsCandidates(seen map[string]struct{}, file string) {
	file = pathpkg.Clean(strings.TrimSpace(strings.ReplaceAll(file, "\\", "/")))
	if !validReviewRepoPath(file) {
		return
	}
	for dir := pathpkg.Dir(file); dir != "." && dir != "/"; dir = pathpkg.Dir(dir) {
		seen[pathpkg.Join(dir, "AGENTS.md")] = struct{}{}
	}
}

func validReviewRepoPath(path string) bool {
	return path != "" && path != "." && path != ".." && !strings.HasPrefix(path, "../") && !strings.HasPrefix(path, "/")
}

func sortedNestedAgentsCandidates(seen map[string]struct{}) []string {
	candidates := make([]string, 0, len(seen))
	for candidate := range seen {
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(i, j int) bool {
		leftDepth := strings.Count(candidates[i], "/")
		rightDepth := strings.Count(candidates[j], "/")
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return candidates[i] < candidates[j]
	})
	return candidates
}

func readBranchSnapshotFile(ctx *BranchContext, includeUnstaged bool, path string, maxBytes int) (string, bool, error) {
	if ctx.Scope == ReviewScopeBranch {
		mode, exists, err := reviewGitTreePathMode(ctx.RepoRoot, ctx.HeadCommit, path)
		if err != nil {
			return "", false, err
		}
		if !exists {
			return "", false, os.ErrNotExist
		}
		if mode == "120000" {
			return "", false, fmt.Errorf("refusing to follow tracked AGENTS.md symlink %q", path)
		}
		return reviewGitOutputLimitedAt(ctx.RepoRoot, maxBytes, "show", ctx.HeadCommit+":"+path)
	}
	if !includeUnstaged {
		mode, exists, err := reviewGitIndexPathMode(ctx.RepoRoot, path)
		if err != nil {
			return "", false, err
		}
		if !exists {
			return "", false, os.ErrNotExist
		}
		if mode == "120000" {
			return "", false, fmt.Errorf("refusing to follow tracked AGENTS.md symlink %q", path)
		}
		return reviewGitOutputLimitedAt(ctx.RepoRoot, maxBytes, "show", ":"+path)
	}
	if _, err := reviewGitOutputAt(ctx.RepoRoot, "ls-files", "--error-unmatch", "--", path); err != nil {
		return "", false, os.ErrNotExist
	}
	return readTrackedWorktreeSnapshotFile(ctx.RepoRoot, path, maxBytes)
}

func readTrackedWorktreeSnapshotFile(root, path string, maxBytes int) (string, bool, error) {
	changed, err := reviewGitPathChanged(root, path)
	if err != nil {
		return "", false, err
	}
	if !changed {
		mode, exists, err := reviewGitTreePathMode(root, "HEAD", path)
		if err != nil {
			return "", false, err
		}
		if !exists {
			return "", false, os.ErrNotExist
		}
		if mode == "120000" {
			return "", false, fmt.Errorf("refusing to follow tracked symlink %q", path)
		}
		return reviewGitOutputLimitedAt(root, maxBytes, "show", "HEAD:"+path)
	}
	return reviewReadFileLimited(filepath.Join(root, filepath.FromSlash(path)), maxBytes)
}

func reviewGitPathChanged(root, path string) (bool, error) {
	cmd := exec.Command("git", "--no-pager", "-C", root, "diff", "--quiet", "--no-ext-diff", "--no-textconv", "HEAD", "--", path)
	err := cmd.Run()
	if err == nil {
		return false, nil
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		return false, err
	}
	return true, nil
}

func reviewGitTreePathMode(root, treeish, path string) (string, bool, error) {
	output, err := reviewGitOutputAt(root, "ls-tree", treeish, "--", path)
	if err != nil || strings.TrimSpace(output) == "" {
		return "", false, err
	}
	return strings.Fields(output)[0], true, nil
}

func reviewGitIndexPathMode(root, path string) (string, bool, error) {
	output, err := reviewGitOutputAt(root, "ls-files", "--stage", "--", path)
	if err != nil || strings.TrimSpace(output) == "" {
		return "", false, err
	}
	return strings.Fields(output)[0], true, nil
}

func reviewEstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return (len(s) + 3) / 4
}

func reviewGitOutput(args ...string) (string, error) {
	return reviewGitOutputAt("", args...)
}

func reviewGitOutputAt(root string, args ...string) (string, error) {
	output, err := reviewGitBytesAt(root, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func reviewGitBytesAt(root string, args ...string) ([]byte, error) {
	gitArgs := []string{"--no-pager"}
	if strings.TrimSpace(root) != "" {
		gitArgs = append(gitArgs, "-C", root)
	}
	cmd := exec.Command("git", append(gitArgs, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, err
	}
	return output, nil
}

func reviewGitOutputLimited(maxBytes int, args ...string) (string, bool, error) {
	return reviewGitOutputLimitedAt("", maxBytes, args...)
}

func reviewGitOutputLimitedAt(root string, maxBytes int, args ...string) (string, bool, error) {
	gitArgs := []string{"--no-pager"}
	if strings.TrimSpace(root) != "" {
		gitArgs = append(gitArgs, "-C", root)
	}
	cmd := exec.Command("git", append(gitArgs, args...)...)
	output, err := cmd.Output()
	if err != nil {
		return "", false, err
	}

	if len(output) > maxBytes {
		return string(output[:maxBytes]), true, nil
	}
	return strings.TrimSpace(string(output)), false, nil
}

func reviewReadFileLimited(path string, maxBytes int) (string, bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", false, fmt.Errorf("refusing to follow tracked symlink %q", path)
	}
	if !info.Mode().IsRegular() {
		return "", false, fmt.Errorf("review context path %q is not a regular file", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer file.Close()
	if maxBytes < 0 {
		maxBytes = 0
	}
	output, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if err != nil {
		return "", false, err
	}
	if len(output) > maxBytes {
		return string(output[:maxBytes]), true, nil
	}
	return string(output), false, nil
}
