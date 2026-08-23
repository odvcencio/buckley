package model

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
)

const strictZDROneUseOracleWire = `{"model":"qwen/qwen3.7-flash","messages":[{"role":"user","content":"generate commit"}],"stream":false,"provider":{"allow_fallbacks":false,"zdr":true}}`

func strictZDROneUseBinding() [sha256.Size]byte {
	return sha256.Sum256([]byte("immutable commit snapshot"))
}

func strictZDROneUseRequest() ChatRequest {
	return ChatRequest{
		Model:    "qwen/qwen3.7-flash",
		Messages: []Message{{Role: "user", Content: "generate commit"}},
	}
}

func newStrictZDROneUseTestClient(t *testing.T, transport http.RoundTripper) (*OneUseStrictZDROpenRouterClient, *Client) {
	t.Helper()
	provider, client := newOSSAdmissionTestProvider(t, transport)
	governed, err := newOneUseStrictZDROpenRouterClient(provider, "qwen/qwen3.7-flash", strictZDROneUseBinding())
	if err != nil {
		t.Fatalf("newOneUseStrictZDROpenRouterClient: %v", err)
	}
	return governed, client
}

func strictZDROneUseSuccess(req *http.Request) *http.Response {
	return ossAdmissionResponse(req, http.StatusOK, `{"id":"ok","model":"qwen/qwen3.7-flash","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
}

func TestOneUseStrictZDROpenRouterClient_ExactWireAndSingleDispatch(t *testing.T) {
	var calls atomic.Int32
	var governed *OneUseStrictZDROpenRouterClient
	governed, raw := newStrictZDROneUseTestClient(t, ossAdmissionRoundTripper(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		if governed == nil || !governed.used.Load() {
			t.Fatal("transport entered before outer one-use CAS")
		}
		if err := assertOSSAdmissionOracleRequest(req, "test-key", false, strictZDROneUseOracleWire); err != nil {
			t.Fatal(err)
		}
		return strictZDROneUseSuccess(req), nil
	}))
	if governed.provider.client != raw || governed.client != raw || governed.httpClient != raw.ossHTTPClient {
		t.Fatal("governed client is not bound to the exact provider/client/transport")
	}
	if governed.httpClient == raw.httpClient {
		t.Fatal("governed request uses the ordinary client")
	}
	if _, logging := governed.httpClient.Transport.(*LoggingTransport); logging {
		t.Fatal("governed request uses the logging transport")
	}
	if governed.httpClient.CheckRedirect == nil {
		t.Fatal("governed request lacks redirect rejection")
	}

	if _, err := governed.ChatCompletion(context.Background(), strictZDROneUseRequest()); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if _, err := governed.ChatCompletion(context.Background(), strictZDROneUseRequest()); !errors.Is(err, ErrOpenRouterStrictZDROneUseSpent) {
		t.Fatalf("second call error = %v, want spent", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("transport calls = %d, want 1", calls.Load())
	}
}

func TestOneUseStrictZDROpenRouterClient_DynamicContentLargeIntegerPreserved(t *testing.T) {
	const expectedWire = `{"model":"qwen/qwen3.7-flash","messages":[{"role":"user","content":{"nonce":9007199254740993}}],"stream":false,"provider":{"allow_fallbacks":false,"zdr":true}}`
	var calls atomic.Int32
	governed, _ := newStrictZDROneUseTestClient(t, ossAdmissionRoundTripper(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		if err := assertOSSAdmissionOracleRequest(req, "test-key", false, expectedWire); err != nil {
			t.Fatal(err)
		}
		return strictZDROneUseSuccess(req), nil
	}))
	req := strictZDROneUseRequest()
	req.Messages[0].Content = map[string]any{"nonce": int64(9007199254740993)}

	if _, err := governed.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("transport calls = %d, want 1", calls.Load())
	}
}

func TestOneUseStrictZDROpenRouterClient_ConcurrentCallsDispatchExactlyOnce(t *testing.T) {
	var calls atomic.Int32
	governed, _ := newStrictZDROneUseTestClient(t, ossAdmissionRoundTripper(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		return strictZDROneUseSuccess(req), nil
	}))

	const workers = 64
	start := make(chan struct{})
	results := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := governed.ChatCompletion(context.Background(), strictZDROneUseRequest())
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	successes := 0
	spent := 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrOpenRouterStrictZDROneUseSpent):
			spent++
		default:
			t.Fatalf("unexpected call error: %v", err)
		}
	}
	if successes != 1 || spent != workers-1 || calls.Load() != 1 {
		t.Fatalf("successes=%d spent=%d calls=%d", successes, spent, calls.Load())
	}
}

func TestOneUseStrictZDROpenRouterClient_InvalidRequestsStayLocalAndUnspent(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ChatRequest)
	}{
		{name: "model", mutate: func(req *ChatRequest) { req.Model = "stealth/ox-alpha" }},
		{name: "stream", mutate: func(req *ChatRequest) { req.Stream = true }},
		{name: "fallback models", mutate: func(req *ChatRequest) { req.Models = []string{"other/model"} }},
		{name: "provider fields", mutate: func(req *ChatRequest) { req.Provider = map[string]any{"zdr": true} }},
		{name: "transforms", mutate: func(req *ChatRequest) { req.Transforms = []string{"middle-out"} }},
		{name: "retention", mutate: func(req *ChatRequest) { req.OpenRouterRetention = OpenRouterRetentionZDR }},
		{name: "retry", mutate: func(req *ChatRequest) { req.RetryMode = RequestRetrySingleAttempt }},
		{name: "prompt cache", mutate: func(req *ChatRequest) { req.PromptCache = &PromptCache{Enabled: true} }},
		{name: "wire cache key", mutate: func(req *ChatRequest) {
			req.Messages[0].Content = map[string]any{"cache_control": map[string]any{"type": "ephemeral"}}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			governed, _ := newStrictZDROneUseTestClient(t, ossAdmissionRoundTripper(func(req *http.Request) (*http.Response, error) {
				calls.Add(1)
				return strictZDROneUseSuccess(req), nil
			}))
			req := strictZDROneUseRequest()
			tt.mutate(&req)
			if _, err := governed.ChatCompletion(context.Background(), req); err == nil {
				t.Fatal("invalid request was accepted")
			}
			if calls.Load() != 0 || governed.used.Load() {
				t.Fatalf("invalid request calls=%d used=%v", calls.Load(), governed.used.Load())
			}
			if _, err := governed.ChatCompletion(context.Background(), strictZDROneUseRequest()); err != nil {
				t.Fatalf("valid request after local rejection: %v", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("transport calls = %d, want 1", calls.Load())
			}
		})
	}
}

func TestOneUseStrictZDROpenRouterClient_PostCASFailureIsSpentWithoutRetry(t *testing.T) {
	var calls atomic.Int32
	governed, _ := newStrictZDROneUseTestClient(t, ossAdmissionRoundTripper(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		return ossAdmissionResponse(req, http.StatusTooManyRequests, `{"error":{"message":"no retry"}}`), nil
	}))
	if _, err := governed.ChatCompletion(context.Background(), strictZDROneUseRequest()); err == nil {
		t.Fatal("expected provider error")
	}
	if _, err := governed.ChatCompletion(context.Background(), strictZDROneUseRequest()); !errors.Is(err, ErrOpenRouterStrictZDROneUseSpent) {
		t.Fatalf("second call error = %v, want spent", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("transport calls = %d, want 1", calls.Load())
	}
}

func TestOneUseStrictZDROpenRouterClient_RedirectIsNotFollowed(t *testing.T) {
	var calls atomic.Int32
	governed, _ := newStrictZDROneUseTestClient(t, ossAdmissionRoundTripper(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		resp := ossAdmissionResponse(req, http.StatusFound, `{"error":{"message":"redirect"}}`)
		resp.Header.Set("Location", "https://redirect.invalid/collect")
		return resp, nil
	}))
	if _, err := governed.ChatCompletion(context.Background(), strictZDROneUseRequest()); err == nil {
		t.Fatal("expected redirect response error")
	}
	if calls.Load() != 1 {
		t.Fatalf("transport calls = %d, want one source request", calls.Load())
	}
}

func TestOneUseStrictZDROpenRouterClient_SealedConstruction(t *testing.T) {
	if _, err := NewOneUseStrictZDROpenRouterClient("", defaultBaseURL, "qwen/qwen3.7-flash", strictZDROneUseBinding()); err == nil {
		t.Fatal("empty credential was accepted")
	}
	if _, err := NewOneUseStrictZDROpenRouterClient("test-key", "https://proxy.invalid/v1", "qwen/qwen3.7-flash", strictZDROneUseBinding()); err == nil {
		t.Fatal("non-OpenRouter endpoint was accepted")
	}
	if _, err := NewOneUseStrictZDROpenRouterClient("test-key", defaultBaseURL, "unqualified", strictZDROneUseBinding()); err == nil {
		t.Fatal("noncanonical model was accepted")
	}
	if _, err := NewOneUseStrictZDROpenRouterClient("test-key", defaultBaseURL, "qwen/qwen3.7-flash", [sha256.Size]byte{}); err == nil {
		t.Fatal("empty context binding was accepted")
	}
	governed, err := NewOneUseStrictZDROpenRouterClient("test-key", defaultBaseURL, "qwen/qwen3.7-flash", strictZDROneUseBinding())
	if err != nil {
		t.Fatalf("valid construction: %v", err)
	}
	t.Cleanup(func() { _ = governed.Close() })
	governed.contextBinding = sha256.Sum256([]byte("mutated"))
	if _, err := governed.ChatCompletion(context.Background(), strictZDROneUseRequest()); err == nil {
		t.Fatal("mutated context binding was accepted")
	}
}
