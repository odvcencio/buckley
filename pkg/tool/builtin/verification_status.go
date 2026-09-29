package builtin

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// VerificationStatusKey names the Result.Data entry a verification tool sets
// when its check could not run at all. Such a result holds no pass or fail
// evidence about the code, so the completion contract must not count it as
// either: a refused call is not a failed test.
const VerificationStatusKey = "verification_status"

// VerificationReasonKey holds the plain-language reason next to
// VerificationStatusKey.
const VerificationReasonKey = "verification_reason"

const (
	// VerificationStatusRejected marks a call refused before launch, for
	// example a command outside the accepted list.
	VerificationStatusRejected = "rejected"
	// VerificationStatusUnavailable marks a check with nothing to run, for
	// example a workspace with no test framework or a missing toolchain.
	VerificationStatusUnavailable = "unavailable"
)

// verificationNotRun builds the Data entries that mark a result as unavailable.
func verificationNotRun(status, reason string) map[string]any {
	return map[string]any{
		VerificationStatusKey: status,
		VerificationReasonKey: reason,
	}
}

// VerificationUnavailableReason returns a plain reason when a failed
// verification-class result carries no evidence about the code, and "" when it
// is a pass or a real failure. A tool marks its own result with
// VerificationStatusKey; for the rest, well-known "nothing to run" messages and
// exit codes from the go, cargo, npm, make, and pytest tools decide.
func VerificationUnavailableReason(result *Result) string {
	if result == nil || result.Success {
		return ""
	}
	if status, _ := result.Data[VerificationStatusKey].(string); status == VerificationStatusRejected || status == VerificationStatusUnavailable {
		if reason, _ := result.Data[VerificationReasonKey].(string); strings.TrimSpace(reason) != "" {
			return reason
		}
		return "the check did not run"
	}
	exitCode, hasExit := verificationExitCode(result)
	if hasExit && (exitCode == 126 || exitCode == 127) {
		return "the command was not found or could not be started on this machine"
	}
	if strings.HasPrefix(result.Error, "sandbox blocked command") {
		return "the sandbox blocked the command"
	}
	family := verificationFamily(result)
	if family == "" {
		return ""
	}
	text := verificationResultText(result)
	// Output that shows tests ran is a real result, even when a test prints
	// one of the messages below. A package that failed to load or set up ran
	// nothing, so its FAIL line does not count.
	if verificationRanTests.MatchString(goSetupFailure.ReplaceAllString(text, "")) {
		return ""
	}
	// npm reports a failed script with a lifecycle error. A script that ran and
	// failed is a real failure, whatever else it printed.
	if family == familyNpm && npmScriptRan.MatchString(text) {
		return ""
	}
	for _, marker := range verificationUnavailableMarkers {
		if marker.family == family && marker.pattern.MatchString(text) {
			return marker.reason
		}
	}
	if hasExit && exitCode == 5 && family == familyPytest && pytestNoTests.MatchString(text) {
		return "pytest found no tests to run"
	}
	return ""
}

// verificationRanTests matches the lines that go, jest, cargo, and pytest print
// only after they ran tests.
var verificationRanTests = regexp.MustCompile(`(?m)^(--- (FAIL|PASS|SKIP)|=== (RUN|PAUSE|CONT)|FAIL[ \t]+\S|ok\s+\S|PASS$|Test Suites:|Tests:\s|test result:|running [1-9][0-9]* tests?|collected [1-9][0-9]* items?|=*\s*[0-9]+ (failed|passed|errors?)\b)`)

// goSetupFailure matches the FAIL line go test prints for a pattern or package
// that could not be loaded.
var goSetupFailure = regexp.MustCompile(`(?m)^FAIL\s[^\n]*\[setup failed\]$`)

// npmScriptRan matches npm's own report that a script ran and failed.
var npmScriptRan = regexp.MustCompile(`(?im)^npm (?:error|err!)\s+(?:code\s+)?elifecycle\b|lifecycle script`)

// pytestNoTests matches pytest's own report that it collected nothing.
var pytestNoTests = regexp.MustCompile(`(?m)^(?:=+ )?(?:no tests ran\b|collected 0 items)`)

// Tool families a verification command belongs to. A marker below applies only
// to its own family, so a pytest test that prints a go message is still a real
// failure.
const (
	familyGo     = "go"
	familyCargo  = "cargo"
	familyNpm    = "npm"
	familyMake   = "make"
	familyPytest = "pytest"
)

// verificationUnavailableMarkers are the diagnostics that go, cargo, npm, make,
// and pytest print when the workspace has nothing for them to run. Each pattern
// is anchored to the start of a line and to the tool's own diagnostic format, so
// a test or script that prints the same words is still a real failure.
var verificationUnavailableMarkers = []struct {
	family  string
	pattern *regexp.Regexp
	reason  string
}{
	{familyGo, regexp.MustCompile(`(?m)^go: (?:go\.mod file not found in current directory or any parent directory|cannot find main module)|pattern \S+: directory prefix \S+ does not contain main module or its selected dependencies`), "there is no go.mod in the workspace"},
	{familyGo, regexp.MustCompile(`(?m)^go: go\.mod requires go[^\n]*\(running go [^;\n]*; GOTOOLCHAIN=local\)`), "the installed Go toolchain is older than go.mod requires"},
	{familyCargo, regexp.MustCompile("(?m)^error: could not find `Cargo\\.toml` in "), "there is no Cargo.toml in the workspace"},
	{familyNpm, regexp.MustCompile(`(?im)^npm (?:error|err!)\s+enoent\b[^\n]*package\.json`), "there is no package.json in the workspace"},
	{familyNpm, regexp.MustCompile(`(?im)^npm (?:error|err!)\s+missing script\b`), "package.json has no script for this check"},
	{familyNpm, regexp.MustCompile(`(?m)^No tests found, exiting with code`), "jest found no tests to run"},
	{familyNpm, regexp.MustCompile(`(?m)^> echo "Error: no test specified" && exit 1$`), "package.json has no test script"},
	{familyMake, regexp.MustCompile(`(?m)^make: \*\*\* No targets specified and no makefile found`), "there is no Makefile in the workspace"},
	{familyMake, regexp.MustCompile("(?m)^make: \\*\\*\\* No rule to make target ['`\"](?:test|check|build|vet|lint)['`\"]\\.\\s+Stop\\."), "the Makefile has no such target"},
	{familyPytest, regexp.MustCompile(`(?m)^\S*python\S*: No module named pytest\b`), "pytest is not installed"},
}

// verificationFamily names the tool family behind a verification result: from
// the framework run_tests reports, or from the first word of the command after
// its wrappers. It returns "" for anything else, and output is never
// classified for those.
func verificationFamily(result *Result) string {
	switch framework, _ := result.Data["framework"].(string); framework {
	case "go":
		return familyGo
	case "jest":
		return familyNpm
	case "pytest":
		return familyPytest
	case "cargo":
		return familyCargo
	}
	command, _ := result.Data["command"].(string)
	fields := stripVerificationWrappers(strings.Fields(command))
	if len(fields) == 0 {
		return ""
	}
	switch fields[0] {
	case "go", "golangci-lint", "staticcheck":
		return familyGo
	case "npm":
		return familyNpm
	case "cargo":
		return familyCargo
	case "pytest", "python", "python3":
		return familyPytest
	case "make":
		return familyMake
	}
	return ""
}

func verificationExitCode(result *Result) (int, bool) {
	switch code := result.Data["exit_code"].(type) {
	case int:
		return code, true
	case int64:
		return int(code), true
	case float64:
		return int(code), true
	}
	return 0, false
}

func verificationResultText(result *Result) string {
	var text strings.Builder
	text.WriteString(result.Error)
	for _, key := range []string{"stdout", "stderr", "output"} {
		if value, ok := result.Data[key].(string); ok {
			text.WriteByte('\n')
			text.WriteString(value)
		}
	}
	return text.String()
}

const (
	surfaceMaxDepth   = 4
	surfaceMaxEntries = 4000
	surfaceMaxParents = 6
)

// HasVerificationSurface reports whether the workspace at root holds anything
// the accepted verification commands could run: a module or project file, a
// Makefile, or a test file, within a few directory levels (parents up to the
// repository root count too, because go, npm, and cargo look upward). When it
// finds none it returns a plain reason. A large tree that outruns the search
// budget counts as having a surface, so this never ends a run wrongly.
func HasVerificationSurface(root string) (bool, string) {
	root = strings.TrimSpace(root)
	if root == "" {
		return true, ""
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return true, ""
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		return true, ""
	}
	if hasProjectMarker(abs) {
		return true, ""
	}
	found, exhausted := false, false
	entries := 0
	walkErr := filepath.WalkDir(abs, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		entries++
		if entries > surfaceMaxEntries {
			exhausted = true
			return fs.SkipAll
		}
		if entry.IsDir() {
			if path == abs {
				return nil
			}
			if skipSurfaceDir(entry.Name()) {
				return fs.SkipDir
			}
			rel, relErr := filepath.Rel(abs, path)
			if relErr != nil || strings.Count(rel, string(filepath.Separator)) >= surfaceMaxDepth {
				return fs.SkipDir
			}
			return nil
		}
		if isVerificationSurfaceFile(entry.Name()) {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	if walkErr != nil || found || exhausted {
		return true, ""
	}
	dir := abs
	for range surfaceMaxParents {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
		if hasProjectMarker(dir) {
			return true, ""
		}
	}
	return false, fmt.Sprintf("no go.mod, package.json, Cargo.toml, Python project file, Makefile, or test file was found within %d directory levels of the workspace", surfaceMaxDepth)
}

func skipSurfaceDir(name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	switch name {
	case "node_modules", "vendor", "__pycache__", "target", "venv":
		return true
	}
	return false
}

func hasProjectMarker(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() && isProjectMarkerFile(entry.Name()) {
			return true
		}
	}
	return false
}

func isProjectMarkerFile(name string) bool {
	switch name {
	case "go.mod", "package.json", "Cargo.toml", "pyproject.toml", "setup.py", "setup.cfg",
		"pytest.ini", "tox.ini", "conftest.py", "Makefile", "makefile", "GNUmakefile":
		return true
	}
	return false
}

func isVerificationSurfaceFile(name string) bool {
	if isProjectMarkerFile(name) {
		return true
	}
	return strings.HasSuffix(name, "_test.go") ||
		(strings.HasPrefix(name, "test_") && strings.HasSuffix(name, ".py")) ||
		strings.HasSuffix(name, "_test.py") ||
		strings.Contains(name, ".test.") || strings.Contains(name, ".spec.")
}
