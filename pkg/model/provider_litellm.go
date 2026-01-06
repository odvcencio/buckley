package model

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
)

// LiteLLMProvider connects to a LiteLLM proxy with OpenAI-compatible APIs.
type LiteLLMProvider struct {
	baseURL      string
	apiKey       string
	httpClient   *http.Client
	modelCache   []ModelInfo
	cacheTTL     time.Duration
	cacheTime    time.Time
	staticModels []string
}

// NewLiteLLMProvider builds a LiteLLM provider from config.
func NewLiteLLMProvider(cfg config.LiteLLMConfig, networkLogsEnabled bool) *LiteLLMProvider {
	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		baseURL = "http://localhost:4000"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	transport := NewLoggingTransportWithEnabled(nil, networkLogsEnabled)
	return &LiteLLMProvider{
		baseURL:      baseURL,
		apiKey:       strings.TrimSpace(cfg.APIKey),
		httpClient:   &http.Client{Timeout: defaultTimeout, Transport: transport},
		cacheTTL:     5 * time.Minute,
		staticModels: cfg.Models,
	}
}

// ID returns provider identifier.
func (p *LiteLLMProvider) ID() string {
	return "litellm"
}

// FetchCatalog returns model metadata from the LiteLLM proxy.
func (p *LiteLLMProvider) FetchCatalog() (*ModelCatalog, error) {
	if time.Since(p.cacheTime) < p.cacheTTL && len(p.modelCache) > 0 {
		return &ModelCatalog{Data: p.modelCache}, nil
	}

	models, err := p.fetchModelInfo()
	if err != nil {
		models, err = p.fetchModels()
		if err != nil {
			if len(p.staticModels) == 0 {
				return nil, fmt.Errorf("litellm list models: %w", err)
			}
			models = p.buildStaticModels()
		}
	}

	if len(models) == 0 && len(p.staticModels) > 0 {
		models = p.buildStaticModels()
	}

	p.modelCache = models
	p.cacheTime = time.Now()
	return &ModelCatalog{Data: models}, nil
}

// GetModelInfo returns cached model metadata when available.
func (p *LiteLLMProvider) GetModelInfo(modelID string) (*ModelInfo, error) {
	catalog, err := p.FetchCatalog()
	if err != nil {
		return nil, err
	}
	for _, info := range catalog.Data {
		if info.ID == modelID {
			return &info, nil
		}
	}
	return nil, fmt.Errorf("litellm model not found: %s", modelID)
}

// ChatCompletion executes a non-streaming request.
func (p *LiteLLMProvider) ChatCompletion(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	req.Model = strings.TrimPrefix(req.Model, "litellm/")
	req.Stream = false
	return p.invoke(ctx, req)
}

// ChatCompletionStream streams responses from LiteLLM.
func (p *LiteLLMProvider) ChatCompletionStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, <-chan error) {
	req.Model = strings.TrimPrefix(req.Model, "litellm/")
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

// SetTimeout updates the LiteLLM client timeout (0 disables timeout).
func (p *LiteLLMProvider) SetTimeout(timeout time.Duration) {
	if p.httpClient != nil {
		p.httpClient.Timeout = timeout
	}
}

func (p *LiteLLMProvider) fetchModelInfo() ([]ModelInfo, error) {
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

		info := ModelInfo{
			ID:            "litellm/" + m.ModelName,
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
		if m.ModelInfo.SupportsVision {
			info.Architecture = Architecture{Modality: "text+image"}
		} else {
			info.Architecture = Architecture{Modality: "text"}
		}

		models = append(models, info)
	}

	return models, nil
}

func (p *LiteLLMProvider) fetchModels() ([]ModelInfo, error) {
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
		models = append(models, ModelInfo{
			ID:            "litellm/" + id,
			Name:          id,
			ContextLength: 8192,
			Architecture:  Architecture{Modality: "text"},
		})
	}
	return models, nil
}

func (p *LiteLLMProvider) buildStaticModels() []ModelInfo {
	models := make([]ModelInfo, 0, len(p.staticModels))
	for _, raw := range p.staticModels {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		id := raw
		if !strings.HasPrefix(id, "litellm/") {
			id = "litellm/" + id
		}
		name := strings.TrimPrefix(id, "litellm/")
		models = append(models, ModelInfo{
			ID:            id,
			Name:          name,
			ContextLength: 8192,
			Architecture:  Architecture{Modality: "text"},
		})
	}
	return models
}

func (p *LiteLLMProvider) invoke(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("litellm chat failed (%d): %s", resp.StatusCode, string(body))
	}

	var chatResp ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &chatResp, nil
}

func (p *LiteLLMProvider) invokeStream(ctx context.Context, req ChatRequest, chunkChan chan<- StreamChunk) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("litellm streaming failed (%d): %s", resp.StatusCode, string(body))
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		data := strings.TrimSpace(scanner.Text())
		if data == "" || data == "data: [DONE]" {
			continue
		}
		if strings.HasPrefix(data, "data: ") {
			data = strings.TrimPrefix(data, "data: ")
		}

		var chunk StreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		chunkChan <- chunk
	}

	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}
