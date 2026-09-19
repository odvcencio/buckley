package model

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestProviderRetryPolicy_RequestPaths(t *testing.T) {
	paths := []string{"openrouter", "openrouter_stream", "transport", "transport_body_stream", "transport_sse"}
	cases := []struct {
		name         string
		status       int
		failures     int32
		config       RetryConfig
		wantAttempts int32
		wantError    bool
	}{
		{"transient_recovery", 503, 1, RetryConfig{MaxRetries: 1}, 2, false},
		{"transient_exhaustion", 503, 10, RetryConfig{MaxRetries: 1, MaxRateLimitRetries: 3}, 2, true},
		{"rate_limit_recovery", 429, 3, RetryConfig{MaxRetries: 1, MaxRateLimitRetries: 3}, 4, false},
		{"rate_limit_exhaustion", 429, 10, RetryConfig{MaxRetries: 1, MaxRateLimitRetries: 3}, 4, true},
		{"rate_limit_keeps_base_budget", 429, 2, RetryConfig{MaxRetries: 2, MaxRateLimitRetries: 1}, 3, false},
		{"permanent_failure", 401, 10, RetryConfig{MaxRetries: 3, MaxRateLimitRetries: 3}, 1, true},
		{"negative_budget", 503, 10, RetryConfig{MaxRetries: -1}, 1, true},
	}
	for _, path := range paths {
		for _, tc := range cases {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				var attempts atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if attempts.Add(1) <= tc.failures {
						w.WriteHeader(tc.status)
						fmt.Fprint(w, `{"error":{"message":"upstream failure"}}`)
						return
					}
					if path == "openrouter_stream" || path == "transport_sse" {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprint(w, "data: {\"id\":\"test\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
						return
					}
					fmt.Fprint(w, `{"id":"test","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
				}))
				t.Cleanup(server.Close)

				client := NewClientWithOptions("test-key", server.URL, ClientOptions{RetryConfig: &tc.config})
				transport := NewProviderTransport(ProviderTransportOptions{RetryConfig: &tc.config})
				ctx := context.Background()
				req := ChatRequest{Model: "test/model", Messages: []Message{{Role: "user", Content: "hi"}}}
				var err error
				switch path {
				case "openrouter":
					_, err = client.ChatCompletion(ctx, req)
				case "openrouter_stream":
					chunks, errs := client.ChatCompletionStream(ctx, req)
					for range chunks {
					}
					for streamErr := range errs {
						err = errors.Join(err, streamErr)
					}
				case "transport":
					_, err = transport.Do(ctx, server.Client(), http.MethodPost, server.URL, req, nil)
				case "transport_body_stream":
					var body io.ReadCloser
					body, err = transport.DoStream(ctx, server.Client(), http.MethodPost, server.URL, req, nil)
					if body != nil {
						_, err = io.Copy(io.Discard, body)
						body.Close()
					}
				case "transport_sse":
					chunks := make(chan StreamChunk, 10)
					err = transport.Stream(ctx, server.Client(), http.MethodPost, server.URL, req, nil, chunks)
				}
				if got := attempts.Load(); got != tc.wantAttempts {
					t.Errorf("requests = %d, want %d", got, tc.wantAttempts)
				}
				if tc.wantError {
					var apiErr *APIError
					if !errors.As(err, &apiErr) || apiErr.StatusCode != tc.status {
						t.Fatalf("error = %v, want APIError with status %d", err, tc.status)
					}
				} else if err != nil {
					t.Fatalf("request did not recover: %v", err)
				}
			})
		}
	}
}
