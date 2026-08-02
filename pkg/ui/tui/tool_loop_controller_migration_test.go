package tui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

	"m31labs.dev/buckley/v2/pkg/conversation"
	"m31labs.dev/buckley/v2/pkg/model"
	"m31labs.dev/buckley/v2/pkg/tool"
	"m31labs.dev/fluffyui/backend/sim"
)

// TestRunToolLoopControllerStopsOnExactRepeatAndSynthesizes proves the
// agentloop.Controller-driven loop still consults the shared Governor every
// round: a model that repeats the exact same tool call/result three times
// trips the default ExactRepeatLimit, Controller reports
// FinishReasonLoopGuard, and finishGuardedToolLoop's existing
// recovery attempt (one more tools-off request) still produces the final
// answer -- exactly as the pre-engine loop did.
func TestRunToolLoopControllerStopsOnExactRepeatAndSynthesizes(t *testing.T) {
	var callCount int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		write := func(payload string) {
			_, _ = io.WriteString(w, "data: "+payload+"\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}

		n := atomic.AddInt32(&callCount, 1)
		if n <= 3 {
			write(`{"id":"r","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-` + strconv.Itoa(int(n)) + `","type":"function","function":{"name":"echo","arguments":"{}"}}]},"finish_reason":null}]}`)
			write(`{"id":"r","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
		} else {
			write(`{"id":"r","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"Stopping due to repeats."},"finish_reason":null}]}`)
			write(`{"id":"r","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		}
		write(`[DONE]`)
	}))
	defer server.Close()

	cfg := newStreamIntegrationConfig(server.URL)
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	app, err := NewWidgetApp(WidgetAppConfig{Backend: sim.New(80, 24)})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}

	registry := tool.NewEmptyRegistry()
	registry.Register(repairNameTool{name: "echo"})
	conv := conversation.New("session-1")
	conv.AddUserMessage("keep trying the same thing")
	sess := &SessionState{ID: "session-1", Conversation: conv, ToolRegistry: registry}

	ctrl := &Controller{app: app, cfg: cfg, modelMgr: mgr, workDir: t.TempDir()}

	text, _, finishReason, _, err := ctrl.runToolLoop(context.Background(), sess, "gpt-4o")
	if err != nil {
		t.Fatalf("runToolLoop error: %v", err)
	}
	if finishReason != "loop_guard" {
		t.Fatalf("finishReason = %q, want loop_guard", finishReason)
	}
	if text != "Stopping due to repeats." {
		t.Fatalf("final text = %q, want the guard-recovery synthesis text", text)
	}
	if atomic.LoadInt32(&callCount) != 4 {
		t.Fatalf("expected exactly 4 requests (3 repeats + 1 recovery), got %d", callCount)
	}

	// The three identical tool calls, and their results, are all persisted
	// (the guard stops the LOOP, not history for calls already dispatched).
	toolMsgs := 0
	for _, m := range conv.Messages {
		if m.Role == "tool" {
			toolMsgs++
		}
	}
	if toolMsgs != 3 {
		t.Fatalf("expected 3 persisted tool results, got %d", toolMsgs)
	}
}

// TestRunToolLoopControllerRetriesWithoutToolsOnUnsupportedError proves
// the reactive tools-off fallback (handleToolLoopModelError,
// isToolUnsupportedError) still works when CallModel's error propagates
// through agentloop.Controller.Run: the first request advertises tools and
// the provider rejects them mid-stream; runToolLoop's retry loop turns
// state.useTools off and calls ctrl.Run again, which succeeds without
// tools -- matching the pre-engine reactive fallback exactly.
func TestRunToolLoopControllerRetriesWithoutToolsOnUnsupportedError(t *testing.T) {
	var requestsWithTools, requestsWithoutTools int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		_ = json.Unmarshal(body, &decoded)
		tools, hasTools := decoded["tools"].([]any)

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		write := func(payload string) {
			_, _ = io.WriteString(w, "data: "+payload+"\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}

		if hasTools && len(tools) > 0 {
			atomic.AddInt32(&requestsWithTools, 1)
			write(`{"id":"r","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":null}],"error":{"message":"model does not support tool calling"}}`)
			write(`[DONE]`)
			return
		}
		atomic.AddInt32(&requestsWithoutTools, 1)
		write(`{"id":"r","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"No tools, but the answer is 42."},"finish_reason":null}]}`)
		write(`{"id":"r","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		write(`[DONE]`)
	}))
	defer server.Close()

	cfg := newStreamIntegrationConfig(server.URL)
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	app, err := NewWidgetApp(WidgetAppConfig{Backend: sim.New(80, 24)})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}

	registry := tool.NewEmptyRegistry()
	registry.Register(repairNameTool{name: "echo"})
	conv := conversation.New("session-1")
	conv.AddUserMessage("answer please")
	sess := &SessionState{ID: "session-1", Conversation: conv, ToolRegistry: registry}

	ctrl := &Controller{app: app, cfg: cfg, modelMgr: mgr, workDir: t.TempDir()}

	text, _, _, _, err := ctrl.runToolLoop(context.Background(), sess, "gpt-4o")
	if err != nil {
		t.Fatalf("runToolLoop error: %v", err)
	}
	if text != "No tools, but the answer is 42." {
		t.Fatalf("final text = %q", text)
	}
	if atomic.LoadInt32(&requestsWithTools) != 1 {
		t.Fatalf("expected exactly 1 request advertising tools, got %d", requestsWithTools)
	}
	if atomic.LoadInt32(&requestsWithoutTools) != 1 {
		t.Fatalf("expected exactly 1 retry request without tools, got %d", requestsWithoutTools)
	}

	last := conv.Messages[len(conv.Messages)-1]
	if last.Role != "assistant" || conversation.GetContentAsString(last.Content) != text {
		t.Fatalf("final answer not persisted after tools-off retry: %+v", last)
	}
}

// TestRunToolLoopControllerExhaustsContextRetriesThenFails proves the
// context-length shrink-and-retry fallback (handleToolLoopContextError,
// state.contextScale/contextRetries) is still bounded to two retries when
// wired through agentloop.Controller: a provider that always reports a
// context-length error causes exactly three requests (the original attempt
// plus two shrunk retries) before runToolLoop gives up and returns the
// error to its caller, exactly like the pre-engine loop's third-strike
// behavior.
func TestRunToolLoopControllerExhaustsContextRetriesThenFails(t *testing.T) {
	var requestCount int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, `data: {"id":"r","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":null}],"error":{"message":"maximum context length exceeded"}}`+"\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	cfg := newStreamIntegrationConfig(server.URL)
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	app, err := NewWidgetApp(WidgetAppConfig{Backend: sim.New(80, 24)})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}

	conv := conversation.New("session-1")
	conv.AddUserMessage("hello")
	sess := &SessionState{ID: "session-1", Conversation: conv}

	ctrl := &Controller{app: app, cfg: cfg, modelMgr: mgr, workDir: t.TempDir()}

	_, _, _, _, err = ctrl.runToolLoop(context.Background(), sess, "gpt-4o")
	if err == nil {
		t.Fatal("expected the third context-length error to be returned to the caller")
	}
	if got := atomic.LoadInt32(&requestCount); got != 3 {
		t.Fatalf("expected exactly 3 requests (original + 2 shrunk retries), got %d", got)
	}
}
