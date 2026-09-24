package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func initStageTestRepo(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.name", "stage-test"},
		{"config", "user.email", "stage-test@example.com"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

func stagedNames(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command("git", "diff", "--cached", "--name-only").Output()
	if err != nil {
		t.Fatalf("git diff --cached: %v", err)
	}
	return strings.Fields(string(out))
}

func TestStageFilesTreatsDashPrefixedNamesAsPaths(t *testing.T) {
	initStageTestRepo(t)
	if err := os.WriteFile("-dash.txt", []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := stageFiles([]string{"-dash.txt"}, false, true); err != nil {
		t.Fatalf("stageFiles: %v", err)
	}
	if got := stagedNames(t); len(got) != 1 || got[0] != "-dash.txt" {
		t.Fatalf("staged = %v, want [-dash.txt]", got)
	}
}

func TestStageFilesReportsGitErrorText(t *testing.T) {
	initStageTestRepo(t)
	if err := os.WriteFile("a.txt", []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A message flag placed after "--" arrives as a path. It must fail as a
	// missing path with git's own explanation, not as an opaque exit status.
	err := stageFiles([]string{"a.txt", "-m", "add something"}, false, true)
	if err == nil {
		t.Fatal("stageFiles succeeded with a stray -m argument")
	}
	if !strings.Contains(err.Error(), "pathspec '-m' did not match any files") {
		t.Fatalf("error = %q, want git's pathspec message", err)
	}
}
