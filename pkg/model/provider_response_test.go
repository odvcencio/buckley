package model

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
)

func TestProviderResponse_ReaderBoundaries(t *testing.T) {
	for _, size := range []int{0, 7, 8, 9, 100} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			reader := &providerResponseReader{source: strings.NewReader(strings.Repeat("x", size)), remaining: 8}
			data, err := io.ReadAll(reader)
			if len(data) != min(size, 8) || errors.Is(err, ErrProviderResponseTooLarge) != (size > 8) {
				t.Fatalf("boundary changed bytes or error: data=%d err=%v", len(data), err)
			}
		})
	}
	reader := &providerResponseReader{source: &trackedProviderErrorBody{Reader: strings.NewReader("prefix"), terminalErr: io.ErrUnexpectedEOF}, remaining: 8}
	data, err := io.ReadAll(reader)
	if string(data) != "prefix" || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal("response guard lost the original read error")
	}
}

func TestProviderResponse_ExactDocumentAndOverflowDuringDecode(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		t.Run(fmt.Sprint(overflow), func(t *testing.T) {
			head, tail := `{"text":"`, `"}`
			flood := &networkLogFlood{remaining: int64(MaxProviderResponseBytes - len(head) - len(tail))}
			ending := tail
			if overflow {
				ending += " "
			}
			response := &http.Response{ContentLength: -1, Body: io.NopCloser(io.MultiReader(strings.NewReader(head), flood, strings.NewReader(ending)))}
			var value struct{ Text string }
			err := decodeProviderResponse(response, &value)
			if errors.Is(err, ErrProviderResponseTooLarge) != overflow || !overflow && (err != nil || len(value.Text) != MaxProviderResponseBytes-len(head)-len(tail)) {
				t.Fatalf("exact-limit document/overflow not distinguished: overflow=%t err=%v", overflow, err)
			}
		})
	}
}

func TestProviderResponse_AdvertisedLimitAcrossAdapters(t *testing.T) {
	for _, mode := range []string{"openrouter", "direct", "openrouter_catalog", "compatible_catalog", "litellm_catalog", "models_dev", "ollama", "ollama_catalog"} {
		t.Run(mode, func(t *testing.T) {
			var bodies []*trackedProviderErrorBody
			reads := 0
			httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				reads++
				body := &trackedProviderErrorBody{Reader: strings.NewReader("not consumed")}
				bodies = append(bodies, body)
				return &http.Response{StatusCode: 200, Header: make(http.Header), ContentLength: MaxProviderResponseBytes + 1, Body: body}, nil
			})}
			client := NewClient("test", "http://fixture.invalid")
			client.httpClient = httpClient
			t.Cleanup(func() { _ = client.Close() })
			var err error
			switch mode {
			case "openrouter":
				var response *ChatResponse
				response, err = client.ChatCompletion(context.Background(), ChatRequest{Model: "fixture"})
				if response != nil {
					t.Fatal("published a partial model response")
				}
			case "direct":
				var response []byte
				response, err = NewProviderTransport(ProviderTransportOptions{}).Do(context.Background(), httpClient, "POST", "http://fixture.invalid", map[string]any{}, nil)
				if response != nil {
					t.Fatal("published a partial response body")
				}
			case "openrouter_catalog":
				_, err = client.FetchCatalog()
			case "compatible_catalog", "litellm_catalog":
				provider := NewOpenAICompatibleProvider(config.OpenAICompatibleConfig{BaseURL: "http://fixture.invalid"}, false)
				provider.httpClient = httpClient
				provider.liteLLMInfo = mode == "litellm_catalog"
				_, err = provider.FetchCatalog()
			case "models_dev":
				_, err = FetchModelsDevCatalog(context.Background(), httpClient, "http://fixture.invalid")
			case "ollama", "ollama_catalog":
				provider := NewOllamaProvider("http://fixture.invalid", false)
				provider.httpClient = httpClient
				if mode == "ollama" {
					_, err = provider.ChatCompletion(context.Background(), ChatRequest{Model: "fixture"})
				} else {
					_, err = provider.FetchCatalog()
				}
			}
			if !errors.Is(err, ErrProviderResponseTooLarge) || isRetryableError(err) || (RetryConfig{MaxRetries: 5}).canRetry(0, err) {
				t.Fatalf("lost terminal budget error: %v", err)
			}
			want := 1
			if mode == "litellm_catalog" {
				want = 2 // Existing fallback probes the generic catalog, not a paid model retry.
			}
			if reads != want {
				t.Fatalf("unexpected request count: %d", reads)
			}
			for _, body := range bodies {
				if !body.closed {
					t.Fatal("rejected body was not closed")
				}
			}
			t.Logf("adapter=%s requests=%d rejected=true retryable=false bodies_closed=true", mode, reads)
		})
	}
}

func TestProviderResponse_RejectedRefreshPreservesCatalog(t *testing.T) {
	client := NewClient("test", "http://fixture.invalid")
	t.Cleanup(func() { _ = client.Close() })
	requests := 0
	client.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"kept","name":"Kept"}]}`))}, nil
		}
		return &http.Response{StatusCode: 200, ContentLength: MaxProviderResponseBytes + 1, Body: io.NopCloser(strings.NewReader("unused"))}, nil
	})}
	first, err := client.FetchCatalog()
	if err != nil || len(first.Data) != 1 {
		t.Fatalf("initial catalog: %v", err)
	}
	age := client.catalogAge
	if catalog, err := client.RefreshCatalog(); catalog != nil || !errors.Is(err, ErrProviderResponseTooLarge) {
		t.Fatal("oversized catalog refresh was not rejected")
	}
	last, err := client.FetchCatalog()
	if err != nil || last != first || client.catalogAge != age || requests != 2 {
		t.Fatal("failed refresh changed the usable cached snapshot")
	}
}

func TestProviderResponse_FreshRequestCanRecover(t *testing.T) {
	client := NewClient("test", "http://fixture.invalid")
	t.Cleanup(func() { _ = client.Close() })
	requests := 0
	client.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			return &http.Response{StatusCode: 200, ContentLength: MaxProviderResponseBytes + 1, Body: io.NopCloser(strings.NewReader("unused"))}, nil
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":"recovered","choices":[{"message":{"role":"assistant","content":"ok"}}]}`))}, nil
	})}
	request := ChatRequest{Model: "fixture"}
	if _, err := client.ChatCompletion(context.Background(), request); !errors.Is(err, ErrProviderResponseTooLarge) || requests != 1 {
		t.Fatal("response limit was retried or untyped")
	}
	response, err := client.ChatCompletion(context.Background(), request)
	if err != nil || response.ID != "recovered" || requests != 2 {
		t.Fatal("a fresh legitimate request did not recover")
	}
}
