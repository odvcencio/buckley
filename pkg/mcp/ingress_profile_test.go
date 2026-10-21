package mcp

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestClient_IngressProfile(t *testing.T) {
	for _, size := range []int{16 << 20, 32 << 20} {
		t.Run(fmt.Sprintf("bytes=%d", size), func(t *testing.T) {
			client, err := NewClient(context.Background(), fakeConfig(t, "large", "-tools=0", fmt.Sprintf("-response-bytes=%d", size)))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close() })
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			result, callErr := client.CallTool(context.Background(), "echo", map[string]any{"text": "report"})
			runtime.ReadMemStats(&after)
			retained := 0
			if result != nil {
				for _, block := range result.Content {
					if text, ok := block.(*sdkmcp.TextContent); ok {
						retained += len(text.Text)
					}
				}
			}
			if callErr == nil || result != nil {
				t.Error("oversized response decoded instead of rejected")
			}
			if callErr != nil && !errors.Is(callErr, ErrMessageTooLarge) {
				t.Errorf("size rejection cause lost: %v", callErr)
			}
			t.Logf("source_payload_bytes=%d client_alloc_bytes=%d returned_text_bytes=%d rejected=%t error=%v", size, after.TotalAlloc-before.TotalAlloc, retained, callErr != nil, callErr)
		})
	}
}
