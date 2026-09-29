package workspaceevidence

import (
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
	localCommits := make(map[string]string)
	for _, entry := range strings.Split(strings.TrimSuffix(reflog, r.startReflog), "\n") {
		id, action, ok := strings.Cut(entry, "\x00")
		if ok && (strings.HasPrefix(action, "commit: ") || strings.HasPrefix(action, "commit (initial): ") || strings.HasPrefix(action, "commit (amend): ")) {
			localCommits[id] = action
		}
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
	for _, commit := range strings.Fields(string(commits)) {
		action, local := localCommits[commit]
		if !local {
			continue
		}
		diffArgs := []string{"diff-tree", "--root", "--no-commit-id", "--name-only", "-r", "--no-ext-diff"}
		if strings.HasPrefix(action, "commit (amend): ") && r.startHead != "" {
			// A message-only amend must not claim the starting commit's edits.
			diffArgs = append(diffArgs, r.startHead)
		}
		diffArgs = append(diffArgs, commit, "--")
		changes, err := gitOutput(ctx, r.root, maxFingerprintPathBytes, diffArgs...)
		if err != nil {
			return false, fmt.Errorf("observe session commit changes: %w", err)
		}
		if len(changes) > 0 {
			return true, nil
		}
	}
	return false, nil
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
