package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/transparency"
)

type strictZDRCaptureClient struct {
	requests       []model.ChatRequest
	streamRequests []model.ChatRequest
}

func (c *strictZDRCaptureClient) ChatCompletion(_ context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	c.requests = append(c.requests, req)
	return &model.ChatResponse{}, nil
}

func (c *strictZDRCaptureClient) ChatCompletionStream(_ context.Context, req model.ChatRequest) (<-chan model.StreamChunk, <-chan error) {
	c.streamRequests = append(c.streamRequests, req)
	chunks := make(chan model.StreamChunk)
	close(chunks)
	errs := make(chan error)
	close(errs)
	return chunks, errs
}

func (c *strictZDRCaptureClient) GetContextLength(modelID string) (int, error) {
	if modelID != "qwen/qwen3.7-flash" {
		return 0, nil
	}
	return 131072, nil
}

func TestStrictZDROneshotClient_ProjectsExactProviderPolicy(t *testing.T) {
	inner := &strictZDRCaptureClient{}
	client, err := strictZDROneshotClientForProvider(inner, "qwen/qwen3.7-flash", "openrouter")
	if err != nil {
		t.Fatalf("strictZDROneshotClientForProvider: %v", err)
	}
	originalProvider := map[string]any{
		"allow_fallbacks": true,
		"zdr":             false,
		"data_collection": "deny",
		"only":            []string{"Qwen"},
	}
	req := model.ChatRequest{
		Model:               "qwen/qwen3.7-flash",
		Models:              []string{"other/fallback"},
		Provider:            originalProvider,
		OpenRouterRetention: model.OpenRouterRetentionNonZDR,
	}
	if _, err := client.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if len(inner.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(inner.requests))
	}
	got := inner.requests[0]
	if got.Model != "qwen/qwen3.7-flash" || len(got.Models) != 0 {
		t.Fatalf("route = model %q fallbacks %#v, want exact model only", got.Model, got.Models)
	}
	if got.OpenRouterRetention != model.OpenRouterRetentionZDR {
		t.Fatalf("retention = %q, want zdr", got.OpenRouterRetention)
	}
	if zdr, ok := got.Provider["zdr"].(bool); !ok || !zdr {
		t.Fatalf("zdr = %#v, want true", got.Provider["zdr"])
	}
	if allow, ok := got.Provider["allow_fallbacks"].(bool); !ok || allow {
		t.Fatalf("allow_fallbacks = %#v, want false", got.Provider["allow_fallbacks"])
	}
	if _, ok := got.Provider["data_collection"]; ok {
		t.Fatal("strict ZDR request retained data_collection")
	}
	if _, ok := got.Provider["only"]; !ok {
		t.Fatal("unrelated provider preference was dropped")
	}
	if originalProvider["allow_fallbacks"] != true || originalProvider["zdr"] != false || originalProvider["data_collection"] != "deny" {
		t.Fatalf("caller provider map mutated: %#v", originalProvider)
	}
}

func TestStrictZDROneshotClient_RejectsModelDriftBeforeDispatch(t *testing.T) {
	inner := &strictZDRCaptureClient{}
	client, err := strictZDROneshotClientForProvider(inner, "qwen/qwen3.7-flash", "openrouter")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ChatCompletion(context.Background(), model.ChatRequest{Model: "stealth/ox-alpha"}); err == nil || !strings.Contains(err.Error(), "exact model required") {
		t.Fatalf("error = %v, want exact-model block", err)
	}
	if len(inner.requests) != 0 {
		t.Fatalf("drifted request dispatched %d times", len(inner.requests))
	}
}

func TestStrictZDROneshotClient_PreservesStreamingAndContextCapabilities(t *testing.T) {
	inner := &strictZDRCaptureClient{}
	client, err := strictZDROneshotClientForProvider(inner, "qwen/qwen3.7-flash", "openrouter")
	if err != nil {
		t.Fatal(err)
	}
	stream, ok := client.(model.StreamingClient)
	if !ok {
		t.Fatalf("client type %T lost streaming", client)
	}
	chunks, errs := stream.ChatCompletionStream(context.Background(), model.ChatRequest{Model: "qwen/qwen3.7-flash"})
	for range chunks {
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(inner.streamRequests) != 1 || inner.streamRequests[0].Provider["zdr"] != true {
		t.Fatalf("stream requests = %#v, want one strict-ZDR request", inner.streamRequests)
	}
	window, ok := client.(model.ContextWindowProvider)
	if !ok {
		t.Fatalf("client type %T lost context-window support", client)
	}
	if got, err := window.GetContextLength("qwen/qwen3.7-flash"); err != nil || got != 131072 {
		t.Fatalf("context length = %d, %v", got, err)
	}
	if _, err := window.GetContextLength("other/model"); err == nil {
		t.Fatal("context lookup accepted model drift")
	}
}

func TestStrictZDROneshotClient_NonOpenRouterIsUnchanged(t *testing.T) {
	inner := &strictZDRCaptureClient{}
	client, err := strictZDROneshotClientForProvider(inner, "openai/gpt-test", "openai")
	if err != nil {
		t.Fatal(err)
	}
	if client != inner {
		t.Fatalf("non-OpenRouter client type = %T, want original", client)
	}
}

func TestStrictZDROneshotClient_RejectsInvalidConstruction(t *testing.T) {
	if _, err := strictZDROneshotClientForProvider(nil, "qwen/qwen3.7-flash", "openrouter"); err == nil {
		t.Fatal("nil OpenRouter client was accepted")
	}
	if _, err := strictZDROneshotClientForProvider(&strictZDRCaptureClient{}, "unqualified", "openrouter"); err == nil {
		t.Fatal("unqualified OpenRouter model was accepted")
	}
}

func TestNewOneshotToolInvoker_APIConstructsStrictZDRClient(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Models.DefaultProvider = "openrouter"
	cfg.Providers.OpenRouter.Enabled = true
	cfg.Providers.OpenRouter.APIKey = "test-key"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	invoker, err := newOneshotToolInvoker(oneshotBackendAPI, "qwen/qwen3.7-flash", cfg, mgr, transparency.ModelPricing{}, nil)
	if err != nil {
		t.Fatalf("newOneshotToolInvoker: %v", err)
	}
	if invoker == nil {
		t.Fatal("API backend returned nil invoker")
	}
}

func TestStrictZDROneshotClient_ManagerEmitsStrictZDRWirePolicy(t *testing.T) {
	requests := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		requests <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	t.Cleanup(server.Close)

	cfg := config.DefaultConfig()
	cfg.Models.DefaultProvider = "openrouter"
	cfg.Models.FallbackChains["qwen/qwen3.7-flash"] = []string{"other/fallback"}
	cfg.Providers.OpenRouter.Enabled = true
	cfg.Providers.OpenRouter.APIKey = "test-key"
	cfg.Providers.OpenRouter.BaseURL = server.URL
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	client, err := strictZDROneshotClientForProvider(mgr, "qwen/qwen3.7-flash", "openrouter")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ChatCompletion(context.Background(), model.ChatRequest{
		Model:    "qwen/qwen3.7-flash",
		Models:   []string{"caller/fallback"},
		Provider: map[string]any{"zdr": false, "allow_fallbacks": true, "data_collection": "deny"},
	}); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	body := <-requests
	if body["model"] != "qwen/qwen3.7-flash" {
		t.Fatalf("wire model = %#v", body["model"])
	}
	if _, ok := body["models"]; ok {
		t.Fatalf("wire included fallback models: %#v", body["models"])
	}
	provider, ok := body["provider"].(map[string]any)
	if !ok {
		t.Fatalf("wire provider = %#v", body["provider"])
	}
	if provider["zdr"] != true || provider["allow_fallbacks"] != false {
		t.Fatalf("wire provider = %#v, want strict ZDR exact route", provider)
	}
	if _, ok := provider["data_collection"]; ok {
		t.Fatalf("wire provider retained data_collection: %#v", provider)
	}
}
