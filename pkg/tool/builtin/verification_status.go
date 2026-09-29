package builtin

import (
	"encoding/json"
	"io"
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
	if strings.HasPrefix(result.Error, "sandbox blocked command") {
		return "the sandbox blocked the command"
	}
	exitCode, hasExit := verificationExitCode(result)
	text := verificationResultText(result)
	// Output that shows tests ran is a real result, whatever the exit code and
	// even when a test prints one of the messages below. A package that failed
	// to load or set up ran nothing, so its FAIL line does not count.
	if verificationRanTests.MatchString(goSetupFailure.ReplaceAllString(text, "")) {
		return ""
	}
	// npm reports a failed script with a lifecycle error. A script that ran and
	// failed is a real failure, whatever else it printed.
	family := verificationFamily(result)
	if family == familyNpm && npmScriptRan.MatchString(text) {
		return ""
	}
	// Exit 126 and 127 mean the shell could not start the command, and its
	// message says so. Any other output is a check that ran.
	if hasExit && (exitCode == 126 || exitCode == 127) && shellStartFailure.MatchString(text) {
		return "the command was not found or could not be started on this machine"
	}
	if family == "" {
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

// shellStartFailure matches the messages a shell prints when it cannot start a
// command.
var shellStartFailure = regexp.MustCompile(`(?im)command not found|: not found\b|no such file or directory|permission denied|cannot execute`)

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
	// A text file larger than surfaceMaxTextBytes, or more text than
	// surfaceTextBudget across the scan, is not read; the scan then counts it as
	// unknown.
	surfaceMaxTextBytes = 64 << 10
	surfaceTextBudget   = 8 << 20
)

// HasVerificationSurface reports whether the workspace at root holds anything
// the accepted verification commands could run. It returns false, with a plain
// reason, only when it searched the whole workspace and every file it found is
// a document, an image, plain data, or a project file that defines no test,
// build, lint, or check. Any other file counts, and so does any part it could
// not search: a subtree below the depth bound, an unreadable directory, a
// symlinked directory, or a tree that outruns the entry budget. Parent
// directories up to the repository root count for project files, because go,
// npm, and cargo look upward. This never ends a run on a guess.
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
	found, unknown := false, false
	entries, textBudget := 0, surfaceTextBudget
	walkErr := filepath.WalkDir(abs, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if entry != nil && entry.IsDir() {
				unknown = true
				return fs.SkipDir
			}
			return nil
		}
		entries++
		if entries > surfaceMaxEntries {
			unknown = true
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
				unknown = true
				return fs.SkipDir
			}
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			// A symlink to a file falls through to the name checks below.
			if info, statErr := os.Stat(path); statErr != nil || info.IsDir() {
				unknown = true
				return nil
			}
		}
		if fileOffersCheck(path, entry.Name()) {
			found = true
			return fs.SkipAll
		}
		if !isProjectFileName(entry.Name()) && isTextLike(entry.Name()) {
			// pytest --doctest-glob runs the examples in any text file, so a text
			// file with an example is something an accepted check can run.
			data, ok := readFileWithin(path, surfaceMaxTextBytes)
			textBudget -= len(data)
			switch {
			case !ok || textBudget < 0:
				unknown = true
				if textBudget < 0 {
					return fs.SkipAll
				}
			case doctestExample.Match(data):
				found = true
				return fs.SkipAll
			}
		}
		return nil
	})
	if walkErr != nil || found || unknown {
		return true, ""
	}
	// go, npm, and cargo look for their manifests in every parent directory, up
	// to the repository root or the filesystem root, so the scan does too.
	dir := abs
	for {
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
	return false, "found only documents, data files, and project files that define no test, build, lint, or check"
}

// skipSurfaceDir reports whether the scan skips a directory by name. It skips
// only .git: git's own data is not part of the workspace, and the fingerprint
// that decides whether the workspace changed leaves it out too. Every other
// directory is searched, hidden ones and tool caches included, because an
// accepted check can be pointed at any path (pytest .checks/test_change.py, or a
// test file left in .pytest_cache). A large directory such as node_modules or
// vendor runs the scan out of entries, which counts as an unknown surface.
func skipSurfaceDir(name string) bool {
	return name == ".git"
}

func hasProjectMarker(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() && projectFileOffersCheck(filepath.Join(dir, entry.Name()), entry.Name()) {
			return true
		}
	}
	return false
}

// fileOffersCheck reports whether the file at path could give an accepted
// verification command something to run. A project file decides by its content.
// Any other file counts unless it is plainly a document, an image, or data:
// go vet main.go, pytest checks.py, and make test with a test.c beside it all run
// without a manifest, so an unknown kind of file is a check that might run.
func fileOffersCheck(path, name string) bool {
	if isProjectFileName(name) {
		return projectFileOffersCheck(path, name)
	}
	return !isInertFile(name)
}

// isProjectFileName reports whether name is a file whose content decides
// whether it offers a check.
func isProjectFileName(name string) bool {
	switch name {
	case "go.mod", "Cargo.toml", "pytest.ini", "package.json", "pyproject.toml", "setup.cfg", "tox.ini":
		return true
	}
	return isMakefileName(name)
}

// inertExtensions are the file kinds no accepted verification command runs:
// documents, images, media, archives, fonts, and plain data.
var inertExtensions = map[string]bool{
	".txt": true, ".md": true, ".markdown": true, ".rst": true, ".adoc": true, ".org": true, ".tex": true,
	".log": true, ".meta": true, ".csv": true, ".tsv": true,
	".json": true, ".jsonl": true, ".yaml": true, ".yml": true, ".toml": true, ".xml": true,
	".html": true, ".htm": true, ".css": true, ".lock": true, ".sum": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".svg": true, ".ico": true, ".webp": true, ".bmp": true,
	".pdf": true, ".docx": true, ".xlsx": true, ".pptx": true, ".odt": true,
	".zip": true, ".tar": true, ".gz": true, ".tgz": true,
	".mp3": true, ".mp4": true, ".wav": true,
	".woff": true, ".woff2": true, ".ttf": true, ".eot": true,
}

// inertDotfiles are the hidden files that hold only settings, never code an
// accepted check can run.
var inertDotfiles = map[string]bool{
	".gitignore": true, ".gitattributes": true, ".gitmodules": true, ".gitkeep": true, ".keep": true,
	".dockerignore": true, ".editorconfig": true, ".env": true, ".mailmap": true, ".ds_store": true,
	".npmrc": true, ".nvmrc": true, ".prettierignore": true, ".eslintignore": true,
	".prettierrc": true, ".eslintrc": true, ".babelrc": true, ".stylelintrc": true, ".browserslistrc": true,
	".python-version": true, ".node-version": true, ".ruby-version": true, ".tool-versions": true,
}

// isInertFile reports whether name is a file no accepted check runs as code: a
// settings dotfile, an all-capitals file such as README or LICENSE, or a file of
// an inert kind. A hidden file is otherwise judged by its name without the
// leading dot, because pytest --import-mode=importlib runs .test_change.py
// (TestHiddenFilesCanBeRunByAcceptedCommands runs it).
func isInertFile(name string) bool {
	if strings.HasPrefix(name, ".") {
		if inertDotfiles[strings.ToLower(name)] {
			return true
		}
		name = strings.TrimPrefix(name, ".")
		if name == "" {
			return true
		}
	}
	ext := filepath.Ext(name)
	if ext == "" {
		return name == strings.ToUpper(name)
	}
	return inertExtensions[strings.ToLower(ext)]
}

// binaryExtensions are the inert kinds that hold no text, so no doctest.
var binaryExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".ico": true, ".webp": true, ".bmp": true,
	".pdf": true, ".docx": true, ".xlsx": true, ".pptx": true, ".odt": true,
	".zip": true, ".tar": true, ".gz": true, ".tgz": true,
	".mp3": true, ".mp4": true, ".wav": true,
	".woff": true, ".woff2": true, ".ttf": true, ".eot": true,
}

// isTextLike reports whether an inert file may hold text: pytest --doctest-glob
// takes any file name, so any text file with a doctest example can be run.
func isTextLike(name string) bool {
	return !binaryExtensions[strings.ToLower(filepath.Ext(name))]
}

// projectFileOffersCheck reports whether a project file gives an accepted
// verification command something to run. It counts only when it says so: a
// package.json needs a test, build, or lint script, a Makefile needs an
// accepted target, and Python configuration needs to mention pytest. A file the
// scan cannot read or parse counts as offering a check. The tools look for
// these files in parent directories, so the scan does too. Test files do not
// propagate that way, so they are not project files.
func projectFileOffersCheck(path, name string) bool {
	switch {
	case isMakefileName(name):
		return makefileOffersCheck(path)
	case name == "package.json":
		return packageJSONOffersCheck(path)
	case name == "pyproject.toml" || name == "setup.cfg" || name == "tox.ini":
		return fileMentions(path, "pytest")
	case name == "go.mod" || name == "Cargo.toml" || name == "pytest.ini":
		return true
	}
	return false
}

// readSmallFile reads path in full when it is at most maxMakefileBytes long.
func readSmallFile(path string) ([]byte, bool) {
	return readFileWithin(path, maxMakefileBytes)
}

// readFileWithin reads path in full when it is at most limit bytes long. The
// second result is false when the file is larger or cannot be read.
func readFileWithin(path string, limit int) ([]byte, bool) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil || len(data) > limit {
		return nil, false
	}
	return data, true
}

// doctestExample matches the prompt of a Python doctest example.
var doctestExample = regexp.MustCompile(`(?m)^[ \t]*>>> `)

// fileMentions reports whether the file at path contains word, ignoring case. A
// file it cannot read in full counts as mentioning it.
func fileMentions(path, word string) bool {
	data, ok := readSmallFile(path)
	return !ok || strings.Contains(strings.ToLower(string(data)), word)
}

// npmInitTestScript is the placeholder test script that npm init writes.
const npmInitTestScript = `echo "Error: no test specified" && exit 1`

// packageJSONOffersCheck reports whether the package.json at path has a test,
// build, or lint script, the scripts npm test and npm run build|lint can run.
// The placeholder test script that npm init writes does not count. A file that
// does not parse counts as offering a check.
func packageJSONOffersCheck(path string) bool {
	data, ok := readSmallFile(path)
	if !ok {
		return true
	}
	var manifest struct {
		Scripts map[string]json.RawMessage `json:"scripts"`
	}
	if json.Unmarshal(data, &manifest) != nil {
		return true
	}
	for name, raw := range manifest.Scripts {
		switch name {
		case "build", "lint":
			return true
		case "test":
			var script string
			if json.Unmarshal(raw, &script) != nil || strings.TrimSpace(script) != npmInitTestScript {
				return true
			}
		}
	}
	return false
}

func isMakefileName(name string) bool {
	return name == "Makefile" || name == "makefile" || name == "GNUmakefile"
}

// maxMakefileBytes bounds how much of a Makefile the scan reads.
const maxMakefileBytes = 1 << 20

// makefileOffersCheck reports whether the Makefile at path could run one of the
// accepted make targets: it defines test, check, build, vet, or lint, or it
// holds something the scan cannot resolve (an include, a rule that matches any
// target, a target named by a variable) or cannot be read in full. Only a
// Makefile that was read completely and defines none of them offers no check.
func makefileOffersCheck(path string) bool {
	data, ok := readSmallFile(path)
	if !ok {
		return true
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || line[0] == '\t' || line[0] == '#' {
			continue
		}
		trimmed := strings.TrimSpace(line)
		for _, directive := range []string{"include ", "-include ", "sinclude "} {
			if strings.HasPrefix(trimmed, directive) {
				return true
			}
		}
		colon := strings.Index(trimmed, ":")
		if colon <= 0 {
			continue
		}
		if equals := strings.Index(trimmed, "="); equals >= 0 && equals < colon {
			continue // a variable assignment such as X = a:b
		}
		if strings.HasPrefix(strings.TrimLeft(trimmed[colon:], ":"), "=") {
			continue // a variable assignment such as CC := gcc
		}
		targets := trimmed[:colon]
		if strings.Contains(targets, "$(") || strings.Contains(targets, "${") {
			return true
		}
		for _, target := range strings.Fields(targets) {
			if makeTargetCanRunCheck(target) {
				return true
			}
		}
	}
	return false
}

// acceptedMakeTargets are the make targets the verification allowlist runs.
var acceptedMakeTargets = []string{"test", "check", "build", "vet", "lint"}

// makeTargetCanRunCheck reports whether a rule target in a Makefile can supply
// the recipe for an accepted make target: the target is one of them, a pattern
// that matches one (a stem of at least one character must fill the %), or
// .DEFAULT, which runs for any target without a rule.
func makeTargetCanRunCheck(target string) bool {
	if target == ".DEFAULT" {
		return true
	}
	prefix, suffix, isPattern := strings.Cut(target, "%")
	for _, accepted := range acceptedMakeTargets {
		if !isPattern {
			if target == accepted {
				return true
			}
			continue
		}
		if !strings.Contains(suffix, "%") && len(accepted) > len(prefix)+len(suffix) &&
			strings.HasPrefix(accepted, prefix) && strings.HasSuffix(accepted, suffix) {
			return true
		}
	}
	return false
}
