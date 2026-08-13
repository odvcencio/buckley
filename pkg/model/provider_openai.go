package model

import (
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
	transport  *ProviderTransport
}

// openAIModels enumerates a curated subset of models with pricing/context info.
var openAIModels = []ModelInfo{
	{
		ID:            "openai/gpt-5.5",
		Name:          "GPT-5.5",
		ContextLength: 400000,
		Architecture: Architecture{
			Modality: "text+image",
		},
		SupportedParameters: []string{"tools", "reasoning"},
	},
	{
		ID:            "openai/gpt-5.4",
		Name:          "GPT-5.4",
		ContextLength: 400000,
		Architecture: Architecture{
			Modality: "text+image",
		},
		SupportedParameters: []string{"tools", "reasoning"},
	},
	{
		ID:            "openai/gpt-5.4-mini",
		Name:          "GPT-5.4 mini",
		ContextLength: 400000,
		Architecture: Architecture{
			Modality: "text+image",
		},
		SupportedParameters: []string{"tools", "reasoning"},
	},
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
		transport: NewProviderTransport(ProviderTransportOptions{}),
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
	// stream_options is a streaming-only OpenAI field. Cost-bounded request
	// normalization includes it in the conservative admission envelope, but
	// ordinary non-streaming dispatch must not put it on the wire.
	req.StreamOptions = nil
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

func (p *OpenAIProvider) setAuthHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
}

func (p *OpenAIProvider) invoke(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	data, err := p.transport.Do(ctx, p.httpClient, "POST", p.baseURL+"/chat/completions", req, p.setAuthHeaders)
	if err != nil {
		return nil, err
	}

	var chatResp ChatResponse
	if err := json.Unmarshal(data, &chatResp); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	return &chatResp, nil
}

func (p *OpenAIProvider) invokeStream(ctx context.Context, req ChatRequest, chunkChan chan<- StreamChunk) error {
	return p.transport.Stream(ctx, p.httpClient, "POST", p.baseURL+"/chat/completions", req, p.setAuthHeaders, chunkChan)
}
