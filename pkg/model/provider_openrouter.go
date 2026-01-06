package model

import (
	"context"
	"time"
)

// OpenRouterProvider satisfies Provider using the OpenRouter API.
type OpenRouterProvider struct {
	client *Client
}

// ID returns provider identifier.
func (p *OpenRouterProvider) ID() string {
	return "openrouter"
}

// FetchCatalog fetches catalog via OpenRouter.
func (p *OpenRouterProvider) FetchCatalog() (*ModelCatalog, error) {
	return p.client.FetchCatalog()
}

// GetModelInfo fetches info for the supplied model.
func (p *OpenRouterProvider) GetModelInfo(modelID string) (*ModelInfo, error) {
	return p.client.GetModelInfo(modelID)
}

// ChatCompletion executes a standard completion.
func (p *OpenRouterProvider) ChatCompletion(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	return p.client.ChatCompletion(ctx, req)
}

// ChatCompletionStream executes a streaming completion.
func (p *OpenRouterProvider) ChatCompletionStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, <-chan error) {
	return p.client.ChatCompletionStream(ctx, req)
}

// SetTimeout updates the OpenRouter client timeout (0 disables timeout).
func (p *OpenRouterProvider) SetTimeout(timeout time.Duration) {
	if p.client != nil {
		p.client.SetTimeout(timeout)
	}
}
