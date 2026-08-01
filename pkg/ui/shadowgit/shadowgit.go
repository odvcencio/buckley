// Package shadowgit snapshots a git worktree into a hidden ref namespace
// (refs/buckley/undo/<session>) so the TUI can revert and restore file
// changes turn by turn, without creating visible branches or touching the
// caller's git index. It is the file-side twin of Buckley's conversation
// history: every operation here is plain git plumbing (write-tree,
// commit-tree, update-ref, diff, cat-file) run against a temporary index
// file, never the repository's real one.
package shadowgit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// ErrNotAGitRepo is returned by New when repoDir is not inside a git
// working tree.
var ErrNotAGitRepo = errors.New("shadowgit: not a git repository")

// EmptyTree is git's well-known SHA-1 for an empty tree, used as the
// "before" state for a session's very first tracked turn.
const EmptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

var unsafeRefChars = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

// SanitizeRefComponent maps sessionID onto characters git accepts in a ref
// name, so any session identifier is safe to embed in refs/buckley/undo/<id>.
func SanitizeRefComponent(sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	sanitized := unsafeRefChars.ReplaceAllString(sessionID, "_")
	sanitized = strings.Trim(sanitized, "._")
	if sanitized == "" {
		sanitized = "session"
	}
	return sanitized
}

// Store wraps the hidden undo ref for one session inside one git working
// tree.
type Store struct {
	repoDir string
	ref     string
}

// New returns a Store rooted at repoDir for sessionID. It returns
// ErrNotAGitRepo when repoDir is not inside a git working tree, since
// shadow-git snapshots have nowhere durable to live otherwise.
func New(repoDir, sessionID string) (*Store, error) {
	repoDir = strings.TrimSpace(repoDir)
	if repoDir == "" {
		return nil, fmt.Errorf("shadowgit: repoDir cannot be empty")
	}
	store := &Store{repoDir: repoDir, ref: "refs/buckley/undo/" + SanitizeRefComponent(sessionID)}
	if _, err := store.run(context.Background(), nil, "rev-parse", "--git-dir"); err != nil {
		return nil, ErrNotAGitRepo
	}
	return store, nil
}

// RefName returns the hidden ref this store reads and writes.
func (s *Store) RefName() string {
	return s.ref
}

// WriteTree snapshots the current worktree into a git tree object via a
// temporary index file and returns its SHA. It never reads or writes
// repoDir's real .git/index.
func (s *Store) WriteTree(ctx context.Context) (string, error) {
	tmpIndex, err := os.CreateTemp("", "buckley-undo-index-*")
	if err != nil {
		return "", fmt.Errorf("shadowgit: create temp index: %w", err)
	}
	tmpIndexPath := tmpIndex.Name()
	_ = tmpIndex.Close()
	// git treats a zero-byte index file as corrupt rather than empty, so
	// remove it and let `git add` create a fresh index at this path.
	if err := os.Remove(tmpIndexPath); err != nil {
		return "", fmt.Errorf("shadowgit: prepare temp index: %w", err)
	}
	defer os.Remove(tmpIndexPath)

	env := []string{"GIT_INDEX_FILE=" + tmpIndexPath}
	if _, err := s.run(ctx, env, "add", "--all", "--", "."); err != nil {
		return "", fmt.Errorf("shadowgit: stage worktree: %w", err)
	}
	tree, err := s.run(ctx, env, "write-tree")
	if err != nil {
		return "", fmt.Errorf("shadowgit: write-tree: %w", err)
	}
	return tree, nil
}

// CommitTree wraps treeSHA in a commit object, with parentSHA as its
// parent (pass "" for no parent), and returns the new commit's SHA. It
// does not move any ref; call UpdateRef to publish it.
func (s *Store) CommitTree(ctx context.Context, treeSHA, parentSHA, message string) (string, error) {
	args := []string{"commit-tree", treeSHA}
	if parentSHA != "" {
		args = append(args, "-p", parentSHA)
	}
	args = append(args, "-m", message)
	env := []string{
		"GIT_AUTHOR_NAME=buckley-undo",
		"GIT_AUTHOR_EMAIL=buckley-undo@localhost",
		"GIT_COMMITTER_NAME=buckley-undo",
		"GIT_COMMITTER_EMAIL=buckley-undo@localhost",
	}
	commit, err := s.run(ctx, env, args...)
	if err != nil {
		return "", fmt.Errorf("shadowgit: commit-tree: %w", err)
	}
	return commit, nil
}

// UpdateRef points the hidden ref at commitSHA, keeping it (and every
// ancestor tree it can reach) alive for future undo/redo.
func (s *Store) UpdateRef(ctx context.Context, commitSHA string) error {
	if _, err := s.run(ctx, nil, "update-ref", s.ref, commitSHA); err != nil {
		return fmt.Errorf("shadowgit: update-ref: %w", err)
	}
	return nil
}

// CurrentRef returns the hidden ref's current commit SHA, or "" if the ref
// does not exist yet.
func (s *Store) CurrentRef(ctx context.Context) (string, error) {
	sha, err := s.run(ctx, nil, "rev-parse", "--verify", "--quiet", s.ref)
	if err != nil {
		return "", nil // ref does not exist yet; not an error
	}
	return sha, nil
}

// CommitTreeSHA returns the tree a commit points at.
func (s *Store) CommitTreeSHA(ctx context.Context, commitSHA string) (string, error) {
	tree, err := s.run(ctx, nil, "rev-parse", "--verify", commitSHA+"^{tree}")
	if err != nil {
		return "", fmt.Errorf("shadowgit: resolve commit tree: %w", err)
	}
	return tree, nil
}

// DiffEntry is one path that differs between two trees.
type DiffEntry struct {
	Path string
	// Status is 'A' (added in target), 'M' (modified), or 'D' (removed in
	// target).
	Status byte
}

// Diff returns the paths that differ between sourceTree and targetTree,
// with --no-renames so every change is reported as a plain add/modify/
// delete triple that Apply can restore file by file.
func (s *Store) Diff(ctx context.Context, sourceTree, targetTree string) ([]DiffEntry, error) {
	out, err := s.run(ctx, nil, "diff", "--no-renames", "--name-status", sourceTree, targetTree)
	if err != nil {
		return nil, fmt.Errorf("shadowgit: diff: %w", err)
	}
	if out == "" {
		return nil, nil
	}
	var entries []DiffEntry
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 2)
		if len(fields) != 2 {
			continue
		}
		status := fields[0]
		if len(status) == 0 {
			continue
		}
		entries = append(entries, DiffEntry{Path: fields[1], Status: status[0]})
	}
	return entries, nil
}

// Clean reports whether the current worktree tree matches expectedTree.
// Callers use this as the dirty-state check before undo/redo: when it is
// false, the worktree has changes the tracked turn did not make, and the
// caller should refuse the operation.
func (s *Store) Clean(ctx context.Context, expectedTree string) (bool, error) {
	current, err := s.WriteTree(ctx)
	if err != nil {
		return false, err
	}
	return current == expectedTree, nil
}

// Apply restores repoDir's worktree so it matches targetTree for exactly
// the paths that differ from sourceTree, leaving every other file
// (including anything the caller's dirty-state check has already vetted)
// untouched.
func (s *Store) Apply(ctx context.Context, sourceTree, targetTree string) error {
	entries, err := s.Diff(ctx, sourceTree, targetTree)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		full := filepath.Join(s.repoDir, entry.Path)
		switch entry.Status {
		case 'D':
			if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("shadowgit: remove %s: %w", entry.Path, err)
			}
		default: // 'A' or 'M'
			if err := s.restoreBlob(ctx, targetTree, entry.Path, full); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) restoreBlob(ctx context.Context, tree, path, destPath string) error {
	mode, err := s.blobMode(ctx, tree, path)
	if err != nil {
		return err
	}
	content, err := s.runBytes(ctx, nil, "show", tree+":"+path)
	if err != nil {
		return fmt.Errorf("shadowgit: read %s from tree: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("shadowgit: create parent dir for %s: %w", path, err)
	}
	perm := os.FileMode(0o644)
	if mode == "100755" {
		perm = 0o755
	}
	if err := os.WriteFile(destPath, content, perm); err != nil {
		return fmt.Errorf("shadowgit: write %s: %w", path, err)
	}
	return nil
}

func (s *Store) blobMode(ctx context.Context, tree, path string) (string, error) {
	out, err := s.run(ctx, nil, "ls-tree", tree, "--", path)
	if err != nil {
		return "", fmt.Errorf("shadowgit: ls-tree %s: %w", path, err)
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return "100644", nil
	}
	return fields[0], nil
}

func (s *Store) run(ctx context.Context, env []string, args ...string) (string, error) {
	out, err := s.runBytes(ctx, env, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (s *Store) runBytes(ctx context.Context, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = s.repoDir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.Bytes(), nil
}
