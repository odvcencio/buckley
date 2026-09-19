package model

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
)

func TestExecutionIdentity_JSONUsesSnakeCase(t *testing.T) {
	resp := ChatResponse{
		ExecutionIdentity: &ExecutionIdentity{
			RequestedModel: "requested/model",
			SelectedModel:  "selected/model",
			ProviderID:     "provider",
			ResponseModel:  "reported-model",
			ResponseID:     "reported-id",
			Conflicted:     true,
		},
	}

	encoded, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	text := string(encoded)
	for _, want := range []string{"requested_model", "selected_model", "provider_id", "response_model", "response_id", "conflicted"} {
		if !strings.Contains(text, want) {
			t.Fatalf("encoded response missing %q: %s", want, text)
		}
	}
}

func TestManagerChatCompletion_StampsAuthoritativeRouteAndObservedResponse(t *testing.T) {
	prov := &stubProvider{
		id:      "selected",
		catalog: ModelCatalog{Data: []ModelInfo{{ID: "selected/model-v1"}}},
		response: &ChatResponse{
			ID:    "provider-response-id",
			Model: "provider-reported-model",
			ExecutionIdentity: &ExecutionIdentity{
				RequestedModel: "forged/requested",
				SelectedModel:  "forged/selected",
				ProviderID:     "forged-provider",
				ResponseModel:  "provider-identity-model",
				ResponseID:     "provider-identity-id",
			},
			Choices: []Choice{{Message: Message{Content: "ok"}, FinishReason: "stop"}},
		},
	}
	mgr := identityTestManager(prov)
	mgr.routingHooks.Register(func(decision *RoutingDecision) *RoutingDecision {
		decision.SelectedModel = "selected/model-v1"
		return decision
	})

	resp, err := mgr.ChatCompletion(context.Background(), ChatRequest{Model: "requested/alias"})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	identity := resp.ExecutionIdentity
	if identity == nil {
		t.Fatal("missing execution identity")
	}
	if identity.RequestedModel != "requested/alias" || identity.SelectedModel != "selected/model-v1" || identity.ProviderID != "selected" {
		t.Fatalf("route identity = %+v, want manager-authoritative route", identity)
	}
	if identity.ResponseModel != "provider-reported-model" || identity.ResponseID != "provider-response-id" {
		t.Fatalf("response identity = %+v, want top-level provider-observed fields to outrank embedded identity claims", identity)
	}
	if prov.lastRequest.Model != "model-v1" {
		t.Fatalf("provider request model = %q, want normalized selected model", prov.lastRequest.Model)
	}
}

func TestManagerChatCompletion_ResponseWithErrorKeepsIdentity(t *testing.T) {
	providerErr := errors.New("provider failed after response")
	prov := &stubProvider{
		id:      "selected",
		catalog: ModelCatalog{Data: []ModelInfo{{ID: "selected/model-v1"}}},
		response: &ChatResponse{
			ID:      "provider-response-id",
			Model:   "provider-reported-model",
			Choices: []Choice{{Message: Message{Content: "partial"}, FinishReason: "stop"}},
		},
		responseErr: providerErr,
	}
	mgr := identityTestManager(prov)

	resp, err := mgr.ChatCompletion(context.Background(), ChatRequest{Model: "selected/model-v1"})
	if !errors.Is(err, providerErr) {
		t.Fatalf("ChatCompletion error = %v, want provider error", err)
	}
	if resp == nil || resp.ExecutionIdentity == nil {
		t.Fatalf("response = %#v, want partial response with identity", resp)
	}
	if got := *resp.ExecutionIdentity; got.RequestedModel != "selected/model-v1" || got.SelectedModel != "selected/model-v1" || got.ProviderID != "selected" || got.ResponseModel != "provider-reported-model" || got.ResponseID != "provider-response-id" {
		t.Fatalf("identity = %+v", got)
	}
}

func TestManagerChatCompletionStream_StampsChunksAndPreservesNoRetryAfterChunk(t *testing.T) {
	providerErr := errors.New("stream failed after chunk")
	prov := &stubProvider{
		id:      "openrouter",
		catalog: ModelCatalog{Data: []ModelInfo{{ID: "selected/model-v1"}}},
		streamPlans: []stubStreamPlan{{
			chunks: []StreamChunk{{
				ID:    "chunk-id",
				Model: "provider-stream-model",
				ExecutionIdentity: &ExecutionIdentity{
					RequestedModel: "forged/requested",
					SelectedModel:  "forged/selected",
					ProviderID:     "forged-provider",
				},
				Choices: []StreamChoice{{Delta: MessageDelta{Content: "partial"}}},
			}},
			err: providerErr,
		}},
	}
	mgr := identityTestManager(prov)

	chunks, errs := mgr.ChatCompletionStream(context.Background(), ChatRequest{Model: "selected/model-v1"})
	var got []StreamChunk
	for chunk := range chunks {
		got = append(got, chunk)
	}
	var gotErr error
	for err := range errs {
		gotErr = err
	}
	if !errors.Is(gotErr, providerErr) {
		t.Fatalf("stream error = %v, want provider error", gotErr)
	}
	if len(got) != 1 || got[0].ExecutionIdentity == nil {
		t.Fatalf("chunks = %#v, want stamped chunk", got)
	}
	identity := got[0].ExecutionIdentity
	if identity.RequestedModel != "selected/model-v1" || identity.SelectedModel != "selected/model-v1" || identity.ProviderID != "openrouter" {
		t.Fatalf("route identity = %+v, want manager-authoritative route", identity)
	}
	if identity.ResponseModel != "provider-stream-model" || identity.ResponseID != "chunk-id" {
		t.Fatalf("response identity = %+v", identity)
	}
	if len(prov.streamRequests) != 1 {
		t.Fatalf("stream requests = %d, want no retry after chunk", len(prov.streamRequests))
	}
}

func TestManagerChatCompletionStream_NonOpenRouterStampsRoute(t *testing.T) {
	prov := &stubProvider{
		id:      "selected",
		catalog: ModelCatalog{Data: []ModelInfo{{ID: "selected/model-v1"}}},
		streamPlans: []stubStreamPlan{{
			chunks: []StreamChunk{{ID: "chunk-id", Model: "reported-model", Choices: []StreamChoice{{Delta: MessageDelta{Content: "ok"}}}}},
		}},
	}
	mgr := identityTestManager(prov)

	chunks, errs := mgr.ChatCompletionStream(context.Background(), ChatRequest{Model: "selected/model-v1"})
	var chunk StreamChunk
	for c := range chunks {
		chunk = c
	}
	for err := range errs {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
	}
	if chunk.ExecutionIdentity == nil {
		t.Fatal("missing chunk identity")
	}
	if got := *chunk.ExecutionIdentity; got.RequestedModel != "selected/model-v1" || got.SelectedModel != "selected/model-v1" || got.ProviderID != "selected" || got.ResponseModel != "reported-model" || got.ResponseID != "chunk-id" {
		t.Fatalf("identity = %+v", got)
	}
}

func TestStampStreamChunks_NilSuccessEndsErrorLaneAndDrainsTail(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	chunks := make(chan StreamChunk)
	errs := make(chan error)
	nilDelivered := make(chan struct{})
	go func() {
		errs <- nil
		close(nilDelivered)
	}()

	out, errOut := stampStreamChunks(ctx, chunks, errs, "requested/model", "selected/model", "selected")
	mustReceiveSignal(t, nilDelivered, "nil success delivered")

	go func() {
		chunks <- StreamChunk{ID: "tail-id", Model: "tail-model", Choices: []StreamChoice{{Delta: MessageDelta{Content: "tail"}}}}
		close(chunks)
	}()

	chunk := mustReceiveChunk(t, out, "tail chunk")
	if chunk.ExecutionIdentity == nil {
		t.Fatal("tail chunk missing execution identity")
	}
	if got := *chunk.ExecutionIdentity; got.RequestedModel != "requested/model" || got.SelectedModel != "selected/model" || got.ProviderID != "selected" || got.ResponseModel != "tail-model" || got.ResponseID != "tail-id" {
		t.Fatalf("tail identity = %+v", got)
	}
	mustCloseWithoutChunk(t, out, "chunk lane")
	mustCloseWithoutError(t, errOut, "error lane")
}

func TestStampStreamChunks_CancelPropagatesContextError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	chunks := make(chan StreamChunk)
	errs := make(chan error)

	out, errOut := stampStreamChunks(ctx, chunks, errs, "requested/model", "selected/model", "selected")
	cancel()

	err := mustReceiveError(t, errOut, "cancellation error")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	mustCloseWithoutChunk(t, out, "chunk lane")
	mustCloseWithoutError(t, errOut, "error lane after cancellation")
}

func TestStampStreamChunks_TerminalErrorPreservesAvailablePrefix(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	providerErr := errors.New("provider stopped")
	chunks := make(chan StreamChunk, 1)
	chunks <- StreamChunk{ID: "prefix-id", Model: "prefix-model", Choices: []StreamChoice{{Delta: MessageDelta{Content: "prefix"}}}}
	errs := make(chan error, 1)
	errs <- providerErr

	out, errOut := stampStreamChunks(ctx, chunks, errs, "requested/model", "selected/model", "selected")

	chunk := mustReceiveChunk(t, out, "available prefix chunk")
	if chunk.ExecutionIdentity == nil || chunk.ExecutionIdentity.ResponseID != "prefix-id" {
		t.Fatalf("prefix identity = %+v", chunk.ExecutionIdentity)
	}
	err := mustReceiveError(t, errOut, "terminal error")
	if !errors.Is(err, providerErr) {
		t.Fatalf("error = %v, want provider error", err)
	}
	mustCloseWithoutChunk(t, out, "chunk lane after terminal error")
	mustCloseWithoutError(t, errOut, "error lane after terminal error")
}

func TestManagerChatCompletionWithContinuation_StampsResponseIdentity(t *testing.T) {
	prov := &identityContinuationProvider{
		stubProvider: &stubProvider{
			id:      "openai",
			catalog: ModelCatalog{Data: []ModelInfo{{ID: "openai/gpt-test"}}},
		},
		response: &ContinuationResponse{
			Response: &ChatResponse{
				ID:      "resp-id",
				Model:   "gpt-test-2026-09-05",
				Choices: []Choice{{Message: Message{Content: "ok"}, FinishReason: "stop"}},
			},
			Continuation: &ProviderContinuation{ProviderID: "openai", ModelID: "openai/gpt-test"},
		},
	}
	mgr := identityTestManager(prov)

	resp, err := mgr.ChatCompletionWithContinuation(context.Background(), ContinuationRequest{Request: ChatRequest{Model: "openai/gpt-test"}})
	if err != nil {
		t.Fatalf("ChatCompletionWithContinuation: %v", err)
	}
	if resp.Response.ExecutionIdentity == nil {
		t.Fatal("missing continuation response identity")
	}
	if got := *resp.Response.ExecutionIdentity; got.RequestedModel != "openai/gpt-test" || got.SelectedModel != "openai/gpt-test" || got.ProviderID != "openai" || got.ResponseModel != "gpt-test-2026-09-05" || got.ResponseID != "resp-id" {
		t.Fatalf("identity = %+v", got)
	}
}

func TestManagerChatCompletionWithContinuation_ResponseWithErrorKeepsIdentityAndDoesNotRetry(t *testing.T) {
	providerErr := errors.New("continuation failed after partial response")
	prov := &identityContinuationProvider{
		stubProvider: &stubProvider{
			id:      "openrouter",
			catalog: ModelCatalog{Data: []ModelInfo{{ID: "openai/gpt-test"}}},
		},
		response: &ContinuationResponse{
			Response: &ChatResponse{
				ID:      "partial-response-id",
				Model:   "provider-reported-model",
				Choices: []Choice{{Message: Message{Content: "partial"}, FinishReason: "stop"}},
				Usage:   Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
			},
		},
		err: providerErr,
	}
	mgr := identityTestManager(prov)

	resp, err := mgr.ChatCompletionWithContinuation(context.Background(), ContinuationRequest{
		Request: ChatRequest{Model: "openai/gpt-test", MaxTokens: 32768},
	})
	if !errors.Is(err, providerErr) {
		t.Fatalf("error = %v, want provider error", err)
	}
	if prov.calls != 1 {
		t.Fatalf("continuation calls = %d, want no retry after response", prov.calls)
	}
	if resp == nil || resp.Response == nil || resp.Response.ExecutionIdentity == nil {
		t.Fatalf("response = %#v, want partial response with identity", resp)
	}
	if got := *resp.Response.ExecutionIdentity; got.ProviderID != "openrouter" || got.ResponseID != "partial-response-id" || got.ResponseModel != "provider-reported-model" {
		t.Fatalf("identity = %+v", got)
	}
	if resp.Response.Usage.TotalTokens != 3 {
		t.Fatalf("usage = %+v, want partial usage preserved", resp.Response.Usage)
	}
}

func mustReceiveSignal(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func mustReceiveChunk(t *testing.T, ch <-chan StreamChunk, label string) StreamChunk {
	t.Helper()
	select {
	case chunk, ok := <-ch:
		if !ok {
			t.Fatalf("%s closed before chunk", label)
		}
		return chunk
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
	panic("unreachable")
}

func mustCloseWithoutChunk(t *testing.T, ch <-chan StreamChunk, label string) {
	t.Helper()
	select {
	case chunk, ok := <-ch:
		if ok {
			t.Fatalf("%s produced unexpected chunk: %#v", label, chunk)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s to close", label)
	}
}

func mustReceiveError(t *testing.T, ch <-chan error, label string) error {
	t.Helper()
	select {
	case err, ok := <-ch:
		if !ok {
			t.Fatalf("%s closed before error", label)
		}
		return err
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
	panic("unreachable")
}

func mustCloseWithoutError(t *testing.T, ch <-chan error, label string) {
	t.Helper()
	select {
	case err, ok := <-ch:
		if ok {
			t.Fatalf("%s produced unexpected error: %v", label, err)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s to close", label)
	}
}

func TestOpenAIProviderChatCompletion_StripsWireBuckleyIdentityAndAttemptEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			t.Fatalf("read request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"wire-response-id",
			"model":"wire-response-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3},
			"execution_identity":{
				"requested_model":"spoofed/request",
				"selected_model":"spoofed/selected",
				"provider_id":"spoofed",
				"response_model":"spoofed-response-model",
				"response_id":"spoofed-response-id",
				"conflicted":true
			},
			"attempt_evidence":[{"usage":{"total_tokens":999},"usage_present":true}]
		}`)
	}))
	defer server.Close()

	provider := NewOpenAIProvider("test-key", server.URL, false)
	resp, err := provider.ChatCompletion(context.Background(), ChatRequest{
		Model:    "gpt-test",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if len(resp.AttemptEvidence) != 0 {
		t.Fatalf("attempt evidence = %#v, want stripped at network boundary", resp.AttemptEvidence)
	}
	if got := resp.ExecutionIdentity; got == nil || got.ResponseID != "wire-response-id" || got.ResponseModel != "wire-response-model" || got.RequestedModel != "" || got.ProviderID != "" || got.Conflicted {
		t.Fatalf("identity = %+v, want derived only from top-level wire fields", got)
	}
}

func TestParseSSEStream_StripsWireBuckleyIdentity(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"id":"chunk-response-id","model":"chunk-response-model","execution_identity":{"requested_model":"spoofed","response_model":"spoofed-response-model","response_id":"spoofed-response-id","conflicted":true},"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")
	chunks := make(chan StreamChunk, 1)

	events, err := ParseSSEStreamWithEventCount(context.Background(), strings.NewReader(stream), chunks)
	if err != nil {
		t.Fatalf("ParseSSEStreamWithEventCount: %v", err)
	}
	if events != 1 {
		t.Fatalf("events = %d, want 1", events)
	}
	chunk := <-chunks
	if got := chunk.ExecutionIdentity; got == nil || got.ResponseID != "chunk-response-id" || got.ResponseModel != "chunk-response-model" || got.RequestedModel != "" || got.Conflicted {
		t.Fatalf("identity = %+v, want derived top-level chunk fields only", got)
	}
}

func TestManagerChatCompletion_CodexSyntheticFieldsAreNotReportedIdentity(t *testing.T) {
	prov := &stubProvider{
		id:      "codex",
		catalog: ModelCatalog{Data: []ModelInfo{{ID: "codex/gpt-test"}}},
		response: &ChatResponse{
			ID:      "codex-local-thread-or-time",
			Model:   "codex/gpt-test",
			Choices: []Choice{{Message: Message{Content: "ok"}, FinishReason: "stop"}},
		},
	}
	mgr := identityTestManager(prov)

	resp, err := mgr.ChatCompletion(context.Background(), ChatRequest{Model: "codex/gpt-test"})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if resp.ExecutionIdentity == nil {
		t.Fatal("missing route identity")
	}
	if got := *resp.ExecutionIdentity; got.ResponseID != "" || got.ResponseModel != "" {
		t.Fatalf("codex response identity = %+v, want no synthetic reported fields", got)
	}
}

func TestManagerChatCompletion_GoogleSyntheticModelIsNotReportedWhenOutputFieldsMissing(t *testing.T) {
	prov := &stubProvider{
		id:      "google",
		catalog: ModelCatalog{Data: []ModelInfo{{ID: "google/gemini-test"}}},
		response: &ChatResponse{
			Model:   "google/gemini-test",
			Choices: []Choice{{Message: Message{Content: "ok"}, FinishReason: "stop"}},
		},
	}
	mgr := identityTestManager(prov)

	resp, err := mgr.ChatCompletion(context.Background(), ChatRequest{Model: "google/gemini-test"})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if resp.ExecutionIdentity == nil {
		t.Fatal("missing route identity")
	}
	if got := *resp.ExecutionIdentity; got.ResponseModel != "" || got.ResponseID != "" {
		t.Fatalf("google missing output fields identity = %+v, want no synthetic reported fields", got)
	}
}

func TestStampStreamChunksStopsOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	chunks := make(chan StreamChunk)
	errs := make(chan error)
	out, _ := stampStreamChunks(ctx, chunks, errs, "requested", "selected", "provider")

	chunks <- StreamChunk{ID: "first", Model: "reported", Choices: []StreamChoice{{Delta: MessageDelta{Content: "one"}}}}
	if chunk := <-out; chunk.ExecutionIdentity == nil || chunk.ExecutionIdentity.ResponseID != "first" {
		t.Fatalf("first stamped chunk = %#v", chunk)
	}
	cancel()
	select {
	case _, ok := <-out:
		if ok {
			t.Fatal("expected stamped stream to close after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("stamped stream did not close after cancellation")
	}
}

func identityTestManager(provider Provider) *Manager {
	id := provider.ID()
	return &Manager{
		config: &config.Config{
			Models:    config.ModelConfig{DefaultProvider: id, FallbackChains: map[string][]string{}},
			Providers: config.ProviderConfig{ModelRouting: map[string]string{}},
		},
		providers:      map[string]Provider{id: provider},
		providerOrder:  []string{id},
		catalog:        map[string]ModelInfo{},
		providerModels: map[string][]string{id: []string{}},
		modelProviders: map[string]string{},
		routingHooks:   NewRoutingHooks(),
	}
}

type identityContinuationProvider struct {
	*stubProvider
	response *ContinuationResponse
	err      error
	calls    int
}

func (p *identityContinuationProvider) SupportsContinuation(string) bool { return true }

func (p *identityContinuationProvider) ChatCompletionWithContinuation(context.Context, ContinuationRequest) (*ContinuationResponse, error) {
	p.calls++
	return p.response, p.err
}
