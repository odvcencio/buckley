package skill

import (
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// BundledSkills holds embedded skill files
//
//go:embed bundled/*.md
var BundledSkills embed.FS

// Loader handles loading skills from various sources
type Loader struct{}

// NewLoader creates a new skill loader
func NewLoader() *Loader {
	return &Loader{}
}

// LoadBundled loads skills embedded in the binary
func (l *Loader) LoadBundled(skills map[string]*Skill) error {
	entries, err := BundledSkills.ReadDir("bundled")
	if err != nil {
		// If bundled directory doesn't exist yet, that's okay
		return nil
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		path := filepath.Join("bundled", entry.Name())
		content, err := BundledSkills.ReadFile(path)
		if err != nil {
			return fmt.Errorf("failed to read bundled skill %s: %w", entry.Name(), err)
		}

		skill, err := l.parseSkillFile(string(content))
		if err != nil {
			return fmt.Errorf("failed to parse bundled skill %s: %w", entry.Name(), err)
		}

		skill.Source = "bundled"
		skill.FilePath = path
		skill.LoadedAt = time.Now()

		skills[skill.Name] = skill
	}

	return nil
}

// LoadFromPlugins loads skills from plugin directories
func (l *Loader) LoadFromPlugins(skills map[string]*Skill) error {
	pluginDirs := []string{}

	// User plugins: ~/.buckley/plugins/*/skills/
	if homeDir, err := os.UserHomeDir(); err == nil {
		userPluginBase := filepath.Join(homeDir, ".buckley", "plugins")
		if entries, err := os.ReadDir(userPluginBase); err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					skillsDir := filepath.Join(userPluginBase, entry.Name(), "skills")
					pluginDirs = append(pluginDirs, skillsDir)
				}
			}
		}
	}

	// Project plugins: ./.buckley/plugins/*/skills/
	if cwd, err := os.Getwd(); err == nil {
		projectPluginBase := filepath.Join(cwd, ".buckley", "plugins")
		if entries, err := os.ReadDir(projectPluginBase); err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					skillsDir := filepath.Join(projectPluginBase, entry.Name(), "skills")
					pluginDirs = append(pluginDirs, skillsDir)
				}
			}
		}
	}

	// Load from all discovered plugin skill directories
	var loadErrs []error
	for _, dir := range pluginDirs {
		if err := l.loadFromDirectory(dir, "plugin", skills); err != nil {
			loadErrs = append(loadErrs, err)
		}
	}

	return errors.Join(loadErrs...)
}

// LoadPersonal loads skills from ~/.buckley/skills/
func (l *Loader) LoadPersonal(skills map[string]*Skill) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil // Not an error if home dir can't be determined
	}

	personalDir := filepath.Join(homeDir, ".buckley", "skills")
	return l.loadFromDirectory(personalDir, "personal", skills)
}

// LoadProject loads skills from ./.buckley/skills/
func (l *Loader) LoadProject(skills map[string]*Skill) error {
	cwd, err := os.Getwd()
	if err != nil {
		return nil // Not an error if cwd can't be determined
	}

	projectDir := filepath.Join(cwd, ".buckley", "skills")
	return l.loadFromDirectory(projectDir, "project", skills)
}

// LoadProjectAgent loads Agent Skills-compatible files from an ancestor agent/skills directory.
func (l *Loader) LoadProjectAgent(skills map[string]*Skill) error {
	cwd, err := os.Getwd()
	if err != nil {
		return nil // Not an error if cwd can't be determined
	}
	skillDirs, err := findProjectAgentSkillDirs(cwd)
	if err != nil {
		return err
	}
	var loadErrs []error
	for _, skillsDir := range skillDirs {
		if err := l.loadAgentSkillDirectory(skillsDir, "agent", skills); err != nil {
			loadErrs = append(loadErrs, err)
		}
	}
	return errors.Join(loadErrs...)
}

// loadFromDirectory loads all SKILL.md files from a directory
func (l *Loader) loadFromDirectory(dir, source string, skills map[string]*Skill) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // Directory not existing is not an error
		}
		return err
	}

	var loadErrs []error
	for _, entry := range entries {
		if entry.IsDir() {
			// Check for SKILL.md in subdirectory
			skillFile := filepath.Join(dir, entry.Name(), "SKILL.md")
			if _, err := os.Stat(skillFile); os.IsNotExist(err) {
				continue
			}
			if err := l.loadSkillFile(skillFile, source, skills); err != nil {
				loadErrs = append(loadErrs, fmt.Errorf("%s: %w", skillFile, err))
			}
		} else if strings.HasSuffix(entry.Name(), ".md") {
			// Load markdown file directly
			skillFile := filepath.Join(dir, entry.Name())
			if err := l.loadSkillFile(skillFile, source, skills); err != nil {
				loadErrs = append(loadErrs, fmt.Errorf("%s: %w", skillFile, err))
			}
		}
	}

	return errors.Join(loadErrs...)
}

var projectAgentSkillRoots = []string{
	filepath.Join("agent", "skills"),
	filepath.Join(".agent", "skills"),
	filepath.Join(".agents", "skills"),
	filepath.Join(".codex", "skills"),
}

func findProjectAgentSkillDirs(start string) ([]string, error) {
	start = strings.TrimSpace(start)
	if start == "" {
		start = "."
	}
	dir, err := filepath.Abs(start)
	if err != nil {
		return nil, fmt.Errorf("resolve project agent skills start: %w", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("stat project agent skills start: %w", err)
	}
	if !info.IsDir() {
		dir = filepath.Dir(dir)
	}

	for {
		var found []string
		for _, rel := range projectAgentSkillRoots {
			candidate := filepath.Join(dir, rel)
			info, err := os.Stat(candidate)
			if err == nil {
				if !info.IsDir() {
					return nil, fmt.Errorf("project agent skills path is not a directory: %s", candidate)
				}
				found = append(found, candidate)
				continue
			}
			if !os.IsNotExist(err) {
				return nil, fmt.Errorf("stat project agent skills: %w", err)
			}
		}
		if len(found) > 0 {
			return found, nil
		}
		if isProjectBoundary(dir) {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return nil, nil
}

func isProjectBoundary(dir string) bool {
	for _, marker := range []string{".git", ".hg"} {
		if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
			return true
		}
	}
	return false
}

func (l *Loader) loadAgentSkillDirectory(dir, source string, skills map[string]*Skill) error {
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("agent skills path is not a directory: %s", dir)
	}

	var loadErrs []error
	walkErr := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path == dir {
				return nil
			}
			if hasAgentSkillPackage(path) {
				name, err := agentSkillNameFromPath(dir, path)
				if err != nil {
					return err
				}
				if err := l.loadAgentSkillFile(filepath.Join(path, "SKILL.md"), source, name, skills); err != nil {
					loadErrs = append(loadErrs, err)
					return filepath.SkipDir
				}
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			return nil
		}
		if strings.EqualFold(entry.Name(), "SKILL.md") {
			return nil
		}
		name, err := agentSkillNameFromPath(dir, strings.TrimSuffix(path, filepath.Ext(path)))
		if err != nil {
			return err
		}
		if err := l.loadAgentSkillFile(path, source, name, skills); err != nil {
			loadErrs = append(loadErrs, err)
		}
		return nil
	})
	if walkErr != nil {
		loadErrs = append(loadErrs, fmt.Errorf("read project agent skills: %w", walkErr))
	}
	return errors.Join(loadErrs...)
}

func hasAgentSkillPackage(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "SKILL.md"))
	return err == nil && !info.IsDir()
}

func agentSkillNameFromPath(root, path string) (string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", fmt.Errorf("resolve agent skill name: %w", err)
	}
	return filepath.ToSlash(rel), nil
}

func (l *Loader) loadAgentSkillFile(path, source, fallbackName string, skills map[string]*Skill) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	skill, err := l.parseAgentSkillFile(string(content), fallbackName)
	if err != nil {
		return fmt.Errorf("failed to parse %s: %w", path, err)
	}

	skill.Source = source
	skill.FilePath = path
	skill.LoadedAt = time.Now()
	if err := skill.Validate(); err != nil {
		return fmt.Errorf("validate %s: %w", path, err)
	}
	skills[skill.Name] = skill
	return nil
}

// loadSkillFile loads a single skill file
func (l *Loader) loadSkillFile(path, source string, skills map[string]*Skill) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	skill, err := l.parseSkillFile(string(content))
	if err != nil {
		return fmt.Errorf("failed to parse %s: %w", path, err)
	}

	skill.Source = source
	skill.FilePath = path
	skill.LoadedAt = time.Now()

	if err := skill.Validate(); err != nil {
		return err
	}

	skills[skill.Name] = skill
	return nil
}

func (l *Loader) parseAgentSkillFile(content, fallbackName string) (*Skill, error) {
	frontmatter, body, ok, err := splitOptionalYAMLFrontmatter(content)
	if err != nil {
		return nil, err
	}

	var skill Skill
	if ok {
		if err := yaml.Unmarshal([]byte(frontmatter), &skill); err != nil {
			return nil, fmt.Errorf("failed to parse YAML frontmatter: %w", err)
		}
	}
	skill.Content = strings.TrimSpace(body)
	if strings.TrimSpace(skill.Name) == "" {
		skill.Name = strings.TrimSpace(fallbackName)
	}
	if strings.TrimSpace(skill.Description) == "" {
		skill.Description = descriptionFromMarkdown(skill.Content, skill.Name)
	}
	return &skill, nil
}

func splitOptionalYAMLFrontmatter(content string) (string, string, bool, error) {
	trimmed := strings.TrimLeft(content, "\ufeff \t\r\n")
	if !strings.HasPrefix(trimmed, "---") {
		return "", content, false, nil
	}
	if len(trimmed) > 3 && trimmed[3] != '\n' && trimmed[3] != '\r' {
		return "", content, false, nil
	}
	parts := strings.SplitN(trimmed, "---", 3)
	if len(parts) < 3 {
		return "", "", false, fmt.Errorf("invalid skill file: unterminated YAML frontmatter")
	}
	return parts[1], parts[2], true, nil
}

func descriptionFromMarkdown(content, name string) string {
	inFence := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence || trimmed == "" {
			continue
		}
		trimmed = strings.TrimLeft(trimmed, "#>*- \t")
		trimmed = strings.TrimSpace(trimmed)
		if trimmed != "" {
			return limitSkillDescription(trimmed)
		}
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "project"
	}
	return fmt.Sprintf("Instructions for the %s skill.", name)
}

func limitSkillDescription(value string) string {
	const maxDescriptionChars = 1024
	if len(value) <= maxDescriptionChars {
		return value
	}
	return value[:maxDescriptionChars]
}

// parseSkillFile parses a SKILL.md file with YAML frontmatter
func (l *Loader) parseSkillFile(content string) (*Skill, error) {
	// Split frontmatter and content
	parts := strings.SplitN(content, "---", 3)
	if len(parts) < 3 {
		return nil, fmt.Errorf("invalid skill file: missing YAML frontmatter")
	}

	// Parse frontmatter
	var skill Skill
	if err := yaml.Unmarshal([]byte(parts[1]), &skill); err != nil {
		return nil, fmt.Errorf("failed to parse YAML frontmatter: %w", err)
	}

	// Store markdown content (everything after second ---)
	skill.Content = strings.TrimSpace(parts[2])

	return &skill, nil
}
