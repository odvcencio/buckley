package model

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"m31labs.dev/buckley/pkg/config"
)

func TestOpenAICompatibleProvider_BlockedConsumerHonorsDeadlines(t *testing.T) {
	for _, tt := range []struct {
		name                 string
		idle, first, elapsed time.Duration
		wantError            string
		upstreamComplete     bool
	}{
		{"idle", time.Second, 0, time.Second, "stream idle timeout", false},
		{"first_content", 0, time.Second, time.Second, "stream first content timeout", false},
		{"parent", time.Minute, time.Minute, 10 * time.Second, "context deadline exceeded", false},
		{"idle_after_upstream_done", time.Second, 0, time.Second, "stream idle timeout", true},
		{"first_content_after_upstream_done", 0, time.Second, time.Second, "stream first content timeout", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				cancelled := make(chan struct{})
				var requests atomic.Int32
				provider := NewOpenAICompatibleProvider(config.OpenAICompatibleConfig{
					BaseURL:                   "http://provider.test",
					StreamIdleTimeout:         tt.idle,
					StreamFirstContentTimeout: tt.first,
				}, false)
				provider.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					requests.Add(1)
					reader, writer := io.Pipe()
					go func() {
						defer writer.Close()
						_, _ = io.WriteString(writer, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n")
						if tt.upstreamComplete {
							_, _ = io.WriteString(writer, "data: [DONE]\n\n")
							_ = writer.Close()
						}
						<-req.Context().Done()
						close(cancelled)
					}()
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": {"text/event-stream"}},
						Body:       reader,
					}, nil
				})}

				// No receiver: fake time advances only once delivery is blocked.
				started := time.Now()
				err := provider.invokeStream(ctx, ChatRequest{Model: "test-model"}, make(chan StreamChunk))
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Errorf("invokeStream error = %v, want %q", err, tt.wantError)
				}
				if elapsed := time.Since(started); elapsed != tt.elapsed {
					t.Errorf("elapsed = %s, want %s", elapsed, tt.elapsed)
				}
				synctest.Wait()
				select {
				case <-cancelled:
				default:
					t.Error("provider request was not cancelled")
				}
				if got := requests.Load(); got != 1 {
					t.Errorf("provider requests = %d, want no replay", got)
				}
			})
		})
	}
}
