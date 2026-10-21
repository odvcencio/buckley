package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/diffsignal"
)

func TestControllerGitDiff_PreservesStagedAndUnstagedChanges(t *testing.T) {
	root := createTUITestGitRepo(t)
	path := filepath.Join(root, "change.go")
	for _, step := range []struct {
		body   string
		staged bool
	}{
		{"package p\nfunc Value() int { return 1 }\n", true},
		{"package p\nfunc Value() int { return 2 }\n", false},
	} {
		if err := os.WriteFile(path, []byte(step.body), 0600); err != nil {
			t.Fatal(err)
		}
		if step.staged {
			runTUITestGit(t, root, "add", "change.go")
		}
	}
	c := &Controller{workDir: root}
	staged, err := c.getGitDiffStaged()
	if err != nil {
		t.Fatal(err)
	}
	if len(staged.Files) != 1 || !strings.Contains(shapeDiff(staged, diffsignal.CommitDiffBudget), "+func Value() int { return 1 }") {
		t.Fatal("staged source lost")
	}
	both, err := c.getGitDiff()
	if err != nil {
		t.Fatal(err)
	}
	out := shapeDiff(both, diffsignal.ReviewDiffBudget)
	if len(both.Files) != 2 || strings.Index(out, "+func Value() int { return 1 }") < 0 || strings.Index(out, "+func Value() int { return 2 }") < strings.Index(out, "+func Value() int { return 1 }") {
		t.Fatalf("staged/unstaged order lost: %s", out)
	}
}

func TestControllerGitDiff_ReportsGitFailure(t *testing.T) {
	c := &Controller{workDir: t.TempDir()}
	if diff, err := c.getGitDiff(); err == nil || diff != nil {
		t.Fatalf("failure hidden: %+v %v", diff, err)
	}
}
