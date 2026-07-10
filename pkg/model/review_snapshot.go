package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"m31labs.dev/buckley/pkg/diffsignal"
)

// ReviewSnapshotMode selects the exact Git state exposed to native review
// verification. The captured descriptor is immutable and can be reproduced for
// every validation retry and approval-critic pass.
type ReviewSnapshotMode string

const (
	// ReviewSnapshotNone disables Git snapshot capture.
	ReviewSnapshotNone ReviewSnapshotMode = ""
	// ReviewSnapshotHead exposes only the captured HEAD commit.
	ReviewSnapshotHead ReviewSnapshotMode = "head"
	// ReviewSnapshotIndex exposes the captured HEAD commit plus the index.
	ReviewSnapshotIndex ReviewSnapshotMode = "index"
	// ReviewSnapshotTrackedWorktree exposes HEAD plus staged and unstaged tracked
	// changes visible to Git. Untracked files and paths hidden with Git index
	// assume-unchanged/skip-worktree flags are intentionally excluded.
	ReviewSnapshotTrackedWorktree ReviewSnapshotMode = "tracked-worktree"
	// ReviewSnapshotWorktree adds explicitly allowlisted, reviewable untracked
	// text files to the tracked worktree snapshot. Sensitive-looking paths,
	// binary files, symlinks, and untracked agent instructions remain excluded.
	ReviewSnapshotWorktree ReviewSnapshotMode = "worktree"
)

const MaxReviewSnapshotPatchBytes = 64 << 20

// ReviewSnapshotPolicy describes what CaptureReviewSnapshot must capture.
// ExpectedCommit is optional; PR review uses it to fail closed unless the local
// verification checkout is pinned to the immutable PR head.
type ReviewSnapshotPolicy struct {
	Mode           ReviewSnapshotMode
	ExpectedCommit string
	// UntrackedPaths is the explicit repository-relative allowlist required by
	// ReviewSnapshotWorktree. It is ignored by modes that exclude untracked data.
	UntrackedPaths []string
}

// ReviewSnapshot is an immutable, content-addressed verification descriptor.
// Accessors return values (and a defensive patch copy) so retries cannot alter
// the state later phases will materialize.
type ReviewSnapshot struct {
	mode            ReviewSnapshotMode
	repositoryRoot  string
	relativeWorkDir string
	commit          string
	patch           []byte
	untracked       []ReviewUntrackedFile
	id              string
}

// NewReviewSnapshot validates and freezes a captured descriptor. workDir may
// be the repository root or a directory below it.
func NewReviewSnapshot(mode ReviewSnapshotMode, repositoryRoot, workDir, commit string, patch []byte) (*ReviewSnapshot, error) {
	if !validReviewSnapshotMode(mode) || mode == ReviewSnapshotNone {
		return nil, fmt.Errorf("invalid review snapshot mode %q", mode)
	}
	root, err := filepath.Abs(strings.TrimSpace(repositoryRoot))
	if err != nil || strings.TrimSpace(repositoryRoot) == "" {
		return nil, fmt.Errorf("review snapshot repository root is required")
	}
	dir := strings.TrimSpace(workDir)
	if dir == "" {
		dir = root
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve review snapshot work directory: %w", err)
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("review snapshot work directory is outside repository root")
	}
	commit = strings.TrimSpace(commit)
	if commit == "" {
		return nil, fmt.Errorf("review snapshot commit is required")
	}
	if len(patch) > MaxReviewSnapshotPatchBytes {
		return nil, fmt.Errorf("review snapshot patch exceeds %d-byte limit", MaxReviewSnapshotPatchBytes)
	}
	if mode == ReviewSnapshotHead && len(patch) != 0 {
		return nil, fmt.Errorf("head-only review snapshot cannot contain a patch")
	}

	frozenPatch := append([]byte(nil), patch...)
	snapshot := &ReviewSnapshot{
		mode:            mode,
		repositoryRoot:  filepath.Clean(root),
		relativeWorkDir: filepath.Clean(rel),
		commit:          commit,
		patch:           frozenPatch,
	}
	snapshot.id = reviewSnapshotID(snapshot)
	return snapshot, nil
}

// CaptureReviewSnapshot captures one exact Git descriptor for a complete
// Framework.RunRLM execution. Passing an empty workDir uses the current working
// directory.
func CaptureReviewSnapshot(ctx context.Context, workDir string, policy ReviewSnapshotPolicy) (*ReviewSnapshot, error) {
	if policy.Mode == ReviewSnapshotNone {
		return nil, nil
	}
	if !validReviewSnapshotMode(policy.Mode) {
		return nil, fmt.Errorf("invalid review snapshot mode %q", policy.Mode)
	}
	if strings.TrimSpace(workDir) == "" {
		var err error
		workDir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve review working directory: %w", err)
		}
	}

	root, err := reviewSnapshotGitOutput(ctx, workDir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("resolve review repository: %w", err)
	}
	root = strings.TrimSpace(root)
	head, err := reviewSnapshotGitOutput(ctx, root, "rev-parse", "HEAD^{commit}")
	if err != nil {
		return nil, fmt.Errorf("resolve review HEAD: %w", err)
	}
	head = strings.TrimSpace(head)

	if expected := strings.TrimSpace(policy.ExpectedCommit); expected != "" {
		resolved, resolveErr := reviewSnapshotGitOutput(ctx, root, "rev-parse", expected+"^{commit}")
		if resolveErr != nil {
			return nil, fmt.Errorf("resolve expected review commit %s: %w", expected, resolveErr)
		}
		resolved = strings.TrimSpace(resolved)
		if head != resolved {
			return nil, fmt.Errorf("local review HEAD %s does not match expected commit %s", head, resolved)
		}
	}

	var patch []byte
	var capturedUntracked []ReviewUntrackedFile
	switch policy.Mode {
	case ReviewSnapshotHead:
		// The commit itself is the complete snapshot.
	case ReviewSnapshotIndex:
		patch, err = reviewSnapshotGitBytes(ctx, root,
			"diff", "--binary", "--full-index", "--no-ext-diff", "--no-textconv", "--cached", head, "--")
	case ReviewSnapshotTrackedWorktree:
		patch, err = reviewSnapshotGitBytes(ctx, root,
			"diff", "--binary", "--full-index", "--no-ext-diff", "--no-textconv", head, "--")
	case ReviewSnapshotWorktree:
		if len(policy.UntrackedPaths) == 0 {
			return nil, fmt.Errorf("worktree review snapshot requires explicitly allowlisted untracked paths")
		}
		patch, err = reviewSnapshotGitBytes(ctx, root,
			"diff", "--binary", "--full-index", "--no-ext-diff", "--no-textconv", head, "--")
		if err == nil {
			capturedUntracked, err = CaptureReviewUntrackedFiles(ctx, root, policy.UntrackedPaths)
			for _, file := range capturedUntracked {
				patch = append(patch, file.Patch...)
			}
			patch = canonicalReviewSnapshotPatch(patch)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("capture %s review snapshot: %w", policy.Mode, err)
	}
	if len(patch) > MaxReviewSnapshotPatchBytes {
		return nil, fmt.Errorf("review snapshot patch exceeds %d-byte limit", MaxReviewSnapshotPatchBytes)
	}

	// A branch switch during capture must not silently relabel the descriptor.
	currentHead, err := reviewSnapshotGitOutput(ctx, root, "rev-parse", "HEAD^{commit}")
	if err != nil || strings.TrimSpace(currentHead) != head {
		return nil, fmt.Errorf("review HEAD changed while snapshot was captured")
	}
	snapshot, err := NewReviewSnapshot(policy.Mode, root, workDir, head, patch)
	if err != nil {
		return nil, err
	}
	if policy.Mode == ReviewSnapshotWorktree {
		snapshot.untracked = cloneReviewUntrackedFiles(capturedUntracked)
	}
	return snapshot, nil
}

func validReviewSnapshotMode(mode ReviewSnapshotMode) bool {
	switch mode {
	case ReviewSnapshotNone, ReviewSnapshotHead, ReviewSnapshotIndex, ReviewSnapshotTrackedWorktree, ReviewSnapshotWorktree:
		return true
	default:
		return false
	}
}

// canonicalReviewSnapshotPatch makes independent tracked and untracked Git
// diff calls compare identically to the single path-sorted diff emitted after
// materialization. Each file segment remains byte-for-byte unchanged.
func canonicalReviewSnapshotPatch(patch []byte) []byte {
	files := diffsignal.Split(string(patch))
	if len(files) < 2 {
		return append([]byte(nil), patch...)
	}
	sort.SliceStable(files, func(i, j int) bool {
		return files[i].Segment < files[j].Segment
	})
	var result strings.Builder
	result.Grow(len(patch))
	for _, file := range files {
		result.WriteString(file.Segment)
	}
	return []byte(result.String())
}

func reviewSnapshotGitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	output, err := reviewSnapshotGitBytes(ctx, dir, args...)
	return string(output), err
}

func reviewSnapshotGitBytes(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-pager", "-C", dir}, args...)...)
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return output, nil
}

func reviewSnapshotID(snapshot *ReviewSnapshot) string {
	hash := sha256.New()
	for _, value := range []string{string(snapshot.mode), snapshot.commit, snapshot.relativeWorkDir} {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	_, _ = hash.Write(snapshot.patch)
	return hex.EncodeToString(hash.Sum(nil))
}

func (s *ReviewSnapshot) Mode() ReviewSnapshotMode {
	if s == nil {
		return ReviewSnapshotNone
	}
	return s.mode
}

func (s *ReviewSnapshot) RepositoryRoot() string {
	if s == nil {
		return ""
	}
	return s.repositoryRoot
}

func (s *ReviewSnapshot) RelativeWorkDir() string {
	if s == nil {
		return ""
	}
	return s.relativeWorkDir
}

func (s *ReviewSnapshot) Commit() string {
	if s == nil {
		return ""
	}
	return s.commit
}

func (s *ReviewSnapshot) Patch() []byte {
	if s == nil {
		return nil
	}
	return append([]byte(nil), s.patch...)
}

// UntrackedFiles returns the exact filtered untracked evidence frozen into a
// worktree snapshot. A non-nil empty slice distinguishes a captured worktree
// with no reviewable untracked files from snapshot modes that exclude them.
func (s *ReviewSnapshot) UntrackedFiles() []ReviewUntrackedFile {
	if s == nil || s.mode != ReviewSnapshotWorktree {
		return nil
	}
	return cloneReviewUntrackedFiles(s.untracked)
}

func (s *ReviewSnapshot) ID() string {
	if s == nil {
		return ""
	}
	return s.id
}
