package parallel

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func ensureGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}
}

func runGitMaybe(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	output, err := runGitMaybe(dir, args...)
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(output))
	}
	return strings.TrimSpace(string(output))
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func initTestRepo(t *testing.T) string {
	t.Helper()
	ensureGit(t)

	dir := t.TempDir()
	if _, err := runGitMaybe(dir, "init", "-b", "main"); err != nil {
		runGit(t, dir, "init")
		runGit(t, dir, "checkout", "-b", "main")
	}

	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")

	writeFile(t, filepath.Join(dir, "file.txt"), "base\n")
	runGit(t, dir, "add", "file.txt")
	runGit(t, dir, "commit", "-m", "base")

	return dir
}

func TestMergeOrchestrator_DryRun_NoChanges(t *testing.T) {
	repo := initTestRepo(t)

	runGit(t, repo, "checkout", "-b", "feature")
	writeFile(t, filepath.Join(repo, "feature.txt"), "feature\n")
	runGit(t, repo, "add", "feature.txt")
	runGit(t, repo, "commit", "-m", "feature")
	runGit(t, repo, "checkout", "main")

	headBefore := runGit(t, repo, "rev-parse", "HEAD")

	orch := NewMergeOrchestrator(repo)
	results := []*AgentResult{
		{TaskID: "task-1", Success: true, Branch: "feature"},
	}
	cfg := DefaultMergeConfig()
	cfg.TargetBranch = "main"
	cfg.DryRun = true

	report, err := orch.MergeResults(context.Background(), results, cfg)
	if err != nil {
		t.Fatalf("MergeResults() error = %v", err)
	}

	headAfter := runGit(t, repo, "rev-parse", "HEAD")
	if headAfter != headBefore {
		t.Errorf("HEAD changed during dry run: before=%s after=%s", headBefore, headAfter)
	}
	if report.Merged != 1 {
		t.Errorf("Merged = %d, want 1", report.Merged)
	}
	if report.Conflicts != 0 {
		t.Errorf("Conflicts = %d, want 0", report.Conflicts)
	}
	if len(report.Results) != 1 || !report.Results[0].Success {
		t.Errorf("expected successful dry-run result, got %+v", report.Results)
	}
}

func TestMergeOrchestrator_StrategyOurs_ResolvesConflicts(t *testing.T) {
	repo := initTestRepo(t)

	runGit(t, repo, "checkout", "-b", "feature")
	writeFile(t, filepath.Join(repo, "file.txt"), "feature\n")
	runGit(t, repo, "add", "file.txt")
	runGit(t, repo, "commit", "-m", "feature")

	runGit(t, repo, "checkout", "main")
	writeFile(t, filepath.Join(repo, "file.txt"), "main\n")
	runGit(t, repo, "add", "file.txt")
	runGit(t, repo, "commit", "-m", "main")

	orch := NewMergeOrchestrator(repo)
	results := []*AgentResult{
		{TaskID: "task-1", Success: true, Branch: "feature"},
	}
	cfg := DefaultMergeConfig()
	cfg.TargetBranch = "main"
	cfg.Strategy = MergeStrategyOurs

	report, err := orch.MergeResults(context.Background(), results, cfg)
	if err != nil {
		t.Fatalf("MergeResults() error = %v", err)
	}
	if report.Merged != 1 {
		t.Errorf("Merged = %d, want 1", report.Merged)
	}
	if report.Conflicts != 1 {
		t.Errorf("Conflicts = %d, want 1", report.Conflicts)
	}
	if len(report.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(report.Results))
	}

	result := report.Results[0]
	if !result.Success {
		t.Fatalf("merge result should succeed, got error: %v", result.Error)
	}
	if !result.HasConflicts {
		t.Error("merge result should record conflicts")
	}
	if result.MergeCommit == "" {
		t.Error("merge commit should be recorded")
	}

	content, err := os.ReadFile(filepath.Join(repo, "file.txt"))
	if err != nil {
		t.Fatalf("read merged file: %v", err)
	}
	if strings.TrimSpace(string(content)) != "main" {
		t.Errorf("expected ours strategy to keep main content, got %q", strings.TrimSpace(string(content)))
	}
}
