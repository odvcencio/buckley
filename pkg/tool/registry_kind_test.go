package tool

import "testing"

func TestToolKind_DefaultKinds(t *testing.T) {
	r := NewRegistry()

	tests := []struct {
		name string
		kind string
	}{
		{"read_file", "read"},
		{"write_file", "edit"},
		{"edit_file", "edit"},
		{"delete_lines", "delete"},
		{"search_text", "search"},
		{"run_shell", "execute"},
		{"explain_code", "think"},
		{"browse_url", "fetch"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.ToolKind(tt.name)
			if got != tt.kind {
				t.Errorf("ToolKind(%q) = %q, want %q", tt.name, got, tt.kind)
			}
		})
	}
}

func TestToolKind_UnregisteredTool(t *testing.T) {
	r := NewRegistry()
	if got := r.ToolKind("nonexistent"); got != "" {
		t.Errorf("ToolKind(nonexistent) = %q, want empty", got)
	}
}

func TestSetToolKind(t *testing.T) {
	r := NewEmptyRegistry()
	r.SetToolKind("my_tool", "search")

	if got := r.ToolKind("my_tool"); got != "search" {
		t.Errorf("ToolKind(my_tool) = %q, want search", got)
	}
}

func TestToolKind_NilRegistry(t *testing.T) {
	var r *Registry
	if got := r.ToolKind("anything"); got != "" {
		t.Errorf("ToolKind on nil = %q, want empty", got)
	}
	r.SetToolKind("anything", "read") // should not panic
}

func TestWithKind_Option(t *testing.T) {
	r := NewRegistry(WithKind("read_file", "other"))

	if got := r.ToolKind("read_file"); got != "other" {
		t.Errorf("ToolKind(read_file) = %q, want other (override)", got)
	}
}

func TestRemove_CleansKind(t *testing.T) {
	r := NewRegistry()
	if r.ToolKind("read_file") == "" {
		t.Fatal("expected read_file to have a kind before removal")
	}

	r.Remove("read_file")

	if got := r.ToolKind("read_file"); got != "" {
		t.Errorf("ToolKind after Remove = %q, want empty", got)
	}
}
