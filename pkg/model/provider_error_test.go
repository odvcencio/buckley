package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/config"
)

type trackedProviderErrorBody struct {
	io.Reader
	closed      bool
	terminalErr error
}

func (r *trackedProviderErrorBody) Read(dst []byte) (int, error) {
	n, err := r.Reader.Read(dst)
	if err == io.EOF && r.terminalErr != nil {
		err, r.terminalErr = r.terminalErr, nil
	}
	return n, err
}

func (r *trackedProviderErrorBody) Close() error { r.closed = true; return nil }

func TestProviderError_NetworkLoggingPreservesBodyAndClose(t *testing.T) {
	for _, mode := range []string{"small", "oversized", "read_error"} {
		t.Run(mode, func(t *testing.T) {
			payload := `{"error":{"message":"capacity limited"}}`
			if mode == "oversized" {
				payload += strings.Repeat(" ", 1<<20)
			}
			var input io.Reader = strings.NewReader(payload)
			original := &trackedProviderErrorBody{Reader: input}
			if mode == "read_error" {
				original.terminalErr = io.ErrUnexpectedEOF
			}
			log, err := os.Create(filepath.Join(t.TempDir(), "network.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			transport := &LoggingTransport{enabled: true, logFile: log, base: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 503, Header: make(http.Header), Body: original}, nil
			})}
			t.Cleanup(func() { _ = transport.Close() })
			req, _ := http.NewRequest("GET", "http://fixture.invalid/models", nil)
			response, err := transport.RoundTrip(req)
			if err != nil || original.closed {
				t.Fatalf("logging consumed response ownership: %v", err)
			}
			body, err := io.ReadAll(response.Body)
			if string(body) != payload || (mode == "read_error") != errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatal("logging changed the body or terminal read error")
			}
			if err := response.Body.Close(); err != nil || !original.closed {
				t.Fatal("closing the logged response did not close its original body")
			}
			if info, err := log.Stat(); err != nil || info.Size() > 12<<10 {
				t.Fatal("network error log entry exceeded its diagnostic budget")
			}
		})
	}
}

func TestProviderError_BodyBoundaryAndReadFailure(t *testing.T) {
	for _, size := range []int{64 << 10, (64 << 10) + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			payload := `{"error":{"message":"capacity limited","code":429}}`
			payload += strings.Repeat(" ", size-len(payload))
			err := parseProviderError(&http.Response{StatusCode: 429, Status: "429 Too Many Requests", Header: http.Header{
				"X-Request-Id": {"req-boundary"}, "Retry-After": {"2"},
			}, Body: io.NopCloser(strings.NewReader(payload))})
			if err.StatusCode != 429 || !err.Retryable || err.RequestID != "req-boundary" || err.RetryAfter != 2*time.Second {
				t.Fatalf("lost HTTP failure metadata: %+v", err)
			}
			if strings.Contains(err.Message, "body truncated") != (size > 64<<10) {
				t.Fatalf("incorrect truncation marker: %v", err)
			}
			if size == 64<<10 && (err.Message != "capacity limited" || err.Code != "429") {
				t.Fatal("exact-budget valid JSON did not preserve structured fields")
			}
		})
	}
	err := parseProviderError(&http.Response{StatusCode: 503, Status: "503 Service Unavailable", Header: http.Header{
		"X-Request-Id": {"req-read-failure"}, "Retry-After": {"3"},
	}, Body: io.NopCloser(iotest.ErrReader(io.ErrUnexpectedEOF))})
	if !err.Retryable || err.RequestID != "req-read-failure" || err.RetryAfter != 3*time.Second || err.Message != "503 Service Unavailable" {
		t.Fatalf("read failure lost useful metadata: %+v", err)
	}
}

func TestProviderError_RetainedFieldsAreBoundedAndUTF8(t *testing.T) {
	long := strings.Repeat("λ", 5000)
	payload, err := json.Marshal(map[string]any{"error": map[string]any{
		"message": long, "type": long[:2000], "code": long[:2000],
		"metadata": map[string]any{"provider_name": long[:2000], "raw": long},
	}, "openrouter_metadata": map[string]any{"is_byok": false}})
	if err != nil {
		t.Fatal(err)
	}
	apiErr := parseProviderError(&http.Response{StatusCode: 402, Header: http.Header{"X-Request-Id": {long}}, Body: io.NopCloser(strings.NewReader(string(payload)))})
	for _, field := range []struct {
		text  string
		limit int
	}{{apiErr.Message, 4096}, {apiErr.Details, 4096}, {apiErr.Type, 256}, {apiErr.Code, 256}, {apiErr.Provider, 256}, {apiErr.RequestID, 256}} {
		if len(field.text) > field.limit || !utf8.ValidString(field.text) {
			t.Fatal("diagnostic exceeded its budget or split UTF-8")
		}
	}
	if !strings.Contains(apiErr.Details, "openrouter_metadata") || !openRouterCreditBilledRoute(apiErr) || len(apiErr.Error()) > 12<<10 {
		t.Fatal("bounded details lost routing evidence or exceeded rendered budget")
	}
}

func TestProviderError_InBandBoundsPreserveClassificationAndEvents(t *testing.T) {
	message := strings.Repeat("x", 128<<10) + " temporarily overloaded"
	envelope, _ := json.Marshal(StreamChunk{Error: &ErrorDetail{Message: message}})
	stream := "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n" + "data: " + string(envelope) + "\n\n"
	chunks := make(chan StreamChunk, 2)
	events, err := ParseSSEStreamWithEventCount(context.Background(), strings.NewReader(stream), chunks)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || events != 1 || len(chunks) != 1 || apiErr.StatusCode != 429 || !apiErr.Retryable || len(apiErr.Message) > 4096 {
		t.Fatalf("stream lost classification/events or exceeded diagnostic budget: events=%d err=%v", events, err)
	}
	if (<-chunks).Choices[0].Delta.Content != "partial" {
		t.Fatal("prior stream output changed")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(envelope)
	}))
	t.Cleanup(server.Close)
	client := NewClient("test", server.URL)
	client.SetRetryConfig(RetryConfig{})
	t.Cleanup(func() { _ = client.Close() })
	_, err = client.ChatCompletion(context.Background(), ChatRequest{Model: "fixture"})
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 429 || !apiErr.Retryable || len(apiErr.Message) > 4096 {
		t.Fatalf("HTTP-200 error lost classification or exceeded diagnostic budget: %v", err)
	}
}

func TestProviderError_CompatibleCatalogFailuresAreBounded(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint(legacy), func(t *testing.T) {
			var requests atomic.Int32
			payload := strings.Repeat("x", 1<<20)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("X-Request-ID", "req-catalog")
				w.WriteHeader(503)
				_, _ = io.WriteString(w, payload)
			}))
			t.Cleanup(server.Close)
			cfg := config.OpenAICompatibleConfig{BaseURL: server.URL}
			provider := NewOpenAICompatibleProvider(cfg, false)
			if legacy {
				provider = NewLiteLLMProvider(cfg, false)
			}
			_, err := provider.FetchCatalog()
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.RequestID != "req-catalog" || apiErr.StatusCode != 503 || len(err.Error()) > 12<<10 || !strings.Contains(err.Error(), "body truncated") {
				t.Fatalf("catalog failure lost evidence or exceeded its budget: %v", err)
			}
			want := int32(1)
			if legacy {
				want = 2
			}
			if requests.Load() != want {
				t.Fatalf("catalog fallback behavior changed: requests=%d", requests.Load())
			}
		})
	}
}

func TestProviderError_HTTPRetryAfterOversizedBody(t *testing.T) {
	for _, mode := range []string{"openrouter", "direct"} {
		t.Run(mode, func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if attempts.Add(1) == 1 {
					w.Header().Set("X-Request-ID", "req-oversized")
					w.WriteHeader(503)
					_, _ = io.WriteString(w, `{"error":{"message":"temporarily overloaded: `)
					part := strings.Repeat("x", 8192)
					for range 256 {
						if _, err := io.WriteString(w, part); err != nil {
							return
						}
					}
					_, _ = io.WriteString(w, `"}}`)
					return
				}
				_, _ = io.WriteString(w, `{"id":"recovered","choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
			}))
			t.Cleanup(server.Close)
			retry := RetryConfig{MaxRetries: 1, InitialInterval: time.Millisecond, MaxInterval: time.Millisecond}
			if mode == "openrouter" {
				client := NewClient("test", server.URL)
				client.SetRetryConfig(retry)
				t.Cleanup(func() { _ = client.Close() })
				response, err := client.ChatCompletion(context.Background(), ChatRequest{Model: "fixture", Messages: []Message{{Role: "user", Content: "test"}}})
				if err != nil || response.ID != "recovered" {
					t.Fatalf("recovery failed: %v", err)
				}
			} else {
				transport := NewProviderTransport(ProviderTransportOptions{RetryConfig: &retry})
				response, err := transport.Do(context.Background(), server.Client(), "POST", server.URL, map[string]any{}, nil)
				if err != nil || !strings.Contains(string(response), "recovered") {
					t.Fatalf("recovery failed: %v", err)
				}
			}
			if attempts.Load() != 2 {
				t.Fatalf("unexpected retry count: %d", attempts.Load())
			}
			t.Logf("path=%s oversized_http_503=true requests=2 recovery_ok=true", mode)
		})
	}
}
