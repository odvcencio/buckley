package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// prepareScopedCommitIndex isolates approved staged content while Git's normal
// index lock prevents another staging operation from changing the original.
func prepareScopedCommitIndex(ctx context.Context, paths []string) (env []string, cleanup func(), err error) {
	output, err := exec.CommandContext(ctx, "git", "rev-parse", "--git-path", "index").Output()
	if err != nil {
		return nil, nil, fmt.Errorf("resolve git index: %w", err)
	}
	indexPath, err := filepath.Abs(strings.TrimSuffix(string(output), "\n"))
	if err != nil {
		return nil, nil, fmt.Errorf("resolve absolute git index: %w", err)
	}
	lockPath := indexPath + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, nil, fmt.Errorf("lock staged index (another Git operation may be active): %w", err)
	}
	var snapshotPath string
	release := func() {
		if snapshotPath != "" {
			_ = os.Remove(snapshotPath + ".lock")
			_ = os.Remove(snapshotPath)
		}
		_ = os.Remove(lockPath)
	}
	defer func() {
		if err != nil {
			release()
		}
	}()
	if err = lock.Close(); err != nil {
		return nil, nil, fmt.Errorf("close index lock: %w", err)
	}
	index, err := os.Open(indexPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open staged index: %w", err)
	}
	defer index.Close()
	// A sibling preserves relative shared-index references in split indexes.
	snapshot, err := os.CreateTemp(filepath.Dir(indexPath), "buckley-commit-index-")
	if err != nil {
		return nil, nil, fmt.Errorf("create scoped index: %w", err)
	}
	snapshotPath = snapshot.Name()
	_, writeErr := io.Copy(snapshot, index)
	closeErr := snapshot.Close()
	if writeErr != nil {
		return nil, nil, fmt.Errorf("copy staged index: %w", writeErr)
	}
	if closeErr != nil {
		return nil, nil, fmt.Errorf("close scoped index: %w", closeErr)
	}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_INDEX_FILE=") && !strings.HasPrefix(entry, "GIT_LITERAL_PATHSPECS=") &&
			!strings.HasPrefix(entry, "GIT_GLOB_PATHSPECS=") && !strings.HasPrefix(entry, "GIT_NOGLOB_PATHSPECS=") && !strings.HasPrefix(entry, "GIT_ICASE_PATHSPECS=") {
			env = append(env, entry)
		}
	}
	env = append(env, "GIT_INDEX_FILE="+snapshotPath, "GIT_LITERAL_PATHSPECS=1", "GIT_GLOB_PATHSPECS=0", "GIT_NOGLOB_PATHSPECS=0", "GIT_ICASE_PATHSPECS=0")
	run := func(input io.Reader, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Env = env
		cmd.Stdin = input
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, runErr := cmd.Output()
		if runErr != nil {
			return nil, fmt.Errorf("git %s: %w: %s", args[0], runErr, strings.TrimSpace(stderr.String()))
		}
		return out, nil
	}
	head := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "--quiet", "HEAD")
	head.Env = env
	headOutput, headErr := head.Output()
	base := strings.TrimSuffix(string(headOutput), "\n")
	if headErr != nil {
		exitErr, ok := headErr.(*exec.ExitError)
		if !ok || exitErr.ExitCode() != 1 {
			return nil, nil, fmt.Errorf("resolve scoped commit base: %w", headErr)
		}
		empty, treeErr := run(nil, "mktree")
		if treeErr != nil {
			return nil, nil, treeErr
		}
		base = strings.TrimSuffix(string(empty), "\n")
	}
	// Reset only the private index outside literal scopes. The positive root
	// includes outside additions and intent-to-add entries; excludes stay
	// relative to the caller's directory, including when it is a subdirectory.
	patterns := make([]io.Reader, 0, len(paths)+1)
	patterns = append(patterns, strings.NewReader(":/\x00"))
	for _, path := range paths {
		patterns = append(patterns, strings.NewReader(":(exclude,literal)"+path+"\x00"))
	}
	if _, err = run(io.MultiReader(patterns...), "-c", "core.splitIndex=false", "--no-literal-pathspecs", "--noglob-pathspecs", "reset", "--quiet", "--no-refresh", base, "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
		return nil, nil, fmt.Errorf("filter scoped index: %w", err)
	}
	return env, release, nil
}
