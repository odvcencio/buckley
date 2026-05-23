package model

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// Message represents a chat message
type Message struct {
	Role             string            `json:"role"`                        // user, assistant, system, tool
	Content          any               `json:"content,omitempty"`           // Can be string or []ContentPart for multimodal
	ToolCalls        []ToolCall        `json:"tool_calls,omitempty"`        // For assistant messages with tool calls
	ToolCallID       string            `json:"tool_call_id,omitempty"`      // For tool response messages
	Name             string            `json:"name,omitempty"`              // Tool name for tool messages
	Reasoning        string            `json:"reasoning,omitempty"`         // Reasoning/thinking content for reasoning continuity
	ReasoningDetails []ReasoningDetail `json:"reasoning_details,omitempty"` // OpenRouter reasoning_details blocks
}

func (m Message) MarshalJSON() ([]byte, error) {
	type messageAlias struct {
		Role             string            `json:"role"`
		Content          any               `json:"content,omitempty"`
		ToolCalls        []ToolCall        `json:"tool_calls,omitempty"`
		ToolCallID       string            `json:"tool_call_id,omitempty"`
		Name             string            `json:"name,omitempty"`
		Reasoning        string            `json:"reasoning,omitempty"`
		ReasoningDetails []ReasoningDetail `json:"reasoning_details,omitempty"`
	}
	return json.Marshal(messageAlias{
		Role:             m.Role,
		Content:          m.Content,
		ToolCalls:        m.ToolCalls,
		ToolCallID:       m.ToolCallID,
		Name:             m.Name,
		Reasoning:        m.Reasoning,
		ReasoningDetails: m.ReasoningDetails,
	})
}

func (m *Message) UnmarshalJSON(data []byte) error {
	type messageWithReasoning struct {
		Role             string            `json:"role"`
		Content          any               `json:"content,omitempty"`
		ToolCalls        []ToolCall        `json:"tool_calls,omitempty"`
		ToolCallID       string            `json:"tool_call_id,omitempty"`
		Name             string            `json:"name,omitempty"`
		Reasoning        string            `json:"reasoning,omitempty"`
		ReasoningContent string            `json:"reasoning_content,omitempty"`
		ReasoningDetails []ReasoningDetail `json:"reasoning_details,omitempty"`
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
	if m.Reasoning == "" {
		m.Reasoning = aux.ReasoningContent
	}
	m.ReasoningDetails = aux.ReasoningDetails
	return nil
}

// ContentPart represents a part of multimodal content (text or image)
type ContentPart struct {
	Type     string    `json:"type"` // "text" or "image_url"
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
	// CacheControl is used by providers that support prompt caching.
	CacheControl *CacheControl `json:"cache_control,omitempty"`
}

// ImageURL represents an image URL in a content part
type ImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"` // "low", "high", "auto"
}

// CacheControl marks content blocks for prompt caching.
type CacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
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

// ReasoningConfig controls extended thinking behavior for models that support it.
type ReasoningConfig struct {
	Effort    string `json:"effort,omitempty"`     // "minimal", "low", "medium", "high", "xhigh"
	MaxTokens int    `json:"max_tokens,omitempty"` // Reasoning token budget for supported providers
	Enabled   *bool  `json:"enabled,omitempty"`    // Enable provider default reasoning mode
	Exclude   *bool  `json:"exclude,omitempty"`    // Use hidden reasoning without returning reasoning tokens
}

// PromptCache configures provider-specific prompt caching behavior.
type PromptCache struct {
	Enabled        bool
	SystemMessages int
	TailMessages   int
}

// ChatRequest represents a chat completion request to an LLM provider.
type ChatRequest struct {
	Model                string            `json:"model"`
	Models               []string          `json:"models,omitempty"` // OpenRouter fallback model list
	Messages             []Message         `json:"messages"`
	Temperature          float64           `json:"temperature,omitempty"`
	MaxTokens            int               `json:"max_tokens,omitempty"`
	MaxCompletionTokens  int               `json:"max_completion_tokens,omitempty"`
	Stream               bool              `json:"stream"`
	Tools                []map[string]any  `json:"tools,omitempty"`               // OpenAI function definitions
	ToolChoice           string            `json:"tool_choice,omitempty"`         // "auto", "none", or specific function
	ParallelToolCalls    *bool             `json:"parallel_tool_calls,omitempty"` // OpenRouter/OpenAI parallel tool calls
	Reasoning            *ReasoningConfig  `json:"reasoning,omitempty"`           // Reasoning config for supported models
	IncludeReasoning     *bool             `json:"include_reasoning,omitempty"`   // OpenRouter legacy reasoning toggle
	Transforms           []string          `json:"transforms,omitempty"`          // Provider-specific prompt transforms (e.g., OpenRouter)
	Provider             map[string]any    `json:"provider,omitempty"`            // OpenRouter provider routing preferences
	ResponseFormat       map[string]any    `json:"response_format,omitempty"`     // JSON mode or JSON schema
	Seed                 *int              `json:"seed,omitempty"`
	ServiceTier          string            `json:"service_tier,omitempty"`
	SessionID            string            `json:"session_id,omitempty"`             // OpenRouter observability/session grouping
	Metadata             map[string]string `json:"metadata,omitempty"`               // OpenRouter request metadata
	Trace                map[string]string `json:"trace,omitempty"`                  // OpenRouter tracing metadata
	CacheControl         *CacheControl     `json:"cache_control,omitempty"`          // OpenRouter top-level prompt caching
	PromptCacheKey       string            `json:"prompt_cache_key,omitempty"`       // OpenAI prompt caching key
	PromptCacheRetention string            `json:"prompt_cache_retention,omitempty"` // OpenAI prompt cache retention
	PromptCache          *PromptCache      `json:"-"`
}

// ChatResponse represents a non-streaming chat completion response.
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

// StreamChunk represents a single chunk from a streaming chat completion.
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
	Role             string            `json:"role,omitempty"`
	Content          string            `json:"content,omitempty"`
	Reasoning        string            `json:"reasoning,omitempty"`         // For thinking/reasoning models
	ReasoningDetails []ReasoningDetail `json:"reasoning_details,omitempty"` // OpenRouter's reasoning_details format
	ToolCalls        []ToolCallDelta   `json:"tool_calls,omitempty"`
}

// ReasoningDetail represents a reasoning block from OpenRouter's reasoning_details format.
type ReasoningDetail struct {
	Type      string                     `json:"type"` // "reasoning.text", "reasoning.summary", "reasoning.encrypted"
	ID        string                     `json:"id,omitempty"`
	Index     int                        `json:"index,omitempty"`
	HasIndex  bool                       `json:"-"`
	Text      string                     `json:"text,omitempty"`      // For reasoning.text
	Summary   string                     `json:"summary,omitempty"`   // For reasoning.summary
	Data      string                     `json:"data,omitempty"`      // For reasoning.encrypted
	Signature *string                    `json:"signature,omitempty"` // For signed reasoning.text
	Format    string                     `json:"format,omitempty"`
	Extra     map[string]json.RawMessage `json:"-"`
}

func (d *ReasoningDetail) UnmarshalJSON(data []byte) error {
	type alias ReasoningDetail
	var aux alias
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	_, aux.HasIndex = raw["index"]
	for _, key := range []string{"type", "id", "index", "text", "summary", "data", "format"} {
		delete(raw, key)
	}
	aux.Extra = raw
	*d = ReasoningDetail(aux)
	return nil
}

func (d ReasoningDetail) MarshalJSON() ([]byte, error) {
	fields := make(map[string]any, len(d.Extra)+8)
	for key, value := range d.Extra {
		fields[key] = value
	}
	if d.Type != "" {
		fields["type"] = d.Type
	}
	if d.ID != "" {
		fields["id"] = d.ID
	}
	if d.HasIndex || d.Index != 0 {
		fields["index"] = d.Index
	}
	if d.Text != "" {
		fields["text"] = d.Text
	}
	if d.Summary != "" {
		fields["summary"] = d.Summary
	}
	if d.Data != "" {
		fields["data"] = d.Data
	}
	if d.Signature != nil {
		fields["signature"] = d.Signature
	}
	if d.Format != "" {
		fields["format"] = d.Format
	}
	return json.Marshal(fields)
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

// Usage tracks token consumption for a single request.
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
