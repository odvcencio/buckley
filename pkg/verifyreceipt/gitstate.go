package verifyreceipt

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// State is the git identity of a work tree at one moment.
type State struct {
	Root   string
	RelDir string
	Repo   string
	Commit string
	Tree   string
	// Dirty is true when git reports any change, including untracked files.
	Dirty bool
	// TrackedChanged is true when a tracked file differs from HEAD.
	TrackedChanged bool
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if asExit(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

func asExit(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
}

// StateOf reads the repository root, commit, tree hash and cleanliness for the
// work tree containing dir.
func StateOf(ctx context.Context, dir string) (State, error) {
	root, err := git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return State{}, fmt.Errorf("verify: %w", err)
	}
	commit, err := git(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return State{}, fmt.Errorf("verify: %w", err)
	}
	tree, err := git(ctx, root, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return State{}, fmt.Errorf("verify: %w", err)
	}
	all, err := git(ctx, root, "status", "--porcelain")
	if err != nil {
		return State{}, fmt.Errorf("verify: %w", err)
	}
	tracked, err := git(ctx, root, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return State{}, fmt.Errorf("verify: %w", err)
	}
	repo, err := git(ctx, root, "config", "--get", "remote.origin.url")
	if err != nil || repo == "" {
		repo = root
	}
	abs, _ := filepath.Abs(dir)
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == "" {
		rel = "."
	}
	return State{
		Root: root, RelDir: filepath.ToSlash(rel), Repo: repo, Commit: commit, Tree: tree,
		Dirty: all != "", TrackedChanged: tracked != "",
	}, nil
}

// TreeOf returns the tree hash of rev in the repository containing dir, or an
// error when the revision is not available locally.
func TreeOf(ctx context.Context, dir, rev string) (string, error) {
	return git(ctx, dir, "rev-parse", "--verify", rev+"^{tree}")
}
