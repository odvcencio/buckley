package reviewsandbox

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// DefaultLocalParallelism caps concurrent local verification commands even
// when no remote wrapper is configured. A crowded host serializes far more
// than it parallelizes: four `go test` processes each defaulting to
// GOMAXPROCS host CPUs queue for the same cores under contention, so package
// compiles that take milliseconds in isolation can take minutes. Capping at
// a quarter of the host's CPUs (minimum 1, maximum 4) keeps a handful of
// concurrent verification commands from each trying to claim the whole
// machine.
func DefaultLocalParallelism() int {
	return min(4, max(1, runtime.NumCPU()/4))
}

// BatchTarget is one Go package directory (relative to the immutable review
// snapshot root, "." for the module root) to verify in a batched
// `go test -json` run.
type BatchTarget struct {
	// Path is the repository-relative directory, matching Result.Path and
	// the "path" parameter of a single run_verification call.
	Path string
}

// batchDefaultTimeout is used only when the caller supplies zero; batched
// runs cover many packages in one remote invocation, so their timeout must
// scale with the batch rather than reuse the single-package defaultTimeout.
const batchDefaultTimeout = 10 * time.Minute

// batchDefaultMaxOutput is used only when the caller supplies zero.
// `go test -json` emits one JSON "output" event per line of ordinary test
// output (every "=== RUN"/"--- PASS", not just failures), and a batch
// combines that for every package in the chunk into one buffer. A large
// package's terminal pass/fail/skip event can be scheduled arbitrarily late
// in the interleaved stream; if the buffer fills before that event arrives,
// parseGoTestJSONStream never sees a terminal event for that package and
// VerifyGoTestBatch correctly (but needlessly) grades it UNAVAILABLE even
// though it actually passed. Observed in production: a chunk containing
// this repository's largest package (cmd/buckley, hundreds of tests)
// alongside five smaller ones filled a 1MiB buffer before cmd/buckley's
// terminal event arrived, while its smaller batch-mates completed and
// reported correctly. This raw internal buffer is independent of what
// reaches the review model: BuildVerificationToolResult still abridges
// large output down to a short tail before encoding it for the model, so
// raising this cap only affects harness-side memory, not context budget.
const batchDefaultMaxOutput = 32 * 1024 * 1024

// VerifyGoTestBatch runs `go test -json` once against every target in the
// same wrapper invocation, in place of one `go test` process per package.
// It requires a configured wrapper (see SetWrapper): batching exists to
// collapse many short-lived remote invocations -- each of which pays a full
// compile-queueing wait on a crowded remote host -- into one, so it is not
// offered for the local bwrap/Codex sandbox path.
//
// A wrapper or transport failure (a non-test exit code, a launch failure, a
// timeout, or cancellation) grades every target in the batch UNAVAILABLE; it
// never falls through to CONFIRMED_FAIL, matching the single-call wrapper
// path in Verify. A target whose package never appears in the parsed event
// stream (for example a mid-run connection drop) is graded UNAVAILABLE
// individually even when its siblings completed normally.
func (e *Executor) VerifyGoTestBatch(parent context.Context, snapshotRoot string, targets []BatchTarget, timeout time.Duration, maxOutput int) (map[string]Result, error) {
	if e == nil {
		return nil, fmt.Errorf("review verification executor is unavailable")
	}
	if len(e.wrapper) == 0 {
		return nil, fmt.Errorf("batched go test verification requires a configured remote wrapper")
	}
	if len(targets) == 0 {
		return map[string]Result{}, nil
	}
	if parent == nil {
		parent = context.Background()
	}
	root, err := filepath.Abs(strings.TrimSpace(snapshotRoot))
	if err != nil || strings.TrimSpace(snapshotRoot) == "" {
		return nil, fmt.Errorf("immutable review snapshot root is required")
	}
	modulePath, err := readGoModuleImportPath(root)
	if err != nil {
		return nil, fmt.Errorf("resolve Go module path for batched verification: %w", err)
	}

	if timeout <= 0 {
		timeout = batchDefaultTimeout
	}
	if maxOutput <= 0 {
		maxOutput = batchDefaultMaxOutput
	}

	byImportPath := make(map[string]string, len(targets))
	argv := []string{"go", "test", "-json", "-count=1"}
	for _, target := range targets {
		cleanPath := filepath.ToSlash(filepath.Clean(strings.TrimSpace(target.Path)))
		if cleanPath == "" {
			cleanPath = "."
		}
		byImportPath[goImportPathForTarget(modulePath, cleanPath)] = cleanPath
		argv = append(argv, goPackagePatternForTarget(cleanPath))
	}

	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	args := append(append([]string(nil), e.wrapper[1:]...), root)
	args = append(args, argv...)
	output, runErr := e.run(ctx, commandInvocation{Name: e.wrapper[0], Args: args}, maxOutput)

	if ok, reason := trustedWrapperBatchExitCode(timeout, output, runErr, ctx.Err()); !ok {
		results := make(map[string]Result, len(targets))
		for _, target := range targets {
			cleanPath := filepath.ToSlash(filepath.Clean(strings.TrimSpace(target.Path)))
			if cleanPath == "" {
				cleanPath = "."
			}
			results[cleanPath] = Result{
				Kind:      KindTest,
				Language:  LanguageGo,
				Path:      cleanPath,
				Command:   "go",
				Argv:      argv,
				ExitCode:  -1,
				Status:    StatusUnavailable,
				Stdout:    output.Stdout,
				Stderr:    output.Stderr,
				Duration:  output.Duration,
				Truncated: output.Truncated,
				Error:     reason,
			}
		}
		return results, nil
	}

	parsed := parseGoTestJSONStream(output.Stdout)
	results := make(map[string]Result, len(targets))
	for importPath, path := range byImportPath {
		summary, seen := parsed[importPath]
		result := Result{
			Kind:      KindTest,
			Language:  LanguageGo,
			Path:      path,
			Command:   "go",
			Argv:      argv,
			Duration:  output.Duration,
			Truncated: output.Truncated,
		}
		switch {
		case !seen || !summary.terminal:
			result.ExitCode = -1
			result.Status = StatusUnavailable
			result.Stdout = output.Stdout
			result.Stderr = output.Stderr
			if seen {
				result.Stdout = summary.output.String()
			}
			result.Error = fmt.Sprintf(
				"batched verification produced no terminal result for package %q; the remote run may have stopped early", importPath)
		case summary.skipped:
			result.ExitCode = 0
			result.Status = StatusPass
			result.NoTestFiles = true
			result.Stdout = summary.output.String()
			result.Duration = summary.elapsed
		case summary.failed:
			result.ExitCode = 1
			result.Status = StatusFail
			result.Stdout = summary.output.String()
			result.Duration = summary.elapsed
			result.Error = "verification command failed"
		default:
			result.ExitCode = 0
			result.Status = StatusPass
			result.Stdout = summary.output.String()
			result.Duration = summary.elapsed
		}
		results[path] = result
	}
	return results, nil
}

// trustedWrapperBatchExitCode mirrors classifyVerificationRun's
// timeout/cancellation/launch-failure/untrusted-exit-code handling for a
// batched invocation, where there is no single Result to attach the
// classification to yet.
func trustedWrapperBatchExitCode(timeout time.Duration, output commandOutput, runErr, contextErr error) (bool, string) {
	if runErr != nil {
		if errors.Is(contextErr, context.DeadlineExceeded) || errors.Is(runErr, context.DeadlineExceeded) {
			return false, fmt.Sprintf("batched verification timed out after %s", timeout)
		}
		if errors.Is(contextErr, context.Canceled) || errors.Is(runErr, context.Canceled) {
			return false, "batched verification canceled before completion"
		}
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			code := exitErr.ExitCode()
			if !trustedWrapperExitCode(LanguageGo, code) {
				return false, wrapperInfrastructureFailure("remote verification wrapper", code)
			}
			return true, ""
		}
		return false, fmt.Sprintf("remote verification wrapper failed to launch: %v", runErr)
	}
	if !trustedWrapperExitCode(LanguageGo, output.ExitCode) {
		return false, wrapperInfrastructureFailure("remote verification wrapper", output.ExitCode)
	}
	return true, ""
}

// goPackageSummary accumulates one package's captured output and elapsed
// time from a `go test -json` event stream. terminal is set only by a
// package-level pass/fail/skip event (Test == ""); a package that
// accumulated "output" events but never reached a terminal event (for
// example an interrupted or truncated stream) must not be reported as a
// pass on that basis alone.
type goPackageSummary struct {
	output   strings.Builder
	elapsed  time.Duration
	failed   bool
	skipped  bool
	terminal bool
}

// goTestJSONEvent is the shape of one line from `go test -json`, also known
// as `go tool test2json` output.
type goTestJSONEvent struct {
	Action  string  `json:"Action"`
	Package string  `json:"Package"`
	Test    string  `json:"Test"`
	Output  string  `json:"Output"`
	Elapsed float64 `json:"Elapsed"`
}

// parseGoTestJSONStream classifies each package that reports a package-level
// pass/fail/skip event (Test == ""). Malformed or unrecognized lines are
// skipped rather than failing the whole batch: partial or truncated output
// from a wrapper failure still lets already-completed packages grade
// normally, while packages with no recognized event fall back to
// UNAVAILABLE in the caller.
func parseGoTestJSONStream(stdout string) map[string]*goPackageSummary {
	packages := make(map[string]*goPackageSummary)
	get := func(pkg string) *goPackageSummary {
		summary, ok := packages[pkg]
		if !ok {
			summary = &goPackageSummary{}
			packages[pkg] = summary
		}
		return summary
	}

	scanner := bufio.NewScanner(strings.NewReader(stdout))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var event goTestJSONEvent
		if err := json.Unmarshal(line, &event); err != nil || strings.TrimSpace(event.Package) == "" {
			continue
		}
		summary := get(event.Package)
		switch event.Action {
		case "output":
			summary.output.WriteString(event.Output)
		case "pass":
			if event.Test == "" {
				summary.terminal = true
				summary.elapsed = time.Duration(event.Elapsed * float64(time.Second))
			}
		case "fail":
			if event.Test == "" {
				summary.terminal = true
				summary.failed = true
				summary.elapsed = time.Duration(event.Elapsed * float64(time.Second))
			}
		case "skip":
			if event.Test == "" {
				summary.terminal = true
				summary.skipped = true
				summary.elapsed = time.Duration(event.Elapsed * float64(time.Second))
			}
		}
	}
	return packages
}

// readGoModuleImportPath reads the `module` directive from go.mod at root.
// Batched verification needs the module's import path to map `go test
// -json`'s reported package (a full import path) back to the
// repository-relative directory the harness requested.
func readGoModuleImportPath(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("read go.mod: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "module ") {
			continue
		}
		module := strings.TrimSpace(strings.TrimPrefix(line, "module"))
		module = strings.Trim(module, "\"")
		if module == "" {
			return "", fmt.Errorf("go.mod module directive is empty")
		}
		return module, nil
	}
	return "", fmt.Errorf("go.mod has no module directive")
}

func goImportPathForTarget(modulePath, path string) string {
	if path == "" || path == "." {
		return modulePath
	}
	return modulePath + "/" + filepath.ToSlash(path)
}

func goPackagePatternForTarget(path string) string {
	if path == "" || path == "." {
		return "."
	}
	return "./" + filepath.ToSlash(path)
}
