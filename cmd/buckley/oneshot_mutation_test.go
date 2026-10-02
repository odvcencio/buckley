package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestOneShotLane_CommittedMutation(t *testing.T) {
	requireMake(t)
	for _, tc := range []struct {
		name       string
		command    string
		unobserved bool
		wantChange bool
	}{
		{name: "commit before tool observation", unobserved: true, wantChange: true},
		{name: "shell edit then commit", command: "printf 'after\\n' >target.txt && git commit -qam 'task change'", wantChange: true},
		{name: "no change", command: "cat target.txt"},
		{name: "merge origin main only", command: "git merge --no-ff origin/main -m 'merge upstream'"},
		{name: "fast-forward origin main only", command: "git merge --ff-only origin/main"},
		{name: "own commit before merge", command: "printf 'after\\n' >target.txt && git commit -qam 'task change' && git merge --no-ff origin/main -m 'merge upstream'", wantChange: true},
		{name: "own commit after merge", command: "git merge --no-ff origin/main -m 'merge upstream' && printf 'after\\n' >target.txt && git commit -qam 'task change'", wantChange: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var script []laneStep
			if tc.command != "" {
				script = append(script, laneStep{tool: "run_shell", args: map[string]any{"command": tc.command}})
			}
			check := verifyStep("make check")
			if tc.unobserved {
				// Commit before the first tool's observation, as when work is
				// completed outside the dispatcher's edit recording path.
				check.beforeReply = func() {
					if err := os.WriteFile("target.txt", []byte("after\n"), 0o600); err != nil {
						t.Error(err)
						return
					}
					runMutationLaneGit(t, "commit", "-qam", "task change")
				}
			}
			script = append(script, check)
			answers := 1
			if !tc.wantChange {
				answers = defaultMaxNoChangeContinuations + 1
			}
			for range answers {
				script = append(script, sayStep("Work finished and verified."))
			}
			run := runScriptedLane(t, map[string]string{"target.txt": "before\n", "Makefile": "check:\n\t@true\n"}, script, prepareMutationLaneGit)
			if tc.wantChange {
				if run.code != 0 || run.requests != len(script) || strings.Contains(run.stderr, "One-shot continuation:") {
					t.Fatalf("committed work was not accepted: %+v", run)
				}
			} else if run.code != 1 || !strings.Contains(run.stderr, "no_observable_change") {
				t.Fatalf("run without its own mutation was accepted: %+v", run)
			}
			for _, result := range run.toolResults {
				if strings.Contains(result, "success: false") || strings.HasPrefix(result, "Error:") {
					t.Fatalf("tool did not execute successfully: %s", result)
				}
			}
			status, err := exec.Command("git", "status", "--porcelain").Output()
			if err != nil || len(status) != 0 {
				t.Fatalf("want clean committed worktree, got %s, %v", status, err)
			}
		})
	}
}

func TestOneShotLane_CommittedMutationAfterVerificationRequiresFreshCheck(t *testing.T) {
	requireMake(t)
	final := sayStep("Work finished and verified.")
	final.beforeReply = func() {
		if err := os.WriteFile("target.txt", []byte("after\n"), 0o600); err != nil {
			t.Error(err)
			return
		}
		runMutationLaneGit(t, "commit", "-qam", "task change")
	}
	script := []laneStep{
		verifyStep("make check"),
		final,
		verifyStep("make check"),
		editStep("after", "before"),
		{tool: "run_shell", args: map[string]any{"command": "git commit -qam 'repair change'"}},
		verifyStep("make check"),
		sayStep("Repaired the change and verified the final contents."),
	}
	run := runScriptedLane(t, map[string]string{
		"target.txt": "before\n",
		"Makefile":   "check:\n\t@test \"$$(cat target.txt)\" = before\n",
	}, script, prepareMutationLaneGit)
	if run.code != 0 || run.requests != len(script) {
		t.Fatalf("stale verification accepted or repair failed: %+v", run)
	}
	if strings.Count(run.stderr, "One-shot continuation:") != 1 || !strings.Contains(run.stderr, "missing successful verification after the latest workspace change") {
		t.Fatalf("missing fresh-check continuation: %s", run.stderr)
	}
	if !strings.Contains(run.stderr, "passed=false") || !strings.Contains(run.stdout, "Repaired the change") {
		t.Fatalf("final contents were not checked and repaired: %+v", run)
	}
}

func prepareMutationLaneGit(t *testing.T) {
	t.Helper()
	runMutationLaneGit(t, "config", "user.name", "Buckley Test")
	runMutationLaneGit(t, "config", "user.email", "buckley@example.invalid")
	runMutationLaneGit(t, "commit", "-qm", "base")
	runMutationLaneGit(t, "branch", "-m", "task")
	runMutationLaneGit(t, "checkout", "-qb", "upstream")
	if err := os.WriteFile("upstream.txt", []byte("upstream change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runMutationLaneGit(t, "add", "upstream.txt")
	runMutationLaneGit(t, "commit", "-qm", "upstream change")
	runMutationLaneGit(t, "update-ref", "refs/remotes/origin/main", "HEAD")
	runMutationLaneGit(t, "checkout", "-q", "task")
}

func runMutationLaneGit(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Errorf("git %v failed: %v: %s", args, err, out)
	}
}
