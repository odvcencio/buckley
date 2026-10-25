// Package shadowgit snapshots a git worktree into a hidden ref namespace
// (refs/buckley/undo-live/<session>/<handle>) so the TUI can revert and restore file
// changes turn by turn, without creating visible branches or touching the
// caller's git index. It is the file-side twin of Buckley's conversation
// history: every operation here is plain git plumbing (write-tree,
// commit-tree, update-ref, diff, cat-file) run against a temporary index
// file, never the repository's real one.
package shadowgit

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
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

// Store owns one live handle's hidden undo ref inside a git working tree.
type Store struct {
	repoDir string
	ref     string
	mu      sync.Mutex
	closed  bool
}

// New returns a Store rooted at repoDir for sessionID. It returns
// ErrNotAGitRepo when repoDir is not inside a git working tree, since
// shadow-git snapshots have nowhere durable to live otherwise.
func New(repoDir, sessionID string) (*Store, error) {
	repoDir = strings.TrimSpace(repoDir)
	if repoDir == "" {
		return nil, fmt.Errorf("shadowgit: repoDir cannot be empty")
	}
	store := &Store{repoDir: repoDir, ref: "refs/buckley/undo-live/" + SanitizeRefComponent(sessionID) + "/" + rand.Text()}
	if _, err := store.run(context.Background(), nil, "rev-parse", "--git-dir"); err != nil {
		return nil, ErrNotAGitRepo
	}
	return store, nil
}

// RetainTrees pins exactly the trees needed by this live undo handle. Its
// parentless root does not retain discarded turns or another handle's history.
// Releasing a tree makes it eligible for ordinary Git garbage collection.
func (s *Store) RetainTrees(ctx context.Context, trees []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("shadowgit: retain trees: %w", os.ErrClosed)
	}
	if len(trees) == 0 {
		return s.clearRef(ctx)
	}
	trees = slices.Clone(trees)
	slices.Sort(trees)
	trees = slices.Compact(trees)
	var input strings.Builder
	for i, tree := range trees {
		fmt.Fprintf(&input, "040000 tree %s\t%04d\n", tree, i)
	}
	out, err := s.runBytes(ctx, nil, strings.NewReader(input.String()), "mktree")
	if err != nil {
		return fmt.Errorf("shadowgit: retain trees: %w", err)
	}
	commit, err := s.CommitTree(ctx, strings.TrimSpace(string(out)), "", "buckley-undo: retained trees")
	if err != nil {
		return err
	}
	return s.UpdateRef(ctx, commit)
}

// Close releases only this handle's ref and prevents late turns from repinning it.
// Other handles, legacy refs, branches, the index and Git objects are untouched.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return s.clearRef(context.Background())
}

func (s *Store) clearRef(ctx context.Context) error {
	if _, err := s.run(ctx, nil, "update-ref", "-d", s.ref); err != nil {
		return fmt.Errorf("shadowgit: release undo ref: %w", err)
	}
	return nil
}

// RefName returns the hidden ref this store reads and writes.
func (s *Store) RefName() string {
	return s.ref
}

// WriteTree snapshots the current worktree into a git tree object via a
// temporary index file and returns its SHA. It never reads or writes
// repoDir's real .git/index.
func (s *Store) WriteTree(ctx context.Context) (string, error) {
	tmpIndexPath, cleanup, err := temporaryIndex()
	if err != nil {
		return "", err
	}
	defer cleanup()

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

func temporaryIndex() (string, func(), error) {
	file, err := os.CreateTemp("", "buckley-undo-index-*")
	if err != nil {
		return "", nil, fmt.Errorf("shadowgit: create temp index: %w", err)
	}
	name := file.Name()
	_ = file.Close()
	// Git rejects a zero-byte index. Let it create a fresh one at this path.
	if err := os.Remove(name); err != nil {
		return "", nil, fmt.Errorf("shadowgit: prepare temp index: %w", err)
	}
	return name, func() {
		_ = os.Remove(name)
		_ = os.Remove(name + ".lock")
	}, nil
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
	// Status is 'A' (added), 'M' (modified), 'D' (removed), or 'T' (type changed).
	Status byte
}

// Diff returns the paths that differ between sourceTree and targetTree,
// with --no-renames so every change is reported as a plain add/modify/
// delete or type change. Paths are relative to the repository root.
func (s *Store) Diff(ctx context.Context, sourceTree, targetTree string) ([]DiffEntry, error) {
	out, err := s.runBytes(ctx, nil, nil, "diff", "--no-renames", "--raw", "-z", sourceTree, targetTree, "--")
	if err != nil {
		return nil, fmt.Errorf("shadowgit: diff: %w", err)
	}
	var entries []DiffEntry
	for len(out) > 0 {
		header, rest, ok := bytes.Cut(out, []byte{0})
		if !ok {
			return nil, fmt.Errorf("shadowgit: unterminated diff header")
		}
		name, rest, ok := bytes.Cut(rest, []byte{0})
		fields := strings.Fields(string(header))
		if !ok || len(fields) != 5 || len(fields[4]) != 1 {
			return nil, fmt.Errorf("shadowgit: invalid diff record")
		}
		if fields[0] == ":160000" || fields[1] == "160000" {
			return nil, fmt.Errorf("shadowgit: cannot restore submodule %q", name)
		}
		entries = append(entries, DiffEntry{Path: string(name), Status: fields[4][0]})
		out = rest
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
	if len(entries) == 0 {
		return nil
	}
	index, cleanup, err := temporaryIndex()
	if err != nil {
		return err
	}
	defer cleanup()
	env := []string{"GIT_INDEX_FILE=" + index}
	if _, err := s.run(ctx, env, "read-tree", "--no-sparse-checkout", sourceTree); err != nil {
		return fmt.Errorf("shadowgit: prepare restore index: %w", err)
	}
	if _, err := s.run(ctx, env, "update-index", "--refresh"); err != nil {
		return fmt.Errorf("shadowgit: worktree changed before restore: %w", err)
	}
	if err := s.checkUntracked(ctx, env, entries); err != nil {
		return err
	}
	if _, err := s.run(ctx, env, "read-tree", "--no-sparse-checkout", "--no-recurse-submodules", "-m", "-u", sourceTree, targetTree); err != nil {
		return fmt.Errorf("shadowgit: restore worktree: %w", err)
	}
	return nil
}

// read-tree may overwrite ignored files. Include them in this check, collapsing
// untracked directories so caches do not need to be enumerated file by file.
func (s *Store) checkUntracked(ctx context.Context, env []string, entries []DiffEntry) error {
	targets := make(map[string]bool, len(entries))
	parents := make(map[string]bool)
	for _, entry := range entries {
		if entry.Status == 'D' {
			continue
		}
		targets[entry.Path] = true
		for parent := path.Dir(entry.Path); parent != "."; parent = path.Dir(parent) {
			parents[parent] = true
		}
	}
	out, err := s.runBytes(ctx, env, nil, "ls-files", "--others", "--directory", "--full-name", "-z")
	if err != nil {
		return fmt.Errorf("shadowgit: check untracked files: %w", err)
	}
	for _, name := range bytes.Split(out, []byte{0}) {
		if len(name) == 0 {
			continue
		}
		name := strings.TrimSuffix(string(name), "/")
		if targets[name] || parents[name] {
			return fmt.Errorf("shadowgit: restore would overwrite untracked path %q", name)
		}
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if targets[parent] {
				return fmt.Errorf("shadowgit: restore would overwrite untracked path %q", name)
			}
		}
	}
	return nil
}

func (s *Store) run(ctx context.Context, env []string, args ...string) (string, error) {
	out, err := s.runBytes(ctx, env, nil, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (s *Store) runBytes(ctx context.Context, env []string, input io.Reader, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = s.repoDir
	cmd.Stdin = input
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
