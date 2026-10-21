package model

import (
	"context"
	"io"
	"net/http"
	"runtime"
	"strings"
	"testing"
)

type repeatedStreamFrame struct {
	frame     string
	remaining int64
	read      int64
	offset    int
}

func (r *repeatedStreamFrame) Read(dst []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	want := min(len(dst), int(r.remaining))
	for offset := 0; offset < want; {
		n := copy(dst[offset:want], r.frame[r.offset:])
		offset += n
		r.offset = (r.offset + n) % len(r.frame)
	}
	r.remaining -= int64(want)
	r.read += int64(want)
	return want, nil
}

func TestProviderStream_KeepaliveBudgetProfile(t *testing.T) {
	frame := ":" + strings.Repeat("x", (64<<10)-2) + "\n"
	flood := &repeatedStreamFrame{frame: frame, remaining: 96 << 20}
	input := io.MultiReader(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"), flood, strings.NewReader("data: [DONE]\n\n"))
	chunks := make(chan StreamChunk, 2)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	events, err := ParseSSEStreamWithEventCount(context.Background(), input, chunks)
	runtime.ReadMemStats(&after)
	t.Logf("offered_keepalive_bytes=%d consumed_keepalive_bytes=%d allocated_bytes=%d events=%d rejected=%t", 96<<20, flood.read, after.TotalAlloc-before.TotalAlloc, events, err != nil)
	if err == nil || flood.read > 64<<20 || events != 1 || (<-chunks).Choices[0].Delta.Content != "partial" {
		t.Error("unbounded keepalives or lost prior output")
	}
}

func TestProviderStream_OllamaFrameAllocationProfile(t *testing.T) {
	flood := &networkLogFlood{remaining: 16 << 20}
	body := &trackedProviderErrorBody{Reader: io.MultiReader(
		strings.NewReader("{\"model\":\"fixture\",\"message\":{\"role\":\"assistant\",\"content\":\"partial\"}}\n{\"model\":\"fixture\",\"message\":{\"role\":\"assistant\",\"content\":\""),
		flood, strings.NewReader("\"},\"done\":true,\"done_reason\":\"stop\"}\n"))}
	requests := 0
	provider := NewOllamaProvider("http://fixture.invalid", false)
	provider.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body}, nil
	})}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	chunks, errs := provider.ChatCompletionStream(context.Background(), ChatRequest{Model: "fixture"})
	acc := NewStreamAccumulator()
	for chunk := range chunks {
		acc.Add(chunk)
	}
	var streamErr error
	for err := range errs {
		streamErr = err
	}
	runtime.ReadMemStats(&after)
	t.Logf("offered_frame_content_bytes=%d consumed_frame_content_bytes=%d allocated_bytes=%d retained_content_bytes=%d requests=%d closed=%t rejected=%t", 16<<20, flood.read, after.TotalAlloc-before.TotalAlloc, len(acc.Content()), requests, body.closed, streamErr != nil)
	if streamErr == nil || flood.read > 1<<20 || acc.Content() != "partial" || requests != 1 || !body.closed {
		t.Error("Ollama frame was unbounded or prior output/ownership changed")
	}
}
