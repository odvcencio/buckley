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
// The first entry is the fallback when a configured model is missing, so the
// GPT-6 family is listed last.
//
// GPT-6 prices are the Standard tier for prompts up to 272K tokens, per
// million tokens, from developers.openai.com/api/docs/pricing (20 Nov 2026).
// CacheWrite is the 30-minute write price; Buckley does not request the
// 12-hour lifetime. The context window of gpt-6.1-sol, gpt-6-sol and
// gpt-6-astra is not shown on the models page; 1.05M is assumed from the
// others.
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
	{
		ID:                  "openai/gpt-6.2-sol",
		Name:                "GPT-6.2 Sol",
		ContextLength:       1050000,
		MaxCompletionTokens: 128000,
		Pricing:             ModelPricing{Prompt: 2.0, Completion: 10.0, CacheRead: 0.1, CacheWrite: 2.5},
		PricingKnown:        true,
		Architecture:        Architecture{Modality: "text+image"},
		SupportedParameters: []string{"tools"},
	},
	{
		ID:                  "openai/gpt-6.2-astra",
		Name:                "GPT-6.2 Astra",
		ContextLength:       1050000,
		MaxCompletionTokens: 128000,
		Pricing:             ModelPricing{Prompt: 8.0, Completion: 40.0, CacheRead: 0.4, CacheWrite: 10.0},
		PricingKnown:        true,
		Architecture:        Architecture{Modality: "text+image"},
		SupportedParameters: []string{"tools"},
	},
	{
		ID:                  "openai/gpt-6.1-terra",
		Name:                "GPT-6.1 Terra",
		ContextLength:       1050000,
		MaxCompletionTokens: 128000,
		Pricing:             ModelPricing{Prompt: 1.0, Completion: 5.0, CacheRead: 0.05, CacheWrite: 1.25},
		PricingKnown:        true,
		Architecture:        Architecture{Modality: "text+image"},
		SupportedParameters: []string{"tools"},
	},
	{
		ID:                  "openai/gpt-6.1-sol",
		Name:                "GPT-6.1 Sol",
		ContextLength:       1050000,
		MaxCompletionTokens: 128000,
		Pricing:             ModelPricing{Prompt: 2.0, Completion: 10.0, CacheRead: 0.1, CacheWrite: 2.5},
		PricingKnown:        true,
		Architecture:        Architecture{Modality: "text+image"},
		SupportedParameters: []string{"tools"},
	},
	{
		ID:                  "openai/gpt-6-sol",
		Name:                "GPT-6 Sol",
		ContextLength:       1050000,
		MaxCompletionTokens: 128000,
		Pricing:             ModelPricing{Prompt: 2.0, Completion: 10.0, CacheRead: 0.2, CacheWrite: 2.5},
		PricingKnown:        true,
		Architecture:        Architecture{Modality: "text+image"},
		SupportedParameters: []string{"tools"},
	},
	{
		ID:                  "openai/gpt-6-astra",
		Name:                "GPT-6 Astra",
		ContextLength:       1050000,
		MaxCompletionTokens: 128000,
		Pricing:             ModelPricing{Prompt: 10.0, Completion: 50.0, CacheRead: 1.0, CacheWrite: 12.5},
		PricingKnown:        true,
		Architecture:        Architecture{Modality: "text+image"},
		SupportedParameters: []string{"tools"},
	},
	{
		ID:                  "openai/gpt-6-luna",
		Name:                "GPT-6 Luna",
		ContextLength:       1050000,
		MaxCompletionTokens: 128000,
		Pricing:             ModelPricing{Prompt: 0.1, Completion: 0.5, CacheRead: 0.01, CacheWrite: 0.125},
		PricingKnown:        true,
		Architecture:        Architecture{Modality: "text+image"},
		SupportedParameters: []string{"tools"},
	},
}

// openAIModelIndex for quick lookup
var openAIModelIndex map[string]ModelInfo

func init() {
	// Each GPT-6 model is also listed in pro mode: the same model and
	// per-token price, sent with reasoning.mode "pro" on the Responses API.
	for _, m := range append([]ModelInfo(nil), openAIModels...) {
		if openAIModelMajor(m.ID) >= 6 {
			pro := m
			pro.ID += "-pro"
			pro.Name += " Pro"
			openAIModels = append(openAIModels, pro)
		}
	}
	openAIModelIndex = make(map[string]ModelInfo, len(openAIModels))
	for _, m := range openAIModels {
		openAIModelIndex[m.ID] = m
	}
}

// openAIProModel splits a pro-mode model name, such as gpt-6.1-terra-pro,
// into the model OpenAI serves (gpt-6.1-terra) and true. OpenAI has no
// separate GPT-6 pro models: pro is reasoning.mode "pro" on the base model.
func openAIProModel(model string) (string, bool) {
	base, ok := strings.CutSuffix(strings.TrimSpace(model), "-pro")
	if !ok || openAIModelMajor(base) < 6 {
		return model, false
	}
	return base, true
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
	if openAIUsesResponses(req) {
		return p.chatCompletionViaResponses(ctx, req)
	}
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
		if openAIUsesResponses(req) {
			// The Responses turn is not streamed: one chunk carries it all.
			resp, err := p.chatCompletionViaResponses(ctx, req)
			if err != nil {
				errChan <- err
				return
			}
			chunkChan <- StreamChunk{
				ID:                resp.ID,
				Model:             resp.Model,
				ExecutionIdentity: cloneExecutionIdentity(resp.ExecutionIdentity),
				Choices: []StreamChoice{{
					Index:        0,
					Delta:        toStreamDelta(resp.Choices[0].Message),
					FinishReason: &resp.Choices[0].FinishReason,
				}},
				Usage: &resp.Usage,
			}
			return
		}
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

// openAIChatRequest is a Chat Completions request body. OpenAI rejects
// fields it does not know with HTTP 400 ("Unknown parameter"), so it carries
// only the OpenAI fields of a ChatRequest.
type openAIChatRequest struct {
	ChatRequest
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

// openAIChatPayload drops the OpenRouter extensions a ChatRequest can carry
// (session_id, trace, transforms, models, provider, usage,
// include_reasoning and top-level cache_control) and sends reasoning as
// reasoning_effort, the Chat Completions field, on reasoning models only. A
// request to turn reasoning off sends no effort, because the accepted lowest
// value differs by model (GPT-6.2 rejects "none").
func openAIChatPayload(req ChatRequest) openAIChatRequest {
	req = openAIChatLimits(req)
	req.SessionID = ""
	req.Trace = nil
	req.Transforms = nil
	req.Models = nil
	req.Provider = nil
	req.Usage = nil
	req.IncludeReasoning = nil
	req.CacheControl = nil
	payload := openAIChatRequest{}
	if req.Reasoning != nil && openAIReasoningModel(req.Model) && (req.Reasoning.Enabled == nil || *req.Reasoning.Enabled) {
		payload.ReasoningEffort = strings.ToLower(strings.TrimSpace(req.Reasoning.Effort))
	}

	req.Reasoning = nil
	payload.ChatRequest = req
	return payload
}

func (p *OpenAIProvider) invoke(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	data, err := p.transport.Do(ctx, p.httpClient, "POST", p.baseURL+"/chat/completions", openAIChatPayload(req), p.setAuthHeaders)
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
	return p.transport.Stream(ctx, p.httpClient, "POST", p.baseURL+"/chat/completions", openAIChatPayload(req), p.setAuthHeaders, chunkChan)
}

// openAIReasoningModel reports whether an OpenAI model takes
// reasoning_effort: the o-series and GPT-5 and later.
func openAIReasoningModel(model string) bool {
	name := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(model), "openai/"))
	if len(name) >= 2 && name[0] == 'o' && name[1] >= '1' && name[1] <= '9' {
		return true
	}
	return openAIModelMajor(model) >= 5
}

// openAIModelMajor returns the major version of a gpt- model, or 0.
func openAIModelMajor(model string) int {
	name := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(model), "openai/"))
	version, ok := strings.CutPrefix(name, "gpt-")
	if !ok {
		return 0
	}
	major := 0
	for _, r := range version {
		if r < '0' || r > '9' {
			break
		}
		major = major*10 + int(r-'0')
	}
	return major
}
