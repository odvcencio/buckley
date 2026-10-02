package workspaceevidence

import (
	"context"
	"os/exec"
	"testing"
)

func TestGitMutationRecorder_Observe(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*testing.T, string)
		want   bool
	}{
		{name: "no change"},
		{name: "shell no-op", change: func(t *testing.T, root string) {
			runMutationShell(t, root, "cat tracked.txt >/dev/null")
		}},
		{name: "shell edit", want: true, change: func(t *testing.T, root string) {
			runMutationShell(t, root, "printf 'after\\n' >tracked.txt")
		}},
		{name: "commit", want: true, change: func(t *testing.T, root string) {
			writeFingerprintTestFile(t, root, "tracked.txt", "after\n")
			runGitForFingerprintTest(t, root, "commit", "-qam", "task change")
		}},
		{name: "shell edit and commit", want: true, change: func(t *testing.T, root string) {
			runMutationShell(t, root, "printf 'after\\n' >tracked.txt && git commit -qam 'task change'")
		}},
		{name: "pushed commit", want: true, change: func(t *testing.T, root string) {
			remote := t.TempDir()
			runGitForFingerprintTest(t, remote, "init", "--bare", "-q")
			runGitForFingerprintTest(t, root, "remote", "add", "origin", remote)
			runMutationShell(t, root, "printf 'after\\n' >tracked.txt && git commit -qam 'task change' && git push -q origin HEAD:main")
		}},
		{name: "empty commit", change: func(t *testing.T, root string) {
			runGitForFingerprintTest(t, root, "commit", "--allow-empty", "-qm", "empty")
		}},
		{name: "message-only amend", change: func(t *testing.T, root string) {
			runGitForFingerprintTest(t, root, "commit", "--amend", "-qm", "new message")
		}},
		{name: "amended content", want: true, change: func(t *testing.T, root string) {
			runMutationShell(t, root, "printf 'after\\n' >tracked.txt && git commit --amend -qam 'task change'")
		}},
		{name: "session commit then message-only amend", want: true, change: func(t *testing.T, root string) {
			writeFingerprintTestFile(t, root, "tracked.txt", "session content\n")
			runGitForFingerprintTest(t, root, "commit", "-qam", "session change")
			runGitForFingerprintTest(t, root, "commit", "--amend", "-qm", "amended message")
		}},
		{name: "session commit amended back to empty", change: func(t *testing.T, root string) {
			writeFingerprintTestFile(t, root, "tracked.txt", "session content\n")
			runGitForFingerprintTest(t, root, "commit", "-qam", "session change")
			writeFingerprintTestFile(t, root, "tracked.txt", "before\n")
			runGitForFingerprintTest(t, root, "commit", "--amend", "--allow-empty", "-qam", "reverted change")
		}},
		{name: "branch change", change: func(t *testing.T, root string) {
			runGitForFingerprintTest(t, root, "checkout", "-qb", "other")
		}},
		{name: "commit on new branch", want: true, change: func(t *testing.T, root string) {
			runGitForFingerprintTest(t, root, "checkout", "-qb", "new-task")
			runMutationShell(t, root, "printf 'after\\n' >tracked.txt && git commit -qam 'task change'")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := newMutationTestRepo(t)
			recorder, err := NewGitMutationRecorder(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			if recorder.startHead == "" || recorder.startBranch != "refs/heads/task" {
				t.Fatalf("missing starting HEAD or branch: %+v", recorder)
			}
			if tc.change != nil {
				tc.change(t, root)
			}
			for range 2 {
				got, err := recorder.Observe(context.Background())
				if err != nil || got != tc.want {
					t.Fatalf("Observe = %v, %v; want %v", got, err, tc.want)
				}
			}
		})
	}
}

func TestGitMutationRecorder_MergeOriginMain(t *testing.T) {
	for _, tc := range []struct {
		name, mergeFlag string
		ownBeforeMerge  bool
		ownAfterMerge   bool
	}{
		{name: "merge only", mergeFlag: "--no-ff"},
		{name: "fast-forward only", mergeFlag: "--ff-only"},
		{name: "own commit before merge", mergeFlag: "--no-ff", ownBeforeMerge: true},
		{name: "own commit after merge", mergeFlag: "--no-ff", ownAfterMerge: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := newMutationTestRepo(t)
			upstream := t.TempDir()
			runGitForFingerprintTest(t, upstream, "clone", "-q", root, ".")
			runGitForFingerprintTest(t, upstream, "config", "user.name", "Upstream Test")
			runGitForFingerprintTest(t, upstream, "config", "user.email", "upstream@example.invalid")
			writeFingerprintTestFile(t, upstream, "upstream.txt", "upstream change\n")
			runGitForFingerprintTest(t, upstream, "add", ".")
			runGitForFingerprintTest(t, upstream, "commit", "-qm", "upstream change")
			runGitForFingerprintTest(t, root, "remote", "add", "origin", upstream)
			recorder, err := NewGitMutationRecorder(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			runGitForFingerprintTest(t, root, "fetch", "-q", "origin", "task:refs/remotes/origin/main")
			if tc.ownBeforeMerge {
				runMutationShell(t, root, "printf 'after\\n' >tracked.txt && git commit -qam 'task change'")
			}
			runGitForFingerprintTest(t, root, "merge", "-q", tc.mergeFlag, "origin/main", "-m", "merge upstream")
			if tc.ownAfterMerge {
				runMutationShell(t, root, "printf 'after\\n' >tracked.txt && git commit -qam 'task change'")
			}
			got, err := recorder.Observe(context.Background())
			if want := tc.ownBeforeMerge || tc.ownAfterMerge; err != nil || got != want {
				t.Fatalf("Observe = %v, %v; want %v", got, err, want)
			}
		})
	}
}

func TestGitMutationRecorder_PreexistingDirtyStateDoesNotCount(t *testing.T) {
	root := newMutationTestRepo(t)
	writeFingerprintTestFile(t, root, "tracked.txt", "preexisting change\n")
	recorder, err := NewGitMutationRecorder(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	runGitForFingerprintTest(t, root, "add", ".")
	if got, err := recorder.Observe(context.Background()); err != nil || got {
		t.Fatalf("staging preexisting changes counted as task work: %v, %v", got, err)
	}
}

func TestGitMutationRecorder_PreexistingUntrackedStagingDoesNotCount(t *testing.T) {
	for _, staged := range []bool{false, true} {
		name := "unstaged baseline"
		if staged {
			name = "staged baseline"
		}
		t.Run(name, func(t *testing.T) {
			root := newMutationTestRepo(t)
			writeFingerprintTestFile(t, root, "existing.txt", "preexisting content\n")
			if staged {
				runGitForFingerprintTest(t, root, "add", "existing.txt")
			}
			recorder, err := NewGitMutationRecorder(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{
				{"add", "existing.txt"},
				{"reset", "-q", "HEAD", "--", "existing.txt"},
				{"add", "existing.txt"},
			} {
				runGitForFingerprintTest(t, root, args...)
				if got, err := recorder.Observe(context.Background()); err != nil || got {
					t.Fatalf("bookkeeping %v counted as task work: %v, %v", args, got, err)
				}
			}
			writeFingerprintTestFile(t, root, "existing.txt", "task content\n")
			if got, err := recorder.Observe(context.Background()); err != nil || !got {
				t.Fatalf("content edit was not observed: %v, %v", got, err)
			}
		})
	}
}

func TestGitMutationRecorder_RebasePreservesSessionAttribution(t *testing.T) {
	for _, tc := range []struct {
		name        string
		preexisting bool
		session     bool
		sameFile    bool
		amend       bool
	}{
		{name: "upstream only"},
		{name: "preexisting local commit", preexisting: true},
		{name: "session commit", session: true},
		{name: "preexisting and session commits", preexisting: true, session: true},
		{name: "session commit with changed context", session: true, sameFile: true},
		{name: "session commit amended after rebase", session: true, amend: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := newMutationTestRepo(t)
			if tc.sameFile {
				writeFingerprintTestFile(t, root, "tracked.txt", "before\n\n\ncontext\n")
				runGitForFingerprintTest(t, root, "commit", "-qam", "base context")
			}
			runGitForFingerprintTest(t, root, "checkout", "-qb", "upstream")
			writeFingerprintTestFile(t, root, "upstream.txt", "imported content\n")
			if tc.sameFile {
				writeFingerprintTestFile(t, root, "tracked.txt", "before\n\n\nimported context\n")
			}
			runGitForFingerprintTest(t, root, "add", ".")
			runGitForFingerprintTest(t, root, "commit", "-qm", "upstream change")
			runGitForFingerprintTest(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
			runGitForFingerprintTest(t, root, "checkout", "-q", "task")
			if tc.preexisting {
				writeFingerprintTestFile(t, root, "preexisting.txt", "earlier work\n")
				runGitForFingerprintTest(t, root, "add", ".")
				runGitForFingerprintTest(t, root, "commit", "-qm", "preexisting change")
			}
			recorder, err := NewGitMutationRecorder(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			if tc.session {
				content := "session content\n"
				if tc.sameFile {
					content += "\n\ncontext\n"
				}
				writeFingerprintTestFile(t, root, "tracked.txt", content)
				runGitForFingerprintTest(t, root, "commit", "-qam", "session change")
			}
			runGitForFingerprintTest(t, root, "rebase", "-q", "origin/main")
			if tc.amend {
				runGitForFingerprintTest(t, root, "commit", "--amend", "-qm", "amended message")
			}
			for range 2 {
				if got, err := recorder.Observe(context.Background()); err != nil || got != tc.session {
					t.Fatalf("Observe after rebase = %v, %v; want %v", got, err, tc.session)
				}
			}
		})
	}
}

func TestGitMutationRecorder_AmendAfterFastForward(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content bool
	}{
		{name: "message only"},
		{name: "content change", content: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := newMutationTestRepo(t)
			runGitForFingerprintTest(t, root, "checkout", "-qb", "upstream")
			writeFingerprintTestFile(t, root, "upstream.txt", "imported content\n")
			runGitForFingerprintTest(t, root, "add", ".")
			runGitForFingerprintTest(t, root, "commit", "-qm", "upstream change")
			runGitForFingerprintTest(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
			runGitForFingerprintTest(t, root, "checkout", "-q", "task")
			recorder, err := NewGitMutationRecorder(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			runGitForFingerprintTest(t, root, "merge", "-q", "--ff-only", "origin/main")
			if tc.content {
				writeFingerprintTestFile(t, root, "tracked.txt", "session content\n")
			}
			for _, message := range []string{"amended message", "amended again"} {
				runGitForFingerprintTest(t, root, "commit", "--amend", "-qam", message)
				if got, err := recorder.Observe(context.Background()); err != nil || got != tc.content {
					t.Fatalf("Observe after amend = %v, %v; want %v", got, err, tc.content)
				}
			}
		})
	}
}

func TestGitMutationRecorder_UnbornFirstCommit(t *testing.T) {
	root := t.TempDir()
	runGitForFingerprintTest(t, root, "init", "-q", "-b", "task")
	runGitForFingerprintTest(t, root, "config", "user.name", "Buckley Test")
	runGitForFingerprintTest(t, root, "config", "user.email", "buckley@example.invalid")
	recorder, err := NewGitMutationRecorder(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	writeFingerprintTestFile(t, root, "tracked.txt", "first change\n")
	runGitForFingerprintTest(t, root, "add", ".")
	runGitForFingerprintTest(t, root, "commit", "-qm", "first change")
	if got, err := recorder.Observe(context.Background()); err != nil || !got {
		t.Fatalf("first commit not observed: %v, %v", got, err)
	}
}

func newMutationTestRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runGitForFingerprintTest(t, root, "init", "-q", "-b", "task")
	runGitForFingerprintTest(t, root, "config", "user.name", "Buckley Test")
	runGitForFingerprintTest(t, root, "config", "user.email", "buckley@example.invalid")
	writeFingerprintTestFile(t, root, "tracked.txt", "before\n")
	runGitForFingerprintTest(t, root, "add", ".")
	runGitForFingerprintTest(t, root, "commit", "-qm", "base")
	return root
}

func runMutationShell(t *testing.T, root, command string) {
	t.Helper()
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("shell command failed: %v: %s", err, out)
	}
}
