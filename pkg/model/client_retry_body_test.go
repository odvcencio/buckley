package model

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type retryBodyContent struct {
	calls int
	err   error
}

func (c *retryBodyContent) MarshalJSON() ([]byte, error) {
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	return json.Marshal("same prompt on every attempt")
}

func TestClient_ChatCompletion_ReusesEncodedRequestAcrossRetries(t *testing.T) {
	for _, mode := range []string{"rate_limit", "network", "in_band"} {
		t.Run(mode, func(t *testing.T) {
			content := &retryBodyContent{}
			client := NewClient("test-key", "http://test.invalid")
			t.Cleanup(func() { _ = client.Close() })
			client.rateLimiter = nil
			client.SetRetryConfig(RetryConfig{MaxRetries: 2, MaxRateLimitRetries: 2, InitialInterval: time.Microsecond, MaxInterval: time.Microsecond, Multiplier: 1})
			var bodies []string
			client.httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(req.Body)
				_ = req.Body.Close()
				if err != nil {
					return nil, err
				}
				bodies = append(bodies, string(body))
				status, response := http.StatusOK, `{"id":"done","choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`
				if len(bodies) < 3 {
					switch mode {
					case "network":
						return nil, io.ErrUnexpectedEOF
					case "rate_limit":
						status = http.StatusTooManyRequests
					}
					response = `{"error":{"message":"retry shortly","code":429}}`
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
			})
			result, err := client.ChatCompletion(context.Background(), ChatRequest{Model: "test/model", Messages: []Message{{Role: "user", Content: content}}})
			if err != nil || result == nil || content.calls != 1 || len(bodies) != 3 {
				t.Fatalf("completion: err=%v encodings=%d requests=%d", err, content.calls, len(bodies))
			}
			if bodies[0] != bodies[1] || bodies[1] != bodies[2] || !strings.Contains(bodies[0], "same prompt on every attempt") {
				t.Fatal("retry body was changed or partially consumed")
			}
		})
	}
}

func TestClient_ChatCompletion_EncodingAdmissionAndFailure(t *testing.T) {
	for _, mode := range []string{"open_circuit", "marshal_error", "canceled_backoff"} {
		t.Run(mode, func(t *testing.T) {
			content := &retryBodyContent{}
			client := NewClient("test-key", "http://test.invalid")
			t.Cleanup(func() { _ = client.Close() })
			client.rateLimiter = nil
			client.SetRetryConfig(RetryConfig{MaxRetries: 2, MaxRateLimitRetries: 2, InitialInterval: time.Hour, MaxInterval: time.Hour, Multiplier: 1})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			client.httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				_ = req.Body.Close()
				cancel()
				return &http.Response{StatusCode: http.StatusTooManyRequests, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"retry shortly","code":429}}`))}, nil
			})
			if mode == "open_circuit" {
				client.circuitBreaker = NewCircuitBreaker(CircuitBreakerConfig{MaxFailures: 1, ResetTimeout: time.Hour})
				_ = client.circuitBreaker.Call(func() error { return io.ErrUnexpectedEOF })
			} else if mode == "marshal_error" {
				content.err = io.ErrClosedPipe
			}
			_, err := client.ChatCompletion(ctx, ChatRequest{Model: "test/model", Messages: []Message{{Role: "user", Content: content}}})
			switch mode {
			case "open_circuit":
				if err == nil || calls != 0 || content.calls != 0 {
					t.Fatalf("open circuit encoded or dispatched: err=%v encodings=%d requests=%d", err, content.calls, calls)
				}
			case "marshal_error":
				if !errors.Is(err, io.ErrClosedPipe) || calls != 0 || content.calls != 1 {
					t.Fatalf("encoding failure changed: err=%v encodings=%d requests=%d", err, content.calls, calls)
				}
			case "canceled_backoff":
				if !errors.Is(err, context.Canceled) || calls != 1 || content.calls != 1 {
					t.Fatalf("canceled retry continued: err=%v encodings=%d requests=%d", err, content.calls, calls)
				}
			}
		})
	}
}
