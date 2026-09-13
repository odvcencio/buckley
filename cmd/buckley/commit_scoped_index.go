package main

import (
	"bytes"
	"context"
	"fmt"
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
	data, err := os.ReadFile(indexPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read staged index: %w", err)
	}
	// A sibling preserves relative shared-index references in split indexes.
	snapshot, err := os.CreateTemp(filepath.Dir(indexPath), "buckley-commit-index-")
	if err != nil {
		return nil, nil, fmt.Errorf("create scoped index: %w", err)
	}
	snapshotPath = snapshot.Name()
	_, writeErr := snapshot.Write(data)
	closeErr := snapshot.Close()
	if writeErr != nil {
		return nil, nil, fmt.Errorf("copy staged index: %w", writeErr)
	}
	if closeErr != nil {
		return nil, nil, fmt.Errorf("close scoped index: %w", closeErr)
	}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_INDEX_FILE=") && !strings.HasPrefix(entry, "GIT_LITERAL_PATHSPECS=") {
			env = append(env, entry)
		}
	}
	env = append(env, "GIT_INDEX_FILE="+snapshotPath, "GIT_LITERAL_PATHSPECS=1")
	run := func(input []byte, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Env = env
		cmd.Stdin = bytes.NewReader(input)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, runErr := cmd.Output()
		if runErr != nil {
			return nil, fmt.Errorf("git %s: %w: %s", args[0], runErr, strings.TrimSpace(stderr.String()))
		}
		return out, nil
	}
	// Include intent-to-add names so even their empty index entries are excluded
	// when they are outside the requested scope.
	names, err := run(nil, "diff", "--cached", "--ita-visible-in-index", "--name-only", "--no-renames", "-z")
	if err != nil {
		return nil, nil, err
	}
	var outside bytes.Buffer
	for _, name := range bytes.Split(names, []byte{0}) {
		if len(name) > 0 && !fileMatchesPaths(string(name), paths) {
			outside.Write(name)
			outside.WriteByte(0)
		}
	}
	if outside.Len() > 0 {
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
		if _, err = run(outside.Bytes(), "restore", "--staged", "--source="+base, "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
			return nil, nil, err
		}
	}
	return env, release, nil
}
