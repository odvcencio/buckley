package plugin

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/odvcencio/buckley/pkg/transparency"
)

// ContextGatherer collects context from various sources for plugin execution.
type ContextGatherer struct {
	workDir string
	flags   map[string]string
	audit   *transparency.ContextAudit
}

// NewContextGatherer creates a context gatherer for the given working directory.
func NewContextGatherer(workDir string, flags map[string]string) *ContextGatherer {
	if workDir == "" {
		workDir, _ = os.Getwd()
	}
	return &ContextGatherer{
		workDir: workDir,
		flags:   flags,
		audit:   transparency.NewContextAudit(),
	}
}

// Gather collects context from all specified sources.
func (g *ContextGatherer) Gather(sources []ContextSource) (string, error) {
	var parts []string

	for _, source := range sources {
		content, err := g.gatherSource(source)
		if err != nil {
			if source.Optional {
				continue
			}
			return "", fmt.Errorf("gather %s: %w", source.Type, err)
		}

		if content != "" {
			// Apply max bytes limit
			if source.MaxBytes > 0 && len(content) > source.MaxBytes {
				originalLen := len(content)
				content = content[:source.MaxBytes]
				// Estimate tokens as bytes/4
				g.audit.AddTruncated(source.Type, len(content)/4, originalLen/4)
			} else {
				// Estimate tokens as bytes/4
				g.audit.Add(source.Type, len(content)/4)
			}
			parts = append(parts, content)
		}
	}

	return strings.Join(parts, "\n\n"), nil
}

// Audit returns the context audit for transparency.
func (g *ContextGatherer) Audit() *transparency.ContextAudit {
	return g.audit
}

func (g *ContextGatherer) gatherSource(source ContextSource) (string, error) {
	switch source.Type {
	case "git_log":
		return g.gatherGitLog(source)
	case "git_diff":
		return g.gatherGitDiff(source)
	case "git_diff_staged":
		return g.gatherGitDiffStaged(source)
	case "git_status":
		return g.gatherGitStatus(source)
	case "file":
		return g.gatherFile(source)
	case "glob":
		return g.gatherGlob(source)
	case "agents_md":
		return g.gatherAgentsMD(source)
	case "env":
		return g.gatherEnv(source)
	case "command":
		return g.gatherCommand(source)
	default:
		return "", fmt.Errorf("unknown context source type: %s", source.Type)
	}
}

func (g *ContextGatherer) gatherGitLog(source ContextSource) (string, error) {
	args := []string{"log", "--no-walk=unsorted"}

	// Handle "since" - can be a tag, commit, or time reference
	since := InterpolateFlags(source.Since, g.flags)
	if since == "" {
		since = "HEAD~10" // Default to last 10 commits
	}

	if since == "last-tag" {
		// Find the last tag
		tagOutput, err := exec.Command("git", "-C", g.workDir, "describe", "--tags", "--abbrev=0").Output()
		if err != nil {
			since = "HEAD~10" // Fallback if no tags
		} else {
			since = strings.TrimSpace(string(tagOutput))
		}
	}

	args = append(args, since+"..HEAD")

	// Format
	format := source.Format
	if format == "" {
		format = "oneline"
	}
	switch format {
	case "oneline":
		args = append(args, "--oneline")
	case "short":
		args = append(args, "--format=short")
	case "medium":
		args = append(args, "--format=medium")
	case "full":
		args = append(args, "--format=full")
	default:
		args = append(args, "--format="+format)
	}

	cmd := exec.Command("git", append([]string{"-C", g.workDir}, args...)...)
	output, err := cmd.Output()
	if err != nil {
		// Try without the range (might be initial commits)
		args = []string{"log", "-10", "--oneline"}
		cmd = exec.Command("git", append([]string{"-C", g.workDir}, args...)...)
		output, err = cmd.Output()
		if err != nil {
			return "", err
		}
	}

	if len(output) == 0 {
		return "", nil
	}

	return fmt.Sprintf("<git_log>\n%s</git_log>", strings.TrimSpace(string(output))), nil
}

func (g *ContextGatherer) gatherGitDiff(source ContextSource) (string, error) {
	path := InterpolateFlags(source.Path, g.flags)
	args := []string{"-C", g.workDir, "diff"}
	if path != "" {
		args = append(args, "--", path)
	}

	output, err := exec.Command("git", args...).Output()
	if err != nil {
		return "", err
	}

	if len(output) == 0 {
		return "", nil
	}

	return fmt.Sprintf("<git_diff>\n%s</git_diff>", strings.TrimSpace(string(output))), nil
}

func (g *ContextGatherer) gatherGitDiffStaged(source ContextSource) (string, error) {
	path := InterpolateFlags(source.Path, g.flags)
	args := []string{"-C", g.workDir, "diff", "--staged"}
	if path != "" {
		args = append(args, "--", path)
	}

	output, err := exec.Command("git", args...).Output()
	if err != nil {
		return "", err
	}

	if len(output) == 0 {
		return "", nil
	}

	return fmt.Sprintf("<git_diff_staged>\n%s</git_diff_staged>", strings.TrimSpace(string(output))), nil
}

func (g *ContextGatherer) gatherGitStatus(source ContextSource) (string, error) {
	output, err := exec.Command("git", "-C", g.workDir, "status", "--porcelain").Output()
	if err != nil {
		return "", err
	}

	if len(output) == 0 {
		return "", nil
	}

	return fmt.Sprintf("<git_status>\n%s</git_status>", strings.TrimSpace(string(output))), nil
}

func (g *ContextGatherer) gatherFile(source ContextSource) (string, error) {
	path := InterpolateFlags(source.Path, g.flags)
	if path == "" {
		return "", fmt.Errorf("file source requires path")
	}

	// Resolve relative to workdir
	if !filepath.IsAbs(path) {
		path = filepath.Join(g.workDir, path)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	filename := filepath.Base(path)
	return fmt.Sprintf("<file name=%q>\n%s</file>", filename, strings.TrimSpace(string(content))), nil
}

func (g *ContextGatherer) gatherGlob(source ContextSource) (string, error) {
	pattern := InterpolateFlags(source.Path, g.flags)
	if pattern == "" {
		return "", fmt.Errorf("glob source requires path pattern")
	}

	// Resolve relative to workdir
	if !filepath.IsAbs(pattern) {
		pattern = filepath.Join(g.workDir, pattern)
	}

	matches, err := filepath.Glob(pattern)
	if err != nil {
		return "", err
	}

	var parts []string
	for _, match := range matches {
		content, err := os.ReadFile(match)
		if err != nil {
			continue
		}
		relPath, _ := filepath.Rel(g.workDir, match)
		parts = append(parts, fmt.Sprintf("<file name=%q>\n%s</file>", relPath, strings.TrimSpace(string(content))))
	}

	return strings.Join(parts, "\n\n"), nil
}

func (g *ContextGatherer) gatherAgentsMD(source ContextSource) (string, error) {
	// Look for AGENTS.md in workdir or parent directories
	paths := []string{
		filepath.Join(g.workDir, "AGENTS.md"),
		filepath.Join(g.workDir, "CLAUDE.md"),
		filepath.Join(g.workDir, ".github", "AGENTS.md"),
	}

	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err == nil {
			return fmt.Sprintf("<agents_md>\n%s</agents_md>", strings.TrimSpace(string(content))), nil
		}
	}

	if source.Optional {
		return "", nil
	}
	return "", fmt.Errorf("agents.md not found")
}

func (g *ContextGatherer) gatherEnv(source ContextSource) (string, error) {
	// Gather specified env vars or all if path is "*"
	path := InterpolateFlags(source.Path, g.flags)

	if path == "*" {
		var lines []string
		for _, env := range os.Environ() {
			// Filter out sensitive vars
			if isSensitiveEnv(env) {
				continue
			}
			lines = append(lines, env)
		}
		return fmt.Sprintf("<env>\n%s</env>", strings.Join(lines, "\n")), nil
	}

	// Specific var names (comma-separated)
	vars := strings.Split(path, ",")
	var lines []string
	for _, v := range vars {
		v = strings.TrimSpace(v)
		if val := os.Getenv(v); val != "" {
			lines = append(lines, fmt.Sprintf("%s=%s", v, val))
		}
	}

	if len(lines) == 0 {
		return "", nil
	}

	return fmt.Sprintf("<env>\n%s</env>", strings.Join(lines, "\n")), nil
}

func (g *ContextGatherer) gatherCommand(source ContextSource) (string, error) {
	// Execute a shell command and capture output
	cmdStr := InterpolateFlags(source.Path, g.flags)
	if cmdStr == "" {
		return "", fmt.Errorf("command source requires path (command string)")
	}

	cmd := exec.Command("sh", "-c", cmdStr)
	cmd.Dir = g.workDir
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("<command cmd=%q>\n%s</command>", cmdStr, strings.TrimSpace(string(output))), nil
}

func isSensitiveEnv(env string) bool {
	lower := strings.ToLower(env)
	sensitive := []string{"key", "secret", "password", "token", "auth", "credential", "private"}
	for _, s := range sensitive {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}
