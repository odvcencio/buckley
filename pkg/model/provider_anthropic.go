package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const anthropicBaseURL = "https://api.anthropic.com"

// AnthropicProvider calls the Claude Messages API.
type AnthropicProvider struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
	version    string
}

var anthropicModels = []ModelInfo{
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
		version: "2023-06-01",
	}
}

// ID returns provider identifier.
func (p *AnthropicProvider) ID() string {
	return "anthropic"
}

// FetchCatalog returns static catalog entries.
func (p *AnthropicProvider) FetchCatalog() (*ModelCatalog, error) {
	return &ModelCatalog{Data: anthropicModels}, nil
}

// GetModelInfo looks up metadata by ID.
func (p *AnthropicProvider) GetModelInfo(modelID string) (*ModelInfo, error) {
	if info, ok := anthropicModelIndex[modelID]; ok {
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

	body, err := json.Marshal(anthReq)
	if err != nil {
		return nil, fmt.Errorf("marshal anthropic request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", p.version)
	httpReq.Header.Set("content-type", "application/json")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("anthropic request failed: %s", resp.Status)
	}

	var anthropicResp anthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&anthropicResp); err != nil {
		return nil, fmt.Errorf("decode anthropic response: %w", err)
	}

	return anthropicResp.toChatResponse()
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
			ID:    resp.ID,
			Model: resp.Model,
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
		Model:       normalizeModelForProvider(req.Model, "anthropic"),
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Stream:      stream,
	}
	if anthReq.MaxTokens == 0 {
		anthReq.MaxTokens = 4096
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
			text := messageContentToText(msg.Content)
			anthReq.Messages = append(anthReq.Messages, anthropicMessage{
				Role: "user",
				Content: []anthropicContent{
					{
						Type:      "tool_result",
						ToolUseID: msg.ToolCallID,
						Content:   text,
					},
				},
			})
		}
	}

	if len(systemParts) > 0 {
		anthReq.System = strings.Join(systemParts, "\n\n")
	}
	if len(req.Tools) > 0 && req.ToolChoice != "none" {
		anthReq.Tools = toAnthropicTools(req.Tools)
		anthReq.ToolChoice = toAnthropicToolChoice(req.ToolChoice)
	}

	return anthReq, nil
}

// anthropicRequest maps to Anthropics messages payload.
type anthropicRequest struct {
	Model       string               `json:"model"`
	System      string               `json:"system,omitempty"`
	Messages    []anthropicMessage   `json:"messages"`
	MaxTokens   int                  `json:"max_tokens"`
	Temperature float64              `json:"temperature,omitempty"`
	Stream      bool                 `json:"stream"`
	Tools       []anthropicTool      `json:"tools,omitempty"`
	ToolChoice  *anthropicToolChoice `json:"tool_choice,omitempty"`
	Metadata    map[string]string    `json:"metadata,omitempty"`
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
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
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
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func (a anthropicResponse) toChatResponse() (*ChatResponse, error) {
	var parts []string
	var toolCalls []ToolCall
	for _, c := range a.Content {
		switch c.Type {
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
	finishReason := "stop"
	if a.StopReason == "tool_use" {
		finishReason = "tool_calls"
	} else if strings.TrimSpace(a.StopReason) != "" {
		finishReason = a.StopReason
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
		Usage: Usage{
			PromptTokens:     a.Usage.InputTokens,
			CompletionTokens: a.Usage.OutputTokens,
			TotalTokens:      a.Usage.InputTokens + a.Usage.OutputTokens,
		},
	}, nil
}

func toAnthropicTools(rawTools []map[string]any) []anthropicTool {
	tools := make([]anthropicTool, 0, len(rawTools))
	for _, raw := range rawTools {
		function, _ := raw["function"].(map[string]any)
		name, _ := function["name"].(string)
		description, _ := function["description"].(string)
		schema, _ := function["parameters"].(map[string]any)
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
	text := strings.TrimSpace(messageContentToText(content))
	if text == "" {
		return nil
	}
	return []anthropicContent{{Type: "text", Text: text}}
}

func toStreamDelta(msg Message) MessageDelta {
	delta := MessageDelta{
		Role:    msg.Role,
		Content: messageContentToText(msg.Content),
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
