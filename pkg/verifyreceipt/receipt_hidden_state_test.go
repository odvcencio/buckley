package verifyreceipt

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRecord_RejectsHiddenTrackedChanges(t *testing.T) {
	for _, flag := range []string{"--assume-unchanged", "--skip-worktree"} {
		t.Run(flag, func(t *testing.T) {
			repo, store := newRepo(t), newStore(t)
			cmd := exec.Command("git", "-C", repo, "update-index", flag, "a.txt")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("set index flag: %v %s", err, out)
			}
			if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("two\n"), 0600); err != nil {
				t.Fatal(err)
			}
			r, err := Record(t.Context(), store, RecordOptions{Dir: repo, Argv: []string{"sh", "-c", "grep -q two a.txt"}, Paths: []string{"**"}})
			if err == nil {
				t.Fatalf("verification of hidden modified content was recorded for HEAD: status=%s tree=%s", r.Status, r.Tree)
			}
		})
	}
}

func TestRecord_CleanHiddenPathsAndSplitIndex(t *testing.T) {
	for _, split := range []bool{false, true} {
		for _, flag := range []string{"--assume-unchanged", "--skip-worktree"} {
			t.Run(fmt.Sprintf("split=%t/%s", split, flag), func(t *testing.T) {
				repo, store := newRepo(t), newStore(t)
				if split {
					if out, err := exec.Command("git", "-C", repo, "update-index", "--split-index").CombinedOutput(); err != nil {
						t.Fatalf("split index: %v %s", err, out)
					}
				}
				if out, err := exec.Command("git", "-C", repo, "update-index", flag, "a.txt").CombinedOutput(); err != nil {
					t.Fatalf("index flag: %v %s", err, out)
				}
				before, err := exec.Command("git", "-C", repo, "ls-files", "-v", "--stage", "-z").Output()
				if err != nil {
					t.Fatal(err)
				}
				receipt, err := Record(t.Context(), store, RecordOptions{Dir: repo, Argv: []string{"sh", "-c", "grep -q one a.txt"}, Paths: []string{"**"}})
				if err != nil || receipt.Status != StatusPass {
					t.Fatalf("clean hidden path rejected: %+v err=%v", receipt, err)
				}
				after, err := exec.Command("git", "-C", repo, "ls-files", "-v", "--stage", "-z").Output()
				if err != nil || !bytes.Equal(before, after) {
					t.Fatalf("real index changed: before=%q after=%q err=%v", before, after, err)
				}
				if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("two\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if _, err := Record(t.Context(), store, RecordOptions{Dir: repo, Argv: []string{"true"}, Paths: []string{"**"}}); err == nil {
					t.Fatal("split/hidden source change accepted")
				}
			})
		}
	}
}

func TestRecord_HiddenContentCannotReuseCachedStat(t *testing.T) {
	repo, store := newRepo(t), newStore(t)
	for _, args := range [][]string{{"config", "core.trustctime", "false"}, {"config", "core.checkstat", "minimal"}, {"update-index", "--assume-unchanged", "a.txt"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	path := filepath.Join(repo, "a.txt")
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("two\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, stat.ModTime(), stat.ModTime()); err != nil {
		t.Fatal(err)
	}
	if _, err := Record(t.Context(), store, RecordOptions{Dir: repo, Argv: []string{"true"}, Paths: []string{"**"}}); err == nil {
		t.Fatal("hidden changed content reused cached file stat")
	}
}

func TestRecord_GitStatusConfigCannotHideUntrackedSource(t *testing.T) {
	repo, store := newRepo(t), newStore(t)
	if out, err := exec.Command("git", "-C", repo, "config", "status.showUntrackedFiles", "no").CombinedOutput(); err != nil {
		t.Fatalf("git config: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repo, "untracked.go"), []byte("package hidden\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Record(t.Context(), store, RecordOptions{Dir: repo, Argv: []string{"true"}, Paths: []string{"**"}}); err == nil {
		t.Fatal("untracked source hidden by config was certified for HEAD")
	}
}

func TestRecord_RefusesCommandThatChangesHiddenSource(t *testing.T) {
	repo, store := newRepo(t), newStore(t)
	if out, err := exec.Command("git", "-C", repo, "update-index", "--skip-worktree", "a.txt").CombinedOutput(); err != nil {
		t.Fatalf("index flag: %v %s", err, out)
	}
	if receipt, err := Record(t.Context(), store, RecordOptions{Dir: repo, Argv: []string{"sh", "-c", `printf 'two\n' > a.txt`}, Paths: []string{"**"}}); err == nil || receipt.ID != "" {
		t.Fatalf("hidden command mutation recorded: %+v err=%v", receipt, err)
	}
}

func TestRecord_HiddenPathWithWhitespacePreservesIndex(t *testing.T) {
	repo, store := newRepo(t), newStore(t)
	name := "line\n spaced.txt"
	run := func(args ...string) []byte {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return out
	}
	if err := os.WriteFile(filepath.Join(repo, name), []byte("one\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run("add", "--", name)
	run("commit", "-qm", "whitespace path")
	run("update-index", "--assume-unchanged", name)
	before := run("ls-files", "-v", "--stage", "-z")
	if _, err := Record(t.Context(), store, RecordOptions{Dir: repo, Argv: []string{"true"}, Paths: []string{"**"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, name), []byte("two\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Record(t.Context(), store, RecordOptions{Dir: repo, Argv: []string{"true"}, Paths: []string{"**"}}); err == nil {
		t.Fatal("newline/space path change was not detected")
	}
	if after := run("ls-files", "-v", "--stage", "-z"); !bytes.Equal(before, after) {
		t.Fatalf("real index changed: before=%q after=%q", before, after)
	}
}
