package image

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Result holds the output of an image generation request.
type Result struct {
	Text      string // Text portion of the response
	ImageData []byte // Decoded PNG image bytes
}

// Client makes image generation requests to OpenRouter.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewClient creates an image generation client.
func NewClient(baseURL, apiKey string) *Client {
	if baseURL == "" {
		baseURL = "https://openrouter.ai/api/v1"
	}
	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 3 * time.Minute,
		},
	}
}

// SetTimeout overrides the default HTTP client timeout.
func (c *Client) SetTimeout(d time.Duration) {
	c.httpClient.Timeout = d
}

// Generate sends an image generation request and returns the result.
func (c *Client) Generate(ctx context.Context, prompt, size string, inputImage []byte, modelID string) (*Result, error) {
	req := buildRequest(prompt, size, inputImage, modelID)

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("HTTP-Referer", "https://github.com/odvcencio/buckley")
	httpReq.Header.Set("X-Title", "Buckley")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(errBody))
	}

	var imgResp imageResponse
	if err := json.NewDecoder(resp.Body).Decode(&imgResp); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	text, imageData, err := imgResp.extractContent()
	if err != nil {
		return nil, err
	}

	return &Result{Text: text, ImageData: imageData}, nil
}
