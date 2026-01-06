package model

import (
	"testing"

	"github.com/odvcencio/buckley/pkg/config"
)

func TestProviderFactory(t *testing.T) {
	tests := []struct {
		name          string
		cfg           *config.Config
		expectedCount int
		expectError   bool
		expectedIDs   []string
	}{
		{
			name: "openrouter_only",
			cfg: &config.Config{
				Providers: config.ProviderConfig{
					OpenRouter: config.ProviderSettings{
						Enabled: true,
						APIKey:  "test-key",
					},
				},
			},
			expectedCount: 1,
			expectedIDs:   []string{"openrouter"},
			expectError:   false,
		},
		{
			name: "multiple_providers",
			cfg: &config.Config{
				Providers: config.ProviderConfig{
					OpenRouter: config.ProviderSettings{
						Enabled: true,
						APIKey:  "test-key-1",
					},
					OpenAI: config.ProviderSettings{
						Enabled: true,
						APIKey:  "test-key-2",
					},
				},
			},
			expectedCount: 2,
			expectedIDs:   []string{"openrouter", "openai"},
			expectError:   false,
		},
		{
			name: "all_providers",
			cfg: &config.Config{
				Providers: config.ProviderConfig{
					OpenRouter: config.ProviderSettings{
						Enabled: true,
						APIKey:  "test-key-1",
					},
					OpenAI: config.ProviderSettings{
						Enabled: true,
						APIKey:  "test-key-2",
					},
					Anthropic: config.ProviderSettings{
						Enabled: true,
						APIKey:  "test-key-3",
					},
					Google: config.ProviderSettings{
						Enabled: true,
						APIKey:  "test-key-4",
					},
				},
			},
			expectedCount: 4,
			expectedIDs:   []string{"openrouter", "openai", "anthropic", "google"},
			expectError:   false,
		},
		{
			name: "no_enabled_providers",
			cfg: &config.Config{
				Providers: config.ProviderConfig{
					OpenRouter: config.ProviderSettings{
						Enabled: false,
					},
				},
			},
			expectError: true,
		},
		{
			name: "enabled_but_no_api_key",
			cfg: &config.Config{
				Providers: config.ProviderConfig{
					OpenRouter: config.ProviderSettings{
						Enabled: true,
						APIKey:  "",
					},
				},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			providers, err := providerFactory(tt.cfg)

			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(providers) != tt.expectedCount {
				t.Errorf("provider count = %d, want %d", len(providers), tt.expectedCount)
			}

			// Verify expected provider IDs
			for _, expectedID := range tt.expectedIDs {
				if _, ok := providers[expectedID]; !ok {
					t.Errorf("expected provider %q not found", expectedID)
				}
			}
		})
	}
}

func TestNormalizeModelForProvider(t *testing.T) {
	tests := []struct {
		name       string
		modelID    string
		providerID string
		expected   string
	}{
		{
			name:       "strips_matching_prefix",
			modelID:    "openai/gpt-4",
			providerID: "openai",
			expected:   "gpt-4",
		},
		{
			name:       "no_prefix_to_strip",
			modelID:    "gpt-4",
			providerID: "openai",
			expected:   "gpt-4",
		},
		{
			name:       "different_provider_prefix",
			modelID:    "openai/gpt-4",
			providerID: "anthropic",
			expected:   "openai/gpt-4",
		},
		{
			name:       "anthropic_model",
			modelID:    "anthropic/claude-3",
			providerID: "anthropic",
			expected:   "claude-3",
		},
		{
			name:       "empty_model",
			modelID:    "",
			providerID: "openai",
			expected:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeModelForProvider(tt.modelID, tt.providerID)
			if got != tt.expected {
				t.Errorf("normalizeModelForProvider(%q, %q) = %q, want %q",
					tt.modelID, tt.providerID, got, tt.expected)
			}
		})
	}
}

func TestMessageContentToText(t *testing.T) {
	tests := []struct {
		name     string
		content  any
		expected string
	}{
		{
			name:     "string_content",
			content:  "Hello, world!",
			expected: "Hello, world!",
		},
		{
			name: "content_parts_slice",
			content: []ContentPart{
				{Type: "text", Text: "Part 1"},
				{Type: "text", Text: "Part 2"},
				{Type: "image_url", ImageURL: &ImageURL{URL: "https://example.com/image.png"}},
			},
			expected: "Part 1\nPart 2",
		},
		{
			name: "any_slice_with_maps",
			content: []any{
				map[string]any{"type": "text", "text": "First"},
				map[string]any{"type": "text", "text": "Second"},
				map[string]any{"type": "image_url", "url": "https://example.com/img.png"},
			},
			expected: "First\nSecond",
		},
		{
			name: "mixed_content_parts",
			content: []ContentPart{
				{Type: "text", Text: "Only text"},
			},
			expected: "Only text",
		},
		{
			name:     "empty_string",
			content:  "",
			expected: "",
		},
		{
			name:     "nil_content",
			content:  nil,
			expected: "<nil>",
		},
		{
			name:     "number_content",
			content:  42,
			expected: "42",
		},
		{
			name: "nested_any_slice",
			content: []any{
				map[string]any{"type": "text", "text": "Nested text"},
			},
			expected: "Nested text",
		},
		{
			name:     "empty_content_parts",
			content:  []ContentPart{},
			expected: "",
		},
		{
			name: "content_parts_without_text",
			content: []ContentPart{
				{Type: "image_url", ImageURL: &ImageURL{URL: "https://example.com/image.png"}},
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := messageContentToText(tt.content)
			if got != tt.expected {
				t.Errorf("messageContentToText() = %q, want %q", got, tt.expected)
			}
		})
	}
}
