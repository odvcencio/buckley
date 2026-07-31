package reviewpolicy

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maximumDirectiveBytes = 256 << 10

// VerificationConstraints contains repository rules that affect review
// verification on the host.
type VerificationConstraints struct {
	TestsRequireContainer bool
	ForbidHostRepoWideGo  bool
}

// ParseVerificationConstraints extracts enforceable host restrictions from an
// applicable AGENTS.md chain.
func ParseVerificationConstraints(directives string) VerificationConstraints {
	var constraints VerificationConstraints
	for _, window := range directiveWindows(directives) {
		lower := strings.ToLower(window)
		if directiveRequiresContainerTests(lower) {
			constraints.TestsRequireContainer = true
		}
		if directiveForbidsRepoWideHostGo(lower) {
			constraints.ForbidHostRepoWideGo = true
		}
	}
	return constraints
}

// LoadApplicableVerificationConstraints reads the root AGENTS.md and each
// nested AGENTS.md that applies to a repository-relative verification path.
func LoadApplicableVerificationConstraints(root, requestedPath string) (VerificationConstraints, error) {
	root, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil || strings.TrimSpace(root) == "" {
		return VerificationConstraints{}, fmt.Errorf("verification root is required")
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return VerificationConstraints{}, fmt.Errorf("resolve verification root: %w", err)
	}

	requestedPath = strings.TrimSpace(requestedPath)
	if requestedPath == "" {
		requestedPath = "."
	}
	if filepath.IsAbs(requestedPath) || strings.ContainsRune(requestedPath, 0) {
		return VerificationConstraints{}, fmt.Errorf("verification path must be repository-relative")
	}
	cleaned := filepath.Clean(requestedPath)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return VerificationConstraints{}, fmt.Errorf("verification path escapes repository root")
	}

	candidates := []string{filepath.Join(root, "AGENTS.md")}
	if cleaned != "." {
		current := root
		for _, part := range strings.Split(cleaned, string(filepath.Separator)) {
			if part == "" || part == "." {
				continue
			}
			current = filepath.Join(current, part)
			candidates = append(candidates, filepath.Join(current, "AGENTS.md"))
		}
	}

	var content strings.Builder
	for _, candidate := range candidates {
		info, statErr := os.Lstat(candidate)
		if os.IsNotExist(statErr) {
			continue
		}
		if statErr != nil {
			return VerificationConstraints{}, fmt.Errorf("inspect %s: %w", filepath.Base(candidate), statErr)
		}
		if !info.Mode().IsRegular() {
			return VerificationConstraints{}, fmt.Errorf("%s is not a regular file", candidate)
		}
		remaining := maximumDirectiveBytes - content.Len()
		if remaining <= 0 {
			return VerificationConstraints{}, fmt.Errorf("applicable AGENTS.md chain exceeds %d bytes", maximumDirectiveBytes)
		}
		file, openErr := os.Open(candidate)
		if openErr != nil {
			return VerificationConstraints{}, fmt.Errorf("open %s: %w", filepath.Base(candidate), openErr)
		}
		chunk, readErr := io.ReadAll(io.LimitReader(file, int64(remaining+1)))
		closeErr := file.Close()
		if readErr != nil {
			return VerificationConstraints{}, fmt.Errorf("read %s: %w", filepath.Base(candidate), readErr)
		}
		if closeErr != nil {
			return VerificationConstraints{}, fmt.Errorf("close %s: %w", filepath.Base(candidate), closeErr)
		}
		if len(chunk) > remaining {
			return VerificationConstraints{}, fmt.Errorf("applicable AGENTS.md chain exceeds %d bytes", maximumDirectiveBytes)
		}
		content.Write(chunk)
		content.WriteByte('\n')
	}
	return ParseVerificationConstraints(content.String()), nil
}

// HostRejection explains why the constrained host verifier must not launch a
// process. An empty result permits the request.
func (c VerificationConstraints) HostRejection(kind, language, requestedPath string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	language = strings.ToLower(strings.TrimSpace(language))
	requestedPath = strings.ToLower(strings.TrimSpace(filepath.ToSlash(requestedPath)))
	if c.TestsRequireContainer && kind == "test" {
		return "repository directives require test execution in Docker or a dedicated container; host verification was not started"
	}
	if c.ForbidHostRepoWideGo && language == "go" &&
		(kind == "build" || kind == "test" || kind == "check") &&
		(strings.Contains(requestedPath, "...") || requestedPath == "./...") {
		return "repository directives forbid a repo-wide Go command on the host; verification was not started"
	}
	return ""
}

func directiveWindows(directives string) []string {
	rawLines := strings.Split(strings.ReplaceAll(directives, "\r\n", "\n"), "\n")
	lines := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	windows := make([]string, 0, len(lines)*2)
	for index, line := range lines {
		windows = append(windows, line)
		if index+1 < len(lines) {
			windows = append(windows, line+" "+lines[index+1])
		}
	}
	return windows
}

func directiveRequiresContainerTests(lower string) bool {
	mentionsVerification := strings.Contains(lower, "test") ||
		strings.Contains(lower, "correctness") ||
		strings.Contains(lower, "parity") ||
		strings.Contains(lower, "race coverage")
	if !mentionsVerification {
		return false
	}
	for _, requirement := range []string{
		"inside docker",
		"docker isolation only",
		"in a dedicated container",
		"dedicated-container only",
		"container only",
	} {
		if strings.Contains(lower, requirement) {
			return true
		}
	}
	return strings.Contains(lower, "use docker") && strings.Contains(lower, "only")
}

func directiveForbidsRepoWideHostGo(lower string) bool {
	if !strings.Contains(lower, "go test ./...") {
		return false
	}
	for _, prohibition := range []string{"do not run", "never run", "must not run", "forbid"} {
		if strings.Contains(lower, prohibition) {
			return true
		}
	}
	return false
}
