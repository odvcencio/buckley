package conversation

import (
	"sync"

	"github.com/pkoukk/tiktoken-go"
)

var (
	// tokenEncoder is the global tiktoken encoder
	tokenEncoder *tiktoken.Tiktoken
	encoderOnce  sync.Once
	encoderErr   error
)

// initTokenEncoder initializes the tiktoken encoder (lazy initialization)
func initTokenEncoder() error {
	encoderOnce.Do(func() {
		// Use cl100k_base encoding (GPT-4, GPT-3.5-turbo, text-embedding-ada-002)
		tokenEncoder, encoderErr = tiktoken.GetEncoding("cl100k_base")
	})
	return encoderErr
}

// CountTokens counts the number of tokens in a text using tiktoken
func CountTokens(text string) int {
	if err := initTokenEncoder(); err != nil {
		// Fallback to estimation if tiktoken fails
		return estimateTokens(text)
	}

	tokens := tokenEncoder.Encode(text, nil, nil)
	return len(tokens)
}

// CountTokensForMessages counts tokens for a list of messages
// This accounts for message formatting overhead
func CountTokensForMessages(messages []Message) int {
	if err := initTokenEncoder(); err != nil {
		// Fallback to estimation
		total := 0
		for _, msg := range messages {
			total += estimateTokens(GetContentAsString(msg.Content))
		}
		return total
	}

	total := 0

	// Each message has overhead: role, content markers, etc.
	// Based on OpenAI's token counting documentation
	for _, msg := range messages {
		// Message overhead: approximately 4 tokens per message
		total += 4

		// Role tokens
		total += len(tokenEncoder.Encode(msg.Role, nil, nil))

		// Content tokens
		total += len(tokenEncoder.Encode(GetContentAsString(msg.Content), nil, nil))
	}

	// Add 2 tokens for the overall structure
	total += 2

	return total
}

// UpdateMessageTokens updates the token count for a message
func UpdateMessageTokens(msg *Message) {
	msg.Tokens = CountTokens(GetContentAsString(msg.Content))
}

// UpdateAllTokens updates token counts for all messages in a conversation
func (c *Conversation) UpdateAllTokens() {
	total := 0
	for i := range c.Messages {
		c.Messages[i].Tokens = CountTokens(GetContentAsString(c.Messages[i].Content))
		total += c.Messages[i].Tokens
	}
	c.TokenCount = total
}

// GetAccurateTokenCount returns an accurate token count for the conversation
// This includes message formatting overhead
func (c *Conversation) GetAccurateTokenCount() int {
	return CountTokensForMessages(c.Messages)
}
