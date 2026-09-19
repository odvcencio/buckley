package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/launchcontract"
)

type launchCatalogProvider struct {
	*stubProvider
	refreshed    ModelCatalog
	refreshCount int
	sourceURL    string
	observedAt   time.Time
}

func (p *launchCatalogProvider) RefreshCatalog() (*ModelCatalog, error) {
	p.refreshCount++
	copy := p.refreshed
	return &copy, nil
}

func (p *launchCatalogProvider) refreshOfficialOpenRouterCatalog(ctx context.Context) (*officialCatalogObservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.refreshCount++
	if p.sourceURL != OpenRouterCatalogSourceURL {
		return nil, errors.New("untrusted source")
	}
	copy := p.refreshed
	digests := make([]string, len(copy.Data))
	for index, info := range copy.Data {
		sum := sha256.Sum256([]byte(info.ID))
		digests[index] = hex.EncodeToString(sum[:])
	}
	observedAt := p.observedAt
	if observedAt.IsZero() {
		observedAt = time.Date(2026, 8, 21, 18, 0, 0, 123_000_000, time.UTC)
	}
	return &officialCatalogObservation{
		Catalog:           &copy,
		ObservedAt:        observedAt,
		ResponseDigest:    strings.Repeat("a", 64),
		ModelObjectDigest: digests,
	}, nil
}

func (p *launchCatalogProvider) CatalogSourceURL() string  { return p.sourceURL }
func (*launchCatalogProvider) openRouterCatalogAuthority() {}

type untrustedCatalogProvider struct {
	*stubProvider
	refreshed ModelCatalog
	sourceURL string
}

func (p *untrustedCatalogProvider) RefreshCatalog() (*ModelCatalog, error) {
	copy := p.refreshed
	return &copy, nil
}

func (p *untrustedCatalogProvider) CatalogSourceURL() string { return p.sourceURL }

type launchContinuationProvider struct {
	*stubProvider
	continuationRequests []ContinuationRequest
	continuationErrors   []error
}

func (p *launchContinuationProvider) SupportsContinuation(string) bool { return true }

func (p *launchContinuationProvider) ChatCompletionWithContinuation(_ context.Context, req ContinuationRequest) (*ContinuationResponse, error) {
	p.continuationRequests = append(p.continuationRequests, req)
	if len(p.continuationErrors) > 0 {
		err := p.continuationErrors[0]
		p.continuationErrors = p.continuationErrors[1:]
		if err != nil {
			return nil, err
		}
	}
	return &ContinuationResponse{Response: &ChatResponse{
		Model:   req.Request.Model,
		Choices: []Choice{{Message: Message{Content: "ok"}, FinishReason: "stop"}},
	}}, nil
}

func TestApplyOpenRouterFreeLaunchRequest_ExactWireContract(t *testing.T) {
	request, err := ApplyOpenRouterFreeLaunchRequest(ChatRequest{
		Model:               OpenRouterLaunchModel,
		Models:              []string{"paid/fallback"},
		Provider:            map[string]any{"allow_fallbacks": true, "zdr": true},
		MaxCompletionTokens: 999,
	}, CanonicalOpenRouterFreeLaunchContract())
	if err != nil {
		t.Fatal(err)
	}
	if request.Model != OpenRouterLaunchModel || len(request.Models) != 0 || request.MaxTokens != OpenRouterLaunchMaxOutput || request.MaxCompletionTokens != 0 || request.RetryMode != RequestRetrySingleAttempt || request.Reasoning == nil || request.Reasoning.Effort != OpenRouterLaunchReasoning {
		t.Fatalf("launch request = %+v", request)
	}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if _, leaked := wire["retry_mode"]; leaked {
		t.Fatal("internal retry mode entered provider JSON")
	}
	provider, ok := wire["provider"].(map[string]any)
	if !ok || len(provider) != 5 || provider["allow_fallbacks"] != false || provider["require_parameters"] != true || provider["data_collection"] != "deny" || provider["zdr"] != false {
		t.Fatalf("provider wire = %#v", wire["provider"])
	}
	maxPrice, ok := provider["max_price"].(map[string]any)
	if !ok || len(maxPrice) != 4 {
		t.Fatalf("max_price = %#v", provider["max_price"])
	}
	for _, dimension := range []string{"prompt", "completion", "request", "image"} {
		if value, ok := maxPrice[dimension].(float64); !ok || value != 0 {
			t.Fatalf("max_price[%s] = %#v", dimension, maxPrice[dimension])
		}
	}
	if _, err := ApplyOpenRouterFreeLaunchRequest(ChatRequest{Model: "ox-alpha"}, CanonicalOpenRouterFreeLaunchContract()); err == nil {
		t.Fatal("conflicting model accepted")
	}
	changed := CanonicalOpenRouterFreeLaunchContract()
	changed.ZDR = true
	if _, err := ApplyOpenRouterFreeLaunchRequest(ChatRequest{}, changed); err == nil {
		t.Fatal("noncanonical contract accepted")
	}
}

func TestObservePrice_BypassesCacheAndBindsOfficialExactModel(t *testing.T) {
	now := time.Date(2026, 8, 21, 18, 0, 0, 123_000_000, time.UTC)
	provider := &launchCatalogProvider{
		stubProvider: &stubProvider{id: OpenRouterLaunchProvider, catalog: ModelCatalog{Data: []ModelInfo{{ID: OpenRouterLaunchModel, RawPricing: map[string]string{"prompt": "9", "completion": "9"}}}}},
		refreshed: ModelCatalog{Data: []ModelInfo{{
			ID: OpenRouterLaunchModel,
			RawPricing: map[string]string{
				"completion": "0.0", "prompt": "0", "request": "0e+9", "image": "-0",
				"web_search": "0", "internal_reasoning": "0", "input_cache_read": "0", "input_cache_write": "0",
			},
		}}},
		sourceURL: OpenRouterCatalogSourceURL, observedAt: now,
	}
	manager := launchTestManager(provider)
	manager.launchAdmissionNow = func() time.Time { return now }
	proof, err := manager.VerifyLaunchPrice(context.Background(), launchPriceProfile(t))
	if err != nil {
		t.Fatal(err)
	}
	evidence := proof.Snapshot()
	if provider.refreshCount != 1 || evidence.CanonicalSlug != OpenRouterLaunchModel || evidence.SourceURL != OpenRouterCatalogSourceURL || !evidence.ExpiresAt.Equal(now.Add(MaxFreePriceEvidenceTTL)) || len(evidence.Digest) != 64 {
		t.Fatalf("evidence = %+v refreshes=%d", evidence, provider.refreshCount)
	}
	if err := evidence.ValidateAt(now); err != nil {
		t.Fatal(err)
	}

	reordered := *provider
	reordered.stubProvider = &stubProvider{id: OpenRouterLaunchProvider}
	reordered.refreshCount = 0
	reordered.refreshed.Data[0].RawPricing = map[string]string{
		"input_cache_write": "0", "input_cache_read": "0", "internal_reasoning": "0", "web_search": "0",
		"image": "-0", "request": "0e+9", "prompt": "0", "completion": "0.0",
	}
	secondManager := launchTestManager(&reordered)
	secondManager.launchAdmissionNow = func() time.Time { return now }
	second, err := secondManager.VerifyLaunchPrice(context.Background(), launchPriceProfile(t))
	if err != nil || !reflect.DeepEqual(evidence, second.Snapshot()) {
		t.Fatalf("map-order evidence = %+v, %v", second.Snapshot(), err)
	}
}

func TestObservePrice_RejectionMatrix(t *testing.T) {
	now := time.Date(2026, 8, 21, 18, 0, 0, 0, time.UTC)
	untrusted := &untrustedCatalogProvider{
		stubProvider: &stubProvider{id: OpenRouterLaunchProvider},
		refreshed:    ModelCatalog{Data: []ModelInfo{{ID: OpenRouterLaunchModel, RawPricing: map[string]string{"prompt": "0", "completion": "0"}}}},
		sourceURL:    OpenRouterCatalogSourceURL,
	}
	if _, err := launchTestManager(untrusted).VerifyLaunchPrice(context.Background(), launchPriceProfile(t)); err == nil {
		t.Fatal("custom provider spoofing the official source accepted")
	}
	tests := []struct {
		name      string
		sourceURL string
		models    []ModelInfo
		clock     time.Time
		observed  time.Time
	}{
		{name: "custom source", sourceURL: "https://example.invalid/models", models: []ModelInfo{{ID: OpenRouterLaunchModel, RawPricing: launchZeroPricing()}}, clock: now, observed: now},
		{name: "alias", sourceURL: OpenRouterCatalogSourceURL, models: []ModelInfo{{ID: "ox-alpha", RawPricing: launchZeroPricing()}}, clock: now, observed: now},
		{name: "duplicate exact", sourceURL: OpenRouterCatalogSourceURL, models: []ModelInfo{{ID: OpenRouterLaunchModel, RawPricing: launchZeroPricing()}, {ID: OpenRouterLaunchModel, RawPricing: launchZeroPricing()}}, clock: now, observed: now},
		{name: "missing prompt", sourceURL: OpenRouterCatalogSourceURL, models: []ModelInfo{{ID: OpenRouterLaunchModel, RawPricing: map[string]string{"completion": "0", "request": "0"}}}, clock: now, observed: now},
		{name: "null price", sourceURL: OpenRouterCatalogSourceURL, models: []ModelInfo{{ID: OpenRouterLaunchModel, RawPricing: map[string]string{"prompt": "null", "completion": "0"}}}, clock: now, observed: now},
		{name: "unknown charge", sourceURL: OpenRouterCatalogSourceURL, models: []ModelInfo{{ID: OpenRouterLaunchModel, RawPricing: map[string]string{"prompt": "0", "completion": "0", "future": "0"}}}, clock: now, observed: now},
		{name: "positive charge", sourceURL: OpenRouterCatalogSourceURL, models: []ModelInfo{{ID: OpenRouterLaunchModel, RawPricing: map[string]string{"prompt": "0", "completion": "1e+2"}}}, clock: now, observed: now},
		{name: "underflow", sourceURL: OpenRouterCatalogSourceURL, models: []ModelInfo{{ID: OpenRouterLaunchModel, RawPricing: map[string]string{"prompt": "0", "completion": "1e-999"}}}, clock: now, observed: now},
		{name: "negative underflow", sourceURL: OpenRouterCatalogSourceURL, models: []ModelInfo{{ID: OpenRouterLaunchModel, RawPricing: map[string]string{"prompt": "0", "completion": "-1e-999"}}}, clock: now, observed: now},
		{name: "nan", sourceURL: OpenRouterCatalogSourceURL, models: []ModelInfo{{ID: OpenRouterLaunchModel, RawPricing: map[string]string{"prompt": "0", "completion": "NaN"}}}, clock: now, observed: now},
		{name: "infinity", sourceURL: OpenRouterCatalogSourceURL, models: []ModelInfo{{ID: OpenRouterLaunchModel, RawPricing: map[string]string{"prompt": "0", "completion": "Inf"}}}, clock: now, observed: now},
		{name: "non UTC clock", sourceURL: OpenRouterCatalogSourceURL, models: []ModelInfo{{ID: OpenRouterLaunchModel, RawPricing: launchZeroPricing()}}, clock: now.In(time.FixedZone("offset", 3600)), observed: now},
		{name: "non UTC observation", sourceURL: OpenRouterCatalogSourceURL, models: []ModelInfo{{ID: OpenRouterLaunchModel, RawPricing: launchZeroPricing()}}, clock: now, observed: now.In(time.FixedZone("offset", 3600))},
		{name: "future observation", sourceURL: OpenRouterCatalogSourceURL, models: []ModelInfo{{ID: OpenRouterLaunchModel, RawPricing: launchZeroPricing()}}, clock: now, observed: now.Add(time.Second)},
		{name: "stale observation", sourceURL: OpenRouterCatalogSourceURL, models: []ModelInfo{{ID: OpenRouterLaunchModel, RawPricing: launchZeroPricing()}}, clock: now, observed: now.Add(-MaxFreePriceEvidenceTTL - time.Nanosecond)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := &launchCatalogProvider{stubProvider: &stubProvider{id: OpenRouterLaunchProvider}, refreshed: ModelCatalog{Data: test.models}, sourceURL: test.sourceURL, observedAt: test.observed}
			manager := launchTestManager(provider)
			manager.launchAdmissionNow = func() time.Time { return test.clock }
			if _, err := manager.VerifyLaunchPrice(context.Background(), launchPriceProfile(t)); err == nil {
				t.Fatal("unsafe catalog evidence accepted")
			}
		})
	}
	var info ModelInfo
	if err := json.Unmarshal([]byte(`{"id":"stealth/ox-alpha","pricing":{"prompt":"0","prompt":"0","completion":"0"}}`), &info); err != nil {
		t.Fatalf("ordinary duplicate pricing decode failed: %v", err)
	}
	for _, document := range []string{
		`{"id":"stealth/ox-alpha","id":"paid/other","pricing":null}`,
		`{"id":"stealth/ox-alpha","pricing":null,"pricing":{"prompt":"0","completion":"0"}}`,
	} {
		if err := json.Unmarshal([]byte(document), &info); err == nil {
			t.Fatalf("ordinary duplicate model field accepted: %s", document)
		}
	}
	var catalog ModelCatalog
	if err := json.Unmarshal([]byte(`{"data":[],"data":[{"id":"stealth/ox-alpha"}]}`), &catalog); err == nil {
		t.Fatal("ordinary duplicate catalog field accepted")
	}
	if err := json.Unmarshal([]byte(`{"id":"stealth/ox-alpha","pricing":{"prompt":false,"completion":"0"}}`), &info); err != nil {
		t.Fatalf("ordinary nonnumeric pricing decode failed: %v", err)
	}
	if err := json.Unmarshal([]byte(`{"id":"stealth/ox-alpha","pricing":[]}`), &info); err != nil {
		t.Fatalf("ordinary nonobject pricing decode failed: %v", err)
	}
	for _, rawPricing := range []string{
		`null`,
		`{"prompt":{"future":true},"completion":"0"}`,
		`{"prompt":"` + strings.Repeat("0", MaxCatalogPricingBytes) + `","completion":"0"}`,
	} {
		if err := json.Unmarshal([]byte(`{"id":"stealth/ox-alpha","pricing":`+rawPricing+`}`), &info); err != nil {
			t.Fatalf("ordinary compatibility pricing %s failed: %v", rawPricing[:min(len(rawPricing), 32)], err)
		}
	}
	if _, _, err := decodeOfficialOpenRouterCatalog([]byte(`{"data":[{"id":"stealth/ox-alpha","id":"stealth/ox-alpha","pricing":{"prompt":"0","completion":"0"}}]}`)); err == nil {
		t.Fatal("strict catalog duplicate model key accepted")
	}
}

func TestObservePrice_CancellationIsPropagated(t *testing.T) {
	provider := &launchCatalogProvider{
		stubProvider: &stubProvider{id: OpenRouterLaunchProvider},
		refreshed:    ModelCatalog{Data: []ModelInfo{{ID: OpenRouterLaunchModel}}},
		sourceURL:    OpenRouterCatalogSourceURL,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := launchTestManager(provider).VerifyLaunchPrice(ctx, launchPriceProfile(t)); err == nil {
		t.Fatal("canceled catalog observation accepted")
	}
	if provider.refreshCount != 0 {
		t.Fatalf("refreshes = %d, want no refresh after cancellation", provider.refreshCount)
	}
}

func launchPriceProfile(t *testing.T) launchcontract.ProfileDescriptor {
	t.Helper()
	profile, err := launchcontract.ResolveProfile("gsxmail")
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func TestOfficialCatalogDecoder_RejectsBoundedOversizeFields(t *testing.T) {
	large := strings.Repeat("x", MaxOfficialCatalogStringBytes+1)
	body := []byte(`{"data":[{"id":"stealth/ox-alpha","name":"` + large + `","pricing":{"prompt":"0","completion":"0"}}]}`)
	if _, _, err := decodeOfficialOpenRouterCatalog(body); err == nil {
		t.Fatal("oversized official catalog field accepted")
	}
}

func TestClient_RequestRetrySingleAttempt_ChatAndStream(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "chat"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			attempts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				attempts++
				http.Error(w, `{"error":"temporary"}`, http.StatusInternalServerError)
			}))
			defer server.Close()
			client := NewClient("test-key", server.URL)
			client.SetRetryConfig(RetryConfig{MaxRetries: 2, MaxRateLimitRetries: 2, InitialInterval: time.Millisecond, MaxInterval: time.Millisecond, Multiplier: 1})
			request, err := ApplyOpenRouterFreeLaunchRequest(ChatRequest{Messages: []Message{{Role: "user", Content: "harmless"}}}, CanonicalOpenRouterFreeLaunchContract())
			if err != nil {
				t.Fatal(err)
			}
			if stream {
				chunks, errs := client.ChatCompletionStream(context.Background(), request)
				for range chunks {
				}
				for range errs {
				}
			} else {
				_, _ = client.ChatCompletion(context.Background(), request)
			}
			if attempts != 1 {
				t.Fatalf("single-attempt calls = %d", attempts)
			}
			attempts = 0
			request.RetryMode = RequestRetryDefault
			if stream {
				chunks, errs := client.ChatCompletionStream(context.Background(), request)
				for range chunks {
				}
				for range errs {
				}
			} else {
				_, _ = client.ChatCompletion(context.Background(), request)
			}
			if attempts != 3 {
				t.Fatalf("ordinary retry calls = %d, want 3", attempts)
			}
			attempts = 0
			request.RetryMode = RequestRetryMode("unknown_future_mode")
			if stream {
				chunks, errs := client.ChatCompletionStream(context.Background(), request)
				for range chunks {
				}
				for range errs {
				}
			} else {
				_, _ = client.ChatCompletion(context.Background(), request)
			}
			if attempts != 1 {
				t.Fatalf("unknown retry mode calls = %d, want fail-closed single attempt", attempts)
			}
		})
	}
}

func TestManager_RequestRetrySingleAttempt_AllAffordabilityPaths(t *testing.T) {
	affordable := &APIError{StatusCode: http.StatusPaymentRequired, Message: "You requested up to 32768 tokens, but can only afford 4156."}

	chatProvider := &stubProvider{id: OpenRouterLaunchProvider, errors: []error{affordable, nil}}
	request, err := ApplyOpenRouterFreeLaunchRequest(ChatRequest{}, CanonicalOpenRouterFreeLaunchContract())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := launchTestManager(chatProvider).ChatCompletion(context.Background(), request); !errors.Is(err, affordable) || len(chatProvider.requests) != 1 {
		t.Fatalf("chat err=%v requests=%d", err, len(chatProvider.requests))
	}

	streamProvider := &stubProvider{id: OpenRouterLaunchProvider, streamPlans: []stubStreamPlan{{err: affordable}, {chunks: []StreamChunk{{Choices: []StreamChoice{{Delta: MessageDelta{Content: "should not run"}}}}}}}}
	chunks, errs := launchTestManager(streamProvider).ChatCompletionStream(context.Background(), request)
	for range chunks {
	}
	var streamErr error
	for err := range errs {
		streamErr = err
	}
	if !errors.Is(streamErr, affordable) || len(streamProvider.streamRequests) != 1 {
		t.Fatalf("stream err=%v requests=%d", streamErr, len(streamProvider.streamRequests))
	}

	continuationProvider := &launchContinuationProvider{stubProvider: &stubProvider{id: OpenRouterLaunchProvider}, continuationErrors: []error{affordable, nil}}
	if _, err := launchTestManager(continuationProvider).ChatCompletionWithContinuation(context.Background(), ContinuationRequest{Request: request}); !errors.Is(err, affordable) || len(continuationProvider.continuationRequests) != 1 {
		t.Fatalf("continuation err=%v requests=%d", err, len(continuationProvider.continuationRequests))
	}

	ordinary := request
	ordinary.RetryMode = RequestRetryDefault
	ordinaryChat := &stubProvider{id: OpenRouterLaunchProvider, errors: []error{affordable, nil}}
	if response, err := launchTestManager(ordinaryChat).ChatCompletion(context.Background(), ordinary); err != nil || response == nil || len(ordinaryChat.requests) != 2 {
		t.Fatalf("ordinary chat response=%v err=%v requests=%d", response, err, len(ordinaryChat.requests))
	}
	ordinaryStream := &stubProvider{id: OpenRouterLaunchProvider, streamPlans: []stubStreamPlan{{err: affordable}, {chunks: []StreamChunk{{Choices: []StreamChoice{{Delta: MessageDelta{Content: "ok"}}}}}}}}
	chunks, errs = launchTestManager(ordinaryStream).ChatCompletionStream(context.Background(), ordinary)
	for range chunks {
	}
	streamErr = nil
	for err := range errs {
		streamErr = err
	}
	if streamErr != nil || len(ordinaryStream.streamRequests) != 2 {
		t.Fatalf("ordinary stream err=%v requests=%d", streamErr, len(ordinaryStream.streamRequests))
	}
	ordinaryContinuation := &launchContinuationProvider{stubProvider: &stubProvider{id: OpenRouterLaunchProvider}, continuationErrors: []error{affordable, nil}}
	if response, err := launchTestManager(ordinaryContinuation).ChatCompletionWithContinuation(context.Background(), ContinuationRequest{Request: ordinary}); err != nil || response == nil || len(ordinaryContinuation.continuationRequests) != 2 {
		t.Fatalf("ordinary continuation response=%v err=%v requests=%d", response, err, len(ordinaryContinuation.continuationRequests))
	}
}

func TestManager_RequestRetrySingleAttempt_RejectsProviderRouteDrift(t *testing.T) {
	request, err := ApplyOpenRouterFreeLaunchRequest(ChatRequest{}, CanonicalOpenRouterFreeLaunchContract())
	if err != nil {
		t.Fatal(err)
	}
	provider := &stubProvider{id: "openai", catalog: ModelCatalog{Data: []ModelInfo{{ID: OpenRouterLaunchModel}}}}
	manager := launchTestManager(provider)
	if _, err := manager.ChatCompletion(context.Background(), request); err == nil {
		t.Fatal("single-attempt launch dispatched through a non-OpenRouter provider")
	}
	if len(provider.requests) != 0 {
		t.Fatalf("provider requests = %d, want zero", len(provider.requests))
	}
	chunks, errs := manager.ChatCompletionStream(context.Background(), request)
	for range chunks {
	}
	if err := <-errs; err == nil {
		t.Fatal("stream launch route drift was accepted")
	}
	continuation := &launchContinuationProvider{stubProvider: provider}
	manager = launchTestManager(continuation)
	if _, err := manager.ChatCompletionWithContinuation(context.Background(), ContinuationRequest{Request: request}); err == nil {
		t.Fatal("continuation launch route drift was accepted")
	}
	if len(continuation.continuationRequests) != 0 {
		t.Fatalf("continuation requests = %d, want zero", len(continuation.continuationRequests))
	}
}

func launchTestManager(provider Provider) *Manager {
	return &Manager{
		config: &config.Config{}, providers: map[string]Provider{provider.ID(): provider},
		providerOrder: []string{provider.ID()}, catalog: map[string]ModelInfo{},
		providerModels: map[string][]string{provider.ID(): {OpenRouterLaunchModel}},
		modelProviders: map[string]string{OpenRouterLaunchModel: provider.ID()},
	}
}

func launchZeroPricing() map[string]string {
	return map[string]string{
		"prompt": "0", "completion": "0", "request": "0", "image": "0",
		"web_search": "0", "internal_reasoning": "0", "input_cache_read": "0", "input_cache_write": "0",
	}
}
