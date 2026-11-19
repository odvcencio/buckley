package model

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const openAIBaseURL = "https://api.openai.com/v1"

// OpenAIProvider provides completions via OpenAI's native API.
type OpenAIProvider struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
	transport  *ProviderTransport
	overrides  catalogOverrides
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
		SupportedParameters:         []string{},
		supportedParametersComplete: true,
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
		SupportedParameters: []string{"tools", "reasoning_effort"},
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

// catalog is the configured overrides, named for this provider even when the
// provider was built without a config.
func (p *OpenAIProvider) catalog() catalogOverrides {
	o := p.overrides
	o.prefix = "openai/"
	return o
}

// FetchCatalog returns the curated catalog.
func (p *OpenAIProvider) FetchCatalog() (*ModelCatalog, error) {
	return &ModelCatalog{Data: p.catalog().apply(openAIModels)}, nil
}

// GetModelInfo returns static metadata for a given model.
func (p *OpenAIProvider) GetModelInfo(modelID string) (*ModelInfo, error) {
	if strings.TrimSpace(modelID) == "" {
		return nil, fmt.Errorf("openai model not found: %s", modelID)
	}
	if info, ok := p.catalog().lookup(openAIModels, openAIModelIndex, modelID); ok && (strings.HasPrefix(modelID, "openai/") || !strings.Contains(modelID, "/")) {
		return &info, nil
	}
	// Forward-compatible fallback: unknown IDs stay usable with minimal
	// metadata, and with a configured price when there is one.
	info := p.catalog().withPrice(ModelInfo{ID: modelID, Name: modelID})
	return &info, nil
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

// openAIChatLimits moves an output limit set as max_tokens to
// max_completion_tokens. OpenAI's Chat Completions API deprecated max_tokens,
// and its reasoning models reject it ("Unsupported parameter: 'max_tokens'");
// every current chat model accepts max_completion_tokens.
func openAIChatLimits(req ChatRequest) ChatRequest {
	if req.MaxTokens > 0 {
		if req.MaxCompletionTokens <= 0 {
			req.MaxCompletionTokens = req.MaxTokens
		}
		req.MaxTokens = 0
	}
	return req
}

func (p *OpenAIProvider) invoke(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	// session_id is an OpenRouter extension, not an OpenAI request field.
	req.SessionID = ""
	req = openAIChatLimits(req)
	data, err := p.transport.Do(ctx, p.httpClient, "POST", p.baseURL+"/chat/completions", req, p.setAuthHeaders)
	if err != nil {
		return nil, err
	}

	var chatResp ChatResponse
	if err := json.Unmarshal(data, &chatResp); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	chatResp.AttemptEvidence = nil
	chatResp.ExecutionIdentity = observedExecutionIdentity(chatResp.ID, chatResp.Model, nil)

	return &chatResp, nil
}

func (p *OpenAIProvider) invokeStream(ctx context.Context, req ChatRequest, chunkChan chan<- StreamChunk) error {
	req.SessionID = ""
	req = openAIChatLimits(req)
	return p.transport.Stream(ctx, p.httpClient, "POST", p.baseURL+"/chat/completions", req, p.setAuthHeaders, chunkChan)
}
