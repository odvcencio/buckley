package parallel

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// MergeOrchestrator handles merging completed agent worktrees back to a target branch.
// It automatically merges non-conflicting changes and pauses for human intervention on conflicts.
type MergeOrchestrator struct {
	repoPath string
}

// MergeResult captures the outcome of merging one branch.
type MergeResult struct {
	TaskID        string
	Branch        string
	Success       bool
	AutoMerged    bool     // True if merged without conflicts
	HasConflicts  bool     // True if merge had conflicts
	ConflictFiles []string // Files with conflicts
	MergeCommit   string   // Commit hash after merge
	Error         error
	Duration      time.Duration
}

// MergeReport summarizes the outcome of merging multiple branches.
type MergeReport struct {
	TargetBranch string
	TotalTasks   int
	Merged       int
	Conflicts    int
	Failed       int
	Results      []MergeResult
	Duration     time.Duration
}

// MergeStrategy defines how to handle merge conflicts.
type MergeStrategy int

const (
	// MergeStrategyPause stops and waits for human intervention on conflicts.
	MergeStrategyPause MergeStrategy = iota
	// MergeStrategySkip skips branches that would cause conflicts.
	MergeStrategySkip
	// MergeStrategyOurs uses "ours" strategy on conflicts.
	MergeStrategyOurs
	// MergeStrategyTheirs uses "theirs" strategy on conflicts.
	MergeStrategyTheirs
)

// MergeConfig configures the merge orchestrator.
type MergeConfig struct {
	TargetBranch   string
	Strategy       MergeStrategy
	DryRun         bool          // If true, check for conflicts but don't actually merge
	CleanupOnMerge bool          // If true, delete worktree branches after successful merge
	Timeout        time.Duration // Per-merge timeout
}

// DefaultMergeConfig returns sensible defaults.
func DefaultMergeConfig() MergeConfig {
	return MergeConfig{
		TargetBranch:   "main",
		Strategy:       MergeStrategyPause,
		DryRun:         false,
		CleanupOnMerge: false,
		Timeout:        5 * time.Minute,
	}
}

// NewMergeOrchestrator creates a new merge orchestrator.
func NewMergeOrchestrator(repoPath string) *MergeOrchestrator {
	return &MergeOrchestrator{repoPath: repoPath}
}

// MergeResults merges completed agent results back to the target branch.
// Results are merged in the provided order.
func (m *MergeOrchestrator) MergeResults(ctx context.Context, results []*AgentResult, cfg MergeConfig) (*MergeReport, error) {
	if strings.TrimSpace(cfg.TargetBranch) == "" {
		cfg.TargetBranch = DefaultMergeConfig().TargetBranch
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultMergeConfig().Timeout
	}
	if len(results) == 0 {
		return &MergeReport{TargetBranch: cfg.TargetBranch}, nil
	}

	start := time.Now()
	report := &MergeReport{
		TargetBranch: cfg.TargetBranch,
		TotalTasks:   len(results),
		Results:      make([]MergeResult, 0, len(results)),
	}

	// Ensure we're on target branch
	if err := m.checkout(ctx, cfg.TargetBranch); err != nil {
		return nil, fmt.Errorf("failed to checkout %s: %w", cfg.TargetBranch, err)
	}

	for _, result := range results {
		if result == nil {
			report.Results = append(report.Results, MergeResult{
				Success: false,
				Error:   fmt.Errorf("nil result"),
			})
			report.Failed++
			continue
		}
		if !result.Success {
			report.Results = append(report.Results, MergeResult{
				TaskID:  result.TaskID,
				Branch:  result.Branch,
				Success: false,
				Error:   fmt.Errorf("task failed, skipping merge"),
			})
			report.Failed++
			continue
		}

		if result.Branch == "" {
			report.Results = append(report.Results, MergeResult{
				TaskID:  result.TaskID,
				Success: false,
				Error:   fmt.Errorf("no branch specified"),
			})
			report.Failed++
			continue
		}

		mergeResult := m.mergeBranch(ctx, result, cfg)
		report.Results = append(report.Results, mergeResult)

		if mergeResult.Success {
			report.Merged++
		}
		if mergeResult.HasConflicts {
			report.Conflicts++
			if cfg.Strategy == MergeStrategyPause && !cfg.DryRun {
				// Stop processing on first conflict
				break
			}
		}
		if !mergeResult.Success && !mergeResult.HasConflicts {
			report.Failed++
		}
	}

	report.Duration = time.Since(start)
	return report, nil
}

// mergeBranch merges a single branch.
func (m *MergeOrchestrator) mergeBranch(ctx context.Context, result *AgentResult, cfg MergeConfig) MergeResult {
	start := time.Now()
	mergeResult := MergeResult{
		TaskID: result.TaskID,
		Branch: result.Branch,
	}

	mergeCtx := ctx
	cancel := func() {}
	if cfg.Timeout > 0 {
		mergeCtx, cancel = context.WithTimeout(ctx, cfg.Timeout)
	}
	defer cancel()

	// Check for conflicts first (dry run)
	hasConflicts, conflictFiles, err := m.checkMergeConflicts(mergeCtx, result.Branch)
	if err != nil {
		mergeResult.Error = err
		mergeResult.Duration = time.Since(start)
		return mergeResult
	}

	if hasConflicts {
		mergeResult.HasConflicts = true
		mergeResult.ConflictFiles = conflictFiles
	}

	if cfg.DryRun {
		mergeResult.Success = !hasConflicts
		mergeResult.AutoMerged = !hasConflicts
		mergeResult.Duration = time.Since(start)
		return mergeResult
	}

	commitMsg := fmt.Sprintf("Merge agent task %s from %s", result.TaskID, result.Branch)

	if hasConflicts {
		switch cfg.Strategy {
		case MergeStrategySkip:
			mergeResult.Error = fmt.Errorf("skipped due to conflicts")
			mergeResult.Duration = time.Since(start)
			return mergeResult

		case MergeStrategyPause:
			mergeResult.Error = fmt.Errorf("paused: conflicts in %s", strings.Join(conflictFiles, ", "))
			mergeResult.Duration = time.Since(start)
			return mergeResult

		case MergeStrategyOurs:
			commit, err := m.mergeWithStrategy(mergeCtx, result.Branch, commitMsg, "ours")
			if err != nil {
				mergeResult.Error = err
				mergeResult.Duration = time.Since(start)
				return mergeResult
			}
			mergeResult.MergeCommit = commit

		case MergeStrategyTheirs:
			commit, err := m.mergeWithStrategy(mergeCtx, result.Branch, commitMsg, "theirs")
			if err != nil {
				mergeResult.Error = err
				mergeResult.Duration = time.Since(start)
				return mergeResult
			}
			mergeResult.MergeCommit = commit
		}
	} else {
		commit, err := m.merge(mergeCtx, result.Branch, commitMsg)
		if err != nil {
			mergeResult.Error = err
			mergeResult.Duration = time.Since(start)
			return mergeResult
		}
		mergeResult.MergeCommit = commit
		mergeResult.AutoMerged = true
	}

	mergeResult.Success = true
	mergeResult.Duration = time.Since(start)

	// Cleanup branch if configured
	if cfg.CleanupOnMerge {
		_ = m.deleteBranch(ctx, result.Branch)
	}

	return mergeResult
}

// checkMergeConflicts checks if merging a branch would cause conflicts.
func (m *MergeOrchestrator) checkMergeConflicts(ctx context.Context, branch string) (bool, []string, error) {
	// Try merge with --no-commit --no-ff to check for conflicts
	cmd := exec.CommandContext(ctx, "git", "merge", "--no-commit", "--no-ff", branch)
	cmd.Dir = m.repoPath
	output, err := cmd.CombinedOutput()

	// Abort the merge regardless of outcome
	m.abortMerge()

	if err == nil {
		// No conflicts
		return false, nil, nil
	}

	// Check if it's a conflict error
	outputStr := string(output)
	if strings.Contains(outputStr, "CONFLICT") || strings.Contains(outputStr, "Automatic merge failed") {
		// Extract conflict files
		files := extractConflictFiles(outputStr)
		return true, files, nil
	}

	return false, nil, fmt.Errorf("merge check failed: %s", outputStr)
}

// merge performs the actual merge.
func (m *MergeOrchestrator) merge(ctx context.Context, branch, message string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "merge", "--no-ff", "-m", message, branch)
	cmd.Dir = m.repoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("merge failed: %s", string(output))
	}

	hash, err := m.headHash(ctx)
	if err != nil {
		return "", nil // Merge succeeded but couldn't get hash
	}

	return hash, nil
}

// mergeWithStrategy merges using a specific conflict resolution strategy.
func (m *MergeOrchestrator) mergeWithStrategy(ctx context.Context, branch, message, strategy string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "merge", "-X", strategy, "--no-ff", "-m", message, branch)
	cmd.Dir = m.repoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("merge with %s strategy failed: %s", strategy, string(output))
	}

	hash, err := m.headHash(ctx)
	if err != nil {
		return "", nil // Merge succeeded but couldn't get hash
	}

	return hash, nil
}

// checkout switches to a branch.
func (m *MergeOrchestrator) checkout(ctx context.Context, branch string) error {
	cmd := exec.CommandContext(ctx, "git", "checkout", branch)
	cmd.Dir = m.repoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("checkout failed: %s", string(output))
	}
	return nil
}

// deleteBranch deletes a branch.
func (m *MergeOrchestrator) deleteBranch(ctx context.Context, branch string) error {
	cmd := exec.CommandContext(ctx, "git", "branch", "-D", branch)
	cmd.Dir = m.repoPath
	_, err := cmd.CombinedOutput()
	return err
}

func (m *MergeOrchestrator) headHash(ctx context.Context) (string, error) {
	hashCmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	hashCmd.Dir = m.repoPath
	hashOutput, err := hashCmd.Output()
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(hashOutput)), nil
}

func (m *MergeOrchestrator) abortMerge() {
	abortCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	abortCmd := exec.CommandContext(abortCtx, "git", "merge", "--abort")
	abortCmd.Dir = m.repoPath
	_ = abortCmd.Run()
}

// extractConflictFiles parses git merge output to find conflicting files.
func extractConflictFiles(output string) []string {
	var files []string
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "CONFLICT") {
			// Extract filename from "CONFLICT (content): Merge conflict in path/to/file"
			if idx := strings.Index(line, "Merge conflict in "); idx >= 0 {
				file := strings.TrimSpace(line[idx+len("Merge conflict in "):])
				files = append(files, file)
			}
		}
	}
	return files
}

// MergeReportMarkdown generates a markdown report of the merge.
func (r *MergeReport) Markdown() string {
	var b strings.Builder

	b.WriteString("## Merge Report\n\n")
	b.WriteString(fmt.Sprintf("**Target Branch:** %s\n", r.TargetBranch))
	b.WriteString(fmt.Sprintf("**Duration:** %s\n\n", r.Duration.Round(time.Millisecond)))

	b.WriteString("### Summary\n\n")
	b.WriteString(fmt.Sprintf("| Metric | Count |\n"))
	b.WriteString(fmt.Sprintf("|--------|-------|\n"))
	b.WriteString(fmt.Sprintf("| Total Tasks | %d |\n", r.TotalTasks))
	b.WriteString(fmt.Sprintf("| Merged | %d |\n", r.Merged))
	b.WriteString(fmt.Sprintf("| Conflicts | %d |\n", r.Conflicts))
	b.WriteString(fmt.Sprintf("| Failed | %d |\n", r.Failed))
	b.WriteString("\n")

	if len(r.Results) > 0 {
		b.WriteString("### Details\n\n")
		b.WriteString("| Task | Branch | Status | Notes |\n")
		b.WriteString("|------|--------|--------|-------|\n")

		for _, result := range r.Results {
			status := "✗"
			notesParts := make([]string, 0, 2)

			if result.HasConflicts {
				status = "⚠"
				if len(result.ConflictFiles) > 0 {
					notesParts = append(notesParts, fmt.Sprintf("conflicts: %s", strings.Join(result.ConflictFiles, ", ")))
				}
			} else if result.Success {
				status = "✓"
			}

			if result.Success && result.MergeCommit != "" {
				notesParts = append(notesParts, fmt.Sprintf("commit: %s", truncateHash(result.MergeCommit)))
			}
			if result.Error != nil {
				notesParts = append(notesParts, result.Error.Error())
			}
			notes := strings.Join(notesParts, "; ")

			b.WriteString(fmt.Sprintf("| %s | %s | %s | %s |\n",
				result.TaskID, result.Branch, status, notes))
		}
	}

	return b.String()
}

func truncateHash(hash string) string {
	if len(hash) > 7 {
		return hash[:7]
	}
	return hash
}

// PreviewMerge does a dry-run merge check for all results.
func (m *MergeOrchestrator) PreviewMerge(ctx context.Context, results []*AgentResult, targetBranch string) (*MergeReport, error) {
	cfg := DefaultMergeConfig()
	cfg.TargetBranch = targetBranch
	cfg.DryRun = true
	return m.MergeResults(ctx, results, cfg)
}

// AutoMerge merges all non-conflicting results automatically.
func (m *MergeOrchestrator) AutoMerge(ctx context.Context, results []*AgentResult, targetBranch string) (*MergeReport, error) {
	cfg := DefaultMergeConfig()
	cfg.TargetBranch = targetBranch
	cfg.Strategy = MergeStrategySkip
	return m.MergeResults(ctx, results, cfg)
}
