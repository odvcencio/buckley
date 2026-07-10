package model

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCaptureReviewSnapshotIndexExcludesUnstagedFix(t *testing.T) {
	repo := initReviewSnapshotRepo(t)
	tracked := filepath.Join(repo, "behavior.txt")

	if err := os.WriteFile(tracked, []byte("staged bug\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCodexProviderGit(t, repo, "add", "behavior.txt")
	if err := os.WriteFile(tracked, []byte("unstaged fix\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	snapshot, err := CaptureReviewSnapshot(context.Background(), repo, ReviewSnapshotPolicy{Mode: ReviewSnapshotIndex})
	if err != nil {
		t.Fatalf("CaptureReviewSnapshot: %v", err)
	}
	isolated, cleanup, err := prepareCodexReviewWorkspace(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("prepareCodexReviewWorkspace: %v", err)
	}
	t.Cleanup(cleanup)

	assertCodexProviderFileContent(t, filepath.Join(isolated, "behavior.txt"), "staged bug\n")
	assertCodexProviderFileContent(t, tracked, "unstaged fix\n")
}

func TestCaptureReviewSnapshotTrackedWorktreeIncludesStagedAndUnstaged(t *testing.T) {
	repo := initReviewSnapshotRepo(t)
	tracked := filepath.Join(repo, "behavior.txt")

	if err := os.WriteFile(tracked, []byte("staged state\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCodexProviderGit(t, repo, "add", "behavior.txt")
	if err := os.WriteFile(tracked, []byte("working state\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stagedAddition := filepath.Join(repo, "staged.txt")
	if err := os.WriteFile(stagedAddition, []byte("staged addition\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCodexProviderGit(t, repo, "add", "staged.txt")
	if err := os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("must not leak\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	snapshot, err := CaptureReviewSnapshot(context.Background(), repo, ReviewSnapshotPolicy{Mode: ReviewSnapshotTrackedWorktree})
	if err != nil {
		t.Fatalf("CaptureReviewSnapshot: %v", err)
	}
	isolated, cleanup, err := prepareCodexReviewWorkspace(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("prepareCodexReviewWorkspace: %v", err)
	}
	t.Cleanup(cleanup)

	assertCodexProviderFileContent(t, filepath.Join(isolated, "behavior.txt"), "working state\n")
	assertCodexProviderFileContent(t, filepath.Join(isolated, "staged.txt"), "staged addition\n")
	refs := runCodexProviderGit(t, isolated, "for-each-ref", "--format=%(refname)")
	if refs != "" {
		t.Fatalf("mutable refs leaked into isolated review repository: %q", refs)
	}
	remotes := runCodexProviderGit(t, isolated, "remote")
	if remotes != "" {
		t.Fatalf("remote leaked into isolated review repository: %q", remotes)
	}
	if _, err := os.Stat(filepath.Join(isolated, "untracked.txt")); !os.IsNotExist(err) {
		t.Fatalf("untracked file leaked into exact snapshot: %v", err)
	}
}

func TestCaptureReviewSnapshotExpectedCommitFailsClosed(t *testing.T) {
	repo := initReviewSnapshotRepo(t)
	_, err := CaptureReviewSnapshot(context.Background(), repo, ReviewSnapshotPolicy{
		Mode:           ReviewSnapshotHead,
		ExpectedCommit: "0000000000000000000000000000000000000000",
	})
	if err == nil {
		t.Fatal("CaptureReviewSnapshot accepted unavailable expected commit")
	}
}

func TestPrepareReviewWorkspaceRejectsEscapingTrackedSymlink(t *testing.T) {
	repo := initReviewSnapshotRepo(t)
	if err := os.Symlink("../outside-secret.txt", filepath.Join(repo, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	runCodexProviderGit(t, repo, "add", "escape")
	runCodexProviderGit(t, repo, "commit", "-m", "escaping symlink")
	snapshot, err := CaptureReviewSnapshot(context.Background(), repo, ReviewSnapshotPolicy{Mode: ReviewSnapshotHead})
	if err != nil {
		t.Fatalf("CaptureReviewSnapshot: %v", err)
	}
	if _, cleanup, err := PrepareReviewWorkspace(context.Background(), snapshot); err == nil {
		if cleanup != nil {
			cleanup()
		}
		t.Fatal("materialized review accepted a tracked symlink escaping the repository")
	}
}

func TestPrepareReviewWorkspacePreservesNestedWorkDirInsideSnapshot(t *testing.T) {
	repo := initReviewSnapshotRepo(t)
	nested := filepath.Join(repo, "nested", "pkg")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "code.go"), []byte("package pkg\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCodexProviderGit(t, repo, "add", "nested/pkg/code.go")
	runCodexProviderGit(t, repo, "commit", "-m", "nested package")
	snapshot, err := CaptureReviewSnapshot(context.Background(), nested, ReviewSnapshotPolicy{Mode: ReviewSnapshotHead})
	if err != nil {
		t.Fatalf("CaptureReviewSnapshot: %v", err)
	}
	workDir, cleanup, err := PrepareReviewWorkspace(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("PrepareReviewWorkspace: %v", err)
	}
	t.Cleanup(cleanup)
	root, err := ReviewWorkspaceRepositoryRoot(context.Background(), workDir)
	if err != nil {
		t.Fatalf("ReviewWorkspaceRepositoryRoot: %v", err)
	}
	if workDir != filepath.Join(root, "nested", "pkg") {
		t.Fatalf("materialized workdir = %q, want nested path under %q", workDir, root)
	}
}

func initReviewSnapshotRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runCodexProviderGit(t, repo, "init")
	runCodexProviderGit(t, repo, "config", "user.email", "test@example.com")
	runCodexProviderGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "behavior.txt"), []byte("committed state\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCodexProviderGit(t, repo, "add", "behavior.txt")
	runCodexProviderGit(t, repo, "commit", "-m", "initial")
	return repo
}
