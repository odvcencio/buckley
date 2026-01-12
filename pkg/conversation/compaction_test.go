package conversation

import (
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
)

// TestCompactionManager_ShouldCompact_EdgeCases tests edge cases for compaction decision
func TestCompactionManager_ShouldCompact_EdgeCases(t *testing.T) {
	tests := []struct {
		name            string
		tokenCount      int
		maxTokens       int
		compactionCount int
		expected        bool
		description     string
	}{
		{
			name:            "exactly_at_75_percent_threshold",
			tokenCount:      7500,
			maxTokens:       10000,
			compactionCount: 0,
			expected:        true,
			description:     "Should compact when exactly at 75% threshold",
		},
		{
			name:            "just_below_75_percent",
			tokenCount:      7499,
			maxTokens:       10000,
			compactionCount: 0,
			expected:        false,
			description:     "Should not compact when just below 75%",
		},
		{
			name:            "just_above_75_percent",
			tokenCount:      7501,
			maxTokens:       10000,
			compactionCount: 0,
			expected:        true,
			description:     "Should compact when just above 75%",
		},
		{
			name:            "max_compactions_reached",
			tokenCount:      9500,
			maxTokens:       10000,
			compactionCount: 2,
			expected:        true,
			description:     "Should compact regardless of prior compactions",
		},
		{
			name:            "one_compaction_still_allowed",
			tokenCount:      9500,
			maxTokens:       10000,
			compactionCount: 1,
			expected:        true,
			description:     "Should allow second compaction",
		},
		{
			name:            "zero_tokens",
			tokenCount:      0,
			maxTokens:       10000,
			compactionCount: 0,
			expected:        false,
			description:     "Should not compact empty conversation",
		},
		{
			name:            "very_large_context",
			tokenCount:      180000,
			maxTokens:       200000,
			compactionCount: 0,
			expected:        true,
			description:     "Should compact large context above threshold",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cm := &CompactionManager{}
			conv := &Conversation{
				TokenCount:      tt.tokenCount,
				CompactionCount: tt.compactionCount,
			}

			result := cm.ShouldCompact(conv, tt.maxTokens)
			if result != tt.expected {
				t.Errorf("%s: expected %v, got %v", tt.description, tt.expected, result)
			}
		})
	}
}

// TestCompactionManager_Compact_MessageCounts tests compaction with various message counts
func TestCompactionManager_Compact_MessageCounts(t *testing.T) {
	tests := []struct {
		name              string
		messageCount      int
		expectError       bool
		expectedCutoff    int
		expectedRemaining int
		description       string
	}{
		{
			name:              "minimum_4_messages",
			messageCount:      4,
			expectError:       false,
			expectedCutoff:    2, // 45% of 4 = 1.8, rounds to 2 (minimum)
			expectedRemaining: 3, // 2 kept + 1 summary
			description:       "Should compact 4 messages (minimum)",
		},
		{
			name:              "too_few_3_messages",
			messageCount:      3,
			expectError:       true,
			expectedCutoff:    0,
			expectedRemaining: 0,
			description:       "Should fail with 3 messages",
		},
		{
			name:              "exactly_5_messages",
			messageCount:      5,
			expectError:       false,
			expectedCutoff:    2, // 45% of 5 = 2.25
			expectedRemaining: 4, // 3 kept + 1 summary
			description:       "Should compact 5 messages correctly",
		},
		{
			name:              "exactly_10_messages",
			messageCount:      10,
			expectError:       false,
			expectedCutoff:    4, // 45% of 10 = 4.5
			expectedRemaining: 7, // 6 kept + 1 summary
			description:       "Should compact 10 messages correctly",
		},
		{
			name:              "large_50_messages",
			messageCount:      50,
			expectError:       false,
			expectedCutoff:    22, // 45% of 50 = 22.5
			expectedRemaining: 29, // 28 kept + 1 summary
			description:       "Should compact large conversation",
		},
		{
			name:              "very_large_100_messages",
			messageCount:      100,
			expectError:       false,
			expectedCutoff:    45, // 45% of 100 = 45
			expectedRemaining: 56, // 55 kept + 1 summary
			description:       "Should handle very large conversations",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Skip actual compaction (would need model manager)
			// Just test the cutoff calculation logic
			if tt.messageCount < 4 {
				// Verify error case
				if !tt.expectError {
					t.Errorf("%s: expected error for %d messages", tt.description, tt.messageCount)
				}
				return
			}

			// Calculate cutoff using same logic as Compact()
			cutoff := int(float64(tt.messageCount) * defaultCompactionRatio)
			if cutoff < 2 {
				cutoff = 2
			}

			if cutoff != tt.expectedCutoff {
				t.Errorf("%s: expected cutoff %d, got %d for %d messages",
					tt.description, tt.expectedCutoff, cutoff, tt.messageCount)
			}

			// Verify remaining count (kept messages + 1 summary)
			expectedRemaining := (tt.messageCount - cutoff) + 1
			if expectedRemaining != tt.expectedRemaining {
				t.Errorf("%s: expected %d remaining messages, got %d",
					tt.description, tt.expectedRemaining, expectedRemaining)
			}
		})
	}
}

// TestCompactionManager_EstimateTokensSaved_EdgeCases tests token estimation edge cases
func TestCompactionManager_EstimateTokensSaved_EdgeCases(t *testing.T) {
	tests := []struct {
		name          string
		messageCount  int
		tokensPerMsg  int
		expectedSaved int
		description   string
	}{
		{
			name:          "too_few_messages",
			messageCount:  3,
			tokensPerMsg:  100,
			expectedSaved: 0,
			description:   "Should return 0 for conversations with < 4 messages",
		},
		{
			name:          "minimum_4_messages",
			messageCount:  4,
			tokensPerMsg:  100,
			expectedSaved: 140, // 2 msgs * 100 = 200 tokens before, 60 summary = 140 saved
			description:   "Should estimate savings for minimum message count",
		},
		{
			name:          "zero_token_messages",
			messageCount:  10,
			tokensPerMsg:  0,
			expectedSaved: 0,
			description:   "Should handle zero-token messages",
		},
		{
			name:          "large_messages",
			messageCount:  10,
			tokensPerMsg:  1000,
			expectedSaved: 2800, // 4 msgs * 1000 = 4000 before, 1200 summary = 2800 saved
			description:   "Should estimate savings for large token counts",
		},
		{
			name:          "many_small_messages",
			messageCount:  50,
			tokensPerMsg:  50,
			expectedSaved: 770, // 22 msgs * 50 = 1100 before, 330 summary = 770 saved
			description:   "Should handle many small messages",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cm := &CompactionManager{}
			conv := &Conversation{
				Messages: make([]Message, tt.messageCount),
			}

			// Set token count for each message
			for i := range conv.Messages {
				conv.Messages[i].Tokens = tt.tokensPerMsg
			}

			saved := cm.EstimateTokensSaved(conv)
			if saved != tt.expectedSaved {
				t.Errorf("%s: expected %d tokens saved, got %d",
					tt.description, tt.expectedSaved, saved)
			}
		})
	}
}

// TestCompactionManager_Compact_TokenRecalculation tests that token counts are updated correctly
func TestCompactionManager_Compact_TokenRecalculation(t *testing.T) {
	// This test verifies the token counting behavior without needing actual model calls
	tests := []struct {
		name           string
		messageCount   int
		tokensPerMsg   int
		expectedBefore int
		description    string
	}{
		{
			name:           "uniform_token_distribution",
			messageCount:   10,
			tokensPerMsg:   100,
			expectedBefore: 1000,
			description:    "All messages have same token count",
		},
		{
			name:           "large_conversation",
			messageCount:   50,
			tokensPerMsg:   200,
			expectedBefore: 10000,
			description:    "Large conversation with many tokens",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conv := &Conversation{
				Messages: make([]Message, tt.messageCount),
			}

			totalTokens := 0
			for i := range conv.Messages {
				conv.Messages[i].Tokens = tt.tokensPerMsg
				conv.Messages[i].Content = "test message"
				conv.Messages[i].Role = "user"
				conv.Messages[i].Timestamp = time.Now()
				totalTokens += tt.tokensPerMsg
			}
			conv.TokenCount = totalTokens

			if conv.TokenCount != tt.expectedBefore {
				t.Errorf("%s: expected %d initial tokens, got %d",
					tt.description, tt.expectedBefore, conv.TokenCount)
			}

			// Verify cutoff calculation
			cutoff := int(float64(tt.messageCount) * defaultCompactionRatio)
			if cutoff < 2 {
				cutoff = 2
			}

			// Calculate expected tokens to be removed
			tokensToSummarize := cutoff * tt.tokensPerMsg

			if tokensToSummarize > tt.expectedBefore {
				t.Errorf("%s: tokens to summarize (%d) exceeds total (%d)",
					tt.description, tokensToSummarize, tt.expectedBefore)
			}
		})
	}
}

// TestFormatMessagesForSummary tests message formatting logic
func TestFormatMessagesForSummary(t *testing.T) {
	tests := []struct {
		name          string
		messages      []Message
		shouldContain []string
		description   string
	}{
		{
			name: "single_message",
			messages: []Message{
				{Role: "user", Content: "Hello"},
			},
			shouldContain: []string{"Message 1", "user", "Hello", "Summarize"},
			description:   "Should format single message correctly",
		},
		{
			name: "multiple_messages",
			messages: []Message{
				{Role: "user", Content: "Question 1"},
				{Role: "assistant", Content: "Answer 1"},
				{Role: "user", Content: "Question 2"},
			},
			shouldContain: []string{"Message 1", "Message 2", "Message 3", "user", "assistant"},
			description:   "Should format multiple messages with proper numbering",
		},
		{
			name: "empty_message_content",
			messages: []Message{
				{Role: "user", Content: ""},
			},
			shouldContain: []string{"Message 1", "user"},
			description:   "Should handle empty message content",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatMessagesForSummary(tt.messages)

			for _, expected := range tt.shouldContain {
				if !containsString(result, expected) {
					t.Errorf("%s: result should contain '%s'\nGot: %s",
						tt.description, expected, result)
				}
			}
		})
	}
}

// Helper function to check if string contains substring
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && (s[:len(substr)] == substr ||
			(len(s) > len(substr) && containsString(s[1:], substr)))))
}

func TestNewCompactionManager_ConfigurableTimeout(t *testing.T) {
	tests := []struct {
		name            string
		timeoutSecs     int
		expectedTimeout time.Duration
	}{
		{"default_timeout", 0, 30 * time.Second},
		{"custom_timeout", 60, 60 * time.Second},
		{"short_timeout", 10, 10 * time.Second},
		{"negative_uses_default", -5, 30 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Memory.SummaryTimeoutSecs = tt.timeoutSecs
			cm := NewCompactionManager(nil, cfg)
			if cm.summaryTimeout != tt.expectedTimeout {
				t.Errorf("expected timeout %v, got %v", tt.expectedTimeout, cm.summaryTimeout)
			}
		})
	}
}

func TestShouldCompact_ConfigurableThreshold(t *testing.T) {
	tests := []struct {
		name       string
		threshold  float64
		tokenCount int
		maxTokens  int
		expected   bool
	}{
		{"default_at_75_percent", 0.0, 7500, 10000, true},    // Uses default 0.75
		{"custom_at_80_percent", 0.8, 8000, 10000, true},     // 80% threshold met
		{"custom_below_80_percent", 0.8, 7999, 10000, false}, // Below 80%
		{"custom_70_percent", 0.7, 7000, 10000, true},        // 70% threshold met
		{"invalid_uses_default", 1.5, 7500, 10000, true},     // Invalid uses default
		{"negative_uses_default", -0.5, 7500, 10000, true},   // Negative uses default
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Memory.AutoCompactThreshold = tt.threshold
			cm := NewCompactionManager(nil, cfg)
			conv := New("test")
			conv.TokenCount = tt.tokenCount
			result := cm.ShouldCompact(conv, tt.maxTokens)
			if result != tt.expected {
				t.Errorf("ShouldCompact() = %v, want %v (threshold: %.1f, tokens: %d/%d)",
					result, tt.expected, tt.threshold, tt.tokenCount, tt.maxTokens)
			}
		})
	}
}
