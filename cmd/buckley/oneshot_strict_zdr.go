package main

import (
	"context"
	"fmt"
	"strings"

	"m31labs.dev/buckley/pkg/model"
)

// strictZDROneshotClient binds OpenRouter one-shot requests to one exact model
// and the provider's strict ZDR route. It does not inspect a workspace or mint
// non-ZDR authority; OSS admission remains a separate trusted-host concern.
type strictZDROneshotClient struct {
	client  model.CompletionClient
	modelID string
}

type strictZDROneshotStreamingClient struct {
	*strictZDROneshotClient
	stream model.StreamingClient
}

func strictZDROneshotClientForProvider(client model.CompletionClient, modelID, providerID string) (model.CompletionClient, error) {
	if strings.TrimSpace(providerID) != "openrouter" {
		return client, nil
	}
	if client == nil {
		return nil, fmt.Errorf("oneshot strict ZDR policy unavailable: no completion client")
	}
	modelID = strings.TrimSpace(modelID)
	if modelID == "" || !strings.Contains(modelID, "/") {
		return nil, fmt.Errorf("oneshot strict ZDR policy unavailable: canonical model ID required")
	}

	governed := &strictZDROneshotClient{client: client, modelID: modelID}
	if stream, ok := client.(model.StreamingClient); ok {
		return &strictZDROneshotStreamingClient{strictZDROneshotClient: governed, stream: stream}, nil
	}
	return governed, nil
}

func (c *strictZDROneshotClient) ChatCompletion(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	governed, err := c.govern(req)
	if err != nil {
		return nil, err
	}
	return c.client.ChatCompletion(ctx, governed)
}

func (c *strictZDROneshotStreamingClient) ChatCompletionStream(ctx context.Context, req model.ChatRequest) (<-chan model.StreamChunk, <-chan error) {
	governed, err := c.govern(req)
	if err != nil {
		return closedOneshotStream(err)
	}
	return c.stream.ChatCompletionStream(ctx, governed)
}

func (c *strictZDROneshotClient) GetContextLength(modelID string) (int, error) {
	if c == nil || strings.TrimSpace(modelID) != c.modelID {
		return 0, fmt.Errorf("oneshot strict ZDR policy blocked: exact model required")
	}
	provider, ok := c.client.(model.ContextWindowProvider)
	if !ok {
		return 0, fmt.Errorf("oneshot model client does not expose context windows")
	}
	return provider.GetContextLength(c.modelID)
}

func (c *strictZDROneshotClient) govern(req model.ChatRequest) (model.ChatRequest, error) {
	if c == nil || c.client == nil || c.modelID == "" {
		return model.ChatRequest{}, fmt.Errorf("oneshot strict ZDR policy unavailable")
	}
	if strings.TrimSpace(req.Model) != c.modelID {
		return model.ChatRequest{}, fmt.Errorf("oneshot strict ZDR policy blocked: exact model required")
	}

	provider := make(map[string]any, len(req.Provider)+2)
	for key, value := range req.Provider {
		provider[key] = value
	}
	provider["allow_fallbacks"] = false
	provider["zdr"] = true
	delete(provider, "data_collection")

	req.Model = c.modelID
	req.Models = nil
	req.Provider = provider
	req.OpenRouterRetention = model.OpenRouterRetentionZDR
	return req, nil
}

func closedOneshotStream(err error) (<-chan model.StreamChunk, <-chan error) {
	chunks := make(chan model.StreamChunk)
	close(chunks)
	errs := make(chan error, 1)
	errs <- err
	close(errs)
	return chunks, errs
}
