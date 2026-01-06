package plugin

import (
	"testing"
)

func TestSimpleEngine(t *testing.T) {
	engine := &SimpleEngine{}

	tests := []struct {
		tmpl string
		data map[string]interface{}
		want string
	}{
		{
			tmpl: "Hello ${name}!",
			data: map[string]interface{}{"name": "World"},
			want: "Hello World!",
		},
		{
			tmpl: "${greeting} ${name}",
			data: map[string]interface{}{"greeting": "Hi", "name": "There"},
			want: "Hi There",
		},
		{
			tmpl: "No vars here",
			data: map[string]interface{}{},
			want: "No vars here",
		},
	}

	for _, tt := range tests {
		got, err := engine.Render(tt.tmpl, tt.data)
		if err != nil {
			t.Errorf("Render(%q) error: %v", tt.tmpl, err)
			continue
		}
		if got != tt.want {
			t.Errorf("Render(%q) = %q, want %q", tt.tmpl, got, tt.want)
		}
	}
}

func TestGoTemplateEngine(t *testing.T) {
	engine := &GoTemplateEngine{}

	tests := []struct {
		tmpl string
		data map[string]interface{}
		want string
	}{
		{
			tmpl: "Hello {{.name}}!",
			data: map[string]interface{}{"name": "World"},
			want: "Hello World!",
		},
		{
			tmpl: "{{range .items}}- {{.}}\n{{end}}",
			data: map[string]interface{}{"items": []string{"a", "b", "c"}},
			want: "- a\n- b\n- c\n",
		},
		{
			tmpl: "{{if .show}}visible{{end}}",
			data: map[string]interface{}{"show": true},
			want: "visible",
		},
		{
			tmpl: "{{upper .text}}",
			data: map[string]interface{}{"text": "hello"},
			want: "HELLO",
		},
	}

	for _, tt := range tests {
		got, err := engine.Render(tt.tmpl, tt.data)
		if err != nil {
			t.Errorf("Render(%q) error: %v", tt.tmpl, err)
			continue
		}
		if got != tt.want {
			t.Errorf("Render(%q) = %q, want %q", tt.tmpl, got, tt.want)
		}
	}
}

func TestHandlebarsEngine(t *testing.T) {
	engine := &HandlebarsEngine{}

	tests := []struct {
		tmpl string
		data map[string]interface{}
		want string
	}{
		{
			tmpl: "Hello {{name}}!",
			data: map[string]interface{}{"name": "World"},
			want: "Hello World!",
		},
		{
			tmpl: "{{#each items}}- {{.}}\n{{/each}}",
			data: map[string]interface{}{"items": []string{"a", "b"}},
			want: "- a\n- b\n",
		},
		{
			tmpl: "{{#if show}}visible{{/if}}",
			data: map[string]interface{}{"show": true},
			want: "visible",
		},
	}

	for _, tt := range tests {
		got, err := engine.Render(tt.tmpl, tt.data)
		if err != nil {
			t.Errorf("Render(%q) error: %v", tt.tmpl, err)
			continue
		}
		if got != tt.want {
			t.Errorf("Render(%q) = %q, want %q", tt.tmpl, got, tt.want)
		}
	}
}

func TestJinja2Engine(t *testing.T) {
	engine := &Jinja2Engine{}

	tests := []struct {
		tmpl string
		data map[string]interface{}
		want string
	}{
		{
			tmpl: "Hello {{ name }}!",
			data: map[string]interface{}{"name": "World"},
			want: "Hello World!",
		},
		{
			tmpl: "{{ text | upper }}",
			data: map[string]interface{}{"text": "hello"},
			want: "HELLO",
		},
		{
			tmpl: "{% if show %}visible{% endif %}",
			data: map[string]interface{}{"show": true},
			want: "visible",
		},
	}

	for _, tt := range tests {
		got, err := engine.Render(tt.tmpl, tt.data)
		if err != nil {
			t.Errorf("Render(%q) error: %v", tt.tmpl, err)
			continue
		}
		if got != tt.want {
			t.Errorf("Render(%q) = %q, want %q", tt.tmpl, got, tt.want)
		}
	}
}

func TestGetTemplateEngine(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"simple", false},
		{"", false}, // default
		{"go", false},
		{"handlebars", false},
		{"jinja2", false},
		{"unknown", true},
	}

	for _, tt := range tests {
		engine, err := GetTemplateEngine(tt.name)
		if tt.wantErr {
			if err == nil {
				t.Errorf("GetTemplateEngine(%q) expected error", tt.name)
			}
		} else {
			if err != nil {
				t.Errorf("GetTemplateEngine(%q) error: %v", tt.name, err)
			}
			if engine == nil {
				t.Errorf("GetTemplateEngine(%q) returned nil engine", tt.name)
			}
		}
	}
}

func TestTemplateFuncs(t *testing.T) {
	engine := &GoTemplateEngine{}

	tests := []struct {
		tmpl string
		data map[string]interface{}
		want string
	}{
		// upper/lower/title
		{"{{upper .s}}", map[string]interface{}{"s": "hello"}, "HELLO"},
		{"{{lower .s}}", map[string]interface{}{"s": "HELLO"}, "hello"},
		{"{{title .s}}", map[string]interface{}{"s": "hello world"}, "Hello World"},

		// trim
		{"{{trim .s}}", map[string]interface{}{"s": "  hello  "}, "hello"},

		// default
		{"{{default \"fallback\" .s}}", map[string]interface{}{"s": ""}, "fallback"},
		{"{{default \"fallback\" .s}}", map[string]interface{}{"s": "value"}, "value"},

		// indent
		{"{{indent 2 .s}}", map[string]interface{}{"s": "line1\nline2"}, "  line1\n  line2"},

		// bullet/numbered
		{"{{bullet .items}}", map[string]interface{}{"items": []interface{}{"a", "b"}}, "- a\n- b"},
		{"{{numbered .items}}", map[string]interface{}{"items": []interface{}{"a", "b"}}, "1. a\n2. b"},
	}

	for _, tt := range tests {
		got, err := engine.Render(tt.tmpl, tt.data)
		if err != nil {
			t.Errorf("Render(%q) error: %v", tt.tmpl, err)
			continue
		}
		if got != tt.want {
			t.Errorf("Render(%q) = %q, want %q", tt.tmpl, got, tt.want)
		}
	}
}
