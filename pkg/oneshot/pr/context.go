package pr

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/odvcencio/buckley/pkg/transparency"
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

	// Get remote URL for context
	if remote, err := gitOutput("remote", "get-url", "origin"); err == nil {
		ctx.RemoteURL = strings.TrimSpace(remote)
	}

	// Get commits on this branch (since divergence from base)
	commits, err := getCommitsSinceBase(ctx.BaseBranch)
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
	diffStat, err := gitOutput("diff", "--stat", ctx.BaseBranch+"...HEAD")
	if err == nil {
		ctx.DiffSummary = diffStat
		audit.Add("diff summary", estimateTokens(diffStat))
	}

	// Get full diff (with limit)
	diff, truncated, err := gitOutputLimited(opts.MaxDiffBytes, "diff", ctx.BaseBranch+"...HEAD")
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get diff: %w", err)
	}
	ctx.FullDiff = diff
	diffTokens := estimateTokens(diff)
	if truncated {
		audit.AddTruncated("full diff", diffTokens, opts.MaxDiffTokens)
	} else {
		audit.Add("full diff", diffTokens)
	}

	// Get stats
	ctx.Stats = getDiffStats(ctx.BaseBranch)

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
func detectBaseBranch() string {
	// Try common default branches
	for _, branch := range []string{"main", "master", "develop"} {
		if _, err := gitOutput("rev-parse", "--verify", branch); err == nil {
			return branch
		}
	}
	// Try origin/HEAD
	if ref, err := gitOutput("symbolic-ref", "refs/remotes/origin/HEAD"); err == nil {
		parts := strings.Split(strings.TrimSpace(ref), "/")
		if len(parts) > 0 {
			return parts[len(parts)-1]
		}
	}
	return "main" // Default fallback
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

// Git helpers

func gitOutput(args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"--no-pager"}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func gitOutputLimited(maxBytes int, args ...string) (string, bool, error) {
	cmd := exec.Command("git", append([]string{"--no-pager"}, args...)...)
	output, err := cmd.Output()
	if err != nil {
		return "", false, err
	}

	if len(output) > maxBytes {
		return string(output[:maxBytes]), true, nil
	}
	return strings.TrimSpace(string(output)), false, nil
}

func readFileLimited(path string, maxBytes int) (string, error) {
	cmd := exec.Command("head", "-c", strconv.Itoa(maxBytes), path)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(output), nil
}
