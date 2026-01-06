package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const googleBaseURL = "https://generativelanguage.googleapis.com/v1beta"

// GoogleProvider talks to the Gemini API.
type GoogleProvider struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

var googleModels = []ModelInfo{
	{
		ID:            "google/gemini-2.0-flash",
		Name:          "Gemini 2.0 Flash",
		ContextLength: 1000000,
		Pricing: ModelPricing{
			Prompt:     0.10,
			Completion: 0.40,
		},
		Architecture: Architecture{
			Modality: "multimodal",
		},
	},
	{
		ID:            "google/gemini-2.0-pro",
		Name:          "Gemini 2.0 Pro",
		ContextLength: 2000000,
		Pricing: ModelPricing{
			Prompt:     3.50,
			Completion: 10.50,
		},
		Architecture: Architecture{
			Modality: "multimodal",
		},
	},
	{
		ID:            "google/gemini-1.5-flash",
		Name:          "Gemini 1.5 Flash",
		ContextLength: 1000000,
		Pricing: ModelPricing{
			Prompt:     0.075,
			Completion: 0.30,
		},
		Architecture: Architecture{
			Modality: "multimodal",
		},
	},
}

var googleModelIndex map[string]ModelInfo

func init() {
	googleModelIndex = make(map[string]ModelInfo, len(googleModels))
	for _, m := range googleModels {
		googleModelIndex[m.ID] = m
	}
}

// NewGoogleProvider builds a provider for Gemini.
func NewGoogleProvider(apiKey, baseURL string, networkLogsEnabled bool) *GoogleProvider {
	if baseURL == "" {
		baseURL = googleBaseURL
	}
	transport := NewLoggingTransportWithEnabled(nil, networkLogsEnabled)
	return &GoogleProvider{
		apiKey:  apiKey,
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout:   defaultTimeout,
			Transport: transport,
		},
	}
}

// ID returns provider identifier.
func (p *GoogleProvider) ID() string {
	return "google"
}

// FetchCatalog provides curated metadata.
func (p *GoogleProvider) FetchCatalog() (*ModelCatalog, error) {
	return &ModelCatalog{Data: googleModels}, nil
}

// GetModelInfo looks up metadata for a model.
func (p *GoogleProvider) GetModelInfo(modelID string) (*ModelInfo, error) {
	if info, ok := googleModelIndex[modelID]; ok {
		return &info, nil
	}
	return nil, fmt.Errorf("google model not found: %s", modelID)
}

// ChatCompletion runs a completion request.
func (p *GoogleProvider) ChatCompletion(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	if len(req.Tools) > 0 {
		return nil, fmt.Errorf("google provider does not support tool calling yet")
	}

	payload, err := p.toGenerateContentRequest(req)
	if err != nil {
		return nil, err
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	endpoint := fmt.Sprintf("%s/models/%s:generateContent?key=%s", p.baseURL, url.PathEscape(payload.Model), url.QueryEscape(p.apiKey))
	httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("google request failed: %s", resp.Status)
	}

	var genResp googleResponse
	if err := json.NewDecoder(resp.Body).Decode(&genResp); err != nil {
		return nil, err
	}

	return genResp.toChatResponse(payload.Model)
}

// ChatCompletionStream proxies to non-streaming variant (Gemini streaming handled later).
func (p *GoogleProvider) ChatCompletionStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, <-chan error) {
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
			errChan <- fmt.Errorf("google: empty response choices")
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

// SetTimeout updates the Google client timeout (0 disables timeout).
func (p *GoogleProvider) SetTimeout(timeout time.Duration) {
	if p.httpClient != nil {
		p.httpClient.Timeout = timeout
	}
}

func (p *GoogleProvider) toGenerateContentRequest(req ChatRequest) (*googleRequest, error) {
	payload := &googleRequest{
		Model: normalizeModelForProvider(req.Model, "google"),
	}

	for _, msg := range req.Messages {
		text := messageContentToText(msg.Content)
		switch msg.Role {
		case "system":
			payload.SystemInstruction = append(payload.SystemInstruction, googlePart{Text: text})
		case "user", "assistant":
			payload.Contents = append(payload.Contents, googleContent{
				Role: msg.Role,
				Parts: []googlePart{
					{Text: text},
				},
			})
		case "tool":
			return nil, fmt.Errorf("google provider does not support tool conversations")
		}
	}

	return payload, nil
}

type googleRequest struct {
	Model             string          `json:"-"`
	Contents          []googleContent `json:"contents"`
	SystemInstruction []googlePart    `json:"system_instruction,omitempty"`
}

type googleContent struct {
	Role  string       `json:"role"`
	Parts []googlePart `json:"parts"`
}

type googlePart struct {
	Text string `json:"text,omitempty"`
}

type googleResponse struct {
	Candidates []struct {
		Content googleContent `json:"content"`
	} `json:"candidates"`
	Usage struct {
		PromptTokens    int `json:"promptTokenCount"`
		CandidateTokens int `json:"candidatesTokenCount"`
		TotalTokens     int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
}

func (g googleResponse) toChatResponse(model string) (*ChatResponse, error) {
	if len(g.Candidates) == 0 {
		return nil, fmt.Errorf("no candidates returned from google provider")
	}
	var textParts []string
	for _, part := range g.Candidates[0].Content.Parts {
		textParts = append(textParts, part.Text)
	}
	content := strings.Join(textParts, "\n")
	msg := Message{
		Role:    "assistant",
		Content: content,
	}

	return &ChatResponse{
		ID:    "",
		Model: "google/" + model,
		Choices: []Choice{
			{
				Index:        0,
				Message:      msg,
				FinishReason: "stop",
			},
		},
		Usage: Usage{
			PromptTokens:     g.Usage.PromptTokens,
			CompletionTokens: g.Usage.CandidateTokens,
			TotalTokens:      g.Usage.TotalTokens,
		},
	}, nil
}
