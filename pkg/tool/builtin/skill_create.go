package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/odvcencio/buckley/pkg/skill"
	"gopkg.in/yaml.v3"
)

// CreateSkillTool writes a new SKILL.md into the project or personal skill directory.
type CreateSkillTool struct {
	workDirAware
	Registry *skill.Registry
}

func (t *CreateSkillTool) Name() string {
	return "create_skill"
}

func (t *CreateSkillTool) Description() string {
	return "Create a new Buckley skill by writing a SKILL.md file under .buckley/skills/<name>/SKILL.md. Skills define reusable workflows with structured guidance for tasks like TDD, debugging, or code review. Provide name, description, and markdown body containing the skill's instructions."
}

func (t *CreateSkillTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"name": {
				Type:        "string",
				Description: "Skill name (lowercase letters, digits, hyphens).",
			},
			"description": {
				Type:        "string",
				Description: "Short trigger description for when the skill should be used.",
			},
			"body": {
				Type:        "string",
				Description: "Markdown body for SKILL.md (no frontmatter). Use imperative instructions.",
			},
			"scope": {
				Type:        "string",
				Description: "Optional scope: project (default) or personal.",
			},
			"overwrite": {
				Type:        "boolean",
				Description: "Overwrite existing SKILL.md if it already exists.",
			},
		},
		Required: []string{"name", "description", "body"},
	}
}

func (t *CreateSkillTool) Execute(params map[string]any) (*Result, error) {
	nameRaw, ok := params["name"].(string)
	if !ok {
		return &Result{Success: false, Error: "name parameter must be a string"}, nil
	}
	description, ok := params["description"].(string)
	if !ok {
		return &Result{Success: false, Error: "description parameter must be a string"}, nil
	}
	body, ok := params["body"].(string)
	if !ok {
		return &Result{Success: false, Error: "body parameter must be a string"}, nil
	}

	scope := "project"
	if rawScope, ok := params["scope"].(string); ok && strings.TrimSpace(rawScope) != "" {
		scope = strings.ToLower(strings.TrimSpace(rawScope))
	}

	overwrite := false
	if rawOverwrite, ok := params["overwrite"].(bool); ok {
		overwrite = rawOverwrite
	}

	name := normalizeSkillName(nameRaw)
	if name == "" {
		return &Result{Success: false, Error: "invalid skill name after normalization"}, nil
	}
	if len(name) > 64 {
		return &Result{Success: false, Error: "skill name must be 64 characters or less"}, nil
	}
	if !skillNamePattern.MatchString(name) {
		return &Result{Success: false, Error: "skill name must contain only lowercase letters, digits, and hyphens"}, nil
	}

	description = strings.TrimSpace(description)
	if description == "" {
		return &Result{Success: false, Error: "description cannot be empty"}, nil
	}
	if len(description) > 1024 {
		return &Result{Success: false, Error: "description must be 1024 characters or less"}, nil
	}

	body = strings.TrimSpace(body)
	if body == "" {
		return &Result{Success: false, Error: "body cannot be empty"}, nil
	}

	targetPath, err := skillPathForScope(scope, t.workDir, name)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	if info, err := os.Stat(targetPath); err == nil {
		if info.IsDir() {
			return &Result{Success: false, Error: "skill path is a directory"}, nil
		}
		if !overwrite {
			return &Result{Success: false, Error: "skill already exists; set overwrite=true to replace"}, nil
		}
	}

	content, err := buildSkillContent(name, description, body)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("failed to create directory: %v", err)}, nil
	}
	if err := os.WriteFile(targetPath, []byte(content), 0644); err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("failed to write skill file: %v", err)}, nil
	}

	reloadErr := ""
	if t.Registry != nil {
		if err := t.Registry.LoadAll(); err != nil {
			reloadErr = err.Error()
		}
	}

	message := fmt.Sprintf("Created skill %q at %s", name, targetPath)
	if reloadErr != "" {
		message += fmt.Sprintf(" (reload failed: %s)", reloadErr)
	}

	data := map[string]any{
		"name":        name,
		"description": description,
		"path":        targetPath,
		"scope":       scope,
	}
	if reloadErr != "" {
		data["reload_error"] = reloadErr
	}

	return &Result{
		Success:       true,
		ShouldAbridge: true,
		Data:          data,
		DisplayData: map[string]any{
			"message": message,
		},
	}, nil
}

var skillNamePattern = regexp.MustCompile(`^[a-z0-9-]+$`)

func normalizeSkillName(raw string) string {
	name := strings.ToLower(strings.TrimSpace(raw))
	name = strings.ReplaceAll(name, "_", "-")
	name = strings.ReplaceAll(name, " ", "-")

	var b strings.Builder
	for _, r := range name {
		if r >= 'a' && r <= 'z' {
			b.WriteRune(r)
			continue
		}
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
			continue
		}
		if r == '-' {
			b.WriteRune(r)
		}
	}

	name = strings.Trim(b.String(), "-")
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}
	return name
}

func buildSkillContent(name, description, body string) (string, error) {
	frontmatter := struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}{
		Name:        name,
		Description: description,
	}
	frontBytes, err := yaml.Marshal(frontmatter)
	if err != nil {
		return "", fmt.Errorf("failed to encode frontmatter: %w", err)
	}

	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString(string(frontBytes))
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimSpace(body))
	b.WriteString("\n")
	return b.String(), nil
}

func skillPathForScope(scope, workDir, name string) (string, error) {
	switch scope {
	case "project":
		rel := filepath.Join(".buckley", "skills", name, "SKILL.md")
		return resolvePath(workDir, rel)
	case "personal":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to resolve home directory: %w", err)
		}
		return filepath.Join(home, ".buckley", "skills", name, "SKILL.md"), nil
	default:
		return "", fmt.Errorf("unsupported scope %q (use project or personal)", scope)
	}
}
