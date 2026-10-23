package model

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// A connection that dies while the response body is being read is a transient
// transport failure. It must be retried with backoff inside one call, and it
// must not count toward opening the circuit more than once.
func TestChatCompletion_RetriesTruncatedResponseBody(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Length", "500")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"x","choices":[`))
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Error("no hijacker")
				return
			}
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	client := NewClient("key", server.URL)
	cfg := DefaultRetryConfig()
	cfg.InitialInterval = time.Millisecond
	cfg.MaxInterval = 5 * time.Millisecond
	client.SetRetryConfig(cfg)

	resp, err := client.ChatCompletion(context.Background(), ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v, want the retry to succeed", err)
	}
	if resp == nil || len(resp.Choices) != 1 {
		t.Fatalf("response = %+v", resp)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("server calls = %d, want 2", got)
	}
	if state := client.CircuitBreakerState(); state != "closed" {
		t.Fatalf("circuit = %s, want closed", state)
	}
}
