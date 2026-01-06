package conversation

import (
	"strings"
	"testing"
)

// BenchmarkEstimateTokens benchmarks the token estimation function
func BenchmarkEstimateTokens(b *testing.B) {
	testCases := []struct {
		name string
		text string
	}{
		{
			name: "short_message",
			text: "Hello, how are you today?",
		},
		{
			name: "medium_message",
			text: strings.Repeat("This is a medium length message. ", 10),
		},
		{
			name: "long_message",
			text: strings.Repeat("This is a longer message with more content to process. ", 100),
		},
		{
			name: "code_snippet",
			text: `func main() {
    fmt.Println("Hello, World!")
    for i := 0; i < 10; i++ {
        fmt.Println(i)
    }
}`,
		},
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = estimateTokens(tc.text)
			}
		})
	}
}

// BenchmarkConversationAddUserMessage benchmarks adding user messages
func BenchmarkConversationAddUserMessage(b *testing.B) {
	conv := New("bench-session")
	message := "This is a benchmark test message"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		conv.AddUserMessage(message)
	}
}

// BenchmarkConversationAddAssistantMessage benchmarks adding assistant messages
func BenchmarkConversationAddAssistantMessage(b *testing.B) {
	conv := New("bench-session")
	message := "This is a benchmark assistant response"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		conv.AddAssistantMessage(message)
	}
}

// BenchmarkGetContentAsString benchmarks content extraction
func BenchmarkGetContentAsString(b *testing.B) {
	testCases := []struct {
		name    string
		content any
	}{
		{
			name:    "string_content",
			content: "Simple string content",
		},
		{
			name: "multimodal_text_only",
			content: []any{
				map[string]any{"type": "text", "text": "First part"},
				map[string]any{"type": "text", "text": "Second part"},
			},
		},
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = GetContentAsString(tc.content)
			}
		})
	}
}

// BenchmarkConversationWithManyMessages benchmarks performance with growing message lists
func BenchmarkConversationWithManyMessages(b *testing.B) {
	sizes := []int{10, 50, 100, 500}

	for _, size := range sizes {
		b.Run(strings.ReplaceAll(b.Name(), "BenchmarkConversationWithManyMessages/", "")+"/"+string(rune(size)), func(b *testing.B) {
			conv := New("bench-session")
			for i := 0; i < size; i++ {
				conv.AddUserMessage("Message " + string(rune(i)))
				conv.AddAssistantMessage("Response " + string(rune(i)))
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				conv.AddUserMessage("New message")
			}
		})
	}
}

// BenchmarkConversationTokenCounting benchmarks token counting across messages
func BenchmarkConversationTokenCounting(b *testing.B) {
	conv := New("bench-session")

	// Add various message types
	conv.AddSystemMessage("You are a helpful assistant")
	for i := 0; i < 20; i++ {
		conv.AddUserMessage("User message " + string(rune(i)))
		conv.AddAssistantMessage("Assistant response " + string(rune(i)))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		total := 0
		for _, msg := range conv.Messages {
			total += msg.Tokens
		}
		_ = total
	}
}

// BenchmarkConversationMessageCount benchmarks accessing message count
func BenchmarkConversationMessageCount(b *testing.B) {
	conv := New("bench-session")

	// Add enough messages to have significant overhead
	for i := 0; i < 100; i++ {
		conv.AddUserMessage(strings.Repeat("Message content ", 50))
		conv.AddAssistantMessage(strings.Repeat("Response content ", 50))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = len(conv.Messages)
		_ = conv.TokenCount
	}
}
