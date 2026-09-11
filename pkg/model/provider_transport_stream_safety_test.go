package model

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseSSEStreamWithEventCount_RejectsNegativeToolCallIndexAfterPartialText(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"id":"1","choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":null}]}`,
		"",
		`data: {"id":"1","choices":[{"index":0,"delta":{"tool_calls":[{"index":-1000000000,"id":"call_bad","type":"function","function":{"name":"bad","arguments":"{}"}}]},"finish_reason":null}]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")
	chunkChan := make(chan StreamChunk, 4)

	events, err := ParseSSEStreamWithEventCount(context.Background(), strings.NewReader(stream), chunkChan)
	if err == nil {
		t.Fatal("expected negative tool call index to fail")
	}
	if !strings.Contains(err.Error(), "streaming protocol violation") || !strings.Contains(err.Error(), "negative") {
		t.Fatalf("error = %v, want explicit protocol violation for negative index", err)
	}
	if events != 1 {
		t.Fatalf("events = %d, want the prior text event preserved", events)
	}
	if len(chunkChan) != 1 {
		t.Fatalf("delivered chunks = %d, want only the prior valid text chunk", len(chunkChan))
	}
	chunk := <-chunkChan
	if got := chunk.Choices[0].Delta.Content; got != "partial" {
		t.Fatalf("content = %q, want partial", got)
	}
}

func TestParseSSEStreamWithEventCount_AcceptsMaxToolCallIndexAndRejectsNext(t *testing.T) {
	t.Run("max index accepted", func(t *testing.T) {
		stream := strings.Join([]string{
			`data: {"id":"1","choices":[{"index":0,"delta":{"tool_calls":[{"index":1024,"id":"call_max","type":"function","function":{"name":"max","arguments":"{}"}}]},"finish_reason":null}]}`,
			"",
			`data: [DONE]`,
			"",
		}, "\n")
		chunkChan := make(chan StreamChunk, 1)

		events, err := ParseSSEStreamWithEventCount(context.Background(), strings.NewReader(stream), chunkChan)
		if err != nil {
			t.Fatalf("ParseSSEStreamWithEventCount() error = %v", err)
		}
		if events != 1 {
			t.Fatalf("events = %d, want 1", events)
		}
		if got := (<-chunkChan).Choices[0].Delta.ToolCalls[0].Index; got != maxStreamingToolCallIndex {
			t.Fatalf("tool call index = %d, want %d", got, maxStreamingToolCallIndex)
		}
	})

	t.Run("next index rejected", func(t *testing.T) {
		stream := strings.Join([]string{
			`data: {"id":"1","choices":[{"index":0,"delta":{"tool_calls":[{"index":1025,"id":"call_too_large","type":"function","function":{"name":"large","arguments":"{}"}}]},"finish_reason":null}]}`,
			"",
			`data: [DONE]`,
			"",
		}, "\n")
		chunkChan := make(chan StreamChunk, 1)

		events, err := ParseSSEStreamWithEventCount(context.Background(), strings.NewReader(stream), chunkChan)
		if err == nil {
			t.Fatal("expected tool call index above safety cap to fail")
		}
		if !strings.Contains(err.Error(), "exceeds maximum supported index") {
			t.Fatalf("error = %v, want maximum-index violation", err)
		}
		if events != 0 {
			t.Fatalf("events = %d, want no delivered events", events)
		}
		if len(chunkChan) != 0 {
			t.Fatalf("delivered chunks = %d, want malformed chunk withheld", len(chunkChan))
		}
	})
}

func TestParseSSEStreamWithEventCount_RejectsHugeToolCallIndexWithoutDelivery(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"id":"1","choices":[{"index":0,"delta":{"tool_calls":[{"index":1000000000,"id":"call_bad","type":"function","function":{"name":"bad","arguments":"{}"}}]},"finish_reason":null}]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")
	chunkChan := make(chan StreamChunk, 1)

	events, err := ParseSSEStreamWithEventCount(context.Background(), strings.NewReader(stream), chunkChan)
	if err == nil {
		t.Fatal("expected huge tool call index to fail")
	}
	if !strings.Contains(err.Error(), "streaming protocol violation") || !strings.Contains(err.Error(), "exceeds maximum supported index") {
		t.Fatalf("error = %v, want explicit resource-bound violation", err)
	}
	if events != 0 {
		t.Fatalf("events = %d, want no delivered events", events)
	}
	if len(chunkChan) != 0 {
		t.Fatalf("delivered chunks = %d, want malformed chunk withheld", len(chunkChan))
	}
}

func TestParseSSEStreamWithEventCount_AllowsValidToolCallFragmentsAndMissingIndex(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"id":"1","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"second","arguments":"{\"b\""}}]},"finish_reason":null}]}`,
		"",
		`data: {"id":"1","choices":[{"index":0,"delta":{"tool_calls":[{"id":"call_a","type":"function","function":{"name":"first","arguments":"{\"a\""}}]},"finish_reason":null}]}`,
		"",
		`data: {"id":"1","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":":2}"}},{"function":{"arguments":":1}"}}]},"finish_reason":null}]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")
	chunkChan := make(chan StreamChunk, 8)

	events, err := ParseSSEStreamWithEventCount(context.Background(), strings.NewReader(stream), chunkChan)
	if err != nil {
		t.Fatalf("ParseSSEStreamWithEventCount() error = %v", err)
	}
	if events != 3 {
		t.Fatalf("events = %d, want 3", events)
	}

	acc := NewStreamAccumulator()
	for i := 0; i < events; i++ {
		acc.Add(<-chunkChan)
	}
	toolCalls := acc.ToolCalls()
	if len(toolCalls) != 2 {
		t.Fatalf("tool calls = %#v, want 2 accumulated calls", toolCalls)
	}
	if toolCalls[0].ID != "call_a" || toolCalls[0].Function.Name != "first" || toolCalls[0].Function.Arguments != `{"a":1}` {
		t.Fatalf("toolCalls[0] = %#v", toolCalls[0])
	}
	if toolCalls[1].ID != "call_b" || toolCalls[1].Function.Name != "second" || toolCalls[1].Function.Arguments != `{"b":2}` {
		t.Fatalf("toolCalls[1] = %#v", toolCalls[1])
	}
}

func TestProviderTransportStream_DoesNotRetryInvalidToolCallIndexAfterPartialEvent(t *testing.T) {
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"},\"finish_reason\":null}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":-1,\"id\":\"call_bad\"}]},\"finish_reason\":null}]}\n\n")
	}))
	defer server.Close()

	transport := NewProviderTransport(ProviderTransportOptions{
		RetryConfig: &RetryConfig{
			MaxRetries:          3,
			MaxRateLimitRetries: 3,
			InitialInterval:     time.Millisecond,
			MaxInterval:         time.Millisecond,
			Multiplier:          1,
		},
	})
	chunkChan := make(chan StreamChunk, 4)
	err := transport.Stream(context.Background(), server.Client(), http.MethodPost, server.URL, map[string]string{"prompt": "hi"}, nil, chunkChan)
	if err == nil {
		t.Fatal("expected invalid stream error")
	}
	if atomic.LoadInt32(&requests) != 1 {
		t.Fatalf("requests = %d, want no retry after a delivered event", requests)
	}
	if len(chunkChan) != 1 {
		t.Fatalf("delivered chunks = %d, want only the prior valid text chunk", len(chunkChan))
	}
}

func TestParseSSEStreamWithEventCount_CancellationPreservesDeliveredEventCount(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := strings.Join([]string{
		`data: {"id":"1","choices":[{"index":0,"delta":{"content":"first"},"finish_reason":null}]}`,
		"",
		`data: {"id":"1","choices":[{"index":0,"delta":{"content":"second"},"finish_reason":null}]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")
	chunkChan := make(chan StreamChunk)
	type result struct {
		events int
		err    error
	}
	done := make(chan result, 1)
	go func() {
		events, err := ParseSSEStreamWithEventCount(ctx, strings.NewReader(stream), chunkChan)
		done <- result{events: events, err: err}
	}()

	chunk := <-chunkChan
	if got := chunk.Choices[0].Delta.Content; got != "first" {
		t.Fatalf("content = %q, want first", got)
	}
	cancel()

	select {
	case got := <-done:
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", got.err)
		}
		if got.events != 1 {
			t.Fatalf("events = %d, want delivered count preserved through cancellation", got.events)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for parser cancellation")
	}
}
