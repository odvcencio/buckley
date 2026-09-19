package oneshot

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tools"
)

type routeCaptureClient struct {
	negative       bool
	genericCalls   int
	routeCalls     int
	streamCalls    int
	contextCalls   int
	routeRequests  []model.ChatRequest
	streamRequests []model.ChatRequest
}

func (c *routeCaptureClient) ChatCompletion(context.Context, model.ChatRequest) (*model.ChatResponse, error) {
	c.genericCalls++
	return routeCaptureResponse(), nil
}

func (c *routeCaptureClient) ChatCompletionForRoute(_ context.Context, req model.ChatRequest, route model.ModelRoute) (*model.ChatResponse, error) {
	c.routeCalls++
	if req.Route != route {
		return nil, &routeCaptureError{message: "request route was not forwarded"}
	}
	c.routeRequests = append(c.routeRequests, req)
	return routeCaptureResponse(), nil
}

func (c *routeCaptureClient) ChatCompletionStreamForRoute(_ context.Context, req model.ChatRequest, route model.ModelRoute) (<-chan model.StreamChunk, <-chan error) {
	c.streamCalls++
	if req.Route != route {
		return routeCaptureFailedStream("request route was not forwarded")
	}
	c.streamRequests = append(c.streamRequests, req)
	chunks := make(chan model.StreamChunk)
	close(chunks)
	errs := make(chan error)
	close(errs)
	return chunks, errs
}

func (c *routeCaptureClient) OfferToolsForRoute(model.ModelRoute) bool { return true }

func (c *routeCaptureClient) ToolsCatalogConfirmedUnavailableForRoute(model.ModelRoute) bool {
	return c.negative
}

func (c *routeCaptureClient) GetContextLengthForRoute(model.ModelRoute) (int, error) {
	c.contextCalls++
	return 8192, nil
}

type routeCaptureError struct{ message string }

func (e *routeCaptureError) Error() string { return e.message }

func routeCaptureFailedStream(message string) (<-chan model.StreamChunk, <-chan error) {
	chunks := make(chan model.StreamChunk)
	close(chunks)
	errs := make(chan error, 1)
	errs <- &routeCaptureError{message: message}
	close(errs)
	return chunks, errs
}

func routeCaptureResponse() *model.ChatResponse {
	return &model.ChatResponse{Choices: []model.Choice{{
		Message:      model.Message{Role: "assistant", Content: "done"},
		FinishReason: "stop",
	}}}
}

func routeBindingRoute() model.ModelRoute {
	return model.ModelRoute{RequestedModel: "alias/model", SelectedModel: "selected/model", ProviderID: "openrouter"}
}

func routeBindingTool() tools.Definition {
	return tools.Definition{Name: "emit", Parameters: tools.ObjectSchema(map[string]tools.Property{}, "")}
}

func newRouteBindingInvoker(client model.CompletionClient, route model.ModelRoute) *DefaultInvoker {
	return NewInvoker(InvokerConfig{Client: client, Model: "wrong/model", Provider: "openrouter", Route: route})
}

func assertRouteRequest(t *testing.T, req model.ChatRequest, route model.ModelRoute, wantsTools bool) {
	t.Helper()
	if req.Model != route.RequestedModel {
		t.Fatalf("request model = %q, want requested model %q", req.Model, route.RequestedModel)
	}
	if req.Route != route {
		t.Fatalf("request route = %+v, want %+v", req.Route, route)
	}
	if wantsTools && len(req.Tools) == 0 {
		t.Fatal("request omitted tool schema")
	}
	if wantsTools && req.ToolChoice != "auto" {
		t.Fatalf("tool choice = %q, want auto without a request profile", req.ToolChoice)
	}
}

func TestDefaultInvokerRouteDispatchesEveryEntryPoint(t *testing.T) {
	route := routeBindingRoute()
	tool := routeBindingTool()

	t.Run("invoke", func(t *testing.T) {
		client := &routeCaptureClient{}
		_, _, err := newRouteBindingInvoker(client, route).Invoke(context.Background(), "system", "user", tool, nil)
		if err != nil {
			t.Fatalf("Invoke: %v", err)
		}
		if client.genericCalls != 0 || client.routeCalls != 1 {
			t.Fatalf("generic/route calls = %d/%d, want 0/1", client.genericCalls, client.routeCalls)
		}
		assertRouteRequest(t, client.routeRequests[0], route, true)
	})

	t.Run("retry", func(t *testing.T) {
		client := &routeCaptureClient{}
		_, _, err := newRouteBindingInvoker(client, route).InvokeWithRetry(context.Background(), "system", "user", tool, nil)
		if err != nil {
			t.Fatalf("InvokeWithRetry: %v", err)
		}
		if client.genericCalls != 0 || client.routeCalls != 2 {
			t.Fatalf("generic/route calls = %d/%d, want 0/2", client.genericCalls, client.routeCalls)
		}
		for _, req := range client.routeRequests {
			assertRouteRequest(t, req, route, true)
		}
	})

	t.Run("stream", func(t *testing.T) {
		client := &routeCaptureClient{}
		_, _, err := newRouteBindingInvoker(client, route).InvokeStream(context.Background(), "system", "user", tool, nil, nil)
		if err != nil {
			t.Fatalf("InvokeStream: %v", err)
		}
		if client.genericCalls != 0 || client.routeCalls != 0 || client.streamCalls != 1 {
			t.Fatalf("generic/route/stream calls = %d/%d/%d, want 0/0/1", client.genericCalls, client.routeCalls, client.streamCalls)
		}
		assertRouteRequest(t, client.streamRequests[0], route, true)
	})

	t.Run("text", func(t *testing.T) {
		client := &routeCaptureClient{}
		_, _, err := newRouteBindingInvoker(client, route).InvokeText(context.Background(), "system", "user", nil)
		if err != nil {
			t.Fatalf("InvokeText: %v", err)
		}
		if client.genericCalls != 0 || client.routeCalls != 1 {
			t.Fatalf("generic/route calls = %d/%d, want 0/1", client.genericCalls, client.routeCalls)
		}
		assertRouteRequest(t, client.routeRequests[0], route, false)
		if len(client.routeRequests[0].Tools) != 0 {
			t.Fatal("text request unexpectedly offered tools")
		}
	})

	t.Run("tools", func(t *testing.T) {
		client := &routeCaptureClient{}
		executor := &routeCaptureExecutor{}
		_, _, err := newRouteBindingInvoker(client, route).InvokeWithTools(context.Background(), "system", "user", []tools.Definition{tool}, executor, 1)
		if err != nil {
			t.Fatalf("InvokeWithTools: %v", err)
		}
		if client.genericCalls != 0 || client.routeCalls != 1 || client.contextCalls == 0 {
			t.Fatalf("generic/route/context calls = %d/%d/%d, want 0/1/>0", client.genericCalls, client.routeCalls, client.contextCalls)
		}
		if executor.calls != 0 {
			t.Fatalf("executor calls = %d, want 0", executor.calls)
		}
		assertRouteRequest(t, client.routeRequests[0], route, true)
	})
}

func TestDefaultInvokerRouteRejectsCatalogConfirmedToollessBeforeIO(t *testing.T) {
	route := routeBindingRoute()
	tool := routeBindingTool()

	assertRejected := func(t *testing.T, client *routeCaptureClient, err error, executor *routeCaptureExecutor) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "tool-capable model") {
			t.Fatalf("error = %v, want actionable tool-capable-model error", err)
		}
		if client.genericCalls != 0 || client.routeCalls != 0 || client.streamCalls != 0 {
			t.Fatalf("provider calls generic/route/stream = %d/%d/%d, want zero", client.genericCalls, client.routeCalls, client.streamCalls)
		}
		if executor != nil && executor.calls != 0 {
			t.Fatalf("executor calls = %d, want zero", executor.calls)
		}
	}

	t.Run("invoke", func(t *testing.T) {
		client := &routeCaptureClient{negative: true}
		_, _, err := newRouteBindingInvoker(client, route).Invoke(context.Background(), "system", "user", tool, nil)
		assertRejected(t, client, err, nil)
	})
	t.Run("stream", func(t *testing.T) {
		client := &routeCaptureClient{negative: true}
		_, _, err := newRouteBindingInvoker(client, route).InvokeStream(context.Background(), "system", "user", tool, nil, nil)
		assertRejected(t, client, err, nil)
	})
	t.Run("retry", func(t *testing.T) {
		client := &routeCaptureClient{negative: true}
		_, _, err := newRouteBindingInvoker(client, route).InvokeWithRetry(context.Background(), "system", "user", tool, nil)
		assertRejected(t, client, err, nil)
	})
	t.Run("tools", func(t *testing.T) {
		client := &routeCaptureClient{negative: true}
		executor := &routeCaptureExecutor{}
		_, _, err := newRouteBindingInvoker(client, route).InvokeWithTools(context.Background(), "system", "user", []tools.Definition{tool}, executor, 1)
		assertRejected(t, client, err, executor)
	})
}

func TestDefaultInvokerRouteRequiresRouteCapableClientAndZeroRouteStaysGeneric(t *testing.T) {
	generic := &routeCaptureClient{}
	zeroRoute := NewInvoker(InvokerConfig{Client: generic, Model: "plain"})
	if _, _, err := zeroRoute.Invoke(context.Background(), "system", "user", routeBindingTool(), nil); err != nil {
		t.Fatalf("zero-route Invoke: %v", err)
	}
	if generic.genericCalls != 1 || generic.routeCalls != 0 {
		t.Fatalf("zero-route generic/route calls = %d/%d, want 1/0", generic.genericCalls, generic.routeCalls)
	}

	plain := &plainRouteBindingClient{}
	_, trace, err := newRouteBindingInvoker(plain, routeBindingRoute()).Invoke(context.Background(), "system", "user", routeBindingTool(), nil)
	if err == nil || !strings.Contains(err.Error(), "route-capable") {
		t.Fatalf("error = %v, want route-capable contract error", err)
	}
	if trace == nil || !strings.Contains(trace.Error, "route-capable") {
		t.Fatalf("trace = %+v, want route contract error", trace)
	}
	if plain.calls != 0 {
		t.Fatalf("generic provider calls = %d, want zero", plain.calls)
	}
}

type routeCaptureExecutor struct{ calls int }

func (e *routeCaptureExecutor) Execute(string, json.RawMessage) (string, error) {
	e.calls++
	return "", nil
}

type plainRouteBindingClient struct{ calls int }

func (c *plainRouteBindingClient) ChatCompletion(context.Context, model.ChatRequest) (*model.ChatResponse, error) {
	c.calls++
	return routeCaptureResponse(), nil
}
