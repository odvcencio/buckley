package model

import (
	"context"
	"io"
	"net/http"
	"runtime"
	"strings"
	"testing"
)

func TestProviderResponse_AllocationProfile(t *testing.T) {
	for _, mode := range []string{"openrouter", "direct"} {
		t.Run(mode, func(t *testing.T) {
			flood := &networkLogFlood{remaining: 64 << 20}
			input := io.MultiReader(strings.NewReader(`{"id":"oversized","choices":[{"message":{"role":"assistant","content":"`), flood, strings.NewReader(`"}}]}`))
			body := &trackedProviderErrorBody{Reader: input}
			requests := 0
			httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				requests++
				return &http.Response{StatusCode: 200, ContentLength: -1, Header: make(http.Header), Body: body}, nil
			})}
			client := NewClient("test", "http://fixture.invalid")
			client.httpClient = httpClient
			t.Cleanup(func() { _ = client.Close() })
			transport := NewProviderTransport(ProviderTransportOptions{})
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			returned := 0
			var err error
			if mode == "openrouter" {
				var response *ChatResponse
				response, err = client.ChatCompletion(context.Background(), ChatRequest{Model: "fixture"})
				if response != nil {
					returned = len(response.Choices[0].Message.Content.(string))
				}
			} else {
				var response []byte
				response, err = transport.Do(context.Background(), httpClient, "POST", "http://fixture.invalid", map[string]any{}, nil)
				returned = len(response)
			}
			runtime.ReadMemStats(&after)
			t.Logf("offered_content_bytes=%d read_payload_bytes=%d allocated_bytes=%d returned_bytes=%d requests=%d closed=%t rejected=%t", 64<<20, flood.read, after.TotalAlloc-before.TotalAlloc, returned, requests, body.closed, err != nil)
			if err == nil || returned != 0 || requests != 1 || !body.closed || flood.read > 32<<20 {
				t.Error("oversized successful response was not rejected within its read budget")
			}
		})
	}
}
