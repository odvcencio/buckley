package model

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const anthropicBaseURL = "https://api.anthropic.com"

// claudeRule is what a Claude 5 model accepts beyond the shared request shape.
type claudeRule struct {
	name               string
	canDisableThinking bool // thinking: {type: "disabled"} is accepted
	canForceTool       bool // a forced tool_choice is accepted
}

// claudeRules lists the Claude 5 models whose thinking is adaptive and
// whose sampling parameters are rejected. Fable and Opus 5.5 always think;
// Sonnet 5.5 returns a 400 for thinking disabled; Haiku 5.5 accepts it. Fable,
// Opus 5.5 and Sonnet 5.5 return a 400 for a forced tool_choice.
var claudeRules = map[string]claudeRule{
	"claude-fable-5-2":  {name: "Claude Fable 5.2"},
	"claude-fable-5-1":  {name: "Claude Fable 5.1"},
	"claude-opus-5-5":   {name: "Claude Opus 5.5"},
	"claude-sonnet-5-5": {name: "Claude Sonnet 5.5"},
	"claude-haiku-5-5":  {name: "Claude Haiku 5.5", canDisableThinking: true, canForceTool: true},
}

func claudeRequestRules(model string) (claudeRule, bool) {
	rule, ok := claudeRules[model]
	return rule, ok
}

// AnthropicProvider calls the Claude Messages API.
type AnthropicProvider struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
	transport  *ProviderTransport
	version    string
	overrides  catalogOverrides
}

var anthropicModels = []ModelInfo{
	{
		ID:                  "anthropic/claude-fable-5-2",
		Name:                "Claude Fable 5.2",
		ContextLength:       1000000,
		MaxCompletionTokens: 128000,
		Pricing:             ModelPricing{Prompt: 8.0, Completion: 40.0, CacheRead: 0.20, CacheWrite: 10.0, CacheWrite1h: 16.0},
		PricingKnown:        true,
		Architecture:        Architecture{Modality: "text+image"},
		SupportedParameters: []string{"tools", "tool_choice", "reasoning"},
	},
	{
		ID:                  "anthropic/claude-fable-5-1",
		Name:                "Claude Fable 5.1",
		ContextLength:       1000000,
		MaxCompletionTokens: 128000,
		Pricing:             ModelPricing{Prompt: 10.0, Completion: 50.0, CacheRead: 0.25, CacheWrite: 12.5, CacheWrite1h: 20.0},
		PricingKnown:        true,
		Architecture:        Architecture{Modality: "text+image"},
		SupportedParameters: []string{"tools", "tool_choice", "reasoning"},
	},
	{
		ID:                  "anthropic/claude-sonnet-5-5",
		Name:                "Claude Sonnet 5.5",
		ContextLength:       1000000,
		MaxCompletionTokens: 128000,
		Pricing:             ModelPricing{Prompt: 2.0, Completion: 10.0, CacheRead: 0.10, CacheWrite: 2.5, CacheWrite1h: 4.0},
		PricingKnown:        true,
		Architecture:        Architecture{Modality: "text+image"},
		SupportedParameters: []string{"tools", "tool_choice", "reasoning"},
	},
	{
		ID:                  "anthropic/claude-haiku-5-5",
		Name:                "Claude Haiku 5.5",
		ContextLength:       1000000,
		MaxCompletionTokens: 128000,
		Pricing:             ModelPricing{Prompt: 0.10, Completion: 0.50, CacheRead: 0.01, CacheWrite: 0.125, CacheWrite1h: 0.20},
		PricingKnown:        true,
		Architecture:        Architecture{Modality: "text+image"},
		SupportedParameters: []string{"tools", "tool_choice", "reasoning"},
	},
	{
		ID:                  "anthropic/claude-opus-5-5",
		Name:                "Claude Opus 5.5",
		ContextLength:       1000000,
		MaxCompletionTokens: 128000,
		Pricing:             ModelPricing{Prompt: 4.0, Completion: 20.0, CacheRead: 0.20, CacheWrite: 5.0, CacheWrite1h: 8.0},
		PricingKnown:        true,
		Architecture:        Architecture{Modality: "text+image"},
		SupportedParameters: []string{"tools", "tool_choice", "reasoning"},
	},
	{
		ID:            "anthropic/claude-3.5-sonnet",
		Name:          "Claude 3.5 Sonnet",
		ContextLength: 200000,
		Pricing: ModelPricing{
			Prompt:     3.0,
			Completion: 15.0,
		},
		Architecture: Architecture{
			Modality: "text+image",
		},
		SupportedParameters: []string{"tools", "functions"},
	},
	{
		ID:            "anthropic/claude-3.5-haiku",
		Name:          "Claude 3.5 Haiku",
		ContextLength: 200000,
		Pricing: ModelPricing{
			Prompt:     1.0,
			Completion: 5.0,
		},
		Architecture: Architecture{
			Modality: "text+image",
		},
		SupportedParameters: []string{"tools", "functions"},
	},
	{
		ID:            "anthropic/claude-3-opus",
		Name:          "Claude 3 Opus",
		ContextLength: 200000,
		Pricing: ModelPricing{
			Prompt:     15.0,
			Completion: 75.0,
		},
		Architecture: Architecture{
			Modality: "text+image",
		},
		SupportedParameters: []string{"tools", "functions"},
	},
}

var anthropicModelIndex map[string]ModelInfo

func init() {
	anthropicModelIndex = make(map[string]ModelInfo, len(anthropicModels))
	for _, m := range anthropicModels {
		anthropicModelIndex[m.ID] = m
	}
}

// NewAnthropicProvider builds an Anthropic provider.
func NewAnthropicProvider(apiKey, baseURL string, networkLogsEnabled bool) *AnthropicProvider {
	baseURL = strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(baseURL), "/"), "/v1")
	if baseURL == "" {
		baseURL = anthropicBaseURL
	}
	transport := NewLoggingTransportWithEnabled(nil, networkLogsEnabled)
	return &AnthropicProvider{
		apiKey:  apiKey,
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout:   defaultTimeout,
			Transport: transport,
		},
		transport: NewProviderTransport(ProviderTransportOptions{}),
		version:   "2023-06-01",
	}
}

// ID returns provider identifier.
func (p *AnthropicProvider) ID() string {
	return "anthropic"
}

// catalog is the configured overrides, named for this provider even when the
// provider was built without a config.
func (p *AnthropicProvider) catalog() catalogOverrides {
	o := p.overrides
	o.prefix = "anthropic/"
	return o
}

// FetchCatalog returns static catalog entries, with configured models and
// prices.
func (p *AnthropicProvider) FetchCatalog() (*ModelCatalog, error) {
	return &ModelCatalog{Data: p.catalog().apply(anthropicModels)}, nil
}

// GetModelInfo looks up metadata by ID, bare or qualified.
func (p *AnthropicProvider) GetModelInfo(modelID string) (*ModelInfo, error) {
	if strings.Contains(modelID, "/") && !strings.HasPrefix(modelID, "anthropic/") {
		return nil, fmt.Errorf("anthropic model not found: %s", modelID)
	}
	if info, ok := p.catalog().lookup(anthropicModels, anthropicModelIndex, modelID); ok {
		return &info, nil
	}
	return nil, fmt.Errorf("anthropic model not found: %s", modelID)
}

// ChatCompletion executes a non-streaming request.
func (p *AnthropicProvider) ChatCompletion(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	anthReq, err := p.toAnthropicRequest(req, false)
	if err != nil {
		return nil, err
	}

	anthropicResp, err := p.send(ctx, anthReq)
	if err != nil {
		return nil, err
	}
	if anthReq.requiredTool != "" && !anthropicResp.calledTool(anthReq.requiredTool) {
		// The model answered in text although the caller required a tool.
		// Show it its answer and ask once more for the tool call.
		first := anthropicResp
		anthReq.Messages = append(anthReq.Messages,
			anthropicMessage{Role: "assistant", Content: first.Content},
			anthropicMessage{Role: "user", Content: []anthropicContent{{Type: "text", Text: forcedToolInstruction(anthReq.requiredTool) + " Call it now."}}},
		)
		anthropicResp, err = p.send(ctx, anthReq)
		if err != nil {
			return nil, err
		}
		anthropicResp.Usage = first.Usage.plus(anthropicResp.Usage)
		if !anthropicResp.calledTool(anthReq.requiredTool) {
			return nil, fmt.Errorf("anthropic: %s did not call the required tool after a retry", anthReq.Model)
		}
	}
	return anthropicResp.toChatResponse()
}

func (p *AnthropicProvider) send(ctx context.Context, anthReq *anthropicRequest) (anthropicResponse, error) {
	data, err := p.transport.Do(ctx, p.httpClient, "POST", p.baseURL+"/v1/messages", anthReq, p.setAuthHeaders)
	if err != nil {
		return anthropicResponse{}, err
	}
	var anthropicResp anthropicResponse
	if err := json.Unmarshal(data, &anthropicResp); err != nil {
		return anthropicResponse{}, fmt.Errorf("decode anthropic response: %w", err)
	}
	return anthropicResp, nil
}

// forcedToolInstruction asks for a tool call in words, for models that
// reject a forced tool_choice.
func forcedToolInstruction(choice string) string {
	if choice == "required" {
		return "Respond only by calling one of the provided tools with complete arguments. Do not reply with text alone."
	}
	return fmt.Sprintf("Respond only by calling the %s tool with complete arguments. Do not reply with text alone.", choice)
}

// calledTool reports whether the response holds a call that satisfies a
// forced tool_choice: any tool for "required", else the named tool.
func (a anthropicResponse) calledTool(choice string) bool {
	for _, c := range a.Content {
		if c.Type == "tool_use" && (choice == "required" || c.Name == choice) {
			return true
		}
	}
	return false
}

// plus adds two responses' token counts, for a request sent twice.
func (u anthropicUsage) plus(next anthropicUsage) anthropicUsage {
	u.InputTokens += next.InputTokens
	u.OutputTokens += next.OutputTokens
	u.CacheReadTokens += next.CacheReadTokens
	u.CacheWriteTokens += next.CacheWriteTokens
	u.CacheCreation.OneHourTokens += next.CacheCreation.OneHourTokens
	u.OutputDetails.ThinkingTokens += next.OutputDetails.ThinkingTokens
	return u
}

func (p *AnthropicProvider) setAuthHeaders(req *http.Request) {
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("anthropic-version", p.version)
}

// ChatCompletionStream falls back to non-streaming implementation for now.
func (p *AnthropicProvider) ChatCompletionStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, <-chan error) {
	chunkChan := make(chan StreamChunk, 1)
	errChan := make(chan error, 1)

	go func() {
		defer close(chunkChan)
		defer close(errChan)

		resp, err := p.ChatCompletion(ctx, req)
		if err != nil {
			errChan <- err
			return
		}

		if len(resp.Choices) == 0 {
			errChan <- fmt.Errorf("anthropic: empty response choices")
			return
		}

		chunkChan <- StreamChunk{
			ID:                resp.ID,
			Model:             resp.Model,
			ExecutionIdentity: cloneExecutionIdentity(resp.ExecutionIdentity),
			Choices: []StreamChoice{
				{
					Index:        0,
					Delta:        toStreamDelta(resp.Choices[0].Message),
					FinishReason: &resp.Choices[0].FinishReason,
				},
			},
			Usage: &resp.Usage,
		}
	}()

	return chunkChan, errChan
}

// SetTimeout updates the Anthropic client timeout (0 disables timeout).
func (p *AnthropicProvider) SetTimeout(timeout time.Duration) {
	if p.httpClient != nil {
		p.httpClient.Timeout = timeout
	}
}

func (p *AnthropicProvider) toAnthropicRequest(req ChatRequest, stream bool) (*anthropicRequest, error) {
	anthReq := &anthropicRequest{
		Model:        normalizeModelForProvider(req.Model, "anthropic"),
		MaxTokens:    req.MaxTokens,
		Temperature:  req.Temperature,
		Stream:       stream,
		CacheControl: req.CacheControl,
	}
	if anthReq.MaxTokens == 0 {
		anthReq.MaxTokens = 4096
	}
	forcedTool := ""
	if rule, ok := claudeRequestRules(anthReq.Model); ok {
		// Claude 5 models think adaptively and reject sampling parameters.
		anthReq.Temperature = 0
		if !rule.canForceTool && len(req.Tools) > 0 && req.ToolChoice != "" && req.ToolChoice != "auto" && req.ToolChoice != "none" {
			// These models return a 400 for a forced tool_choice. Ask for the
			// tool in the system prompt instead, send tool_choice auto, and let
			// ChatCompletion retry once when no tool call comes back.
			forcedTool = req.ToolChoice
			req.ToolChoice = "auto"
			if req.Reasoning != nil && req.Reasoning.Enabled != nil && !*req.Reasoning.Enabled && !rule.canDisableThinking {
				// Callers turn thinking off only because some models cannot
				// think and answer a forced tool call; auto tool choice can.
				reasoning := *req.Reasoning
				reasoning.Enabled = nil
				req.Reasoning = &reasoning
			}
		}
		if req.Reasoning != nil {
			if req.Reasoning.Enabled != nil && !*req.Reasoning.Enabled {
				if !rule.canDisableThinking {
					return nil, fmt.Errorf("anthropic: %s cannot disable thinking; select a lower effort", rule.name)
				}
				anthReq.Thinking = &anthropicThinkingConfig{Type: "disabled"}
			}
			if effort := strings.ToLower(strings.TrimSpace(req.Reasoning.Effort)); effort != "" {
				switch effort {
				case "low", "medium", "high", "xhigh", "max":
					anthReq.OutputConfig = &anthropicOutputConfig{Effort: effort}
				default:
					return nil, fmt.Errorf("anthropic: unsupported effort %q", effort)
				}
			}
		}
		if !rule.canForceTool && req.ToolChoice != "" && req.ToolChoice != "auto" && req.ToolChoice != "none" {
			// A forced tool_choice with no tools to choose from.
			return nil, fmt.Errorf("anthropic: %s requires automatic tool choice", rule.name)
		}
	}

	var systemParts []string
	for _, msg := range req.Messages {
		switch msg.Role {
		case "system":
			if text := messageContentToText(msg.Content); strings.TrimSpace(text) != "" {
				systemParts = append(systemParts, text)
			}
		case "user", "assistant":
			content, err := anthropicMessageContent(msg)
			if err != nil {
				return nil, err
			}
			if len(content) == 0 {
				continue
			}
			anthReq.Messages = append(anthReq.Messages, anthropicMessage{
				Role:    msg.Role,
				Content: content,
			})
		case "tool":
			// Anthropic requires tool_result blocks in a user message.
			// Merge consecutive tool results into the preceding user message
			// to maintain required user/assistant alternation.
			toolResult := anthropicContent{
				Type:      "tool_result",
				ToolUseID: msg.ToolCallID,
				Content:   messageContentToText(msg.Content),
			}
			n := len(anthReq.Messages)
			if n > 0 && anthReq.Messages[n-1].Role == "user" {
				anthReq.Messages[n-1].Content = append(anthReq.Messages[n-1].Content, toolResult)
			} else {
				anthReq.Messages = append(anthReq.Messages, anthropicMessage{
					Role:    "user",
					Content: []anthropicContent{toolResult},
				})
			}
		}
	}

	if forcedTool != "" {
		anthReq.requiredTool = forcedTool
		systemParts = append(systemParts, forcedToolInstruction(forcedTool))
	}
	if len(systemParts) > 0 {
		system := strings.Join(systemParts, "\n\n")
		if req.PromptCache != nil && req.PromptCache.Enabled {
			// A breakpoint on the system prompt caches the tools and system
			// prompt together, whatever follows them.
			anthReq.System = []anthropicContent{{
				Type:         "text",
				Text:         system,
				CacheControl: &CacheControl{Type: promptCacheControlType, TTL: req.PromptCache.TTL},
			}}
		} else {
			anthReq.System = system
		}
	}
	if len(req.Tools) > 0 && req.ToolChoice != "none" {
		anthReq.Tools = toAnthropicTools(req.Tools)
		anthReq.ToolChoice = toAnthropicToolChoice(req.ToolChoice)
	}

	return anthReq, nil
}

// anthropicRequest maps to Anthropics messages payload.
type anthropicRequest struct {
	Model        string                   `json:"model"`
	System       any                      `json:"system,omitempty"` // a string, or text blocks with cache_control
	Messages     []anthropicMessage       `json:"messages"`
	MaxTokens    int                      `json:"max_tokens"`
	Temperature  float64                  `json:"temperature,omitempty"`
	Stream       bool                     `json:"stream"`
	Tools        []anthropicTool          `json:"tools,omitempty"`
	ToolChoice   *anthropicToolChoice     `json:"tool_choice,omitempty"`
	Metadata     map[string]string        `json:"metadata,omitempty"`
	OutputConfig *anthropicOutputConfig   `json:"output_config,omitempty"`
	Thinking     *anthropicThinkingConfig `json:"thinking,omitempty"`
	CacheControl *CacheControl            `json:"cache_control,omitempty"`
	// requiredTool is the forced tool_choice ("required" or a tool name) that
	// was sent as auto because the model rejects forced tool use.
	requiredTool string
}

type anthropicThinkingConfig struct {
	Type string `json:"type"`
}

type anthropicOutputConfig struct {
	Effort string `json:"effort"`
}

type anthropicMessage struct {
	Role    string             `json:"role"`
	Content []anthropicContent `json:"content"`
}

type anthropicContent struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	Content   any            `json:"content,omitempty"`
	// Source is an image block's data.
	Source *anthropicImageSource `json:"source,omitempty"`
	// CacheControl ends a cached prefix at this block.
	CacheControl *CacheControl `json:"cache_control,omitempty"`
	raw          json.RawMessage
}

// Keep the complete native block, including empty thinking and unknown fields.
// Signed thinking is bound to the original assistant content and block order.
func (c *anthropicContent) UnmarshalJSON(data []byte) error {
	type plain anthropicContent
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*c = anthropicContent(decoded)
	c.raw = append(json.RawMessage(nil), data...)
	return nil
}

func (c anthropicContent) MarshalJSON() ([]byte, error) {
	if len(c.raw) > 0 {
		return c.raw, nil
	}
	type plain anthropicContent
	return json.Marshal(plain(c))
}

const anthropicContentFormat = "anthropic-content-v1"

type anthropicTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputSchema any    `json:"input_schema"`
}

type anthropicToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

type anthropicResponse struct {
	ID         string             `json:"id"`
	Model      string             `json:"model"`
	Content    []anthropicContent `json:"content"`
	StopReason string             `json:"stop_reason,omitempty"`
	Usage      anthropicUsage     `json:"usage"`
}

type anthropicUsage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_input_tokens"`
	CacheWriteTokens int `json:"cache_creation_input_tokens"`
	CacheCreation    struct {
		OneHourTokens int `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
	OutputDetails struct {
		ThinkingTokens int `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
}

func (a anthropicResponse) toChatResponse() (*ChatResponse, error) {
	var parts []string
	var toolCalls []ToolCall
	var hasThinking bool
	for _, c := range a.Content {
		switch c.Type {
		case "thinking", "redacted_thinking":
			hasThinking = true
		case "text":
			if strings.TrimSpace(c.Text) != "" {
				parts = append(parts, c.Text)
			}
		case "tool_use":
			payload, err := json.Marshal(c.Input)
			if err != nil {
				return nil, fmt.Errorf("marshal tool input: %w", err)
			}
			toolCalls = append(toolCalls, ToolCall{
				ID:   c.ID,
				Type: "function",
				Function: FunctionCall{
					Name:      c.Name,
					Arguments: string(payload),
				},
			})
		}
	}

	content := strings.Join(parts, "\n")
	msg := Message{
		Role:      "assistant",
		Content:   content,
		ToolCalls: toolCalls,
	}
	if hasThinking {
		content, err := json.Marshal(a.Content)
		if err != nil {
			return nil, fmt.Errorf("preserve anthropic assistant content: %w", err)
		}
		msg.ReasoningDetails = []ReasoningDetail{{
			Type: "reasoning.encrypted", Format: anthropicContentFormat, Data: string(content),
		}}
	}
	finishReason := "stop"
	if a.StopReason == "tool_use" {
		finishReason = "tool_calls"
	} else if strings.TrimSpace(a.StopReason) != "" {
		finishReason = a.StopReason
	}

	usage := Usage{
		PromptTokens:       a.Usage.InputTokens + a.Usage.CacheReadTokens + a.Usage.CacheWriteTokens,
		CompletionTokens:   a.Usage.OutputTokens,
		CacheWriteTokens:   a.Usage.CacheWriteTokens,
		CacheWrite1hTokens: a.Usage.CacheCreation.OneHourTokens,
	}
	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	if a.Usage.CacheReadTokens > 0 {
		usage.PromptTokensDetails = &PromptTokensDetails{CachedTokens: a.Usage.CacheReadTokens}
	}
	if a.Usage.OutputDetails.ThinkingTokens > 0 {
		usage.CompletionTokenDetails = &CompletionTokenDetails{ReasoningTokens: a.Usage.OutputDetails.ThinkingTokens}
	}
	return &ChatResponse{
		ID:    a.ID,
		Model: "anthropic/" + a.Model,
		Choices: []Choice{
			{
				Index:        0,
				Message:      msg,
				FinishReason: finishReason,
			},
		},
		Usage:             usage,
		ExecutionIdentity: observedExecutionIdentity(a.ID, a.Model, nil),
	}, nil
}

func toAnthropicTools(rawTools []map[string]any) []anthropicTool {
	tools := make([]anthropicTool, 0, len(rawTools))
	for _, raw := range rawTools {
		function, _ := raw["function"].(map[string]any)
		name, _ := function["name"].(string)
		description, _ := function["description"].(string)
		schema := function["parameters"]
		if strings.TrimSpace(name) == "" {
			continue
		}
		tools = append(tools, anthropicTool{
			Name:        name,
			Description: description,
			InputSchema: schema,
		})
	}
	return tools
}

func toAnthropicToolChoice(choice string) *anthropicToolChoice {
	choice = strings.TrimSpace(choice)
	switch choice {
	case "", "auto":
		return &anthropicToolChoice{Type: "auto"}
	case "required":
		return &anthropicToolChoice{Type: "any"}
	default:
		return &anthropicToolChoice{Type: "tool", Name: choice}
	}
}

func anthropicMessageContent(msg Message) ([]anthropicContent, error) {
	if msg.Role == "assistant" {
		for _, detail := range msg.ReasoningDetails {
			if detail.Format == anthropicContentFormat {
				var content []anthropicContent
				if err := json.Unmarshal([]byte(detail.Data), &content); err != nil {
					return nil, fmt.Errorf("restore anthropic assistant content: %w", err)
				}
				return content, nil
			}
		}
	}
	content := anthropicTextBlocks(msg.Content)
	for _, call := range msg.ToolCalls {
		input := map[string]any{}
		if strings.TrimSpace(call.Function.Arguments) != "" {
			if err := json.Unmarshal([]byte(call.Function.Arguments), &input); err != nil {
				input = map[string]any{"raw": call.Function.Arguments}
			}
		}
		content = append(content, anthropicContent{
			Type:  "tool_use",
			ID:    call.ID,
			Name:  call.Function.Name,
			Input: input,
		})
	}
	return content, nil
}

func anthropicTextBlocks(content any) []anthropicContent {
	if parts, ok := content.([]ContentPart); ok && (contentHasCacheControl(parts) || contentHasImage(parts)) {
		// Keep each part its own block so images reach the model and a cache
		// breakpoint ends exactly where the stable prefix ends.
		var blocks []anthropicContent
		for _, part := range parts {
			var block anthropicContent
			switch {
			case part.ImageURL != nil && part.ImageURL.URL != "":
				block = anthropicContent{Type: "image", Source: anthropicImageSourceFor(part.ImageURL.URL)}
			case (part.Type == "" || part.Type == "text") && part.Text != "":
				block = anthropicContent{Type: "text", Text: part.Text}
			default:
				continue
			}
			if part.CacheControl != nil {
				control := *part.CacheControl
				block.CacheControl = &control
			}
			blocks = append(blocks, block)
		}
		return blocks
	}
	text := strings.TrimSpace(messageContentToText(content))
	if text == "" {
		return nil
	}
	return []anthropicContent{{Type: "text", Text: text}}
}

func toStreamDelta(msg Message) MessageDelta {
	delta := MessageDelta{
		Role:             msg.Role,
		Content:          messageContentToText(msg.Content),
		Reasoning:        msg.Reasoning,
		ReasoningDetails: msg.ReasoningDetails,
	}
	if len(msg.ToolCalls) == 0 {
		return delta
	}
	delta.ToolCalls = make([]ToolCallDelta, 0, len(msg.ToolCalls))
	for i, call := range msg.ToolCalls {
		delta.ToolCalls = append(delta.ToolCalls, ToolCallDelta{
			Index: i,
			ID:    call.ID,
			Type:  call.Type,
			Function: &FunctionCallDelta{
				Name:      call.Function.Name,
				Arguments: call.Function.Arguments,
			},
		})
	}
	return delta
}

// anthropicImageSource is the source of an Anthropic image block: base64
// data with its media type, or a URL.
type anthropicImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

// anthropicImageSourceFor turns an image_url (a data: URL or a web URL) into
// an Anthropic image source.
func anthropicImageSourceFor(url string) *anthropicImageSource {
	if rest, ok := strings.CutPrefix(url, "data:"); ok {
		if meta, data, ok := strings.Cut(rest, ","); ok {
			if mediaType, ok := strings.CutSuffix(meta, ";base64"); ok {
				return &anthropicImageSource{Type: "base64", MediaType: mediaType, Data: data}
			}
		}
	}
	return &anthropicImageSource{Type: "url", URL: url}
}

func contentHasImage(parts []ContentPart) bool {
	for _, part := range parts {
		if part.ImageURL != nil && part.ImageURL.URL != "" {
			return true
		}
	}
	return false
}
