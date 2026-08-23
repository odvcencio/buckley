package worktree

// This file contains the dormant exact-workspace implementation. Legacy
// linked-worktree behavior remains isolated in manager.go.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	maxBranchNameBytes    = 512
	maxGitOutputBytes     = 64 << 10
	maxGitExecutableBytes = 128 << 20
	secureGitTimeout      = 5 * time.Minute
	secureGitWaitDelay    = 2 * time.Second
)

// SecureWorktree is a one-use capability for an isolated checkout. Absolute
// paths and authority identifiers are deliberately private so JSON encoding
// cannot disclose or recreate cleanup authority.
type SecureWorktree struct {
	Branch string `json:"branch"`
	Commit string `json:"commit"`

	path               string
	repositoryRoot     string
	gitDir             string
	commonGitDir       string
	sourceGitDir       string
	sourceCommonGitDir string
	secureOwner        [32]byte
	secureID           [32]byte
}

// Path returns the canonical isolated checkout path. It is intentionally a
// method rather than a serializable field.
func (checkout *SecureWorktree) Path() string {
	if checkout == nil {
		return ""
	}
	return checkout.path
}

// SecureManager creates isolated, run-owned checkouts without sharing mutable
// Git administration or configuration with the source repository. It is the
// only manager suitable for pre-provider OSS admission. The activating broker
// must scope every model tool to SecureWorktree.Path() and must not expose its
// parent or quarantine roots. This package cannot make same-UID processes
// tamper-proof.
type SecureManager struct {
	sourcePath              string
	sourceRoot              string
	sourceRootIdentity      os.FileInfo
	sourceGitDir            string
	sourceGitIdentity       os.FileInfo
	sourceCommonGitDir      string
	sourceCommonGitIdentity os.FileInfo
	sourceObjectsDir        string
	sourceObjectsIdentity   os.FileInfo
	runParent               string
	runParentIdentity       os.FileInfo
	runRoot                 string
	runRootIdentity         os.FileInfo
	checkoutsRoot           string
	checkoutsIdentity       os.FileInfo
	quarantineRoot          string
	quarantineIdentity      os.FileInfo
	templateRoot            string
	templateIdentity        os.FileInfo
	gitExecutable           string
	gitExecutableIdentity   secureExecutableIdentity
	environment             []string
	owner                   [32]byte
	createTimeout           time.Duration
	cleanupTimeout          time.Duration
	runGit                  secureGitRunner
	beforeQuarantineRename  func(source, quarantine string)
	beforeRecursiveRemoval  func(path string)
	beforeRemovalEntry      func(path string)
	lifecycleGate           chan struct{}
	active                  map[[32]byte]secureCheckoutRecord
	residual                map[[32]byte]secureCheckoutRecord
	closeResidual           string
	closeResidualIdentity   os.FileInfo
	closed                  bool
}

type secureExecutableIdentity struct {
	fileInfo os.FileInfo
	size     int64
	mode     os.FileMode
	modTime  time.Time
	digest   [sha256.Size]byte
}

type secureCheckoutRecord struct {
	issuedDir         string
	containerDir      string
	containerIdentity os.FileInfo
	quarantined       bool
	path              string
	pathIdentity      os.FileInfo
	branch            string
	commit            string
	gitDir            string
	gitDirIdentity    os.FileInfo
	commonGitDir      string
	commonGitIdentity os.FileInfo
	objectsDir        string
	objectsIdentity   os.FileInfo
}

type secureRepositoryIdentity struct {
	root            string
	rootIdentity    os.FileInfo
	gitDir          string
	gitIdentity     os.FileInfo
	commonGitDir    string
	commonIdentity  os.FileInfo
	objectsDir      string
	objectsIdentity os.FileInfo
}

type secureGitRunner func(ctx context.Context, dir string, args ...string) ([]byte, error)

// NewSecureManager claims a previously nonexistent run root and binds it to
// the canonical source repository. The root must not overlap any registered
// worktree, including the source worktree itself.
func NewSecureManager(ctx context.Context, repoPath, runRoot string) (_ *SecureManager, returnErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	gitExecutable, gitExecutableIdentity, err := resolvePinnedExecutable(ctx, "git")
	if err != nil {
		return nil, fmt.Errorf("resolve trusted git executable: %w", err)
	}
	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" {
		return nil, fmt.Errorf("repository path is required")
	}
	requestedPath, err := canonicalExistingDirectory(repoPath)
	if err != nil {
		return nil, fmt.Errorf("resolve repository path: %w", err)
	}

	environment := secureGitEnvironment()
	runner := func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		if ctx == nil {
			ctx = context.Background()
		}
		invocationCtx, cancel := context.WithTimeout(ctx, secureGitTimeout)
		defer cancel()
		if err := validateFileIdentity(invocationCtx, gitExecutable, gitExecutableIdentity, "trusted git executable"); err != nil {
			return nil, err
		}
		return gitOutputWithExecutable(invocationCtx, environment, gitExecutable, dir, args...)
	}
	rootOutput, err := runner(ctx, requestedPath, "rev-parse", "--path-format=absolute", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("not a git repository: %s: %w", requestedPath, err)
	}
	sourceRoot, err := canonicalExistingDirectory(strings.TrimSpace(string(rootOutput)))
	if err != nil {
		return nil, fmt.Errorf("resolve source repository root: %w", err)
	}
	if !isSameOrDescendant(sourceRoot, requestedPath) {
		return nil, fmt.Errorf("requested path is not contained by the discovered repository root")
	}
	sourceGitDir, err := canonicalGitDirectoryWithRunner(ctx, runner, sourceRoot, "--git-dir")
	if err != nil {
		return nil, fmt.Errorf("resolve source git directory: %w", err)
	}
	sourceCommonGitDir, err := canonicalGitDirectoryWithRunner(ctx, runner, sourceRoot, "--git-common-dir")
	if err != nil {
		return nil, fmt.Errorf("resolve source common git directory: %w", err)
	}
	sourceObjectsDir, err := canonicalGitPathWithRunner(ctx, runner, sourceRoot, "objects")
	if err != nil {
		return nil, fmt.Errorf("resolve source object directory: %w", err)
	}
	if !isSameOrDescendant(sourceCommonGitDir, sourceGitDir) || !isStrictDescendant(sourceCommonGitDir, sourceObjectsDir) {
		return nil, fmt.Errorf("source git directory relationships are not canonical")
	}
	sourceRootIdentity, err := captureDirectoryIdentity(sourceRoot)
	if err != nil {
		return nil, fmt.Errorf("capture source root identity: %w", err)
	}
	sourceGitIdentity, err := captureDirectoryIdentity(sourceGitDir)
	if err != nil {
		return nil, fmt.Errorf("capture source git directory identity: %w", err)
	}
	sourceCommonGitIdentity, err := captureDirectoryIdentity(sourceCommonGitDir)
	if err != nil {
		return nil, fmt.Errorf("capture source common git directory identity: %w", err)
	}
	sourceObjectsIdentity, err := captureDirectoryIdentity(sourceObjectsDir)
	if err != nil {
		return nil, fmt.Errorf("capture source object directory identity: %w", err)
	}
	if err := rejectIncompleteOrExternalRepositoryPosture(ctx, runner, sourceRoot, sourceObjectsDir); err != nil {
		return nil, fmt.Errorf("reject source repository posture: %w", err)
	}

	runRoot = strings.TrimSpace(runRoot)
	if runRoot == "" {
		return nil, fmt.Errorf("explicit run-owned root is required")
	}
	runRoot = expandHomeDir(runRoot)
	if !filepath.IsAbs(runRoot) {
		return nil, fmt.Errorf("run-owned root must be absolute")
	}
	runRoot, err = canonicalPath(runRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve run-owned root: %w", err)
	}
	if _, err := os.Lstat(runRoot); err == nil {
		return nil, fmt.Errorf("run-owned root must not already exist: %s", runRoot)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect run-owned root: %w", err)
	}
	parent, err := canonicalExistingDirectory(filepath.Dir(runRoot))
	if err != nil {
		return nil, fmt.Errorf("resolve run-owned root parent: %w", err)
	}
	if !isSameOrDescendant(parent, filepath.Dir(runRoot)) || !isSameOrDescendant(filepath.Dir(runRoot), parent) {
		return nil, fmt.Errorf("run-owned root parent identity mismatch")
	}
	parentIdentity, err := captureDirectoryIdentity(parent)
	if err != nil {
		return nil, fmt.Errorf("capture run-owned root parent identity: %w", err)
	}
	for description, protectedPath := range map[string]string{
		"source root":                 sourceRoot,
		"source git directory":        sourceGitDir,
		"source common git directory": sourceCommonGitDir,
		"source object directory":     sourceObjectsDir,
	} {
		if pathsOverlap(runRoot, protectedPath) {
			return nil, fmt.Errorf("run-owned root overlaps %s: %s", description, protectedPath)
		}
	}

	worktreeOutput, err := runner(ctx, sourceRoot, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, fmt.Errorf("list source worktrees: %w", err)
	}
	for _, path := range parseNULWorktreePaths(worktreeOutput) {
		canonical, resolveErr := canonicalPath(path)
		if resolveErr != nil {
			return nil, fmt.Errorf("resolve registered worktree %q: %w", path, resolveErr)
		}
		if pathsOverlap(runRoot, canonical) {
			return nil, fmt.Errorf("run-owned root overlaps registered worktree: %s", canonical)
		}
	}

	owner, err := randomSecureID()
	if err != nil {
		return nil, fmt.Errorf("generate secure manager identity: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.Mkdir(runRoot, 0o700); err != nil {
		return nil, fmt.Errorf("claim run-owned root: %w", err)
	}
	keepRunRoot := false
	var runRootIdentity os.FileInfo
	defer func() {
		if !keepRunRoot {
			if cleanupErr := removeExactEmptyDirectory(runRoot, runRootIdentity, "failed run-owned root"); cleanupErr != nil {
				returnErr = errors.Join(returnErr, cleanupErr)
			}
		}
	}()
	runRootIdentity, err = captureDirectoryIdentity(runRoot)
	if err != nil {
		return nil, fmt.Errorf("capture run-owned root identity: %w", err)
	}

	templateRoot := filepath.Join(runRoot, "git-template")
	checkoutsRoot := filepath.Join(runRoot, "checkouts")
	quarantineRoot := filepath.Join(runRoot, "quarantine")
	if err := os.Mkdir(templateRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create empty git template: %w", err)
	}
	keepTemplate := false
	var templateIdentity os.FileInfo
	defer func() {
		if !keepTemplate {
			if cleanupErr := removeExactEmptyDirectory(templateRoot, templateIdentity, "failed git template"); cleanupErr != nil {
				returnErr = errors.Join(returnErr, cleanupErr)
			}
		}
	}()
	templateIdentity, err = captureDirectoryIdentity(templateRoot)
	if err != nil {
		return nil, fmt.Errorf("capture git template identity: %w", err)
	}
	if err := os.Mkdir(checkoutsRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create secure checkout root: %w", err)
	}
	keepCheckouts := false
	var checkoutsIdentity os.FileInfo
	defer func() {
		if !keepCheckouts {
			if cleanupErr := removeExactEmptyDirectory(checkoutsRoot, checkoutsIdentity, "failed secure checkout root"); cleanupErr != nil {
				returnErr = errors.Join(returnErr, cleanupErr)
			}
		}
	}()
	checkoutsIdentity, err = captureDirectoryIdentity(checkoutsRoot)
	if err != nil {
		return nil, fmt.Errorf("capture secure checkout root identity: %w", err)
	}
	if err := os.Mkdir(quarantineRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create secure quarantine root: %w", err)
	}
	keepQuarantine := false
	var quarantineIdentity os.FileInfo
	defer func() {
		if !keepQuarantine {
			if cleanupErr := removeExactEmptyDirectory(quarantineRoot, quarantineIdentity, "failed secure quarantine root"); cleanupErr != nil {
				returnErr = errors.Join(returnErr, cleanupErr)
			}
		}
	}()
	quarantineIdentity, err = captureDirectoryIdentity(quarantineRoot)
	if err != nil {
		return nil, fmt.Errorf("capture secure quarantine root identity: %w", err)
	}
	manager := &SecureManager{
		sourcePath:              requestedPath,
		sourceRoot:              sourceRoot,
		sourceRootIdentity:      sourceRootIdentity,
		sourceGitDir:            sourceGitDir,
		sourceGitIdentity:       sourceGitIdentity,
		sourceCommonGitDir:      sourceCommonGitDir,
		sourceCommonGitIdentity: sourceCommonGitIdentity,
		sourceObjectsDir:        sourceObjectsDir,
		sourceObjectsIdentity:   sourceObjectsIdentity,
		runParent:               parent,
		runParentIdentity:       parentIdentity,
		runRoot:                 runRoot,
		runRootIdentity:         runRootIdentity,
		checkoutsRoot:           checkoutsRoot,
		checkoutsIdentity:       checkoutsIdentity,
		quarantineRoot:          quarantineRoot,
		quarantineIdentity:      quarantineIdentity,
		templateRoot:            templateRoot,
		templateIdentity:        templateIdentity,
		gitExecutable:           gitExecutable,
		gitExecutableIdentity:   gitExecutableIdentity,
		environment:             append([]string(nil), environment...),
		owner:                   owner,
		createTimeout:           secureGitTimeout,
		cleanupTimeout:          secureGitTimeout,
		runGit:                  runner,
		lifecycleGate:           newLifecycleGate(),
		active:                  make(map[[32]byte]secureCheckoutRecord),
		residual:                make(map[[32]byte]secureCheckoutRecord),
	}
	if err := manager.validateManagerIdentities(ctx, true); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	keepQuarantine = true
	keepCheckouts = true
	keepTemplate = true
	keepRunRoot = true
	return manager, nil
}

// CreateAt creates a standalone local clone with isolated Git administration
// and configuration, then checks out one exact full commit. Source refs and
// source worktree files are never modified. Its deadline begins at call entry
// and covers lifecycle admission and all cancellable work. After a mutation,
// rollback performs only a fixed number of identity checks and a same-volume
// quarantine rename; recursive cleanup is deferred to explicit Close. Portable
// Go cannot interrupt an OS syscall already executing, and subprocess reaping
// may consume the configured WaitDelay after cancellation. This package does
// not claim protection from arbitrary same-UID mutation.
func (sm *SecureManager) CreateAt(ctx context.Context, branchName, commitOID string) (_ *SecureWorktree, returnErr error) {
	if sm == nil || sm.runGit == nil || strings.TrimSpace(sm.runRoot) == "" {
		return nil, fmt.Errorf("secure checkout manager is not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := sm.createTimeout
	if timeout <= 0 {
		timeout = secureGitTimeout
	}
	createCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := acquireLifecycleGate(createCtx, sm.lifecycleGate, "acquire secure checkout lifecycle"); err != nil {
		return nil, err
	}
	defer releaseLifecycleGate(sm.lifecycleGate)
	if sm.closed {
		return nil, fmt.Errorf("secure checkout manager is closed")
	}
	if err := checkContext(createCtx, "begin secure checkout"); err != nil {
		return nil, err
	}
	if err := sm.validateManagerIdentities(createCtx, true); err != nil {
		return nil, err
	}
	branch, err := validateBranchNameWithRunner(createCtx, sm.runGit, sm.sourceRoot, branchName)
	if err != nil {
		return nil, err
	}
	// Repository posture and all pinned identities are revalidated immediately
	// before the first command that can resolve or lazily fetch an object.
	if err := sm.validateManagerIdentities(createCtx, true); err != nil {
		return nil, err
	}
	commit, err := validateCommitOIDWithRunner(createCtx, sm.runGit, sm.sourceRoot, commitOID)
	if err != nil {
		return nil, err
	}
	if err := checkContext(createCtx, "validated exact source commit"); err != nil {
		return nil, err
	}
	checkoutID, err := randomSecureID()
	if err != nil {
		return nil, fmt.Errorf("generate checkout identity: %w", err)
	}
	containerDir, err := canonicalPath(filepath.Join(sm.checkoutsRoot, hex.EncodeToString(checkoutID[:])))
	if err != nil {
		return nil, fmt.Errorf("resolve checkout container: %w", err)
	}
	if !isStrictDescendant(sm.checkoutsRoot, containerDir) {
		return nil, fmt.Errorf("checkout container escapes run-owned root")
	}
	if filepath.Dir(containerDir) != sm.checkoutsRoot {
		return nil, fmt.Errorf("checkout container is not an immediate child of the checkout root")
	}
	if err := sm.validateManagerIdentities(createCtx, true); err != nil {
		return nil, err
	}
	if err := checkContext(createCtx, "reserve checkout container"); err != nil {
		return nil, err
	}
	if err := os.Mkdir(containerDir, 0o700); err != nil {
		return nil, fmt.Errorf("reserve checkout container: %w", err)
	}
	record := secureCheckoutRecord{issuedDir: containerDir, containerDir: containerDir}
	rollback := true
	defer func() {
		if !rollback {
			return
		}
		var cleanupErr error
		if record.containerIdentity == nil {
			cleanupErr = removeExactEmptyDirectory(containerDir, nil, "failed checkout container")
		} else {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), secureGitWaitDelay)
			defer cancel()
			var updated secureCheckoutRecord
			updated, cleanupErr = sm.quarantineContainerLocked(rollbackCtx, record)
			sm.residual[checkoutID] = updated
		}
		if cleanupErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("rollback secure checkout: %w", cleanupErr))
		}
	}()
	record.containerIdentity, err = captureDirectoryIdentity(containerDir)
	if err != nil {
		return nil, fmt.Errorf("capture checkout container identity: %w", err)
	}
	if err := checkContext(createCtx, "captured checkout container identity"); err != nil {
		return nil, err
	}

	target := filepath.Join(containerDir, "source")
	if err := sm.validateManagerIdentities(createCtx, true); err != nil {
		return nil, fmt.Errorf("validate immediately before clone: %w", err)
	}
	if err := validateDirectoryIdentity(containerDir, record.containerIdentity, "checkout container"); err != nil {
		return nil, err
	}
	if _, err := sm.runGit(
		createCtx,
		sm.runRoot,
		"clone",
		"--quiet",
		"--local",
		"--no-hardlinks",
		"--no-checkout",
		"--no-tags",
		"--no-recurse-submodules",
		"--template="+sm.templateRoot,
		"--",
		sm.sourceRoot,
		target,
	); err != nil {
		return nil, fmt.Errorf("create isolated local clone: %w", err)
	}
	if err := checkContext(createCtx, "completed isolated clone"); err != nil {
		return nil, err
	}
	if err := sm.validateManagerIdentities(createCtx, true); err != nil {
		return nil, fmt.Errorf("validate immediately after clone: %w", err)
	}
	if err := validateDirectoryIdentity(containerDir, record.containerIdentity, "checkout container"); err != nil {
		return nil, err
	}
	target, err = canonicalExistingDirectory(target)
	if err != nil {
		return nil, fmt.Errorf("resolve isolated checkout: %w", err)
	}
	if !isStrictDescendant(containerDir, target) || !isStrictDescendant(sm.runRoot, target) {
		return nil, fmt.Errorf("isolated checkout escaped run-owned root")
	}
	targetIdentity, err := sm.inspectIsolatedRepository(createCtx, target, containerDir, true)
	if err != nil {
		return nil, fmt.Errorf("inspect isolated repository before object resolution: %w", err)
	}
	if _, err := sm.runGit(createCtx, target, "remote", "remove", "origin"); err != nil {
		return nil, fmt.Errorf("remove local source transport from isolated checkout: %w", err)
	}
	if err := validateIsolatedRepositoryIdentity(createCtx, targetIdentity); err != nil {
		return nil, err
	}
	if err := validateIsolatedTargetConfig(createCtx, sm.runGit, target, sm.sourceRoot, false); err != nil {
		return nil, err
	}
	if _, err := validateCommitOIDWithRunner(createCtx, sm.runGit, target, commit); err != nil {
		return nil, fmt.Errorf("verify cloned exact commit: %w", err)
	}
	if _, err := sm.runGit(
		createCtx,
		target,
		"checkout",
		"--quiet",
		"--no-recurse-submodules",
		"--no-track",
		"-B",
		branch,
		commit,
	); err != nil {
		return nil, fmt.Errorf("checkout exact commit: %w", err)
	}
	if err := checkContext(createCtx, "checked out exact commit"); err != nil {
		return nil, err
	}
	if err := validateIsolatedRepositoryIdentity(createCtx, targetIdentity); err != nil {
		return nil, err
	}
	if err := validateIsolatedTargetConfig(createCtx, sm.runGit, target, sm.sourceRoot, false); err != nil {
		return nil, err
	}
	finalTargetIdentity, err := sm.inspectIsolatedRepository(createCtx, target, containerDir, false)
	if err != nil {
		return nil, fmt.Errorf("reinspect isolated repository after checkout: %w", err)
	}
	if err := requireSameRepositoryIdentity(targetIdentity, finalTargetIdentity); err != nil {
		return nil, err
	}
	targetIdentity = finalTargetIdentity

	worktree, inspectedRecord, err := sm.inspectCheckout(createCtx, targetIdentity, record, branch, commit, checkoutID)
	if err != nil {
		return nil, fmt.Errorf("verify isolated checkout: %w", err)
	}
	record = inspectedRecord
	if err := sm.validateManagerIdentities(createCtx, true); err != nil {
		return nil, fmt.Errorf("validate manager before checkout activation: %w", err)
	}
	if err := validateCheckoutRecord(createCtx, record); err != nil {
		return nil, fmt.Errorf("validate checkout before activation: %w", err)
	}
	if err := validateIsolatedTargetConfig(createCtx, sm.runGit, target, sm.sourceRoot, false); err != nil {
		return nil, err
	}
	if err := checkContext(createCtx, "activate secure checkout"); err != nil {
		return nil, err
	}
	if _, exists := sm.active[checkoutID]; exists {
		return nil, fmt.Errorf("checkout identity collision")
	}
	sm.active[checkoutID] = record
	rollback = false
	return worktree, nil
}

// Remove deletes exactly one run-owned checkout capability. Its finite total
// budget starts before lifecycle admission. Portable Go cannot preempt a
// filesystem syscall already executing, but cleanup observes the deadline
// between bounded directory batches and performs no background mutation.
func (sm *SecureManager) Remove(ctx context.Context, checkout *SecureWorktree) error {
	if sm == nil {
		return fmt.Errorf("secure checkout manager is required")
	}
	cleanupCtx, cancel := sm.newCleanupContext(ctx)
	defer cancel()
	return sm.removeWithinBudget(cleanupCtx, checkout)
}

// RemoveExact is the explicit exact-workspace spelling of Remove. It has an
// independent call-entry budget and the same one-use capability semantics.
func (sm *SecureManager) RemoveExact(ctx context.Context, checkout *SecureWorktree) error {
	if sm == nil {
		return fmt.Errorf("secure checkout manager is required")
	}
	cleanupCtx, cancel := sm.newCleanupContext(ctx)
	defer cancel()
	return sm.removeWithinBudget(cleanupCtx, checkout)
}

func (sm *SecureManager) removeWithinBudget(ctx context.Context, checkout *SecureWorktree) error {
	if err := checkContext(ctx, "begin secure checkout removal"); err != nil {
		return err
	}
	if checkout == nil {
		return fmt.Errorf("secure checkout is required")
	}
	if checkout.secureOwner != sm.owner || checkout.secureID == ([32]byte{}) {
		return fmt.Errorf("checkout capability was not issued by this manager")
	}
	if err := acquireLifecycleGate(ctx, sm.lifecycleGate, "acquire secure checkout lifecycle"); err != nil {
		return err
	}
	defer releaseLifecycleGate(sm.lifecycleGate)
	if sm.closed {
		return fmt.Errorf("secure checkout manager is closed")
	}
	record, ok := sm.active[checkout.secureID]
	if !ok {
		return fmt.Errorf("checkout capability is unknown or already consumed")
	}
	updated, err := sm.removeContainerLocked(ctx, record)
	if err != nil {
		sm.active[checkout.secureID] = updated
		return err
	}
	delete(sm.active, checkout.secureID)
	return nil
}

// Close has a finite default total budget starting at call entry.
func (sm *SecureManager) Close() error {
	if sm == nil {
		return fmt.Errorf("secure checkout manager is required")
	}
	ctx, cancel := sm.newCleanupContext(context.Background())
	defer cancel()
	return sm.closeWithinBudget(ctx)
}

// CloseContext applies a finite total budget even when ctx is nil or has no
// deadline. Cleanup is synchronous and leaves identity-bound quarantine state
// for an exact retry whenever the budget expires.
func (sm *SecureManager) CloseContext(ctx context.Context) error {
	if sm == nil {
		return fmt.Errorf("secure checkout manager is required")
	}
	cleanupCtx, cancel := sm.newCleanupContext(ctx)
	defer cancel()
	return sm.closeWithinBudget(cleanupCtx)
}

func (sm *SecureManager) newCleanupContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	timeout := sm.cleanupTimeout
	if timeout <= 0 {
		timeout = secureGitTimeout
	}
	return context.WithTimeout(parent, timeout)
}

func (sm *SecureManager) closeWithinBudget(ctx context.Context) error {
	if err := checkContext(ctx, "begin secure checkout close"); err != nil {
		return err
	}
	if err := acquireLifecycleGate(ctx, sm.lifecycleGate, "acquire secure checkout lifecycle"); err != nil {
		return err
	}
	defer releaseLifecycleGate(sm.lifecycleGate)
	if sm.closed {
		return nil
	}
	if sm.closeResidual != "" {
		return sm.finishCloseResidualLocked(ctx)
	}
	if len(sm.active) != 0 {
		return fmt.Errorf("secure checkout manager has %d active capabilities", len(sm.active))
	}
	if err := sm.validateLifecycleIdentities(ctx, false); err != nil {
		return err
	}
	for id, record := range sm.residual {
		if err := checkContext(ctx, "clean residual checkout evidence"); err != nil {
			return err
		}
		updated, err := sm.removeContainerLocked(ctx, record)
		if err != nil {
			sm.residual[id] = updated
			return fmt.Errorf("clean residual checkout evidence: %w", err)
		}
		delete(sm.residual, id)
	}
	for _, item := range []struct {
		description string
		path        string
	}{
		{"secure checkout root", sm.checkoutsRoot},
		{"secure quarantine root", sm.quarantineRoot},
		{"git template", sm.templateRoot},
	} {
		if err := requireEmptyDirectory(ctx, item.path, item.description); err != nil {
			return err
		}
	}
	if err := checkContext(ctx, "quarantine run-owned root for close"); err != nil {
		return err
	}
	quarantine, err := freshQuarantinePath(sm.runParent, "buckley-run")
	if err != nil {
		return err
	}
	remaining, quarantineErr := quarantineExactPath(sm.runRoot, sm.runRootIdentity, sm.runParent, sm.runParentIdentity, quarantine, sm.runParent, sm.runParentIdentity, nil, "run-owned root")
	if remaining {
		sm.closeResidual = quarantine
		sm.closeResidualIdentity = sm.runRootIdentity
	}
	if quarantineErr != nil {
		return quarantineErr
	}
	if !remaining {
		return fmt.Errorf("close quarantine did not retain the run-owned identity")
	}
	return sm.finishCloseResidualLocked(ctx)
}

func (sm *SecureManager) finishCloseResidualLocked(ctx context.Context) error {
	if sm.closeResidual == "" || sm.closeResidualIdentity == nil {
		return fmt.Errorf("close residual identity is unavailable")
	}
	if filepath.Dir(sm.closeResidual) != sm.runParent {
		return fmt.Errorf("close residual escaped the pinned run parent")
	}
	if err := validateDirectoryIdentity(sm.runParent, sm.runParentIdentity, "run-owned root parent"); err != nil {
		return err
	}
	if _, err := os.Lstat(sm.runRoot); err == nil {
		return fmt.Errorf("unowned replacement remains at the original run-owned root")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("verify original run-owned root absence: %w", err)
	}
	if sm.beforeRecursiveRemoval != nil {
		sm.beforeRecursiveRemoval(sm.closeResidual)
		if err := checkContext(ctx, "begin close residual removal"); err != nil {
			return err
		}
	}
	if err := removeQuarantinedTree(ctx, sm.closeResidual, sm.closeResidualIdentity, sm.beforeRemovalEntry, "run-owned root"); err != nil {
		return err
	}
	sm.closeResidual = ""
	sm.closeResidualIdentity = nil
	sm.closed = true
	return nil
}

func (sm *SecureManager) inspectCheckout(ctx context.Context, identity secureRepositoryIdentity, record secureCheckoutRecord, branch, commit string, checkoutID [32]byte) (*SecureWorktree, secureCheckoutRecord, error) {
	root := identity.root
	headOutput, err := sm.runGit(ctx, root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || strings.TrimSpace(string(headOutput)) != commit {
		return nil, secureCheckoutRecord{}, fmt.Errorf("checkout commit identity mismatch")
	}
	branchOutput, err := sm.runGit(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || strings.TrimSpace(string(branchOutput)) != branch {
		return nil, secureCheckoutRecord{}, fmt.Errorf("checkout branch identity mismatch")
	}
	statusOutput, err := sm.runGit(ctx, root, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return nil, secureCheckoutRecord{}, fmt.Errorf("inspect checkout status: %w", err)
	}
	if len(bytes.TrimSpace(statusOutput)) != 0 {
		return nil, secureCheckoutRecord{}, fmt.Errorf("exact checkout is unexpectedly dirty")
	}
	record.path = root
	record.pathIdentity = identity.rootIdentity
	record.branch = branch
	record.commit = commit
	record.gitDir = identity.gitDir
	record.gitDirIdentity = identity.gitIdentity
	record.commonGitDir = identity.commonGitDir
	record.commonGitIdentity = identity.commonIdentity
	record.objectsDir = identity.objectsDir
	record.objectsIdentity = identity.objectsIdentity
	return &SecureWorktree{
		Branch:             branch,
		Commit:             commit,
		path:               root,
		repositoryRoot:     sm.sourceRoot,
		gitDir:             identity.gitDir,
		commonGitDir:       identity.commonGitDir,
		sourceGitDir:       sm.sourceGitDir,
		sourceCommonGitDir: sm.sourceCommonGitDir,
		secureOwner:        sm.owner,
		secureID:           checkoutID,
	}, record, nil
}

func (sm *SecureManager) quarantineContainerLocked(ctx context.Context, record secureCheckoutRecord) (secureCheckoutRecord, error) {
	if err := checkContext(ctx, "begin checkout quarantine"); err != nil {
		return record, err
	}
	if err := sm.validateLifecycleIdentities(ctx, false); err != nil {
		return record, err
	}
	if record.quarantined {
		return record, nil
	}
	clean := filepath.Clean(record.containerDir)
	if clean != record.issuedDir || !isStrictDescendant(sm.checkoutsRoot, clean) || filepath.Dir(clean) != sm.checkoutsRoot {
		return record, fmt.Errorf("refusing quarantine outside issued checkout root: %s", clean)
	}
	quarantine, err := freshQuarantinePath(sm.quarantineRoot, "checkout")
	if err != nil {
		return record, err
	}
	if err := checkContext(ctx, "rename checkout into quarantine"); err != nil {
		return record, err
	}
	remaining, quarantineErr := quarantineExactPath(clean, record.containerIdentity, sm.checkoutsRoot, sm.checkoutsIdentity, quarantine, sm.quarantineRoot, sm.quarantineIdentity, sm.beforeQuarantineRename, "secure checkout container")
	if remaining {
		record.containerDir = quarantine
		record.quarantined = true
	}
	if quarantineErr != nil {
		return record, quarantineErr
	}
	if !remaining {
		return record, fmt.Errorf("secure checkout quarantine did not retain the issued identity")
	}
	if _, err := os.Lstat(record.issuedDir); err == nil {
		return record, fmt.Errorf("unowned replacement remains at issued checkout path: %s", record.issuedDir)
	} else if !os.IsNotExist(err) {
		return record, fmt.Errorf("verify issued checkout path absence: %w", err)
	}
	return record, checkContext(ctx, "completed checkout quarantine")
}

func (sm *SecureManager) removeContainerLocked(ctx context.Context, record secureCheckoutRecord) (secureCheckoutRecord, error) {
	if err := checkContext(ctx, "begin quarantined checkout removal"); err != nil {
		return record, err
	}
	if err := sm.validateLifecycleIdentities(ctx, false); err != nil {
		return record, err
	}
	if !record.quarantined {
		updated, err := sm.quarantineContainerLocked(ctx, record)
		record = updated
		if err != nil {
			return record, err
		}
	}
	if !isStrictDescendant(sm.quarantineRoot, record.containerDir) || filepath.Dir(record.containerDir) != sm.quarantineRoot {
		return record, fmt.Errorf("quarantined checkout path escaped quarantine root")
	}
	if _, err := os.Lstat(record.issuedDir); err == nil {
		return record, fmt.Errorf("unowned replacement remains at issued checkout path: %s", record.issuedDir)
	} else if !os.IsNotExist(err) {
		return record, fmt.Errorf("verify issued checkout path absence: %w", err)
	}
	if sm.beforeRecursiveRemoval != nil {
		sm.beforeRecursiveRemoval(record.containerDir)
		if err := checkContext(ctx, "begin recursive checkout removal"); err != nil {
			return record, err
		}
	}
	if err := removeQuarantinedTree(ctx, record.containerDir, record.containerIdentity, sm.beforeRemovalEntry, "secure checkout container"); err != nil {
		return record, err
	}
	return record, nil
}

func (sm *SecureManager) inspectIsolatedRepository(ctx context.Context, root, containerDir string, allowOrigin bool) (secureRepositoryIdentity, error) {
	rootOutput, err := sm.runGit(ctx, root, "rev-parse", "--path-format=absolute", "--show-toplevel")
	if err != nil {
		return secureRepositoryIdentity{}, err
	}
	canonicalRoot, err := canonicalExistingDirectory(strings.TrimSpace(string(rootOutput)))
	if err != nil {
		return secureRepositoryIdentity{}, err
	}
	if canonicalRoot != root || !isStrictDescendant(containerDir, canonicalRoot) {
		return secureRepositoryIdentity{}, fmt.Errorf("isolated repository root identity mismatch")
	}
	gitDir, err := canonicalGitDirectoryWithRunner(ctx, sm.runGit, canonicalRoot, "--git-dir")
	if err != nil {
		return secureRepositoryIdentity{}, err
	}
	commonGitDir, err := canonicalGitDirectoryWithRunner(ctx, sm.runGit, canonicalRoot, "--git-common-dir")
	if err != nil {
		return secureRepositoryIdentity{}, err
	}
	objectsDir, err := canonicalGitPathWithRunner(ctx, sm.runGit, canonicalRoot, "objects")
	if err != nil {
		return secureRepositoryIdentity{}, err
	}
	if gitDir != commonGitDir || !isStrictDescendant(containerDir, gitDir) || !isStrictDescendant(commonGitDir, objectsDir) {
		return secureRepositoryIdentity{}, fmt.Errorf("isolated git administration is not self-contained")
	}
	if err := validateIsolatedTargetConfig(ctx, sm.runGit, canonicalRoot, sm.sourceRoot, allowOrigin); err != nil {
		return secureRepositoryIdentity{}, err
	}
	if err := rejectObjectStorePosture(ctx, objectsDir); err != nil {
		return secureRepositoryIdentity{}, err
	}
	identity := secureRepositoryIdentity{root: canonicalRoot, gitDir: gitDir, commonGitDir: commonGitDir, objectsDir: objectsDir}
	if identity.rootIdentity, err = captureDirectoryIdentity(canonicalRoot); err != nil {
		return secureRepositoryIdentity{}, err
	}
	if identity.gitIdentity, err = captureDirectoryIdentity(gitDir); err != nil {
		return secureRepositoryIdentity{}, err
	}
	if identity.commonIdentity, err = captureDirectoryIdentity(commonGitDir); err != nil {
		return secureRepositoryIdentity{}, err
	}
	if identity.objectsIdentity, err = captureDirectoryIdentity(objectsDir); err != nil {
		return secureRepositoryIdentity{}, err
	}
	return identity, nil
}

func validateIsolatedRepositoryIdentity(ctx context.Context, identity secureRepositoryIdentity) error {
	for _, item := range []struct {
		path        string
		identity    os.FileInfo
		description string
	}{
		{identity.root, identity.rootIdentity, "isolated repository root"},
		{identity.gitDir, identity.gitIdentity, "isolated git directory"},
		{identity.commonGitDir, identity.commonIdentity, "isolated common git directory"},
		{identity.objectsDir, identity.objectsIdentity, "isolated object directory"},
	} {
		if err := validateDirectoryIdentity(item.path, item.identity, item.description); err != nil {
			return err
		}
	}
	return rejectObjectStorePosture(ctx, identity.objectsDir)
}

func requireSameRepositoryIdentity(first, second secureRepositoryIdentity) error {
	for _, item := range []struct {
		firstPath  string
		secondPath string
		firstInfo  os.FileInfo
		secondInfo os.FileInfo
	}{
		{first.root, second.root, first.rootIdentity, second.rootIdentity},
		{first.gitDir, second.gitDir, first.gitIdentity, second.gitIdentity},
		{first.commonGitDir, second.commonGitDir, first.commonIdentity, second.commonIdentity},
		{first.objectsDir, second.objectsDir, first.objectsIdentity, second.objectsIdentity},
	} {
		if item.firstPath != item.secondPath || item.firstInfo == nil || item.secondInfo == nil || !os.SameFile(item.firstInfo, item.secondInfo) {
			return fmt.Errorf("isolated repository identity changed during checkout")
		}
	}
	return nil
}

func validateCheckoutRecord(ctx context.Context, record secureCheckoutRecord) error {
	if err := validateDirectoryIdentity(record.issuedDir, record.containerIdentity, "checkout container"); err != nil {
		return err
	}
	return validateIsolatedRepositoryIdentity(ctx, secureRepositoryIdentity{
		root: record.path, rootIdentity: record.pathIdentity,
		gitDir: record.gitDir, gitIdentity: record.gitDirIdentity,
		commonGitDir: record.commonGitDir, commonIdentity: record.commonGitIdentity,
		objectsDir: record.objectsDir, objectsIdentity: record.objectsIdentity,
	})
}

func (sm *SecureManager) validateManagerIdentities(ctx context.Context, requireQuarantineEmpty bool) error {
	if err := checkContext(ctx, "validate secure manager identities"); err != nil {
		return err
	}
	if err := validateFileIdentity(ctx, sm.gitExecutable, sm.gitExecutableIdentity, "trusted git executable"); err != nil {
		return err
	}
	for _, item := range []struct {
		path        string
		identity    os.FileInfo
		description string
	}{
		{sm.sourceRoot, sm.sourceRootIdentity, "source root"},
		{sm.sourceGitDir, sm.sourceGitIdentity, "source git directory"},
		{sm.sourceCommonGitDir, sm.sourceCommonGitIdentity, "source common git directory"},
		{sm.sourceObjectsDir, sm.sourceObjectsIdentity, "source object directory"},
	} {
		if err := checkContext(ctx, "validate source directory identities"); err != nil {
			return err
		}
		if err := validateDirectoryIdentity(item.path, item.identity, item.description); err != nil {
			return err
		}
	}
	if err := sm.validateLifecycleIdentities(ctx, requireQuarantineEmpty); err != nil {
		return err
	}
	if err := rejectIncompleteOrExternalRepositoryPosture(ctx, sm.runGit, sm.sourceRoot, sm.sourceObjectsDir); err != nil {
		return fmt.Errorf("reject source repository posture: %w", err)
	}
	worktrees, err := sm.runGit(ctx, sm.sourceRoot, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return fmt.Errorf("list source worktrees: %w", err)
	}
	for _, path := range parseNULWorktreePaths(worktrees) {
		if err := checkContext(ctx, "validate registered worktree identities"); err != nil {
			return err
		}
		canonical, err := canonicalPath(path)
		if err != nil {
			return err
		}
		if pathsOverlap(sm.runRoot, canonical) {
			return fmt.Errorf("run-owned root overlaps registered worktree: %s", canonical)
		}
	}
	return checkContext(ctx, "validated secure manager identities")
}

func (sm *SecureManager) validateLifecycleIdentities(ctx context.Context, requireQuarantineEmpty bool) error {
	for _, item := range []struct {
		path        string
		identity    os.FileInfo
		description string
	}{
		{sm.runParent, sm.runParentIdentity, "run-owned root parent"},
		{sm.runRoot, sm.runRootIdentity, "run-owned root"},
		{sm.checkoutsRoot, sm.checkoutsIdentity, "secure checkout root"},
		{sm.quarantineRoot, sm.quarantineIdentity, "secure quarantine root"},
		{sm.templateRoot, sm.templateIdentity, "git template"},
	} {
		if err := checkContext(ctx, "validate secure lifecycle identities"); err != nil {
			return err
		}
		if err := validateDirectoryIdentity(item.path, item.identity, item.description); err != nil {
			return err
		}
	}
	if filepath.Dir(sm.runRoot) != sm.runParent || filepath.Dir(sm.checkoutsRoot) != sm.runRoot || filepath.Dir(sm.quarantineRoot) != sm.runRoot || filepath.Dir(sm.templateRoot) != sm.runRoot {
		return fmt.Errorf("secure lifecycle directory relationship changed")
	}
	if err := requireEmptyDirectory(ctx, sm.templateRoot, "git template"); err != nil {
		return err
	}
	if requireQuarantineEmpty {
		if err := requireEmptyDirectory(ctx, sm.quarantineRoot, "secure quarantine root"); err != nil {
			return err
		}
	}
	return nil
}

func captureDirectoryIdentity(path string) (os.FileInfo, error) {
	clean := filepath.Clean(path)
	info, err := os.Lstat(clean)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("path is not an owned directory: %s", clean)
	}
	canonical, err := canonicalExistingDirectory(clean)
	if err != nil {
		return nil, err
	}
	if canonical != clean {
		return nil, fmt.Errorf("directory canonical identity mismatch: %s", clean)
	}
	return info, nil
}

func validateDirectoryIdentity(path string, expected os.FileInfo, description string) error {
	if expected == nil {
		return fmt.Errorf("%s identity is unavailable", description)
	}
	clean := filepath.Clean(path)
	canonical, err := canonicalExistingDirectory(clean)
	if os.IsNotExist(err) {
		return fmt.Errorf("%s is missing; cleanup identity cannot be proven", description)
	}
	if err != nil {
		return fmt.Errorf("validate %s identity: %w", description, err)
	}
	if canonical != clean {
		return fmt.Errorf("%s canonical identity changed", description)
	}
	// Take the filesystem identity snapshot after canonical resolution so this
	// is the final filesystem observation before SameFile and deletion.
	actual, err := os.Lstat(clean)
	if os.IsNotExist(err) {
		return fmt.Errorf("%s is missing; cleanup identity cannot be proven", description)
	}
	if err != nil {
		return fmt.Errorf("validate %s identity: %w", description, err)
	}
	if actual.Mode()&os.ModeSymlink != 0 || !actual.IsDir() {
		return fmt.Errorf("%s is not an owned directory", description)
	}
	if !os.SameFile(actual, expected) {
		return fmt.Errorf("%s identity changed", description)
	}
	return nil
}

func removeExactEmptyDirectory(path string, expected os.FileInfo, description string) error {
	clean := filepath.Clean(path)
	if _, err := os.Lstat(clean); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect %s: %w", description, err)
	}
	if expected != nil {
		if err := validateDirectoryIdentity(clean, expected, description); err != nil {
			return err
		}
	} else {
		// This path is used only by a defer installed immediately after a
		// successful Mkdir when identity capture itself failed. It never recurses.
		if _, err := captureDirectoryIdentity(clean); err != nil {
			return fmt.Errorf("validate fresh %s: %w", description, err)
		}
	}
	// os.Remove is the atomic emptiness check for a directory. Avoid a preceding
	// ReadDir traversal so failure cleanup remains a fixed number of syscalls.
	if err := os.Remove(clean); err != nil {
		return fmt.Errorf("remove empty %s: %w", description, err)
	}
	if _, err := os.Lstat(clean); err == nil {
		return fmt.Errorf("%s remains after removal", description)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("verify %s absence: %w", description, err)
	}
	return nil
}

func requireEmptyDirectory(ctx context.Context, path, description string) error {
	if err := checkContext(ctx, "inspect "+description); err != nil {
		return err
	}
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", description, err)
	}
	entries, readErr := directory.ReadDir(1)
	closeErr := directory.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return fmt.Errorf("read %s: %w", description, readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close %s: %w", description, closeErr)
	}
	if len(entries) != 0 {
		return fmt.Errorf("%s is not empty", description)
	}
	return checkContext(ctx, "inspected "+description)
}

func checkContext(ctx context.Context, stage string) error {
	if ctx == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%s: %w", stage, err)
	}
	return nil
}

func newLifecycleGate() chan struct{} {
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	return gate
}

func acquireLifecycleGate(ctx context.Context, gate chan struct{}, stage string) error {
	if gate == nil {
		return fmt.Errorf("%s: lifecycle gate is unavailable", stage)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-gate:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%s: %w", stage, ctx.Err())
	}
}

func releaseLifecycleGate(gate chan struct{}) {
	if gate == nil {
		return
	}
	select {
	case gate <- struct{}{}:
	default:
		panic("worktree: lifecycle gate released without acquisition")
	}
}

func freshQuarantinePath(parent, prefix string) (string, error) {
	id, err := randomSecureID()
	if err != nil {
		return "", fmt.Errorf("generate quarantine identity: %w", err)
	}
	path, err := canonicalPath(filepath.Join(parent, prefix+"-"+hex.EncodeToString(id[:])))
	if err != nil {
		return "", fmt.Errorf("resolve quarantine path: %w", err)
	}
	if filepath.Dir(path) != parent {
		return "", fmt.Errorf("quarantine path escaped its parent")
	}
	if _, err := os.Lstat(path); err == nil {
		return "", fmt.Errorf("quarantine path already exists: %s", path)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect quarantine path: %w", err)
	}
	return path, nil
}

// quarantineExactPath transfers authority with a bounded sequence of identity
// checks and one same-volume rename. It never walks or recursively removes the
// moved tree. A true result means the expected identity remains at quarantine.
func quarantineExactPath(source string, expected os.FileInfo, sourceParent string, sourceParentIdentity os.FileInfo, quarantine, quarantineParent string, quarantineParentIdentity os.FileInfo, beforeRename func(string, string), description string) (bool, error) {
	if filepath.Dir(source) != sourceParent || filepath.Dir(quarantine) != quarantineParent {
		return false, fmt.Errorf("%s quarantine parent relationship changed", description)
	}
	if err := validateDirectoryIdentity(sourceParent, sourceParentIdentity, description+" parent"); err != nil {
		return false, err
	}
	if err := validateDirectoryIdentity(quarantineParent, quarantineParentIdentity, description+" quarantine parent"); err != nil {
		return false, err
	}
	if err := validateDirectoryIdentity(source, expected, description); err != nil {
		return false, err
	}
	if filepath.Dir(source) == source || filepath.Dir(quarantine) == quarantine {
		return false, fmt.Errorf("refusing to quarantine a filesystem root")
	}
	if filepath.VolumeName(source) != filepath.VolumeName(quarantine) {
		return false, fmt.Errorf("quarantine must be on the same filesystem volume")
	}
	if beforeRename != nil {
		beforeRename(source, quarantine)
	}
	if err := validateDirectoryIdentity(sourceParent, sourceParentIdentity, description+" parent"); err != nil {
		return false, err
	}
	if err := validateDirectoryIdentity(quarantineParent, quarantineParentIdentity, description+" quarantine parent"); err != nil {
		return false, err
	}
	if _, err := os.Lstat(quarantine); err == nil {
		return false, fmt.Errorf("quarantine destination appeared before rename: %s", quarantine)
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("inspect quarantine destination: %w", err)
	}
	if err := os.Rename(source, quarantine); err != nil {
		return false, fmt.Errorf("quarantine %s: %w", description, err)
	}
	moved, err := captureDirectoryIdentity(quarantine)
	if err != nil {
		restoreErr := restoreQuarantinePath(quarantine, source, sourceParent, sourceParentIdentity, quarantineParent, quarantineParentIdentity)
		return restoreErr != nil, errors.Join(fmt.Errorf("capture quarantined %s identity: %w", description, err), restoreErr)
	}
	if !os.SameFile(moved, expected) {
		restoreErr := restoreQuarantinePath(quarantine, source, sourceParent, sourceParentIdentity, quarantineParent, quarantineParentIdentity)
		return restoreErr != nil, errors.Join(fmt.Errorf("quarantined %s identity changed", description), restoreErr)
	}
	if _, err := os.Lstat(source); err == nil {
		return true, fmt.Errorf("replacement appeared at original %s path", description)
	} else if !os.IsNotExist(err) {
		return true, fmt.Errorf("verify original %s path absence: %w", description, err)
	}
	return true, nil
}

func restoreQuarantinePath(quarantine, source, sourceParent string, sourceParentIdentity os.FileInfo, quarantineParent string, quarantineParentIdentity os.FileInfo) error {
	if filepath.Dir(source) != sourceParent || filepath.Dir(quarantine) != quarantineParent {
		return fmt.Errorf("quarantine restore parent relationship changed")
	}
	if err := validateDirectoryIdentity(sourceParent, sourceParentIdentity, "quarantine restore source parent"); err != nil {
		return err
	}
	if err := validateDirectoryIdentity(quarantineParent, quarantineParentIdentity, "quarantine restore parent"); err != nil {
		return err
	}
	if _, err := os.Lstat(source); err == nil {
		return fmt.Errorf("cannot restore quarantine because original path is occupied: %s", source)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect quarantine restore path: %w", err)
	}
	if err := os.Rename(quarantine, source); err != nil {
		return fmt.Errorf("restore quarantine evidence: %w", err)
	}
	if _, err := os.Lstat(quarantine); err == nil {
		return fmt.Errorf("quarantine evidence remains after restore")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("verify quarantine restore: %w", err)
	}
	return nil
}

type secureRemovalFrame struct {
	path     string
	identity os.FileInfo
}

// removeQuarantinedTree removes only beneath an identity-bound quarantine
// root. It reads a bounded directory batch, checks the deadline between every
// entry and syscall, and returns synchronously with the remaining tree intact
// on cancellation. A syscall already in progress cannot be preempted.
func removeQuarantinedTree(ctx context.Context, root string, expected os.FileInfo, beforeEntry func(string), description string) error {
	if expected == nil {
		return fmt.Errorf("%s identity is unavailable", description)
	}
	if err := validateDirectoryIdentity(root, expected, "quarantined "+description); err != nil {
		return err
	}
	stack := []secureRemovalFrame{{path: root, identity: expected}}
	for len(stack) != 0 {
		if err := checkContext(ctx, "remove quarantined "+description); err != nil {
			return err
		}
		frame := stack[len(stack)-1]
		if beforeEntry != nil {
			beforeEntry(frame.path)
			if err := checkContext(ctx, "remove quarantined "+description); err != nil {
				return err
			}
		}
		if err := validateDirectoryIdentity(frame.path, frame.identity, "quarantined "+description); err != nil {
			return err
		}
		directory, err := os.Open(frame.path)
		if err != nil {
			return fmt.Errorf("open quarantined %s: %w", description, err)
		}
		entries, readErr := directory.ReadDir(64)
		closeErr := directory.Close()
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return fmt.Errorf("read quarantined %s: %w", description, readErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close quarantined %s: %w", description, closeErr)
		}
		descended := false
		for _, entry := range entries {
			if err := checkContext(ctx, "remove quarantined "+description+" entry"); err != nil {
				return err
			}
			child := filepath.Join(frame.path, entry.Name())
			if filepath.Dir(child) != frame.path || !isStrictDescendant(root, child) {
				return fmt.Errorf("quarantined %s entry escaped its root", description)
			}
			if beforeEntry != nil {
				beforeEntry(child)
				if err := checkContext(ctx, "remove quarantined "+description+" entry"); err != nil {
					return err
				}
			}
			info, err := os.Lstat(child)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return fmt.Errorf("inspect quarantined %s entry: %w", description, err)
			}
			mode := info.Mode()
			switch {
			case mode&os.ModeSymlink != 0 || mode.IsRegular():
				if err := os.Remove(child); err != nil && !os.IsNotExist(err) {
					return fmt.Errorf("remove quarantined %s entry: %w", description, err)
				}
			case mode.IsDir():
				stack = append(stack, secureRemovalFrame{path: child, identity: info})
				descended = true
			default:
				return fmt.Errorf("refusing special filesystem entry in quarantined %s: %s", description, child)
			}
			if descended {
				break
			}
		}
		if descended || len(entries) != 0 {
			continue
		}
		if err := checkContext(ctx, "remove quarantined "+description+" directory"); err != nil {
			return err
		}
		if err := validateDirectoryIdentity(frame.path, frame.identity, "quarantined "+description); err != nil {
			return err
		}
		if err := os.Remove(frame.path); err != nil {
			return fmt.Errorf("remove quarantined %s directory: %w", description, err)
		}
		if _, err := os.Lstat(frame.path); err == nil {
			return fmt.Errorf("quarantined %s remains after removal", description)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("verify quarantined %s absence: %w", description, err)
		}
		stack = stack[:len(stack)-1]
	}
	return nil
}

func validateBranchNameWithRunner(ctx context.Context, runner secureGitRunner, root, branchName string) (string, error) {
	branch := strings.TrimSpace(branchName)
	if branch == "" || branch != branchName || len(branch) > maxBranchNameBytes || strings.HasPrefix(branch, "-") {
		return "", fmt.Errorf("invalid branch name: %q", branchName)
	}
	output, err := runner(ctx, root, "check-ref-format", "--branch", branch)
	if err != nil || strings.TrimSpace(string(output)) != branch {
		return "", fmt.Errorf("invalid branch name: %q", branchName)
	}
	return branch, nil
}

func validateCommitOIDWithRunner(ctx context.Context, runner secureGitRunner, root, commitOID string) (string, error) {
	if commitOID == "" || commitOID != strings.TrimSpace(commitOID) || strings.ToLower(commitOID) != commitOID {
		return "", fmt.Errorf("commit must be an exact full lowercase object ID")
	}
	formatOutput, err := runner(ctx, root, "rev-parse", "--show-object-format")
	if err != nil {
		return "", fmt.Errorf("resolve repository object format: %w", err)
	}
	wantLength := 0
	switch strings.TrimSpace(string(formatOutput)) {
	case "sha1":
		wantLength = 40
	case "sha256":
		wantLength = 64
	default:
		return "", fmt.Errorf("unsupported repository object format")
	}
	if len(commitOID) != wantLength {
		return "", fmt.Errorf("commit must be an exact full %d-character object ID", wantLength)
	}
	if _, err := hex.DecodeString(commitOID); err != nil {
		return "", fmt.Errorf("commit must be an exact full hexadecimal object ID")
	}
	typeOutput, err := runner(ctx, root, "cat-file", "-t", commitOID)
	if err != nil {
		return "", fmt.Errorf("resolve exact commit object: %w", err)
	}
	if strings.TrimSpace(string(typeOutput)) != "commit" {
		return "", fmt.Errorf("object %s is not a commit", commitOID)
	}
	resolvedOutput, err := runner(ctx, root, "rev-parse", "--verify", commitOID+"^{commit}")
	if err != nil || strings.TrimSpace(string(resolvedOutput)) != commitOID {
		return "", fmt.Errorf("commit object identity did not resolve exactly")
	}
	return commitOID, nil
}

func localConfigKeys(ctx context.Context, runner secureGitRunner, root string) ([]string, error) {
	output, err := runner(ctx, root, "config", "--local", "--no-includes", "--null", "--name-only", "--list")
	if err != nil {
		return nil, fmt.Errorf("inspect local git config: %w", err)
	}
	keys := make([]string, 0)
	for _, rawKey := range bytes.Split(output, []byte{0}) {
		key := strings.ToLower(strings.TrimSpace(string(rawKey)))
		if key != "" {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

func rejectIncompleteOrExternalRepositoryPosture(ctx context.Context, runner secureGitRunner, root, objectsDir string) error {
	keys, err := localConfigKeys(ctx, runner, root)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if key == "include.path" ||
			(strings.HasPrefix(key, "includeif.") && strings.HasSuffix(key, ".path")) ||
			key == "extensions.worktreeconfig" ||
			key == "extensions.partialclone" ||
			(strings.HasPrefix(key, "remote.") && (strings.HasSuffix(key, ".promisor") || strings.HasSuffix(key, ".partialclonefilter") || strings.HasSuffix(key, ".vcs") || strings.HasSuffix(key, ".uploadpack") || strings.HasSuffix(key, ".receivepack"))) ||
			key == "core.sshcommand" || key == "core.alternaterefscommand" || key == "credential.helper" ||
			(strings.HasPrefix(key, "credential.") && strings.HasSuffix(key, ".helper")) {
			return fmt.Errorf("repository config permits incomplete objects, external config, or an external helper: %s", key)
		}
	}
	return rejectObjectStorePosture(ctx, objectsDir)
}

func validateIsolatedTargetConfig(ctx context.Context, runner secureGitRunner, root, sourceRoot string, allowOrigin bool) error {
	keys, err := localConfigKeys(ctx, runner, root)
	if err != nil {
		return err
	}
	allowed := map[string]struct{}{
		"core.repositoryformatversion": {},
		"core.filemode":                {},
		"core.bare":                    {},
		"core.logallrefupdates":        {},
		"core.ignorecase":              {},
		"core.precomposeunicode":       {},
		"core.symlinks":                {},
		"extensions.objectformat":      {},
		"extensions.refstorage":        {},
	}
	if allowOrigin {
		allowed["remote.origin.url"] = struct{}{}
		allowed["remote.origin.fetch"] = struct{}{}
		allowed["remote.origin.tagopt"] = struct{}{}
	}
	for _, key := range keys {
		_, ok := allowed[key]
		if !ok && allowOrigin && strings.HasPrefix(key, "branch.") && (strings.HasSuffix(key, ".remote") || strings.HasSuffix(key, ".merge")) {
			ok = true
		}
		if !ok {
			return fmt.Errorf("isolated repository config is outside the safe allowlist: %s", key)
		}
	}
	if allowOrigin {
		originOutput, err := runner(ctx, root, "config", "--local", "--no-includes", "--get", "remote.origin.url")
		if err != nil {
			return fmt.Errorf("inspect isolated origin: %w", err)
		}
		origin, err := canonicalExistingDirectory(strings.TrimSpace(string(originOutput)))
		if err != nil || origin != sourceRoot {
			return fmt.Errorf("isolated origin is not the pinned local source")
		}
	}
	return nil
}

func rejectObjectStorePosture(ctx context.Context, objectsDir string) error {
	if err := inspectObjectStoreDirectory(ctx, objectsDir, objectsDir); err != nil {
		return fmt.Errorf("inspect git object store posture: %w", err)
	}
	for _, name := range []string{"alternates", "http-alternates"} {
		if err := checkContext(ctx, "inspect git object store alternates"); err != nil {
			return err
		}
		path := filepath.Join(objectsDir, "info", name)
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("git object store contains external alternates: %s", name)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect git object store: %w", err)
		}
	}
	return nil
}

func inspectObjectStoreDirectory(ctx context.Context, root, directoryPath string) error {
	if err := checkContext(ctx, "inspect git object store posture"); err != nil {
		return err
	}
	directory, err := os.Open(directoryPath)
	if err != nil {
		return err
	}
	defer directory.Close()
	for {
		if err := checkContext(ctx, "inspect git object store posture"); err != nil {
			return err
		}
		entries, readErr := directory.ReadDir(64)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		for _, entry := range entries {
			if err := checkContext(ctx, "inspect git object store posture entry"); err != nil {
				return err
			}
			path := filepath.Join(directoryPath, entry.Name())
			if filepath.Dir(path) != directoryPath || !isStrictDescendant(root, path) {
				return fmt.Errorf("git object store entry escaped its root")
			}
			info, err := os.Lstat(path)
			if err != nil {
				return err
			}
			mode := info.Mode()
			if mode&os.ModeSymlink != 0 {
				return fmt.Errorf("git object store contains a symlink or reparse link: %s", path)
			}
			if !mode.IsDir() && !mode.IsRegular() {
				return fmt.Errorf("git object store contains a special filesystem entry: %s", path)
			}
			if strings.HasSuffix(strings.ToLower(entry.Name()), ".promisor") {
				return fmt.Errorf("git object store contains a promisor pack marker: %s", entry.Name())
			}
			if mode.IsDir() {
				if err := inspectObjectStoreDirectory(ctx, root, path); err != nil {
					return err
				}
			}
		}
		if errors.Is(readErr, io.EOF) || len(entries) == 0 {
			return checkContext(ctx, "inspected git object store posture")
		}
	}
}

func canonicalGitDirectoryWithRunner(ctx context.Context, runner secureGitRunner, root, flag string) (string, error) {
	output, err := runner(ctx, root, "rev-parse", "--path-format=absolute", flag)
	if err != nil {
		return "", err
	}
	return canonicalExistingDirectory(strings.TrimSpace(string(output)))
}

func canonicalGitPathWithRunner(ctx context.Context, runner secureGitRunner, root, path string) (string, error) {
	output, err := runner(ctx, root, "rev-parse", "--path-format=absolute", "--git-path", path)
	if err != nil {
		return "", err
	}
	return canonicalExistingDirectory(strings.TrimSpace(string(output)))
}

func parseNULWorktreePaths(output []byte) []string {
	var paths []string
	for _, field := range bytes.Split(output, []byte{0}) {
		line := string(field)
		if strings.HasPrefix(line, "worktree ") {
			paths = append(paths, strings.TrimPrefix(line, "worktree "))
		}
	}
	return paths
}

func pathsOverlap(first, second string) bool {
	return isSameOrDescendant(first, second) || isSameOrDescendant(second, first)
}

func isSameOrDescendant(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil || filepath.IsAbs(relative) {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func randomSecureID() ([32]byte, error) {
	var id [32]byte
	_, err := rand.Read(id[:])
	return id, err
}

func canonicalExistingDirectory(path string) (string, error) {
	canonical, err := canonicalPath(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("path is not a directory: %s", canonical)
	}
	return canonical, nil
}

// canonicalPath resolves every existing prefix and retains only missing tail
// components. This keeps prospective worktree paths bound to their real root.
func canonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	current := abs
	var missing []string
	for {
		if _, lstatErr := os.Lstat(current); lstatErr == nil {
			resolved, resolveErr := filepath.EvalSymlinks(current)
			if resolveErr != nil {
				return "", resolveErr
			}
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved), nil
		} else if !os.IsNotExist(lstatErr) {
			return "", lstatErr
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("no existing path prefix for %s", abs)
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func isStrictDescendant(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == "." || filepath.IsAbs(relative) {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func gitOutputWithEnvironment(ctx context.Context, environment []string, dir string, args ...string) ([]byte, error) {
	return gitOutputWithExecutable(ctx, environment, "git", dir, args...)
}

func gitOutputWithExecutable(ctx context.Context, environment []string, executable, dir string, args ...string) ([]byte, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("git command is required")
	}
	if strings.TrimSpace(executable) == "" {
		return nil, fmt.Errorf("git executable is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	commandCtx, commandCancel := context.WithTimeout(ctx, secureGitTimeout)
	defer commandCancel()
	execCtx, execCancel := context.WithCancel(commandCtx)
	defer execCancel()
	gitArgs := []string{
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.fsmonitor=false",
		"-c", "core.pager=cat",
		"-c", "maintenance.auto=false",
		"-c", "gc.auto=0",
	}
	gitArgs = append(gitArgs, args...)
	cmd := exec.CommandContext(execCtx, executable, gitArgs...)
	cmd.Dir = dir
	cmd.Env = append([]string(nil), environment...)
	cmd.WaitDelay = secureGitWaitDelay
	stdout := &boundedGitOutput{limit: maxGitOutputBytes, cancel: execCancel}
	stderr := &boundedGitOutput{limit: maxGitOutputBytes, cancel: execCancel}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if stdout.Exceeded() || stderr.Exceeded() {
			return nil, fmt.Errorf("git %s output exceeded %d bytes", args[0], maxGitOutputBytes)
		}
		if commandCtx.Err() != nil {
			return nil, fmt.Errorf("git %s: %w", args[0], commandCtx.Err())
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = strings.TrimSpace(stdout.String())
		}
		if message == "" {
			return nil, fmt.Errorf("git %s: %w", args[0], err)
		}
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, message)
	}
	if stdout.Exceeded() || stderr.Exceeded() {
		return nil, fmt.Errorf("git %s output exceeded %d bytes", args[0], maxGitOutputBytes)
	}
	return stdout.Bytes(), nil
}

func resolvePinnedExecutable(ctx context.Context, name string) (string, secureExecutableIdentity, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", secureExecutableIdentity{}, err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", secureExecutableIdentity{}, err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", secureExecutableIdentity{}, err
	}
	path = filepath.Clean(path)
	identity, err := captureExecutableIdentity(ctx, path, maxGitExecutableBytes)
	if err != nil {
		return "", secureExecutableIdentity{}, err
	}
	return path, identity, nil
}

func captureExecutableIdentity(ctx context.Context, path string, limit int64) (secureExecutableIdentity, error) {
	if limit <= 0 {
		return secureExecutableIdentity{}, fmt.Errorf("executable content limit must be positive")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	file, err := os.Open(path)
	if err != nil {
		return secureExecutableIdentity{}, err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return secureExecutableIdentity{}, err
	}
	if !before.Mode().IsRegular() {
		return secureExecutableIdentity{}, fmt.Errorf("executable is not a regular file: %s", path)
	}
	if before.Size() < 0 || before.Size() > limit {
		return secureExecutableIdentity{}, fmt.Errorf("executable size %d exceeds content limit %d", before.Size(), limit)
	}
	hasher := sha256.New()
	buffer := make([]byte, 128<<10)
	var read int64
	for {
		if err := checkContext(ctx, "hash executable content"); err != nil {
			return secureExecutableIdentity{}, err
		}
		remaining := limit + 1 - read
		if remaining <= 0 {
			return secureExecutableIdentity{}, fmt.Errorf("executable content exceeds limit %d", limit)
		}
		chunk := buffer
		if int64(len(chunk)) > remaining {
			chunk = chunk[:remaining]
		}
		count, readErr := file.Read(chunk)
		if count > 0 {
			read += int64(count)
			_, _ = hasher.Write(chunk[:count])
			if read > limit {
				return secureExecutableIdentity{}, fmt.Errorf("executable content exceeds limit %d", limit)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return secureExecutableIdentity{}, fmt.Errorf("hash executable content: %w", readErr)
		}
	}
	after, err := file.Stat()
	if err != nil {
		return secureExecutableIdentity{}, err
	}
	if read != before.Size() || !os.SameFile(before, after) || before.Size() != after.Size() || before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
		return secureExecutableIdentity{}, fmt.Errorf("executable identity changed while hashing")
	}
	pathInfo, err := os.Stat(path)
	if err != nil {
		return secureExecutableIdentity{}, err
	}
	if !os.SameFile(after, pathInfo) || after.Size() != pathInfo.Size() || after.Mode() != pathInfo.Mode() || !after.ModTime().Equal(pathInfo.ModTime()) {
		return secureExecutableIdentity{}, fmt.Errorf("executable path identity changed while hashing")
	}
	identity := secureExecutableIdentity{
		fileInfo: pathInfo,
		size:     pathInfo.Size(),
		mode:     pathInfo.Mode(),
		modTime:  pathInfo.ModTime(),
	}
	copy(identity.digest[:], hasher.Sum(nil))
	return identity, nil
}

func validateFileIdentity(ctx context.Context, path string, expected secureExecutableIdentity, description string) error {
	if expected.fileInfo == nil || !filepath.IsAbs(path) {
		return fmt.Errorf("%s identity is unavailable", description)
	}
	canonical, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("resolve %s: %w", description, err)
	}
	if filepath.Clean(canonical) != filepath.Clean(path) {
		return fmt.Errorf("%s canonical identity changed", description)
	}
	actual, err := captureExecutableIdentity(ctx, path, maxGitExecutableBytes)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", description, err)
	}
	if !os.SameFile(actual.fileInfo, expected.fileInfo) || actual.size != expected.size || actual.mode != expected.mode || !actual.modTime.Equal(expected.modTime) {
		return fmt.Errorf("%s identity changed", description)
	}
	if actual.digest != expected.digest {
		return fmt.Errorf("%s content identity changed", description)
	}
	return nil
}

func secureGitEnvironment() []string {
	return append(minimalProcessEnvironment(),
		"GIT_NO_LAZY_FETCH=1",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_ASKPASS="+os.DevNull,
		"SSH_ASKPASS="+os.DevNull,
		"GCM_INTERACTIVE=never",
		"LC_ALL=C",
		"LANG=C",
	)
}

func minimalProcessEnvironment() []string {
	allowed := map[string]struct{}{
		"COMSPEC": {}, "HOME": {}, "HOMEDRIVE": {}, "HOMEPATH": {},
		"PATH": {}, "PATHEXT": {}, "SYSTEMROOT": {}, "TEMP": {},
		"TMP": {}, "TMPDIR": {}, "TZ": {}, "USERPROFILE": {}, "WINDIR": {},
	}
	environment := make([]string, 0, len(allowed))
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, keep := allowed[strings.ToUpper(key)]; keep {
			environment = append(environment, entry)
		}
	}
	return environment
}

func scrubbedGitEnvironment() []string {
	environment := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		upperKey := strings.ToUpper(key)
		if ok && (strings.HasPrefix(upperKey, "GIT_") || strings.HasPrefix(upperKey, "GCM_") || upperKey == "SSH_ASKPASS") {
			continue
		}
		environment = append(environment, entry)
	}
	return environment
}

type boundedGitOutput struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	limit    int
	exceeded bool
	cancel   context.CancelFunc
}

func (output *boundedGitOutput) Write(data []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	remaining := output.limit - output.buffer.Len()
	if remaining <= 0 {
		output.exceeded = true
		if output.cancel != nil {
			output.cancel()
		}
		return len(data), nil
	}
	keep := len(data)
	if keep > remaining {
		keep = remaining
		output.exceeded = true
	}
	_, _ = output.buffer.Write(data[:keep])
	if output.exceeded && output.cancel != nil {
		output.cancel()
	}
	return len(data), nil
}

func (output *boundedGitOutput) Exceeded() bool {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.exceeded
}

func (output *boundedGitOutput) String() string {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.buffer.String()
}

func (output *boundedGitOutput) Bytes() []byte {
	output.mu.Lock()
	defer output.mu.Unlock()
	return append([]byte(nil), output.buffer.Bytes()...)
}

// CreateWithSpec creates a worktree and provisions containers based on a spec.
