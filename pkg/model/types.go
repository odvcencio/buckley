package model

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// Message represents a chat message
type Message struct {
	Role       string     `json:"role"`                   // user, assistant, system, tool
	Content    any        `json:"content,omitempty"`      // Can be string or []ContentPart for multimodal
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`   // For assistant messages with tool calls
	ToolCallID string     `json:"tool_call_id,omitempty"` // For tool response messages
	Name       string     `json:"name,omitempty"`         // Tool name for tool messages
	Reasoning  string     `json:"-"`                      // Reasoning/thinking content (never sent in requests; decoded from responses when present)
}

func (m Message) MarshalJSON() ([]byte, error) {
	type messageNoReasoning struct {
		Role       string     `json:"role"`
		Content    any        `json:"content,omitempty"`
		ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
		ToolCallID string     `json:"tool_call_id,omitempty"`
		Name       string     `json:"name,omitempty"`
	}
	return json.Marshal(messageNoReasoning{
		Role:       m.Role,
		Content:    m.Content,
		ToolCalls:  m.ToolCalls,
		ToolCallID: m.ToolCallID,
		Name:       m.Name,
	})
}

func (m *Message) UnmarshalJSON(data []byte) error {
	type messageWithReasoning struct {
		Role       string     `json:"role"`
		Content    any        `json:"content,omitempty"`
		ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
		ToolCallID string     `json:"tool_call_id,omitempty"`
		Name       string     `json:"name,omitempty"`
		Reasoning  string     `json:"reasoning,omitempty"`
	}
	var aux messageWithReasoning
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	m.Role = aux.Role
	m.Content = aux.Content
	m.ToolCalls = aux.ToolCalls
	m.ToolCallID = aux.ToolCallID
	m.Name = aux.Name
	m.Reasoning = aux.Reasoning
	return nil
}

// ContentPart represents a part of multimodal content (text or image)
type ContentPart struct {
	Type     string    `json:"type"` // "text" or "image_url"
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

// ImageURL represents an image URL in a content part
type ImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"` // "low", "high", "auto"
}

// ToolCall represents a function/tool call from the assistant
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // Always "function" for now
	Function FunctionCall `json:"function"`
}

// FunctionCall represents the function being called
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string
}

// ReasoningConfig represents the reasoning configuration for models that support it
type ReasoningConfig struct {
	Effort string `json:"effort,omitempty"` // "low", "medium", "high"
}

// ChatRequest represents a request to the chat completion API
type ChatRequest struct {
	Model       string           `json:"model"`
	Messages    []Message        `json:"messages"`
	Temperature float64          `json:"temperature,omitempty"`
	MaxTokens   int              `json:"max_tokens,omitempty"`
	Stream      bool             `json:"stream"`
	Tools       []map[string]any `json:"tools,omitempty"`       // OpenAI function definitions
	ToolChoice  string           `json:"tool_choice,omitempty"` // "auto", "none", or specific function
	Reasoning   *ReasoningConfig `json:"reasoning,omitempty"`   // Reasoning config for supported models
	Transforms  []string         `json:"transforms,omitempty"`  // Provider-specific prompt transforms (e.g., OpenRouter)
}

// ChatResponse represents a non-streaming chat completion response
type ChatResponse struct {
	ID      string   `json:"id"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

// Choice represents a completion choice
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// StreamChunk represents a streaming response chunk
type StreamChunk struct {
	ID      string         `json:"id"`
	Model   string         `json:"model"`
	Choices []StreamChoice `json:"choices"`
	Usage   *Usage         `json:"usage,omitempty"` // Only present in final chunk
}

// StreamChoice represents a streaming choice
type StreamChoice struct {
	Index        int          `json:"index"`
	Delta        MessageDelta `json:"delta"`
	FinishReason *string      `json:"finish_reason"`
}

// MessageDelta represents incremental content in a stream
type MessageDelta struct {
	Role      string          `json:"role,omitempty"`
	Content   string          `json:"content,omitempty"`
	Reasoning string          `json:"reasoning,omitempty"` // For thinking/reasoning models
	ToolCalls []ToolCallDelta `json:"tool_calls,omitempty"`
}

// ToolCallDelta represents incremental tool call data in streaming
type ToolCallDelta struct {
	Index    int                `json:"index"`
	ID       string             `json:"id,omitempty"`
	Type     string             `json:"type,omitempty"`
	Function *FunctionCallDelta `json:"function,omitempty"`
}

// FunctionCallDelta represents incremental function call data
type FunctionCallDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// Usage represents token usage information
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ModelCatalog represents the list of available models
type ModelCatalog struct {
	Data []ModelInfo `json:"data"`
}

// ModelInfo represents information about a model
type ModelInfo struct {
	ID                  string       `json:"id"`
	Name                string       `json:"name"`
	Description         string       `json:"description"`
	ContextLength       int          `json:"context_length"`
	Pricing             ModelPricing `json:"pricing"`
	Created             int64        `json:"created"` // Unix timestamp
	Architecture        Architecture `json:"architecture,omitempty"`
	SupportedParameters []string     `json:"supported_parameters,omitempty"`
}

// Architecture contains model architecture details
type Architecture struct {
	Modality     string `json:"modality,omitempty"` // "text", "text+image", "text->image", etc.
	Tokenizer    string `json:"tokenizer,omitempty"`
	InstructType string `json:"instruct_type,omitempty"`
}

// ModelPricing represents pricing information for a model
type ModelPricing struct {
	Prompt     float64 `json:"prompt"`     // Per 1M tokens
	Completion float64 `json:"completion"` // Per 1M tokens
}

// UnmarshalJSON handles string or number pricing values from the API
func (p *ModelPricing) UnmarshalJSON(data []byte) error {
	// Try to unmarshal as an object with string values first
	var raw struct {
		Prompt     any `json:"prompt"`
		Completion any `json:"completion"`
	}

	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	// Convert prompt price
	// OpenRouter API returns pricing in "per token" format (e.g., 0.0000006)
	// We need to convert to "per million tokens" format (e.g., 0.60)
	switch v := raw.Prompt.(type) {
	case string:
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return err
		}
		p.Prompt = f * 1_000_000 // Convert from per-token to per-million-tokens
	case float64:
		p.Prompt = v * 1_000_000 // Convert from per-token to per-million-tokens
	}

	// Convert completion price
	switch v := raw.Completion.(type) {
	case string:
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return err
		}
		p.Completion = f * 1_000_000 // Convert from per-token to per-million-tokens
	case float64:
		p.Completion = v * 1_000_000 // Convert from per-token to per-million-tokens
	}

	return nil
}

// ErrorResponse represents an API error response
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail contains error information
type ErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

// APIError represents a structured API error with retry information
type APIError struct {
	StatusCode int
	Message    string
	Type       string
	Code       string
	Retryable  bool
	RetryAfter time.Duration
}

// Error implements the error interface
func (e *APIError) Error() string {
	if e.Type != "" && e.Code != "" {
		return fmt.Sprintf("HTTP %d: %s (type: %s, code: %s)", e.StatusCode, e.Message, e.Type, e.Code)
	}
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Message)
}

// IsRateLimitError returns true if this is a rate limit error
func (e *APIError) IsRateLimitError() bool {
	return e.StatusCode == 429
}
