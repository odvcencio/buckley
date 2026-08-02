package tui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"m31labs.dev/buckley/v2/pkg/conversation"
	"m31labs.dev/buckley/v2/pkg/model"
	"m31labs.dev/buckley/v2/pkg/storage"
	"m31labs.dev/buckley/v2/pkg/tool"
	"m31labs.dev/fluffyui/backend/sim"
)

// TestRunToolLoop_SecondRequestCarriesToolCallAndResult is the cross-round
// transcript invariant carried over from prior agentloop.Controller
// migrations (see pkg/headless's TestRunner_SecondRequestCarriesToolCallAndResult):
// after round one dispatches a tool, round two's wire request must contain
// both the assistant tool-call message and its tool result, or the model
// loops on stale history. This is the sharpest possible check that
// newToolLoopController's History sink (recordToolLoopCalls +
// AddToolResponseMessage) actually lands in sess.Conversation before the
// next round's BuildRequest reads it.
func TestRunToolLoop_SecondRequestCarriesToolCallAndResult(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		write := func(payload string) {
			_, _ = io.WriteString(w, "data: "+payload+"\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
		if len(bodies) == 1 {
			write(`{"id":"r1","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_hist_1","type":"function","function":{"name":"echo","arguments":"{\"text\":\"hi\"}"}}]},"finish_reason":null}]}`)
			write(`{"id":"r1","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
		} else {
			write(`{"id":"r2","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"done"},"finish_reason":null}]}`)
			write(`{"id":"r2","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
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
	conv.AddUserMessage("please echo hi")
	sess := &SessionState{ID: "session-1", Conversation: conv, ToolRegistry: registry}

	ctrl := &Controller{app: app, cfg: cfg, modelMgr: mgr, workDir: t.TempDir()}

	if _, _, _, _, err := ctrl.runToolLoop(context.Background(), sess, "gpt-4o"); err != nil {
		t.Fatalf("runToolLoop error: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("expected 2 model requests, got %d", len(bodies))
	}

	var second map[string]any
	if err := json.Unmarshal([]byte(bodies[1]), &second); err != nil {
		t.Fatalf("decode second request: %v", err)
	}
	raw := bodies[1]
	if !strings.Contains(raw, "call_hist_1") {
		t.Fatalf("second request missing assistant tool-call message: %s", raw)
	}
	if !strings.Contains(raw, `"role":"tool"`) {
		t.Fatalf("second request missing tool result message: %s", raw)
	}
}

// TestRunToolLoop_ReloadedTranscriptMatchesInMemory is the persistence
// invariant carried over from prior agentloop.Controller migrations: after
// a full turn (tool call, tool result, final answer) completes, a fresh
// Conversation reloaded from storage must match the in-memory one message
// for message. This is what proves the History sink neither drops a
// message (a reload missing the tool exchange) nor double-appends one (the
// final answer persisted twice -- the exact bug class the streaming
// no-double-render invariant guards against on the render side).
func TestRunToolLoop_ReloadedTranscriptMatchesInMemory(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		write := func(payload string) {
			_, _ = io.WriteString(w, "data: "+payload+"\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
		if len(bodies) == 1 {
			write(`{"id":"r1","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"Let me check that."},"finish_reason":null}]}`)
			write(`{"id":"r1","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"echo","arguments":"{}"}}]},"finish_reason":null}]}`)
			write(`{"id":"r1","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
		} else {
			write(`{"id":"r2","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"Done: 42 files."},"finish_reason":null}]}`)
			write(`{"id":"r2","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
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

	store, err := storage.New(t.TempDir() + "/invariants.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSession("session-1"); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}

	registry := tool.NewEmptyRegistry()
	registry.Register(repairNameTool{name: "echo"})
	conv := conversation.New("session-1")
	conv.AddUserMessage("How many files?")
	if err := conv.SaveMessage(store, conv.Messages[0]); err != nil {
		t.Fatalf("save user message: %v", err)
	}
	sess := &SessionState{ID: "session-1", Conversation: conv, ToolRegistry: registry}

	ctrl := &Controller{app: app, cfg: cfg, modelMgr: mgr, store: store, workDir: t.TempDir()}

	if _, _, _, _, err := ctrl.runToolLoop(context.Background(), sess, "gpt-4o"); err != nil {
		t.Fatalf("runToolLoop error: %v", err)
	}

	reloaded := conversation.New("session-1")
	if err := reloaded.LoadFromStorage(store); err != nil {
		t.Fatalf("reload conversation: %v", err)
	}

	if len(reloaded.Messages) != len(conv.Messages) {
		t.Fatalf("reloaded message count = %d, want %d (in-memory): reloaded=%+v inMemory=%+v",
			len(reloaded.Messages), len(conv.Messages), roleList(reloaded.Messages), roleList(conv.Messages))
	}
	for i := range conv.Messages {
		want := conv.Messages[i]
		got := reloaded.Messages[i]
		if got.Role != want.Role {
			t.Fatalf("message %d role = %q, want %q", i, got.Role, want.Role)
		}
		if conversation.GetContentAsString(got.Content) != conversation.GetContentAsString(want.Content) {
			t.Fatalf("message %d content = %q, want %q", i, conversation.GetContentAsString(got.Content), conversation.GetContentAsString(want.Content))
		}
		if len(got.ToolCalls) != len(want.ToolCalls) {
			t.Fatalf("message %d tool call count = %d, want %d", i, len(got.ToolCalls), len(want.ToolCalls))
		}
		if got.ToolCallID != want.ToolCallID || got.Name != want.Name {
			t.Fatalf("message %d tool identity = (%q,%q), want (%q,%q)", i, got.ToolCallID, got.Name, want.ToolCallID, want.Name)
		}
	}

	// The exact shape this invariant exists to catch: one tool-call message,
	// one tool result, one final assistant answer -- no duplicates from a
	// History sink that also re-appended what finishToolLoopResponse
	// already persisted.
	toolCalls, toolResults, finals := 0, 0, 0
	for _, m := range reloaded.Messages {
		switch {
		case m.Role == "assistant" && len(m.ToolCalls) > 0:
			toolCalls++
		case m.Role == "tool":
			toolResults++
		case m.Role == "assistant":
			finals++
		}
	}
	if toolCalls != 1 || toolResults != 1 || finals != 1 {
		t.Fatalf("persisted transcript wrong: toolCalls=%d toolResults=%d finals=%d", toolCalls, toolResults, finals)
	}
}

func roleList(msgs []conversation.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Role
	}
	return out
}
