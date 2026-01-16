package ralph

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const maxRulesChars = 4000

// BuildProjectContext builds a textual snapshot of project context.
func BuildProjectContext(workDir string) string {
	root := strings.TrimSpace(workDir)
	if root == "" {
		return ""
	}
	if IsGitRepo(root) {
		if repoRoot, err := GetRepoRoot(root); err == nil {
			root = repoRoot
		}
	}

	branch := gitBranch(root)
	patterns := detectPatterns(root)
	structure := summarizeStructure(root)
	rules := loadRules(root)

	lines := []string{}
	if branch != "" {
		lines = append(lines, "git_branch: "+branch)
	}
	if len(patterns) > 0 {
		lines = append(lines, "detected_patterns: "+strings.Join(patterns, ", "))
	}
	if len(structure) > 0 {
		lines = append(lines, "repo_structure: "+strings.Join(structure, ", "))
	}
	if rules != "" {
		lines = append(lines, "rules:\n"+rules)
	}
	return strings.Join(lines, "\n")
}

func gitBranch(root string) string {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func detectPatterns(root string) []string {
	patterns := []string{}
	if exists(filepath.Join(root, "go.mod")) {
		patterns = append(patterns, "go module")
	}
	if exists(filepath.Join(root, "package.json")) {
		patterns = append(patterns, "node package")
	}
	if exists(filepath.Join(root, "pyproject.toml")) {
		patterns = append(patterns, "python pyproject")
	}
	if exists(filepath.Join(root, "requirements.txt")) {
		patterns = append(patterns, "python requirements")
	}
	if exists(filepath.Join(root, "Cargo.toml")) {
		patterns = append(patterns, "rust cargo")
	}
	if exists(filepath.Join(root, "Makefile")) {
		patterns = append(patterns, "makefile")
	}
	if exists(filepath.Join(root, "pom.xml")) {
		patterns = append(patterns, "maven")
	}
	if exists(filepath.Join(root, "build.gradle")) || exists(filepath.Join(root, "build.gradle.kts")) {
		patterns = append(patterns, "gradle")
	}
	sort.Strings(patterns)
	return patterns
}

func summarizeStructure(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	ignored := map[string]struct{}{
		".git":           {},
		".buckley":       {},
		".ralph-sandbox": {},
		".ralph-logs":    {},
		"node_modules":   {},
	}

	out := []string{}
	for _, entry := range entries {
		name := entry.Name()
		if _, skip := ignored[name]; skip {
			continue
		}
		if len(out) >= 20 {
			break
		}
		if entry.IsDir() {
			out = append(out, name+"/")
		} else {
			out = append(out, name)
		}
	}
	return out
}

func loadRules(root string) string {
	parts := []string{}
	agentsPath := filepath.Join(root, "AGENTS.md")
	if data, err := os.ReadFile(agentsPath); err == nil {
		parts = append(parts, "AGENTS.md:\n"+truncateRules(string(data)))
	}
	claudePath := filepath.Join(root, "CLAUDE.md")
	if data, err := os.ReadFile(claudePath); err == nil {
		parts = append(parts, "CLAUDE.md:\n"+truncateRules(string(data)))
	}
	return strings.Join(parts, "\n\n")
}

func truncateRules(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= maxRulesChars {
		return value
	}
	return value[:maxRulesChars] + "..."
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
