package model

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

const (
	defaultBaseURL = "https://openrouter.ai/api/v1"
	defaultTimeout = 5 * time.Minute
	maxRetries     = 3
	baseRetryDelay = 1 * time.Second
	maxRetryDelay  = 30 * time.Second

	// Rate limiting: OpenRouter allows ~200 requests/minute for most tiers
	// We use a conservative 60 requests/minute (1/second) to stay well under limits
	defaultRateLimit = rate.Limit(1) // 1 request per second
	defaultBurstSize = 10            // Allow small bursts
)

// Client is an OpenRouter API client
type Client struct {
	apiKey      string
	baseURL     string
	httpClient  *http.Client
	transport   *LoggingTransport
	catalog     *ModelCatalog
	catalogAge  time.Time
	rateLimiter *rate.Limiter
}

type ClientOptions struct {
	NetworkLogsEnabled bool
}

// NewClient creates a new OpenRouter client
func NewClient(apiKey string, baseURL string) *Client {
	return NewClientWithOptions(apiKey, baseURL, ClientOptions{NetworkLogsEnabled: false})
}

func NewClientWithOptions(apiKey string, baseURL string, opts ClientOptions) *Client {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	transport := NewLoggingTransportWithEnabled(nil, opts.NetworkLogsEnabled)
	return &Client{
		apiKey:      apiKey,
		baseURL:     baseURL,
		transport:   transport,
		rateLimiter: rate.NewLimiter(defaultRateLimit, defaultBurstSize),
		httpClient: &http.Client{
			Timeout:   defaultTimeout,
			Transport: transport,
		},
	}
}

// Close closes the client and its resources
func (c *Client) Close() error {
	if c.transport != nil {
		return c.transport.Close()
	}
	return nil
}

// SetTimeout updates the underlying HTTP client timeout (0 disables timeout).
func (c *Client) SetTimeout(timeout time.Duration) {
	if c.httpClient != nil {
		c.httpClient.Timeout = timeout
	}
}

// doWithRateLimit executes an HTTP request with proactive rate limiting
func (c *Client) doWithRateLimit(ctx context.Context, req *http.Request) (*http.Response, error) {
	// Wait for rate limiter (this is the proactive part - we wait BEFORE hitting the API)
	if c.rateLimiter != nil {
		if err := c.rateLimiter.Wait(ctx); err != nil {
			return nil, fmt.Errorf("rate limit wait: %w", err)
		}
	}
	return c.httpClient.Do(req)
}

// FetchCatalog fetches the model catalog from OpenRouter
func (c *Client) FetchCatalog() (*ModelCatalog, error) {
	// Return cached catalog if less than 24h old
	if c.catalog != nil && time.Since(c.catalogAge) < 24*time.Hour {
		return c.catalog, nil
	}

	ctx := context.Background()
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	c.setHeaders(req)

	resp, err := c.doWithRateLimit(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("fetching catalog: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.parseError(resp)
	}

	var catalog ModelCatalog
	if err := json.NewDecoder(resp.Body).Decode(&catalog); err != nil {
		return nil, fmt.Errorf("decoding catalog: %w", err)
	}

	c.catalog = &catalog
	c.catalogAge = time.Now()

	return &catalog, nil
}

// GetModelInfo returns information about a specific model
func (c *Client) GetModelInfo(modelID string) (*ModelInfo, error) {
	if c.catalog == nil {
		if _, err := c.FetchCatalog(); err != nil {
			return nil, err
		}
	}

	for _, model := range c.catalog.Data {
		if model.ID == modelID {
			return &model, nil
		}
	}

	return nil, fmt.Errorf("model not found: %s", modelID)
}

// ChatCompletion performs a non-streaming chat completion with automatic retries
func (c *Client) ChatCompletion(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	req.Stream = false

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// Log retry attempt
			delay := c.calculateRetryDelay(attempt, lastErr)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		body, err := json.Marshal(req)
		if err != nil {
			return nil, fmt.Errorf("marshaling request: %w", err)
		}

		httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("creating request: %w", err)
		}

		c.setHeaders(httpReq)
		httpReq.Header.Set("Content-Type", "application/json")

		resp, err := c.doWithRateLimit(ctx, httpReq)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			apiErr := c.parseError(resp)
			lastErr = apiErr

			// Only retry if error is retryable
			if ae, ok := apiErr.(*APIError); ok && ae.Retryable {
				continue
			}
			return nil, apiErr
		}

		var chatResp ChatResponse
		if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
			return nil, fmt.Errorf("decoding response: %w", err)
		}

		return &chatResp, nil
	}

	return nil, fmt.Errorf("max retries exceeded: %w", lastErr)
}

// calculateRetryDelay calculates the delay before the next retry using exponential backoff
func (c *Client) calculateRetryDelay(attempt int, lastErr error) time.Duration {
	// Check if error has Retry-After header
	if apiErr, ok := lastErr.(*APIError); ok && apiErr.RetryAfter > 0 {
		if apiErr.RetryAfter > maxRetryDelay {
			return maxRetryDelay
		}
		return apiErr.RetryAfter
	}

	if attempt <= 0 {
		return baseRetryDelay
	}

	// Exponential backoff: 1s, 2s, 4s, 8s, ... up to maxRetryDelay
	multiplier := 1
	for i := 0; i < attempt-1; i++ {
		multiplier *= 2
	}
	delay := baseRetryDelay * time.Duration(multiplier)
	if delay > maxRetryDelay {
		delay = maxRetryDelay
	}

	return delay
}

// ChatCompletionStream performs a streaming chat completion with automatic retries
func (c *Client) ChatCompletionStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, <-chan error) {
	chunkChan := make(chan StreamChunk, 10)
	errChan := make(chan error, 1)

	go func() {
		defer close(errChan)
		defer close(chunkChan)

		req.Stream = true

		body, err := json.Marshal(req)
		if err != nil {
			errChan <- fmt.Errorf("marshaling request: %w", err)
			return
		}

		// Retry loop for establishing connection
		var lastErr error
		for attempt := 0; attempt <= maxRetries; attempt++ {
			if attempt > 0 {
				delay := c.calculateRetryDelay(attempt, lastErr)
				select {
				case <-ctx.Done():
					errChan <- ctx.Err()
					return
				case <-time.After(delay):
				}
			}

			httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewReader(body))
			if err != nil {
				errChan <- fmt.Errorf("creating request: %w", err)
				return
			}

			c.setHeaders(httpReq)
			httpReq.Header.Set("Content-Type", "application/json")
			httpReq.Header.Set("Accept", "text/event-stream")

			resp, err := c.doWithRateLimit(ctx, httpReq)
			if err != nil {
				lastErr = err
				continue
			}

			if resp.StatusCode != http.StatusOK {
				apiErr := c.parseError(resp)
				resp.Body.Close()
				lastErr = apiErr

				// Only retry if error is retryable
				if ae, ok := apiErr.(*APIError); ok && ae.Retryable {
					continue
				}
				errChan <- apiErr
				return
			}

			// Connection successful, parse SSE stream
			// Note: Once streaming starts, we don't retry mid-stream
			defer resp.Body.Close()
			if err := c.parseSSEStream(ctx, resp.Body, chunkChan); err != nil {
				errChan <- err
				return
			}

			// Stream completed successfully
			errChan <- nil
			return
		}

		// Max retries exceeded
		errChan <- fmt.Errorf("max retries exceeded: %w", lastErr)
	}()

	return chunkChan, errChan
}

// parseSSEStream parses Server-Sent Events stream
func (c *Client) parseSSEStream(ctx context.Context, r io.Reader, chunkChan chan<- StreamChunk) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 1MB max line

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := scanner.Text()

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}

		// Parse SSE field
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")

		// Check for stream end
		if data == "[DONE]" {
			return nil
		}

		// Parse JSON chunk
		var chunk StreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return fmt.Errorf("decoding chunk: %w", err)
		}

		chunkChan <- chunk
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading stream: %w", err)
	}

	return nil
}

// setHeaders sets common request headers
func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("HTTP-Referer", "https://github.com/odvcencio/buckley")
	req.Header.Set("X-Title", "Buckley")
}

// parseError parses an error response and wraps it with additional context
func (c *Client) parseError(resp *http.Response) error {
	// Read the body for debugging
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return &APIError{
			StatusCode: resp.StatusCode,
			Message:    resp.Status,
			Retryable:  resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500,
		}
	}

	var errResp ErrorResponse
	if err := json.Unmarshal(body, &errResp); err != nil {
		// Include raw body in error message for debugging
		rawBody := string(body)
		if len(rawBody) > 500 {
			rawBody = rawBody[:500] + "..."
		}
		message := resp.Status
		if rawBody != "" {
			message = fmt.Sprintf("%s (raw: %s)", resp.Status, rawBody)
		}
		return &APIError{
			StatusCode: resp.StatusCode,
			Message:    message,
			Retryable:  resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500,
		}
	}

	// Use parsed error message if available, fallback to status
	message := errResp.Error.Message
	if message == "" {
		message = resp.Status
	}

	return &APIError{
		StatusCode: resp.StatusCode,
		Message:    message,
		Type:       errResp.Error.Type,
		Code:       errResp.Error.Code,
		Retryable:  resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500,
		RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
	}
}

// parseRetryAfter parses the Retry-After header
func parseRetryAfter(header string) time.Duration {
	if header == "" {
		return 0
	}

	// Try parsing as seconds (integer)
	if seconds, err := strconv.Atoi(header); err == nil {
		return time.Duration(seconds) * time.Second
	}

	// Try parsing as HTTP-date (not commonly used for 429)
	if t, err := time.Parse(time.RFC1123, header); err == nil {
		return time.Until(t)
	}

	return 0
}

// ValidateAPIKey validates the API key by making a test request
func (c *Client) ValidateAPIKey() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := c.FetchCatalog()
	if err != nil {
		return fmt.Errorf("validating API key: %w", err)
	}

	// Try a minimal completion to verify full access
	req := ChatRequest{
		Model: "openai/gpt-3.5-turbo",
		Messages: []Message{
			{Role: "user", Content: "Hi"},
		},
		MaxTokens: 5,
	}

	_, err = c.ChatCompletion(ctx, req)
	if err != nil {
		return fmt.Errorf("validating API key with test completion: %w", err)
	}

	return nil
}
