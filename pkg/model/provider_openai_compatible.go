package model

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/config"
)

// OpenAICompatibleProvider connects to an OpenAI-compatible chat API.
type OpenAICompatibleProvider struct {
	providerID    string
	modelPrefix   string
	liteLLMInfo   bool
	baseURL       string
	apiKey        string
	httpClient    *http.Client
	transport     *ProviderTransport
	modelCache    []ModelInfo
	cacheTTL      time.Duration
	cacheTime     time.Time
	staticModels  []string
	staticParams  map[string][]string
	staticContext map[string]int
}

// LiteLLMProvider is the deprecated name for OpenAICompatibleProvider.
type LiteLLMProvider = OpenAICompatibleProvider

// NewOpenAICompatibleProvider builds the canonical generic provider.
func NewOpenAICompatibleProvider(cfg config.OpenAICompatibleConfig, networkLogsEnabled bool) *OpenAICompatibleProvider {
	return newOpenAICompatibleProvider("openai_compatible", false, cfg, networkLogsEnabled)
}

// NewLiteLLMProvider builds the deprecated LiteLLM-compatible provider.
func NewLiteLLMProvider(cfg config.LiteLLMConfig, networkLogsEnabled bool) *OpenAICompatibleProvider {
	return newOpenAICompatibleProvider("litellm", true, cfg, networkLogsEnabled)
}

func newOpenAICompatibleProvider(providerID string, liteLLMInfo bool, cfg config.OpenAICompatibleConfig, networkLogsEnabled bool) *OpenAICompatibleProvider {
	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" && liteLLMInfo {
		baseURL = "http://localhost:4000"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	transport := NewLoggingTransportWithEnabled(nil, networkLogsEnabled)
	return &OpenAICompatibleProvider{
		providerID:    providerID,
		modelPrefix:   providerID + "/",
		liteLLMInfo:   liteLLMInfo,
		baseURL:       baseURL,
		apiKey:        strings.TrimSpace(cfg.APIKey),
		httpClient:    &http.Client{Timeout: defaultTimeout, Transport: transport},
		transport:     NewProviderTransport(ProviderTransportOptions{}),
		cacheTTL:      5 * time.Minute,
		staticModels:  cfg.Models,
		staticParams:  cfg.SupportedParameters,
		staticContext: cfg.ContextLengths,
	}
}

// ID returns provider identifier.
func (p *OpenAICompatibleProvider) ID() string {
	return p.providerID
}

// FetchCatalog returns model metadata from the compatible API.
func (p *OpenAICompatibleProvider) FetchCatalog() (*ModelCatalog, error) {
	if time.Since(p.cacheTime) < p.cacheTTL && len(p.modelCache) > 0 {
		return &ModelCatalog{Data: p.modelCache}, nil
	}

	var (
		models []ModelInfo
		err    error
	)
	if p.liteLLMInfo {
		models, err = p.fetchModelInfo()
		if err != nil {
			models, err = p.fetchModels()
		}
	} else {
		models, err = p.fetchModels()
	}
	if err != nil {
		if len(p.staticModels) == 0 {
			return nil, fmt.Errorf("%s list models: %w", p.providerID, err)
		}
		models = p.buildStaticModels()
	}

	if len(models) == 0 && len(p.staticModels) > 0 {
		models = p.buildStaticModels()
	}

	p.modelCache = models
	p.cacheTime = time.Now()
	return &ModelCatalog{Data: models}, nil
}

// GetModelInfo returns cached model metadata when available.
func (p *OpenAICompatibleProvider) GetModelInfo(modelID string) (*ModelInfo, error) {
	catalog, err := p.FetchCatalog()
	if err != nil {
		return nil, err
	}
	for _, info := range catalog.Data {
		if info.ID == modelID {
			return &info, nil
		}
	}
	return nil, fmt.Errorf("%s model not found: %s", p.providerID, modelID)
}

// ChatCompletion executes a non-streaming request.
func (p *OpenAICompatibleProvider) ChatCompletion(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	req.Model = strings.TrimPrefix(req.Model, p.modelPrefix)
	req.Stream = false
	return p.invoke(ctx, req)
}

// ChatCompletionStream streams responses from the compatible API.
func (p *OpenAICompatibleProvider) ChatCompletionStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, <-chan error) {
	req.Model = strings.TrimPrefix(req.Model, p.modelPrefix)
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

// SetTimeout updates the client timeout (0 disables timeout).
func (p *OpenAICompatibleProvider) SetTimeout(timeout time.Duration) {
	if p.httpClient != nil {
		p.httpClient.Timeout = timeout
	}
}

func (p *OpenAICompatibleProvider) fetchModelInfo() ([]ModelInfo, error) {
	req, err := http.NewRequest("GET", p.baseURL+"/model/info", nil)
	if err != nil {
		return nil, err
	}
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("model info returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Data []struct {
			ModelName string `json:"model_name"`
			ModelInfo struct {
				ID                      string  `json:"id"`
				MaxTokens               int     `json:"max_tokens"`
				MaxInputTokens          int     `json:"max_input_tokens"`
				InputCostPerToken       float64 `json:"input_cost_per_token"`
				OutputCostPerToken      float64 `json:"output_cost_per_token"`
				Mode                    string  `json:"mode"`
				SupportsFunctionCalling bool    `json:"supports_function_calling"`
				SupportsVision          bool    `json:"supports_vision"`
			} `json:"model_info"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	models := make([]ModelInfo, 0, len(result.Data))
	for _, m := range result.Data {
		if m.ModelInfo.Mode != "" && m.ModelInfo.Mode != "chat" {
			continue
		}

		contextLength := m.ModelInfo.MaxInputTokens
		if contextLength == 0 {
			contextLength = m.ModelInfo.MaxTokens
		}
		if contextLength == 0 {
			contextLength = 8192
		}
		contextLength = p.configuredContextLength(p.modelPrefix+m.ModelName, contextLength)

		info := ModelInfo{
			ID:            p.modelPrefix + m.ModelName,
			Name:          m.ModelName,
			ContextLength: contextLength,
			Pricing: ModelPricing{
				Prompt:     m.ModelInfo.InputCostPerToken * 1_000_000,
				Completion: m.ModelInfo.OutputCostPerToken * 1_000_000,
			},
		}
		if m.ModelInfo.SupportsFunctionCalling {
			info.SupportedParameters = []string{"tools", "functions"}
		}
		info.SupportedParameters = p.mergeConfiguredParameters(info.ID, info.SupportedParameters)
		if m.ModelInfo.SupportsVision {
			info.Architecture = Architecture{Modality: "text+image"}
		} else {
			info.Architecture = Architecture{Modality: "text"}
		}

		models = append(models, info)
	}

	return models, nil
}

func (p *OpenAICompatibleProvider) fetchModels() ([]ModelInfo, error) {
	req, err := http.NewRequest("GET", p.baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("models returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	models := make([]ModelInfo, 0, len(result.Data))
	for _, m := range result.Data {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		info := ModelInfo{
			ID:            p.modelPrefix + id,
			Name:          id,
			ContextLength: p.configuredContextLength(p.modelPrefix+id, 8192),
			Architecture:  Architecture{Modality: "text"},
		}
		info.SupportedParameters = p.mergeConfiguredParameters(info.ID, nil)
		models = append(models, info)
	}
	return models, nil
}

func (p *OpenAICompatibleProvider) buildStaticModels() []ModelInfo {
	models := make([]ModelInfo, 0, len(p.staticModels))
	for _, raw := range p.staticModels {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		id := raw
		if !strings.HasPrefix(id, p.modelPrefix) {
			id = p.modelPrefix + id
		}
		name := strings.TrimPrefix(id, p.modelPrefix)
		info := ModelInfo{
			ID:            id,
			Name:          name,
			ContextLength: p.configuredContextLength(id, 8192),
			Architecture:  Architecture{Modality: "text"},
		}
		info.SupportedParameters = p.mergeConfiguredParameters(info.ID, nil)
		models = append(models, info)
	}
	return models
}

func (p *OpenAICompatibleProvider) configuredContextLength(modelID string, fallback int) int {
	configured := p.staticContext[modelID]
	if configured <= 0 {
		configured = p.staticContext[strings.TrimPrefix(modelID, p.modelPrefix)]
	}
	if configured > 0 {
		return configured
	}
	return fallback
}

func (p *OpenAICompatibleProvider) mergeConfiguredParameters(modelID string, discovered []string) []string {
	parameters := append([]string(nil), discovered...)
	configured := p.staticParams[modelID]
	if len(configured) == 0 {
		configured = p.staticParams[strings.TrimPrefix(modelID, p.modelPrefix)]
	}
	for _, parameter := range configured {
		parameter = strings.TrimSpace(parameter)
		if parameter != "" && !containsString(parameters, parameter) {
			parameters = append(parameters, parameter)
		}
	}
	return parameters
}

func (p *OpenAICompatibleProvider) setAuthHeaders(req *http.Request) {
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
}

func (p *OpenAICompatibleProvider) invoke(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	data, err := p.transport.Do(ctx, p.httpClient, "POST", p.baseURL+"/chat/completions", req, p.setAuthHeaders)
	if err != nil {
		return nil, err
	}

	var chatResp ChatResponse
	if err := json.Unmarshal(data, &chatResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &chatResp, nil
}

func (p *OpenAICompatibleProvider) invokeStream(ctx context.Context, req ChatRequest, chunkChan chan<- StreamChunk) error {
	return p.transport.Stream(ctx, p.httpClient, "POST", p.baseURL+"/chat/completions", req, p.setAuthHeaders, chunkChan)
}
