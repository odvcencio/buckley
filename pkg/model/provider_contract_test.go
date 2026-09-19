package model_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
)

type providerContractCase struct {
	name        string
	modelID     string
	newProvider func(string) model.Provider
	success     func(http.ResponseWriter, *http.Request, chan<- error)
}

func TestProviderContract_ChatCompletionNormalizesContentAndUsage(t *testing.T) {
	for _, tc := range providerContractCases() {
		t.Run(tc.name, func(t *testing.T) {
			handlerErrs := make(chan error, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				tc.success(w, r, handlerErrs)
			}))
			defer server.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			resp, err := tc.newProvider(server.URL).ChatCompletion(ctx, model.ChatRequest{
				Model:    tc.modelID,
				Messages: []model.Message{{Role: "user", Content: "hello"}},
			})
			if err != nil {
				t.Fatalf("ChatCompletion: %v", err)
			}
			assertNoHandlerErrors(t, handlerErrs)
			assertContractResponse(t, tc.name, resp)
		})
	}
}

func TestProviderContract_ChatCompletionStreamClosesChannelsAfterSuccess(t *testing.T) {
	for _, tc := range providerContractCases() {
		t.Run(tc.name, func(t *testing.T) {
			handlerErrs := make(chan error, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				tc.success(w, r, handlerErrs)
			}))
			defer server.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			chunks, errs := tc.newProvider(server.URL).ChatCompletionStream(ctx, model.ChatRequest{
				Model:    tc.modelID,
				Messages: []model.Message{{Role: "user", Content: "hello"}},
			})
			gotChunks, gotErrs := drainStream(t, chunks, errs)
			assertNoHandlerErrors(t, handlerErrs)
			if len(gotErrs) > 0 {
				t.Fatalf("stream errors = %v, want none", gotErrs)
			}
			if len(gotChunks) == 0 {
				t.Fatalf("stream chunks = 0, want at least one")
			}
			if !streamHasContent(gotChunks, "contract ok") {
				t.Fatalf("stream chunks missing content %q: %+v", "contract ok", gotChunks)
			}
			if !streamHasExactUsage(gotChunks) {
				t.Fatalf("stream chunks missing exact usage 7/5/12: %+v", gotChunks)
			}
		})
	}
}

func TestProviderContract_ChatCompletionStreamPreCanceledContextClosesChannels(t *testing.T) {
	for _, tc := range providerContractCases() {
		t.Run(tc.name, func(t *testing.T) {
			var requests int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&requests, 1)
				tc.success(w, r, make(chan error, 1))
			}))
			defer server.Close()

			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			chunks, errs := tc.newProvider(server.URL).ChatCompletionStream(ctx, model.ChatRequest{
				Model:    tc.modelID,
				Messages: []model.Message{{Role: "user", Content: "hello"}},
			})
			gotChunks, gotErrs := drainStream(t, chunks, errs)
			if len(gotChunks) != 0 {
				t.Fatalf("pre-canceled stream chunks = %+v, want none", gotChunks)
			}
			if !hasError(gotErrs, context.Canceled) {
				t.Fatalf("pre-canceled stream errors = %v, want context.Canceled", gotErrs)
			}
			if got := atomic.LoadInt32(&requests); got != 0 {
				t.Fatalf("pre-canceled stream made %d HTTP requests, want zero", got)
			}
		})
	}
}

func TestProviderContract_ChatCompletionStreamInFlightCancellationClosesChannels(t *testing.T) {
	for _, tc := range providerContractCases() {
		t.Run(tc.name, func(t *testing.T) {
			started := make(chan struct{})
			serverCanceled := make(chan struct{})
			release := make(chan struct{})
			startedOnce := sync.Once{}
			serverCanceledOnce := sync.Once{}
			releaseOnce := sync.Once{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				startedOnce.Do(func() { close(started) })
				select {
				case <-r.Context().Done():
					serverCanceledOnce.Do(func() { close(serverCanceled) })
				case <-release:
					tc.success(w, r, make(chan error, 1))
				}
			}))
			defer func() {
				releaseOnce.Do(func() { close(release) })
				server.Close()
			}()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			chunks, errs := tc.newProvider(server.URL).ChatCompletionStream(ctx, model.ChatRequest{
				Model:    tc.modelID,
				Messages: []model.Message{{Role: "user", Content: "hello"}},
			})
			waitClosed(t, started, "server request arrival")
			cancel()
			drained := make(chan streamDrainResult, 1)
			go func() {
				chunks, errs, timedOut := collectStream(chunks, errs, 2*time.Second)
				drained <- streamDrainResult{chunks: chunks, errs: errs, timedOut: timedOut}
			}()
			waitClosed(t, serverCanceled, "server request cancellation")
			result := waitDrainResult(t, drained)
			if result.timedOut {
				t.Fatalf("timeout waiting for in-flight canceled stream channels to close")
			}
			if streamHasAnyContent(result.chunks) {
				t.Fatalf("in-flight canceled stream produced content chunks: %+v", result.chunks)
			}
			if !hasError(result.errs, context.Canceled) {
				t.Fatalf("in-flight canceled stream errors = %v, want context.Canceled", result.errs)
			}
		})
	}
}

func providerContractCases() []providerContractCase {
	return []providerContractCase{
		{
			name:    "google",
			modelID: "google/gemini-2.0-flash",
			newProvider: func(baseURL string) model.Provider {
				return model.NewGoogleProvider("test-key", baseURL, false)
			},
			success: func(w http.ResponseWriter, r *http.Request, errs chan<- error) {
				if r.URL.Path != "/models/gemini-2.0-flash:generateContent" {
					errs <- fmt.Errorf("google path = %q", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{
					"candidates":[{"content":{"role":"model","parts":[{"text":"contract ok"}]},"finishReason":"STOP"}],
					"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":5,"totalTokenCount":12}
				}`)
			},
		},
		{
			name:    "anthropic",
			modelID: "anthropic/claude-3.5-haiku",
			newProvider: func(baseURL string) model.Provider {
				return model.NewAnthropicProvider("test-key", baseURL, false)
			},
			success: func(w http.ResponseWriter, r *http.Request, errs chan<- error) {
				if r.URL.Path != "/v1/messages" {
					errs <- fmt.Errorf("anthropic path = %q", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{
					"id":"msg-contract",
					"model":"claude-3.5-haiku",
					"content":[{"type":"text","text":"contract ok"}],
					"stop_reason":"end_turn",
					"usage":{"input_tokens":7,"output_tokens":5}
				}`)
			},
		},
		{
			name:    "openai-compatible",
			modelID: "openai_compatible/contract-model",
			newProvider: func(baseURL string) model.Provider {
				return model.NewOpenAICompatibleProvider(config.OpenAICompatibleConfig{
					BaseURL: baseURL,
					APIKey:  "test-key",
					Models:  []string{"contract-model"},
				}, false)
			},
			success: openAICompatibleContractHandler,
		},
		{
			name:    "ollama",
			modelID: "contract-model",
			newProvider: func(baseURL string) model.Provider {
				return model.NewOllamaProvider(baseURL, false)
			},
			success: ollamaContractHandler,
		},
	}
}

func openAICompatibleContractHandler(w http.ResponseWriter, r *http.Request, errs chan<- error) {
	if r.URL.Path != "/chat/completions" {
		errs <- fmt.Errorf("openai-compatible path = %q", r.URL.Path)
	}
	stream, ok := requestStream(r, errs)
	if !ok {
		http.Error(w, "decode request", http.StatusBadRequest)
		return
	}
	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"chunk-contract\",\"model\":\"contract-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"contract ok\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"chunk-contract\",\"model\":\"contract-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":5,\"total_tokens\":12}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{
		"id":"chatcmpl-contract",
		"model":"contract-model",
		"choices":[{"index":0,"message":{"role":"assistant","content":"contract ok"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":7,"completion_tokens":5,"total_tokens":12}
	}`)
}

func ollamaContractHandler(w http.ResponseWriter, r *http.Request, errs chan<- error) {
	if r.URL.Path != "/api/chat" {
		errs <- fmt.Errorf("ollama path = %q", r.URL.Path)
	}
	w.Header().Set("Content-Type", "application/json")
	stream, ok := requestStream(r, errs)
	if !ok {
		http.Error(w, "decode request", http.StatusBadRequest)
		return
	}
	if stream {
		_, _ = io.WriteString(w, `{"model":"contract-model","message":{"role":"assistant","content":"contract ok"},"done":false}`+"\n")
		_, _ = io.WriteString(w, `{"model":"contract-model","done":true,"done_reason":"stop","prompt_eval_count":7,"eval_count":5}`+"\n")
		return
	}
	_, _ = io.WriteString(w, `{
		"model":"contract-model",
		"message":{"role":"assistant","content":"contract ok"},
		"done":true,
		"done_reason":"stop",
		"prompt_eval_count":7,
		"eval_count":5
	}`)
}

func requestStream(r *http.Request, errs chan<- error) (bool, bool) {
	var body struct {
		Stream bool `json:"stream"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		errs <- fmt.Errorf("decode request stream flag: %w", err)
		return false, false
	}
	return body.Stream, true
}

func assertContractResponse(t *testing.T, provider string, resp *model.ChatResponse) {
	t.Helper()
	if resp == nil {
		t.Fatal("response is nil")
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("%s choices = %d, want 1", provider, len(resp.Choices))
	}
	choice := resp.Choices[0]
	if choice.Message.Role != "assistant" {
		t.Fatalf("%s role = %q, want assistant", provider, choice.Message.Role)
	}
	if got := fmt.Sprint(choice.Message.Content); got != "contract ok" {
		t.Fatalf("%s content = %q, want contract ok", provider, got)
	}
	if strings.TrimSpace(choice.FinishReason) == "" {
		t.Fatalf("%s finish reason is empty", provider)
	}
	if resp.Usage.PromptTokens != 7 || resp.Usage.CompletionTokens != 5 || resp.Usage.TotalTokens != 12 {
		t.Fatalf("%s usage = %+v, want 7/5/12", provider, resp.Usage)
	}
}

func drainStream(t *testing.T, chunks <-chan model.StreamChunk, errs <-chan error) ([]model.StreamChunk, []error) {
	t.Helper()
	gotChunks, gotErrs, timedOut := collectStream(chunks, errs, 2*time.Second)
	if timedOut {
		t.Fatalf("timeout waiting for stream channels to close")
	}
	return gotChunks, gotErrs
}

type streamDrainResult struct {
	chunks   []model.StreamChunk
	errs     []error
	timedOut bool
}

func collectStream(chunks <-chan model.StreamChunk, errs <-chan error, timeout time.Duration) ([]model.StreamChunk, []error, bool) {
	var gotChunks []model.StreamChunk
	var gotErrs []error
	timer := time.After(timeout)
	for chunks != nil || errs != nil {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				chunks = nil
				continue
			}
			gotChunks = append(gotChunks, chunk)
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				gotErrs = append(gotErrs, err)
			}
		case <-timer:
			return gotChunks, gotErrs, true
		}
	}
	return gotChunks, gotErrs, false
}

func waitDrainResult(t *testing.T, drained <-chan streamDrainResult) streamDrainResult {
	t.Helper()
	select {
	case result := <-drained:
		return result
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for drain goroutine")
		return streamDrainResult{timedOut: true}
	}
}

func streamHasContent(chunks []model.StreamChunk, want string) bool {
	for _, chunk := range chunks {
		for _, choice := range chunk.Choices {
			if strings.Contains(choice.Delta.Content, want) {
				return true
			}
		}
	}
	return false
}

func streamHasAnyContent(chunks []model.StreamChunk) bool {
	for _, chunk := range chunks {
		for _, choice := range chunk.Choices {
			if strings.TrimSpace(choice.Delta.Content) != "" {
				return true
			}
		}
	}
	return false
}

func streamHasExactUsage(chunks []model.StreamChunk) bool {
	for _, chunk := range chunks {
		if chunk.Usage != nil && chunk.Usage.PromptTokens == 7 && chunk.Usage.CompletionTokens == 5 && chunk.Usage.TotalTokens == 12 {
			return true
		}
	}
	return false
}

func hasError(errs []error, target error) bool {
	for _, err := range errs {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

func waitClosed(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for %s", label)
	}
}

func assertNoHandlerErrors(t *testing.T, errs <-chan error) {
	t.Helper()
	for {
		select {
		case err := <-errs:
			if err != nil {
				t.Fatal(err)
			}
		default:
			return
		}
	}
}
