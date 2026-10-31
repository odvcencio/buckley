package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	gopath "path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"m31labs.dev/buckley/internal/gitversion"
	"m31labs.dev/buckley/pkg/terminal"
)

const envPRBase = "BUCKLEY_PR_BASE"

// prHead is the branch a pull request comes from. It is resolved once, before
// the model call, so the evidence, the display, the push and the open-PR
// lookup all describe the same commit.
type prHead struct {
	// Name is the branch name used for display, git push and the open-PR lookup.
	Name string
	// GhHead is the value for gh pr create --head. It is empty unless the
	// caller passed --head: without it gh infers the head repository (a fork,
	// for example) from the checkout, and a bare name would be looked up in
	// the base repository.
	GhHead string
	// Owner is the login that owns the repository holding the head branch: the
	// account behind the push remote. The open-PR lookup matches it against each
	// listed PR's head repository, because gh lists PRs by branch name only, so
	// a branch of the base repository and a branch of a fork can share a name.
	// preflightPR sets it for a real run. A dry run looks up no PR and leaves it
	// empty.
	Owner string
	// Ref is the fully qualified ref the PR evidence is read from:
	// refs/heads/<Name>, or refs/remotes/<remote>/<Name> when the remote
	// branch is the one to describe.
	Ref string
	// SHA is the commit Ref pointed at when it was resolved.
	SHA string
	// Local reports that Ref is the local branch.
	Local bool
	// PushNeeded reports that the push step applies: Ref is the local branch
	// and the remote lacks it or some of its commits. The one other case is the
	// checked-out branch (no --head) that the remote has in full but that does
	// not track it. gh finds the head through the upstream, so the push, which
	// sends nothing, records it.
	PushNeeded bool
	// SetUpstream reports that the push passes -u. See wantsUpstream.
	SetUpstream bool
	// Ahead and Behind count the commits only the local branch has and only
	// the remote branch has.
	Ahead, Behind int
	// RemoteErr is why the remote branch could not be read. It is set only for
	// a local branch that --push=false let through: nothing says whether the
	// remote has the branch, or which commits.
	RemoteErr error
}

// explicitOwner returns the owner the caller named with --head owner:branch,
// or "" when the caller gave a bare name or no --head.
func (h prHead) explicitOwner() string {
	if owner, _, ok := strings.Cut(h.GhHead, ":"); ok {
		return owner
	}
	return ""
}

// prHeadOptions says how resolvePRHead may use the remote.
type prHeadOptions struct {
	// Flag is the --head value; empty means the checked-out branch.
	Flag   string
	Remote string
	// Push is the --push setting.
	Push bool
	// CheckRemote compares the branch with the remote, which needs the
	// network. A dry run leaves it off so a branch that exists locally still
	// previews offline.
	CheckRemote bool
}

var ghOwnerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*$`)

// resolvePRHead turns --head (or the checked-out branch) into one prHead.
// Evidence comes from commits only, and the states are:
//
//	local only    evidence from the local branch; pushed, with -u as wantsUpstream says
//	remote only   evidence from the remote-tracking ref, after a one-branch fetch; no push
//	equal         local branch; nothing to send, so no push and the branch config
//	              stays as it is. The exception is the checked-out branch (no
//	              --head) when it does not track the remote branch: -u sends
//	              nothing but records the upstream gh needs
//	local ahead   local branch; fast-forward push, with -u under the same rule as
//	              local only. With --push=false nothing is pushed, so the PR
//	              holds only the remote branch and the evidence is the
//	              remote-tracking ref
//	local behind  remote-tracking ref; no push
//	diverged      refused: buckley never force-pushes
//	not found     refused
func resolvePRHead(opts prHeadOptions) (prHead, error) {
	name, ghHead, err := prHeadName(opts.Flag)
	if err != nil {
		return prHead{}, err
	}
	localRef := "refs/heads/" + name
	remoteRef := "refs/remotes/" + opts.Remote + "/" + name

	localSHA, err := prCommit(localRef)
	if err != nil {
		return prHead{}, err
	}
	local := prHead{Name: name, GhHead: ghHead, Ref: localRef, SHA: localSHA, Local: true}
	if localSHA != "" && !opts.CheckRemote {
		return local, nil
	}

	onRemote, err := fetchPRBranch(opts.Remote, name, remoteRef)
	if err != nil {
		if localSHA == "" || opts.Push {
			return prHead{}, err
		}
		// With --push=false the caller owns publishing the branch, so an
		// unreachable remote does not stop a branch that exists locally. The
		// caller learns from RemoteErr that the comparison did not happen.
		local.RemoteErr = err
		return local, nil
	}
	remoteSHA := ""
	if onRemote {
		if remoteSHA, err = prCommit(remoteRef); err != nil {
			return prHead{}, err
		}
	}
	fromRemote := prHead{Name: name, GhHead: ghHead, Ref: remoteRef, SHA: remoteSHA}

	switch {
	case !onRemote && localSHA == "":
		return prHead{}, fmt.Errorf("branch %q was not found locally or on %s; run `git fetch %s` or check the name", name, opts.Remote, opts.Remote)
	case !onRemote:
		if !opts.Push {
			return prHead{}, fmt.Errorf("branch %q is not on %s and --push=false: push it first or drop --push=false", name, opts.Remote)
		}
		local.PushNeeded = true
		if local.SetUpstream, err = wantsUpstream(local, opts.Remote); err != nil {
			return prHead{}, err
		}
		return local, nil
	case localSHA == "":
		return fromRemote, nil
	case localSHA == remoteSHA:
		if ghHead == "" {
			if local.SetUpstream, err = wantsUpstream(local, opts.Remote); err != nil {
				return prHead{}, err
			}
			local.PushNeeded = local.SetUpstream
		}
		return local, nil
	}

	ahead, behind, err := prDivergence(localRef, remoteRef)
	if err != nil {
		return prHead{}, err
	}
	switch {
	case behind == 0 && opts.Push:
		local.PushNeeded, local.Ahead = true, ahead
		if local.SetUpstream, err = wantsUpstream(local, opts.Remote); err != nil {
			return prHead{}, err
		}
		return local, nil
	case behind == 0, ahead == 0:
		// Nothing gets pushed, so the PR holds exactly what the remote branch
		// holds, and the text has to describe that, not the local commits.
		fromRemote.Ahead, fromRemote.Behind = ahead, behind
		return fromRemote, nil
	}
	return prHead{}, fmt.Errorf("branch %q has diverged from %s/%s (%d local-only, %d remote-only commits); buckley never force-pushes, so rebase or merge first", name, opts.Remote, name, ahead, behind)
}

// prHeadName returns the branch name for --head, or for the checked-out
// branch when the flag is empty. owner:branch (a fork) keeps the whole value
// for gh and uses the part after the colon for git.
func prHeadName(flag string) (name, ghHead string, err error) {
	flag = strings.TrimSpace(flag)
	if flag == "" {
		ref, err := prGit(gitLocalTimeout, "symbolic-ref", "--quiet", "HEAD")
		if err != nil {
			if prExitCode(err) == 1 {
				return "", "", errors.New("HEAD is detached; pass --head <branch>")
			}
			return "", "", fmt.Errorf("read the current branch: %w", err)
		}
		name = strings.TrimPrefix(ref, "refs/heads/")
		if name == ref {
			return "", "", fmt.Errorf("HEAD points at %s, not a branch; pass --head <branch>", ref)
		}
		return name, "", nil
	}
	name = flag
	if owner, branch, ok := strings.Cut(flag, ":"); ok {
		if !ghOwnerPattern.MatchString(owner) {
			return "", "", fmt.Errorf("--head %q: %q is not a GitHub owner", flag, owner)
		}
		name = branch
	}
	if err := checkBranchName(name); err != nil {
		return "", "", fmt.Errorf("--head: %w", err)
	}
	return name, flag, nil
}

// checkBranchName rejects anything git would not accept as a plain branch
// name. That includes a leading "-", which would reach git as an option.
func checkBranchName(name string) error {
	if name == "" || strings.HasPrefix(name, "-") {
		return fmt.Errorf("%q is not a valid branch name", name)
	}
	// --branch expands "@{-1}" to a branch name or hash, so the output must
	// match the input to prove it was a plain name.
	if out, err := prGit(gitLocalTimeout, "check-ref-format", "--branch", name); err != nil || out != name {
		return fmt.Errorf("%q is not a valid branch name", name)
	}
	return nil
}

// wantsUpstream reports whether pushing head should pass -u.
//
// Without --head, gh finds the head through the branch's upstream, so the push
// sets it unless the branch already tracks the branch of the same name on
// remote. With --head, gh is told the head and another agent may have the
// branch checked out, so only a branch that has no upstream gets one: an
// upstream that exists, even one that points elsewhere, is left alone.
func wantsUpstream(head prHead, remote string) (bool, error) {
	merge, err := prConfigValue("branch." + head.Name + ".merge")
	if err != nil {
		return false, err
	}
	if head.GhHead != "" {
		return merge == "", nil
	}
	tracked, err := prConfigValue("branch." + head.Name + ".remote")
	if err != nil {
		return false, err
	}
	return tracked != remote || merge != "refs/heads/"+head.Name, nil
}

// prConfigValue returns the value of a git config key, or "" when it is unset.
func prConfigValue(key string) (string, error) {
	value, err := prGit(gitLocalTimeout, "config", "--get", key)
	if err != nil {
		if prExitCode(err) == 1 {
			return "", nil
		}
		return "", fmt.Errorf("read git config %s: %w", key, err)
	}
	return value, nil
}

// prRemoteName returns the remote that pushes and fetches use.
func prRemoteName() (string, error) {
	remote := strings.TrimSpace(os.Getenv("BUCKLEY_REMOTE_NAME"))
	if remote == "" {
		return "origin", nil
	}
	if strings.HasPrefix(remote, "-") {
		return "", fmt.Errorf("BUCKLEY_REMOTE_NAME %q is not a valid remote name", remote)
	}
	return remote, nil
}

// prHeadOwner returns the owner of the repository the head branch lives in:
// the account behind the push remote, which is where buckley pushes the branch
// and reads the remote branch from. --head owner:branch has to name that same
// account. Otherwise the push and the PR text would come from one repository
// while the PR is looked up, and opened, in another.
func prHeadOwner(head prHead, remote string) (string, error) {
	pushed, err := prRemoteOwner(remote)
	if err != nil {
		return "", err
	}
	if named := head.explicitOwner(); named != "" && !strings.EqualFold(named, pushed) {
		return "", fmt.Errorf("--head %s names a branch of %s, but remote %s pushes to a repository of %s; the push and the PR text come from that remote. Drop the owner, or set BUCKLEY_REMOTE_NAME to a remote of %s", head.GhHead, named, remote, pushed, named)
	}
	return pushed, nil
}

// prRemoteOwner returns the account in the push URL of remote. The error never
// repeats the URL, because it may carry a credential.
func prRemoteOwner(remote string) (string, error) {
	out, err := prGit(gitLocalTimeout, "remote", "get-url", "--push", remote)
	if err != nil {
		return "", fmt.Errorf("read the push URL of remote %s: %w", remote, err)
	}
	url, _, _ := strings.Cut(out, "\n")
	owner, ok := ownerFromRemoteURL(url)
	if !ok {
		return "", fmt.Errorf("cannot tell which account remote %s pushes to: its push URL does not end in owner/repo", remote)
	}
	return owner, nil
}

// ownerFromRemoteURL returns the owner in a remote URL of the form
// https://host/owner/repo.git, ssh://git@host:port/owner/repo.git or
// git@host:owner/repo.git. A plain path is read the same way, from its last two
// components.
func ownerFromRemoteURL(raw string) (string, bool) {
	path := raw
	if _, rest, ok := strings.Cut(raw, "://"); ok {
		if _, path, ok = strings.Cut(rest, "/"); !ok {
			return "", false
		}
	} else if host, rest, ok := strings.Cut(raw, ":"); ok && !strings.Contains(host, "/") {
		path = rest
	}
	path = strings.TrimSuffix(strings.TrimRight(path, "/"), ".git")
	dir, repo := gopath.Split(path)
	owner := gopath.Base(strings.TrimRight(dir, "/"))
	if repo == "" || owner == "." || owner == ".." {
		return "", false
	}
	return owner, true
}

// fetchPRBranch fetches one branch into refs/remotes/<remote>/<name>, the only
// ref it writes. It reports onRemote=false when the remote has no such branch.
func fetchPRBranch(remote, name, remoteRef string) (onRemote bool, err error) {
	refspec := "+refs/heads/" + name + ":" + remoteRef
	args := []string{"fetch", "--no-tags", "--no-recurse-submodules"}
	// A checkout has one FETCH_HEAD, and a plain fetch rewrites it. A `git pull`
	// that the agent working in this checkout runs at the same moment reads it
	// after its own fetch, so it could merge this branch into the one that agent
	// has checked out.
	if ok, installed := gitversion.AtLeast(2, 29); ok {
		args = append(args, "--no-write-fetch-head")
	} else {
		prWarnf("git %s cannot skip writing FETCH_HEAD (needs 2.29): a `git pull` running in this checkout right now could merge %s/%s", installed, remote, name)
	}
	_, fetchErr := prGit(gitPushTimeout, append(args, remote, refspec)...)
	if fetchErr == nil {
		return true, nil
	}
	// ls-remote tells a missing branch (exit 2) from a network or remote error.
	_, lsErr := prGit(gitPushTimeout, "ls-remote", "--exit-code", "--heads", remote, "refs/heads/"+name)
	if prExitCode(lsErr) == 2 {
		return false, nil
	}
	return false, fmt.Errorf("fetch %s from %s: %w", name, remote, fetchErr)
}

// prDivergence counts the commits only a has and only b has.
func prDivergence(a, b string) (onlyA, onlyB int, err error) {
	out, err := prGit(gitLocalTimeout, "rev-list", "--left-right", "--count", a+"..."+b)
	if err != nil {
		return 0, 0, fmt.Errorf("compare %s with %s: %w", a, b, err)
	}
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("compare %s with %s: unexpected output %q", a, b, out)
	}
	if onlyA, err = strconv.Atoi(fields[0]); err != nil {
		return 0, 0, fmt.Errorf("compare %s with %s: %w", a, b, err)
	}
	if onlyB, err = strconv.Atoi(fields[1]); err != nil {
		return 0, 0, fmt.Errorf("compare %s with %s: %w", a, b, err)
	}
	return onlyA, onlyB, nil
}

// pushPRHead publishes the local head branch. The explicit refspec pushes the
// branch even when a tag has the same name or the worktree is on another
// branch, and it carries no "+", so git refuses anything but a fast-forward.
// It passes -u only when head.SetUpstream says to.
func pushPRHead(head prHead, remote string) error {
	target := remote + "/" + head.Name
	spinner := terminal.NewSpinner(fmt.Sprintf("Pushing to %s...", target))
	spinner.Start()

	ctx, cancel := context.WithTimeout(context.Background(), gitPushTimeout)
	defer cancel()
	refspec := "refs/heads/" + head.Name + ":refs/heads/" + head.Name
	args := []string{"push"}
	if head.SetUpstream {
		args = append(args, "-u")
	}
	output, err := exec.CommandContext(ctx, "git", append(args, remote, refspec)...).CombinedOutput()
	if err != nil {
		spinner.StopWithError(fmt.Sprintf("push failed: %s", strings.TrimSpace(string(output))))
		return fmt.Errorf("git push failed: %w", err)
	}

	if tip, _ := prCommit(head.Ref); tip != "" {
		spinner.StopWithSuccess(fmt.Sprintf("Pushed: %s to %s", tip, target))
	} else {
		spinner.StopWithSuccess(fmt.Sprintf("Pushed to %s", target))
	}
	return nil
}

// warnIfPRHeadMoved tells the caller when the head branch no longer points at
// the commit the PR text was written for.
func warnIfPRHeadMoved(head prHead) {
	if tip, _ := prCommit(head.Ref); tip != "" && tip != head.SHA {
		prWarnf("%s moved from %s to %s while the PR was generated; the PR text may not describe the commits now on the branch", head.Name, shortSHA(head.SHA), shortSHA(tip))
	}
}

// prBase is the branch a pull request merges into.
type prBase struct {
	// Short is the branch name gh receives as --base.
	Short string
	// Ref is the fully qualified ref the diff, log and file list are read
	// against: the remote branch when it exists, because a local copy can sit
	// behind it. It is never a short name. Git reads "origin/main" or "main"
	// through refs/tags/ and refs/heads/ before refs/remotes/, so a tag or a
	// local branch with that name would replace the ref that was checked.
	Ref string
	// Label is Ref without its refs/heads/ or refs/remotes/ prefix, the form
	// people and the model read: "origin/main" or "main".
	Label string
	// Explicit reports that --base or BUCKLEY_PR_BASE chose the branch.
	Explicit bool
}

func newPRBase(short, ref string, explicit bool) prBase {
	label := strings.TrimPrefix(strings.TrimPrefix(ref, "refs/heads/"), "refs/remotes/")
	return prBase{Short: short, Ref: ref, Label: label, Explicit: explicit}
}

// resolvePRBase picks the base branch. Precedence: --base, BUCKLEY_PR_BASE,
// the remote's default branch, <remote>/main|master|develop, then the same
// names locally. A "<remote>/" prefix is dropped because gh wants a branch name.
func resolvePRBase(flag, remote string) (prBase, error) {
	requested, source := strings.TrimSpace(flag), "--base"
	if requested == "" {
		requested, source = strings.TrimSpace(os.Getenv(envPRBase)), envPRBase
	}
	if requested != "" {
		short := strings.TrimPrefix(requested, remote+"/")
		if err := checkBranchName(short); err != nil {
			return prBase{}, fmt.Errorf("%s: %w", source, err)
		}
		ref, ok := prBaseRef(remote, short)
		if !ok {
			return prBase{}, fmt.Errorf("%s: base branch %q was not found locally or on %s", source, short, remote)
		}
		return newPRBase(short, ref, true), nil
	}

	remotePrefix := "refs/remotes/" + remote + "/"
	if ref, err := prGit(gitLocalTimeout, "symbolic-ref", "--quiet", remotePrefix+"HEAD"); err == nil {
		if short := strings.TrimPrefix(ref, remotePrefix); short != ref && prRefExists(ref) {
			return newPRBase(short, ref, false), nil
		}
	}
	candidates := []string{"main", "master", "develop"}
	for _, short := range candidates {
		if ref := remotePrefix + short; prRefExists(ref) {
			return newPRBase(short, ref, false), nil
		}
	}
	for _, short := range candidates {
		if ref := "refs/heads/" + short; prRefExists(ref) {
			return newPRBase(short, ref, false), nil
		}
	}
	return prBase{}, fmt.Errorf("cannot detect the base branch: pass --base <branch> or set %s", envPRBase)
}

// prBaseRef returns the fully qualified ref of base branch short: the remote
// branch when it exists, else the local one.
func prBaseRef(remote, short string) (string, bool) {
	for _, ref := range []string{"refs/remotes/" + remote + "/" + short, "refs/heads/" + short} {
		if prRefExists(ref) {
			return ref, true
		}
	}
	return "", false
}

// countPRCommits counts the commits head has that base lacks.
func countPRCommits(base, head string) (int, error) {
	out, err := prGit(gitLocalTimeout, "rev-list", "--count", base+".."+head)
	if err != nil {
		return 0, fmt.Errorf("count commits in %s..%s: %w", base, head, err)
	}
	n, err := strconv.Atoi(out)
	if err != nil {
		return 0, fmt.Errorf("count commits in %s..%s: %w", base, head, err)
	}
	return n, nil
}

// prCommit returns the commit ref points at. It returns "" and no error when
// the ref does not exist.
func prCommit(ref string) (string, error) {
	out, err := prGit(gitLocalTimeout, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		if prExitCode(err) == 1 {
			return "", nil
		}
		return "", fmt.Errorf("resolve %s: %w", ref, err)
	}
	return out, nil
}

func prRefExists(ref string) bool {
	sha, err := prCommit(ref)
	return err == nil && sha != ""
}

// prGit runs git with a deadline and returns its trimmed standard output. A
// failure wraps the exit error, so prExitCode can read the status, and carries
// git's standard error text.
func prGit(timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", append([]string{"--no-pager"}, args...)...).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			if stderr := strings.TrimSpace(string(exit.Stderr)); stderr != "" {
				return "", fmt.Errorf("git %s: %w: %s", args[0], err, stderr)
			}
		}
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return strings.TrimSpace(string(out)), nil
}

// prExitCode returns the exit status inside err, or -1 when err holds none.
func prExitCode(err error) int {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// prWarnf prints a warning on standard error, which stays out of the PR text a
// caller may be capturing from standard output.
func prWarnf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "warning: "+format+"\n", args...)
}
