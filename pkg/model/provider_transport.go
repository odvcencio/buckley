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
	"time"

	"golang.org/x/time/rate"
)

// HeaderFunc sets provider-specific headers (auth scheme, API version, and
// so on) on an outgoing request. It runs after Content-Type is already set
// to application/json.
type HeaderFunc func(*http.Request)

// ProviderTransport centralizes the resilience Client already has for the
// OpenRouter path -- retry-before-body on 429/5xx, a rate limiter, a circuit
// breaker, structured *APIError, and Client's SSE parse semantics (error on
// a malformed chunk, in-band error detection) -- so the direct-HTTP
// providers (OpenAI, Anthropic, Google, LiteLLM) do not each hand-roll their
// own. It takes the *http.Client per call instead of owning one, so a
// provider's own httpClient field (swappable in tests) stays the single
// source of truth for the client actually used.
type ProviderTransport struct {
	rateLimiter    *rate.Limiter
	circuitBreaker *CircuitBreaker
	retryConfig    RetryConfig
}

// ProviderTransportOptions configures a new ProviderTransport. Zero values
// fall back to the same defaults Client uses.
type ProviderTransportOptions struct {
	RateLimit            rate.Limit
	BurstSize            int
	CircuitBreakerConfig *CircuitBreakerConfig
	RetryConfig          *RetryConfig
}

// NewProviderTransport builds a transport with its own rate limiter and
// circuit breaker, isolated from every other provider's transport.
func NewProviderTransport(opts ProviderTransportOptions) *ProviderTransport {
	rateLimit := opts.RateLimit
	if rateLimit <= 0 {
		rateLimit = defaultRateLimit
	}
	burst := opts.BurstSize
	if burst <= 0 {
		burst = defaultBurstSize
	}
	cbConfig := DefaultCircuitBreakerConfig()
	if opts.CircuitBreakerConfig != nil {
		cbConfig = *opts.CircuitBreakerConfig
	}
	retryConfig := DefaultRetryConfig()
	if opts.RetryConfig != nil {
		retryConfig = *opts.RetryConfig
	}
	return &ProviderTransport{
		rateLimiter:    rate.NewLimiter(rateLimit, burst),
		circuitBreaker: NewCircuitBreaker(cbConfig),
		retryConfig:    retryConfig,
	}
}

// SetRetryConfig updates the retry configuration used for subsequent calls.
// Tests use this to shorten backoff delays for scenarios that intentionally
// exhaust retries; production callers rarely need it.
func (t *ProviderTransport) SetRetryConfig(cfg RetryConfig) {
	if t == nil {
		return
	}
	t.retryConfig = cfg
}

func (t *ProviderTransport) retryConfigOrDefault() RetryConfig {
	if t == nil {
		return DefaultRetryConfig()
	}
	return t.retryConfig
}

func (t *ProviderTransport) retryLimit(err error) int {
	config := t.retryConfigOrDefault()
	limit := max(config.MaxRetries, 0)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.IsRateLimitError() && config.MaxRateLimitRetries > limit {
		limit = config.MaxRateLimitRetries
	}
	return limit
}

func (t *ProviderTransport) canRetry(attempt int, err error) bool {
	return isRetryableError(err) && attempt < t.retryLimit(err)
}

// calculateRetryDelay mirrors Client.calculateRetryDelay: honor a
// provider-reported Retry-After first, then fall back to exponential backoff
// with positive jitter.
func (t *ProviderTransport) calculateRetryDelay(attempt int, lastErr error) time.Duration {
	var apiErr *APIError
	if errors.As(lastErr, &apiErr) && apiErr.RetryAfter > 0 {
		return apiErr.RetryAfter
	}

	config := t.retryConfigOrDefault()
	if attempt <= 0 {
		return config.InitialInterval
	}
	delay := float64(config.InitialInterval)
	for i := 0; i < attempt-1; i++ {
		delay *= config.Multiplier
	}
	delay = min(delay, float64(config.MaxInterval))
	if delay < float64(config.MaxInterval) {
		delay += rand.Float64() * delay * 0.2
	}
	return min(time.Duration(delay), config.MaxInterval)
}

func (t *ProviderTransport) waitForRateLimit(ctx context.Context) error {
	if t == nil || t.rateLimiter == nil {
		return nil
	}
	if err := t.rateLimiter.Wait(ctx); err != nil {
		return fmt.Errorf("rate limit wait: %w", err)
	}
	return nil
}

func (t *ProviderTransport) runWithBreaker(call func() error) error {
	if t == nil || t.circuitBreaker == nil {
		return call()
	}
	return t.circuitBreaker.Call(call)
}

// Do marshals payload as the request body, retries 429/5xx responses and
// network errors before any caller sees a body -- matching
// Client.ChatCompletion's retry loop exactly -- and returns the successful
// response body already read and closed. Failures are always a *APIError
// (from parseProviderError) or a wrapped context/network error.
func (t *ProviderTransport) Do(ctx context.Context, httpClient *http.Client, method, url string, payload any, setHeaders HeaderFunc) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	var result []byte
	call := func() error {
		var lastErr error
		for attempt := 0; ; attempt++ {
			if attempt > 0 {
				delay := t.calculateRetryDelay(attempt, lastErr)
				select {
				case <-ctx.Done():
					return errors.Join(ctx.Err(), lastErr)
				case <-time.After(delay):
				}
			}

			httpReq, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
			if err != nil {
				return fmt.Errorf("creating request: %w", err)
			}
			httpReq.Header.Set("Content-Type", "application/json")
			if setHeaders != nil {
				setHeaders(httpReq)
			}

			if err := t.waitForRateLimit(ctx); err != nil {
				return err
			}

			resp, err := httpClient.Do(httpReq)
			if err != nil {
				lastErr = err
				if t.canRetry(attempt, lastErr) {
					continue
				}
				return retryExhaustedError(attempt, lastErr)
			}

			if resp.StatusCode != http.StatusOK {
				apiErr := parseProviderError(resp)
				resp.Body.Close()
				lastErr = apiErr
				if t.canRetry(attempt, apiErr) {
					continue
				}
				if isRetryableError(apiErr) {
					return retryExhaustedError(attempt, apiErr)
				}
				return apiErr
			}

			data, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				return fmt.Errorf("reading response body: %w", err)
			}
			result = data
			return nil
		}
	}

	if err := t.runWithBreaker(call); err != nil {
		return nil, err
	}
	return result, nil
}

// DoStream marshals payload, retries the initial connection with the same
// 429/5xx semantics as Do, and returns the open response body once connected
// with a 200 status. Mid-stream retry is out of scope, matching Client: once
// bytes start flowing the caller (typically via ParseSSEStream) owns error
// handling for the rest of the stream.
func (t *ProviderTransport) DoStream(ctx context.Context, httpClient *http.Client, method, url string, payload any, setHeaders HeaderFunc) (io.ReadCloser, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	var result io.ReadCloser
	call := func() error {
		var lastErr error
		for attempt := 0; ; attempt++ {
			if attempt > 0 {
				delay := t.calculateRetryDelay(attempt, lastErr)
				select {
				case <-ctx.Done():
					return errors.Join(ctx.Err(), lastErr)
				case <-time.After(delay):
				}
			}

			httpReq, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
			if err != nil {
				return fmt.Errorf("creating request: %w", err)
			}
			httpReq.Header.Set("Content-Type", "application/json")
			httpReq.Header.Set("Accept", "text/event-stream")
			if setHeaders != nil {
				setHeaders(httpReq)
			}

			if err := t.waitForRateLimit(ctx); err != nil {
				return err
			}

			resp, err := httpClient.Do(httpReq)
			if err != nil {
				lastErr = err
				if t.canRetry(attempt, lastErr) {
					continue
				}
				return retryExhaustedError(attempt, lastErr)
			}

			if resp.StatusCode != http.StatusOK {
				apiErr := parseProviderError(resp)
				resp.Body.Close()
				lastErr = apiErr
				if t.canRetry(attempt, apiErr) {
					continue
				}
				if isRetryableError(apiErr) {
					return retryExhaustedError(attempt, apiErr)
				}
				return apiErr
			}

			result = resp.Body
			return nil
		}
	}

	if err := t.runWithBreaker(call); err != nil {
		return nil, err
	}
	return result, nil
}

// parseProviderError mirrors Client.parseError: it reads and classifies a
// non-200 response into a structured *APIError, tolerating both OpenRouter's
// error envelope and the plainer {"error":{"message":...}} shape most direct
// vendor APIs use.
func parseProviderError(resp *http.Response) *APIError {
	provider := strings.TrimSpace(resp.Header.Get("X-Provider-Name"))
	requestID := firstNonEmptyHeader(resp.Header, "X-Request-ID", "X-Generation-ID", "Request-Id")
	retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
	retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500

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

// ParseSSEStream parses a Server-Sent Events stream into StreamChunk values
// with the same semantics as Client's SSE parser: a malformed JSON chunk is
// a hard error (never silently skipped, unlike the hand-rolled parsers this
// replaces), and a chunk carrying an in-band Error field is translated into
// a structured *APIError instead of being forwarded as if it were a normal
// delta.
func ParseSSEStream(ctx context.Context, r io.Reader, chunkChan chan<- StreamChunk) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 1MB max line

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			return nil
		}

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
