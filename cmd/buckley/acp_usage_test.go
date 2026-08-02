package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"m31labs.dev/buckley/pkg/acp"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/rules"
	"m31labs.dev/buckley/pkg/tool"
)

// TestSendACPUsageUpdate_SendsUsedAndSize locks N1's wire content: used
// comes from the round's model.Usage.TotalTokens, size from the model's
// context window, and cost stays unset (Buckley has no per-request cost
// figure available at this call site).
func TestSendACPUsageUpdate_SendsUsedAndSize(t *testing.T) {
	t.Parallel()

	var got []acp.SessionUpdate
	sendACPUsageUpdate(func(update acp.SessionUpdate) error {
		got = append(got, update)
		return nil
	}, &model.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150}, 128000)

	if len(got) != 1 {
		t.Fatalf("updates = %+v, want 1", got)
	}
	u := got[0]
	if u.SessionUpdate != acp.SessionUpdateUsageUpdate {
		t.Fatalf("sessionUpdate = %q, want %q", u.SessionUpdate, acp.SessionUpdateUsageUpdate)
	}
	if u.UsageUsed == nil || *u.UsageUsed != 150 {
		t.Fatalf("UsageUsed = %v, want 150", u.UsageUsed)
	}
	if u.UsageSize == nil || *u.UsageSize != 128000 {
		t.Fatalf("UsageSize = %v, want 128000", u.UsageSize)
	}
	if u.UsageCost != nil {
		t.Fatalf("UsageCost = %+v, want nil", u.UsageCost)
	}
}

// TestSendACPUsageUpdate_NoOpWithoutUsageOrContextWindow guards against a
// misleading zero/zero usage_update when either input is unavailable.
func TestSendACPUsageUpdate_NoOpWithoutUsageOrContextWindow(t *testing.T) {
	t.Parallel()

	var calls int
	stream := func(acp.SessionUpdate) error {
		calls++
		return nil
	}

	sendACPUsageUpdate(stream, nil, 128000)
	sendACPUsageUpdate(stream, &model.Usage{TotalTokens: 10}, 0)

	if calls != 0 {
		t.Fatalf("stream called %d times, want 0", calls)
	}
}

// TestRunACPLoop_EmitsUsageUpdateAfterTurn is N1's acceptance test: a full
// prompt turn through runACPLoop must emit a usage_update session update
// once the round's usage is available, carrying the token count the fake
// provider reported.
func TestRunACPLoop_EmitsUsageUpdateAfterTurn(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: "+
			`{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"42"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":120,"completion_tokens":30,"total_tokens":150}}`+
			"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	t.Cleanup(server.Close)

	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai"

	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// A real rules engine is required here (unlike the S1/S6 tests, which
	// pick a model unknown to the static catalog to keep the tool-turn path
	// disabled): "gpt-4o" is in the OpenAI provider's static catalog with a
	// known context length, which this test needs for a non-zero usage
	// window, but that also makes SupportsTools true and exercises the
	// governed tool-turn path, which needs a non-nil evaluator.
	engine, err := rules.NewDefaultEngine()
	if err != nil {
		t.Fatalf("rules.NewDefaultEngine: %v", err)
	}

	conv := conversation.New("session-1")
	conv.AddUserMessage("what is the answer?")
	registry := tool.NewEmptyRegistry()
	collector := &collectingStream{}

	if _, err := runACPLoop(context.Background(), cfg, mgr, conv, registry, nil, engine, "gpt-4o", "", "session-1", nil, func(string, ...interface{}) {}, collector.fn); err != nil {
		t.Fatalf("runACPLoop: %v", err)
	}

	var usageUpdates []acp.SessionUpdate
	for _, u := range collector.updates {
		if u.SessionUpdate == acp.SessionUpdateUsageUpdate {
			usageUpdates = append(usageUpdates, u)
		}
	}
	if len(usageUpdates) != 1 {
		t.Fatalf("usage_update count = %d, want 1 (updates=%#v)", len(usageUpdates), collector.updates)
	}
	got := usageUpdates[0]
	if got.UsageUsed == nil || *got.UsageUsed != 150 {
		t.Fatalf("UsageUsed = %v, want 150", got.UsageUsed)
	}
	if got.UsageSize == nil || *got.UsageSize != 128000 {
		t.Fatalf("UsageSize = %v, want 128000 (gpt-4o's static context length)", got.UsageSize)
	}
}
