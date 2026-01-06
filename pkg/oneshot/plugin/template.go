package plugin

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
)

// TemplateEngine renders output using a template.
type TemplateEngine interface {
	Render(tmpl string, data map[string]interface{}) (string, error)
}

// GetTemplateEngine returns the appropriate engine for the template type.
func GetTemplateEngine(templateType string) (TemplateEngine, error) {
	switch templateType {
	case "simple", "":
		return &SimpleEngine{}, nil
	case "go":
		return &GoTemplateEngine{}, nil
	case "handlebars":
		return &HandlebarsEngine{}, nil
	case "jinja2":
		return &Jinja2Engine{}, nil
	default:
		return nil, fmt.Errorf("unknown template type: %s", templateType)
	}
}

// SimpleEngine uses ${var} syntax for simple variable substitution.
type SimpleEngine struct{}

func (e *SimpleEngine) Render(tmpl string, data map[string]interface{}) (string, error) {
	result := tmpl

	for key, value := range data {
		placeholder := "${" + key + "}"
		result = strings.ReplaceAll(result, placeholder, fmt.Sprint(value))
	}

	return result, nil
}

// GoTemplateEngine uses Go's text/template.
type GoTemplateEngine struct{}

func (e *GoTemplateEngine) Render(tmpl string, data map[string]interface{}) (string, error) {
	t, err := template.New("output").Funcs(templateFuncs()).Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}

	return buf.String(), nil
}

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"upper": strings.ToUpper,
		"lower": strings.ToLower,
		"title": strings.Title,
		"trim":  strings.TrimSpace,
		"join": func(sep string, items []interface{}) string {
			strs := make([]string, len(items))
			for i, item := range items {
				strs[i] = fmt.Sprint(item)
			}
			return strings.Join(strs, sep)
		},
		"joinStrings": strings.Join,
		"default": func(def, val interface{}) interface{} {
			if val == nil || val == "" {
				return def
			}
			return val
		},
		"indent": func(spaces int, s string) string {
			pad := strings.Repeat(" ", spaces)
			lines := strings.Split(s, "\n")
			for i, line := range lines {
				if line != "" {
					lines[i] = pad + line
				}
			}
			return strings.Join(lines, "\n")
		},
		"wrap": func(width int, s string) string {
			return wordWrap(s, width)
		},
		"bullet": func(items []interface{}) string {
			var lines []string
			for _, item := range items {
				lines = append(lines, "- "+fmt.Sprint(item))
			}
			return strings.Join(lines, "\n")
		},
		"numbered": func(items []interface{}) string {
			var lines []string
			for i, item := range items {
				lines = append(lines, fmt.Sprintf("%d. %s", i+1, item))
			}
			return strings.Join(lines, "\n")
		},
		"date": func(format string) string {
			// Simple date formatting - could be enhanced
			return "{{date}}"
		},
	}
}

func wordWrap(s string, width int) string {
	var lines []string
	words := strings.Fields(s)
	var current string

	for _, word := range words {
		if len(current)+len(word)+1 > width {
			if current != "" {
				lines = append(lines, current)
			}
			current = word
		} else {
			if current == "" {
				current = word
			} else {
				current += " " + word
			}
		}
	}
	if current != "" {
		lines = append(lines, current)
	}
	return strings.Join(lines, "\n")
}

// HandlebarsEngine provides Handlebars-compatible templating.
// Uses a subset of Handlebars syntax compatible with Go.
type HandlebarsEngine struct{}

func (e *HandlebarsEngine) Render(tmpl string, data map[string]interface{}) (string, error) {
	// Convert Handlebars syntax to Go template syntax
	converted := tmpl

	// {{variable}} -> {{.variable}}
	// {{#each items}}...{{/each}} -> {{range .items}}...{{end}}
	// {{#if condition}}...{{/if}} -> {{if .condition}}...{{end}}
	// {{else}} stays the same

	// Simple variable replacement: {{var}} -> {{.var}}
	// But not block helpers like {{#each}} or {{/each}}
	converted = convertHandlebarsVars(converted)

	// Block helpers
	converted = strings.ReplaceAll(converted, "{{#each ", "{{range .")
	converted = strings.ReplaceAll(converted, "{{/each}}", "{{end}}")
	converted = strings.ReplaceAll(converted, "{{#if ", "{{if .")
	converted = strings.ReplaceAll(converted, "{{/if}}", "{{end}}")
	converted = strings.ReplaceAll(converted, "{{#unless ", "{{if not .")
	converted = strings.ReplaceAll(converted, "{{/unless}}", "{{end}}")

	// Use Go template engine for rendering
	goEngine := &GoTemplateEngine{}
	return goEngine.Render(converted, data)
}

func convertHandlebarsVars(tmpl string) string {
	// This is a simplified conversion - handles {{var}} but not complex expressions
	result := tmpl
	i := 0
	for i < len(result) {
		start := strings.Index(result[i:], "{{")
		if start == -1 {
			break
		}
		start += i

		// Skip block helpers and special syntax
		if start+2 < len(result) {
			next := result[start+2]
			if next == '#' || next == '/' || next == '>' || next == '!' || next == '^' {
				i = start + 2
				continue
			}
		}

		// Find the end
		end := strings.Index(result[start:], "}}")
		if end == -1 {
			break
		}
		end += start

		// Get the content between {{ and }}
		content := result[start+2 : end]
		content = strings.TrimSpace(content)

		// Skip if already has a dot prefix or is a keyword
		if strings.HasPrefix(content, ".") || content == "else" || strings.HasPrefix(content, "range") || strings.HasPrefix(content, "if") || strings.HasPrefix(content, "end") {
			i = end + 2
			continue
		}

		// Add dot prefix for variable access
		newContent := "{{." + content + "}}"
		result = result[:start] + newContent + result[end+2:]
		i = start + len(newContent)
	}
	return result
}

// Jinja2Engine provides Jinja2-compatible templating.
// Uses a subset of Jinja2 syntax compatible with Go.
type Jinja2Engine struct{}

func (e *Jinja2Engine) Render(tmpl string, data map[string]interface{}) (string, error) {
	// Convert Jinja2 syntax to Go template syntax
	converted := tmpl

	// {{ variable }} -> {{.variable}}
	// {% for item in items %}...{% endfor %} -> {{range .items}}...{{end}}
	// {% if condition %}...{% endif %} -> {{if .condition}}...{{end}}
	// {{ variable | filter }} -> {{filter .variable}}

	// Variable expressions
	converted = convertJinja2Vars(converted)

	// Block statements
	converted = convertJinja2Blocks(converted)

	// Use Go template engine for rendering
	goEngine := &GoTemplateEngine{}
	return goEngine.Render(converted, data)
}

func convertJinja2Vars(tmpl string) string {
	result := tmpl
	i := 0
	for i < len(result) {
		start := strings.Index(result[i:], "{{")
		if start == -1 {
			break
		}
		start += i

		end := strings.Index(result[start:], "}}")
		if end == -1 {
			break
		}
		end += start

		content := strings.TrimSpace(result[start+2 : end])

		// Handle filters: {{ var | filter }}
		if strings.Contains(content, "|") {
			parts := strings.SplitN(content, "|", 2)
			varName := strings.TrimSpace(parts[0])
			filter := strings.TrimSpace(parts[1])

			// Map common Jinja2 filters to Go template funcs
			newContent := fmt.Sprintf("{{%s .%s}}", mapJinja2Filter(filter), varName)
			result = result[:start] + newContent + result[end+2:]
			i = start + len(newContent)
			continue
		}

		// Skip if already has a dot prefix
		if strings.HasPrefix(content, ".") {
			i = end + 2
			continue
		}

		// Add dot prefix
		newContent := "{{." + content + "}}"
		result = result[:start] + newContent + result[end+2:]
		i = start + len(newContent)
	}
	return result
}

func convertJinja2Blocks(tmpl string) string {
	result := tmpl

	// {% for item in items %} -> {{range .items}}
	// This is simplified - real Jinja2 has more complex for syntax
	result = strings.ReplaceAll(result, "{% endfor %}", "{{end}}")
	result = strings.ReplaceAll(result, "{% endif %}", "{{end}}")
	result = strings.ReplaceAll(result, "{% else %}", "{{else}}")

	// Handle for loops: {% for x in items %}
	for {
		start := strings.Index(result, "{% for ")
		if start == -1 {
			break
		}
		end := strings.Index(result[start:], "%}")
		if end == -1 {
			break
		}
		end += start

		// Parse: for item in items
		content := result[start+7 : end]
		content = strings.TrimSpace(content)

		// Extract "items" from "item in items"
		if inIdx := strings.Index(content, " in "); inIdx != -1 {
			items := strings.TrimSpace(content[inIdx+4:])
			newContent := "{{range ." + items + "}}"
			result = result[:start] + newContent + result[end+2:]
		} else {
			break
		}
	}

	// Handle if: {% if condition %}
	for {
		start := strings.Index(result, "{% if ")
		if start == -1 {
			break
		}
		end := strings.Index(result[start:], "%}")
		if end == -1 {
			break
		}
		end += start

		content := strings.TrimSpace(result[start+6 : end])
		newContent := "{{if ." + content + "}}"
		result = result[:start] + newContent + result[end+2:]
	}

	return result
}

func mapJinja2Filter(filter string) string {
	switch filter {
	case "upper":
		return "upper"
	case "lower":
		return "lower"
	case "title":
		return "title"
	case "trim":
		return "trim"
	case "default":
		return "default"
	default:
		return filter // Pass through unknown filters
	}
}
