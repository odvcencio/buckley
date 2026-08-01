package persona

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrNotAPersonaFile marks a file that has no YAML frontmatter block, so
// directory discovery can skip stray non-agent markdown files instead of
// treating them as malformed persona files.
var ErrNotAPersonaFile = errors.New("persona: not a persona file, no frontmatter block")

// rawFrontmatter mirrors the exact field names tiller's own agent files use
// (name, description, tools, model), plus the OpenCode fields (mode,
// permission) and buckley-specific additive fields (tier, step_cap, color).
// Unknown keys are ignored, so a tiller file with fields this struct does
// not know about still parses.
type rawFrontmatter struct {
	Name        string               `yaml:"name"`
	Description string               `yaml:"description"`
	Mode        string               `yaml:"mode"`
	Model       string               `yaml:"model"`
	Tier        string               `yaml:"tier"`
	Tools       string               `yaml:"tools"`
	Permission  map[string]yaml.Node `yaml:"permission"`
	StepCap     int                  `yaml:"step_cap"`
	Color       string               `yaml:"color"`
}

// Parse parses a tiller-compatible persona file's bytes into a Persona.
// sourcePath is recorded on the result and used to derive a fallback name
// when the frontmatter omits "name"; pass "" when there is no source file.
func Parse(data []byte, sourcePath string) (Persona, error) {
	frontmatter, body, err := splitFrontmatter(string(data))
	if err != nil {
		return Persona{}, err
	}

	var raw rawFrontmatter
	if err := yaml.Unmarshal([]byte(frontmatter), &raw); err != nil {
		return Persona{}, fmt.Errorf("persona: parsing frontmatter in %s: %w", sourcePath, err)
	}

	name := strings.TrimSpace(raw.Name)
	if name == "" {
		name = personaNameFromPath(sourcePath)
	}
	if name == "" {
		return Persona{}, fmt.Errorf("persona: %s: missing required \"name\" field", sourcePath)
	}

	mode := ModeSubagent
	if strings.EqualFold(strings.TrimSpace(raw.Mode), string(ModePrimary)) {
		mode = ModePrimary
	}

	p := Persona{
		Name:                name,
		Description:         strings.TrimSpace(raw.Description),
		Mode:                mode,
		Model:               strings.TrimSpace(raw.Model),
		Tier:                Tier(strings.ToLower(strings.TrimSpace(raw.Tier))),
		Prompt:              strings.TrimSpace(body),
		AllowedTools:        splitTools(raw.Tools),
		PermissionOverrides: stringifyPermission(raw.Permission),
		StepCap:             raw.StepCap,
		Color:               strings.TrimSpace(raw.Color),
		SourcePath:          sourcePath,
	}
	return p, nil
}

// splitFrontmatter splits content into its YAML frontmatter block and the
// markdown body that follows it. It expects the tiller/Claude Code/OpenCode
// convention: a "---" line, the YAML block, then a closing "---" line.
func splitFrontmatter(content string) (frontmatter, body string, err error) {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	trimmed := strings.TrimLeft(content, "\n")
	lines := strings.Split(trimmed, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", "", ErrNotAPersonaFile
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			frontmatter = strings.Join(lines[1:i], "\n")
			body = strings.Join(lines[i+1:], "\n")
			return frontmatter, body, nil
		}
	}
	return "", "", fmt.Errorf("persona: unterminated frontmatter block")
}

// splitTools parses tiller's Claude Code "tools" field: a single comma
// separated scalar, e.g. "Read, Glob, Grep, Edit, Write, Bash". Returns nil
// when the field is empty, meaning "inherit all tools".
func splitTools(tools string) []string {
	tools = strings.TrimSpace(tools)
	if tools == "" {
		return nil
	}
	parts := strings.Split(tools, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// stringifyPermission converts OpenCode's nested "permission" frontmatter
// into opaque per-key strings. Scalar values (e.g. "edit: ask") become their
// plain value; nested maps (e.g. glob-keyed "bash" rules) are re-encoded as
// a YAML block. pkg/policy will interpret this data in a later increment;
// here it stays opaque.
func stringifyPermission(permission map[string]yaml.Node) map[string]string {
	if len(permission) == 0 {
		return nil
	}
	out := make(map[string]string, len(permission))
	for key, node := range permission {
		node := node
		if node.Kind == yaml.ScalarNode {
			out[key] = strings.TrimSpace(node.Value)
			continue
		}
		encoded, err := yaml.Marshal(&node)
		if err != nil {
			continue
		}
		out[key] = strings.TrimSpace(string(encoded))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func personaNameFromPath(path string) string {
	base := filepath.Base(strings.TrimSpace(path))
	return strings.TrimSuffix(base, filepath.Ext(base))
}
