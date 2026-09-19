package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tools"
)

type oneshotRouteWireRequest struct {
	Model      string                 `json:"model"`
	Tools      []map[string]any       `json:"tools"`
	ToolChoice string                 `json:"tool_choice"`
	Reasoning  *model.ReasoningConfig `json:"reasoning"`
}

func TestNewOneshotToolInvokerRouteBindsSelectedWireAndPreventsDrift(t *testing.T) {
	var (
		mu       sync.Mutex
		requests []oneshotRouteWireRequest
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"selected-capable","supported_parameters":["tools","reasoning"]},{"id":"selected-toolless","supported_parameters":[]},{"id":"selected-unknown"}]}`)
		case "/chat/completions":
			var request oneshotRouteWireRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode provider request: %v", err)
				return
			}
			mu.Lock()
			requests = append(requests, request)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"route-test","model":"selected-capable","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	cfg := config.DefaultConfig()
	cfg.Models.DefaultProvider = "openai_compatible"
	cfg.Models.Reasoning = "auto"
	cfg.Providers.OpenAICompatible.Enabled = true
	cfg.Providers.OpenAICompatible.APIKey = "test-key"
	cfg.Providers.OpenAICompatible.BaseURL = server.URL
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	selected := "selected-capable"
	mgr.RoutingHooks().Register(func(decision *model.RoutingDecision) *model.RoutingDecision {
		if decision != nil && decision.RequestedModel == "route-alias" {
			decision.SelectedModel = "openai_compatible/" + selected
		}
		return decision
	})

	invoke := func(t *testing.T) error {
		t.Helper()
		toolInvoker, err := newOneshotToolInvoker(oneshotBackendAPI, "commit", "route-alias", cfg, mgr, nil)
		if err != nil {
			return err
		}
		_, _, err = toolInvoker.Invoke(context.Background(), "system", "user", oneshotRouteBindingTool(), nil)
		return err
	}

	t.Run("selected capable route retains schemas and reasoning", func(t *testing.T) {
		selected = "selected-capable"
		if err := invoke(t); err != nil {
			t.Fatalf("Invoke: %v", err)
		}
		mu.Lock()
		captured := append([]oneshotRouteWireRequest(nil), requests...)
		mu.Unlock()
		if len(captured) != 1 {
			t.Fatalf("provider requests = %d, want 1", len(captured))
		}
		if captured[0].Model != "selected-capable" || len(captured[0].Tools) != 1 || captured[0].ToolChoice != "required" {
			t.Fatalf("wire request = %+v, want selected model with required schema", captured[0])
		}
		if captured[0].Reasoning == nil || captured[0].Reasoning.Effort == "" {
			t.Fatalf("reasoning = %+v, want selected-route reasoning profile", captured[0].Reasoning)
		}
	})

	t.Run("unknown selected metadata remains schema eligible", func(t *testing.T) {
		selected = "selected-unknown"
		mu.Lock()
		requests = nil
		mu.Unlock()
		if err := invoke(t); err != nil {
			t.Fatalf("Invoke: %v", err)
		}
		mu.Lock()
		captured := append([]oneshotRouteWireRequest(nil), requests...)
		mu.Unlock()
		if len(captured) != 1 || len(captured[0].Tools) != 1 || captured[0].ToolChoice != "required" {
			t.Fatalf("unknown-route wire requests = %+v, want one required-schema request", captured)
		}
	})

	t.Run("catalog-confirmed toolless route makes zero chat requests", func(t *testing.T) {
		selected = "selected-toolless"
		mu.Lock()
		requests = nil
		mu.Unlock()
		err := invoke(t)
		if err == nil || !strings.Contains(err.Error(), "tool-capable model") {
			t.Fatalf("error = %v, want actionable tool-capable-model error", err)
		}
		mu.Lock()
		count := len(requests)
		mu.Unlock()
		if count != 0 {
			t.Fatalf("provider requests = %d, want zero", count)
		}
	})

	t.Run("route drift fails before provider request", func(t *testing.T) {
		selected = "selected-capable"
		toolInvoker, err := newOneshotToolInvoker(oneshotBackendAPI, "commit", "route-alias", cfg, mgr, nil)
		if err != nil {
			t.Fatalf("newOneshotToolInvoker: %v", err)
		}
		selected = "selected-unknown"
		mu.Lock()
		requests = nil
		mu.Unlock()
		_, _, err = toolInvoker.Invoke(context.Background(), "system", "user", oneshotRouteBindingTool(), nil)
		if err == nil || !strings.Contains(err.Error(), "model route changed before dispatch") {
			t.Fatalf("error = %v, want route-drift error", err)
		}
		mu.Lock()
		count := len(requests)
		mu.Unlock()
		if count != 0 {
			t.Fatalf("provider requests = %d, want zero", count)
		}
	})
}

func oneshotRouteBindingTool() tools.Definition {
	return tools.Definition{Name: "generate_commit", Parameters: tools.ObjectSchema(map[string]tools.Property{}, "")}
}

func TestGovernedOneshotClientRouteForwardingEnforcesPolicyFirst(t *testing.T) {
	setOneshotTestWorkspace(t, t.TempDir())
	inner := &governedRouteCaptureClient{}
	client, err := oneshotClientForProvider(inner, "stealth/route-alias", "openrouter", "zdr")
	if err != nil {
		t.Fatalf("oneshotClientForProvider: %v", err)
	}
	governed, ok := client.(*governedOpenRouterClient)
	if !ok {
		t.Fatalf("client type = %T, want governed wrapper", client)
	}
	route := model.ModelRoute{RequestedModel: "stealth/route-alias", SelectedModel: "selected", ProviderID: "openrouter"}
	if _, err := governed.ChatCompletionForRoute(context.Background(), model.ChatRequest{Model: "stealth/route-alias", Route: route}, route); err != nil {
		t.Fatalf("ChatCompletionForRoute: %v", err)
	}
	if inner.genericCalls != 0 || inner.routeCalls != 1 {
		t.Fatalf("generic/route calls = %d/%d, want 0/1", inner.genericCalls, inner.routeCalls)
	}
	if inner.request.Route != route || inner.request.Provider["allow_fallbacks"] != false || inner.request.Provider["zdr"] != true {
		t.Fatalf("forwarded request = %+v, want route and governed provider fields", inner.request)
	}
	if !governed.OfferToolsForRoute(route) || governed.ToolsCatalogConfirmedUnavailableForRoute(route) {
		t.Fatal("governed capability forwarding did not preserve inner route facts")
	}
	if window, err := governed.GetContextLengthForRoute(route); err != nil || window != 4096 {
		t.Fatalf("route context = %d, %v, want 4096", window, err)
	}
	chunks, errs := governed.ChatCompletionStreamForRoute(context.Background(), model.ChatRequest{Model: "stealth/route-alias", Route: route}, route)
	for range chunks {
	}
	for err := range errs {
		if err != nil {
			t.Fatalf("ChatCompletionStreamForRoute: %v", err)
		}
	}
	if inner.streamCalls != 1 || inner.streamRequest.Provider["zdr"] != true {
		t.Fatalf("stream calls/request = %d/%+v, want policy-before-route forwarding", inner.streamCalls, inner.streamRequest)
	}
}

type governedRouteCaptureClient struct {
	genericCalls  int
	routeCalls    int
	streamCalls   int
	request       model.ChatRequest
	streamRequest model.ChatRequest
}

func (c *governedRouteCaptureClient) ChatCompletion(context.Context, model.ChatRequest) (*model.ChatResponse, error) {
	c.genericCalls++
	return &model.ChatResponse{}, nil
}

func (c *governedRouteCaptureClient) ChatCompletionForRoute(_ context.Context, req model.ChatRequest, _ model.ModelRoute) (*model.ChatResponse, error) {
	c.routeCalls++
	c.request = req
	return &model.ChatResponse{}, nil
}

func (c *governedRouteCaptureClient) ChatCompletionStreamForRoute(_ context.Context, req model.ChatRequest, _ model.ModelRoute) (<-chan model.StreamChunk, <-chan error) {
	c.streamCalls++
	c.streamRequest = req
	chunks := make(chan model.StreamChunk)
	close(chunks)
	errs := make(chan error)
	close(errs)
	return chunks, errs
}

func (c *governedRouteCaptureClient) OfferToolsForRoute(model.ModelRoute) bool { return true }

func (c *governedRouteCaptureClient) ToolsCatalogConfirmedUnavailableForRoute(model.ModelRoute) bool {
	return false
}

func (c *governedRouteCaptureClient) GetContextLengthForRoute(model.ModelRoute) (int, error) {
	return 4096, nil
}
