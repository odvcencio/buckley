package model

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	if snapshot.UntrackedFiles() != nil {
		t.Fatal("tracked-worktree snapshot exposed an untracked capture marker")
	}
}

func TestCaptureReviewSnapshotWorktreeIncludesReviewableUntrackedSource(t *testing.T) {
	repo := initReviewSnapshotRepo(t)
	trackedFiles := map[string]string{
		"go.mod":       "module example.test/review-snapshot\n\ngo 1.24\n",
		"z_compile.go": "package snapshot\n\nfunc Value() int { return untrackedValue() }\n",
		".gitignore":   "ignored.go\n",
	}
	for path, content := range trackedFiles {
		if err := os.WriteFile(filepath.Join(repo, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runCodexProviderGit(t, repo, "add", "go.mod", "z_compile.go", ".gitignore")
	runCodexProviderGit(t, repo, "commit", "-m", "add compilable package")
	if err := os.WriteFile(filepath.Join(repo, "z_compile.go"), []byte("package snapshot\n\n// tracked worktree edit\nfunc Value() int { return untrackedValue() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	untrackedSource := "package snapshot\n\nfunc untrackedValue() int { return 42 }\n"
	if err := os.WriteFile(filepath.Join(repo, "a_helper.go"), []byte(untrackedSource), 0o644); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string][]byte{
		"ignored.go":            []byte("package snapshot\nfunc untrackedValue() int { return 0 }\n"),
		".env":                  []byte("TOKEN=must-not-leak\n"),
		".env.production.local": []byte("TOKEN=production-must-not-leak\n"),
		"asset.bin":             {0xff, 0x00, 0x01},
		"AGENTS.md":             []byte("untracked instructions must not leak\n"),
		"credentials.json":      []byte(`{"token":"must-not-leak"}`),
		"credentials.txt":       []byte("plaintext-credentials-must-not-leak\n"),
		".git-credentials":      []byte("https://user:secret@example.test\n"),
		".docker/config.json":   []byte(`{"auths":{"example.test":{"auth":"must-not-leak"}}}`),
		"control.go":            []byte("package snapshot\n// \x1b[31mterminal control\n"),
		"invalid.go":            {0xff, 0xfe, 'g', 'o'},
	} {
		fullPath := filepath.Join(repo, path)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	policy := ReviewSnapshotPolicy{Mode: ReviewSnapshotWorktree, UntrackedPaths: []string{"a_helper.go"}}
	snapshot, err := CaptureReviewSnapshot(context.Background(), repo, policy)
	if err != nil {
		t.Fatalf("CaptureReviewSnapshot: %v", err)
	}
	patch := string(snapshot.Patch())
	if !strings.Contains(patch, "a_helper.go") || !strings.Contains(patch, "untrackedValue") || !strings.Contains(patch, "tracked worktree edit") {
		t.Fatalf("worktree snapshot omitted untracked source:\n%s", patch)
	}
	for _, forbidden := range []string{"ignored.go", "TOKEN=must-not-leak", "production-must-not-leak", "plaintext-credentials-must-not-leak", "user:secret", "terminal control", "asset.bin", "invalid.go", "untracked instructions must not leak", "credentials.json"} {
		if strings.Contains(patch, forbidden) {
			t.Fatalf("worktree snapshot exposed excluded untracked content %q:\n%s", forbidden, patch)
		}
	}
	captured := snapshot.UntrackedFiles()
	if len(captured) != 1 || captured[0].Path != "a_helper.go" || !strings.Contains(string(captured[0].Patch), "untrackedValue") {
		t.Fatalf("captured untracked evidence = %#v, want only a_helper.go", captured)
	}
	captured[0].Path = "mutated.go"
	captured[0].Patch[0] ^= 0xff
	again := snapshot.UntrackedFiles()
	if len(again) != 1 || again[0].Path != "a_helper.go" || !strings.Contains(string(again[0].Patch), "untrackedValue") {
		t.Fatal("snapshot untracked evidence was mutable through its accessor")
	}
	if err := os.WriteFile(filepath.Join(repo, "a_helper.go"), []byte("package snapshot\n\nfunc untrackedValue() int { return 99 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	newer, err := CaptureReviewSnapshot(context.Background(), repo, policy)
	if err != nil {
		t.Fatalf("recapture changed worktree: %v", err)
	}
	if newer.ID() == snapshot.ID() {
		t.Fatal("untracked source change did not alter immutable snapshot identity")
	}

	isolated, cleanup, err := PrepareReviewWorkspace(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("PrepareReviewWorkspace: %v", err)
	}
	t.Cleanup(cleanup)
	assertCodexProviderFileContent(t, filepath.Join(isolated, "a_helper.go"), untrackedSource)
	for _, path := range []string{"ignored.go", ".env", ".env.production.local", "asset.bin", "AGENTS.md", "credentials.json", "credentials.txt", ".git-credentials", ".docker/config.json", "control.go", "invalid.go"} {
		if _, err := os.Stat(filepath.Join(isolated, path)); !os.IsNotExist(err) {
			t.Fatalf("excluded untracked file %q materialized: %v", path, err)
		}
	}

	cmd := exec.Command("go", "test", ".")
	cmd.Dir = isolated
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("materialized worktree did not compile with untracked source: %v\n%s", err, output)
	}
}

func TestCaptureReviewSnapshotWorktreeRequiresExplicitUntrackedAllowlist(t *testing.T) {
	repo := initReviewSnapshotRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "config.local.yaml"), []byte("password: ordinary-name-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CaptureReviewSnapshot(context.Background(), repo, ReviewSnapshotPolicy{Mode: ReviewSnapshotWorktree}); err == nil {
		t.Fatal("worktree snapshot captured untracked content without an explicit path allowlist")
	}
	if _, err := CaptureReviewSnapshot(context.Background(), repo, ReviewSnapshotPolicy{
		Mode:           ReviewSnapshotWorktree,
		UntrackedPaths: []string{"config.local.yaml"},
	}); err != nil {
		t.Fatalf("explicit ordinary-named allowlisted path was not capturable: %v", err)
	}
}

func TestExcludeReviewUntrackedPathRejectsInjectionAndSecretLocations(t *testing.T) {
	tests := []struct {
		path    string
		exclude bool
	}{
		{path: "src/helper.go", exclude: false},
		{path: "sample.env", exclude: false},
		{path: "example.env", exclude: false},
		{path: ".env.example", exclude: false},
		{path: ".env.production.local", exclude: true},
		{path: "production.env", exclude: true},
		{path: "service.env.local", exclude: true},
		{path: "secrets/prod.yaml", exclude: true},
		{path: "credentials/token.txt", exclude: true},
		{path: "credentials.txt", exclude: true},
		{path: "app-credential.toml", exclude: true},
		{path: "access-token.txt", exclude: true},
		{path: "database-password.ini", exclude: true},
		{path: ".git-credentials", exclude: true},
		{path: ".docker/config.json", exclude: true},
		{path: "src/credentials.go", exclude: false},
		{path: "src/token.go", exclude: false},
		{path: "src/control\x01.go", exclude: true},
		{path: "src/delete\x7f.go", exclude: true},
		{path: "src/bidi\u202e.go", exclude: true},
		{path: string([]byte{'s', 'r', 'c', '/', 0xff, '.', 'g', 'o'}), exclude: true},
	}
	for _, test := range tests {
		if got := excludeReviewUntrackedPath(test.path); got != test.exclude {
			t.Errorf("excludeReviewUntrackedPath(%q) = %v, want %v", test.path, got, test.exclude)
		}
	}
}

func TestReadStableReviewUntrackedFileRejectsSymlinkEscape(t *testing.T) {
	repo := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.go")
	if err := os.WriteFile(outsideFile, []byte("package stolen\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	expected, err := os.Stat(outsideFile)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	if _, err := readStableReviewUntrackedFile(root, filepath.Join("escape", "secret.go"), expected); err == nil {
		t.Fatal("rooted untracked capture followed a symlink outside the repository")
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
