package commit

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/odvcencio/buckley/pkg/transparency"
)

// Context contains all the information needed for commit generation.
type Context struct {
	// Git information
	Diff     string
	Files    []FileChange
	Stats    DiffStats
	Branch   string
	RepoRoot string

	// Project context
	AgentsMD string

	// Computed
	Areas    []string  // Affected areas/packages
	Warnings []Warning // Warnings about potentially unintended files
}

// Warning represents a warning about a staged file.
type Warning struct {
	Severity string // "error", "warning", "info"
	Category string // "secrets", "build", "deps", "binary", "large"
	Path     string
	Message  string
}

// FileChange represents a single file change.
type FileChange struct {
	Status  string // A, M, D, R, C
	Path    string
	OldPath string // For renames
}

// DiffStats contains diff statistics.
type DiffStats struct {
	Files       int
	Insertions  int
	Deletions   int
	BinaryFiles int
}

// TotalChanges returns insertions + deletions.
func (ds DiffStats) TotalChanges() int {
	return ds.Insertions + ds.Deletions
}

// Options for context assembly.
type ContextOptions struct {
	MaxDiffBytes  int
	MaxDiffTokens int
	IncludeAgents bool
}

// DefaultContextOptions returns sensible defaults.
func DefaultContextOptions() ContextOptions {
	return ContextOptions{
		MaxDiffBytes:  80_000,
		MaxDiffTokens: 20_000,
		IncludeAgents: true,
	}
}

// AssembleContext gathers all context needed for commit generation.
// Returns the context and a transparency audit of what was included.
func AssembleContext(opts ContextOptions) (*Context, *transparency.ContextAudit, error) {
	audit := transparency.NewContextAudit()
	ctx := &Context{}

	// Get repo root
	root, err := gitOutput("rev-parse", "--show-toplevel")
	if err != nil {
		return nil, nil, fmt.Errorf("not in a git repository: %w", err)
	}
	ctx.RepoRoot = strings.TrimSpace(root)

	// Get branch
	branch, _ := gitOutput("rev-parse", "--abbrev-ref", "HEAD")
	ctx.Branch = strings.TrimSpace(branch)
	audit.Add("branch", estimateTokens(ctx.Branch))

	// Get staged files
	nameStatus, err := gitOutput("diff", "--cached", "--name-status")
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get staged files: %w", err)
	}
	ctx.Files = parseNameStatus(nameStatus)
	if len(ctx.Files) == 0 {
		return nil, nil, fmt.Errorf("no staged changes")
	}
	audit.Add("staged files", estimateTokens(nameStatus))

	// Get diff
	diff, truncated, err := gitOutputLimited(opts.MaxDiffBytes, "diff", "--cached")
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get diff: %w", err)
	}
	ctx.Diff = diff
	diffTokens := estimateTokens(diff)
	if truncated {
		audit.AddTruncated("git diff", diffTokens, opts.MaxDiffTokens)
	} else {
		audit.Add("git diff", diffTokens)
	}

	// Get stats
	ctx.Stats = getDiffStats()
	if ctx.Stats.Files == 0 {
		ctx.Stats.Files = len(ctx.Files)
	}

	// Extract affected areas
	ctx.Areas = extractAreas(ctx.Files)

	// Load AGENTS.md if requested
	if opts.IncludeAgents {
		agentsPath := filepath.Join(ctx.RepoRoot, "AGENTS.md")
		if content, err := readFileLimited(agentsPath, 10_000); err == nil && content != "" {
			ctx.AgentsMD = content
			audit.Add("AGENTS.md", estimateTokens(content))
		}
	}

	// Detect warnings about potentially unintended files
	ctx.Warnings = detectWarnings(ctx.Files, ctx.AgentsMD)

	return ctx, audit, nil
}

// parseNameStatus parses git diff --name-status output.
func parseNameStatus(output string) []FileChange {
	var changes []FileChange
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			continue
		}
		change := FileChange{
			Status: parts[0][:1], // First char only (ignore scores)
			Path:   parts[len(parts)-1],
		}
		if (change.Status == "R" || change.Status == "C") && len(parts) >= 3 {
			change.OldPath = parts[1]
			change.Path = parts[2]
		}
		changes = append(changes, change)
	}
	return changes
}

// getDiffStats extracts diff statistics.
func getDiffStats() DiffStats {
	output, err := gitOutput("diff", "--cached", "--numstat")
	if err != nil {
		return DiffStats{}
	}

	var stats DiffStats
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			continue
		}
		stats.Files++

		ins, errIns := strconv.Atoi(parts[0])
		del, errDel := strconv.Atoi(parts[1])
		if errIns != nil || errDel != nil {
			stats.BinaryFiles++
			continue
		}
		stats.Insertions += ins
		stats.Deletions += del
	}
	return stats
}

// extractAreas identifies affected areas from file paths.
func extractAreas(files []FileChange) []string {
	seen := make(map[string]bool)
	var areas []string

	for _, f := range files {
		area := areaFromPath(f.Path)
		if area != "" && !seen[area] {
			seen[area] = true
			areas = append(areas, area)
		}
	}
	return areas
}

// areaFromPath extracts the area/package from a file path.
func areaFromPath(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return ""
	}
	switch parts[0] {
	case "pkg", "cmd", "internal":
		return parts[1]
	case "web", "docs", "scripts":
		return parts[0]
	default:
		return parts[0]
	}
}

// estimateTokens provides a rough token estimate.
// Uses the common heuristic of ~4 chars per token.
func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return (len(s) + 3) / 4
}

// Git helpers

func gitOutput(args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"--no-pager"}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func gitOutputLimited(maxBytes int, args ...string) (string, bool, error) {
	cmd := exec.Command("git", append([]string{"--no-pager"}, args...)...)
	output, err := cmd.Output()
	if err != nil {
		return "", false, err
	}

	if len(output) > maxBytes {
		return string(output[:maxBytes]), true, nil
	}
	return strings.TrimSpace(string(output)), false, nil
}

func readFileLimited(path string, maxBytes int) (string, error) {
	cmd := exec.Command("head", "-c", strconv.Itoa(maxBytes), path)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

// detectWarnings analyzes staged files for potentially unintended commits.
func detectWarnings(files []FileChange, agentsMD string) []Warning {
	var warnings []Warning

	// Check if project explicitly vendors dependencies
	vendorsDeps := strings.Contains(strings.ToLower(agentsMD), "vendor") &&
		(strings.Contains(strings.ToLower(agentsMD), "check in") ||
			strings.Contains(strings.ToLower(agentsMD), "checked in") ||
			strings.Contains(strings.ToLower(agentsMD), "commit"))

	for _, f := range files {
		if f.Status == "D" {
			continue // Deletions are fine
		}

		path := strings.ToLower(f.Path)
		base := strings.ToLower(filepath.Base(f.Path))
		dir := filepath.Dir(f.Path)

		// Secrets/sensitive files - always error
		if isSecretFile(base, path) {
			warnings = append(warnings, Warning{
				Severity: "error",
				Category: "secrets",
				Path:     f.Path,
				Message:  "likely contains secrets or credentials",
			})
			continue
		}

		// Build artifacts
		if isBuildArtifact(base, path, dir) {
			warnings = append(warnings, Warning{
				Severity: "warning",
				Category: "build",
				Path:     f.Path,
				Message:  "appears to be a build artifact",
			})
			continue
		}

		// Dependencies (only warn if project doesn't vendor)
		if !vendorsDeps && isDependencyFile(path, dir) {
			warnings = append(warnings, Warning{
				Severity: "warning",
				Category: "deps",
				Path:     f.Path,
				Message:  "appears to be a dependency file (add vendor policy to AGENTS.md to suppress)",
			})
			continue
		}

		// Large binaries/media
		if isLargeBinary(base) {
			warnings = append(warnings, Warning{
				Severity: "info",
				Category: "binary",
				Path:     f.Path,
				Message:  "binary or media file - consider using Git LFS",
			})
		}
	}

	return warnings
}

// isSecretFile returns true if the file likely contains secrets.
func isSecretFile(base, path string) bool {
	// Exact matches
	secretFiles := []string{
		".env", ".env.local", ".env.development", ".env.production", ".env.test",
		".envrc",
		"credentials.json", "credentials.yaml", "credentials.yml",
		"secrets.json", "secrets.yaml", "secrets.yml",
		".secrets",
		"id_rsa", "id_ed25519", "id_ecdsa", "id_dsa",
		".pem", ".key", ".p12", ".pfx",
		".htpasswd", ".netrc", ".npmrc", ".pypirc",
		"service-account.json", "serviceaccount.json",
		"kubeconfig", ".kube/config",
	}
	for _, s := range secretFiles {
		if base == s || strings.HasSuffix(base, s) {
			return true
		}
	}

	// Patterns
	if strings.HasSuffix(base, ".env") && base != "sample.env" && base != "example.env" && base != ".env.example" {
		return true
	}
	if strings.HasSuffix(base, ".key") || strings.HasSuffix(base, ".pem") {
		return true
	}
	if strings.Contains(base, "secret") && (strings.HasSuffix(base, ".json") || strings.HasSuffix(base, ".yaml") || strings.HasSuffix(base, ".yml")) {
		return true
	}
	if strings.Contains(base, "credential") && (strings.HasSuffix(base, ".json") || strings.HasSuffix(base, ".yaml") || strings.HasSuffix(base, ".yml")) {
		return true
	}

	// AWS credentials
	if strings.Contains(path, ".aws/") && (base == "credentials" || base == "config") {
		return true
	}

	return false
}

// isBuildArtifact returns true if the file appears to be a build output.
func isBuildArtifact(base, path, dir string) bool {
	// Build output directories
	buildDirs := []string{"dist/", "build/", "out/", "target/", "bin/", ".next/", "__pycache__/", ".pytest_cache/"}
	for _, d := range buildDirs {
		if strings.HasPrefix(path, d) || strings.Contains(path, "/"+d) {
			return true
		}
	}

	// Build artifacts by extension
	buildExts := []string{".o", ".a", ".so", ".dylib", ".dll", ".exe", ".class", ".pyc", ".pyo"}
	for _, ext := range buildExts {
		if strings.HasSuffix(base, ext) {
			return true
		}
	}

	// Coverage/test artifacts
	if base == "coverage.out" || base == "coverage.html" || strings.HasPrefix(base, "coverage-") {
		return true
	}
	if base == ".coverage" || strings.HasSuffix(base, ".lcov") {
		return true
	}

	return false
}

// isDependencyFile returns true if the file appears to be a dependency.
func isDependencyFile(path, dir string) bool {
	// Node
	if strings.HasPrefix(path, "node_modules/") || strings.Contains(path, "/node_modules/") {
		return true
	}

	// Go vendor (only if not explicitly vendoring)
	if strings.HasPrefix(path, "vendor/") && !strings.HasPrefix(path, "vendor/modules.txt") {
		// Check if it's Go vendor (has go files)
		if strings.HasSuffix(path, ".go") {
			return true
		}
	}

	// Python
	if strings.Contains(path, "site-packages/") || strings.Contains(path, ".venv/") || strings.HasPrefix(path, "venv/") {
		return true
	}

	// Ruby
	if strings.Contains(path, "/gems/") || strings.HasPrefix(path, "vendor/bundle/") {
		return true
	}

	// Rust
	if strings.HasPrefix(path, "target/debug/deps/") || strings.HasPrefix(path, "target/release/deps/") {
		return true
	}

	return false
}

// isLargeBinary returns true if the file is a large binary/media type.
func isLargeBinary(base string) bool {
	binaryExts := []string{
		".zip", ".tar", ".gz", ".bz2", ".7z", ".rar",
		".mp4", ".mov", ".avi", ".mkv", ".webm",
		".mp3", ".wav", ".flac", ".aac",
		".psd", ".ai", ".sketch",
		".sqlite", ".db",
	}
	for _, ext := range binaryExts {
		if strings.HasSuffix(base, ext) {
			return true
		}
	}
	return false
}
