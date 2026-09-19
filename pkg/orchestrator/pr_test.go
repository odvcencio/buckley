package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
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

func TestPRCreatorGenerateDescriptionRejectsParseableTruncatedContent(t *testing.T) {
	parseableTruncated := "## Summary\n\n- Add a complete-looking PR body\n\n## Testing\n\n- go test ./pkg/orchestrator"
	pc := NewPRCreator(&mockPRModelClient{
		response: chatResponseWithFinishReason(parseableTruncated, "length"),
	}, nil)

	description, err := pc.generateDescription(&Plan{FeatureName: "Utility safety"}, []string{"abc123 fix: utility safety"})
	if err == nil {
		t.Fatal("generateDescription succeeded with an incomplete finish reason")
	}
	if description != "" {
		t.Fatalf("generateDescription returned incomplete PR content: %q", description)
	}
	var incomplete *IncompleteUtilityResponseError
	if !errors.As(err, &incomplete) {
		t.Fatalf("expected IncompleteUtilityResponseError, got %T: %v", err, err)
	}
	if got := incomplete.FinishReason(); got != "length" {
		t.Fatalf("FinishReason() = %q, want length", got)
	}
	if !strings.Contains(incomplete.PublicDraft(), "complete-looking PR body") {
		t.Fatalf("PublicDraft() omitted public draft: %q", incomplete.PublicDraft())
	}
}

func TestPRCreatorGenerateDescriptionIncompleteResponseErrorDoesNotLeakProviderDetails(t *testing.T) {
	rawProviderErr := errors.New("provider raw failure: native reasoning says SECRET_RAW")
	resp := chatResponseWithFinishReason("<think>private reasoning SECRET_THINK</think>\n## Summary\n\nPublic PR body", "length")
	pc := NewPRCreator(&mockPRModelClient{
		response: resp,
		err:      rawProviderErr,
	}, nil)

	description, err := pc.generateDescription(&Plan{FeatureName: "Utility safety"}, []string{"abc123 fix: utility safety"})
	if err == nil {
		t.Fatal("generateDescription succeeded with response plus error")
	}
	if description != "" {
		t.Fatalf("generateDescription returned incomplete PR content: %q", description)
	}
	var incomplete *IncompleteUtilityResponseError
	if !errors.As(err, &incomplete) {
		t.Fatalf("expected IncompleteUtilityResponseError, got %T: %v", err, err)
	}
	if !errors.Is(err, rawProviderErr) {
		t.Fatalf("expected provider error to unwrap from typed error")
	}
	for _, forbidden := range []string{"SECRET_RAW", "SECRET_THINK", "native reasoning", "<think>"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("Error() leaked %q: %q", forbidden, err.Error())
		}
		if strings.Contains(incomplete.PublicDraft(), forbidden) {
			t.Fatalf("PublicDraft() leaked %q: %q", forbidden, incomplete.PublicDraft())
		}
	}
	if !strings.Contains(incomplete.PublicDraft(), "Public PR body") {
		t.Fatalf("PublicDraft() omitted public content: %q", incomplete.PublicDraft())
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
