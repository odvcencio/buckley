package workspaceevidence

import (
	"bytes"
	"context"
	"fmt"
	"strings"
)

// GitMutationRecorder keeps the run's starting history and workspace state.
// Local commit reflog entries distinguish task commits from imported history,
// even when the task branch is pushed or merges a remote branch.
type GitMutationRecorder struct {
	root        string
	startHead   string
	startBranch string
	startReflog string
	startState  string
}

func NewGitMutationRecorder(ctx context.Context, root string) (*GitMutationRecorder, error) {
	ctx, cancel := context.WithTimeout(ctx, fingerprintTimeout(root))
	defer cancel()
	head, unborn, err := gitFingerprintHead(ctx, root)
	if err != nil {
		return nil, err
	}
	branch, err := gitOutput(ctx, root, maxFingerprintPathBytes, "rev-parse", "--symbolic-full-name", "HEAD")
	if err != nil && unborn == "" {
		return nil, fmt.Errorf("observe session branch: %w", err)
	}
	recorder := &GitMutationRecorder{root: root, startHead: head, startBranch: strings.TrimSpace(string(branch))}
	if unborn != "" {
		recorder.startBranch = unborn
	} else {
		recorder.startReflog, err = gitMutationReflog(ctx, root)
		if err != nil {
			return nil, err
		}
	}
	recorder.startState, err = gitMutationFingerprint(ctx, root)
	if err != nil {
		return nil, err
	}
	return recorder, nil
}

// Observe reports content changes or nonempty commits created in this run.
// HEAD movement alone, including a merge or fast-forward, is not task work.
func (r *GitMutationRecorder) Observe(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, fingerprintTimeout(r.root))
	defer cancel()
	head, _, err := gitFingerprintHead(ctx, r.root)
	if err != nil {
		return false, err
	}
	if head != "" && head != r.startHead {
		committed, err := r.committedMutation(ctx, head)
		if err != nil || committed {
			return committed, err
		}
	}
	state, err := gitMutationFingerprint(ctx, r.root)
	if err != nil {
		return false, err
	}
	return state != r.startState, nil
}

func (r *GitMutationRecorder) committedMutation(ctx context.Context, head string) (bool, error) {
	reflog, err := gitMutationReflog(ctx, r.root)
	if err != nil {
		return false, err
	}
	if !strings.HasSuffix(reflog, r.startReflog) {
		return false, fmt.Errorf("session reflog for %s no longer contains the starting history", r.startBranch)
	}
	// Each attributed commit has a comparison base. An amendment of imported
	// history starts at its immediate predecessor; further amendments inherit
	// that base so message changes retain only the session's content edits.
	localCommits := make(map[string]string)
	replayed := make(map[string]bool)
	entries := strings.Split(strings.TrimSuffix(reflog, r.startReflog), "\n")
	previous := r.startHead
	for i := len(entries) - 1; i >= 0; i-- {
		id, action, ok := strings.Cut(entries[i], "\x00")
		if !ok {
			continue
		}
		switch {
		case strings.HasPrefix(action, "commit: "), strings.HasPrefix(action, "commit (initial): "):
			localCommits[id] = ""
		case strings.HasPrefix(action, "commit (amend): "):
			base := previous
			if inherited, local := localCommits[previous]; local {
				base = inherited
			}
			localCommits[id] = base
			if replayed[previous] {
				replayed[id] = true
			}
		case strings.HasPrefix(action, "rebase"):
			replayed[id] = true
		}
		previous = id
	}
	if len(localCommits) == 0 {
		return false, nil
	}
	args := []string{"rev-list", "--no-merges", head}
	if r.startHead != "" {
		args = append(args, "^"+r.startHead)
	}
	args = append(args, "--")
	commits, err := gitOutput(ctx, r.root, maxFingerprintPathBytes, args...)
	if err != nil {
		return false, fmt.Errorf("observe session commits: %w", err)
	}
	reachable := make(map[string]bool)
	for _, commit := range strings.Fields(string(commits)) {
		reachable[commit] = true
	}
	var rewrittenLocal []string
	for commit, base := range localCommits {
		diffArgs := []string{"diff-tree", "--root", "--no-commit-id", "--name-only", "-r", "--no-ext-diff"}
		if base != "" {
			diffArgs = append(diffArgs, base)
		}
		diffArgs = append(diffArgs, commit, "--")
		changes, err := gitOutput(ctx, r.root, maxFingerprintPathBytes, diffArgs...)
		if err != nil {
			return false, fmt.Errorf("observe session commit changes: %w", err)
		}
		if len(changes) == 0 {
			continue
		}
		if reachable[commit] {
			return true, nil
		}
		rewrittenLocal = append(rewrittenLocal, commit)
	}
	// Compare only session-attributed patches with reachable rebase results.
	// Zero-context patches tolerate nearby upstream edits during a clean rebase.
	localPatches := make(map[string]bool)
	for _, commit := range rewrittenLocal {
		patch, err := gitMutationPatchID(ctx, r.root, commit)
		if err != nil {
			return false, err
		}
		if patch != "" {
			localPatches[patch] = true
		}
	}
	for commit := range replayed {
		if !reachable[commit] || len(localPatches) == 0 {
			continue
		}
		patch, err := gitMutationPatchID(ctx, r.root, commit)
		if err != nil {
			return false, err
		}
		if localPatches[patch] {
			return true, nil
		}
	}
	return false, nil
}

func gitMutationPatchID(ctx context.Context, root, commit string) (string, error) {
	patch, err := gitOutput(ctx, root, maxFingerprintPathBytes, "diff-tree", "--root", "--no-commit-id", "--no-ext-diff", "--no-renames", "--unified=0", "--binary", "-p", commit, "--")
	if err != nil {
		return "", fmt.Errorf("observe session patch: %w", err)
	}
	result, err := runGitCommandWithInput(ctx, root, maxFingerprintPathBytes, bytes.NewReader(patch), "patch-id", "--stable")
	if err != nil {
		return "", fmt.Errorf("observe session patch ID: %w", err)
	}
	if result.exitCode != 0 {
		return "", gitCommandExitError(result, "patch-id")
	}
	if fields := strings.Fields(string(result.stdout)); len(fields) > 0 {
		return fields[0], nil
	}
	return "", nil
}

func gitMutationReflog(ctx context.Context, root string) (string, error) {
	raw, err := gitOutput(ctx, root, maxFingerprintPathBytes, "reflog", "show", "--format=%H%x00%gs", "HEAD")
	if err != nil {
		return "", fmt.Errorf("observe session reflog: %w", err)
	}
	return string(raw), nil
}

func gitMutationFingerprint(ctx context.Context, root string) (string, error) {
	return gitStateFingerprint(ctx, root, true)
}
