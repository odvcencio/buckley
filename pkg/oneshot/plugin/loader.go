// Package plugin provides YAML-based one-shot command definitions.
package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/odvcencio/buckley/pkg/oneshot"
	"github.com/odvcencio/buckley/pkg/tools"
)

// Definition is the YAML structure for a one-shot plugin.
type Definition struct {
	// Name is the command name (e.g., "changelog")
	Name string `yaml:"name"`

	// Version is the plugin version
	Version string `yaml:"version"`

	// SourcePath is the file path this definition was loaded from (set by loader)
	SourcePath string `yaml:"-"`

	// Description is a short description for help text
	Description string `yaml:"description"`

	// Tool defines the model's structured output contract
	Tool ToolDef `yaml:"tool"`

	// Context defines what sources to gather
	Context []ContextSource `yaml:"context"`

	// Flags defines CLI flags for this command
	Flags []FlagDef `yaml:"flags"`

	// Output defines how to render and handle the result
	Output OutputDef `yaml:"output"`
}

// ToolDef defines the tool schema in YAML.
type ToolDef struct {
	Name        string              `yaml:"name"`
	Description string              `yaml:"description"`
	Parameters  map[string]ParamDef `yaml:"parameters"`
	Required    []string            `yaml:"required"`
}

// ParamDef defines a tool parameter.
type ParamDef struct {
	Type        string              `yaml:"type"`
	Description string              `yaml:"description"`
	Enum        []string            `yaml:"enum,omitempty"`
	Items       *ParamDef           `yaml:"items,omitempty"`
	Properties  map[string]ParamDef `yaml:"properties,omitempty"`
	MaxLength   int                 `yaml:"maxLength,omitempty"`
}

// ContextSource defines a context gathering source.
type ContextSource struct {
	Type     string `yaml:"type"` // git_log, file, agents_md, env, etc.
	Path     string `yaml:"path,omitempty"`
	Since    string `yaml:"since,omitempty"`
	Format   string `yaml:"format,omitempty"`
	Optional bool   `yaml:"optional,omitempty"`
	MaxBytes int    `yaml:"max_bytes,omitempty"`
}

// FlagDef defines a CLI flag.
type FlagDef struct {
	Name        string `yaml:"name"`
	Type        string `yaml:"type"` // string, bool, int, duration
	Description string `yaml:"description"`
	Default     string `yaml:"default,omitempty"`
	Required    bool   `yaml:"required,omitempty"`
}

// OutputDef defines how to render and handle results.
type OutputDef struct {
	Template string      `yaml:"template"` // simple, go, handlebars, jinja2
	Format   string      `yaml:"format"`   // The template string
	Actions  []ActionDef `yaml:"actions,omitempty"`
}

// ActionDef defines a post-result action.
type ActionDef struct {
	Name        string            `yaml:"name"`
	Description string            `yaml:"description"`
	Command     string            `yaml:"command"` // prepend_file, clipboard, etc.
	Args        map[string]string `yaml:"args,omitempty"`
}

// Loader discovers and loads plugin definitions.
type Loader struct {
	paths []string
}

// NewLoader creates a plugin loader that searches the given paths.
func NewLoader(paths ...string) *Loader {
	return &Loader{paths: paths}
}

// DefaultLoader creates a loader with standard search paths.
func DefaultLoader() *Loader {
	home, _ := os.UserHomeDir()
	return NewLoader(
		filepath.Join(home, ".buckley", "tools"),
		".buckley/tools",
	)
}

// LoadAll discovers and loads all plugins from search paths.
func (l *Loader) LoadAll() ([]*Definition, error) {
	var defs []*Definition

	for _, searchPath := range l.paths {
		files, err := filepath.Glob(filepath.Join(searchPath, "*.yaml"))
		if err != nil {
			continue
		}

		for _, file := range files {
			def, err := l.LoadFile(file)
			if err != nil {
				// Log warning but continue
				fmt.Fprintf(os.Stderr, "Warning: failed to load plugin %s: %v\n", file, err)
				continue
			}
			defs = append(defs, def)
		}

		// Also check .yml files
		ymlFiles, err := filepath.Glob(filepath.Join(searchPath, "*.yml"))
		if err != nil {
			continue
		}

		for _, file := range ymlFiles {
			def, err := l.LoadFile(file)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to load plugin %s: %v\n", file, err)
				continue
			}
			defs = append(defs, def)
		}
	}

	return defs, nil
}

// LoadFile loads a single plugin definition from a YAML file.
func (l *Loader) LoadFile(path string) (*Definition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}

	var def Definition
	if err := yaml.Unmarshal(data, &def); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}

	if def.Name == "" {
		return nil, fmt.Errorf("plugin name is required")
	}

	def.SourcePath = path
	return &def, nil
}

// Register loads all plugins and registers them with the oneshot registry.
func (l *Loader) Register(registry *oneshot.Registry) error {
	defs, err := l.LoadAll()
	if err != nil {
		return err
	}

	for _, def := range defs {
		cmd := def.ToCommand()
		if err := registry.Register(cmd); err != nil {
			return fmt.Errorf("register plugin %s: %w", def.Name, err)
		}
	}

	return nil
}

// ToCommand converts a plugin definition to a oneshot command.
func (d *Definition) ToCommand() *oneshot.Command {
	return &oneshot.Command{
		Name:        d.Name,
		Description: d.Description,
		Tool:        d.ToToolDefinition(),
		Builtin:     false,
		Source:      d.SourcePath,
	}
}

// ToToolDefinition converts the YAML tool def to a tools.Definition.
func (d *Definition) ToToolDefinition() tools.Definition {
	params := make(map[string]tools.Property)

	for name, p := range d.Tool.Parameters {
		params[name] = paramDefToProperty(p)
	}

	return tools.Definition{
		Name:        d.Tool.Name,
		Description: d.Tool.Description,
		Parameters:  tools.ObjectSchema(params, d.Tool.Required...),
	}
}

func paramDefToProperty(p ParamDef) tools.Property {
	prop := tools.Property{
		Type:        p.Type,
		Description: p.Description,
		MaxLength:   p.MaxLength,
	}

	if len(p.Enum) > 0 {
		prop.Enum = p.Enum
	}

	if p.Items != nil {
		items := paramDefToProperty(*p.Items)
		prop.Items = &items
	}

	// Note: Nested object properties (p.Properties) are parsed from YAML
	// but not currently mapped to tools.Property since the schema doesn't
	// support nested Properties. Complex nested schemas should use Items
	// for array elements or be defined as separate parameters.

	return prop
}

// InterpolateFlags replaces ${FLAG:name} and ${FLAG:name|default} patterns with flag values.
func InterpolateFlags(template string, flags map[string]string) string {
	result := template

	// First pass: replace ${FLAG:name} with known values
	for name, value := range flags {
		placeholder := "${FLAG:" + name + "}"
		result = strings.ReplaceAll(result, placeholder, value)
	}

	// Second pass: handle ${FLAG:name|default} patterns
	// This handles both known flags (use value) and unknown flags (use default)
	for {
		start := strings.Index(result, "${FLAG:")
		if start == -1 {
			break
		}
		end := strings.Index(result[start:], "}")
		if end == -1 {
			break
		}
		end += start

		// Extract the content between ${FLAG: and }
		content := result[start+7 : end] // 7 = len("${FLAG:")

		// Check for default syntax
		pipeIdx := strings.Index(content, "|")
		if pipeIdx == -1 {
			// No default, and flag wasn't replaced in first pass - leave as is
			// This shouldn't normally happen, but skip past this token
			result = result[:start] + result[start+1:]
			continue
		}

		name := content[:pipeIdx]
		defaultVal := content[pipeIdx+1:]

		if value, ok := flags[name]; ok && value != "" {
			result = result[:start] + value + result[end+1:]
		} else {
			result = result[:start] + defaultVal + result[end+1:]
		}
	}

	return result
}
