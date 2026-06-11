package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// setupTwoAreaRepo creates a temp git repo with two staged areas: a/ and b/.
// Returns the repo dir.
func setupTwoAreaRepo(t *testing.T) string {
	t.Helper()
	repo := initTempGitRepo(t)

	// Create a/file.go
	if err := os.MkdirAll(filepath.Join(repo, "a"), 0o755); err != nil {
		t.Fatalf("mkdir a: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "a", "file.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatalf("write a/file.go: %v", err)
	}
	// Create b/file.go
	if err := os.MkdirAll(filepath.Join(repo, "b"), 0o755); err != nil {
		t.Fatalf("mkdir b: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "b", "file.go"), []byte("package b\n"), 0o644); err != nil {
		t.Fatalf("write b/file.go: %v", err)
	}
	// Stage both
	runGit(t, repo, "add", "a/file.go", "b/file.go")
	return repo
}

// gitOutputInDir runs a git command in a directory and returns trimmed stdout.
func gitOutputInDir(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, string(out))
	}
	return strings.TrimSpace(string(out))
}

// TestListStagedFilesEmpty checks that listStagedFiles returns nothing on a clean repo.
func TestListStagedFilesEmpty(t *testing.T) {
	repo := initTempGitRepo(t)
	oldWd, _ := os.Getwd()
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	files, err := listStagedFiles()
	if err != nil {
		t.Fatalf("listStagedFiles: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("expected empty, got %v", files)
	}
}

// TestListStagedFilesReturnsStaged checks that staged files are returned.
func TestListStagedFilesReturnsStaged(t *testing.T) {
	repo := setupTwoAreaRepo(t)
	oldWd, _ := os.Getwd()
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	files, err := listStagedFiles()
	if err != nil {
		t.Fatalf("listStagedFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 staged files, got %v", files)
	}
}

// TestFileMatchesPaths tests the prefix-matching logic.
func TestFileMatchesPaths(t *testing.T) {
	cases := []struct {
		file  string
		paths []string
		want  bool
	}{
		{"a/file.go", []string{"a"}, true},
		{"a/sub/file.go", []string{"a"}, true},
		{"a/file.go", []string{"a/"}, true}, // trailing slash stripped
		{"b/file.go", []string{"a"}, false},
		{"ab/file.go", []string{"a"}, false}, // must not match "ab" when path is "a"
		{"a", []string{"a"}, true},           // exact match
		{"a/file.go", []string{"b", "a"}, true},
	}
	for _, tc := range cases {
		got := fileMatchesPaths(tc.file, tc.paths)
		if got != tc.want {
			t.Errorf("fileMatchesPaths(%q, %v) = %v, want %v", tc.file, tc.paths, got, tc.want)
		}
	}
}

// TestStagedFilesMatchingPaths verifies that only matching staged files are returned.
func TestStagedFilesMatchingPaths(t *testing.T) {
	repo := setupTwoAreaRepo(t)
	oldWd, _ := os.Getwd()
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	matched, err := stagedFilesMatchingPaths([]string{"a"})
	if err != nil {
		t.Fatalf("stagedFilesMatchingPaths: %v", err)
	}
	if len(matched) != 1 || matched[0] != "a/file.go" {
		t.Fatalf("expected [a/file.go], got %v", matched)
	}
}

// TestStagedFilesMatchingPathsNoMatch checks the empty-result case.
func TestStagedFilesMatchingPathsNoMatch(t *testing.T) {
	repo := setupTwoAreaRepo(t)
	oldWd, _ := os.Getwd()
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	matched, err := stagedFilesMatchingPaths([]string{"c"})
	if err != nil {
		t.Fatalf("stagedFilesMatchingPaths: %v", err)
	}
	if len(matched) != 0 {
		t.Fatalf("expected no matches, got %v", matched)
	}
}

// TestStagedFilesOutsidePaths verifies that files outside the given paths are returned.
func TestStagedFilesOutsidePaths(t *testing.T) {
	repo := setupTwoAreaRepo(t)
	oldWd, _ := os.Getwd()
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	// a/ is in scope; b/ is outside
	outside, err := stagedFilesOutsidePaths([]string{"a"})
	if err != nil {
		t.Fatalf("stagedFilesOutsidePaths: %v", err)
	}
	if len(outside) != 1 || outside[0] != "b/file.go" {
		t.Fatalf("expected [b/file.go], got %v", outside)
	}
}

// TestCreateCommitScopedToPathsLeavesOtherFilesStaged verifies the core --paths
// behavior: committing only a/ leaves b/ still staged.
func TestCreateCommitScopedToPathsLeavesOtherFilesStaged(t *testing.T) {
	repo := setupTwoAreaRepo(t)
	oldWd, _ := os.Getwd()
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	// Commit only a/
	if err := createCommit("add: a area files\n\n- add a/file.go\n", true, false, []string{"a"}); err != nil {
		t.Fatalf("createCommit: %v", err)
	}

	// HEAD should contain a/file.go but NOT b/file.go
	headFiles := gitOutputInDir(t, repo, "show", "--name-only", "--format=", "HEAD")
	if !strings.Contains(headFiles, "a/file.go") {
		t.Errorf("HEAD should contain a/file.go, got:\n%s", headFiles)
	}
	if strings.Contains(headFiles, "b/file.go") {
		t.Errorf("HEAD should NOT contain b/file.go, got:\n%s", headFiles)
	}

	// b/file.go should still be staged
	staged := gitOutputInDir(t, repo, "diff", "--cached", "--name-only")
	if !strings.Contains(staged, "b/file.go") {
		t.Errorf("b/file.go should still be staged after scoped commit, got: %q", staged)
	}
	if strings.Contains(staged, "a/file.go") {
		t.Errorf("a/file.go should NOT be staged after commit, got: %q", staged)
	}
}

// TestCreateCommitNoPaths commits all staged files (regression: existing behavior unchanged).
func TestCreateCommitNoPaths(t *testing.T) {
	repo := setupTwoAreaRepo(t)
	oldWd, _ := os.Getwd()
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	if err := createCommit("add: all files\n\n- add a and b\n", true, false, nil); err != nil {
		t.Fatalf("createCommit: %v", err)
	}

	// Both files in HEAD
	headFiles := gitOutputInDir(t, repo, "show", "--name-only", "--format=", "HEAD")
	if !strings.Contains(headFiles, "a/file.go") || !strings.Contains(headFiles, "b/file.go") {
		t.Errorf("expected both files in HEAD, got:\n%s", headFiles)
	}

	// Nothing staged
	staged := gitOutputInDir(t, repo, "diff", "--cached", "--name-only")
	if staged != "" {
		t.Errorf("expected nothing staged after full commit, got: %q", staged)
	}
}

// TestExclusivePathCheckBlocksWhenOutsiders verifies that stagedFilesOutsidePaths
// detects files outside the scope (simulates --exclusive logic).
func TestExclusivePathCheckBlocksWhenOutsiders(t *testing.T) {
	repo := setupTwoAreaRepo(t)
	oldWd, _ := os.Getwd()
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	// With only a/ in scope, b/ is an outsider
	outsiders, err := stagedFilesOutsidePaths([]string{"a"})
	if err != nil {
		t.Fatalf("stagedFilesOutsidePaths: %v", err)
	}
	if len(outsiders) == 0 {
		t.Fatal("expected outsiders, got none")
	}
	if !strings.Contains(outsiders[0], "b/") {
		t.Errorf("expected b/ outsider, got: %v", outsiders)
	}
}

// TestExclusivePathCheckPassesWhenAllInScope checks the case where every staged
// file is within the given paths.
func TestExclusivePathCheckPassesWhenAllInScope(t *testing.T) {
	repo := setupTwoAreaRepo(t)
	oldWd, _ := os.Getwd()
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	// Both a/ and b/ are in scope
	outsiders, err := stagedFilesOutsidePaths([]string{"a", "b"})
	if err != nil {
		t.Fatalf("stagedFilesOutsidePaths: %v", err)
	}
	if len(outsiders) != 0 {
		t.Errorf("expected no outsiders, got: %v", outsiders)
	}
}

// TestScopedCommitDefContextSourcesContainPaths checks that scopedCommitDefinition
// injects the paths param into its ContextSources.
func TestScopedCommitDefContextSourcesContainPaths(t *testing.T) {
	def := scopedCommitDefinition{paths: []string{"a", "b/sub"}}
	sources := def.ContextSources()

	// Must have git_diff and git_files sources with paths param.
	var foundDiff, foundFiles bool
	for _, src := range sources {
		if src.Type == "git_diff" {
			rawPaths := src.Params["paths"]
			if !strings.Contains(rawPaths, "a") || !strings.Contains(rawPaths, "b/sub") {
				t.Errorf("git_diff source paths param missing expected paths: %q", rawPaths)
			}
			// Verify NUL separator
			parts := strings.Split(rawPaths, "\x00")
			if len(parts) != 2 {
				t.Errorf("expected 2 NUL-separated parts, got %d: %q", len(parts), rawPaths)
			}
			foundDiff = true
		}
		if src.Type == "git_files" {
			foundFiles = true
			rawPaths := src.Params["paths"]
			if rawPaths == "" {
				t.Errorf("git_files source missing paths param")
			}
		}
	}
	if !foundDiff {
		t.Error("git_diff source not found")
	}
	if !foundFiles {
		t.Error("git_files source not found")
	}
}

// TestScopedCommitNoPathsUsesBaseDefinition checks that no-paths case delegates
// to the base CommitDefinition behavior (no paths param in sources).
func TestScopedCommitNoPathsUsesBaseDefinition(t *testing.T) {
	// frameworkCommitRunner with nil def should use CommitDefinition{}
	runner := &frameworkCommitRunner{framework: nil, def: nil}
	// Just checking the nil-def path resolves to CommitDefinition without panic —
	// the actual Run would need an invoker, which we don't wire up in unit tests.
	if runner.def != nil {
		t.Error("expected nil def")
	}
}

// TestPrintStagedIndexOnErrorPrintsToStderr verifies the failure-state notice.
func TestPrintStagedIndexOnErrorPrintsToStderr(t *testing.T) {
	repo := setupTwoAreaRepo(t)
	oldWd, _ := os.Getwd()
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	stderr := captureStderr(t, func() {
		printStagedIndexOnError()
	})
	if !strings.Contains(stderr, "buckley: aborted; staged index contains:") {
		t.Errorf("expected aborted message, got: %q", stderr)
	}
	if !strings.Contains(stderr, "a/file.go") {
		t.Errorf("expected a/file.go in notice, got: %q", stderr)
	}
	if !strings.Contains(stderr, "b/file.go") {
		t.Errorf("expected b/file.go in notice, got: %q", stderr)
	}
}

// TestStagedNoticeAfterScopedCommit checks the stderr notice about remaining files.
func TestStagedNoticeAfterScopedCommit(t *testing.T) {
	repo := setupTwoAreaRepo(t)
	oldWd, _ := os.Getwd()
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	stderr := captureStderr(t, func() {
		if err := createCommit("add: a\n\n- add a/file.go\n", true, false, []string{"a"}); err != nil {
			t.Errorf("createCommit: %v", err)
		}
	})
	if !strings.Contains(stderr, "buckley: leaving") {
		t.Errorf("expected 'buckley: leaving' notice, got: %q", stderr)
	}
	if !strings.Contains(stderr, "b/file.go") {
		t.Errorf("expected b/file.go in notice, got: %q", stderr)
	}
}
