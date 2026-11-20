package model

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// commitTool is the shape of the commit path's structured-message tool.
var commitTool = map[string]any{
	"type": "function",
	"function": map[string]any{
		"name":        "submit_commit",
		"description": "Submit the commit message.",
		"parameters": map[string]any{
			"type":       "object",
			"properties": map[string]any{"subject": map[string]any{"type": "string"}},
			"required":   []string{"subject"},
		},
	},
}

func TestOpenAIChatPayloadSendsOnlyOpenAIFields(t *testing.T) {
	include := true
	req := ChatRequest{
		Model:            "gpt-6.2-sol",
		Messages:         []Message{{Role: "user", Content: "hi"}},
		MaxTokens:        100,
		SessionID:        "inv-1",
		Trace:            map[string]string{"trace_id": "inv-1"},
		Metadata:         map[string]string{"k": "v"},
		Transforms:       []string{"middle-out"},
		Models:           []string{"a", "b"},
		Provider:         map[string]any{"only": []string{"openai"}},
		Usage:            &UsageRequestOptions{Include: true},
		IncludeReasoning: &include,
		CacheControl:     &CacheControl{Type: "ephemeral"},
		Reasoning:        &ReasoningConfig{Effort: "high"},
	}
	raw, err := json.Marshal(openAIChatPayload(req))
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"trace", "session_id", "transforms", "models", "provider", "usage", "include_reasoning", "cache_control", "reasoning", "max_tokens"} {
		if _, ok := wire[field]; ok {
			t.Errorf("OpenAI request carries %q: %s", field, raw)
		}
	}
	if wire["reasoning_effort"] != "high" || wire["max_completion_tokens"] != float64(100) {
		t.Fatalf("request = %s, want reasoning_effort high and max_completion_tokens 100", raw)
	}

	// Older chat models take no reasoning_effort, and turning reasoning off
	// sends none at all (GPT-6.2 rejects "none").
	off := false
	for _, tt := range []ChatRequest{
		{Model: "gpt-4o", Reasoning: &ReasoningConfig{Effort: "high"}},
		{Model: "gpt-6.2-sol", Reasoning: &ReasoningConfig{Effort: "low", Enabled: &off}},
	} {
		raw, _ := json.Marshal(openAIChatPayload(tt))
		if strings.Contains(string(raw), "reasoning") {
			t.Errorf("%s: request %s carries reasoning", tt.Model, raw)
		}
	}
}

func TestOpenAIGPT6ToolRequestsUseTheResponsesAPI(t *testing.T) {
	if !openAIUsesResponses(ChatRequest{Model: "openai/gpt-6.2-astra", Tools: []map[string]any{commitTool}}) ||
		openAIUsesResponses(ChatRequest{Model: "gpt-6.2-sol"}) ||
		openAIUsesResponses(ChatRequest{Model: "gpt-5.5", Tools: []map[string]any{commitTool}}) {
		t.Fatal("wrong API chosen")
	}

	var paths []string
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &body)
		_, _ = io.WriteString(w, `{"id":"resp_1","model":"gpt-6.2-sol","status":"completed","output":[{"type":"function_call","call_id":"call_1","name":"submit_commit","arguments":"{\"subject\":\"add: x\"}"}],"usage":{"input_tokens":1000,"output_tokens":50,"total_tokens":1050,"input_tokens_details":{"cached_tokens":800,"cache_write_tokens":100}}}`)
	}))
	defer server.Close()
	off := false
	provider := NewOpenAIProvider("test", server.URL, false)
	resp, err := provider.ChatCompletion(context.Background(), ChatRequest{
		Model:       "gpt-6.2-sol",
		Messages:    []Message{{Role: "system", Content: "rules"}, {Role: "user", Content: "diff"}},
		Tools:       []map[string]any{commitTool},
		ToolChoice:  "required",
		Temperature: 0.2,
		MaxTokens:   4096,
		Reasoning:   &ReasoningConfig{Enabled: &off},
		Trace:       map[string]string{"trace_id": "inv-1"},
		Metadata:    map[string]string{"k": "v"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != "/responses" {
		t.Fatalf("paths = %v, want one Responses API call", paths)
	}
	for _, field := range []string{"temperature", "trace", "reasoning", "messages"} {
		if _, ok := body[field]; ok {
			t.Errorf("Responses request carries %q: %v", field, body)
		}
	}
	if body["tool_choice"] != "required" || body["max_output_tokens"] != float64(4096) {
		t.Fatalf("Responses request = %v", body)
	}
	calls := resp.Choices[0].Message.ToolCalls
	if len(calls) != 1 || calls[0].Function.Name != "submit_commit" || resp.Usage.CacheWrites() != 100 || resp.Usage.PromptTokensDetails.CachedTokens != 800 {
		t.Fatalf("response = %#v", resp)
	}
}

func TestAnthropicForcedToolIsAskedForOnModelsThatRejectIt(t *testing.T) {
	off := false
	req := ChatRequest{
		Model:      "anthropic/claude-sonnet-5-5",
		Messages:   []Message{{Role: "system", Content: "Write a commit message."}, {Role: "user", Content: "diff"}},
		Tools:      []map[string]any{commitTool},
		ToolChoice: "required",
		Reasoning:  &ReasoningConfig{Enabled: &off},
	}
	anthReq, err := (&AnthropicProvider{}).toAnthropicRequest(req, false)
	if err != nil {
		t.Fatal(err)
	}
	if anthReq.ToolChoice == nil || anthReq.ToolChoice.Type != "auto" || anthReq.requiredTool != "required" {
		t.Fatalf("tool_choice = %#v, required = %q; want auto and a required tool", anthReq.ToolChoice, anthReq.requiredTool)
	}
	if anthReq.Thinking != nil {
		t.Fatal("thinking disabled was sent to a model that rejects it")
	}
	if system, _ := anthReq.System.(string); !strings.Contains(system, "Respond only by calling one of the provided tools") {
		t.Fatalf("system = %q, want the tool instruction", anthReq.System)
	}
	// Haiku 5.5 accepts a forced tool, so it still gets one.
	req.Model = "anthropic/claude-haiku-5-5"
	anthReq, err = (&AnthropicProvider{}).toAnthropicRequest(req, false)
	if err != nil || anthReq.ToolChoice.Type != "any" || anthReq.requiredTool != "" {
		t.Fatalf("haiku: %#v, %v", anthReq, err)
	}
}

func TestAnthropicRetriesOnceWhenNoToolCallComes(t *testing.T) {
	for _, tt := range []struct {
		name      string
		responses []string
		wantCalls int
		wantErr   string
	}{
		{"called first time", []string{anthropicToolUseReply}, 1, ""},
		{"called on retry", []string{anthropicTextReply, anthropicToolUseReply}, 2, ""},
		{"never called", []string{anthropicTextReply, anthropicTextReply}, 2, "did not call the required tool"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			var bodies []map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				bodies = append(bodies, body)
				_, _ = io.WriteString(w, tt.responses[len(bodies)-1])
			}))
			defer server.Close()
			provider := NewAnthropicProvider("test", server.URL, false)
			resp, err := provider.ChatCompletion(context.Background(), ChatRequest{
				Model:      "anthropic/claude-opus-5-5",
				Messages:   []Message{{Role: "user", Content: "diff"}},
				Tools:      []map[string]any{commitTool},
				ToolChoice: "submit_commit",
			})
			if len(bodies) != tt.wantCalls {
				t.Fatalf("calls = %d, want %d", len(bodies), tt.wantCalls)
			}
			for _, body := range bodies {
				if choice, _ := body["tool_choice"].(map[string]any); choice["type"] != "auto" {
					t.Fatalf("tool_choice = %v, want auto", body["tool_choice"])
				}
			}
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if calls := resp.Choices[0].Message.ToolCalls; len(calls) != 1 || calls[0].Function.Name != "submit_commit" {
				t.Fatalf("tool calls = %#v", calls)
			}
			if tt.wantCalls == 2 {
				messages, _ := bodies[1]["messages"].([]any)
				if len(messages) != 3 {
					t.Fatalf("retry messages = %v, want the question, the text answer and a request for the tool", messages)
				}
				if resp.Usage.PromptTokens != 200 || resp.Usage.CompletionTokens != 20 {
					t.Fatalf("usage = %#v, want both attempts counted", resp.Usage)
				}
			}
		})
	}
}

const (
	anthropicTextReply    = `{"id":"m1","model":"claude-opus-5-5","content":[{"type":"text","text":"add: x"}],"stop_reason":"end_turn","usage":{"input_tokens":100,"output_tokens":10}}`
	anthropicToolUseReply = `{"id":"m2","model":"claude-opus-5-5","content":[{"type":"tool_use","id":"t1","name":"submit_commit","input":{"subject":"add: x"}}],"stop_reason":"tool_use","usage":{"input_tokens":100,"output_tokens":10}}`
)

func TestOpenAIProModelsSendReasoningModePro(t *testing.T) {
	for _, tt := range []struct{ requested, base string }{
		{"openai/gpt-6.1-terra-pro", "gpt-6.1-terra"},
		{"gpt-6-luna-pro", "gpt-6-luna"},
		{"gpt-6.2-astra-pro", "gpt-6.2-astra"},
	} {
		var paths []string
		var body map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			paths = append(paths, r.URL.Path)
			_ = json.NewDecoder(r.Body).Decode(&body)
			_, _ = io.WriteString(w, `{"id":"resp_1","model":"x","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15,"output_tokens_details":{"reasoning_tokens":3}}}`)
		}))
		resp, err := NewOpenAIProvider("test", server.URL, false).ChatCompletion(context.Background(), ChatRequest{
			Model:     tt.requested,
			Messages:  []Message{{Role: "user", Content: "q"}},
			Reasoning: &ReasoningConfig{Effort: "high"},
		})
		server.Close()
		if err != nil {
			t.Fatalf("%s: %v", tt.requested, err)
		}
		reasoning, _ := body["reasoning"].(map[string]any)
		if len(paths) != 1 || paths[0] != "/responses" || body["model"] != tt.base || reasoning["mode"] != "pro" || reasoning["effort"] != "high" {
			t.Fatalf("%s: paths %v body %v, want %s on /responses with reasoning.mode pro", tt.requested, paths, body, tt.base)
		}
		if resp.Usage.CompletionTokenDetails.ReasoningTokens != 3 {
			t.Fatalf("%s: reasoning tokens lost: %#v", tt.requested, resp.Usage)
		}
	}
	// GPT-5 pro models are real model IDs and are not rewritten.
	if base, pro := openAIProModel("gpt-5.5-pro"); pro || base != "gpt-5.5-pro" {
		t.Fatal("gpt-5.5-pro was rewritten")
	}
	info, err := (&OpenAIProvider{}).GetModelInfo("gpt-6.1-terra-pro")
	if err != nil || info.Pricing.Prompt != 1 || info.Pricing.CacheRead != 0.05 || !info.PricingKnown {
		t.Fatalf("terra pro info = %#v, %v; want Terra's prices", info, err)
	}
}
