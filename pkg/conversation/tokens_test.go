package conversation

import (
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/model"
)

func TestCountTokens(t *testing.T) {
	tests := []struct {
		name string
		text string
		min  int
		max  int
	}{
		{
			name: "short text",
			text: "Hello",
			min:  1,
			max:  2,
		},
		{
			name: "medium text",
			text: "This is a test of token counting functionality",
			min:  8,
			max:  15,
		},
		{
			name: "empty string",
			text: "",
			min:  0,
			max:  0,
		},
		{
			name: "code snippet",
			text: `func main() { fmt.Println("hello") }`,
			min:  8,
			max:  20,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			count := CountTokens(tt.text)

			// Allow for variance since tiktoken may or may not be available
			if count < tt.min || count > tt.max {
				t.Errorf("CountTokens(%q) = %d, expected between %d and %d",
					tt.text, count, tt.min, tt.max)
			}
		})
	}
}

func TestCountTokensConsistency(t *testing.T) {
	text := "The quick brown fox jumps over the lazy dog"

	// Count multiple times to ensure consistency
	count1 := CountTokens(text)
	count2 := CountTokens(text)
	count3 := CountTokens(text)

	if count1 != count2 || count2 != count3 {
		t.Errorf("Token counts not consistent: %d, %d, %d", count1, count2, count3)
	}
}

func TestCountTokensForMessages(t *testing.T) {
	messages := []Message{
		{Role: "user", Content: "Hello!", Timestamp: time.Now()},
		{Role: "assistant", Content: "Hi there! How can I help?", Timestamp: time.Now()},
		{Role: "user", Content: "I need assistance with Go", Timestamp: time.Now()},
	}

	count := CountTokensForMessages(messages)

	// Should include message overhead + content tokens
	if count < 10 {
		t.Errorf("Expected at least 10 tokens for 3 messages, got %d", count)
	}

	// Count should be more than sum of content tokens (due to overhead)
	simpleSum := 0
	for _, msg := range messages {
		simpleSum += CountTokens(GetContentAsString(msg.Content))
	}

	if count <= simpleSum {
		t.Errorf("CountTokensForMessages (%d) should be greater than simple sum (%d) due to overhead",
			count, simpleSum)
	}
}

func TestCountTokensForMessagesEmpty(t *testing.T) {
	messages := []Message{}

	count := CountTokensForMessages(messages)

	// Empty message list should still have minimal overhead
	if count < 0 {
		t.Errorf("Expected non-negative count for empty messages, got %d", count)
	}
}

func TestUpdateMessageTokens(t *testing.T) {
	msg := &Message{
		Role:      "user",
		Content:   "Test message for token counting",
		Timestamp: time.Now(),
		Tokens:    0,
	}

	UpdateMessageTokens(msg)

	if msg.Tokens == 0 {
		t.Error("Expected non-zero tokens after UpdateMessageTokens")
	}
}

func TestUpdateAllTokens(t *testing.T) {
	conv := &Conversation{
		SessionID: "test",
		Messages: []Message{
			{Role: "user", Content: "First message", Timestamp: time.Now(), Tokens: 0},
			{Role: "assistant", Content: "Second message response", Timestamp: time.Now(), Tokens: 0},
			{Role: "user", Content: "Third message", Timestamp: time.Now(), Tokens: 0},
		},
		TokenCount: 0,
	}

	conv.UpdateAllTokens()

	if conv.TokenCount == 0 {
		t.Error("Expected non-zero conversation token count")
	}

	for i, msg := range conv.Messages {
		if msg.Tokens == 0 {
			t.Errorf("Message %d should have non-zero tokens", i)
		}
	}

	// Verify TokenCount matches sum of message tokens
	sum := 0
	for _, msg := range conv.Messages {
		sum += msg.Tokens
	}

	if conv.TokenCount != sum {
		t.Errorf("TokenCount (%d) should equal sum of message tokens (%d)",
			conv.TokenCount, sum)
	}
}

func TestGetAccurateTokenCount(t *testing.T) {
	conv := &Conversation{
		SessionID: "test",
		Messages: []Message{
			{Role: "user", Content: "Hello", Timestamp: time.Now(), Tokens: 1},
			{Role: "assistant", Content: "Hi!", Timestamp: time.Now(), Tokens: 1},
		},
		TokenCount: 2,
	}

	accurate := conv.GetAccurateTokenCount()

	// Accurate count should be >= stored token count due to message overhead
	if accurate < conv.TokenCount {
		t.Errorf("Accurate token count (%d) should be >= stored count (%d)",
			accurate, conv.TokenCount)
	}
}

func TestInitTokenEncoderIdempotent(t *testing.T) {
	// Call multiple times to ensure it's safe
	err1 := initTokenEncoder()
	err2 := initTokenEncoder()
	err3 := initTokenEncoder()

	// All calls should return the same error (or nil)
	if err1 != err2 || err2 != err3 {
		t.Error("initTokenEncoder should be idempotent")
	}
}

func TestTokenCountingWithMultimodalContent(t *testing.T) {
	conv := New("test")

	// Add message with multimodal content using proper ContentPart type
	conv.Messages = append(conv.Messages, Message{
		Role: "user",
		Content: []model.ContentPart{
			{Type: "text", Text: "Look at this"},
			{Type: "image_url", ImageURL: &model.ImageURL{URL: "data:image/png;base64,abc"}},
		},
		Timestamp: time.Now(),
	})

	conv.UpdateAllTokens()

	// Should count text tokens even with multimodal content
	if conv.TokenCount == 0 {
		t.Error("Expected non-zero tokens for multimodal content")
	}
}

func TestEstimateTokensFallback(t *testing.T) {
	// Test the fallback estimation logic directly
	text := "This is exactly sixteen characters"
	estimate := estimateTokens(text)

	// Should be roughly len/4 = 35/4 ≈ 8-9 tokens
	if estimate < 6 || estimate > 12 {
		t.Errorf("Estimate %d out of expected range for text length %d", estimate, len(text))
	}
}

func TestLongContentTokenCounting(t *testing.T) {
	// Generate a long piece of content
	longText := ""
	for i := 0; i < 1000; i++ {
		longText += "word "
	}

	count := CountTokens(longText)

	// Should handle long content without crashing
	if count == 0 {
		t.Error("Expected non-zero token count for long text")
	}

	// Approximate check: 5000 characters / 4 ≈ 1250 tokens minimum
	if count < 800 {
		t.Errorf("Expected at least 800 tokens for 5000 char text, got %d", count)
	}
}

func TestTokenCountingSpecialCharacters(t *testing.T) {
	texts := []string{
		"Hello! 😊",
		"Code: `fmt.Println(\"test\")`",
		"Math: α + β = γ",
		"Chinese: 你好世界",
		"Arabic: مرحبا بك",
		"JSON: {\"key\": \"value\"}",
	}

	for _, text := range texts {
		count := CountTokens(text)
		if count == 0 && len(text) > 0 {
			t.Errorf("Expected non-zero tokens for: %s", text)
		}
	}
}
