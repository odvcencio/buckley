package review

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
	BaseBranch      string // Defaults to main or master
}

// DefaultBranchContextOptions returns sensible defaults.
func DefaultBranchContextOptions() BranchContextOptions {
	return BranchContextOptions{
		MaxDiffBytes:    200_000,
		IncludeUnstaged: true,
		IncludeAgents:   true,
		BaseBranch:      "", // Auto-detect
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

	// Get repo root
	root, err := gitOutput("rev-parse", "--show-toplevel")
	if err != nil {
		return nil, nil, fmt.Errorf("not in a git repository: %w", err)
	}
	ctx.RepoRoot = strings.TrimSpace(root)

	// Get current branch
	branch, _ := gitOutput("rev-parse", "--abbrev-ref", "HEAD")
	ctx.Branch = strings.TrimSpace(branch)
	audit.Add("branch", estimateTokens(ctx.Branch))

	// Determine base branch
	ctx.BaseBranch = opts.BaseBranch
	if ctx.BaseBranch == "" {
		ctx.BaseBranch = detectBaseBranch()
	}
	audit.Add("base branch", estimateTokens(ctx.BaseBranch))

	// Get files changed from base
	nameStatus, err := gitOutput("diff", "--name-status", ctx.BaseBranch+"...HEAD")
	if err != nil {
		// Try without ...HEAD syntax
		nameStatus, err = gitOutput("diff", "--name-status", ctx.BaseBranch)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get changed files: %w", err)
		}
	}
	ctx.Files = parseNameStatus(nameStatus)
	audit.Add("changed files", estimateTokens(nameStatus))

	// Get diff from base
	diff, truncated, err := gitOutputLimited(opts.MaxDiffBytes, "diff", ctx.BaseBranch+"...HEAD")
	if err != nil {
		diff, truncated, err = gitOutputLimited(opts.MaxDiffBytes, "diff", ctx.BaseBranch)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get diff: %w", err)
	}
	ctx.Diff = diff
	diffTokens := estimateTokens(diff)
	if truncated {
		audit.AddTruncated("git diff", diffTokens, opts.MaxDiffBytes/4)
	} else {
		audit.Add("git diff", diffTokens)
	}

	// Get stats
	ctx.Stats = getDiffStats(ctx.BaseBranch)
	if ctx.Stats.Files == 0 {
		ctx.Stats.Files = len(ctx.Files)
	}

	// Get unstaged changes if requested
	if opts.IncludeUnstaged {
		unstaged, _ := gitOutput("diff")
		if strings.TrimSpace(unstaged) != "" {
			ctx.Unstaged = unstaged
			audit.Add("unstaged changes", estimateTokens(unstaged))
		}
	}

	// Get recent log
	log, _ := gitOutput("log", "--oneline", "-20", ctx.BaseBranch+"..HEAD")
	if strings.TrimSpace(log) != "" {
		ctx.RecentLog = log
		audit.Add("recent commits", estimateTokens(log))
	}

	// Load AGENTS.md
	if opts.IncludeAgents {
		agentsPath := filepath.Join(ctx.RepoRoot, "AGENTS.md")
		if content, err := readFileLimited(agentsPath, 10_000); err == nil && content != "" {
			ctx.AgentsMD = content
			audit.Add("AGENTS.md", estimateTokens(content))
		}
	}

	return ctx, audit, nil
}

// AssembleProjectContext gathers context for project review.
func AssembleProjectContext(opts ProjectContextOptions) (*ProjectContext, *transparency.ContextAudit, error) {
	audit := transparency.NewContextAudit()
	ctx := &ProjectContext{}

	// Get repo root
	root, err := gitOutput("rev-parse", "--show-toplevel")
	if err != nil {
		return nil, nil, fmt.Errorf("not in a git repository: %w", err)
	}
	ctx.RepoRoot = strings.TrimSpace(root)

	// Get current branch
	branch, _ := gitOutput("rev-parse", "--abbrev-ref", "HEAD")
	ctx.Branch = strings.TrimSpace(branch)
	audit.Add("branch", estimateTokens(ctx.Branch))

	// Get directory tree
	tree, _ := getTree(ctx.RepoRoot, opts.MaxTreeDepth)
	if tree != "" {
		ctx.Tree = tree
		audit.Add("directory tree", estimateTokens(tree))
	}

	// Load key config files
	goModPath := filepath.Join(ctx.RepoRoot, "go.mod")
	if content, err := readFileLimited(goModPath, 5_000); err == nil && content != "" {
		ctx.GoMod = content
		audit.Add("go.mod", estimateTokens(content))
	}

	pkgJSONPath := filepath.Join(ctx.RepoRoot, "package.json")
	if content, err := readFileLimited(pkgJSONPath, 5_000); err == nil && content != "" {
		ctx.PackageJSON = content
		audit.Add("package.json", estimateTokens(content))
	}

	// Load README
	readmePath := filepath.Join(ctx.RepoRoot, "README.md")
	if content, err := readFileLimited(readmePath, 20_000); err == nil && content != "" {
		ctx.ReadmeMD = content
		audit.Add("README.md", estimateTokens(content))
	}

	// Load AGENTS.md
	if opts.IncludeAgents {
		agentsPath := filepath.Join(ctx.RepoRoot, "AGENTS.md")
		if content, err := readFileLimited(agentsPath, 10_000); err == nil && content != "" {
			ctx.AgentsMD = content
			audit.Add("AGENTS.md", estimateTokens(content))
		}
	}

	// Get recent log
	log, _ := gitOutput("log", "--oneline", "-30")
	if strings.TrimSpace(log) != "" {
		ctx.RecentLog = log
		audit.Add("recent commits", estimateTokens(log))
	}

	return ctx, audit, nil
}

// detectBaseBranch tries to find main or master branch.
func detectBaseBranch() string {
	// Check if main exists
	if _, err := gitOutput("rev-parse", "--verify", "main"); err == nil {
		return "main"
	}
	// Check if master exists
	if _, err := gitOutput("rev-parse", "--verify", "master"); err == nil {
		return "master"
	}
	// Check remote
	if _, err := gitOutput("rev-parse", "--verify", "origin/main"); err == nil {
		return "origin/main"
	}
	if _, err := gitOutput("rev-parse", "--verify", "origin/master"); err == nil {
		return "origin/master"
	}
	return "main" // Default
}

// parseNameStatus parses git diff --name-status output.
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

// getDiffStats extracts diff statistics.
func getDiffStats(base string) DiffStats {
	output, err := gitOutput("diff", "--numstat", base+"...HEAD")
	if err != nil {
		output, _ = gitOutput("diff", "--numstat", base)
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

// getTree generates a directory tree.
func getTree(root string, maxDepth int) (string, error) {
	args := []string{"-L", strconv.Itoa(maxDepth), "-I", "node_modules|.git|vendor|__pycache__|.venv|target", "--dirsfirst"}
	cmd := exec.Command("tree", args...)
	cmd.Dir = root
	output, err := cmd.Output()
	if err != nil {
		// Fallback to find if tree not installed
		cmd := exec.Command("find", ".", "-maxdepth", strconv.Itoa(maxDepth), "-type", "d", "-not", "-path", "*/.*", "-not", "-path", "*/node_modules/*", "-not", "-path", "*/vendor/*")
		cmd.Dir = root
		output, err = cmd.Output()
		if err != nil {
			return "", err
		}
	}
	return string(output), nil
}

// estimateTokens provides a rough token estimate.
func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return (len(s) + 3) / 4
}

// gitOutput runs a git command and returns output.
func gitOutput(args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"--no-pager"}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// gitOutputLimited runs git with output limit.
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

// readFileLimited reads a file up to maxBytes.
func readFileLimited(path string, maxBytes int) (string, error) {
	cmd := exec.Command("head", "-c", strconv.Itoa(maxBytes), path)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(output), nil
}
