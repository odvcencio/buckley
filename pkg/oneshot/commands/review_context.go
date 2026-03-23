package commands

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/odvcencio/buckley/pkg/transparency"
)

// BranchContext contains context for branch review.
type BranchContext struct {
	RepoRoot   string
	Branch     string
	BaseBranch string
	Files      []FileChange
	Stats      DiffStats
	Diff       string
	Unstaged   string
	RecentLog  string
	AgentsMD   string
}

// ProjectContext contains context for project review.
type ProjectContext struct {
	RepoRoot    string
	Branch      string
	Tree        string
	GoMod       string
	PackageJSON string
	ReadmeMD    string
	AgentsMD    string
	RecentLog   string
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
	IncludeAgents   bool
	BaseBranch      string
}

// DefaultBranchContextOptions returns sensible defaults.
func DefaultBranchContextOptions() BranchContextOptions {
	return BranchContextOptions{
		MaxDiffBytes:    200_000,
		IncludeUnstaged: true,
		IncludeAgents:   true,
		BaseBranch:      "",
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

	branch, _ := reviewGitOutput("rev-parse", "--abbrev-ref", "HEAD")
	ctx.Branch = strings.TrimSpace(branch)
	audit.Add("branch", reviewEstimateTokens(ctx.Branch))

	ctx.BaseBranch = opts.BaseBranch
	if ctx.BaseBranch == "" {
		ctx.BaseBranch = detectBaseBranch()
	}
	audit.Add("base branch", reviewEstimateTokens(ctx.BaseBranch))

	nameStatus, err := reviewGitOutput("diff", "--name-status", ctx.BaseBranch+"...HEAD")
	if err != nil {
		nameStatus, err = reviewGitOutput("diff", "--name-status", ctx.BaseBranch)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get changed files: %w", err)
		}
	}
	ctx.Files = parseNameStatus(nameStatus)
	audit.Add("changed files", reviewEstimateTokens(nameStatus))

	diff, truncated, err := reviewGitOutputLimited(opts.MaxDiffBytes, "diff", ctx.BaseBranch+"...HEAD")
	if err != nil {
		diff, truncated, err = reviewGitOutputLimited(opts.MaxDiffBytes, "diff", ctx.BaseBranch)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get diff: %w", err)
	}
	ctx.Diff = diff
	diffTokens := reviewEstimateTokens(diff)
	if truncated {
		audit.AddTruncated("git diff", diffTokens, opts.MaxDiffBytes/4)
	} else {
		audit.Add("git diff", diffTokens)
	}

	ctx.Stats = getDiffStats(ctx.BaseBranch)
	if ctx.Stats.Files == 0 {
		ctx.Stats.Files = len(ctx.Files)
	}

	if opts.IncludeUnstaged {
		unstaged, _ := reviewGitOutput("diff")
		if strings.TrimSpace(unstaged) != "" {
			ctx.Unstaged = unstaged
			audit.Add("unstaged changes", reviewEstimateTokens(unstaged))
		}
	}

	log, _ := reviewGitOutput("log", "--oneline", "-20", ctx.BaseBranch+"..HEAD")
	if strings.TrimSpace(log) != "" {
		ctx.RecentLog = log
		audit.Add("recent commits", reviewEstimateTokens(log))
	}

	if opts.IncludeAgents {
		agentsPath := filepath.Join(ctx.RepoRoot, "AGENTS.md")
		if content, err := reviewReadFileLimited(agentsPath, 10_000); err == nil && content != "" {
			ctx.AgentsMD = content
			audit.Add("AGENTS.md", reviewEstimateTokens(content))
		}
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

	branch, _ := reviewGitOutput("rev-parse", "--abbrev-ref", "HEAD")
	ctx.Branch = strings.TrimSpace(branch)
	audit.Add("branch", reviewEstimateTokens(ctx.Branch))

	tree, _ := getTree(ctx.RepoRoot, opts.MaxTreeDepth)
	if tree != "" {
		ctx.Tree = tree
		audit.Add("directory tree", reviewEstimateTokens(tree))
	}

	goModPath := filepath.Join(ctx.RepoRoot, "go.mod")
	if content, err := reviewReadFileLimited(goModPath, 5_000); err == nil && content != "" {
		ctx.GoMod = content
		audit.Add("go.mod", reviewEstimateTokens(content))
	}

	pkgJSONPath := filepath.Join(ctx.RepoRoot, "package.json")
	if content, err := reviewReadFileLimited(pkgJSONPath, 5_000); err == nil && content != "" {
		ctx.PackageJSON = content
		audit.Add("package.json", reviewEstimateTokens(content))
	}

	readmePath := filepath.Join(ctx.RepoRoot, "README.md")
	if content, err := reviewReadFileLimited(readmePath, 20_000); err == nil && content != "" {
		ctx.ReadmeMD = content
		audit.Add("README.md", reviewEstimateTokens(content))
	}

	if opts.IncludeAgents {
		agentsPath := filepath.Join(ctx.RepoRoot, "AGENTS.md")
		if content, err := reviewReadFileLimited(agentsPath, 10_000); err == nil && content != "" {
			ctx.AgentsMD = content
			audit.Add("AGENTS.md", reviewEstimateTokens(content))
		}
	}

	log, _ := reviewGitOutput("log", "--oneline", "-30")
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
	sb.WriteString(fmt.Sprintf("- **Root**: %s\n", ctx.RepoRoot))
	sb.WriteString(fmt.Sprintf("- **Branch**: %s\n", ctx.Branch))
	sb.WriteString(fmt.Sprintf("- **Base Branch**: %s\n", ctx.BaseBranch))
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
		sb.WriteString("## Unstaged Changes (not yet committed)\n\n")
		sb.WriteString("```diff\n")
		sb.WriteString(ctx.Unstaged)
		sb.WriteString("\n```\n\n")
	}

	if ctx.AgentsMD != "" {
		sb.WriteString("## Project Guidelines (AGENTS.md)\n\n")
		sb.WriteString(ctx.AgentsMD)
		sb.WriteString("\n\n")
	}

	return sb.String()
}

// BuildProjectPrompt builds the user prompt for project review.
func BuildProjectPrompt(ctx *ProjectContext) string {
	var sb strings.Builder

	sb.WriteString("## Repository Information\n\n")
	sb.WriteString(fmt.Sprintf("- **Root**: %s\n", ctx.RepoRoot))
	sb.WriteString(fmt.Sprintf("- **Branch**: %s\n", ctx.Branch))
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
		sb.WriteString("## AGENTS.md\n\n")
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
func detectBaseBranch() string {
	if _, err := reviewGitOutput("rev-parse", "--verify", "main"); err == nil {
		return "main"
	}
	if _, err := reviewGitOutput("rev-parse", "--verify", "master"); err == nil {
		return "master"
	}
	if _, err := reviewGitOutput("rev-parse", "--verify", "origin/main"); err == nil {
		return "origin/main"
	}
	if _, err := reviewGitOutput("rev-parse", "--verify", "origin/master"); err == nil {
		return "origin/master"
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

func getDiffStats(base string) DiffStats {
	output, err := reviewGitOutput("diff", "--numstat", base+"...HEAD")
	if err != nil {
		output, _ = reviewGitOutput("diff", "--numstat", base)
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

func getTree(root string, maxDepth int) (string, error) {
	args := []string{"-L", strconv.Itoa(maxDepth), "-I", "node_modules|.git|vendor|__pycache__|.venv|target", "--dirsfirst"}
	cmd := exec.Command("tree", args...)
	cmd.Dir = root
	output, err := cmd.Output()
	if err != nil {
		cmd := exec.Command("find", ".", "-maxdepth", strconv.Itoa(maxDepth), "-type", "d", "-not", "-path", "*/.*", "-not", "-path", "*/node_modules/*", "-not", "-path", "*/vendor/*")
		cmd.Dir = root
		output, err = cmd.Output()
		if err != nil {
			return "", err
		}
	}
	return string(output), nil
}

func reviewEstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return (len(s) + 3) / 4
}

func reviewGitOutput(args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"--no-pager"}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func reviewGitOutputLimited(maxBytes int, args ...string) (string, bool, error) {
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

func reviewReadFileLimited(path string, maxBytes int) (string, error) {
	cmd := exec.Command("head", "-c", strconv.Itoa(maxBytes), path)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(output), nil
}
