package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"m31labs.dev/buckley/internal/gitversion"
)

// prRepo is a work clone with a bare origin. Both start with one commit on
// main, and the test process works inside the clone.
type prRepo struct {
	work   string
	origin string
}

// newPRRepo returns a prRepo whose origin belongs to owner "o", the account in
// the PR URLs the tests feed to the fake gh.
func newPRRepo(t *testing.T) prRepo {
	t.Helper()
	return newPRRepoOwnedBy(t, "o")
}

// newPRRepoOwnedBy puts the bare origin at <dir>/<owner>/r.git. That is the
// owner/repo tail of a GitHub remote URL, so the push remote's owner can be read
// from it while the remote still works offline.
func newPRRepoOwnedBy(t *testing.T, owner string) prRepo {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("BUCKLEY_REMOTE_NAME", "")
	t.Setenv(envPRBase, "")

	originParent := filepath.Join(t.TempDir(), owner)
	if err := os.MkdirAll(originParent, 0o755); err != nil {
		t.Fatal(err)
	}
	origin := filepath.Join(originParent, "r.git")
	runGitIn(t, originParent, "init", "-q", "--bare", "-b", "main", origin)

	work := t.TempDir()
	runGitIn(t, work, "init", "-q", "-b", "main")
	runGitIn(t, work, "config", "user.email", "test@example.com")
	runGitIn(t, work, "config", "user.name", "Test User")
	writeFile(t, work, "base.txt", "base\n")
	runGitIn(t, work, "add", "base.txt")
	runGitIn(t, work, "commit", "-q", "-m", "base")
	runGitIn(t, work, "remote", "add", "origin", origin)
	runGitIn(t, work, "push", "-q", "-u", "origin", "main")
	t.Chdir(work)
	return prRepo{work: work, origin: origin}
}

// commit adds one commit on branch, creating the branch from main when it is
// missing, and leaves that branch checked out.
func (r prRepo) commit(t *testing.T, branch, file string) string {
	t.Helper()
	if _, ok := runGitAllowFail(t, r.work, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); ok {
		runGitIn(t, r.work, "checkout", "-q", branch)
	} else {
		runGitIn(t, r.work, "checkout", "-q", "-b", branch, "main")
	}
	writeFile(t, r.work, file, file+" on "+branch+"\n")
	runGitIn(t, r.work, "add", file)
	runGitIn(t, r.work, "commit", "-q", "-m", "add "+file)
	return runGitIn(t, r.work, "rev-parse", "HEAD")
}

// park checks out a throwaway branch so the head under test is not the
// checked-out branch.
func (r prRepo) park(t *testing.T) {
	t.Helper()
	runGitIn(t, r.work, "checkout", "-q", "-B", "other", "main")
}

func (r prRepo) rev(t *testing.T, ref string) string {
	t.Helper()
	return runGitIn(t, r.work, "rev-parse", ref)
}

func (r prRepo) breakRemote(t *testing.T) {
	t.Helper()
	runGitIn(t, r.work, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))
}

func TestResolvePRHead_Policy(t *testing.T) {
	const feature = "refs/heads/feature"
	const remoteFeature = "refs/remotes/origin/feature"

	cases := []struct {
		name            string
		setup           func(t *testing.T, r prRepo)
		flag            string
		noPush          bool
		offline         bool
		wantRef         string
		wantPush        bool
		wantSetUpstream bool
		wantAhead       int
		wantBehind      int
		wantUnverified  bool
		wantErr         string
	}{
		{
			name: "local only is pushed with an upstream",
			setup: func(t *testing.T, r prRepo) {
				r.commit(t, "feature", "f.txt")
				r.park(t)
			},
			flag: "feature", wantRef: feature, wantPush: true, wantSetUpstream: true,
		},
		{
			name: "local only with --head and an upstream already set is pushed without rewriting it",
			setup: func(t *testing.T, r prRepo) {
				r.commit(t, "feature", "f.txt")
				runGitIn(t, r.work, "branch", "-q", "--set-upstream-to=origin/main", "feature")
				r.park(t)
			},
			flag: "feature", wantRef: feature, wantPush: true,
		},
		{
			name: "local only with --push=false is refused before the model call",
			setup: func(t *testing.T, r prRepo) {
				r.commit(t, "feature", "f.txt")
				r.park(t)
			},
			flag: "feature", noPush: true, wantErr: "--push=false",
		},
		{
			name: "remote only reads the fetched remote-tracking ref and pushes nothing",
			setup: func(t *testing.T, r prRepo) {
				r.commit(t, "feature", "f.txt")
				runGitIn(t, r.work, "push", "-q", "origin", "feature")
				r.park(t)
				runGitIn(t, r.work, "branch", "-q", "-D", "feature")
				runGitIn(t, r.work, "update-ref", "-d", remoteFeature)
			},
			flag: "feature", wantRef: remoteFeature,
		},
		{
			name: "equal with --head sends nothing and leaves the branch config alone",
			setup: func(t *testing.T, r prRepo) {
				r.commit(t, "feature", "f.txt")
				runGitIn(t, r.work, "push", "-q", "origin", "feature")
				r.park(t)
			},
			flag: "feature", wantRef: feature,
		},
		{
			name: "equal on the checked-out branch records the upstream gh finds the head through",
			setup: func(t *testing.T, r prRepo) {
				r.commit(t, "feature", "f.txt")
				runGitIn(t, r.work, "push", "-q", "origin", "feature")
			},
			flag: "", wantRef: feature, wantPush: true, wantSetUpstream: true,
		},
		{
			name: "equal on the checked-out branch that already tracks its remote branch sends nothing",
			setup: func(t *testing.T, r prRepo) {
				r.commit(t, "feature", "f.txt")
				runGitIn(t, r.work, "push", "-q", "-u", "origin", "feature")
			},
			flag: "", wantRef: feature,
		},
		{
			name: "equal on the checked-out branch that tracks another branch re-points the upstream",
			setup: func(t *testing.T, r prRepo) {
				r.commit(t, "feature", "f.txt")
				runGitIn(t, r.work, "push", "-q", "origin", "feature")
				runGitIn(t, r.work, "branch", "-q", "--set-upstream-to=origin/main", "feature")
			},
			flag: "", wantRef: feature, wantPush: true, wantSetUpstream: true,
		},
		{
			name: "local ahead fast-forwards and records the upstream of a branch that had none",
			setup: func(t *testing.T, r prRepo) {
				r.commit(t, "feature", "f.txt")
				runGitIn(t, r.work, "push", "-q", "origin", "feature")
				r.commit(t, "feature", "g.txt")
				r.park(t)
			},
			flag: "feature", wantRef: feature, wantPush: true, wantSetUpstream: true, wantAhead: 1,
		},
		{
			name: "local ahead with --head and an upstream already set is pushed without rewriting it",
			setup: func(t *testing.T, r prRepo) {
				r.commit(t, "feature", "f.txt")
				runGitIn(t, r.work, "push", "-q", "origin", "feature")
				runGitIn(t, r.work, "branch", "-q", "--set-upstream-to=origin/main", "feature")
				r.commit(t, "feature", "g.txt")
				r.park(t)
			},
			flag: "feature", wantRef: feature, wantPush: true, wantAhead: 1,
		},
		{
			name: "local ahead on the checked-out branch that already tracks its remote branch is pushed without -u",
			setup: func(t *testing.T, r prRepo) {
				r.commit(t, "feature", "f.txt")
				runGitIn(t, r.work, "push", "-q", "-u", "origin", "feature")
				r.commit(t, "feature", "g.txt")
			},
			flag: "", wantRef: feature, wantPush: true, wantAhead: 1,
		},
		{
			name: "local ahead on the checked-out branch re-points an upstream that tracks another branch",
			setup: func(t *testing.T, r prRepo) {
				r.commit(t, "feature", "f.txt")
				runGitIn(t, r.work, "push", "-q", "origin", "feature")
				runGitIn(t, r.work, "branch", "-q", "--set-upstream-to=origin/main", "feature")
				r.commit(t, "feature", "g.txt")
			},
			flag: "", wantRef: feature, wantPush: true, wantSetUpstream: true, wantAhead: 1,
		},
		{
			name: "local behind reads the remote branch and pushes nothing",
			setup: func(t *testing.T, r prRepo) {
				first := r.commit(t, "feature", "f.txt")
				r.commit(t, "feature", "g.txt")
				runGitIn(t, r.work, "push", "-q", "origin", "feature")
				r.park(t)
				runGitIn(t, r.work, "branch", "-q", "-f", "feature", first)
			},
			flag: "feature", wantRef: remoteFeature, wantBehind: 1,
		},
		{
			name: "local ahead with --push=false describes the remote branch the PR will hold",
			setup: func(t *testing.T, r prRepo) {
				r.commit(t, "feature", "f.txt")
				runGitIn(t, r.work, "push", "-q", "origin", "feature")
				r.commit(t, "feature", "g.txt")
				r.park(t)
			},
			flag: "feature", noPush: true, wantRef: remoteFeature, wantAhead: 1,
		},
		{
			name: "diverged is refused and never forced",
			setup: func(t *testing.T, r prRepo) {
				r.commit(t, "feature", "f.txt")
				runGitIn(t, r.work, "push", "-q", "origin", "feature")
				runGitIn(t, r.work, "reset", "-q", "--hard", "main")
				r.commit(t, "feature", "other.txt")
				r.park(t)
			},
			flag: "feature", wantErr: "diverged",
		},
		{
			name:  "not found names git fetch",
			setup: func(t *testing.T, r prRepo) { r.park(t) },
			flag:  "ghost", wantErr: "git fetch origin",
		},
		{
			name: "a tag with the branch name does not capture the head",
			setup: func(t *testing.T, r prRepo) {
				r.commit(t, "feature", "f.txt")
				r.park(t)
				runGitIn(t, r.work, "tag", "feature", "main")
			},
			flag: "feature", wantRef: feature, wantPush: true, wantSetUpstream: true,
		},
		{
			name: "the checked-out branch is the default head",
			setup: func(t *testing.T, r prRepo) {
				r.commit(t, "feature", "f.txt")
			},
			flag: "", wantRef: feature, wantPush: true, wantSetUpstream: true,
		},
		{
			name: "owner:branch uses the branch part for git",
			setup: func(t *testing.T, r prRepo) {
				r.commit(t, "feature", "f.txt")
				r.park(t)
			},
			flag: "octo-org:feature", wantRef: feature, wantPush: true, wantSetUpstream: true,
		},
		{
			name:  "a name git rejects",
			setup: func(t *testing.T, r prRepo) { r.park(t) },
			flag:  "a..b", wantErr: "not a valid branch name",
		},
		{
			name:  "a name that would be read as an option",
			setup: func(t *testing.T, r prRepo) { r.park(t) },
			flag:  "--upload-pack=evil", wantErr: "not a valid branch name",
		},
		{
			name: "detached HEAD without --head",
			setup: func(t *testing.T, r prRepo) {
				r.commit(t, "feature", "f.txt")
				runGitIn(t, r.work, "checkout", "-q", "--detach", "feature")
			},
			flag: "", wantErr: "detached",
		},
		{
			name: "detached HEAD is ignored when --head is given",
			setup: func(t *testing.T, r prRepo) {
				r.commit(t, "feature", "f.txt")
				runGitIn(t, r.work, "checkout", "-q", "--detach", "main")
			},
			flag: "feature", wantRef: feature, wantPush: true, wantSetUpstream: true,
		},
		{
			name: "a dry run stays offline when the branch exists locally",
			setup: func(t *testing.T, r prRepo) {
				r.commit(t, "feature", "f.txt")
				r.park(t)
				r.breakRemote(t)
			},
			flag: "feature", offline: true, wantRef: feature,
		},
		{
			name: "--push=false tolerates an unreachable remote for a local branch",
			setup: func(t *testing.T, r prRepo) {
				r.commit(t, "feature", "f.txt")
				r.park(t)
				r.breakRemote(t)
			},
			flag: "feature", noPush: true, wantRef: feature, wantUnverified: true,
		},
		{
			name: "an unreachable remote stops a run that must push",
			setup: func(t *testing.T, r prRepo) {
				r.commit(t, "feature", "f.txt")
				r.park(t)
				r.breakRemote(t)
			},
			flag: "feature", wantErr: "fetch feature from origin",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newPRRepo(t)
			tc.setup(t, r)
			checkedOut := r.rev(t, "HEAD")
			_, localBefore := runGitAllowFail(t, r.work, "rev-parse", "--verify", "--quiet", feature)

			head, err := resolvePRHead(prHeadOptions{Flag: tc.flag, Remote: "origin", Push: !tc.noPush, CheckRemote: !tc.offline})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("resolvePRHead() error = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolvePRHead: %v", err)
			}
			if head.Ref != tc.wantRef {
				t.Errorf("Ref = %q, want %q", head.Ref, tc.wantRef)
			}
			if want := r.rev(t, tc.wantRef); head.SHA != want {
				t.Errorf("SHA = %s, want the tip of %s (%s)", head.SHA, tc.wantRef, want)
			}
			if head.Local != (tc.wantRef == feature) {
				t.Errorf("Local = %v for %s", head.Local, head.Ref)
			}
			if head.PushNeeded != tc.wantPush {
				t.Errorf("PushNeeded = %v, want %v", head.PushNeeded, tc.wantPush)
			}
			if head.SetUpstream != tc.wantSetUpstream {
				t.Errorf("SetUpstream = %v, want %v", head.SetUpstream, tc.wantSetUpstream)
			}
			if head.Ahead != tc.wantAhead || head.Behind != tc.wantBehind {
				t.Errorf("Ahead, Behind = %d, %d, want %d, %d", head.Ahead, head.Behind, tc.wantAhead, tc.wantBehind)
			}
			if (head.RemoteErr != nil) != tc.wantUnverified {
				t.Errorf("RemoteErr = %v, want an error = %v: a local branch accepted without comparing it with the remote has to say so", head.RemoteErr, tc.wantUnverified)
			}
			if head.Name != "feature" {
				t.Errorf("Name = %q, want feature", head.Name)
			}
			if wantGh := tc.flag; head.GhHead != wantGh {
				t.Errorf("GhHead = %q, want %q (set only when --head was given)", head.GhHead, wantGh)
			}
			if got := r.rev(t, "HEAD"); got != checkedOut {
				t.Errorf("HEAD moved from %s to %s: resolution must not touch the worktree", checkedOut, got)
			}
			if _, existsAfter := runGitAllowFail(t, r.work, "rev-parse", "--verify", "--quiet", feature); existsAfter != localBefore {
				t.Errorf("local branch %s existed = %v before and %v after: resolution may write refs/remotes only", feature, localBefore, existsAfter)
			}
			if status := runGitIn(t, r.work, "status", "--porcelain"); status != "" {
				t.Errorf("worktree changed: %q", status)
			}
		})
	}
}

func TestResolvePRHead_RejectsUnsafeNames(t *testing.T) {
	r := newPRRepo(t)
	r.commit(t, "feature", "f.txt")
	r.park(t)

	for _, flag := range []string{
		"-x",
		"--upload-pack=evil",
		"a..b",
		"x y",
		"@{-1}",
		"feature:",
		"-o:feature",
		"bad owner:feature",
		"owner:-x",
		":feature",
	} {
		t.Run(flag, func(t *testing.T) {
			if _, err := resolvePRHead(prHeadOptions{Flag: flag, Remote: "origin", Push: true, CheckRemote: true}); err == nil {
				t.Fatalf("resolvePRHead(%q) = nil error, want a rejection", flag)
			}
		})
	}
}

func TestPrRemoteName_RejectsOptionLikeNames(t *testing.T) {
	t.Setenv("BUCKLEY_REMOTE_NAME", "")
	if got, err := prRemoteName(); err != nil || got != "origin" {
		t.Fatalf("prRemoteName() = %q, %v, want origin", got, err)
	}
	t.Setenv("BUCKLEY_REMOTE_NAME", "fork")
	if got, err := prRemoteName(); err != nil || got != "fork" {
		t.Fatalf("prRemoteName() = %q, %v, want fork", got, err)
	}
	t.Setenv("BUCKLEY_REMOTE_NAME", "--upload-pack=evil")
	if _, err := prRemoteName(); err == nil {
		t.Fatal("prRemoteName() accepted an option-like remote name")
	}
}

func TestPushPRHead_LeavesWorktreeAlone(t *testing.T) {
	r := newPRRepo(t)
	featureTip := r.commit(t, "feature", "f.txt")
	r.park(t)
	otherTip := r.rev(t, "HEAD")
	writeFile(t, r.work, "dirty.txt", "another agent's uncommitted work\n")
	writeFile(t, r.work, "base.txt", "edited by another agent\n")
	statusBefore := runGitIn(t, r.work, "status", "--porcelain")

	head, err := resolvePRHead(prHeadOptions{Flag: "feature", Remote: "origin", Push: true, CheckRemote: true})
	if err != nil {
		t.Fatalf("resolvePRHead: %v", err)
	}
	var pushErr error
	out := captureStdout(t, func() { pushErr = pushPRHead(head, "origin") })
	if pushErr != nil {
		t.Fatalf("pushPRHead: %v", pushErr)
	}

	if remoteTip := runGitIn(t, r.origin, "rev-parse", "refs/heads/feature"); remoteTip != featureTip {
		t.Errorf("remote feature = %s, want %s", remoteTip, featureTip)
	}
	if !strings.Contains(out, "Pushed: "+featureTip) {
		t.Errorf("output %q does not report the head tip %s", out, featureTip)
	}
	if strings.Contains(out, otherTip) {
		t.Errorf("output %q reports the worktree's HEAD %s", out, otherTip)
	}
	if got := r.rev(t, "HEAD"); got != otherTip {
		t.Errorf("HEAD = %s, want it unchanged at %s", got, otherTip)
	}
	if got := runGitIn(t, r.work, "branch", "--show-current"); got != "other" {
		t.Errorf("checked-out branch = %q, want other", got)
	}
	if statusAfter := runGitIn(t, r.work, "status", "--porcelain"); statusAfter != statusBefore {
		t.Errorf("worktree status changed:\nbefore %q\nafter  %q", statusBefore, statusAfter)
	}
	if got := runGitIn(t, r.work, "for-each-ref", "--format=%(upstream:short)", "refs/heads/feature"); got != "origin/feature" {
		t.Errorf("upstream of feature = %q, want origin/feature", got)
	}
	if data, err := os.ReadFile(filepath.Join(r.work, "base.txt")); err != nil || string(data) != "edited by another agent\n" {
		t.Errorf("base.txt = %q, %v: the dirty edit must survive", data, err)
	}
}

func TestPushPRHead_TagWithTheBranchNameDoesNotBlockThePush(t *testing.T) {
	r := newPRRepo(t)
	tip := r.commit(t, "feature", "f.txt")
	r.park(t)
	runGitIn(t, r.work, "tag", "feature", "main")

	head, err := resolvePRHead(prHeadOptions{Flag: "feature", Remote: "origin", Push: true, CheckRemote: true})
	if err != nil {
		t.Fatalf("resolvePRHead: %v", err)
	}
	var pushErr error
	captureStdout(t, func() { pushErr = pushPRHead(head, "origin") })
	if pushErr != nil {
		t.Fatalf("pushPRHead: %v (a bare branch name is ambiguous with the tag; the refspec must not be)", pushErr)
	}
	if got := runGitIn(t, r.origin, "rev-parse", "refs/heads/feature"); got != tip {
		t.Errorf("remote branch feature = %s, want %s", got, tip)
	}
	if _, onRemote := runGitAllowFail(t, r.origin, "rev-parse", "--verify", "--quiet", "refs/tags/feature"); onRemote {
		t.Error("the push created a tag on the remote")
	}
}

func TestPushPRHead_NeverForces(t *testing.T) {
	r := newPRRepo(t)
	r.commit(t, "feature", "f.txt")
	runGitIn(t, r.work, "push", "-q", "origin", "feature")
	remoteTip := runGitIn(t, r.origin, "rev-parse", "refs/heads/feature")

	runGitIn(t, r.work, "reset", "-q", "--hard", "main")
	r.commit(t, "feature", "rewritten.txt")
	r.park(t)

	head := prHead{Name: "feature", Ref: "refs/heads/feature", Local: true, PushNeeded: true}
	var pushErr error
	captureStdout(t, func() { pushErr = pushPRHead(head, "origin") })
	if pushErr == nil {
		t.Fatal("pushPRHead() = nil, want git to refuse the non-fast-forward push")
	}
	if got := runGitIn(t, r.origin, "rev-parse", "refs/heads/feature"); got != remoteTip {
		t.Errorf("remote feature = %s, want it untouched at %s", got, remoteTip)
	}
}

func TestWarnIfPRHeadMoved_BranchAdvances(t *testing.T) {
	r := newPRRepo(t)
	pinned := r.commit(t, "feature", "f.txt")
	head := prHead{Name: "feature", Ref: "refs/heads/feature", SHA: pinned, Local: true}

	if out := captureStderr(t, func() { warnIfPRHeadMoved(head) }); out != "" {
		t.Fatalf("warning for an unmoved branch: %q", out)
	}
	moved := r.commit(t, "feature", "g.txt")
	out := captureStderr(t, func() { warnIfPRHeadMoved(head) })
	if !strings.Contains(out, shortSHA(pinned)) || !strings.Contains(out, shortSHA(moved)) {
		t.Fatalf("warning %q should name %s and %s", out, shortSHA(pinned), shortSHA(moved))
	}
}

func TestResolvePRBase_Precedence(t *testing.T) {
	cases := []struct {
		name         string
		setup        func(t *testing.T, r prRepo)
		flag         string
		env          string
		wantShort    string
		wantRef      string
		wantLabel    string
		wantExplicit bool
		wantCommits  int
		wantErr      string
	}{
		{
			name:  "flag wins over the environment",
			setup: func(t *testing.T, r prRepo) { runGitIn(t, r.work, "branch", "release", "main") },
			flag:  "release", env: "main",
			wantShort: "release", wantRef: "refs/heads/release", wantLabel: "release", wantExplicit: true,
		},
		{
			name:      "environment wins over detection",
			setup:     func(t *testing.T, r prRepo) { runGitIn(t, r.work, "branch", "release", "main") },
			env:       "release",
			wantShort: "release", wantRef: "refs/heads/release", wantLabel: "release", wantExplicit: true,
		},
		{
			name: "a remote branch is the evidence when it exists",
			setup: func(t *testing.T, r prRepo) {
				runGitIn(t, r.work, "push", "-q", "origin", "main:release")
				runGitIn(t, r.work, "fetch", "-q", "origin")
			},
			flag:      "release",
			wantShort: "release", wantRef: "refs/remotes/origin/release", wantLabel: "origin/release", wantExplicit: true,
		},
		{
			name: "an origin/ prefix is dropped for gh",
			flag: "origin/main", wantShort: "main", wantRef: "refs/remotes/origin/main", wantLabel: "origin/main", wantExplicit: true,
		},
		{
			name: "the remote default branch beats main, master and develop",
			setup: func(t *testing.T, r prRepo) {
				runGitIn(t, r.work, "push", "-q", "origin", "main:trunk")
				runGitIn(t, r.work, "fetch", "-q", "origin")
				runGitIn(t, r.work, "remote", "set-head", "origin", "trunk")
			},
			wantShort: "trunk", wantRef: "refs/remotes/origin/trunk", wantLabel: "origin/trunk",
		},
		{
			name: "a trunk default is found with no main, master or develop anywhere",
			setup: func(t *testing.T, r prRepo) {
				runGitIn(t, r.work, "push", "-q", "origin", "main:trunk")
				runGitIn(t, r.work, "fetch", "-q", "origin")
				runGitIn(t, r.work, "remote", "set-head", "origin", "trunk")
				runGitIn(t, r.work, "checkout", "-q", "-b", "work")
				runGitIn(t, r.work, "branch", "-q", "-D", "main")
				runGitIn(t, r.origin, "symbolic-ref", "HEAD", "refs/heads/trunk")
				runGitIn(t, r.work, "push", "-q", "origin", "--delete", "main")
				runGitIn(t, r.work, "update-ref", "-d", "refs/remotes/origin/main")
			},
			wantShort: "trunk", wantRef: "refs/remotes/origin/trunk", wantLabel: "origin/trunk",
		},
		{
			name:      "remote main is the evidence over the local copy",
			wantShort: "main", wantRef: "refs/remotes/origin/main", wantLabel: "origin/main",
		},
		{
			name: "local branches are the last resort",
			setup: func(t *testing.T, r prRepo) {
				runGitIn(t, r.work, "remote", "remove", "origin")
			},
			wantShort: "main", wantRef: "refs/heads/main", wantLabel: "main",
		},
		{
			name: "no match asks for --base",
			setup: func(t *testing.T, r prRepo) {
				runGitIn(t, r.work, "checkout", "-q", "-b", "work")
				runGitIn(t, r.work, "remote", "remove", "origin")
				runGitIn(t, r.work, "branch", "-q", "-D", "main")
			},
			wantErr: "--base",
		},
		{
			name: "a local branch named origin/main does not stand in for the remote branch",
			setup: func(t *testing.T, r prRepo) {
				first := r.commit(t, "feature", "f.txt")
				r.commit(t, "feature", "g.txt")
				r.park(t)
				runGitIn(t, r.work, "branch", "origin/main", first)
			},
			wantShort: "main", wantRef: "refs/remotes/origin/main", wantLabel: "origin/main", wantCommits: 2,
		},
		{
			name: "a tag named origin/main does not stand in for the remote branch",
			setup: func(t *testing.T, r prRepo) {
				first := r.commit(t, "feature", "f.txt")
				r.commit(t, "feature", "g.txt")
				r.park(t)
				runGitIn(t, r.work, "tag", "origin/main", first)
			},
			wantShort: "main", wantRef: "refs/remotes/origin/main", wantLabel: "origin/main", wantCommits: 2,
		},
		{
			name: "a tag named main does not stand in for the local branch",
			setup: func(t *testing.T, r prRepo) {
				first := r.commit(t, "feature", "f.txt")
				r.commit(t, "feature", "g.txt")
				r.park(t)
				runGitIn(t, r.work, "remote", "remove", "origin")
				runGitIn(t, r.work, "tag", "main", first)
			},
			wantShort: "main", wantRef: "refs/heads/main", wantLabel: "main", wantCommits: 2,
		},
		{
			name: "an explicit base is read through the same full ref",
			setup: func(t *testing.T, r prRepo) {
				first := r.commit(t, "feature", "f.txt")
				r.commit(t, "feature", "g.txt")
				r.park(t)
				runGitIn(t, r.work, "tag", "main", first)
			},
			flag:      "main",
			wantShort: "main", wantRef: "refs/remotes/origin/main", wantLabel: "origin/main", wantExplicit: true, wantCommits: 2,
		},
		{
			name: "an explicit base that does not exist is an error",
			flag: "nosuch", wantErr: "nosuch",
		},
		{
			name: "an option-like base is rejected",
			flag: "--output=x", wantErr: "not a valid branch name",
		},
		{
			name: "a bad environment value names the variable",
			env:  "--output=x", wantErr: envPRBase,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newPRRepo(t)
			if tc.setup != nil {
				tc.setup(t, r)
			}
			t.Setenv(envPRBase, tc.env)

			base, err := resolvePRBase(tc.flag, "origin")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("resolvePRBase() error = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolvePRBase: %v", err)
			}
			want := prBase{Short: tc.wantShort, Ref: tc.wantRef, Label: tc.wantLabel, Explicit: tc.wantExplicit}
			if base != want {
				t.Fatalf("resolvePRBase() = %+v, want %+v", base, want)
			}
			if tc.wantCommits != 0 {
				got, err := countPRCommits(base.Ref, "refs/heads/feature")
				if err != nil {
					t.Fatalf("countPRCommits: %v", err)
				}
				if got != tc.wantCommits {
					t.Errorf("countPRCommits(%s, feature) = %d, want %d: another ref replaced the base", base.Ref, got, tc.wantCommits)
				}
			}
		})
	}
}

func TestOwnerFromRemoteURL_Forms(t *testing.T) {
	cases := []struct {
		url       string
		wantOwner string
	}{
		{"https://github.com/octo/repo.git", "octo"},
		{"https://github.com/octo/repo", "octo"},
		{"https://github.com/octo/repo/", "octo"},
		{"https://x-access-token:secret@github.com/octo/repo.git", "octo"},
		{"https://ghe.example.com/octo/repo.git", "octo"},
		{"ssh://git@github.com/octo/repo.git", "octo"},
		{"ssh://git@github.com:22/octo/repo.git", "octo"},
		{"git@github.com:octo/repo.git", "octo"},
		{"github.com:octo/repo", "octo"},
		{"/srv/git/octo/repo.git", "octo"},
		{"file:///srv/git/octo/repo.git", "octo"},
		{"../octo/repo.git", "octo"},
		{"repo.git", ""},
		{"https://github.com", ""},
		{"https://github.com/repo.git", ""},
		{"git@github.com:repo.git", ""},
		{"../repo.git", ""},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.url, func(t *testing.T) {
			got, ok := ownerFromRemoteURL(tc.url)
			if got != tc.wantOwner || ok != (tc.wantOwner != "") {
				t.Fatalf("ownerFromRemoteURL(%q) = %q, %v, want %q", tc.url, got, ok, tc.wantOwner)
			}
		})
	}
}

func TestPRRemoteOwner_ReadsThePushURL(t *testing.T) {
	cases := []struct {
		name      string
		setup     func(t *testing.T, r prRepo)
		remote    string
		wantOwner string
		wantErr   string
	}{
		{
			name: "the fetch URL",
			setup: func(t *testing.T, r prRepo) {
				runGitIn(t, r.work, "remote", "set-url", "origin", "git@github.com:octo/repo.git")
			},
			remote:    "origin",
			wantOwner: "octo",
		},
		{
			name: "a push URL wins over the fetch URL, because pushes go there",
			setup: func(t *testing.T, r prRepo) {
				runGitIn(t, r.work, "remote", "set-url", "origin", "https://github.com/parent/repo.git")
				runGitIn(t, r.work, "remote", "set-url", "--push", "origin", "https://github.com/octo/repo.git")
			},
			remote:    "origin",
			wantOwner: "octo",
		},
		{
			name: "a remote other than origin",
			setup: func(t *testing.T, r prRepo) {
				runGitIn(t, r.work, "remote", "add", "fork", "https://github.com/octo/repo.git")
			},
			remote:    "fork",
			wantOwner: "octo",
		},
		{
			name:    "a remote that does not exist",
			remote:  "ghost",
			wantErr: "read the push URL of remote ghost",
		},
		{
			name: "a URL without owner/repo never leaks into the error",
			setup: func(t *testing.T, r prRepo) {
				runGitIn(t, r.work, "remote", "set-url", "origin", "https://user:s3cret@host.example")
			},
			remote:  "origin",
			wantErr: "does not end in owner/repo",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newPRRepo(t)
			if tc.setup != nil {
				tc.setup(t, r)
			}

			owner, err := prRemoteOwner(tc.remote)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("prRemoteOwner() error = %v, want it to contain %q", err, tc.wantErr)
				}
				if strings.Contains(err.Error(), "s3cret") {
					t.Errorf("error repeats the credential in the URL: %v", err)
				}
				return
			}
			if err != nil || owner != tc.wantOwner {
				t.Fatalf("prRemoteOwner() = %q, %v, want %q", owner, err, tc.wantOwner)
			}
		})
	}
}

func TestPRHeadOwner_MatchesTheRemoteTheBranchIsPushedTo(t *testing.T) {
	cases := []struct {
		name      string
		head      prHead
		remote    string
		wantOwner string
		wantErr   []string
	}{
		{
			name:      "a bare head belongs to the push remote's owner",
			head:      prHead{Name: "feature"},
			remote:    "origin",
			wantOwner: "octo",
		},
		{
			name:      "the same owner named with --head owner:branch",
			head:      prHead{Name: "feature", GhHead: "octo:feature"},
			remote:    "origin",
			wantOwner: "octo",
		},
		{
			name:      "owners compare without case",
			head:      prHead{Name: "feature", GhHead: "OCTO:feature"},
			remote:    "origin",
			wantOwner: "octo",
		},
		{
			name:      "a bare --head belongs to the push remote's owner too",
			head:      prHead{Name: "feature", GhHead: "feature"},
			remote:    "origin",
			wantOwner: "octo",
		},
		{
			name:    "another owner than the push remote's is refused",
			head:    prHead{Name: "feature", GhHead: "parent-owner:feature"},
			remote:  "origin",
			wantErr: []string{"parent-owner:feature", "names a branch of parent-owner", "origin pushes to a repository of octo", "BUCKLEY_REMOTE_NAME"},
		},
		{
			name:    "a remote that does not exist",
			head:    prHead{Name: "feature"},
			remote:  "ghost",
			wantErr: []string{"read the push URL of remote ghost"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newPRRepoOwnedBy(t, "octo")

			owner, err := prHeadOwner(tc.head, tc.remote)
			if len(tc.wantErr) > 0 {
				if err == nil {
					t.Fatalf("prHeadOwner() = %q, nil, want an error", owner)
				}
				for _, want := range tc.wantErr {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("prHeadOwner() error = %v, want it to contain %q", err, want)
					}
				}
				return
			}
			if err != nil || owner != tc.wantOwner {
				t.Fatalf("prHeadOwner() = %q, %v, want %q", owner, err, tc.wantOwner)
			}
		})
	}
}

// requireGit skips a test that needs a git option the installed git may lack.
func requireGit(t *testing.T, major, minor int, why string) {
	t.Helper()
	if ok, installed := gitversion.AtLeast(major, minor); !ok {
		t.Skipf("git %s lacks %s (needs %d.%d)", installed, why, major, minor)
	}
}

// fakeGitScript records every call. With FAKE_GIT_VERSION set it also acts like
// that git release: it reports the version and rejects the options newer
// releases added, as the real older git does with "unknown option". Otherwise
// it hands every call to the real git.
const fakeGitScript = `#!/bin/sh
for a in "$@"; do printf '%s\000' "$a" >> "$FAKE_GIT_LOG"; done
printf '\036' >> "$FAKE_GIT_LOG"
if [ -n "$FAKE_GIT_VERSION" ]; then
	if [ "$1" = version ]; then echo "git version $FAKE_GIT_VERSION"; exit 0; fi
	minor=${FAKE_GIT_VERSION#2.}; minor=${minor%%.*}
	for a in "$@"; do
		case "$a" in
		--attr-source=*|--source=*)
			if [ "$minor" -lt 40 ]; then echo "unknown option: ${a%%=*}" >&2; exit 129; fi ;;
		--no-write-fetch-head)
			if [ "$minor" -lt 29 ]; then echo "unknown option: $a" >&2; exit 129; fi ;;
		esac
	done
fi
exec "$FAKE_GIT_REAL" "$@"
`

// fakeGit is a git executable on PATH that records its calls. A version makes it
// act like that release; "" keeps the real git's behavior.
type fakeGit struct{ log string }

func installFakeGit(t *testing.T, version string) *fakeGit {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("look up git: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(fakeGitScript), 0o755); err != nil {
		t.Fatal(err)
	}
	g := &fakeGit{log: filepath.Join(dir, "calls.log")}
	t.Setenv("FAKE_GIT_VERSION", version)
	t.Setenv("FAKE_GIT_REAL", realGit)
	t.Setenv("FAKE_GIT_LOG", g.log)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return g
}

// calls returns each git invocation as its argument list.
func (g *fakeGit) calls(t *testing.T) [][]string {
	t.Helper()
	data, err := os.ReadFile(g.log)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var calls [][]string
	for _, record := range strings.Split(string(data), "\x1e") {
		if record != "" {
			calls = append(calls, strings.Split(strings.TrimSuffix(record, "\x00"), "\x00"))
		}
	}
	return calls
}

// withSubcommand returns the calls whose git subcommand is name: the first
// argument that is not a global option.
func withSubcommand(calls [][]string, name string) [][]string {
	var matched [][]string
	for _, call := range calls {
		for _, arg := range call {
			if strings.HasPrefix(arg, "-") {
				continue
			}
			if arg == name {
				matched = append(matched, call)
			}
			break
		}
	}
	return matched
}

// remoteOnlyFeature leaves feature on origin only, with the worktree on another
// branch and no remote-tracking ref, so resolving the head has to fetch.
func remoteOnlyFeature(t *testing.T, r prRepo) {
	t.Helper()
	r.commit(t, "feature", "f.txt")
	runGitIn(t, r.work, "push", "-q", "origin", "feature")
	r.park(t)
	runGitIn(t, r.work, "branch", "-q", "-D", "feature")
	runGitIn(t, r.work, "update-ref", "-d", "refs/remotes/origin/feature")
}

// TestFetchPRBranch_LeavesFetchHeadAlone covers the checkout's FETCH_HEAD file.
// A plain fetch rewrites it, and a `git pull` that the agent working in the
// checkout runs at the same moment reads it after its own fetch, so it could
// merge the --head branch into the branch it has checked out.
func TestFetchPRBranch_LeavesFetchHeadAlone(t *testing.T) {
	requireGit(t, 2, 29, "git fetch --no-write-fetch-head")
	for _, tc := range []struct {
		name  string
		prime bool
	}{
		{"a FETCH_HEAD another command wrote is not replaced", true},
		{"no FETCH_HEAD is created", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newPRRepo(t)
			remoteOnlyFeature(t, r)
			fetchHead := filepath.Join(r.work, ".git", "FETCH_HEAD")
			if err := os.Remove(fetchHead); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if tc.prime {
				runGitIn(t, r.work, "fetch", "-q", "origin", "main")
			}
			before, beforeErr := os.ReadFile(fetchHead)

			head, err := resolvePRHead(prHeadOptions{Flag: "feature", Remote: "origin", Push: true, CheckRemote: true})
			if err != nil {
				t.Fatalf("resolvePRHead: %v", err)
			}
			if head.Ref != "refs/remotes/origin/feature" {
				t.Fatalf("Ref = %q: the head was not fetched, so the test checked nothing", head.Ref)
			}

			after, afterErr := os.ReadFile(fetchHead)
			if (beforeErr == nil) != (afterErr == nil) || string(before) != string(after) {
				t.Errorf("FETCH_HEAD changed:\nbefore %q (%v)\nafter  %q (%v)", before, beforeErr, after, afterErr)
			}
		})
	}
}

func TestFetchPRBranch_OldGitFetchesWithoutTheFlagAndSaysSo(t *testing.T) {
	r := newPRRepo(t)
	remoteOnlyFeature(t, r)
	g := installFakeGit(t, "2.28.0")

	var head prHead
	var err error
	stderr := captureStderr(t, func() {
		head, err = resolvePRHead(prHeadOptions{Flag: "feature", Remote: "origin", Push: true, CheckRemote: true})
	})
	if err != nil {
		t.Fatalf("resolvePRHead on a git without --no-write-fetch-head: %v", err)
	}
	if head.Ref != "refs/remotes/origin/feature" {
		t.Errorf("Ref = %q, want the fetched remote-tracking ref", head.Ref)
	}
	for _, call := range withSubcommand(g.calls(t), "fetch") {
		if slices.Contains(call, "--no-write-fetch-head") {
			t.Errorf("git %v passes --no-write-fetch-head to a git that does not have it", call)
		}
	}
	if lines := strings.Split(strings.TrimSpace(stderr), "\n"); len(lines) != 1 ||
		!strings.Contains(lines[0], "warning:") || !strings.Contains(lines[0], "2.28.0") || !strings.Contains(lines[0], "FETCH_HEAD") {
		t.Errorf("stderr = %q, want one warning line that names the git version and FETCH_HEAD", stderr)
	}
}

// TestPushPRHead_RecordsTheUpstreamOnlyWhenAsked follows a head from its
// resolution to the push, with a branch whose upstream is another agent's
// choice (origin/main). With --head, gh needs no upstream, so the push leaves
// that config alone. Without --head, gh finds the head through the upstream, so
// the push re-points it at the branch itself.
func TestPushPRHead_RecordsTheUpstreamOnlyWhenAsked(t *testing.T) {
	for _, tc := range []struct {
		name      string
		flag      string
		upstream  bool
		wantMerge string
	}{
		{"--head keeps an upstream the branch already has", "feature", true, "refs/heads/main"},
		{"--head records an upstream for a branch that had none", "feature", false, "refs/heads/feature"},
		{"the checked-out branch gets the upstream gh infers its head from", "", true, "refs/heads/feature"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newPRRepo(t)
			r.commit(t, "feature", "f.txt")
			runGitIn(t, r.work, "push", "-q", "origin", "feature")
			if tc.upstream {
				runGitIn(t, r.work, "branch", "-q", "--set-upstream-to=origin/main", "feature")
			}
			tip := r.commit(t, "feature", "g.txt")
			if tc.flag != "" {
				r.park(t)
			}

			head, err := resolvePRHead(prHeadOptions{Flag: tc.flag, Remote: "origin", Push: true, CheckRemote: true})
			if err != nil {
				t.Fatalf("resolvePRHead: %v", err)
			}
			if !head.PushNeeded {
				t.Fatalf("PushNeeded = false for a branch one commit ahead")
			}
			var pushErr error
			captureStdout(t, func() { pushErr = pushPRHead(head, "origin") })
			if pushErr != nil {
				t.Fatalf("pushPRHead: %v", pushErr)
			}

			if got := runGitIn(t, r.origin, "rev-parse", "refs/heads/feature"); got != tip {
				t.Errorf("remote feature = %s, want %s", got, tip)
			}
			merge, _ := runGitAllowFail(t, r.work, "config", "--get", "branch.feature.merge")
			if got := strings.TrimSpace(merge); got != tc.wantMerge {
				t.Errorf("branch.feature.merge = %q, want %q", got, tc.wantMerge)
			}
		})
	}
}

// TestRunPRCommand_HeadStatesLeaveTheBranchConfigAlone runs the whole command
// for a --head branch whose upstream another agent chose (origin/main), and
// reads the git calls it made. A branch the remote already has in full is not
// pushed at all, and one that is ahead is pushed without -u.
func TestRunPRCommand_HeadStatesLeaveTheBranchConfigAlone(t *testing.T) {
	for _, tc := range []struct {
		name      string
		ahead     bool
		wantPush  bool
		wantMerge string
	}{
		{"equal to the remote branch: no push", false, false, "refs/heads/main"},
		{"ahead of the remote branch: a push that does not rewrite the config", true, true, "refs/heads/main"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newPRRepo(t)
			r.commit(t, "feature", "f.txt")
			runGitIn(t, r.work, "push", "-q", "origin", "feature")
			runGitIn(t, r.work, "branch", "-q", "--set-upstream-to=origin/main", "feature")
			tip := runGitIn(t, r.work, "rev-parse", "feature")
			if tc.ahead {
				tip = r.commit(t, "feature", "g.txt")
			}
			r.park(t)
			stubPRDependencies(t, func() error { return nil })
			stubPRModel(t)
			stubGh(t)
			installFakeGh(t, "[]", "https://github.com/o/r/pull/12\n")
			g := installFakeGit(t, "")

			var err error
			captureStdout(t, func() { err = runPRCommand([]string{"--yes", "--cost=false", "--head", "feature"}) })
			if err != nil {
				t.Fatalf("runPRCommand: %v", err)
			}

			pushes := withSubcommand(g.calls(t), "push")
			if tc.wantPush != (len(pushes) == 1) {
				t.Fatalf("git push calls = %v, want a push = %v", pushes, tc.wantPush)
			}
			for _, push := range pushes {
				if slices.Contains(push, "-u") || slices.Contains(push, "--set-upstream") {
					t.Errorf("git %v records an upstream for a branch that has one", push)
				}
			}
			if got := runGitIn(t, r.origin, "rev-parse", "refs/heads/feature"); got != tip {
				t.Errorf("remote feature = %s, want %s", got, tip)
			}
			merge, _ := runGitAllowFail(t, r.work, "config", "--get", "branch.feature.merge")
			if got := strings.TrimSpace(merge); got != tc.wantMerge {
				t.Errorf("branch.feature.merge = %q, want %q: another checkout's tracking config was rewritten", got, tc.wantMerge)
			}
		})
	}
}
