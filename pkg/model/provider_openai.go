package model

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const openAIBaseURL = "https://api.openai.com/v1"

// OpenAIProvider provides completions via OpenAI's native API.
type OpenAIProvider struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

// openAIModels enumerates a curated subset of models with pricing/context info.
var openAIModels = []ModelInfo{
	{
		ID:            "openai/gpt-4.1",
		Name:          "GPT-4.1",
		ContextLength: 128000,
		Pricing: ModelPricing{
			Prompt:     30.0,
			Completion: 60.0,
		},
		Architecture: Architecture{
			Modality: "text+image",
		},
		SupportedParameters: []string{"tools"},
	},
	{
		ID:            "openai/gpt-4o",
		Name:          "GPT-4o",
		ContextLength: 128000,
		Pricing: ModelPricing{
			Prompt:     5.0,
			Completion: 15.0,
		},
		Architecture: Architecture{
			Modality: "multimodal",
		},
		SupportedParameters: []string{"tools", "functions"},
	},
	{
		ID:            "openai/gpt-4o-mini",
		Name:          "GPT-4o mini",
		ContextLength: 128000,
		Pricing: ModelPricing{
			Prompt:     0.15,
			Completion: 0.60,
		},
		Architecture: Architecture{
			Modality: "text+image",
		},
		SupportedParameters: []string{"tools", "functions"},
	},
	{
		ID:            "openai/o1-mini",
		Name:          "o1-mini",
		ContextLength: 128000,
		Pricing: ModelPricing{
			Prompt:     15.0,
			Completion: 60.0,
		},
		Architecture: Architecture{
			Modality: "text",
		},
		SupportedParameters: []string{},
	},
	{
		ID:            "openai/o3-mini",
		Name:          "o3-mini",
		ContextLength: 200000,
		Pricing: ModelPricing{
			Prompt:     45.0,
			Completion: 90.0,
		},
		Architecture: Architecture{
			Modality: "text",
		},
		SupportedParameters: []string{},
	},
}

// openAIModelIndex for quick lookup
var openAIModelIndex map[string]ModelInfo

func init() {
	openAIModelIndex = make(map[string]ModelInfo, len(openAIModels))
	for _, m := range openAIModels {
		openAIModelIndex[m.ID] = m
	}
}

// NewOpenAIProvider builds a provider using the supplied API key.
func NewOpenAIProvider(apiKey, baseURL string, networkLogsEnabled bool) *OpenAIProvider {
	if baseURL == "" {
		baseURL = openAIBaseURL
	}
	transport := NewLoggingTransportWithEnabled(nil, networkLogsEnabled)
	return &OpenAIProvider{
		apiKey:  apiKey,
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout:   defaultTimeout,
			Transport: transport,
		},
	}
}

// ID returns provider identifier.
func (p *OpenAIProvider) ID() string {
	return "openai"
}

// FetchCatalog returns the curated catalog.
func (p *OpenAIProvider) FetchCatalog() (*ModelCatalog, error) {
	return &ModelCatalog{Data: openAIModels}, nil
}

// GetModelInfo returns static metadata for a given model.
func (p *OpenAIProvider) GetModelInfo(modelID string) (*ModelInfo, error) {
	if info, ok := openAIModelIndex[modelID]; ok {
		return &info, nil
	}
	return nil, fmt.Errorf("openai model not found: %s", modelID)
}

// ChatCompletion executes a completion request via OpenAI.
func (p *OpenAIProvider) ChatCompletion(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	req.Stream = false
	return p.invoke(ctx, req)
}

// ChatCompletionStream streams responses from OpenAI.
func (p *OpenAIProvider) ChatCompletionStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, <-chan error) {
	req.Stream = true
	chunkChan := make(chan StreamChunk, 10)
	errChan := make(chan error, 1)

	go func() {
		defer close(chunkChan)
		defer close(errChan)
		if err := p.invokeStream(ctx, req, chunkChan); err != nil {
			errChan <- err
		}
	}()

	return chunkChan, errChan
}

// SetTimeout updates the OpenAI client timeout (0 disables timeout).
func (p *OpenAIProvider) SetTimeout(timeout time.Duration) {
	if p.httpClient != nil {
		p.httpClient.Timeout = timeout
	}
}

func (p *OpenAIProvider) invoke(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openai request failed: %s", resp.Status)
	}

	var chatResp ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	return &chatResp, nil
}

func (p *OpenAIProvider) invokeStream(ctx context.Context, req ChatRequest, chunkChan chan<- StreamChunk) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("openai streaming request failed: %s", resp.Status)
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		data := scanner.Text()
		if data == "" {
			continue
		}

		if len(data) > 6 && data[:6] == "data: " {
			data = data[6:]
		}
		if data == "[DONE]" {
			break
		}

		var chunk StreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return fmt.Errorf("decoding chunk: %w", err)
		}
		chunkChan <- chunk
	}

	return scanner.Err()
}
