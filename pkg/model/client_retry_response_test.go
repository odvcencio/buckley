package model

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type responseCloseRecorder struct {
	io.Reader
	onClose func()
}

func (r *responseCloseRecorder) Close() error {
	r.onClose()
	return nil
}

func TestClient_ChatCompletion_ClosesResponseBeforeRetry(t *testing.T) {
	client := NewClient("test-key", "http://test.invalid")
	t.Cleanup(func() { _ = client.Close() })
	client.rateLimiter = nil
	client.SetRetryConfig(RetryConfig{MaxRetries: 2, MaxRateLimitRetries: 2, InitialInterval: time.Microsecond, MaxInterval: time.Microsecond, Multiplier: 1})
	attempts, open, closed := 0, 0, 0
	client.httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		_ = req.Body.Close()
		if open != 0 {
			t.Errorf("retry retained %d previous response bodies", open)
		}
		attempts++
		open++
		body := `{"error":{"message":"retry shortly","code":429}}`
		if attempts == 3 {
			body = `{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: &responseCloseRecorder{
			Reader: strings.NewReader(body), onClose: func() { open--; closed++ },
		}}, nil
	})
	_, err := client.ChatCompletion(context.Background(), ChatRequest{Model: "test/model", Messages: []Message{{Role: "user", Content: "test"}}})
	if err != nil || attempts != 3 || open != 0 || closed != 3 {
		t.Fatalf("err=%v attempts=%d open=%d closed=%d", err, attempts, open, closed)
	}
}

func TestClient_ChatCompletion_ClosesResponseBeforeBackoff(t *testing.T) {
	client := NewClient("test-key", "http://test.invalid")
	t.Cleanup(func() { _ = client.Close() })
	client.rateLimiter = nil
	client.SetRetryConfig(RetryConfig{MaxRateLimitRetries: 1, InitialInterval: time.Hour, MaxInterval: time.Hour, Multiplier: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	attempts, closed := 0, 0
	client.httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		_ = req.Body.Close()
		attempts++
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: &responseCloseRecorder{
			Reader:  strings.NewReader(`{"error":{"message":"retry shortly","code":429}}`),
			onClose: func() { closed++; cancel() },
		}}, nil
	})
	_, err := client.ChatCompletion(ctx, ChatRequest{Model: "test/model", Messages: []Message{{Role: "user", Content: "test"}}})
	if !errors.Is(err, context.Canceled) || attempts != 1 || closed != 1 {
		t.Fatalf("response stayed open during backoff: err=%v attempts=%d closed=%d", err, attempts, closed)
	}
}

func TestClient_ChatCompletion_ClosesResponseOnReturn(t *testing.T) {
	for _, tc := range []struct {
		name          string
		body          string
		contentLength int64
		wantErr       bool
	}{
		{name: "success", body: `{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`},
		{name: "invalid_json", body: `{`, wantErr: true},
		{name: "oversized", contentLength: MaxProviderResponseBytes + 1, wantErr: true},
		{name: "empty_choices", body: `{"choices":[]}`, wantErr: true},
		{name: "permanent_error", body: `{"error":{"message":"invalid request","type":"invalid_request"}}`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := NewClient("test-key", "http://test.invalid")
			t.Cleanup(func() { _ = client.Close() })
			closed := 0
			client.httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				_ = req.Body.Close()
				return &http.Response{StatusCode: http.StatusOK, ContentLength: tc.contentLength, Header: make(http.Header), Body: &responseCloseRecorder{
					Reader: strings.NewReader(tc.body), onClose: func() { closed++ },
				}}, nil
			})
			_, err := client.ChatCompletion(context.Background(), ChatRequest{Model: "test/model", Messages: []Message{{Role: "user", Content: "test"}}})
			if (err != nil) != tc.wantErr || closed != 1 {
				t.Fatalf("err=%v wantErr=%v closed=%d", err, tc.wantErr, closed)
			}
			if tc.name == "oversized" && !errors.Is(err, ErrProviderResponseTooLarge) {
				t.Fatalf("response limit error changed: %v", err)
			}
		})
	}
}
