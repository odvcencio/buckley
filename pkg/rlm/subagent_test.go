package rlm

import (
	"testing"

	"github.com/odvcencio/buckley/pkg/tool"
)

func TestNewSubAgentValidation(t *testing.T) {
	registry := tool.NewEmptyRegistry()

	tests := []struct {
		name    string
		cfg     SubAgentInstanceConfig
		deps    SubAgentDeps
		wantErr string
	}{
		{
			name:    "empty ID",
			cfg:     SubAgentInstanceConfig{ID: "", Model: "test-model"},
			deps:    SubAgentDeps{Models: nil, Registry: registry},
			wantErr: "sub-agent ID required",
		},
		{
			name:    "nil model manager",
			cfg:     SubAgentInstanceConfig{ID: "test-agent", Model: "test-model"},
			deps:    SubAgentDeps{Models: nil, Registry: registry},
			wantErr: "model manager required",
		},
		{
			name:    "nil registry",
			cfg:     SubAgentInstanceConfig{ID: "test-agent", Model: "test-model"},
			deps:    SubAgentDeps{Models: nil, Registry: nil},
			wantErr: "model manager required", // fails on models first
		},
		{
			name:    "empty model",
			cfg:     SubAgentInstanceConfig{ID: "test-agent", Model: ""},
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
