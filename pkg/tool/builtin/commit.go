package builtin

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// CommitChangesTool creates a governed git commit through Buckley's own
// `buckley commit` runtime.
//
// The tool never shells out and never pushes: it invokes the current Buckley
// executable directly with a fixed, safe argv (`commit --yes --push=false
// --minimal-output --exclusive`). It passes no --backend or --model override,
// so the configured default commit backend/model remain authoritative. The
// commit is scoped to an explicit, non-empty set of paths which are validated
// and deduplicated by absolute identity against the active workdir (the
// process working directory when none is configured), rejected if any of them
// escapes that boundary (including via symlinks), and passed to the runtime as
// workdir-relative scopes because cmd/buckley matches --paths entries against
// repository-relative staged names.
type CommitChangesTool struct {
	workDirAware

	// Executable overrides the path of the Buckley binary used for the
	// commit invocation. When empty, the running binary (os.Executable) is
	// used, falling back to a PATH lookup for "buckley". Exposed for tests.
	Executable string
}

func (t *CommitChangesTool) Name() string {
	return "commit_changes"
}

func (t *CommitChangesTool) Description() string {
	return "Create a governed git commit through Buckley's own commit runtime using the configured default commit model. " +
		"Commits only the explicitly provided paths; refuses to commit while unrelated staged files exist (--exclusive), " +
		"never pushes, and returns the new HEAD."
}

func (t *CommitChangesTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"paths": {
				Type:        "array",
				Description: "Non-empty list of files or directories to commit. Paths are resolved against the active workdir; paths escaping it are rejected.",
				Items: &PropertySchema{
					Type:        "string",
					Description: "File or directory path inside the active workdir",
				},
			},
		},
		Required:             []string{"paths"},
		AdditionalProperties: false,
	}
}

func (t *CommitChangesTool) Execute(params map[string]any) (*Result, error) {
	paths, err := t.canonicalPaths(params)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	executable, err := t.resolveExecutable()
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	args := buildCommitChangesArgv(executable, paths)

	ctx, cancel := t.execContext()
	defer cancel()

	// Direct exec, no shell: argv[0] plus arguments only.
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	if dir := strings.TrimSpace(t.workDir); dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = mergeEnv(cmd.Env, t.env)
	stdout := newLimitedBuffer(t.outputLimit())
	stderr := newLimitedBuffer(t.outputLimit())
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	runErr := cmd.Run()

	if ctx.Err() != nil {
		return &Result{
			Success: false,
			Error:   "commit_changes timed out",
		}, nil
	}

	data := map[string]any{
		"paths":  paths,
		"stdout": stdout.String(),
		"stderr": stderr.String(),
	}
	if stdout.Truncated() {
		data["stdout_truncated"] = true
	}
	if stderr.Truncated() {
		data["stderr_truncated"] = true
	}

	if runErr != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		message := fmt.Sprintf("buckley commit failed: %v", runErr)
		if detail != "" {
			message += ": " + detail
		}
		data["head"] = ""
		return &Result{Success: false, Error: message, Data: data}, nil
	}

	head := t.currentHead(ctx)
	data["head"] = head

	result := &Result{Success: true, Data: data}
	if stdout.Truncated() || stderr.Truncated() {
		result.ShouldAbridge = true
		result.DisplayData = data
	}
	return result, nil
}

// defaultCommitMaxOutputBytes bounds each of stdout and stderr when no
// registry maximum is configured (SetMaxOutputBytes(0) means "unset", and
// newLimitedBuffer treats zero as unbounded). Without this floor a commit
// runtime that keeps talking could balloon the result payload indefinitely.
// It matches the 100KB per-stream default used by the TUI and headless
// frontends; an explicitly configured maximum still wins when stricter.
const defaultCommitMaxOutputBytes = 100_000

// outputLimit returns the effective per-stream capture bound: the stricter
// configured maximum when one is set, otherwise the finite package default.
func (t *CommitChangesTool) outputLimit() int {
	if t.maxOutputBytes > 0 {
		return t.maxOutputBytes
	}
	return defaultCommitMaxOutputBytes
}

// commitBaseDir returns the absolute directory every commit path scope is
// validated against and made relative to: the configured workdir when set,
// otherwise the process working directory (os.Getwd). Binding the unset case
// to os.Getwd keeps the safety boundary meaningful; without it, an unset
// workdir would let arbitrary absolute paths through unvalidated.
func (t *CommitChangesTool) commitBaseDir() (string, error) {
	if dir := strings.TrimSpace(t.workDir); dir != "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return "", fmt.Errorf("invalid workdir: %w", err)
		}
		return filepath.Clean(abs), nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	return filepath.Clean(cwd), nil
}

// canonicalPaths extracts, validates, canonicalizes, and deduplicates the
// paths parameter. It requires an explicit non-empty array of non-empty
// strings, rejects any entry that escapes the active workdir boundary, and
// deduplicates by absolute identity. The returned scopes are relative to the
// base directory: cmd/buckley's stagedFilesMatchingPaths compares --paths
// entries against repository-relative staged names, so absolute paths would
// never match anything.
func (t *CommitChangesTool) canonicalPaths(params map[string]any) ([]string, error) {
	rawList, ok := params["paths"]
	if !ok || rawList == nil {
		return nil, fmt.Errorf("paths parameter required: provide a non-empty array of paths to commit")
	}

	var entries []string
	switch typed := rawList.(type) {
	case []any:
		for _, item := range typed {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("paths must be an array of strings")
			}
			entries = append(entries, s)
		}
	case []string:
		entries = append(entries, typed...)
	default:
		return nil, fmt.Errorf("paths must be an array of strings")
	}

	if len(entries) == 0 {
		return nil, fmt.Errorf("paths must be a non-empty array of paths to commit")
	}

	base, err := t.commitBaseDir()
	if err != nil {
		return nil, err
	}
	resolvedBase := evalSymlinksFallback(base)

	canonical := make([]string, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, raw := range entries {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return nil, fmt.Errorf("paths entries cannot be empty")
		}
		candidate := raw
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(base, candidate)
		}
		candidate = filepath.Clean(candidate)
		if !isWithinDir(base, candidate) {
			return nil, fmt.Errorf("path %q escapes workdir", raw)
		}
		// Harden against symlink escapes.
		if !isWithinDir(resolvedBase, evalSymlinksFallbackForTarget(candidate)) {
			return nil, fmt.Errorf("path %q escapes workdir via symlink", raw)
		}
		// Deduplicate by absolute identity so "./a.go", "sub/../a.go",
		// and their absolute spellings collapse into one scope.
		if seen[candidate] {
			continue
		}
		seen[candidate] = true
		rel, err := filepath.Rel(base, candidate)
		if err != nil {
			return nil, fmt.Errorf("invalid path %q: %w", raw, err)
		}
		canonical = append(canonical, rel)
	}
	return canonical, nil
}

// resolveExecutable finds the current Buckley executable: an explicit test
// override first, then the running binary via os.Executable (robust even when
// os.Args[0] is a bare name or the binary was launched through PATH), then a
// PATH lookup as a last resort.
func (t *CommitChangesTool) resolveExecutable() (string, error) {
	if exe := strings.TrimSpace(t.Executable); exe != "" {
		return exe, nil
	}
	if self, err := os.Executable(); err == nil && strings.TrimSpace(self) != "" {
		return self, nil
	}
	exe, err := exec.LookPath("buckley")
	if err != nil {
		return "", fmt.Errorf("buckley executable not found. Ensure buckley is built or in PATH.")
	}
	return exe, nil
}

// buildCommitChangesArgv builds the exact argv used to run the governed
// commit. Flags are fixed and safe: confirmation is skipped, pushing is
// disabled, output is minimized, the scoped-paths guard is on, every path is
// passed as a repeatable --paths scope AND as a trailing positional after --
// so it is staged by the same run. Paths must already be workdir-relative:
// cmd/buckley matches them against repository-relative staged names. No
// --backend/--model/--timeout override is ever emitted: the configured
// default commit model stays authoritative.
func buildCommitChangesArgv(executable string, paths []string) []string {
	args := []string{
		executable,
		"commit",
		"--yes",
		"--push=false",
		"--minimal-output",
		"--exclusive",
	}
	for _, p := range paths {
		args = append(args, "--paths", p)
	}
	args = append(args, "--")
	args = append(args, paths...)
	return args
}

// currentHead resolves HEAD in the tool's workdir after a successful commit.
// Failures yield an empty string rather than failing an already-created commit.
func (t *CommitChangesTool) currentHead(ctx context.Context) string {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD") //nolint:gosec // fixed git argv, no user input
	if dir := strings.TrimSpace(t.workDir); dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = mergeEnv(cmd.Env, t.env)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
