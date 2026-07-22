package oneshot

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/rlm"
)

func TestFormatIncompleteRLMResponseRetainsCompletedEvidence(t *testing.T) {
	result := &rlm.SubAgentResult{
		Summary:      "Inspected the sharding contract.",
		InputTokens:  120,
		OutputTokens: 30,
		TokensUsed:   150,
		ToolCalls: []rlm.SubAgentToolCall{{
			Name:      "search_text",
			Arguments: `{"query":"race_root"}`,
			Result:    "found aggregate gate",
			Success:   true,
		}},
	}

	got := formatIncompleteRLMResponse(result, errors.Join(context.DeadlineExceeded, errors.New("provider still working")))
	for _, want := range []string{"Incomplete agent result", "not a completed or validated result", "Inspected the sharding contract", "search_text", "found aggregate gate", "120 input", "1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("salvage output missing %q:\n%s", want, got)
		}
	}
	if !strings.HasSuffix(got, "\n") {
		t.Fatal("salvage output must end with newline")
	}
}

func TestReviewSnapshotRegistryReadsOnlyMaterializedState(t *testing.T) {
	repo := t.TempDir()
	runReviewRegistryGit(t, repo, "init", "-q")
	runReviewRegistryGit(t, repo, "config", "user.email", "test@example.com")
	runReviewRegistryGit(t, repo, "config", "user.name", "Test User")
	tracked := filepath.Join(repo, "behavior.txt")
	if err := os.WriteFile(tracked, []byte("captured behavior\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runReviewRegistryGit(t, repo, "add", "behavior.txt")
	runReviewRegistryGit(t, repo, "commit", "-m", "initial")

	snapshot, err := model.CaptureReviewSnapshot(context.Background(), repo, model.ReviewSnapshotPolicy{Mode: model.ReviewSnapshotHead})
	if err != nil {
		t.Fatalf("CaptureReviewSnapshot: %v", err)
	}
	if err := os.WriteFile(tracked, []byte("newer live behavior\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	untracked := filepath.Join(repo, "untracked-secret.txt")
	if err := os.WriteFile(untracked, []byte("untracked secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	workDir, cleanup, err := model.PrepareReviewWorkspace(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("PrepareReviewWorkspace: %v", err)
	}
	t.Cleanup(cleanup)
	root, err := model.ReviewWorkspaceRepositoryRoot(context.Background(), workDir)
	if err != nil {
		t.Fatalf("ReviewWorkspaceRepositoryRoot: %v", err)
	}
	registry, err := newReviewSnapshotRegistry(root, []string{"read_file", "find_files", "search_text"})
	if err != nil {
		t.Fatalf("newReviewSnapshotRegistry: %v", err)
	}

	read, err := registry.Execute("read_file", map[string]any{"path": "behavior.txt"})
	if err != nil || !read.Success || !strings.Contains(read.Data["content"].(string), "captured behavior") {
		t.Fatalf("snapshot read = %#v, err=%v", read, err)
	}
	for _, path := range []string{"untracked-secret.txt", untracked} {
		outside, execErr := registry.Execute("read_file", map[string]any{"path": path})
		if execErr != nil {
			t.Fatalf("confined read %q: %v", path, execErr)
		}
		if outside.Success {
			t.Fatalf("confined read exposed %q: %#v", path, outside.Data)
		}
	}

	files, err := registry.Execute("find_files", map[string]any{"pattern": "*.txt", "base_path": "."})
	if err != nil || !files.Success {
		t.Fatalf("snapshot find_files = %#v, err=%v", files, err)
	}
	matches, _ := files.Data["matches"].([]string)
	if len(matches) != 1 || matches[0] != "behavior.txt" {
		t.Fatalf("snapshot file inventory = %#v, want only behavior.txt", matches)
	}

	search, err := registry.Execute("search_text", map[string]any{"query": "newer live|untracked secret", "path": "."})
	if err != nil || !search.Success {
		t.Fatalf("snapshot search_text = %#v, err=%v", search, err)
	}
	if count, _ := search.Data["count"].(int); count != 0 {
		t.Fatalf("snapshot search exposed excluded live state: %#v", search.Data)
	}
}

func TestReviewSnapshotRegistryRejectsNonReviewTools(t *testing.T) {
	if _, err := newReviewSnapshotRegistry(t.TempDir(), []string{"read_file", "run_shell"}); err == nil {
		t.Fatal("snapshot registry accepted an executable tool")
	}
}

func TestReviewSnapshotRegistryExplicitlyRegistersSealedVerification(t *testing.T) {
	root := t.TempDir()
	registry, err := newReviewSnapshotRegistry(root, []string{"read_file", "run_verification"}, "/usr/bin/true")
	if err != nil {
		t.Fatalf("newReviewSnapshotRegistry: %v", err)
	}
	verification, ok := registry.Get("run_verification")
	if !ok {
		t.Fatal("snapshot registry omitted explicitly allowed run_verification")
	}
	if _, mutable := verification.(interface{ SetWorkDir(string) }); mutable {
		t.Fatal("run_verification root can be rebound through generic SetWorkDir")
	}
	if got := registry.ToolKind("run_verification"); got != "execute" {
		t.Fatalf("run_verification kind = %q, want execute", got)
	}
}

func runReviewRegistryGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}
