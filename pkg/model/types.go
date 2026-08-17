package model

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// RequestMetadataReadOnly marks a model turn whose execution environment must
// not be able to modify the current checkout. Providers with native agent tools
// use this to enforce their own read-only sandbox.
const (
	RequestMetadataReadOnly       = "buckley.read_only"
	RequestMetadataReviewSnapshot = "buckley.review_snapshot"
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

// StreamOptions controls provider streaming metadata. It is populated only
// for providers that explicitly support the option; leaving it nil preserves
// the ordinary streaming wire shape for every other adapter.
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
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
	StreamOptions        *StreamOptions    `json:"stream_options,omitempty"`
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
	// ReviewSnapshot pins native verification to the immutable Git state
	// captured once for an entire review run. Native providers materialize it;
	// API-backed review tools are bound to the same descriptor by the agent runner.
	ReviewSnapshot *ReviewSnapshot `json:"-"`
}

// ChatResponse represents a non-streaming chat completion response.
type ChatResponse struct {
	ID                string                     `json:"id"`
	Model             string                     `json:"model"`
	Choices           []Choice                   `json:"choices"`
	Usage             Usage                      `json:"usage"`
	Error             *ErrorDetail               `json:"error,omitempty"`
	ExecutionEvidence []CommandExecutionEvidence `json:"execution_evidence,omitempty"`
}

// CommandExecutionEvidence records a native provider command event. ExitCode
// is a pointer so a missing exit status can never be mistaken for success.
// Consumers must also require Status == "completed" before trusting it.
type CommandExecutionEvidence struct {
	ID               string `json:"id,omitempty"`
	Command          string `json:"command"`
	AggregatedOutput string `json:"aggregated_output,omitempty"`
	ExitCode         *int   `json:"exit_code,omitempty"`
	Status           string `json:"status"`
	WorkingDirectory string `json:"working_directory,omitempty"`
	RepositoryRoot   string `json:"repository_root,omitempty"`
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
	Error   *ErrorDetail   `json:"error,omitempty"` // OpenRouter may report mid-stream failures in-band
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

// UnmarshalJSON decodes the JSON object once into a map[string]json.RawMessage
// (rather than the prior implementation's two full decodes -- one into a
// typed struct alias, one into a raw map, both walking the entire document
// including large Text/Data payloads) and then decodes each known field from
// its already-isolated raw slice. Whatever keys remain after the known ones
// are removed become Extra, preserving unknown provider fields.
//
// "signature" is deliberately left in the raw map (not deleted alongside the
// other known keys) so an explicit `"signature":null` -- indistinguishable
// from an absent key once decoded into the *string field -- still round-trips
// on re-encoding via Extra; MarshalJSON below skips Extra's copy once the
// named field has already written the key.
func (d *ReasoningDetail) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	var out ReasoningDetail
	if v, ok := raw["type"]; ok {
		if err := json.Unmarshal(v, &out.Type); err != nil {
			return fmt.Errorf("reasoning detail type: %w", err)
		}
	}
	if v, ok := raw["id"]; ok {
		if err := json.Unmarshal(v, &out.ID); err != nil {
			return fmt.Errorf("reasoning detail id: %w", err)
		}
	}
	if v, ok := raw["index"]; ok {
		if err := json.Unmarshal(v, &out.Index); err != nil {
			return fmt.Errorf("reasoning detail index: %w", err)
		}
		out.HasIndex = true
	}
	if v, ok := raw["text"]; ok {
		if err := json.Unmarshal(v, &out.Text); err != nil {
			return fmt.Errorf("reasoning detail text: %w", err)
		}
	}
	if v, ok := raw["summary"]; ok {
		if err := json.Unmarshal(v, &out.Summary); err != nil {
			return fmt.Errorf("reasoning detail summary: %w", err)
		}
	}
	if v, ok := raw["data"]; ok {
		if err := json.Unmarshal(v, &out.Data); err != nil {
			return fmt.Errorf("reasoning detail data: %w", err)
		}
	}
	if v, ok := raw["signature"]; ok {
		if err := json.Unmarshal(v, &out.Signature); err != nil {
			return fmt.Errorf("reasoning detail signature: %w", err)
		}
	}
	if v, ok := raw["format"]; ok {
		if err := json.Unmarshal(v, &out.Format); err != nil {
			return fmt.Errorf("reasoning detail format: %w", err)
		}
	}

	for _, key := range []string{"type", "id", "index", "text", "summary", "data", "format"} {
		delete(raw, key)
	}
	if len(raw) > 0 {
		out.Extra = raw
	}
	*d = out
	return nil
}

// MarshalJSON appends each present field directly to a growing byte slice in
// declared order instead of the prior implementation's approach of building
// a map[string]any and marshaling it (which allocates the map, reflects over
// each any-typed value, and sorts keys alphabetically for output). Key order
// in the result is therefore declaration order, not alphabetical; callers
// must treat ReasoningDetail JSON as unordered, same as before.
func (d ReasoningDetail) MarshalJSON() ([]byte, error) {
	buf := make([]byte, 0, 64+len(d.Type)+len(d.ID)+len(d.Text)+len(d.Summary)+len(d.Data)+len(d.Format))
	buf = append(buf, '{')
	first := true

	appendField := func(key string, value any) error {
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if !first {
			buf = append(buf, ',')
		}
		first = false
		buf = append(buf, '"')
		buf = append(buf, key...)
		buf = append(buf, '"', ':')
		buf = append(buf, encoded...)
		return nil
	}

	if d.Type != "" {
		if err := appendField("type", d.Type); err != nil {
			return nil, err
		}
	}
	if d.ID != "" {
		if err := appendField("id", d.ID); err != nil {
			return nil, err
		}
	}
	if d.HasIndex || d.Index != 0 {
		if err := appendField("index", d.Index); err != nil {
			return nil, err
		}
	}
	if d.Text != "" {
		if err := appendField("text", d.Text); err != nil {
			return nil, err
		}
	}
	if d.Summary != "" {
		if err := appendField("summary", d.Summary); err != nil {
			return nil, err
		}
	}
	if d.Data != "" {
		if err := appendField("data", d.Data); err != nil {
			return nil, err
		}
	}
	signatureWritten := false
	if d.Signature != nil {
		if err := appendField("signature", d.Signature); err != nil {
			return nil, err
		}
		signatureWritten = true
	}
	if d.Format != "" {
		if err := appendField("format", d.Format); err != nil {
			return nil, err
		}
	}
	for key, value := range d.Extra {
		if key == "signature" && signatureWritten {
			continue
		}
		keyEncoded, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		if !first {
			buf = append(buf, ',')
		}
		first = false
		buf = append(buf, keyEncoded...)
		buf = append(buf, ':')
		if len(value) == 0 {
			buf = append(buf, 'n', 'u', 'l', 'l')
		} else {
			buf = append(buf, value...)
		}
	}
	buf = append(buf, '}')
	return buf, nil
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
	PromptTokens           int                     `json:"prompt_tokens"`
	CompletionTokens       int                     `json:"completion_tokens"`
	TotalTokens            int                     `json:"total_tokens"`
	PromptTokensDetails    *PromptTokensDetails    `json:"prompt_tokens_details,omitempty"`
	CompletionTokenDetails *CompletionTokenDetails `json:"completion_tokens_details,omitempty"`
	CacheWriteTokens       int                     `json:"cache_write_tokens,omitempty"`
}

type PromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens,omitempty"`
}

type CompletionTokenDetails struct {
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

// AddUsage combines provider usage without dropping cache or reasoning detail
// fields when a harness makes several requests for one turn.
func AddUsage(total Usage, next Usage) Usage {
	total.PromptTokens += next.PromptTokens
	total.CompletionTokens += next.CompletionTokens
	total.TotalTokens += next.TotalTokens
	total.CacheWriteTokens += next.CacheWriteTokens
	if next.PromptTokensDetails != nil {
		if total.PromptTokensDetails == nil {
			total.PromptTokensDetails = &PromptTokensDetails{}
		}
		total.PromptTokensDetails.CachedTokens += next.PromptTokensDetails.CachedTokens
	}
	if next.CompletionTokenDetails != nil {
		if total.CompletionTokenDetails == nil {
			total.CompletionTokenDetails = &CompletionTokenDetails{}
		}
		total.CompletionTokenDetails.ReasoningTokens += next.CompletionTokenDetails.ReasoningTokens
	}
	return total
}

// RequestTokenEstimate describes the approximate model input footprint.
type RequestTokenEstimate struct {
	Messages int
	Tools    int
	Fixed    int
	Total    int
}

// EstimateRequestTokens includes tool schemas and request controls, which the
// conversation-only char/4 estimator historically missed. It walks the
// request fields directly instead of JSON-marshaling the whole request (the
// prior implementation, kept unexported below as
// estimateRequestTokensByMarshal for differential testing), adding a JSON
// envelope estimate -- key names, quotes, and an escaping approximation for
// the characters Go's encoder treats specially -- on top of each field's raw
// byte length so the result tracks the marshal-based byte count.
func EstimateRequestTokens(req ChatRequest) RequestTokenEstimate {
	estimate := RequestTokenEstimate{
		Messages: estimateMessagesBytes(req.Messages) / 4,
		Tools:    estimateToolsBytes(req.Tools) / 4,
		Fixed:    estimateFixedRequestBytes(req) / 4,
	}
	estimate.Total = estimate.Messages + estimate.Tools + estimate.Fixed
	return estimate
}

// estimateRequestTokensByMarshal is the original JSON-marshal-based
// estimator. It stays unexported and unused in production so the
// differential test in types_field_estimate_test.go can assert the
// field-walk estimate above tracks it within tolerance.
func estimateRequestTokensByMarshal(req ChatRequest) RequestTokenEstimate {
	messages, _ := json.Marshal(req.Messages)
	tools, _ := json.Marshal(req.Tools)
	copyReq := req
	copyReq.Messages = nil
	copyReq.Tools = nil
	fixed, _ := json.Marshal(copyReq)
	estimate := RequestTokenEstimate{
		Messages: len(messages) / 4,
		Tools:    len(tools) / 4,
		Fixed:    len(fixed) / 4,
	}
	estimate.Total = estimate.Messages + estimate.Tools + estimate.Fixed
	return estimate
}

// jsonStringBytes approximates the JSON-encoded byte length of s as a quoted
// string literal, including the escaping Go's encoder applies to quotes,
// backslashes, common control characters, and (by default) the HTML-unsafe
// runes '<', '>', and '&'.
func jsonStringBytes(s string) int {
	n := len(s) + 2 // opening and closing quotes
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"' || c == '\\':
			n++ // one raw byte becomes a two-byte escape
		case c == '\n' || c == '\r' || c == '\t' || c == '\b' || c == '\f':
			n++ // one raw byte becomes a two-byte escape
		case c < 0x20:
			n += 5 // one raw byte becomes a six-byte \u00XX escape
		case c == '<' || c == '>' || c == '&':
			n += 5 // one raw byte becomes a six-byte \uXXXX escape
		}
	}
	return n
}

func jsonNumberBytes(n int) int {
	if n == 0 {
		return 1
	}
	digits := 0
	if n < 0 {
		digits++
		n = -n
	}
	for n > 0 {
		digits++
		n /= 10
	}
	return digits
}

func jsonFloatBytes(f float64) int {
	return len(strconv.FormatFloat(f, 'g', -1, 64))
}

func jsonBoolBytes(b bool) int {
	if b {
		return 4 // true
	}
	return 5 // false
}

// jsonKeyValueBytes returns the bytes contributed by one "key":value pair
// (excluding the separating comma, which callers add between fields).
func jsonKeyValueBytes(key string, valueBytes int) int {
	return len(key) + 3 + valueBytes // quotes (2) + colon (1)
}

// estimateAnyBytes walks an arbitrary decoded-JSON value (as produced by
// map[string]any/[]any-shaped request fields such as tool schemas and
// provider routing preferences) without marshaling it. Shapes outside the
// common decoded-JSON set fall back to json.Marshal; those fields are small
// and infrequent relative to message content, so the allocation there does
// not reintroduce the cost this estimator avoids for the message-heavy path.
func estimateAnyBytes(v any) int {
	switch t := v.(type) {
	case nil:
		return 4 // null
	case string:
		return jsonStringBytes(t)
	case bool:
		return jsonBoolBytes(t)
	case float64:
		return jsonFloatBytes(t)
	case int:
		return jsonNumberBytes(t)
	case json.RawMessage:
		if t == nil {
			return 4
		}
		return len(t)
	case map[string]any:
		return estimateAnyMapBytes(t)
	case []any:
		return estimateAnySliceBytes(t)
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			return 4
		}
		return len(encoded)
	}
}

func estimateAnyMapBytes(m map[string]any) int {
	if m == nil {
		return 4
	}
	if len(m) == 0 {
		return 2
	}
	n := 2
	i := 0
	for key, value := range m {
		if i > 0 {
			n++
		}
		n += jsonKeyValueBytes(key, estimateAnyBytes(value))
		i++
	}
	return n
}

func estimateAnySliceBytes(values []any) int {
	if values == nil {
		return 4
	}
	if len(values) == 0 {
		return 2
	}
	n := 2
	for i, value := range values {
		if i > 0 {
			n++
		}
		n += estimateAnyBytes(value)
	}
	return n
}

func estimateStringMapBytes(m map[string]string) int {
	if m == nil {
		return 4
	}
	if len(m) == 0 {
		return 2
	}
	n := 2
	i := 0
	for key, value := range m {
		if i > 0 {
			n++
		}
		n += jsonKeyValueBytes(key, jsonStringBytes(value))
		i++
	}
	return n
}

func estimateStringSliceBytes(values []string) int {
	if values == nil {
		return 4
	}
	if len(values) == 0 {
		return 2
	}
	n := 2
	for i, value := range values {
		if i > 0 {
			n++
		}
		n += jsonStringBytes(value)
	}
	return n
}

func estimateMessagesBytes(messages []Message) int {
	if messages == nil {
		return 4
	}
	if len(messages) == 0 {
		return 2
	}
	n := 2
	for i, msg := range messages {
		if i > 0 {
			n++
		}
		n += estimateMessageBytes(msg)
	}
	return n
}

// estimateMessageBytes mirrors Message.MarshalJSON's field order and
// omitempty rules (role and content differ: role has no omitempty tag, and
// an any-typed content field with omitempty is only skipped when the
// interface itself is nil, not when it holds an empty string).
func estimateMessageBytes(msg Message) int {
	n := 2
	fields := 0

	n += jsonKeyValueBytes("role", jsonStringBytes(msg.Role))
	fields++

	if msg.Content != nil {
		n += jsonKeyValueBytes("content", estimateContentBytes(msg.Content))
		fields++
	}
	if len(msg.ToolCalls) > 0 {
		n += jsonKeyValueBytes("tool_calls", estimateToolCallsBytes(msg.ToolCalls))
		fields++
	}
	if msg.ToolCallID != "" {
		n += jsonKeyValueBytes("tool_call_id", jsonStringBytes(msg.ToolCallID))
		fields++
	}
	if msg.Name != "" {
		n += jsonKeyValueBytes("name", jsonStringBytes(msg.Name))
		fields++
	}
	if msg.Reasoning != "" {
		n += jsonKeyValueBytes("reasoning", jsonStringBytes(msg.Reasoning))
		fields++
	}
	if len(msg.ReasoningDetails) > 0 {
		n += jsonKeyValueBytes("reasoning_details", estimateReasoningDetailsBytes(msg.ReasoningDetails))
		fields++
	}
	if fields > 1 {
		n += fields - 1
	}
	return n
}

func estimateContentBytes(content any) int {
	switch v := content.(type) {
	case string:
		return jsonStringBytes(v)
	case []ContentPart:
		return estimateContentPartsBytes(v)
	default:
		// Rare shapes (e.g. []any content parts from a JSON round-trip) are
		// not on the hot per-turn estimation path, so a direct marshal here
		// trades a small, infrequent allocation for correctness.
		encoded, err := json.Marshal(v)
		if err != nil {
			return 4
		}
		return len(encoded)
	}
}

func estimateContentPartsBytes(parts []ContentPart) int {
	if parts == nil {
		return 4
	}
	if len(parts) == 0 {
		return 2
	}
	n := 2
	for i, part := range parts {
		if i > 0 {
			n++
		}
		n += estimateContentPartBytes(part)
	}
	return n
}

func estimateContentPartBytes(part ContentPart) int {
	n := 2
	fields := 1
	n += jsonKeyValueBytes("type", jsonStringBytes(part.Type))
	if part.Text != "" {
		n += jsonKeyValueBytes("text", jsonStringBytes(part.Text))
		fields++
	}
	if part.ImageURL != nil {
		n += jsonKeyValueBytes("image_url", estimateImageURLBytes(*part.ImageURL))
		fields++
	}
	if part.CacheControl != nil {
		n += jsonKeyValueBytes("cache_control", estimateCacheControlBytes(*part.CacheControl))
		fields++
	}
	if fields > 1 {
		n += fields - 1
	}
	return n
}

func estimateImageURLBytes(img ImageURL) int {
	n := 2
	fields := 1
	n += jsonKeyValueBytes("url", jsonStringBytes(img.URL))
	if img.Detail != "" {
		n += jsonKeyValueBytes("detail", jsonStringBytes(img.Detail))
		fields++
	}
	if fields > 1 {
		n += fields - 1
	}
	return n
}

func estimateCacheControlBytes(cc CacheControl) int {
	n := 2
	fields := 1
	n += jsonKeyValueBytes("type", jsonStringBytes(cc.Type))
	if cc.TTL != "" {
		n += jsonKeyValueBytes("ttl", jsonStringBytes(cc.TTL))
		fields++
	}
	if fields > 1 {
		n += fields - 1
	}
	return n
}

func estimateToolCallsBytes(calls []ToolCall) int {
	if calls == nil {
		return 4
	}
	if len(calls) == 0 {
		return 2
	}
	n := 2
	for i, call := range calls {
		if i > 0 {
			n++
		}
		n += estimateToolCallBytes(call)
	}
	return n
}

func estimateToolCallBytes(call ToolCall) int {
	n := 2
	n += jsonKeyValueBytes("id", jsonStringBytes(call.ID))
	n++
	n += jsonKeyValueBytes("type", jsonStringBytes(call.Type))
	n++
	n += jsonKeyValueBytes("function", estimateFunctionCallBytes(call.Function))
	return n
}

func estimateFunctionCallBytes(fn FunctionCall) int {
	n := 2
	n += jsonKeyValueBytes("name", jsonStringBytes(fn.Name))
	n++
	n += jsonKeyValueBytes("arguments", jsonStringBytes(fn.Arguments))
	return n
}

func estimateReasoningDetailsBytes(details []ReasoningDetail) int {
	if details == nil {
		return 4
	}
	if len(details) == 0 {
		return 2
	}
	n := 2
	for i, detail := range details {
		if i > 0 {
			n++
		}
		n += estimateReasoningDetailBytes(detail)
	}
	return n
}

// estimateReasoningDetailBytes mirrors ReasoningDetail.MarshalJSON's field
// set (Extra keys plus the conditionally-included named fields).
func estimateReasoningDetailBytes(detail ReasoningDetail) int {
	n := 2
	fields := 0
	for key, raw := range detail.Extra {
		n += jsonKeyValueBytes(key, len(raw))
		fields++
	}
	if detail.Type != "" {
		n += jsonKeyValueBytes("type", jsonStringBytes(detail.Type))
		fields++
	}
	if detail.ID != "" {
		n += jsonKeyValueBytes("id", jsonStringBytes(detail.ID))
		fields++
	}
	if detail.HasIndex || detail.Index != 0 {
		n += jsonKeyValueBytes("index", jsonNumberBytes(detail.Index))
		fields++
	}
	if detail.Text != "" {
		n += jsonKeyValueBytes("text", jsonStringBytes(detail.Text))
		fields++
	}
	if detail.Summary != "" {
		n += jsonKeyValueBytes("summary", jsonStringBytes(detail.Summary))
		fields++
	}
	if detail.Data != "" {
		n += jsonKeyValueBytes("data", jsonStringBytes(detail.Data))
		fields++
	}
	if detail.Signature != nil {
		n += jsonKeyValueBytes("signature", jsonStringBytes(*detail.Signature))
		fields++
	}
	if detail.Format != "" {
		n += jsonKeyValueBytes("format", jsonStringBytes(detail.Format))
		fields++
	}
	if fields > 1 {
		n += fields - 1
	}
	return n
}

func estimateToolsBytes(tools []map[string]any) int {
	if tools == nil {
		return 4
	}
	if len(tools) == 0 {
		return 2
	}
	n := 2
	for i, tool := range tools {
		if i > 0 {
			n++
		}
		n += estimateAnyMapBytes(tool)
	}
	return n
}

func estimateReasoningConfigBytes(cfg ReasoningConfig) int {
	n := 2
	fields := 0
	if cfg.Effort != "" {
		n += jsonKeyValueBytes("effort", jsonStringBytes(cfg.Effort))
		fields++
	}
	if cfg.MaxTokens != 0 {
		n += jsonKeyValueBytes("max_tokens", jsonNumberBytes(cfg.MaxTokens))
		fields++
	}
	if cfg.Enabled != nil {
		n += jsonKeyValueBytes("enabled", jsonBoolBytes(*cfg.Enabled))
		fields++
	}
	if cfg.Exclude != nil {
		n += jsonKeyValueBytes("exclude", jsonBoolBytes(*cfg.Exclude))
		fields++
	}
	if fields > 1 {
		n += fields - 1
	}
	return n
}

// estimateFixedRequestBytes mirrors ChatRequest's field order and omitempty
// rules for every field except Messages and Tools, which EstimateRequestTokens
// accounts for separately (matching estimateRequestTokensByMarshal, which
// nils those two fields before marshaling the rest). Messages has no
// omitempty tag, so even nilled it contributes a "messages":null pair; Tools
// has omitempty, so nilled it contributes nothing here.
func estimateFixedRequestBytes(req ChatRequest) int {
	n := 2
	fields := 0

	n += jsonKeyValueBytes("model", jsonStringBytes(req.Model))
	fields++

	if len(req.Models) > 0 {
		n += jsonKeyValueBytes("models", estimateStringSliceBytes(req.Models))
		fields++
	}

	n += jsonKeyValueBytes("messages", 4) // "messages":null
	fields++

	if req.Temperature != 0 {
		n += jsonKeyValueBytes("temperature", jsonFloatBytes(req.Temperature))
		fields++
	}
	if req.MaxTokens != 0 {
		n += jsonKeyValueBytes("max_tokens", jsonNumberBytes(req.MaxTokens))
		fields++
	}
	if req.MaxCompletionTokens != 0 {
		n += jsonKeyValueBytes("max_completion_tokens", jsonNumberBytes(req.MaxCompletionTokens))
		fields++
	}

	n += jsonKeyValueBytes("stream", jsonBoolBytes(req.Stream))
	fields++
	if req.StreamOptions != nil {
		n += jsonKeyValueBytes("stream_options", estimateStreamOptionsBytes(*req.StreamOptions))
		fields++
	}

	// Tools is intentionally skipped: EstimateRequestTokens computes it via
	// estimateToolsBytes, same as the marshal-based split.

	if req.ToolChoice != "" {
		n += jsonKeyValueBytes("tool_choice", jsonStringBytes(req.ToolChoice))
		fields++
	}
	if req.ParallelToolCalls != nil {
		n += jsonKeyValueBytes("parallel_tool_calls", jsonBoolBytes(*req.ParallelToolCalls))
		fields++
	}
	if req.Reasoning != nil {
		n += jsonKeyValueBytes("reasoning", estimateReasoningConfigBytes(*req.Reasoning))
		fields++
	}
	if req.IncludeReasoning != nil {
		n += jsonKeyValueBytes("include_reasoning", jsonBoolBytes(*req.IncludeReasoning))
		fields++
	}
	if len(req.Transforms) > 0 {
		n += jsonKeyValueBytes("transforms", estimateStringSliceBytes(req.Transforms))
		fields++
	}
	if len(req.Provider) > 0 {
		n += jsonKeyValueBytes("provider", estimateAnyMapBytes(req.Provider))
		fields++
	}
	if len(req.ResponseFormat) > 0 {
		n += jsonKeyValueBytes("response_format", estimateAnyMapBytes(req.ResponseFormat))
		fields++
	}
	if req.Seed != nil {
		n += jsonKeyValueBytes("seed", jsonNumberBytes(*req.Seed))
		fields++
	}
	if req.ServiceTier != "" {
		n += jsonKeyValueBytes("service_tier", jsonStringBytes(req.ServiceTier))
		fields++
	}
	if req.SessionID != "" {
		n += jsonKeyValueBytes("session_id", jsonStringBytes(req.SessionID))
		fields++
	}
	if len(req.Metadata) > 0 {
		n += jsonKeyValueBytes("metadata", estimateStringMapBytes(req.Metadata))
		fields++
	}
	if len(req.Trace) > 0 {
		n += jsonKeyValueBytes("trace", estimateStringMapBytes(req.Trace))
		fields++
	}
	if req.CacheControl != nil {
		n += jsonKeyValueBytes("cache_control", estimateCacheControlBytes(*req.CacheControl))
		fields++
	}
	if req.PromptCacheKey != "" {
		n += jsonKeyValueBytes("prompt_cache_key", jsonStringBytes(req.PromptCacheKey))
		fields++
	}
	if req.PromptCacheRetention != "" {
		n += jsonKeyValueBytes("prompt_cache_retention", jsonStringBytes(req.PromptCacheRetention))
		fields++
	}

	if fields > 1 {
		n += fields - 1
	}
	return n
}

func estimateStreamOptionsBytes(options StreamOptions) int {
	return 2 + jsonKeyValueBytes("include_usage", jsonBoolBytes(options.IncludeUsage))
}

// ModelCatalog represents the list of available models
type ModelCatalog struct {
	Data []ModelInfo `json:"data"`
}

// ModelInfo represents information about a model
type ModelInfo struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	ContextLength int    `json:"context_length"`
	// MaxCompletionTokens is the provider-advertised completion ceiling. It
	// is optional because not every provider publishes one. OpenRouter nests
	// this value under top_provider; UnmarshalJSON promotes it here so callers
	// can safely clamp large synthesis requests without hard-coding model IDs.
	MaxCompletionTokens int          `json:"max_completion_tokens,omitempty"`
	Pricing             ModelPricing `json:"pricing"`
	// PricingKnown records that a provider catalog explicitly supplied both
	// prompt and completion prices. It distinguishes an authoritative free
	// model from a zero-value ModelPricing whose prices are simply unavailable.
	PricingKnown        bool         `json:"-"`
	Created             int64        `json:"created"` // Unix timestamp
	Architecture        Architecture `json:"architecture,omitempty"`
	SupportedParameters []string     `json:"supported_parameters,omitempty"`
}

// UnmarshalJSON accepts the common top-level catalog shape and OpenRouter's
// nested top_provider.max_completion_tokens capability in one place.
func (m *ModelInfo) UnmarshalJSON(data []byte) error {
	type modelInfoAlias ModelInfo
	var base modelInfoAlias
	if err := json.Unmarshal(data, &base); err != nil {
		return err
	}
	var capabilities struct {
		TopProvider struct {
			MaxCompletionTokens int `json:"max_completion_tokens"`
		} `json:"top_provider"`
		Pricing map[string]json.RawMessage `json:"pricing"`
	}
	if err := json.Unmarshal(data, &capabilities); err != nil {
		return err
	}
	*m = ModelInfo(base)
	m.PricingKnown = explicitPricingValue(capabilities.Pricing["prompt"]) &&
		explicitPricingValue(capabilities.Pricing["completion"])
	if m.MaxCompletionTokens <= 0 {
		m.MaxCompletionTokens = capabilities.TopProvider.MaxCompletionTokens
	}
	return nil
}

func explicitPricingValue(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return false
	}
	switch value := value.(type) {
	case float64:
		return true
	case string:
		_, err := strconv.ParseFloat(value, 64)
		return err == nil
	default:
		return false
	}
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
	Error              ErrorDetail     `json:"error"`
	OpenRouterMetadata json.RawMessage `json:"openrouter_metadata,omitempty"`
}

// ErrorDetail contains error information
type ErrorDetail struct {
	Message  string          `json:"message"`
	Type     string          `json:"type"`
	Code     string          `json:"code"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

// UnmarshalJSON accepts the string and numeric error codes used by
// OpenRouter's regular and streaming error envelopes.
func (e *ErrorDetail) UnmarshalJSON(data []byte) error {
	var raw struct {
		Message  string          `json:"message"`
		Type     string          `json:"type"`
		Code     json.RawMessage `json:"code"`
		Metadata json.RawMessage `json:"metadata"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	e.Message = raw.Message
	e.Type = raw.Type
	e.Metadata = append(e.Metadata[:0], raw.Metadata...)
	e.Code = ""
	if len(raw.Code) == 0 || string(raw.Code) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw.Code, &e.Code); err == nil {
		return nil
	}
	e.Code = strings.TrimSpace(string(raw.Code))
	return nil
}

// APIError represents a structured API error with retry information
type APIError struct {
	StatusCode int
	Message    string
	Type       string
	Code       string
	Provider   string
	Details    string
	RequestID  string
	Retryable  bool
	RetryAfter time.Duration
}

// Error implements the error interface
func (e *APIError) Error() string {
	message := fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Message)
	if e.Type != "" && e.Code != "" {
		message += fmt.Sprintf(" (type: %s, code: %s)", e.Type, e.Code)
	}

	qualifiers := make([]string, 0, 3)
	if e.Provider != "" {
		qualifiers = append(qualifiers, "provider: "+e.Provider)
	}
	if e.RequestID != "" {
		qualifiers = append(qualifiers, "request: "+e.RequestID)
	}
	if e.RetryAfter > 0 {
		qualifiers = append(qualifiers, "retry after: "+e.RetryAfter.String())
	}

	if len(qualifiers) > 0 {
		message += " (" + strings.Join(qualifiers, "; ") + ")"
	}
	if e.Details != "" && e.Details != e.Message {
		message += ": " + e.Details
	}
	if openRouterPolicyBlocked(e) {
		message += "; OpenRouter filtered every eligible endpoint; check Settings > Privacy and Guardrails for ZDR, data-collection, provider, and model restrictions"
	}
	return message
}

func openRouterPolicyBlocked(e *APIError) bool {
	if e == nil || e.StatusCode != 404 {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(e.Message + " " + e.Details))
	if !strings.Contains(text, "no endpoint") && !strings.Contains(text, "no endpoints") {
		return false
	}
	return strings.Contains(text, "guardrail") ||
		strings.Contains(text, "data policy") ||
		strings.Contains(text, "zero data retention") ||
		strings.Contains(text, "zdr")
}

// IsRateLimitError returns true if this is a rate limit error
func (e *APIError) IsRateLimitError() bool {
	return e.StatusCode == 429
}
