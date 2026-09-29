package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/model"
)

func acpStreamRetryCandidate(ctx context.Context, turn acpStreamTurn, err error) bool {
	return acpStreamRetryLimit(ctx, turn, err) > 0
}

func noGatewayBackoff(t *testing.T) *int32 {
	t.Helper()
	old := acpStreamRetrySleep
	var sleeps int32
	acpStreamRetrySleep = func(context.Context, int) error {
		atomic.AddInt32(&sleeps, 1)
		return nil
	}
	t.Cleanup(func() { acpStreamRetrySleep = old })
	return &sleeps
}

func acpSSEErrorChunk(w io.Writer, code, message string) {
	_, _ = fmt.Fprintf(w, "data: {\"error\":{\"code\":%q,\"message\":%q}}\n\n", code, message)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func TestStreamACPTurn_RetriesMidStream502BeforeToolExecution(t *testing.T) {
	sleeps := noGatewayBackoff(t)
	mgr, requests, _ := newACPHTTPStreamManager(t, func(w http.ResponseWriter, r *http.Request, ordinal int32) {
		if ordinal <= 2 {
			acpSSEChunk(t, w, "partial", "", "")
			acpSSEErrorChunk(w, "502", "upstream terminated the stream")
			return
		}
		acpSSEChunk(t, w, "recovered", "", "stop")
		writeACPDone(w)
	})
	collector := &collectingStream{}
	turn, err := streamACPTurnWithDelivery(context.Background(), mgr, model.ChatRequest{
		Model:    "gpt-4o",
		Messages: []model.Message{{Role: "user", Content: "answer"}},
	}, collector.fn, false)
	if err != nil {
		t.Fatalf("streamACPTurnWithDelivery: %v", err)
	}
	if got := model.ExtractTextContentOrEmpty(turn.Message.Content); got != "recovered" {
		t.Fatalf("content = %q, want recovered", got)
	}
	if got := atomic.LoadInt32(requests); got != 3 {
		t.Fatalf("provider requests = %d, want 3", got)
	}
	if got := atomic.LoadInt32(sleeps); got != 2 {
		t.Fatalf("backoff waits = %d, want 2", got)
	}
	if got := collector.messageChunks(); len(got) != 1 || got[0] != "recovered" {
		t.Fatalf("delivered chunks = %#v, want only the recovered answer", got)
	}
}

func TestStreamACPTurn_GatewayRetryIsBounded(t *testing.T) {
	noGatewayBackoff(t)
	mgr, requests, _ := newACPHTTPStreamManager(t, func(w http.ResponseWriter, r *http.Request, ordinal int32) {
		acpSSEChunk(t, w, "partial", "", "")
		acpSSEErrorChunk(w, "502", "bad gateway")
	})
	_, err := streamACPTurnWithDelivery(context.Background(), mgr, model.ChatRequest{
		Model:    "gpt-4o",
		Messages: []model.Message{{Role: "user", Content: "answer"}},
	}, nil, false)
	if err == nil {
		t.Fatal("expected an error after retries are exhausted")
	}
	if got := atomic.LoadInt32(requests); got != 1+acpGatewayRetryLimit {
		t.Fatalf("provider requests = %d, want %d", got, 1+acpGatewayRetryLimit)
	}
}

func TestStreamACPTurn_NoGatewayRetryAfterToolCallStarted(t *testing.T) {
	sleeps := noGatewayBackoff(t)
	mgr, requests, _ := newACPHTTPStreamManager(t, func(w http.ResponseWriter, r *http.Request, ordinal int32) {
		acpSSEJSON(t, w, map[string]any{
			"id": "chatcmpl-tool",
			"choices": []any{map[string]any{
				"index": 0,
				"delta": map[string]any{"tool_calls": []any{map[string]any{
					"index": 0, "id": "call-1", "type": "function",
					"function": map[string]any{"name": "read_file", "arguments": `{"path":"x"}`},
				}}},
				"finish_reason": nil,
			}},
		})
		acpSSEErrorChunk(w, "502", "bad gateway")
	})
	_, err := streamACPTurnWithDelivery(context.Background(), mgr, model.ChatRequest{
		Model:    "gpt-4o",
		Messages: []model.Message{{Role: "user", Content: "read"}},
	}, nil, false)
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := atomic.LoadInt32(requests); got != 1 {
		t.Fatalf("provider requests = %d, want no retry after a tool call started", got)
	}
	if atomic.LoadInt32(sleeps) != 0 {
		t.Fatal("must not back off when there is no retry")
	}
}

func TestStreamACPTurn_NoRetryOnPaymentAuthOrQuota(t *testing.T) {
	cases := []struct{ code, message string }{
		{"402", "insufficient credits"},
		{"401", "invalid api key"},
		{"403", "forbidden"},
		{"429", "quota exceeded"},
		{"502", "provider quota exhausted"},
	}
	for _, tc := range cases {
		t.Run(tc.code+"_"+tc.message, func(t *testing.T) {
			noGatewayBackoff(t)
			mgr, requests, _ := newACPHTTPStreamManager(t, func(w http.ResponseWriter, r *http.Request, ordinal int32) {
				acpSSEChunk(t, w, "partial", "", "")
				acpSSEErrorChunk(w, tc.code, tc.message)
			})
			_, err := streamACPTurnWithDelivery(context.Background(), mgr, model.ChatRequest{
				Model:    "gpt-4o",
				Messages: []model.Message{{Role: "user", Content: "answer"}},
			}, nil, false)
			if err == nil {
				t.Fatal("expected an error")
			}
			if got := atomic.LoadInt32(requests); got != 1 {
				t.Fatalf("provider requests = %d, want exactly 1 (no retry)", got)
			}
		})
	}
}

func TestStreamACPTurn_ToolHistoryReplayOnlyWhenStreamIsReplaySafe(t *testing.T) {
	noGatewayBackoff(t)
	req := model.ChatRequest{
		Model: "gpt-4o",
		Messages: []model.Message{
			{Role: "user", Content: "go"},
			{Role: "assistant", ToolCalls: []model.ToolCall{{ID: "call-1"}}},
			{Role: "tool", ToolCallID: "call-1", Content: "done"},
		},
	}
	handler := func(w http.ResponseWriter, r *http.Request, ordinal int32) {
		if ordinal == 1 {
			acpSSEChunk(t, w, "partial", "", "")
			acpSSEErrorChunk(w, "503", "unavailable")
			return
		}
		acpSSEChunk(t, w, "ok", "", "stop")
		writeACPDone(w)
	}

	mgr, requests, _ := newACPHTTPStreamManager(t, handler)
	if _, err := streamACPTurnWithDelivery(context.Background(), mgr, req, nil, false); err == nil {
		t.Fatal("interactive stream with tool history must not replay")
	}
	if got := atomic.LoadInt32(requests); got != 1 {
		t.Fatalf("interactive requests = %d, want 1", got)
	}

	mgr, requests, _ = newACPHTTPStreamManager(t, handler)
	ctx := withACPReplaySafeStream(context.Background())
	if _, err := streamACPTurnWithDelivery(ctx, mgr, req, nil, false); err != nil {
		t.Fatalf("replay-safe stream: %v", err)
	}
	if got := atomic.LoadInt32(requests); got != 2 {
		t.Fatalf("replay-safe requests = %d, want 2", got)
	}
}

func TestAcpStreamRetrySleepHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := acpStreamRetrySleep(ctx, 1); err == nil {
		t.Fatal("expected cancellation error")
	}
	if time.Since(start) > time.Second {
		t.Fatal("sleep did not stop on cancellation")
	}
}
