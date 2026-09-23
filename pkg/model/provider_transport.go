package model

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

// Internal wire safety cap only; this is not a task/tool-call governance budget.
const maxStreamingToolCallIndex = 1024

// HeaderFunc sets provider-specific headers (auth scheme, API version, and
// so on) on an outgoing request. It runs after Content-Type is already set
// to application/json.
type HeaderFunc func(*http.Request)

// ProviderTransport centralizes the resilience Client already has for the
// OpenRouter path -- retry-before-body on 429/5xx, a rate limiter, a circuit
// breaker, structured *APIError, and Client's SSE parse semantics (error on
// a malformed chunk, in-band error detection) -- so the direct-HTTP
// providers (OpenAI, Anthropic, Google, OpenAI-compatible) do not each hand-roll their
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

func (t *ProviderTransport) waitForRateLimit(ctx context.Context) error {
	if t == nil || t.rateLimiter == nil {
		return nil
	}
	if err := t.rateLimiter.Wait(ctx); err != nil {
		return fmt.Errorf("rate limit wait: %w", err)
	}
	return nil
}

// waitBeforeProviderRetry sleeps for the computed retry backoff before a
// subsequent attempt. Attempt 0 performs no wait. On cancellation it returns
// the context error joined with the last failure so both causes stay visible.
func (t *ProviderTransport) waitBeforeProviderRetry(ctx context.Context, attempt int, lastErr error) error {
	if attempt <= 0 {
		return nil
	}
	delay := t.retryConfigOrDefault().retryDelay(attempt, lastErr)
	select {
	case <-ctx.Done():
		return errors.Join(ctx.Err(), lastErr)
	case <-time.After(delay):
	}
	return nil
}

// newProviderRequest builds the JSON request shared by Do, DoStream, and
// Stream: Content-Type is always application/json, Accept is set to
// text/event-stream only for streaming requests, and setHeaders (when
// non-nil) runs last so providers can override or extend them.
func newProviderRequest(ctx context.Context, method, url string, body []byte, stream bool, setHeaders HeaderFunc) (*http.Request, error) {
	httpReq, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	}
	if setHeaders != nil {
		setHeaders(httpReq)
	}
	return httpReq, nil
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
			if err := t.waitBeforeProviderRetry(ctx, attempt, lastErr); err != nil {
				return err
			}

			httpReq, err := newProviderRequest(ctx, method, url, body, false, setHeaders)
			if err != nil {
				return err
			}

			if err := t.waitForRateLimit(ctx); err != nil {
				return err
			}

			resp, err := httpClient.Do(httpReq)
			if err != nil {
				lastErr = err
				if t.retryConfigOrDefault().canRetry(attempt, lastErr) {
					continue
				}
				return retryExhaustedError(attempt, lastErr)
			}

			if resp.StatusCode != http.StatusOK {
				apiErr := parseProviderError(resp)
				resp.Body.Close()
				lastErr = apiErr
				if t.retryConfigOrDefault().canRetry(attempt, apiErr) {
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
// with a 200 status. Callers that consume OpenAI-compatible SSE should prefer
// Stream so an interruption before the first event can be retried safely.
func (t *ProviderTransport) DoStream(ctx context.Context, httpClient *http.Client, method, url string, payload any, setHeaders HeaderFunc) (io.ReadCloser, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	var result io.ReadCloser
	call := func() error {
		var lastErr error
		for attempt := 0; ; attempt++ {
			if err := t.waitBeforeProviderRetry(ctx, attempt, lastErr); err != nil {
				return err
			}

			httpReq, err := newProviderRequest(ctx, method, url, body, true, setHeaders)
			if err != nil {
				return err
			}

			if err := t.waitForRateLimit(ctx); err != nil {
				return err
			}

			resp, err := httpClient.Do(httpReq)
			if err != nil {
				lastErr = err
				if t.retryConfigOrDefault().canRetry(attempt, lastErr) {
					continue
				}
				return retryExhaustedError(attempt, lastErr)
			}

			if resp.StatusCode != http.StatusOK {
				apiErr := parseProviderError(resp)
				resp.Body.Close()
				lastErr = apiErr
				if t.retryConfigOrDefault().canRetry(attempt, apiErr) {
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

// Stream executes an OpenAI-compatible SSE request. It retries an interrupted
// response only if no event reached chunkChan, avoiding duplicate text or a
// replayed tool call after the provider has started its response.
func (t *ProviderTransport) Stream(ctx context.Context, httpClient *http.Client, method, url string, payload any, setHeaders HeaderFunc, chunkChan chan<- StreamChunk) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshaling request: %w", err)
	}

	call := func() error {
		var lastErr error
		for attempt := 0; ; attempt++ {
			if err := t.waitBeforeProviderRetry(ctx, attempt, lastErr); err != nil {
				return err
			}

			httpReq, err := newProviderRequest(ctx, method, url, body, true, setHeaders)
			if err != nil {
				return err
			}
			if err := t.waitForRateLimit(ctx); err != nil {
				return err
			}

			resp, err := httpClient.Do(httpReq)
			if err != nil {
				lastErr = err
				if t.retryConfigOrDefault().canRetry(attempt, lastErr) {
					continue
				}
				return retryExhaustedError(attempt, lastErr)
			}
			if resp.StatusCode != http.StatusOK {
				apiErr := parseProviderError(resp)
				resp.Body.Close()
				lastErr = apiErr
				if t.retryConfigOrDefault().canRetry(attempt, apiErr) {
					continue
				}
				if isRetryableError(apiErr) {
					return retryExhaustedError(attempt, apiErr)
				}
				return apiErr
			}

			events, streamErr := ParseSSEStreamWithEventCount(ctx, resp.Body, chunkChan)
			_ = resp.Body.Close()
			if streamErr == nil {
				return nil
			}
			lastErr = fmt.Errorf("parsing SSE stream: %w", streamErr)
			if events == 0 && t.retryConfigOrDefault().canRetry(attempt, lastErr) {
				continue
			}
			if events == 0 && isRetryableError(lastErr) {
				return retryExhaustedError(attempt, lastErr)
			}
			return lastErr
		}
	}

	return t.runWithBreaker(call)
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
	if message == "" && errResp.LimitSource != "" {
		message = fmt.Sprintf("upstream provider rate limit (limit_source: %s)", errResp.LimitSource)
	} else if message == "" {
		message = resp.Status
	}
	metadataProvider, details := providerErrorMetadata(errResp.Error.Metadata)
	if provider == "" {
		provider = metadataProvider
	}

	return &APIError{
		StatusCode:  resp.StatusCode,
		Message:     message,
		Type:        errResp.Error.Type,
		Code:        errResp.Error.Code,
		Provider:    provider,
		Details:     details,
		RequestID:   requestID,
		Retryable:   retryable,
		RetryAfter:  retryAfter,
		LimitSource: errResp.LimitSource,
	}
}

// ParseSSEStream parses a Server-Sent Events stream into StreamChunk values
// with the same semantics as Client's SSE parser: a malformed JSON chunk is
// a hard error (never silently skipped, unlike the hand-rolled parsers this
// replaces), and a chunk carrying an in-band Error field is translated into
// a structured *APIError instead of being forwarded as if it were a normal
// delta.
func ParseSSEStream(ctx context.Context, r io.Reader, chunkChan chan<- StreamChunk) error {
	_, err := ParseSSEStreamWithEventCount(ctx, r, chunkChan)
	return err
}

// ParseSSEStreamWithEventCount parses an OpenAI-compatible SSE response and
// reports how many events were delivered. A stream without [DONE] ended
// incompletely even when its connection reached EOF without a scanner error.
func ParseSSEStreamWithEventCount(ctx context.Context, r io.Reader, chunkChan chan<- StreamChunk) (int, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 1MB max line
	events := 0

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return events, ctx.Err()
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
			return events, nil
		}

		var chunk StreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return events, fmt.Errorf("decoding chunk: %w", err)
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
			retryable := statusCode == http.StatusTooManyRequests || statusCode >= 500
			if !retryable && looksLikeTransientProviderError(chunk.Error) {
				// The SSE data: chunk carries an in-band transient
				// (usually rate-limit) error before any content, with no
				// numeric status in its "code" field (C8). Treat it like a
				// 429: retryable, with the same backoff.
				statusCode = http.StatusTooManyRequests
				retryable = true
			}
			return events, &APIError{
				StatusCode: statusCode,
				Message:    message,
				Type:       chunk.Error.Type,
				Code:       chunk.Error.Code,
				Provider:   provider,
				Details:    details,
				Retryable:  retryable,
			}
		}
		if err := validateStreamingToolCallIndices(chunk); err != nil {
			return events, err
		}
		chunk.ExecutionIdentity = observedExecutionIdentity(chunk.ID, chunk.Model, nil)

		select {
		case chunkChan <- chunk:
			events++
		case <-ctx.Done():
			return events, ctx.Err()
		}
	}

	if err := scanner.Err(); err != nil {
		return events, fmt.Errorf("reading stream: %w", err)
	}
	return events, io.ErrUnexpectedEOF
}

func validateStreamingToolCallIndices(chunk StreamChunk) error {
	for _, choice := range chunk.Choices {
		for _, toolCall := range choice.Delta.ToolCalls {
			if toolCall.Index < 0 {
				return fmt.Errorf("streaming protocol violation: tool call index %d is negative", toolCall.Index)
			}
			// The accumulator stores tool calls by dense slice index. A sparse
			// untrusted index can otherwise force excessive allocation.
			if toolCall.Index > maxStreamingToolCallIndex {
				return fmt.Errorf("streaming protocol violation: tool call index %d exceeds maximum supported index %d", toolCall.Index, maxStreamingToolCallIndex)
			}
		}
	}
	return nil
}
