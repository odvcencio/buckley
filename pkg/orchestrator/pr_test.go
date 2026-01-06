package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/model"
)

func TestNewPRCreator(t *testing.T) {
	cfg := config.DefaultConfig()
	pc := NewPRCreator(nil, cfg)
	if pc == nil {
		t.Fatal("expected non-nil PRCreator")
	}
	if pc.cfg != cfg {
		t.Error("expected config to be set")
	}
}

func TestPRCreator_getUtilityModel(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *config.Config
		expected string
	}{
		{
			name:     "nil config uses default",
			cfg:      nil,
			expected: config.DefaultUtilityModel,
		},
		{
			name:     "default config uses default utility model",
			cfg:      config.DefaultConfig(),
			expected: config.DefaultUtilityModel,
		},
		{
			name: "custom PR model",
			cfg: func() *config.Config {
				c := config.DefaultConfig()
				c.Models.Utility.PR = "custom/model"
				return c
			}(),
			expected: "custom/model",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pc := NewPRCreator(nil, tt.cfg)
			got := pc.getUtilityModel()
			if got != tt.expected {
				t.Errorf("getUtilityModel() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestPRCreator_suggestLabels(t *testing.T) {
	pc := NewPRCreator(nil, nil)

	tests := []struct {
		name           string
		plan           *Plan
		expectLabels   []string
		unexpectLabels []string
	}{
		{
			name: "basic plan gets enhancement",
			plan: &Plan{
				Tasks: []Task{
					{Title: "Add feature X"},
				},
			},
			expectLabels: []string{"enhancement"},
		},
		{
			name: "plan with tests gets tests label",
			plan: &Plan{
				Tasks: []Task{
					{Title: "Add unit tests for feature"},
					{Title: "Test coverage improvements"},
				},
			},
			expectLabels: []string{"enhancement", "tests"},
		},
		{
			name: "plan with docs gets documentation label",
			plan: &Plan{
				Tasks: []Task{
					{Title: "Update README"},
					{Title: "Add documentation for API"},
				},
			},
			expectLabels: []string{"enhancement", "documentation"},
		},
		{
			name: "plan with both tests and docs",
			plan: &Plan{
				Tasks: []Task{
					{Title: "Add tests"},
					{Title: "Update docs"},
				},
			},
			expectLabels: []string{"enhancement", "tests", "documentation"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			labels := pc.suggestLabels(tt.plan)

			for _, expected := range tt.expectLabels {
				found := false
				for _, label := range labels {
					if label == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected label %q not found in %v", expected, labels)
				}
			}

			for _, unexpected := range tt.unexpectLabels {
				for _, label := range labels {
					if label == unexpected {
						t.Errorf("unexpected label %q found in %v", unexpected, labels)
					}
				}
			}
		})
	}
}

func TestPRCreator_buildPRPrompt(t *testing.T) {
	pc := NewPRCreator(nil, nil)

	plan := &Plan{
		FeatureName: "User Authentication",
		Description: "Add JWT-based authentication",
		Tasks: []Task{
			{Title: "Add auth middleware"},
			{Title: "Create login endpoint"},
		},
		Context: PlanContext{
			ResearchSummary: "Analyzed existing auth patterns",
			ResearchRisks:   []string{"Token expiry handling"},
		},
	}

	commits := []string{
		"abc123 feat: add auth middleware",
		"def456 feat: create login endpoint",
	}

	prompt := pc.buildPRPrompt(plan, commits)

	// Check that key information is included
	if !strings.Contains(prompt, "User Authentication") {
		t.Error("prompt should contain feature name")
	}
	if !strings.Contains(prompt, "JWT-based authentication") {
		t.Error("prompt should contain description")
	}
	if !strings.Contains(prompt, "Add auth middleware") {
		t.Error("prompt should contain task titles")
	}
	if !strings.Contains(prompt, "abc123") {
		t.Error("prompt should contain commits")
	}
	if !strings.Contains(prompt, "Analyzed existing auth patterns") {
		t.Error("prompt should contain research summary")
	}
	if !strings.Contains(prompt, "Token expiry handling") {
		t.Error("prompt should contain research risks")
	}
}

func TestPRCreator_GeneratePR_NilPlan(t *testing.T) {
	pc := NewPRCreator(nil, nil)
	_, err := pc.GeneratePR(nil)
	if err == nil {
		t.Error("expected error for nil plan")
	}
}

// mockPRModelClient implements ModelClient for testing
type mockPRModelClient struct {
	response *model.ChatResponse
	err      error
}

func (m *mockPRModelClient) ChatCompletion(_ context.Context, _ model.ChatRequest) (*model.ChatResponse, error) {
	return m.response, m.err
}

func (m *mockPRModelClient) SupportsReasoning(_ string) bool {
	return false
}
