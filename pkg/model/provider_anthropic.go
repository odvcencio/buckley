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
	if len(req.Tools) > 0 {
		return nil, fmt.Errorf("anthropic provider does not support tool calling yet")
	}

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
					Index: 0,
					Delta: MessageDelta{
						Role:    resp.Choices[0].Message.Role,
						Content: fmt.Sprint(resp.Choices[0].Message.Content),
					},
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

	cache := req.PromptCache
	cacheEnabled := cache != nil && cache.Enabled

	var systemParts []string
	for _, msg := range req.Messages {
		text := messageContentToText(msg.Content)
		switch msg.Role {
		case "system":
			systemParts = append(systemParts, text)
		case "user", "assistant":
			anthReq.Messages = append(anthReq.Messages, anthropicMessage{
				Role: msg.Role,
				Content: []anthropicContent{
					{
						Type: "text",
						Text: text,
					},
				},
			})
		case "tool":
			return nil, fmt.Errorf("anthropic provider does not support tool response messages")
		}
	}

	if len(systemParts) > 0 {
		if cacheEnabled && cache.SystemMessages > 0 {
			systemBlocks := make([]anthropicContent, len(systemParts))
			for i, part := range systemParts {
				block := anthropicContent{
					Type: "text",
					Text: part,
				}
				if i < cache.SystemMessages {
					block.CacheControl = promptCacheControl()
				}
				systemBlocks[i] = block
			}
			anthReq.System = systemBlocks
		} else {
			anthReq.System = strings.Join(systemParts, "\n\n")
		}
	}

	if cacheEnabled && cache.TailMessages > 0 && len(anthReq.Messages) > 0 {
		start := len(anthReq.Messages) - cache.TailMessages
		if start < 0 {
			start = 0
		}
		for i := start; i < len(anthReq.Messages); i++ {
			for j := range anthReq.Messages[i].Content {
				anthReq.Messages[i].Content[j].CacheControl = promptCacheControl()
			}
		}
	}

	return anthReq, nil
}

func promptCacheControl() *anthropicCacheControl {
	return &anthropicCacheControl{Type: "ephemeral"}
}

// anthropicRequest maps to Anthropics messages payload.
type anthropicRequest struct {
	Model       string             `json:"model"`
	System      any                `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature float64            `json:"temperature,omitempty"`
	Stream      bool               `json:"stream"`
	Tools       []map[string]any   `json:"tools,omitempty"`
	ToolChoice  map[string]any     `json:"tool_choice,omitempty"`
	Metadata    map[string]string  `json:"metadata,omitempty"`
}

type anthropicMessage struct {
	Role    string             `json:"role"`
	Content []anthropicContent `json:"content"`
}

type anthropicContent struct {
	Type         string                 `json:"type"`
	Text         string                 `json:"text"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicCacheControl struct {
	Type string `json:"type"`
}

type anthropicResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Content []struct {
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func (a anthropicResponse) toChatResponse() (*ChatResponse, error) {
	var parts []string
	for _, c := range a.Content {
		parts = append(parts, c.Text)
	}

	content := strings.Join(parts, "\n")
	msg := Message{
		Role:    "assistant",
		Content: content,
	}

	return &ChatResponse{
		ID:    a.ID,
		Model: "anthropic/" + a.Model,
		Choices: []Choice{
			{
				Index:        0,
				Message:      msg,
				FinishReason: "stop",
			},
		},
		Usage: Usage{
			PromptTokens:     a.Usage.InputTokens,
			CompletionTokens: a.Usage.OutputTokens,
			TotalTokens:      a.Usage.InputTokens + a.Usage.OutputTokens,
		},
	}, nil
}
