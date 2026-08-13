package reviewsandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type Kind string

const (
	KindBuild Kind = "build"
	KindTest  Kind = "test"
	KindCheck Kind = "check"
)

type Language string

const (
	LanguageAuto   Language = "auto"
	LanguageGo     Language = "go"
	LanguageRust   Language = "rust"
	LanguagePython Language = "python"
	LanguageNode   Language = "node"
)

type Status string

const (
	StatusPass          Status = "PASS"
	StatusFail          Status = "FAIL"
	StatusNotApplicable Status = "NOT_APPLICABLE"
	StatusUnavailable   Status = "UNAVAILABLE"
)

const (
	defaultTimeout              = 5 * time.Minute
	maximumTimeout              = 15 * time.Minute
	defaultMaxOutput            = 256 * 1024
	maximumMaxOutput            = 2 * 1024 * 1024
	maximumPatternBytes         = 4096
	verificationProcessWaitTime = 2 * time.Second
	maximumWorkspaceBytes       = 2 << 30
	maximumWorkspaceEntries     = 500_000
)

type Request struct {
	SnapshotRoot   string
	SourceRoot     string
	Kind           Kind
	Language       Language
	Path           string
	Pattern        string
	Timeout        time.Duration
	MaxOutputBytes int
}

type Result struct {
	Kind         Kind
	Language     Language
	Path         string
	Pattern      string
	Command      string
	Argv         []string
	ExitCode     int
	Status       Status
	Stdout       string
	Stderr       string
	Duration     time.Duration
	Truncated    bool
	NoTestFiles  bool
	NoTestScript bool
	Error        string
}

var errNodeTestScriptNotDeclared = errors.New("package.json has no test script")

type Verifier interface {
	Verify(context.Context, Request) Result
}

type commandInvocation struct {
	Name string
	Args []string
	Dir  string
	Env  []string
}

type commandOutput struct {
	Stdout    string
	Stderr    string
	ExitCode  int
	Duration  time.Duration
	Truncated bool
}

type commandRunner func(context.Context, commandInvocation, int) (commandOutput, error)

type Executor struct {
	lookPath     func(string) (string, error)
	codexCommand string
	run          commandRunner
	tempDir      func(string, string) (string, error)
	removeAll    func(string) error
	reuseRuntime bool
	runtimeMu    sync.Mutex
	runtimeDir   string
}

func NewExecutor() *Executor {
	return NewExecutorWithCodexCommand("")
}

// NewExecutorWithCodexCommand configures an absolute Codex executable. When
// command is empty, discovery is limited to the fixed trusted executable path.
func NewExecutorWithCodexCommand(command string) *Executor {
	return &Executor{
		lookPath:     trustedLookPath,
		codexCommand: strings.TrimSpace(command),
		run:          runCommand,
		tempDir:      os.MkdirTemp,
		removeAll:    os.RemoveAll,
	}
}

// NewSessionExecutorWithCodexCommand reuses one private build runtime until
// Close. This lets verification calls share safe compiler caches.
func NewSessionExecutorWithCodexCommand(command string) *Executor {
	executor := NewExecutorWithCodexCommand(command)
	executor.reuseRuntime = true
	return executor
}

// Close removes a session executor's private build runtime.
func (e *Executor) Close() error {
	if e == nil {
		return nil
	}
	e.runtimeMu.Lock()
	runtimeDir := e.runtimeDir
	e.runtimeDir = ""
	e.runtimeMu.Unlock()
	if runtimeDir == "" || e.removeAll == nil {
		return nil
	}
	return e.removeAll(runtimeDir)
}

func (e *Executor) Verify(parent context.Context, request Request) Result {
	result := Result{Kind: request.Kind, Language: request.Language, ExitCode: -1, Status: StatusUnavailable}
	if e == nil {
		result.Error = "review verification executor is unavailable"
		return result
	}
	if parent == nil {
		parent = context.Background()
	}

	root, workDir, err := resolveSnapshotDirectory(request.SnapshotRoot, request.Path)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	relativePath, relErr := filepath.Rel(root, workDir)
	if relErr != nil {
		result.Error = fmt.Sprintf("normalize verification path: %v", relErr)
		return result
	}
	result.Path = filepath.ToSlash(filepath.Clean(relativePath))
	result.Pattern = strings.TrimSpace(request.Pattern)
	language, err := resolveLanguage(request.Language, workDir)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.Language = language
	if language == LanguageNode {
		workDir, err = resolveNodePackageDirectory(root, workDir)
		if err != nil {
			result.Error = err.Error()
			return result
		}
		relativePath, err = filepath.Rel(root, workDir)
		if err != nil {
			result.Error = fmt.Sprintf("normalize promoted Node package path: %v", err)
			return result
		}
		result.Path = filepath.ToSlash(filepath.Clean(relativePath))
	}
	plan, err := verificationPlan(request.Kind, language, request.Pattern, workDir)
	if err != nil {
		if errors.Is(err, errNodeTestScriptNotDeclared) {
			result.Status = StatusNotApplicable
			result.Argv = []string{}
			result.NoTestScript = true
			return result
		}
		result.Error = err.Error()
		return result
	}
	resolved, err := e.lookPath(plan.command)
	if err != nil {
		result.Command = plan.command
		result.Argv = append([]string{plan.command}, plan.args...)
		result.Error = fmt.Sprintf("verification executable %q is unavailable: %v", plan.command, err)
		return result
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		result.Error = fmt.Sprintf("resolve verification executable: %v", err)
		return result
	}
	result.Command = resolved
	result.Argv = append([]string{resolved}, plan.args...)

	// Go verification prefers the native bubblewrap launcher. Its isolated
	// network namespace permits loopback sockets while denying external
	// network access, so ordinary httptest and nested verifier tests remain
	// usable. It also avoids a Codex subprocess and its preflight on every Go
	// target. Other languages use the Codex sandbox, and Go falls back to it
	// when bubblewrap is unavailable.
	var bwrap string
	var bwrapErr error
	if language == LanguageGo {
		bwrap, bwrapErr = e.lookPath("bwrap")
	}
	useNativeGo := language == LanguageGo && bwrapErr == nil
	var codex string
	var codexErr error
	if !useNativeGo {
		codex, codexErr = e.resolveCodex()
	}
	if !useNativeGo && codexErr != nil {
		if language == LanguageGo {
			result.Error = fmt.Sprintf(
				"no verification sandbox executor is available: Codex was not found (%v) and the native Go sandbox (bwrap) was not found (%v)",
				codexErr, bwrapErr)
		} else {
			result.Error = fmt.Sprintf(
				"unsupported verification command: %s %s verification requires the Codex sandbox executor, which is unavailable (%v); only Go build, test, and check commands run without Codex",
				language, request.Kind, codexErr)
		}
		return result
	}

	runtimeDir, cleanupRuntime, err := e.prepareRuntime()
	if err != nil {
		result.Error = fmt.Sprintf("create private verification runtime: %v", err)
		return result
	}
	defer cleanupRuntime()

	timeout := request.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	if timeout > maximumTimeout {
		result.Error = fmt.Sprintf("verification timeout exceeds %s", maximumTimeout)
		return result
	}
	maxOutput := request.MaxOutputBytes
	if maxOutput <= 0 {
		maxOutput = defaultMaxOutput
	}
	if maxOutput > maximumMaxOutput {
		result.Error = fmt.Sprintf("verification output limit exceeds %d bytes", maximumMaxOutput)
		return result
	}

	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	_, writableWorkDir, cleanupWorkspace, err := e.prepareWritableWorkspace(ctx, root, relativePath, runtimeDir)
	if err != nil {
		result.Error = fmt.Sprintf("prepare writable verification workspace: %v", err)
		return result
	}
	defer cleanupWorkspace()
	var additionalReadRoots []string
	if language == LanguageNode {
		nodeModulesRoot, projectionErr := projectNodeDependencies(root, request.SourceRoot, relativePath, writableWorkDir)
		if projectionErr != nil {
			result.Error = projectionErr.Error()
			return result
		}
		additionalReadRoots = append(additionalReadRoots, nodeModulesRoot)
	}

	params := launchParams{
		root:       root,
		workDir:    writableWorkDir,
		resolved:   resolved,
		plan:       plan,
		runtimeDir: runtimeDir,
		maxOutput:  maxOutput,
		readRoots:  additionalReadRoots,
	}
	var output commandOutput
	var runErr error
	var launchFailure, launcherLabel string
	if useNativeGo {
		launcherLabel = "native Go sandbox"
		output, runErr, launchFailure = e.runViaNativeGo(ctx, bwrap, params)
	} else {
		launcherLabel = "Codex sandbox"
		output, runErr, launchFailure = e.runViaCodex(ctx, codex, params)
	}
	if launchFailure != "" {
		result.Error = launchFailure
		return result
	}
	return classifyVerificationRun(result, request, language, timeout, launcherLabel, output, runErr, ctx.Err())
}

// resolveCodex resolves the configured or trusted Codex sandbox executable.
func (e *Executor) resolveCodex() (string, error) {
	var codex string
	var err error
	if e.codexCommand != "" {
		codex, err = resolveExplicitExecutable(e.codexCommand)
	} else {
		codex, err = e.lookPath("codex")
	}
	if err != nil {
		return "", err
	}
	codex, err = filepath.Abs(codex)
	if err != nil {
		return "", err
	}
	if canonical, evalErr := filepath.EvalSymlinks(codex); evalErr == nil {
		codex = canonical
	}
	return codex, nil
}

// launchParams carries the resolved, already-validated invocation details
// shared by every sandbox launcher.
type launchParams struct {
	root       string
	workDir    string
	resolved   string
	plan       plan
	runtimeDir string
	maxOutput  int
	readRoots  []string
}

// runViaCodex launches the resolved verification command inside Codex's OS
// sandbox profile. Behavior is unchanged from before this executor gained a
// native Go verification path: same preflight check, same read-only
// workspace/toolchain permission profile, same restricted environment.
func (e *Executor) runViaCodex(ctx context.Context, codex string, params launchParams) (commandOutput, error, string) {
	readRoots := []string{params.root, filepath.Dir(params.resolved)}
	if canonical, evalErr := filepath.EvalSymlinks(params.resolved); evalErr == nil {
		readRoots = append(readRoots, filepath.Dir(canonical))
	}
	readRoots = append(readRoots, params.readRoots...)
	policyArgs := verificationPermissionArgsWithReadRoots(codex, params.runtimeDir, readRoots...)
	preflightExecutable, preflightErr := e.lookPath("true")
	if preflightErr != nil {
		return commandOutput{}, nil, fmt.Sprintf("Codex sandbox preflight is unavailable: %v", preflightErr)
	}
	preflightArgs := []string{"sandbox", "-P", PermissionProfileName, "-C", params.workDir}
	preflightArgs = append(preflightArgs, policyArgs...)
	preflightArgs = append(preflightArgs, "--", preflightExecutable)
	preflight, preflightRunErr := e.run(ctx, commandInvocation{
		Name: codex,
		Args: preflightArgs,
		Dir:  params.workDir,
		Env:  RestrictedCommandEnvironment(params.runtimeDir),
	}, 16*1024)
	if preflightRunErr != nil || preflight.ExitCode != 0 {
		message := strings.TrimSpace("Codex sandbox preflight failed: " + preflight.Stderr)
		if message == "Codex sandbox preflight failed:" {
			message = fmt.Sprintf("Codex sandbox preflight failed: %v", preflightRunErr)
		}
		return commandOutput{}, nil, message
	}

	sandboxArgs := []string{"sandbox", "-P", PermissionProfileName, "-C", params.workDir}
	sandboxArgs = append(sandboxArgs, policyArgs...)
	sandboxArgs = append(sandboxArgs, "--", params.resolved)
	sandboxArgs = append(sandboxArgs, params.plan.args...)
	output, runErr := e.run(ctx, commandInvocation{
		Name: codex,
		Args: sandboxArgs,
		Dir:  params.workDir,
		Env:  RestrictedCommandEnvironment(params.runtimeDir),
	}, params.maxOutput)
	return output, runErr, ""
}

// runViaNativeGo launches a recognized Go build/test/check command through a
// bubblewrap (bwrap) mount- and network-namespace sandbox when Codex is not
// installed. It enforces the same invariants as the Codex path: the entire
// immutable snapshot root and Go toolchain are bind-mounted read-only. The
// command runs in a disposable copy below the private runtime directory, which
// is the only writable location, and the network namespace is unshared.
func (e *Executor) runViaNativeGo(ctx context.Context, bwrap string, params launchParams) (commandOutput, error, string) {
	goRoot := filepath.Dir(filepath.Dir(params.resolved))
	readRoots := []string{params.root, goRoot}
	if canonical, evalErr := filepath.EvalSymlinks(params.resolved); evalErr == nil {
		readRoots = append(readRoots, filepath.Dir(canonical))
	}
	home, _ := os.UserHomeDir()
	if modCache := firstExistingDirectory(filepath.Join(home, "go", "pkg", "mod")); modCache != "" {
		readRoots = append(readRoots, modCache)
	}
	for _, systemRoot := range []string{"/usr", "/etc"} {
		if info, statErr := os.Stat(systemRoot); statErr == nil && info.IsDir() {
			readRoots = append(readRoots, systemRoot)
		}
	}
	readRoots = canonicalExistingDirectories(readRoots)

	args := []string{"--tmpfs", "/tmp"}
	for _, readRoot := range readRoots {
		args = append(args, "--ro-bind", readRoot, readRoot)
	}
	// Usr-merged distributions expose these as root-level symlinks. Binding
	// their resolved directories recreates the conventional paths inside the
	// otherwise empty bubblewrap root, which Git local transports and script
	// shebangs still invoke directly.
	for _, optional := range []string{"/bin", "/sbin", "/lib", "/lib64"} {
		args = append(args, "--ro-bind-try", optional, optional)
	}
	args = append(args,
		// Bind the private runtime directory writable, then remount the /tmp
		// tmpfs itself read-only. bwrap's remount-ro applies only to the exact
		// mount point given, so the nested runtimeDir bind stays writable while
		// the rest of /tmp is not: the private runtime directory remains the
		// only writable location, matching the Codex sandbox invariant exactly.
		"--bind", params.runtimeDir, params.runtimeDir,
		"--remount-ro", "/tmp",
		"--proc", "/proc",
		"--dev", "/dev",
		"--unshare-net",
		"--unshare-uts",
		"--unshare-ipc",
		"--die-with-parent",
		"--chdir", params.workDir,
		"--clearenv",
	)

	env := ToolEnvironment(params.runtimeDir)
	env["GOROOT"] = goRoot
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, "--setenv", key, env[key])
	}
	args = append(args, "--", params.resolved)
	args = append(args, params.plan.args...)

	output, runErr := e.run(ctx, commandInvocation{
		Name: bwrap,
		Args: args,
		Dir:  params.workDir,
	}, params.maxOutput)
	return output, runErr, ""
}

// classifyVerificationRun applies the shared PASS/FAIL/UNAVAILABLE
// classification to a completed sandbox launch, independent of which
// launcher produced it.
func classifyVerificationRun(result Result, request Request, language Language, timeout time.Duration, launcherLabel string, output commandOutput, runErr, contextErr error) Result {
	result.Stdout = output.Stdout
	result.Stderr = output.Stderr
	result.ExitCode = output.ExitCode
	result.Duration = output.Duration
	result.Truncated = output.Truncated
	if runErr != nil {
		if errors.Is(contextErr, context.DeadlineExceeded) || errors.Is(runErr, context.DeadlineExceeded) {
			result.ExitCode = 124
			result.Status = StatusUnavailable
			result.Error = fmt.Sprintf("verification timed out after %s", timeout)
			return result
		}
		if errors.Is(contextErr, context.Canceled) || errors.Is(runErr, context.Canceled) {
			result.ExitCode = -1
			result.Status = StatusUnavailable
			result.Error = "verification canceled before completion"
			return result
		}
		// An ExitError means the OS sandbox ran and the verification command
		// returned a real failure. Launcher/profile failures are unavailable.
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			if verificationOutputShowsSandboxRestriction(output.Stdout + "\n" + output.Stderr) {
				result.Status = StatusUnavailable
				result.ExitCode = -1
				result.Error = "verification requires a capability denied by the review sandbox"
				return result
			}
			result.Status = StatusFail
			if result.ExitCode < 0 {
				result.ExitCode = exitErr.ExitCode()
			}
			result.Error = "verification command failed"
			return result
		}
		result.Status = StatusUnavailable
		result.ExitCode = -1
		result.Error = fmt.Sprintf("%s failed to launch: %v", launcherLabel, runErr)
		return result
	}
	if output.ExitCode != 0 {
		if verificationOutputShowsSandboxRestriction(output.Stdout + "\n" + output.Stderr) {
			result.Status = StatusUnavailable
			result.ExitCode = -1
			result.Error = "verification requires a capability denied by the review sandbox"
			return result
		}
		result.Status = StatusFail
		result.Error = "verification command failed"
		return result
	}
	combinedOutput := output.Stdout + "\n" + output.Stderr
	if request.Kind == KindTest && verificationOutputShowsNoTestFiles(language, combinedOutput) {
		result.NoTestFiles = true
		result.Status = StatusPass
		return result
	}
	if request.Kind == KindTest && verificationOutputShowsNoTests(language, combinedOutput) {
		result.Status = StatusFail
		result.Error = "verification command completed without executing tests"
		return result
	}
	result.Status = StatusPass
	return result
}

func verificationOutputShowsSandboxRestriction(output string) bool {
	output = strings.ToLower(output)
	for _, markers := range [][2]string{
		{"listen tcp", "operation not permitted"},
		{"socket", "operation not permitted"},
		{"bwrap", "operation not permitted"},
		{"namespace", "operation not permitted"},
		{"netlink_route", "operation not permitted"},
	} {
		if strings.Contains(output, markers[0]) && strings.Contains(output, markers[1]) {
			return true
		}
	}
	return false
}

func (e *Executor) prepareRuntime() (string, func(), error) {
	if !e.reuseRuntime {
		runtimeDir, err := e.tempDir("", "buckley-review-verification-*")
		if err != nil {
			return "", func() {}, err
		}
		if err := PrepareRuntime(runtimeDir); err != nil {
			_ = e.removeAll(runtimeDir)
			return "", func() {}, err
		}
		return runtimeDir, func() { _ = e.removeAll(runtimeDir) }, nil
	}

	e.runtimeMu.Lock()
	defer e.runtimeMu.Unlock()
	if e.runtimeDir != "" {
		return e.runtimeDir, func() {}, nil
	}
	runtimeDir, err := e.tempDir("", "buckley-review-verification-*")
	if err != nil {
		return "", func() {}, err
	}
	if err := PrepareRuntime(runtimeDir); err != nil {
		_ = e.removeAll(runtimeDir)
		return "", func() {}, err
	}
	e.runtimeDir = runtimeDir
	return runtimeDir, func() {}, nil
}

// prepareWritableWorkspace copies one immutable snapshot into a disposable
// directory below the private runtime. Verification may create repo-relative
// fixtures, caches, and logs there without mutating the review evidence used by
// the agent or by later verification calls.
func (e *Executor) prepareWritableWorkspace(ctx context.Context, snapshotRoot, relativePath, runtimeDir string) (string, string, func(), error) {
	if e == nil || e.tempDir == nil {
		return "", "", func() {}, fmt.Errorf("verification workspace allocator is unavailable")
	}
	workspaces := filepath.Join(runtimeDir, "workspaces")
	if err := os.MkdirAll(workspaces, 0o700); err != nil {
		return "", "", func() {}, err
	}
	writableRoot, err := e.tempDir(workspaces, "run-*")
	if err != nil {
		return "", "", func() {}, err
	}
	cleanup := func() {
		if e.removeAll != nil {
			_ = e.removeAll(writableRoot)
		}
	}
	if err := copyReviewWorkspace(ctx, snapshotRoot, writableRoot); err != nil {
		cleanup()
		return "", "", func() {}, err
	}
	writableWorkDir := filepath.Join(writableRoot, filepath.FromSlash(relativePath))
	info, err := os.Stat(writableWorkDir)
	if err != nil || !info.IsDir() {
		cleanup()
		if err == nil {
			err = fmt.Errorf("not a directory")
		}
		return "", "", func() {}, fmt.Errorf("resolve copied verification path: %w", err)
	}
	return writableRoot, writableWorkDir, cleanup, nil
}

func copyReviewWorkspace(ctx context.Context, sourceRoot, destinationRoot string) error {
	sourceRoot = filepath.Clean(sourceRoot)
	copyBuffer := make([]byte, 128*1024)
	var copiedBytes int64
	var copiedEntries int
	return filepath.WalkDir(sourceRoot, func(sourcePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(sourceRoot, sourcePath)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("copy path escapes immutable snapshot")
		}
		if relative == "." {
			return os.Chmod(destinationRoot, 0o700)
		}
		copiedEntries++
		if copiedEntries > maximumWorkspaceEntries {
			return fmt.Errorf("snapshot exceeds %d-entry verification workspace limit", maximumWorkspaceEntries)
		}
		destinationPath := filepath.Join(destinationRoot, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case entry.IsDir():
			return os.Mkdir(destinationPath, info.Mode().Perm()|0o700)
		case info.Mode().IsRegular():
			if info.Size() < 0 || copiedBytes > maximumWorkspaceBytes-info.Size() {
				return fmt.Errorf("snapshot exceeds %d-byte verification workspace limit", maximumWorkspaceBytes)
			}
			copiedBytes += info.Size()
			return copyReviewFile(ctx, sourcePath, destinationPath, info.Mode().Perm()|0o600, copyBuffer)
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(sourcePath)
			if err != nil {
				return err
			}
			lexicalTarget := filepath.Join(filepath.Dir(sourcePath), target)
			if filepath.IsAbs(target) || !pathWithinRoot(sourceRoot, lexicalTarget) {
				return fmt.Errorf("snapshot symlink %q escapes the repository", relative)
			}
			if resolved, resolveErr := filepath.EvalSymlinks(sourcePath); resolveErr == nil && !pathWithinRoot(sourceRoot, resolved) {
				return fmt.Errorf("snapshot symlink %q resolves outside the repository", relative)
			}
			return os.Symlink(target, destinationPath)
		default:
			return fmt.Errorf("snapshot path %q has unsupported mode %s", relative, info.Mode())
		}
	})
}

func copyReviewFile(ctx context.Context, sourcePath, destinationPath string, mode os.FileMode, buffer []byte) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	destination, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	// Hide os.File.ReadFrom so CopyBuffer reuses the invocation-wide buffer
	// instead of allocating one fallback buffer for every source file.
	_, copyErr := io.CopyBuffer(struct{ io.Writer }{destination}, contextReader{ctx: ctx, reader: source}, buffer)
	closeErr := destination.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

func pathWithinRoot(root, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

type nodeLockPackage struct {
	Version   json.RawMessage `json:"version"`
	Resolved  json.RawMessage `json:"resolved"`
	Integrity json.RawMessage `json:"integrity"`
	Link      json.RawMessage `json:"link"`
}

type nodePackageLock struct {
	Packages map[string]nodeLockPackage `json:"packages"`
}

// projectNodeDependencies exposes an ambient install only when it is exact
// evidence for the immutable package being verified. The source repository is
// host-owned input, never model-controlled, and only the validated
// node_modules directory becomes a read root.
func projectNodeDependencies(snapshotRoot, sourceRoot, packagePath, writablePackage string) (string, error) {
	sourceRoot = strings.TrimSpace(sourceRoot)
	if sourceRoot == "" {
		return "", fmt.Errorf("Node verification dependencies are unavailable: source repository root is not sealed")
	}
	canonicalSource, err := filepath.EvalSymlinks(filepath.Clean(sourceRoot))
	if err != nil {
		return "", fmt.Errorf("Node verification dependencies are unavailable: resolve source repository root: %w", err)
	}
	info, err := os.Stat(canonicalSource)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("Node verification dependencies are unavailable: source repository root is not a directory")
	}
	sourcePackage := filepath.Join(canonicalSource, filepath.FromSlash(packagePath))
	resolvedPackage, err := filepath.EvalSymlinks(sourcePackage)
	if err != nil || !pathWithinRoot(canonicalSource, resolvedPackage) {
		return "", fmt.Errorf("Node verification dependencies are unavailable: source package escapes repository root")
	}

	for _, name := range []string{"package.json", "package-lock.json"} {
		snapshotContent, readSnapshotErr := readRegularFileWithin(snapshotRoot, filepath.Join(snapshotRoot, filepath.FromSlash(packagePath), name))
		sourceContent, readSourceErr := readRegularFileWithin(canonicalSource, filepath.Join(resolvedPackage, name))
		if readSnapshotErr != nil || readSourceErr != nil {
			return "", fmt.Errorf("Node verification dependencies are unavailable: %s is missing or unsafe", name)
		}
		if !bytes.Equal(snapshotContent, sourceContent) {
			return "", fmt.Errorf("Node verification dependencies are unavailable: source %s does not match immutable snapshot", name)
		}
	}

	rootLockPath := filepath.Join(resolvedPackage, "package-lock.json")
	installedLockPath := filepath.Join(resolvedPackage, "node_modules", ".package-lock.json")
	rootLockContent, rootErr := readRegularFileWithin(canonicalSource, rootLockPath)
	installedLockContent, installedErr := readRegularFileWithin(canonicalSource, installedLockPath)
	if rootErr != nil || installedErr != nil {
		return "", fmt.Errorf("Node verification dependencies are unavailable: node_modules lock identity is missing or unsafe")
	}
	var expected, installed nodePackageLock
	if json.Unmarshal(rootLockContent, &expected) != nil || json.Unmarshal(installedLockContent, &installed) != nil || expected.Packages == nil || installed.Packages == nil {
		return "", fmt.Errorf("Node verification dependencies are unavailable: package lock identity is invalid")
	}
	delete(expected.Packages, "")
	if !reflect.DeepEqual(expected.Packages, installed.Packages) {
		return "", fmt.Errorf("Node verification dependencies are unavailable: node_modules does not exactly match package-lock.json")
	}

	nodeModules := filepath.Join(resolvedPackage, "node_modules")
	canonicalModules, err := filepath.EvalSymlinks(nodeModules)
	if err != nil || !pathWithinRoot(canonicalSource, canonicalModules) {
		return "", fmt.Errorf("Node verification dependencies are unavailable: node_modules escapes repository root")
	}
	info, err = os.Stat(canonicalModules)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("Node verification dependencies are unavailable: node_modules is not a directory")
	}
	if err := validateNodeModulesSymlinks(canonicalModules); err != nil {
		return "", fmt.Errorf("Node verification dependencies are unavailable: %w", err)
	}
	projection := filepath.Join(writablePackage, "node_modules")
	if _, err := os.Lstat(projection); err == nil || !os.IsNotExist(err) {
		return "", fmt.Errorf("Node verification dependencies are unavailable: immutable snapshot already contains node_modules")
	}
	if err := os.Symlink(canonicalModules, projection); err != nil {
		return "", fmt.Errorf("Node verification dependencies are unavailable: project node_modules: %w", err)
	}
	return canonicalModules, nil
}

func validateNodeModulesSymlinks(nodeModulesRoot string) error {
	entries := 0
	return filepath.WalkDir(nodeModulesRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		entries++
		if entries > maximumWorkspaceEntries {
			return fmt.Errorf("node_modules exceeds %d-entry verification limit", maximumWorkspaceEntries)
		}
		if entry.Type()&os.ModeSymlink == 0 {
			return nil
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return fmt.Errorf("node_modules symlink %q is unresolved", path)
		}
		if !pathWithinRoot(nodeModulesRoot, resolved) {
			return fmt.Errorf("node_modules symlink %q escapes the validated dependency root", path)
		}
		return nil
	})
}

func readRegularFileWithin(root, path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !pathWithinRoot(root, resolved) {
		return nil, fmt.Errorf("path escapes root")
	}
	return os.ReadFile(resolved)
}

var rustRunningNonZeroTestsRE = regexp.MustCompile(`(?m)\brunning\s+[1-9][0-9]*\s+tests?\b`)

func verificationOutputShowsNoTestFiles(language Language, output string) bool {
	return language == LanguageGo && strings.Contains(strings.ToLower(output), "[no test files]")
}

func verificationOutputShowsNoTests(language Language, output string) bool {
	lower := strings.ToLower(output)
	for _, marker := range []string{
		"[no test files]",
		"no tests to run",
		"no tests ran",
		"no tests found",
		"collected 0 item",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	if language != LanguageRust || !strings.Contains(lower, "running 0 tests") {
		return false
	}
	return !rustRunningNonZeroTestsRE.MatchString(lower)
}

type plan struct {
	command string
	args    []string
}

func verificationPlan(kind Kind, language Language, pattern, workDir string) (plan, error) {
	if kind != KindBuild && kind != KindTest && kind != KindCheck {
		return plan{}, fmt.Errorf("verification kind must be build, test, or check")
	}
	pattern = strings.TrimSpace(pattern)
	if len(pattern) > maximumPatternBytes || strings.ContainsRune(pattern, 0) || strings.HasPrefix(pattern, "-") {
		return plan{}, fmt.Errorf("verification pattern is invalid")
	}
	if kind != KindTest && pattern != "" {
		return plan{}, fmt.Errorf("verification pattern is only supported for tests")
	}

	switch language {
	case LanguageGo:
		switch kind {
		case KindBuild:
			return plan{"go", []string{"test", "-count=1", "-run", "^$", "."}}, nil
		case KindTest:
			args := []string{"test", "-count=1"}
			if pattern != "" {
				args = append(args, "-v", "-run", anchorGoTestPattern(pattern))
			}
			return plan{"go", append(args, ".")}, nil
		case KindCheck:
			return plan{"go", []string{"vet", "."}}, nil
		}
	case LanguageRust:
		base := []string{"--offline", "--locked"}
		switch kind {
		case KindBuild:
			return plan{"cargo", append([]string{"build"}, base...)}, nil
		case KindTest:
			args := append([]string{"test"}, base...)
			if pattern != "" {
				args = append(args, pattern)
			}
			return plan{"cargo", args}, nil
		case KindCheck:
			return plan{"cargo", append([]string{"check"}, base...)}, nil
		}
	case LanguagePython:
		switch kind {
		case KindBuild, KindCheck:
			return plan{"python3", []string{"-m", "compileall", "-q", "."}}, nil
		case KindTest:
			args := []string{"-m", "pytest", "-p", "no:cacheprovider", "-q"}
			if pattern != "" {
				args = append(args, "-k", pattern)
			}
			return plan{"python3", append(args, ".")}, nil
		}
	case LanguageNode:
		script, err := nodeScriptForKind(kind, workDir)
		if err != nil {
			return plan{}, err
		}
		args := []string{"--offline", "run", script}
		if kind == KindTest && pattern != "" {
			args = append(args, "--", "--testNamePattern", pattern)
		}
		return plan{"npm", args}, nil
	}
	return plan{}, fmt.Errorf("verification language %q is unsupported", language)
}

func anchorGoTestPattern(pattern string) string {
	parts := strings.Split(pattern, "/")
	for index, part := range parts {
		parts[index] = "^(" + part + ")$"
	}
	return strings.Join(parts, "/")
}

func resolveSnapshotDirectory(snapshotRoot, requested string) (string, string, error) {
	root, err := filepath.Abs(strings.TrimSpace(snapshotRoot))
	if err != nil || strings.TrimSpace(snapshotRoot) == "" {
		return "", "", fmt.Errorf("immutable review snapshot root is required")
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", fmt.Errorf("resolve immutable review snapshot root: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return "", "", fmt.Errorf("immutable review snapshot root is not a directory")
	}
	requested = strings.TrimSpace(requested)
	if requested == "" {
		requested = "."
	}
	if filepath.IsAbs(requested) || strings.ContainsRune(requested, 0) {
		return "", "", fmt.Errorf("verification path must be snapshot-relative")
	}
	candidate, err := filepath.EvalSymlinks(filepath.Join(root, filepath.Clean(requested)))
	if err != nil {
		return "", "", fmt.Errorf("resolve verification path: %w", err)
	}
	rel, err := filepath.Rel(root, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("verification path escapes immutable snapshot")
	}
	info, err = os.Stat(candidate)
	if err != nil || !info.IsDir() {
		return "", "", fmt.Errorf("verification path is not a directory")
	}
	return filepath.Clean(root), filepath.Clean(candidate), nil
}

// resolveNodePackageDirectory promotes a nested Node verification target to
// its nearest package root. The walk is bounded by the already-canonicalized
// immutable snapshot root, and a package.json symlink must resolve inside that
// same snapshot before it can select a package.
func resolveNodePackageDirectory(snapshotRoot, requestedDir string) (string, error) {
	root, err := filepath.EvalSymlinks(filepath.Clean(snapshotRoot))
	if err != nil {
		return "", fmt.Errorf("resolve immutable snapshot root for Node verification: %w", err)
	}
	current, err := filepath.EvalSymlinks(filepath.Clean(requestedDir))
	if err != nil {
		return "", fmt.Errorf("resolve Node verification path: %w", err)
	}
	if !pathWithinRoot(root, current) {
		return "", fmt.Errorf("node verification path escapes immutable snapshot")
	}

	for {
		manifestPath := filepath.Join(current, "package.json")
		info, err := os.Lstat(manifestPath)
		switch {
		case err == nil:
			resolvedManifest, resolveErr := filepath.EvalSymlinks(manifestPath)
			if resolveErr != nil {
				return "", fmt.Errorf("resolve package.json: %w", resolveErr)
			}
			if !pathWithinRoot(root, resolvedManifest) {
				return "", fmt.Errorf("package.json escapes immutable snapshot")
			}
			if info.Mode()&os.ModeSymlink != 0 {
				info, err = os.Stat(resolvedManifest)
				if err != nil {
					return "", fmt.Errorf("stat package.json: %w", err)
				}
			}
			if !info.Mode().IsRegular() {
				return "", fmt.Errorf("package.json is not a regular file")
			}
			return current, nil
		case !os.IsNotExist(err):
			return "", fmt.Errorf("inspect package.json: %w", err)
		}

		if current == root {
			break
		}
		parent := filepath.Dir(current)
		if parent == current || !pathWithinRoot(root, parent) {
			break
		}
		current = parent
	}
	return "", fmt.Errorf("no package.json found at or above verification path within immutable snapshot")
}

func resolveLanguage(language Language, workDir string) (Language, error) {
	if language == "" {
		language = LanguageAuto
	}
	if language != LanguageAuto {
		switch language {
		case LanguageGo, LanguageRust, LanguagePython, LanguageNode:
			return language, nil
		default:
			return "", fmt.Errorf("verification language %q is unsupported", language)
		}
	}
	checks := []struct {
		language Language
		files    []string
	}{
		{LanguageGo, []string{"go.mod"}},
		{LanguageRust, []string{"Cargo.toml"}},
		{LanguageNode, []string{"package.json"}},
		{LanguagePython, []string{"pyproject.toml", "setup.py", "pytest.ini"}},
	}
	for _, check := range checks {
		for _, name := range check.files {
			if info, err := os.Stat(filepath.Join(workDir, name)); err == nil && !info.IsDir() {
				return check.language, nil
			}
		}
	}
	return "", fmt.Errorf("no supported language manifest found in verification path")
}

// PrepareRuntime creates the private writable directories referenced by the
// shared review sandbox environment. Native Codex and API verification must
// both call this before launching any build or test process.
func PrepareRuntime(runtimeDir string) error {
	for _, dir := range []string{"codex-home", "home", "go-build", "go-tmp", "cargo-home", "cargo-target", "npm-cache", "pip-cache", "pycache", "xdg-cache", "yarn-cache"} {
		if err := os.MkdirAll(filepath.Join(runtimeDir, dir), 0o700); err != nil {
			return err
		}
	}
	home, _ := os.UserHomeDir()
	for _, name := range []string{"registry", "git"} {
		target := filepath.Join(home, ".cargo", name)
		if info, err := os.Stat(target); err == nil && info.IsDir() {
			if err := os.Symlink(target, filepath.Join(runtimeDir, "cargo-home", name)); err != nil && !os.IsExist(err) {
				return err
			}
		}
	}
	return nil
}

func runCommand(ctx context.Context, invocation commandInvocation, maxOutput int) (commandOutput, error) {
	started := time.Now()
	stdout := newLimitedBuffer(maxOutput)
	stderr := newLimitedBuffer(maxOutput)
	cmd := exec.CommandContext(ctx, invocation.Name, invocation.Args...)
	cmd.Dir = invocation.Dir
	cmd.Env = append([]string(nil), invocation.Env...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = verificationProcessWaitTime
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		exitCode = -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}
	return commandOutput{
		Stdout:    stdout.String(),
		Stderr:    stderr.String(),
		ExitCode:  exitCode,
		Duration:  time.Since(started),
		Truncated: stdout.truncated || stderr.truncated,
	}, err
}

type limitedBuffer struct {
	buffer    bytes.Buffer
	remaining int
	truncated bool
}

func newLimitedBuffer(max int) *limitedBuffer {
	return &limitedBuffer{remaining: max}
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	original := len(p)
	if b.remaining > 0 {
		write := p
		if len(write) > b.remaining {
			write = write[:b.remaining]
		}
		_, _ = b.buffer.Write(write)
		b.remaining -= len(write)
	}
	if original > 0 && b.remaining == 0 {
		b.truncated = true
	}
	return original, nil
}

func (b *limitedBuffer) String() string {
	if !b.truncated {
		return b.buffer.String()
	}
	return b.buffer.String() + "\n... (output truncated)"
}

var _ io.Writer = (*limitedBuffer)(nil)

func nodeScriptForKind(kind Kind, workDir string) (string, error) {
	content, err := os.ReadFile(filepath.Join(workDir, "package.json"))
	if err != nil {
		return "", fmt.Errorf("read package.json: %w", err)
	}
	var manifest struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(content, &manifest); err != nil {
		return "", fmt.Errorf("parse package.json: %w", err)
	}
	script := string(kind)
	if strings.TrimSpace(manifest.Scripts[script]) == "" {
		if kind == KindTest {
			return "", errNodeTestScriptNotDeclared
		}
		return "", fmt.Errorf("package.json has no %q script", script)
	}
	return script, nil
}
