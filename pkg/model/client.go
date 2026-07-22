package model

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	defaultBaseURL = "https://openrouter.ai/api/v1"
	defaultTimeout = 5 * time.Minute

	// Rate limiting: OpenRouter allows ~200 requests/minute for most tiers
	// We use a conservative 60 requests/minute (1/second) to stay well under limits
	defaultRateLimit = rate.Limit(1) // 1 request per second
	defaultBurstSize = 10            // Allow small bursts
)

// RetryConfig configures the retry mechanism for idempotent HTTP requests.
type RetryConfig struct {
	MaxRetries          int
	MaxRateLimitRetries int
	InitialInterval     time.Duration
	MaxInterval         time.Duration
	Multiplier          float64
}

// DefaultRetryConfig returns the default retry configuration.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries:          3,
		MaxRateLimitRetries: 12,
		InitialInterval:     1 * time.Second,
		MaxInterval:         30 * time.Second,
		Multiplier:          2.0,
	}
}

// DefaultTransport returns an optimized http.Transport with tuned connection pool settings.
func DefaultTransport() *http.Transport {
	return &http.Transport{
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		MaxConnsPerHost:       50,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// Additional recommended settings
		ForceAttemptHTTP2:  true,
		DisableKeepAlives:  false,
		DisableCompression: false,
	}
}

// Client is an OpenRouter API client
type Client struct {
	apiKey         string
	baseURL        string
	httpClient     *http.Client
	transport      *LoggingTransport
	catalog        *ModelCatalog
	catalogAge     time.Time
	catalogMu      sync.Mutex
	rateLimiter    *rate.Limiter
	circuitBreaker *CircuitBreaker
	retryConfig    RetryConfig
}

type ClientOptions struct {
	NetworkLogsEnabled bool
	// CircuitBreakerConfig is optional; if nil, default config is used
	CircuitBreakerConfig *CircuitBreakerConfig
	// RetryConfig is optional; if nil, default config is used
	RetryConfig *RetryConfig
}

// NewClient creates a new OpenRouter client
func NewClient(apiKey string, baseURL string) *Client {
	return NewClientWithOptions(apiKey, baseURL, ClientOptions{NetworkLogsEnabled: false})
}

func NewClientWithOptions(apiKey string, baseURL string, opts ClientOptions) *Client {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}

	// Use optimized transport as base
	baseTransport := DefaultTransport()
	transport := NewLoggingTransportWithEnabled(baseTransport, opts.NetworkLogsEnabled)

	// Configure circuit breaker
	var cbConfig CircuitBreakerConfig
	if opts.CircuitBreakerConfig != nil {
		cbConfig = *opts.CircuitBreakerConfig
	} else {
		cbConfig = DefaultCircuitBreakerConfig()
	}

	// Configure retry
	var retryConfig RetryConfig
	if opts.RetryConfig != nil {
		retryConfig = *opts.RetryConfig
	} else {
		retryConfig = DefaultRetryConfig()
	}

	return &Client{
		apiKey:         apiKey,
		baseURL:        baseURL,
		transport:      transport,
		rateLimiter:    rate.NewLimiter(defaultRateLimit, defaultBurstSize),
		circuitBreaker: NewCircuitBreaker(cbConfig),
		httpClient: &http.Client{
			Timeout:   defaultTimeout,
			Transport: transport,
		},
		retryConfig: retryConfig,
	}
}

// Close closes the client and its resources
func (c *Client) Close() error {
	if c.transport != nil {
		return c.transport.Close()
	}
	return nil
}

// CircuitBreakerState returns the current state of the circuit breaker
func (c *Client) CircuitBreakerState() string {
	if c.circuitBreaker != nil {
		return c.circuitBreaker.State()
	}
	return "disabled"
}

// ResetCircuitBreaker manually resets the circuit breaker to closed state
func (c *Client) ResetCircuitBreaker() {
	if c.circuitBreaker != nil {
		c.circuitBreaker.Reset()
	}
}

// SetTimeout updates the underlying HTTP client timeout (0 disables timeout).
func (c *Client) SetTimeout(timeout time.Duration) {
	if c.httpClient != nil {
		c.httpClient.Timeout = timeout
	}
}

// SetRetryConfig updates the retry configuration.
func (c *Client) SetRetryConfig(config RetryConfig) {
	c.retryConfig = config
}

// isRetryableError checks if an error is retryable based on status code.
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Retryable
	}
	// Network errors are generally retryable
	return true
}

func (c *Client) retryLimit(err error) int {
	limit := max(c.retryConfig.MaxRetries, 0)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.IsRateLimitError() && c.retryConfig.MaxRateLimitRetries > limit {
		limit = c.retryConfig.MaxRateLimitRetries
	}
	return limit
}

func (c *Client) canRetryModelRequest(attempt int, err error) bool {
	return isRetryableError(err) && attempt < c.retryLimit(err)
}

func retryExhaustedError(attempt int, err error) error {
	return fmt.Errorf("model request retries exhausted after %d attempts: %w", attempt+1, err)
}

// isIdempotentMethod checks if an HTTP method is idempotent and safe to retry.
func isIdempotentMethod(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPut, http.MethodDelete:
		return true
	case http.MethodPost, http.MethodPatch:
		return false
	default:
		// Conservative default: assume non-idempotent
		return false
	}
}

// calculateBackoff calculates the delay for the next retry attempt using exponential backoff with jitter.
func (c *Client) calculateBackoff(attempt int) time.Duration {
	if attempt <= 0 {
		return c.retryConfig.InitialInterval
	}

	// Calculate exponential delay: InitialInterval * Multiplier^attempt
	delay := float64(c.retryConfig.InitialInterval)
	for range attempt {
		delay *= c.retryConfig.Multiplier
	}

	// Cap at MaxInterval
	delay = min(delay, float64(c.retryConfig.MaxInterval))

	// Add jitter: random value between 0 and delay/2
	// This helps prevent thundering herd when multiple clients retry simultaneously
	jitter := time.Duration(rand.Float64() * delay * 0.5)
	delay = delay*0.75 + float64(jitter)

	return time.Duration(delay)
}

// DoWithRetry executes an HTTP request with retry logic for idempotent methods.
// For non-idempotent methods (POST, PATCH), it behaves like a regular Do.
func (c *Client) DoWithRetry(req *http.Request) (*http.Response, error) {
	// Non-idempotent methods should not be retried automatically
	if !isIdempotentMethod(req.Method) {
		if c.rateLimiter != nil {
			if err := c.rateLimiter.Wait(req.Context()); err != nil {
				return nil, fmt.Errorf("rate limit wait: %w", err)
			}
		}
		return c.httpClient.Do(req)
	}

	// Idempotent method: apply retry logic
	var lastErr error
	var resp *http.Response

	for attempt := 0; attempt <= c.retryConfig.MaxRetries; attempt++ {
		if attempt > 0 {
			delay := c.calculateBackoff(attempt - 1)
			select {
			case <-req.Context().Done():
				return nil, req.Context().Err()
			case <-time.After(delay):
			}
		}

		// Clone the request for retry to ensure body can be re-read
		reqClone := req.Clone(req.Context())
		if req.Body != nil && req.Body != http.NoBody {
			bodyBytes, err := io.ReadAll(req.Body)
			if err != nil {
				return nil, fmt.Errorf("reading request body: %w", err)
			}
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			reqClone.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		// Wait for rate limiter
		if c.rateLimiter != nil {
			if err := c.rateLimiter.Wait(req.Context()); err != nil {
				return nil, fmt.Errorf("rate limit wait: %w", err)
			}
		}

		resp, lastErr = c.httpClient.Do(reqClone)
		if lastErr == nil {
			// Check if we got a retryable HTTP status code
			if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
				apiErr := c.parseError(resp)
				resp.Body.Close()
				lastErr = apiErr
				if isRetryableError(apiErr) && attempt < c.retryConfig.MaxRetries {
					continue
				}
				return nil, apiErr
			}
			return resp, nil
		}

		// Network error - check if we should retry
		if attempt < c.retryConfig.MaxRetries && isRetryableError(lastErr) {
			continue
		}
	}

	if lastErr != nil {
		return nil, fmt.Errorf("max retries (%d) exceeded: %w", c.retryConfig.MaxRetries, lastErr)
	}
	return nil, fmt.Errorf("max retries (%d) exceeded", c.retryConfig.MaxRetries)
}

// FetchCatalog fetches the model catalog from OpenRouter
func (c *Client) FetchCatalog() (*ModelCatalog, error) {
	return c.fetchCatalog(false)
}

// RefreshCatalog bypasses the in-memory catalog cache.
func (c *Client) RefreshCatalog() (*ModelCatalog, error) {
	return c.fetchCatalog(true)
}

func (c *Client) fetchCatalog(force bool) (*ModelCatalog, error) {
	c.catalogMu.Lock()
	defer c.catalogMu.Unlock()

	// Return cached catalog if less than 24h old
	if !force && c.catalog != nil && time.Since(c.catalogAge) < 24*time.Hour {
		return c.catalog, nil
	}

	ctx := context.Background()
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	c.setHeaders(req)

	resp, err := c.DoWithRetry(req)
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

	var result *ChatResponse

	// Wrap the actual API call with circuit breaker
	err := c.circuitBreaker.Call(func() error {
		var lastErr error
		for attempt := 0; ; attempt++ {
			if attempt > 0 {
				// Log retry attempt
				delay := c.calculateRetryDelay(attempt, lastErr)
				select {
				case <-ctx.Done():
					return errors.Join(ctx.Err(), lastErr)
				case <-time.After(delay):
				}
			}

			body, err := json.Marshal(req)
			if err != nil {
				return fmt.Errorf("marshaling request: %w", err)
			}

			httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewReader(body))
			if err != nil {
				return fmt.Errorf("creating request: %w", err)
			}

			c.setHeaders(httpReq)
			httpReq.Header.Set("Content-Type", "application/json")

			// Wait for rate limiter
			if c.rateLimiter != nil {
				if err := c.rateLimiter.Wait(ctx); err != nil {
					return fmt.Errorf("rate limit wait: %w", err)
				}
			}

			resp, err := c.httpClient.Do(httpReq)
			if err != nil {
				lastErr = err
				if c.canRetryModelRequest(attempt, lastErr) {
					continue
				}
				return retryExhaustedError(attempt, lastErr)
			}
			if resp.StatusCode != http.StatusOK {
				apiErr := c.parseError(resp)
				resp.Body.Close()
				lastErr = apiErr

				if c.canRetryModelRequest(attempt, apiErr) {
					continue
				}
				if isRetryableError(apiErr) {
					return retryExhaustedError(attempt, apiErr)
				}
				return apiErr
			}
			defer resp.Body.Close()

			var chatResp ChatResponse
			if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			if chatResp.Error != nil {
				message := strings.TrimSpace(chatResp.Error.Message)
				if message == "" {
					message = "provider returned an error response"
				}
				return &APIError{
					StatusCode: resp.StatusCode,
					Message:    message,
					Type:       chatResp.Error.Type,
					Code:       chatResp.Error.Code,
					Retryable:  false,
				}
			}
			if len(chatResp.Choices) == 0 {
				return NoResponseChoicesError(req, &chatResp)
			}

			result = &chatResp
			return nil
		}

		return retryExhaustedError(c.retryLimit(lastErr), lastErr)
	})

	if err != nil {
		return nil, err
	}
	return result, nil
}

// calculateRetryDelay calculates the delay before the next retry using exponential backoff
func (c *Client) calculateRetryDelay(attempt int, lastErr error) time.Duration {
	// Check if error has Retry-After header
	var apiErr *APIError
	if errors.As(lastErr, &apiErr) && apiErr.RetryAfter > 0 {
		// Retry-After is the provider's earliest acceptable retry time, not a
		// backoff suggestion. The caller's context remains the upper bound.
		return apiErr.RetryAfter
	}

	if attempt <= 0 {
		return c.retryConfig.InitialInterval
	}

	// Exponential backoff with positive jitter avoids retrying before the
	// calculated delay while spreading concurrent clients across the window.
	delay := float64(c.retryConfig.InitialInterval)
	for i := 0; i < attempt-1; i++ {
		delay *= c.retryConfig.Multiplier
	}
	delay = min(delay, float64(c.retryConfig.MaxInterval))
	if delay < float64(c.retryConfig.MaxInterval) {
		delay += rand.Float64() * delay * 0.2
	}
	return min(time.Duration(delay), c.retryConfig.MaxInterval)
}

// ChatCompletionStream performs a streaming chat completion with automatic retries
func (c *Client) ChatCompletionStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, <-chan error) {
	chunkChan := make(chan StreamChunk, 10)
	errChan := make(chan error, 1)

	go func() {
		defer close(errChan)
		defer close(chunkChan)

		// Check if circuit breaker allows the request
		circuitErr := c.circuitBreaker.Call(func() error {
			return c.executeStreamRequest(ctx, req, chunkChan)
		})

		if circuitErr != nil {
			errChan <- circuitErr
		}
	}()

	return chunkChan, errChan
}

// executeStreamRequest performs the actual streaming request
func (c *Client) executeStreamRequest(ctx context.Context, req ChatRequest, chunkChan chan<- StreamChunk) error {
	req.Stream = true

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshaling request: %w", err)
	}

	// Retry loop for establishing connection
	var lastErr error
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			delay := c.calculateRetryDelay(attempt, lastErr)
			select {
			case <-ctx.Done():
				return errors.Join(ctx.Err(), lastErr)
			case <-time.After(delay):
			}
		}

		httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("creating request: %w", err)
		}

		c.setHeaders(httpReq)
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")

		// Wait for rate limiter
		if c.rateLimiter != nil {
			if err := c.rateLimiter.Wait(ctx); err != nil {
				return fmt.Errorf("rate limit wait: %w", err)
			}
		}

		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			lastErr = err
			if c.canRetryModelRequest(attempt, lastErr) {
				continue
			}
			return retryExhaustedError(attempt, lastErr)
		}

		if resp.StatusCode != http.StatusOK {
			apiErr := c.parseError(resp)
			resp.Body.Close()
			lastErr = apiErr

			// Only retry if error is retryable
			if c.canRetryModelRequest(attempt, apiErr) {
				continue
			}
			if isRetryableError(apiErr) {
				return retryExhaustedError(attempt, apiErr)
			}
			return apiErr
		}

		// Connection successful, parse SSE stream
		// Note: Once streaming starts, we don't retry mid-stream
		defer resp.Body.Close()
		if err := c.parseSSEStream(ctx, resp.Body, chunkChan); err != nil {
			return fmt.Errorf("parsing SSE stream: %w", err)
		}

		// Stream completed successfully
		return nil
	}

	return retryExhaustedError(c.retryLimit(lastErr), lastErr)
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
		if chunk.Error != nil {
			statusCode, _ := strconv.Atoi(strings.TrimSpace(chunk.Error.Code))
			if statusCode == 0 {
				statusCode = http.StatusOK
			}
			message := strings.TrimSpace(chunk.Error.Message)
			if message == "" {
				message = "provider returned a streaming error"
			}
			provider, details := providerErrorMetadata(chunk.Error.Metadata)
			return &APIError{
				StatusCode: statusCode,
				Message:    message,
				Type:       chunk.Error.Type,
				Code:       chunk.Error.Code,
				Provider:   provider,
				Details:    details,
				Retryable:  statusCode == http.StatusTooManyRequests || statusCode >= 500,
			}
		}

		select {
		case chunkChan <- chunk:
		case <-ctx.Done():
			return ctx.Err()
		}
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
	req.Header.Set("X-OpenRouter-Experimental-Metadata", "enabled")
}

// parseError parses an error response and wraps it with additional context
func (c *Client) parseError(resp *http.Response) error {
	provider := strings.TrimSpace(resp.Header.Get("X-Provider-Name"))
	requestID := firstNonEmptyHeader(resp.Header, "X-Request-ID", "X-Generation-ID")
	retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
	retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500

	// Read the body for debugging
	const maxErrorResponseSize = 50 * 1024 * 1024 // 50MB
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxErrorResponseSize))
	if readErr != nil {
		return &APIError{
			StatusCode: resp.StatusCode,
			Message:    resp.Status,
			Provider:   provider,
			RequestID:  requestID,
			Retryable:  retryable,
			RetryAfter: retryAfter,
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
			Provider:   provider,
			RequestID:  requestID,
			Retryable:  retryable,
			RetryAfter: retryAfter,
		}
	}

	// Use parsed error message if available, fallback to status
	message := errResp.Error.Message
	if message == "" {
		message = resp.Status
	}
	metadataProvider, details := providerErrorMetadata(errResp.Error.Metadata)
	if provider == "" {
		provider = metadataProvider
	}

	return &APIError{
		StatusCode: resp.StatusCode,
		Message:    message,
		Type:       errResp.Error.Type,
		Code:       errResp.Error.Code,
		Provider:   provider,
		Details:    details,
		RequestID:  requestID,
		Retryable:  retryable,
		RetryAfter: retryAfter,
	}
}

func firstNonEmptyHeader(header http.Header, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(header.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

func providerErrorMetadata(data json.RawMessage) (string, string) {
	if len(data) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return "", ""
	}
	var metadata struct {
		ProviderName string          `json:"provider_name"`
		Raw          json.RawMessage `json:"raw"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		return "", boundedErrorDetail(string(data))
	}
	return strings.TrimSpace(metadata.ProviderName), providerRawErrorDetail(metadata.Raw)
}

func providerRawErrorDetail(raw json.RawMessage) string {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		trimmed := strings.TrimSpace(text)
		if json.Valid([]byte(trimmed)) {
			return providerRawErrorDetail(json.RawMessage(trimmed))
		}
		return boundedErrorDetail(trimmed)
	}
	var envelope struct {
		Error   ErrorDetail `json:"error"`
		Message string      `json:"message"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil {
		if message := strings.TrimSpace(envelope.Error.Message); message != "" {
			return boundedErrorDetail(message)
		}
		if message := strings.TrimSpace(envelope.Message); message != "" {
			return boundedErrorDetail(message)
		}
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err == nil {
		return boundedErrorDetail(compact.String())
	}
	return boundedErrorDetail(string(raw))
}

func boundedErrorDetail(detail string) string {
	const maxDetailBytes = 1000
	detail = strings.Join(strings.Fields(detail), " ")
	if len(detail) <= maxDetailBytes {
		return detail
	}
	return detail[:maxDetailBytes] + "..."
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
