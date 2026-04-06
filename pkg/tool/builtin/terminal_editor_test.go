package builtin

import (
	"testing"
)

func TestTerminalEditorTool(t *testing.T) {
	tool := &TerminalEditorTool{}

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "edit_file_terminal" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "edit_file_terminal")
		}
		if tool.Description() == "" {
			t.Error("Description() should not be empty")
		}
		params := tool.Parameters()
		if params.Type != "object" {
			t.Errorf("Parameters().Type = %q, want %q", params.Type, "object")
		}
	})

	t.Run("missing file parameter", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for missing file")
		}
	})
}

func TestGetString(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{name: "nil value", value: nil, want: ""},
		{name: "string value", value: "hello", want: "hello"},
		{name: "empty string", value: "", want: ""},
		{name: "int value", value: 42, want: "42"},
		{name: "float value", value: 3.14, want: "3.14"},
		{name: "bool true", value: true, want: "true"},
		{name: "bool false", value: false, want: "false"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := getString(tc.value)
			if got != tc.want {
				t.Errorf("getString(%v) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

func TestValidateEditor(t *testing.T) {
	tests := []struct {
		name    string
		editor  string
		wantErr bool
	}{
		{name: "simple name", editor: "vim", wantErr: false},
		{name: "nano", editor: "nano", wantErr: false},
		{name: "emacs", editor: "emacs", wantErr: false},
		{name: "empty", editor: "", wantErr: true},
		{name: "semicolon injection", editor: "vim;rm -rf /", wantErr: true},
		{name: "pipe injection", editor: "vim|cat", wantErr: true},
		{name: "ampersand", editor: "vim&", wantErr: true},
		{name: "dollar expansion", editor: "vim$HOME", wantErr: true},
		{name: "backtick", editor: "`rm -rf /`", wantErr: true},
		{name: "subshell", editor: "$(rm -rf /)", wantErr: true},
		{name: "path with slash", editor: "/usr/bin/vim", wantErr: true},
		{name: "relative path", editor: "bin/vim", wantErr: true},
		{name: "backslash", editor: "bin\\vim", wantErr: true},
		{name: "spaces", editor: "my editor", wantErr: true},
		{name: "newline", editor: "vim\nrm", wantErr: true},
		{name: "single quotes", editor: "vim'", wantErr: true},
		{name: "double quotes", editor: "vim\"", wantErr: true},
		{name: "angle brackets", editor: "vim>out", wantErr: true},
		{name: "braces", editor: "vim{}", wantErr: true},
		{name: "hyphenated name", editor: "nvim-qt", wantErr: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateEditor(tc.editor)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateEditor(%q) error = %v, wantErr %v", tc.editor, err, tc.wantErr)
			}
		})
	}
}

func TestFirstNonEmpty(t *testing.T) {
	tests := []struct {
		name   string
		values []string
		want   string
	}{
		{name: "no values", values: nil, want: ""},
		{name: "empty slice", values: []string{}, want: ""},
		{name: "single empty", values: []string{""}, want: ""},
		{name: "single non-empty", values: []string{"foo"}, want: "foo"},
		{name: "first non-empty", values: []string{"", "", "bar", "baz"}, want: "bar"},
		{name: "whitespace only", values: []string{"   ", "  ", "foo"}, want: "foo"},
		{name: "all empty", values: []string{"", "  ", "   "}, want: ""},
		{name: "first is non-empty", values: []string{"first", "second"}, want: "first"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := firstNonEmpty(tc.values...)
			if got != tc.want {
				t.Errorf("firstNonEmpty(%v) = %q, want %q", tc.values, got, tc.want)
			}
		})
	}
}
