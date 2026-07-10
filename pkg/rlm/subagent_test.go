package rlm

import (
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestFormatToolResultHonorsAbridgedDisplayData(t *testing.T) {
	result := &builtin.Result{
		Success: true,
		Data: map[string]any{
			"stdout": "FULL_OUTPUT_MUST_NOT_ENTER_MODEL_CONTEXT",
		},
		ShouldAbridge: true,
		DisplayData: map[string]any{
			"status": "PASS",
			"argv":   []string{"go", "test", "."},
		},
	}
	formatted := formatToolResult(result)
	if strings.Contains(formatted, "FULL_OUTPUT_MUST_NOT_ENTER_MODEL_CONTEXT") {
		t.Fatalf("abridged tool result leaked full output: %s", formatted)
	}
	if !strings.Contains(formatted, "PASS") {
		t.Fatalf("abridged tool result omitted trusted status: %s", formatted)
	}
}

func TestNewSubAgentValidation(t *testing.T) {
	registry := tool.NewEmptyRegistry()

	tests := []struct {
		name    string
		cfg     SubAgentConfig
		deps    SubAgentDeps
		wantErr string
	}{
		{
			name:    "empty ID",
			cfg:     SubAgentConfig{ID: "", Model: "test-model"},
			deps:    SubAgentDeps{Models: nil, Registry: registry},
			wantErr: "sub-agent ID required",
		},
		{
			name:    "nil model manager",
			cfg:     SubAgentConfig{ID: "test-agent", Model: "test-model"},
			deps:    SubAgentDeps{Models: nil, Registry: registry},
			wantErr: "model manager required",
		},
		{
			name:    "nil registry",
			cfg:     SubAgentConfig{ID: "test-agent", Model: "test-model"},
			deps:    SubAgentDeps{Models: nil, Registry: nil},
			wantErr: "model manager required", // fails on models first
		},
		{
			name:    "empty model",
			cfg:     SubAgentConfig{ID: "test-agent", Model: ""},
			deps:    SubAgentDeps{Registry: registry},
			wantErr: "model manager required", // fails on models first
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewSubAgent(tt.cfg, tt.deps)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if err.Error() != tt.wantErr {
				t.Errorf("error = %q, want %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestSubAgentDefaultPrompt(t *testing.T) {
	// Verify the default prompt is set when empty
	if defaultSubAgentPrompt == "" {
		t.Fatal("defaultSubAgentPrompt should not be empty")
	}

	// Check key elements are present
	keywords := []string{
		"sub-agent",
		"summary",
		"tool",
		"Read Before Writing",
	}

	for _, kw := range keywords {
		if !containsString(defaultSubAgentPrompt, kw) {
			t.Errorf("default prompt should contain %q", kw)
		}
	}
}

func TestSubAgentMaxIterationsDefault(t *testing.T) {
	if defaultSubAgentMaxIterations <= 0 {
		t.Errorf("defaultSubAgentMaxIterations should be positive, got %d", defaultSubAgentMaxIterations)
	}
	if defaultSubAgentMaxIterations != 25 {
		t.Errorf("defaultSubAgentMaxIterations = %d, want 25", defaultSubAgentMaxIterations)
	}
}

func TestBuildToolDefinitionsFiltersToAllowedRegistryNames(t *testing.T) {
	registry := tool.NewRegistry()
	allowed := map[string]struct{}{
		"read_file": {},
		"run_shell": {},
	}

	definitions := buildToolDefinitions(registry, allowed)
	if len(definitions) != len(allowed) {
		t.Fatalf("definitions = %d, want %d", len(definitions), len(allowed))
	}
	seen := make(map[string]bool)
	for _, definition := range definitions {
		function, _ := definition["function"].(map[string]any)
		name, _ := function["name"].(string)
		seen[name] = true
	}
	for name := range allowed {
		if !seen[name] {
			t.Errorf("missing allowed tool %q from definitions: %#v", name, definitions)
		}
	}
	if seen["write_file"] {
		t.Fatal("write_file leaked into read-only review tool definitions")
	}
}

func TestReadOnlyToolSetAndRequestPolicy(t *testing.T) {
	if !isReadOnlyToolSet([]string{"read_file", "find_files", "search_text"}) {
		t.Fatal("review read tools should select read-only execution")
	}
	for _, tools := range [][]string{
		nil,
		{"read_file", "run_shell"},
		{"read_file", "write_file"},
	} {
		if isReadOnlyToolSet(tools) {
			t.Fatalf("tool set should remain writable/unsafe: %v", tools)
		}
	}

	req := model.ChatRequest{}
	applyExecutionPolicy(&req, true, nil)
	if got := req.Metadata[model.RequestMetadataReadOnly]; got != "true" {
		t.Fatalf("read-only metadata = %q, want true", got)
	}

	writable := model.ChatRequest{}
	applyExecutionPolicy(&writable, false, nil)
	if writable.Metadata != nil {
		t.Fatalf("writable request unexpectedly received policy metadata: %v", writable.Metadata)
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStringHelper(s, substr))
}

func containsStringHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
