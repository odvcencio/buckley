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
	"regexp"
	"strings"
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
	StatusPass        Status = "PASS"
	StatusFail        Status = "FAIL"
	StatusUnavailable Status = "UNAVAILABLE"
)

const (
	defaultTimeout   = 5 * time.Minute
	maximumTimeout   = 15 * time.Minute
	defaultMaxOutput = 256 * 1024
	maximumMaxOutput = 2 * 1024 * 1024
)

type Request struct {
	SnapshotRoot   string
	Kind           Kind
	Language       Language
	Path           string
	Pattern        string
	Timeout        time.Duration
	MaxOutputBytes int
}

type Result struct {
	Kind      Kind
	Language  Language
	Path      string
	Pattern   string
	Command   string
	Argv      []string
	ExitCode  int
	Status    Status
	Stdout    string
	Stderr    string
	Duration  time.Duration
	Truncated bool
	Error     string
}

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
	plan, err := verificationPlan(request.Kind, language, request.Pattern, workDir)
	if err != nil {
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

	var codex string
	if e.codexCommand != "" {
		codex, err = resolveExplicitExecutable(e.codexCommand)
	} else {
		codex, err = e.lookPath("codex")
	}
	if err != nil {
		result.Error = fmt.Sprintf("Codex sandbox is unavailable: %v", err)
		return result
	}
	codex, err = filepath.Abs(codex)
	if err != nil {
		result.Error = fmt.Sprintf("resolve Codex sandbox executable: %v", err)
		return result
	}
	if canonical, evalErr := filepath.EvalSymlinks(codex); evalErr == nil {
		codex = canonical
	}

	runtimeDir, err := e.tempDir("", "buckley-review-verification-*")
	if err != nil {
		result.Error = fmt.Sprintf("create private verification runtime: %v", err)
		return result
	}
	defer func() { _ = e.removeAll(runtimeDir) }()
	if err := PrepareRuntime(runtimeDir); err != nil {
		result.Error = fmt.Sprintf("prepare private verification runtime: %v", err)
		return result
	}

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
	readRoots := []string{root, filepath.Dir(resolved)}
	if canonical, evalErr := filepath.EvalSymlinks(resolved); evalErr == nil {
		readRoots = append(readRoots, filepath.Dir(canonical))
	}
	policyArgs := PermissionArgsWithReadRoots(codex, runtimeDir, readRoots...)
	preflightExecutable, preflightErr := e.lookPath("true")
	if preflightErr != nil {
		result.Error = fmt.Sprintf("Codex sandbox preflight is unavailable: %v", preflightErr)
		return result
	}
	preflightArgs := []string{"sandbox", "-P", PermissionProfileName, "-C", workDir}
	preflightArgs = append(preflightArgs, policyArgs...)
	preflightArgs = append(preflightArgs, "--", preflightExecutable)
	preflight, preflightRunErr := e.run(ctx, commandInvocation{
		Name: codex,
		Args: preflightArgs,
		Dir:  workDir,
		Env:  RestrictedCommandEnvironment(runtimeDir),
	}, 16*1024)
	if preflightRunErr != nil || preflight.ExitCode != 0 {
		result.Error = strings.TrimSpace("Codex sandbox preflight failed: " + preflight.Stderr)
		if result.Error == "Codex sandbox preflight failed:" {
			result.Error = fmt.Sprintf("Codex sandbox preflight failed: %v", preflightRunErr)
		}
		return result
	}

	sandboxArgs := []string{"sandbox", "-P", PermissionProfileName, "-C", workDir}
	sandboxArgs = append(sandboxArgs, policyArgs...)
	sandboxArgs = append(sandboxArgs, "--", resolved)
	sandboxArgs = append(sandboxArgs, plan.args...)
	output, runErr := e.run(ctx, commandInvocation{
		Name: codex,
		Args: sandboxArgs,
		Dir:  workDir,
		Env:  RestrictedCommandEnvironment(runtimeDir),
	}, maxOutput)
	result.Stdout = output.Stdout
	result.Stderr = output.Stderr
	result.ExitCode = output.ExitCode
	result.Duration = output.Duration
	result.Truncated = output.Truncated
	if runErr != nil {
		if errors.Is(runErr, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			result.ExitCode = 124
			result.Status = StatusFail
			result.Error = fmt.Sprintf("verification timed out after %s", timeout)
			return result
		}
		// An ExitError means the OS sandbox ran and the verification command
		// returned a real failure. Launcher/profile failures are unavailable.
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			result.Status = StatusFail
			if result.ExitCode < 0 {
				result.ExitCode = exitErr.ExitCode()
			}
			result.Error = "verification command failed"
			return result
		}
		result.Status = StatusUnavailable
		result.ExitCode = -1
		result.Error = fmt.Sprintf("Codex sandbox failed to launch: %v", runErr)
		return result
	}
	if output.ExitCode != 0 {
		result.Status = StatusFail
		result.Error = "verification command failed"
		return result
	}
	if request.Kind == KindTest && verificationOutputShowsNoTests(language, output.Stdout+"\n"+output.Stderr) {
		result.Status = StatusFail
		result.Error = "verification command completed without executing tests"
		return result
	}
	result.Status = StatusPass
	return result
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
	return !regexp.MustCompile(`(?m)\brunning\s+[1-9][0-9]*\s+tests?\b`).MatchString(lower)
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
	if len(pattern) > 256 || strings.ContainsRune(pattern, 0) || strings.HasPrefix(pattern, "-") {
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
				args = append(args, "-v", "-run", pattern)
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
		return "", fmt.Errorf("package.json has no %q script", script)
	}
	return script, nil
}
