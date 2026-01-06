package skill

import (
	"embed"
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
	for _, dir := range pluginDirs {
		if err := l.loadFromDirectory(dir, "plugin", skills); err != nil {
			// Plugin directories may not exist, which is fine
			continue
		}
	}

	return nil
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

// loadFromDirectory loads all SKILL.md files from a directory
func (l *Loader) loadFromDirectory(dir, source string, skills map[string]*Skill) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // Directory not existing is not an error
		}
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			// Check for SKILL.md in subdirectory
			skillFile := filepath.Join(dir, entry.Name(), "SKILL.md")
			if err := l.loadSkillFile(skillFile, source, skills); err != nil {
				// Log but don't fail on individual skill errors
				continue
			}
		} else if strings.HasSuffix(entry.Name(), ".md") {
			// Load markdown file directly
			skillFile := filepath.Join(dir, entry.Name())
			if err := l.loadSkillFile(skillFile, source, skills); err != nil {
				continue
			}
		}
	}

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
