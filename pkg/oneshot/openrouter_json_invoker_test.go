package oneshot

import (
	"context"
	"errors"
	"testing"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tools"
)

type capturingOpenRouterJSONClient struct {
	requests []model.ChatRequest
	response *model.ChatResponse
	err      error
}

func (c *capturingOpenRouterJSONClient) ChatCompletion(_ context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	c.requests = append(c.requests, req)
	return c.response, c.err
}

func TestOpenRouterJSONInvoker_UsesOneExactToolFreeStrictZDRRequest(t *testing.T) {
	client := &capturingOpenRouterJSONClient{response: &model.ChatResponse{
		Choices: []model.Choice{{
			Message: model.Message{Content: `{"action":"fix","subject":"restore API commits","body":["Use one bounded request"]}`},
		}},
		Usage: model.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}}
	invoker, err := NewOpenRouterJSONInvoker(InvokerConfig{
		Client:          client,
		Model:           "openai/gpt-5.6-luna-pro",
		Provider:        "openrouter",
		ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatalf("NewOpenRouterJSONInvoker: %v", err)
	}
	tool := tools.Definition{
		Name: "generate_commit",
		Parameters: tools.ObjectSchema(map[string]tools.Property{
			"action":  tools.StringProperty("action"),
			"subject": tools.StringProperty("subject"),
			"body":    tools.ArrayProperty("body", tools.StringProperty("bullet")),
		}, "action", "subject", "body"),
	}

	result, _, err := invoker.Invoke(context.Background(), "system", "user", tool, nil)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if len(client.requests) != 1 {
		t.Fatalf("requests = %d, want exactly one", len(client.requests))
	}
	req := client.requests[0]
	if req.Model != "openai/gpt-5.6-luna-pro" {
		t.Fatalf("model = %q, want exact requested model", req.Model)
	}
	if len(req.Models) != 0 || req.Provider["allow_fallbacks"] != false {
		t.Fatalf("fallback route = models:%v provider:%v, want one exact route", req.Models, req.Provider)
	}
	if req.OpenRouterRetention != model.OpenRouterRetentionZDR || req.Provider["zdr"] != true {
		t.Fatalf("retention = %q provider:%v, want strict ZDR", req.OpenRouterRetention, req.Provider)
	}
	if req.RetryMode != model.RequestRetrySingleAttempt {
		t.Fatalf("retry mode = %q, want single attempt", req.RetryMode)
	}
	if len(req.Tools) != 0 || req.ToolChoice != "" || req.ParallelToolCalls != nil {
		t.Fatalf("tool fields = tools:%v choice:%q parallel:%v, want none", req.Tools, req.ToolChoice, req.ParallelToolCalls)
	}
	if req.PromptCache != nil || req.CacheControl != nil || req.PromptCacheKey != "" || req.PromptCacheRetention != "" {
		t.Fatalf("cache fields are populated: %+v", req)
	}
	if req.SessionID != "" || len(req.Metadata) != 0 || len(req.Trace) != 0 || req.ReviewSnapshot != nil {
		t.Fatalf("continuation/session fields are populated: %+v", req)
	}
	if len(req.Transforms) != 0 {
		t.Fatalf("transforms = %v, want none", req.Transforms)
	}
	jsonSchema, ok := req.ResponseFormat["json_schema"].(map[string]any)
	if req.ResponseFormat["type"] != "json_schema" || !ok || jsonSchema["strict"] != true || jsonSchema["name"] != "generate_commit" {
		t.Fatalf("response format = %#v, want strict generate_commit JSON schema", req.ResponseFormat)
	}
	if result == nil || result.ToolCall == nil || result.ToolCall.Name != "generate_commit" {
		t.Fatalf("result = %#v, want synthetic generate_commit result", result)
	}
}

func TestOpenRouterJSONInvoker_DoesNotRetryErrors(t *testing.T) {
	wantErr := errors.New("transient provider failure")
	client := &capturingOpenRouterJSONClient{err: wantErr}
	invoker, err := NewOpenRouterJSONInvoker(InvokerConfig{
		Client:   client,
		Model:    "openai/gpt-5.6-luna-pro",
		Provider: "openrouter",
	})
	if err != nil {
		t.Fatalf("NewOpenRouterJSONInvoker: %v", err)
	}
	_, _, err = invoker.Invoke(context.Background(), "system", "user", tools.Definition{
		Name:       "generate_commit",
		Parameters: tools.ObjectSchema(map[string]tools.Property{}),
	}, nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Invoke error = %v, want %v", err, wantErr)
	}
	if len(client.requests) != 1 {
		t.Fatalf("requests = %d, want exactly one", len(client.requests))
	}
}

func TestNewOpenRouterJSONInvoker_RejectsOtherProviders(t *testing.T) {
	_, err := NewOpenRouterJSONInvoker(InvokerConfig{
		Client:   &capturingOpenRouterJSONClient{},
		Model:    "openai/gpt-5.6-luna-pro",
		Provider: "codex",
	})
	if err == nil {
		t.Fatal("expected non-OpenRouter provider to be rejected")
	}
}
