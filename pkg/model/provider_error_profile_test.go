package model

import (
	"io"
	"net/http"
	"runtime"
	"strings"
	"testing"
)

type providerErrorFlood struct {
	remaining int
	read      int
}

func (r *providerErrorFlood) Read(dst []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := min(len(dst), r.remaining)
	for i := range dst[:n] {
		dst[i] = 'x'
	}
	r.remaining -= n
	r.read += n
	return n, nil
}

func TestProviderError_AllocationProfile(t *testing.T) {
	for _, mode := range []string{"openrouter", "direct"} {
		t.Run(mode, func(t *testing.T) {
			flood := &providerErrorFlood{remaining: 8 << 20}
			body := io.MultiReader(strings.NewReader(`{"error":{"message":"temporarily overloaded: `), flood,
				strings.NewReader(`","type":"provider_error","code":503}}`))
			resp := &http.Response{StatusCode: 503, Status: "503 Service Unavailable", Body: io.NopCloser(body), Header: http.Header{
				"X-Request-Id": {"req-profile"}, "Retry-After": {"1"},
			}}
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			var apiErr *APIError
			if mode == "openrouter" {
				apiErr = (&Client{}).parseError(resp).(*APIError)
			} else {
				apiErr = parseProviderError(resp)
			}
			rendered := apiErr.Error()
			runtime.ReadMemStats(&after)
			if !apiErr.Retryable || apiErr.RequestID != "req-profile" || !strings.Contains(rendered, "temporarily overloaded") {
				t.Fatal("lost retryability, request ID, or useful diagnostic prefix")
			}
			t.Logf("offered_message_bytes=%d read_payload_bytes=%d allocated_bytes=%d retained_message_bytes=%d rendered_error_bytes=%d retryable=true request_id=%s", 8<<20, flood.read, after.TotalAlloc-before.TotalAlloc, len(apiErr.Message), len(rendered), apiErr.RequestID)
			if flood.read > 64<<10 || len(rendered) > 12<<10 {
				t.Error("provider error exceeded read or retained diagnostic budget")
			}
		})
	}
}
