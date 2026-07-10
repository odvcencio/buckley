package commands

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"m31labs.dev/buckley/pkg/model"
)

const (
	reviewEvidenceBuild = "build"
	reviewEvidenceTest  = "test"
)

type reviewCoverageTarget struct {
	Path      string
	Recursive bool
}

type reviewCommandEvidenceDetails struct {
	Kind     string
	Language string
	Targets  []reviewCoverageTarget
}

var cargoTestCountPattern = regexp.MustCompile(`(?m)\brunning\s+([0-9]+)\s+tests?\b`)

// classifyReviewCommandEvidence recognizes only standalone, completed build
// and test invocations. It deliberately does not infer success from prose or
// from a command merely containing a familiar substring.
func classifyReviewCommandEvidence(evidence model.CommandExecutionEvidence) (string, bool) {
	details, ok := classifyReviewCommandEvidenceDetails(evidence)
	return details.Kind, ok
}

func classifyReviewCommandEvidenceDetails(evidence model.CommandExecutionEvidence) (reviewCommandEvidenceDetails, bool) {
	if !strings.EqualFold(strings.TrimSpace(evidence.Status), "completed") ||
		evidence.ExitCode == nil || *evidence.ExitCode != 0 {
		return reviewCommandEvidenceDetails{}, false
	}
	workDir := strings.TrimSpace(evidence.WorkingDirectory)
	repositoryRoot := strings.TrimSpace(evidence.RepositoryRoot)
	if workDir == "" || repositoryRoot == "" || filepath.Clean(workDir) != filepath.Clean(repositoryRoot) {
		return reviewCommandEvidenceDetails{}, false
	}

	argv, ok := reviewCommandArgv(evidence.Command)
	if !ok {
		return reviewCommandEvidenceDetails{}, false
	}
	kind, runner := classifyReviewArgv(argv)
	if kind == "" {
		return reviewCommandEvidenceDetails{}, false
	}
	if kind == reviewEvidenceTest && reviewOutputShowsNoTests(runner, evidence.AggregatedOutput) {
		return reviewCommandEvidenceDetails{}, false
	}
	if kind == reviewEvidenceTest && runner == "go" && goReviewUsesFocusedFilter(argv) &&
		!goReviewOutputProvesTestExecution(evidence.AggregatedOutput) {
		return reviewCommandEvidenceDetails{}, false
	}
	details := reviewCommandEvidenceDetails{
		Kind:     kind,
		Language: reviewEvidenceLanguage(runner),
		Targets:  reviewCommandCoverage(argv, runner),
	}
	if details.Language == "" || len(details.Targets) == 0 {
		return reviewCommandEvidenceDetails{}, false
	}
	return details, true
}

func reviewEvidenceLanguage(runner string) string {
	switch runner {
	case "go":
		return "go"
	case "cargo":
		return "rust"
	case "pytest":
		return "python"
	case "npm", "yarn", "pnpm", "yarn.cmd", "pnpm.cmd":
		return "node"
	case "make":
		return "*"
	default:
		return ""
	}
}

func reviewCommandCoverage(argv []string, runner string) []reviewCoverageTarget {
	argv, ok := stripReviewEnvironment(argv)
	if !ok || len(argv) == 0 {
		return nil
	}
	if runner == "cargo" {
		return []reviewCoverageTarget{{
			Path:      ".",
			Recursive: hasExactArg(argv[2:], "--workspace", "--all"),
		}}
	}
	if runner != "go" {
		return []reviewCoverageTarget{{Path: ".", Recursive: true}}
	}
	if len(argv) < 2 {
		return nil
	}
	targets := goReviewPackageTargets(argv[2:])
	if len(targets) == 0 {
		targets = []string{"."}
	}
	coverage := make([]reviewCoverageTarget, 0, len(targets))
	for _, target := range targets {
		clean := filepath.ToSlash(filepath.Clean(target))
		recursive := clean == "..." || strings.HasSuffix(clean, "/...")
		clean = strings.TrimSuffix(clean, "/...")
		if clean == "..." || clean == "" {
			clean = "."
		}
		coverage = append(coverage, reviewCoverageTarget{Path: clean, Recursive: recursive})
	}
	return coverage
}

func goReviewPackageTargets(args []string) []string {
	var targets []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			targets = append(targets, arg)
			continue
		}
		name, _, hasValue := strings.Cut(arg, "=")
		switch name {
		case "-tags", "-mod", "-p", "-count", "-timeout", "-parallel", "-cpu", "-covermode", "-coverpkg", "-vet", "-shuffle", "-run":
			if !hasValue {
				index++
			}
		}
	}
	return targets
}

func reviewCommandArgv(command string) ([]string, bool) {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, false
	}
	outer, ok := splitReviewShellWords(command)
	if !ok || len(outer) == 0 {
		return nil, false
	}
	if isReviewShell(filepath.Base(outer[0])) {
		script, wrapped := reviewShellScript(outer)
		if !wrapped || hasUnsafeReviewShellSyntax(script) {
			return nil, false
		}
		return splitReviewShellWords(script)
	}
	if hasUnsafeReviewShellSyntax(command) {
		return nil, false
	}
	return outer, true
}

func isReviewShell(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "bash", "dash", "sh", "zsh":
		return true
	default:
		return false
	}
}

func reviewShellScript(argv []string) (string, bool) {
	// Codex currently reports native commands as /bin/bash -lc '<payload>'.
	// Accept the equivalent separate -l -c form, but reject additional shell
	// arguments because their execution semantics are less obvious.
	if len(argv) == 3 && strings.HasPrefix(argv[1], "-") &&
		strings.Contains(strings.TrimPrefix(argv[1], "-"), "c") {
		return argv[2], strings.TrimSpace(argv[2]) != ""
	}
	if len(argv) == 4 && argv[1] == "-l" && argv[2] == "-c" {
		return argv[3], strings.TrimSpace(argv[3]) != ""
	}
	return "", false
}

func hasUnsafeReviewShellSyntax(command string) bool {
	var quote byte
	escaped := false
	for i := 0; i < len(command); i++ {
		char := command[i]
		if escaped {
			escaped = false
			continue
		}
		if quote != '\'' && char == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if char == quote {
				quote = 0
				continue
			}
			if char == '*' || char == '?' || char == '[' || char == ']' || char == '{' || char == '}' || char == '~' ||
				quote == '"' && (char == '`' || char == '$') {
				return true
			}
			continue
		}
		switch char {
		case '\'', '"':
			quote = char
		case ';', '|', '&', '<', '>', '(', ')', '\n', '\r', '`', '$', '*', '?', '[', ']', '{', '}', '~':
			return true
		}
	}
	return quote != 0 || escaped
}

func splitReviewShellWords(command string) ([]string, bool) {
	var (
		words   []string
		word    strings.Builder
		quote   byte
		escaped bool
		started bool
	)
	flush := func() {
		if started {
			words = append(words, word.String())
			word.Reset()
			started = false
		}
	}
	for i := 0; i < len(command); i++ {
		char := command[i]
		if escaped {
			word.WriteByte(char)
			started = true
			escaped = false
			continue
		}
		if quote == '\'' {
			if char == '\'' {
				quote = 0
			} else {
				word.WriteByte(char)
				started = true
			}
			continue
		}
		if quote == '"' {
			switch char {
			case '"':
				quote = 0
			case '\\':
				escaped = true
			default:
				word.WriteByte(char)
				started = true
			}
			continue
		}
		switch {
		case char == '\'' || char == '"':
			quote = char
			started = true
		case char == '\\':
			escaped = true
			started = true
		case unicode.IsSpace(rune(char)):
			flush()
		default:
			word.WriteByte(char)
			started = true
		}
	}
	if quote != 0 || escaped {
		return nil, false
	}
	flush()
	return words, true
}

func classifyReviewArgv(argv []string) (kind, runner string) {
	var environmentOK bool
	argv, environmentOK = stripReviewEnvironment(argv)
	if !environmentOK || len(argv) == 0 || hasReviewNonExecutionFlag(argv) {
		return "", ""
	}
	runner = strings.ToLower(filepath.Base(argv[0]))
	switch runner {
	case "go", "go.exe":
		if len(argv) < 2 {
			return "", ""
		}
		switch argv[1] {
		case "build":
			if validateGoBuildArgs(argv[2:]) {
				return reviewEvidenceBuild, "go"
			}
			return "", ""
		case "test":
			buildOnly, safe := validateGoTestArgs(argv[2:])
			if !safe {
				return "", ""
			}
			if buildOnly {
				return reviewEvidenceBuild, "go"
			}
			return reviewEvidenceTest, "go"
		}
	case "cargo", "cargo.exe":
		if len(argv) < 2 {
			return "", ""
		}
		if kind := classifyCargoReviewArgs(argv[1], argv[2:]); kind != "" {
			return kind, "cargo"
		}
	case "pytest", "pytest.exe", "py.test", "py.test.exe":
		if !safePytestReviewArgs(argv[1:]) {
			return "", ""
		}
		return reviewEvidenceTest, "pytest"
	case "python", "python3", "python.exe", "python3.exe", "py", "py.exe":
		if len(argv) >= 3 && argv[1] == "-m" && (argv[2] == "pytest" || argv[2] == "py.test") {
			if !safePytestReviewArgs(argv[3:]) {
				return "", ""
			}
			return reviewEvidenceTest, "pytest"
		}
	case "npm", "npm.cmd":
		return classifyPackageScript(argv[1:], "npm")
	case "yarn", "yarn.cmd", "pnpm", "pnpm.cmd":
		return classifyPackageScript(argv[1:], runner)
	case "make", "gmake", "make.exe", "gmake.exe":
		return classifyMakeTarget(argv[1:])
	}
	return "", ""
}

func stripReviewEnvironment(argv []string) ([]string, bool) {
	if len(argv) > 0 && isReviewEnvironmentAssignment(argv[0]) {
		return nil, false
	}
	if len(argv) > 0 && filepath.Base(argv[0]) == "env" {
		return nil, false
	}
	return argv, true
}

func isReviewEnvironmentAssignment(word string) bool {
	index := strings.IndexByte(word, '=')
	if index <= 0 {
		return false
	}
	for i, char := range word[:index] {
		if i == 0 {
			if char != '_' && !unicode.IsLetter(char) {
				return false
			}
			continue
		}
		if char != '_' && !unicode.IsLetter(char) && !unicode.IsDigit(char) {
			return false
		}
	}
	return true
}

func hasReviewNonExecutionFlag(argv []string) bool {
	for _, arg := range argv[1:] {
		switch strings.ToLower(strings.TrimSpace(arg)) {
		case "-h", "--help", "--version", "-version", "--dry-run", "--dryrun":
			return true
		}
	}
	return false
}

func validateGoBuildArgs(args []string) bool {
	return validateGoArgs(args, false) == reviewEvidenceBuild
}

func validateGoTestArgs(args []string) (bool, bool) {
	kind := validateGoArgs(args, true)
	return kind == reviewEvidenceBuild, kind != ""
}

func validateGoArgs(args []string, test bool) string {
	kind := reviewEvidenceBuild
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			if !safeGoPackageTarget(arg) {
				return ""
			}
			continue
		}
		name, value, hasValue := strings.Cut(arg, "=")
		switch name {
		case "-race", "-msan", "-asan", "-trimpath", "-v":
			if hasValue {
				return ""
			}
		case "-tags":
			return ""
		case "-mod", "-p":
			if !hasValue {
				index++
				if index >= len(args) || strings.TrimSpace(args[index]) == "" {
					return ""
				}
				value = args[index]
			}
			if name == "-p" {
				count, err := strconv.Atoi(value)
				if err != nil || count < 1 {
					return ""
				}
			} else if value != "readonly" && value != "vendor" {
				return ""
			}
		default:
			if !test {
				return ""
			}
			switch name {
			case "-json", "-failfast", "-cover", "-fullpath":
				if hasValue {
					return ""
				}
			case "-count", "-timeout", "-parallel", "-cpu", "-covermode", "-coverpkg", "-vet", "-shuffle":
				if !hasValue {
					index++
					if index >= len(args) || strings.TrimSpace(args[index]) == "" {
						return ""
					}
					value = args[index]
				}
				if name == "-count" {
					count, err := strconv.Atoi(value)
					if err != nil || count < 1 {
						return ""
					}
				}
			case "-run":
				if !hasValue {
					index++
					if index >= len(args) {
						return ""
					}
					value = args[index]
				}
				if strings.TrimSpace(value) == "" {
					return ""
				}
				if value == "^$" {
					kind = reviewEvidenceBuild
				} else if !hasExactArg(args, "-v") {
					// Focused native tests are trustworthy only with verbose output,
					// which lets the provider prove at least one test actually ran.
					return ""
				}
			default:
				return ""
			}
		}
	}
	if test && kind != reviewEvidenceBuild {
		return reviewEvidenceTest
	}
	// A test command without -run '^$' is real test evidence.
	if test {
		hasBuildOnlyRun := false
		for index, arg := range args {
			if arg == "-run" && index+1 < len(args) && args[index+1] == "^$" || strings.HasPrefix(arg, "-run=") && strings.TrimPrefix(arg, "-run=") == "^$" {
				hasBuildOnlyRun = true
			}
		}
		if !hasBuildOnlyRun {
			return reviewEvidenceTest
		}
	}
	return kind
}

func safeGoPackageTarget(target string) bool {
	if target == "" || filepath.IsAbs(target) || strings.HasSuffix(strings.ToLower(target), ".go") {
		return false
	}
	if target != "." && target != "./..." && !strings.HasPrefix(target, "./") {
		return false
	}
	clean := filepath.Clean(target)
	return clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func classifyPackageScript(args []string, runner string) (string, string) {
	if len(args) == 0 {
		return "", ""
	}
	for len(args) > 0 && args[0] == "--offline" {
		args = args[1:]
	}
	if len(args) == 0 {
		return "", ""
	}
	script := args[0]
	if script == "run" || script == "run-script" {
		if len(args) != 2 {
			return "", ""
		}
		script = args[1]
	} else if len(args) != 1 {
		return "", ""
	}
	// npm build is not equivalent to npm run build, so only accept npm's
	// explicit run form. yarn and pnpm expose build/test scripts directly.
	if runner == "npm" && args[0] == "build" {
		return "", ""
	}
	switch script {
	case "build":
		return reviewEvidenceBuild, runner
	case "test":
		return reviewEvidenceTest, runner
	default:
		return "", ""
	}
}

func safePytestReviewArgs(args []string) bool {
	for _, arg := range args {
		switch {
		case arg == ".", arg == "-q", arg == "-v", arg == "-vv", arg == "-s",
			arg == "--strict-markers", arg == "--strict-config", arg == "--disable-warnings",
			strings.HasPrefix(arg, "--tb="), strings.HasPrefix(arg, "--color="), strings.HasPrefix(arg, "--maxfail="):
			continue
		default:
			return false
		}
	}
	return true
}

func classifyCargoReviewArgs(subcommand string, args []string) string {
	kind := ""
	switch subcommand {
	case "build", "check":
		kind = reviewEvidenceBuild
	case "test":
		kind = reviewEvidenceTest
	default:
		return ""
	}
	for _, arg := range args {
		switch arg {
		case "--workspace", "--all", "--all-targets", "--locked", "--offline", "--release":
			continue
		case "--no-run":
			if subcommand != "test" {
				return ""
			}
			kind = reviewEvidenceBuild
		default:
			return ""
		}
	}
	return kind
}

func classifyMakeTarget(args []string) (string, string) {
	var targets []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if isReviewEnvironmentAssignment(arg) {
			return "", ""
		}
		switch arg {
		case "-n", "--just-print", "--dry-run", "--recon", "-q", "--question", "-t", "--touch":
			return "", ""
		case "-C", "--directory":
			return "", ""
		case "-f", "--file", "-I", "--include-dir":
			return "", ""
		}
		if strings.HasPrefix(arg, "--directory=") {
			return "", ""
		}
		if strings.HasPrefix(arg, "--file=") || strings.HasPrefix(arg, "--include-dir=") {
			return "", ""
		}
		if strings.HasPrefix(arg, "-j") ||
			strings.HasPrefix(arg, "--jobs") || arg == "-s" || arg == "--silent" {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return "", ""
		}
		targets = append(targets, arg)
	}
	if len(targets) != 1 {
		return "", ""
	}
	switch targets[0] {
	case "build":
		return reviewEvidenceBuild, "make"
	case "test":
		return reviewEvidenceTest, "make"
	default:
		return "", ""
	}
}

func hasExactArg(args []string, targets ...string) bool {
	for _, arg := range args {
		for _, target := range targets {
			if arg == target {
				return true
			}
		}
	}
	return false
}

func hasArgPrefix(args []string, prefix string) bool {
	for _, arg := range args {
		if arg == prefix || strings.HasPrefix(arg, prefix+"=") {
			return true
		}
	}
	return false
}

func reviewOutputShowsNoTests(runner, output string) bool {
	lower := strings.ToLower(output)
	if runner == "go" && strings.Contains(lower, "[no test files]") {
		// Recursive Go runs commonly contain a mix of utility packages without
		// tests and packages whose tests completed successfully. Reject a truly
		// testless target, but do not discard the entire repo-wide evidence event
		// merely because one package printed the standard '?' marker.
		hasPassingPackage := false
		for _, line := range strings.Split(lower, "\n") {
			fields := strings.Fields(line)
			if len(fields) > 0 && fields[0] == "ok" {
				hasPassingPackage = true
				break
			}
		}
		if !hasPassingPackage {
			return true
		}
	}
	for _, marker := range []string{
		"no tests to run",
		"no tests ran",
		"no tests found",
		"collected 0 item",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	if runner != "cargo" {
		return false
	}
	matches := cargoTestCountPattern.FindAllStringSubmatch(lower, -1)
	if len(matches) == 0 {
		return false
	}
	for _, match := range matches {
		count, err := strconv.Atoi(match[1])
		if err == nil && count > 0 {
			return false
		}
	}
	return true
}

func goReviewUsesFocusedFilter(argv []string) bool {
	argv, ok := stripReviewEnvironment(argv)
	if !ok || len(argv) < 3 || filepath.Base(argv[0]) != "go" || argv[1] != "test" {
		return false
	}
	for index, arg := range argv[2:] {
		if arg == "-run" && index+3 < len(argv) {
			return argv[index+3] != "^$"
		}
		if strings.HasPrefix(arg, "-run=") {
			return strings.TrimPrefix(arg, "-run=") != "^$"
		}
	}
	return false
}

func goReviewOutputProvesTestExecution(output string) bool {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "--- PASS:") || strings.HasPrefix(line, "=== RUN") {
			return true
		}
	}
	return false
}
