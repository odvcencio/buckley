package conversation

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/storage"
)

// Message represents a conversation message
type Message struct {
	Role        string
	Content     any // Can be string or []model.ContentPart for multimodal
	Timestamp   time.Time
	Tokens      int              // Estimated for Phase 1, accurate in Phase 3
	ToolCalls   []model.ToolCall // For assistant messages with tool calls
	ToolCallID  string           // For tool response messages
	Name        string           // Tool name for tool messages
	IsSummary   bool             // Indicates this message is a summary created by compaction
	IsTruncated bool             // Indicates this message was interrupted/incomplete
	Reasoning   string           // Reasoning/thinking content for reasoning models
}

// Conversation manages a conversation with the LLM
type Conversation struct {
	SessionID       string
	Messages        []Message
	TokenCount      int
	CompactionCount int
}

const (
	contentTypeText = "text"
	contentTypeJSON = "json"
)

// New creates a new conversation
func New(sessionID string) *Conversation {
	return &Conversation{
		SessionID:       sessionID,
		Messages:        []Message{},
		TokenCount:      0,
		CompactionCount: 0,
	}
}

// GetContentAsString extracts string content from a Message
// If content is multimodal, it extracts just the text parts
func GetContentAsString(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []model.ContentPart:
		return renderContentParts(v)
	case []any:
		parts := make([]model.ContentPart, 0, len(v))
		for _, raw := range v {
			if m, ok := raw.(map[string]any); ok {
				part := model.ContentPart{}
				if t, ok := m["type"].(string); ok {
					part.Type = t
				}
				if txt, ok := m["text"].(string); ok {
					part.Text = txt
				}
				parts = append(parts, part)
			}
		}
		return renderContentParts(parts)
	default:
		return ""
	}
}

func renderContentParts(parts []model.ContentPart) string {
	var texts []string
	for _, part := range parts {
		if strings.TrimSpace(part.Type) == "text" && part.Text != "" {
			texts = append(texts, part.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// AddUserMessage adds a user message
func (c *Conversation) AddUserMessage(content string) {
	msg := Message{
		Role:      "user",
		Content:   content,
		Timestamp: time.Now(),
		Tokens:    estimateTokens(content),
		IsSummary: false,
	}
	c.Messages = append(c.Messages, msg)
	c.TokenCount += msg.Tokens
}

// AddUserMessageParts adds a user message with multimodal parts.
func (c *Conversation) AddUserMessageParts(parts []model.ContentPart) {
	text := GetContentAsString(parts)
	msg := Message{
		Role:      "user",
		Content:   parts,
		Timestamp: time.Now(),
		Tokens:    estimateTokens(text),
		IsSummary: false,
	}
	c.Messages = append(c.Messages, msg)
	c.TokenCount += msg.Tokens
}

// AddAssistantMessage adds an assistant message
func (c *Conversation) AddAssistantMessage(content string) {
	c.AddAssistantMessageWithReasoning(content, "")
}

// AddAssistantMessageWithReasoning adds an assistant message with reasoning content
func (c *Conversation) AddAssistantMessageWithReasoning(content string, reasoning string) {
	tokens := estimateTokens(content) + estimateTokens(reasoning)
	msg := Message{
		Role:      "assistant",
		Content:   content,
		Timestamp: time.Now(),
		Tokens:    tokens,
		IsSummary: false,
		Reasoning: reasoning,
	}
	c.Messages = append(c.Messages, msg)
	c.TokenCount += msg.Tokens
}

// AddAssistantMessageParts adds an assistant message with multimodal parts.
func (c *Conversation) AddAssistantMessageParts(parts []model.ContentPart, reasoning string) {
	text := GetContentAsString(parts)
	tokens := estimateTokens(text) + estimateTokens(reasoning)
	msg := Message{
		Role:      "assistant",
		Content:   parts,
		Timestamp: time.Now(),
		Tokens:    tokens,
		IsSummary: false,
		Reasoning: reasoning,
	}
	c.Messages = append(c.Messages, msg)
	c.TokenCount += msg.Tokens
}

// AddSystemMessage adds a system message
func (c *Conversation) AddSystemMessage(content string) {
	msg := Message{
		Role:      "system",
		Content:   content,
		Timestamp: time.Now(),
		Tokens:    estimateTokens(content),
		IsSummary: false,
	}
	c.Messages = append(c.Messages, msg)
	c.TokenCount += msg.Tokens
}

// AddToolCallMessage adds an assistant message with tool calls
func (c *Conversation) AddToolCallMessage(toolCalls []model.ToolCall) {
	msg := Message{
		Role:      "assistant",
		Content:   "", // Tool calls don't have content
		Timestamp: time.Now(),
		Tokens:    estimateToolCallTokens(toolCalls),
		ToolCalls: toolCalls,
		IsSummary: false,
	}
	c.Messages = append(c.Messages, msg)
	c.TokenCount += msg.Tokens
}

// AddToolResponseMessage adds a tool response message
func (c *Conversation) AddToolResponseMessage(toolCallID string, name string, content string) {
	msg := Message{
		Role:       "tool",
		Content:    content,
		Timestamp:  time.Now(),
		Tokens:     estimateTokens(content),
		ToolCallID: toolCallID,
		Name:       name,
		IsSummary:  false,
	}
	c.Messages = append(c.Messages, msg)
	c.TokenCount += msg.Tokens
}

// ToModelMessages converts conversation messages to model messages
func (c *Conversation) ToModelMessages() []model.Message {
	msgs := make([]model.Message, len(c.Messages))
	for i, msg := range c.Messages {
		var content any
		switch v := msg.Content.(type) {
		case string:
			if v != "" {
				content = v
			}
		case []model.ContentPart:
			if len(v) > 0 {
				content = v
			}
		case nil:
			// leave nil so omitempty works
		default:
			content = v
		}

		// Some providers (and some "thinking" models) return assistant text in the
		// reasoning channel with an empty content field. When that happens, we still
		// need to include the assistant's output in the prompt history to preserve
		// conversation continuity.
		if msg.Role == "assistant" && content == nil && len(msg.ToolCalls) == 0 && strings.TrimSpace(msg.Reasoning) != "" {
			content = msg.Reasoning
		}

		msgs[i] = model.Message{
			Role:       msg.Role,
			Content:    content, // Will be nil if empty, triggering omitempty
			ToolCalls:  msg.ToolCalls,
			ToolCallID: msg.ToolCallID,
			Name:       msg.Name,
			Reasoning:  msg.Reasoning, // Pass reasoning back to model for continuity
		}
	}
	return msgs
}

// GetLastN returns the last N messages
func (c *Conversation) GetLastN(n int) []Message {
	if n >= len(c.Messages) {
		return c.Messages
	}
	return c.Messages[len(c.Messages)-n:]
}

// Clear clears all messages
func (c *Conversation) Clear() {
	c.Messages = []Message{}
	c.TokenCount = 0
	c.CompactionCount = 0
}

// estimateTokens provides a rough token estimate
// In Phase 3, this will be replaced with accurate tiktoken counting
func estimateTokens(text string) int {
	// Rough estimate: ~4 characters per token
	return len(text) / 4
}

// estimateToolCallTokens estimates tokens for tool calls
func estimateToolCallTokens(toolCalls []model.ToolCall) int {
	total := 0
	for _, tc := range toolCalls {
		// Function name + arguments
		total += estimateTokens(tc.Function.Name)
		total += estimateTokens(tc.Function.Arguments)
		total += 10 // Overhead for structure
	}
	return total
}

// NeedsCompaction checks if compaction is needed
// Placeholder for Phase 3
func (c *Conversation) NeedsCompaction(maxTokens int, threshold float64) bool {
	return float64(c.TokenCount) >= float64(maxTokens)*threshold
}

// UpdateTokenCount recalculates token count
func (c *Conversation) UpdateTokenCount() {
	total := 0
	for i := range c.Messages {
		if c.Messages[i].Tokens == 0 {
			c.Messages[i].Tokens = estimateTokens(GetContentAsString(c.Messages[i].Content))
		}
		total += c.Messages[i].Tokens
	}
	c.TokenCount = total
}

// LoadFromStorage loads a conversation from the database
func (c *Conversation) LoadFromStorage(store *storage.Store) error {
	// Get all messages for this session
	messages, err := store.GetAllMessages(c.SessionID)
	if err != nil {
		return err
	}

	// Convert storage messages to conversation messages
	c.Messages = make([]Message, len(messages))
	totalTokens := 0
	compactions := 0

	for i, msg := range messages {
		c.Messages[i] = Message{
			Role:        msg.Role,
			Content:     materializeMessageContent(msg),
			Timestamp:   msg.Timestamp,
			Tokens:      msg.Tokens,
			IsSummary:   msg.IsSummary,
			IsTruncated: msg.IsTruncated,
			Reasoning:   msg.Reasoning,
		}
		totalTokens += msg.Tokens
		if msg.IsSummary {
			compactions++
		}
	}

	c.TokenCount = totalTokens
	c.CompactionCount = compactions
	return nil
}

// SaveMessage saves a message to storage
func (c *Conversation) SaveMessage(store *storage.Store, msg Message) error {
	contentText, contentJSON, contentType, err := serializeMessageContent(msg.Content)
	if err != nil {
		return fmt.Errorf("serialize message content: %w", err)
	}

	storageMsg := &storage.Message{
		SessionID:   c.SessionID,
		Role:        msg.Role,
		Content:     contentText,
		ContentJSON: contentJSON,
		ContentType: contentType,
		Reasoning:   msg.Reasoning,
		Timestamp:   msg.Timestamp,
		Tokens:      msg.Tokens,
		IsSummary:   msg.IsSummary,
		IsTruncated: msg.IsTruncated,
	}

	return store.SaveMessage(storageMsg)
}

// SaveAllMessages saves all messages to storage
func (c *Conversation) SaveAllMessages(store *storage.Store) error {
	messages := make([]storage.Message, len(c.Messages))
	for i, msg := range c.Messages {
		contentText, contentJSON, contentType, err := serializeMessageContent(msg.Content)
		if err != nil {
			return fmt.Errorf("serialize message %d: %w", i, err)
		}
		messages[i] = storage.Message{
			SessionID:   c.SessionID,
			Role:        msg.Role,
			Content:     contentText,
			ContentJSON: contentJSON,
			ContentType: contentType,
			Reasoning:   msg.Reasoning,
			Timestamp:   msg.Timestamp,
			Tokens:      msg.Tokens,
			IsSummary:   msg.IsSummary,
			IsTruncated: msg.IsTruncated,
		}
	}
	return store.ReplaceMessages(c.SessionID, messages)
}

func serializeMessageContent(content any) (text string, jsonData string, messageType string, err error) {
	text = GetContentAsString(content)
	switch v := content.(type) {
	case nil:
		return "", "", contentTypeText, nil
	case string:
		return v, "", contentTypeText, nil
	case []model.ContentPart:
		data, err := json.Marshal(v)
		if err != nil {
			return "", "", "", err
		}
		return text, string(data), contentTypeJSON, nil
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return "", "", "", err
		}
		return text, string(data), contentTypeJSON, nil
	}
}

func materializeMessageContent(msg storage.Message) any {
	if msg.ContentType == contentTypeJSON && strings.TrimSpace(msg.ContentJSON) != "" {
		var parts []model.ContentPart
		if err := json.Unmarshal([]byte(msg.ContentJSON), &parts); err == nil {
			return parts
		}
	}
	return msg.Content
}

// MaterializeContent deserializes JSON content or returns fallback text.
// Used when restoring multimodal content from storage.
func MaterializeContent(contentJSON string, fallbackText string) any {
	if strings.TrimSpace(contentJSON) != "" {
		var parts []model.ContentPart
		if err := json.Unmarshal([]byte(contentJSON), &parts); err == nil {
			return parts
		}
	}
	return fallbackText
}
