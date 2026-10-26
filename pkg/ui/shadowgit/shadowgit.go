// Package shadowgit snapshots worktree files into a live handle's private Git
// objects and retention ref. Capture and restore preserve repository settings
// through temporary indexes; collection uses only the owned bare repository.
// The user's branches, object database and real index remain untouched.
package shadowgit

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// ErrNotAGitRepo is returned by New when repoDir is not inside a git
// working tree.
var ErrNotAGitRepo = errors.New("shadowgit: not a git repository")

// ErrWorktreeChanged means the source snapshot no longer matches the worktree.
// Apply returns it before restoring any files.
var ErrWorktreeChanged = errors.New("shadowgit: worktree differs from source snapshot")

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

// Store owns one live handle's private snapshot objects and retention ref.
type Store struct {
	repoDir          string
	gitDir           string
	commonDir        string
	workTree         string
	pending          []string
	removed          bool
	ref              string
	mu               sync.Mutex
	closed           bool
	collectionNeeded bool
	lease            *cacheLease
}

// New returns a Store rooted at repoDir for sessionID. It returns
// ErrNotAGitRepo when repoDir is not inside a git working tree, since
// shadow-git snapshots have nowhere durable to live otherwise.
func New(repoDir, sessionID string) (*Store, error) {
	repoDir = strings.TrimSpace(repoDir)
	if repoDir == "" {
		return nil, fmt.Errorf("shadowgit: repoDir cannot be empty")
	}
	store := &Store{repoDir: repoDir, ref: "refs/worktree/buckley/undo-live/" + SanitizeRefComponent(sessionID) + "/" + rand.Text()}
	if err := store.initStorage(context.Background()); err != nil {
		store.lease.close()
		return nil, err
	}
	return store, nil
}

// RetainTrees pins exactly the trees needed by this live undo handle. Its
// parentless root does not retain discarded turns or another handle's history.
// Expired private objects are collected without touching the user's object database.
func (s *Store) RetainTrees(ctx context.Context, trees []string) error {
	committed, err := s.RetainTreesWithinLimit(ctx, trees, 0)
	if committed && err != nil {
		slog.WarnContext(ctx, "Could not collect released undo snapshots", "error", err)
		return nil
	}
	return err
}

// RetainTreesWithinLimit refuses a nonempty retention root that exceeds limit.
// Zero disables admission accounting. The committed result distinguishes an
// unchanged root from cleanup errors after publication; callers must publish
// their new records whenever committed is true. Snapshot capture is temporary
// staging and can exceed this retained-storage budget.
func (s *Store) RetainTreesWithinLimit(ctx context.Context, trees []string, limit int64) (committed bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false, fmt.Errorf("shadowgit: retain trees: %w", os.ErrClosed)
	}
	if limit < 0 {
		return false, fmt.Errorf("shadowgit: storage limit must be nonnegative")
	}
	defer func() {
		if err == nil || committed || errors.Is(err, ErrStorageLimit) {
			return
		}
		// A failed publication leaves only the previous root supported. Do not
		// accumulate fresh staging snapshots while publication stays blocked.
		clear(s.pending)
		s.pending = nil
		collectErr := s.collect(ctx)
		s.collectionNeeded = collectErr != nil
		err = errors.Join(err, collectErr)
	}()
	if len(trees) == 0 {
		if err := s.clearRef(ctx); err != nil {
			return false, err
		}
		clear(s.pending)
		s.pending = nil
		err := s.collect(ctx)
		s.collectionNeeded = err != nil
		return true, err
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
		return false, fmt.Errorf("shadowgit: retain trees: %w", err)
	}
	commit, err := s.commitTree(ctx, strings.TrimSpace(string(out)), "", "buckley-undo: retained trees")
	if err != nil {
		return false, err
	}
	var pack *retentionPack
	if limit > 0 {
		var used int64
		used, pack, err = s.retainedBytes(ctx, commit)
		if err != nil {
			return false, err
		}
		if pack != nil {
			defer func() {
				cleanupErr := pack.close()
				s.collectionNeeded = s.collectionNeeded || cleanupErr != nil
				err = errors.Join(err, cleanupErr)
			}()
		}
		if used > limit {
			return false, fmt.Errorf("%w: need %d bytes, limit %d", ErrStorageLimit, used, limit)
		}
	}
	if err := s.updateRef(ctx, commit); err != nil {
		return false, err
	}
	clear(s.pending)
	s.pending = nil
	if pack != nil {
		err = s.collectPack(ctx, pack)
	} else {
		err = s.collect(ctx)
	}
	s.collectionNeeded = err != nil
	return true, err
}

// Close releases only this handle's ref and prevents late turns from repinning it.
// It removes the owned cache without changing other handles or the user's Git data.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.removed {
		return nil
	}
	s.closed = true
	return s.closeStorage()
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", fmt.Errorf("shadowgit: snapshot: %w", os.ErrClosed)
	}
	if s.collectionNeeded {
		if err := s.collect(ctx); err != nil {
			return "", err
		}
		s.collectionNeeded = false
	}
	tree, err := s.writeTree(ctx)
	if err != nil {
		collectErr := s.collect(ctx)
		s.collectionNeeded = collectErr != nil
		return "", errors.Join(err, collectErr)
	}
	s.pending = append(s.pending, tree)
	return tree, nil
}

func (s *Store) writeTree(ctx context.Context) (string, error) {
	tmpIndexPath, cleanup, err := s.temporaryIndex()
	if err != nil {
		return "", err
	}
	defer cleanup()
	return s.stageWorktree(ctx, []string{"GIT_INDEX_FILE=" + tmpIndexPath})
}

func (s *Store) stageWorktree(ctx context.Context, env []string) (string, error) {
	if _, err := s.run(ctx, env, "add", "--all", "--", "."); err != nil {
		return "", fmt.Errorf("shadowgit: stage worktree: %w", err)
	}
	tree, err := s.run(ctx, env, "write-tree")
	if err != nil {
		return "", fmt.Errorf("shadowgit: write-tree: %w", err)
	}
	return tree, nil
}

func (s *Store) temporaryIndex() (string, func(), error) {
	file, err := os.CreateTemp(s.gitDir, "index-*")
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", fmt.Errorf("shadowgit: commit: %w", os.ErrClosed)
	}
	commit, err := s.commitTree(ctx, treeSHA, parentSHA, message)
	if err == nil {
		s.pending = append(s.pending, commit)
	}
	return commit, err
}

func (s *Store) commitTree(ctx context.Context, treeSHA, parentSHA, message string) (string, error) {
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("shadowgit: update ref: %w", os.ErrClosed)
	}
	if err := s.updateRef(ctx, commitSHA); err != nil {
		return err
	}
	clear(s.pending)
	s.pending = nil
	return nil
}

func (s *Store) updateRef(ctx context.Context, commitSHA string) error {
	if _, err := s.run(ctx, nil, "update-ref", s.ref, commitSHA); err != nil {
		return fmt.Errorf("shadowgit: update-ref: %w", err)
	}
	return nil
}

// CurrentRef returns the hidden ref's current commit SHA, or "" if the ref
// does not exist yet.
func (s *Store) CurrentRef(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", nil
	}
	sha, err := s.run(ctx, nil, "rev-parse", "--verify", "--quiet", s.ref)
	if err != nil {
		return "", nil // ref does not exist yet; not an error
	}
	return sha, nil
}

// CommitTreeSHA returns the tree a commit points at.
func (s *Store) CommitTreeSHA(ctx context.Context, commitSHA string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", fmt.Errorf("shadowgit: resolve tree: %w", os.ErrClosed)
	}
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, fmt.Errorf("shadowgit: diff: %w", os.ErrClosed)
	}
	return s.diff(ctx, sourceTree, targetTree)
}

func (s *Store) diff(ctx context.Context, sourceTree, targetTree string) ([]DiffEntry, error) {
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
// Apply performs the same check itself before restoring any files.
func (s *Store) Clean(ctx context.Context, expectedTree string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false, fmt.Errorf("shadowgit: check worktree: %w", os.ErrClosed)
	}
	clean, err := s.clean(ctx, expectedTree)
	return clean, errors.Join(err, s.collect(ctx))
}

func (s *Store) clean(ctx context.Context, expectedTree string) (bool, error) {
	current, err := s.writeTree(ctx)
	if err != nil {
		return false, err
	}
	return current == expectedTree, nil
}

// Apply restores repoDir's worktree so it matches targetTree for exactly
// the paths that differ from sourceTree, leaving every other file
// untouched. It refuses with ErrWorktreeChanged if the current worktree no
// longer matches sourceTree, even when the two snapshots have no file changes.
func (s *Store) Apply(ctx context.Context, sourceTree, targetTree string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("shadowgit: restore: %w", os.ErrClosed)
	}
	if err := s.collect(ctx); err != nil {
		return err
	}
	entries, err := s.diff(ctx, sourceTree, targetTree)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		clean, err := s.clean(ctx, sourceTree)
		if err != nil {
			return err
		}
		if !clean {
			return ErrWorktreeChanged
		}
		return nil
	}
	index, cleanup, err := s.temporaryIndex()
	if err != nil {
		return err
	}
	defer cleanup()
	env := []string{"GIT_INDEX_FILE=" + index}
	current, err := s.stageWorktree(ctx, env)
	if err != nil {
		return err
	}
	if current != sourceTree {
		return errors.Join(ErrWorktreeChanged, s.collect(ctx))
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

func (s *Store) command(ctx context.Context, env []string, args ...string) *exec.Cmd {
	// Every index used here is private and short-lived. Split indexes would
	// leave sharedindex sidecars in the user's Git directory after cleanup.
	options := []string{"-c", "core.splitIndex=false", "-c", "core.logAllRefUpdates=false", "-c", "gc.auto=0", "-c", "maintenance.auto=false"}
	if s.gitDir != "" {
		options = append(options, "-c", "core.hooksPath="+filepath.Join(s.gitDir, "hooks"))
	}
	cmd := exec.CommandContext(ctx, "git", append(options, args...)...)
	cmd.Dir = s.repoDir
	cmd.Env = s.environment(env)
	if s.lease != nil && s.lease.file != nil {
		inheritCacheLease(cmd, s.lease.file)
	}
	return cmd
}

func (s *Store) runBytes(ctx context.Context, env []string, input io.Reader, args ...string) ([]byte, error) {
	cmd := s.command(ctx, env, args...)
	cmd.Stdin = input
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
