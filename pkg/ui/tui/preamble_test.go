package tui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"m31labs.dev/buckley/v2/pkg/conversation"
	"m31labs.dev/buckley/v2/pkg/model"
	"m31labs.dev/buckley/v2/pkg/tool"
	"m31labs.dev/fluffyui/backend/sim"
)

// TestRunToolLoopPreservesPreambleAcrossToolCallAndWireHistory drives a
// two-round turn (preamble text, then a tool call, then a final answer)
// through a real multi-request SSE server and proves the preamble survives
// end to end: it is persisted alongside the tool-call message instead of
// the old hardcoded "", it renders as its own streamed transcript bubble,
// and -- the wire-history consequence that makes preservation meaningful --
// the SECOND request's message history actually carries it back to the
// model.
func TestRunToolLoopPreservesPreambleAcrossToolCallAndWireHistory(t *testing.T) {
	var mu sync.Mutex
	var requestBodies []map[string]any
	var callCount int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		mu.Lock()
		requestBodies = append(requestBodies, decoded)
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		write := func(payload string) {
			_, _ = io.WriteString(w, "data: "+payload+"\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}

		if atomic.AddInt32(&callCount, 1) == 1 {
			write(`{"id":"r1","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"Let me check that."},"finish_reason":null}]}`)
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

	registry := tool.NewEmptyRegistry()
	registry.Register(repairNameTool{name: "echo"})
	conv := conversation.New("session-1")
	conv.AddUserMessage("How many files?")
	sess := &SessionState{ID: "session-1", Conversation: conv, ToolRegistry: registry}

	ctrl := &Controller{app: app, cfg: cfg, modelMgr: mgr, workDir: t.TempDir()}

	text, _, finishReason, _, err := ctrl.runToolLoop(context.Background(), sess, "gpt-4o")
	if err != nil {
		t.Fatalf("runToolLoop error: %v", err)
	}
	if finishReason != "stop" {
		t.Fatalf("finishReason = %q, want stop", finishReason)
	}
	if text != "Done: 42 files." {
		t.Fatalf("final text = %q, want %q", text, "Done: 42 files.")
	}

	// Persisted conversation: user, assistant(preamble+tool_call), tool, assistant(final).
	msgs := conv.Messages
	if len(msgs) != 4 {
		t.Fatalf("expected 4 persisted messages, got %d: %+v", len(msgs), msgs)
	}
	assistant := msgs[1]
	if assistant.Role != "assistant" || conversation.GetContentAsString(assistant.Content) != "Let me check that." {
		t.Fatalf("preamble content not persisted: %+v", assistant)
	}
	if len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].Function.Name != "echo" {
		t.Fatalf("tool call not persisted alongside preamble: %+v", assistant.ToolCalls)
	}
	if msgs[2].Role != "tool" || msgs[2].Name != "echo" {
		t.Fatalf("tool result not persisted: %+v", msgs[2])
	}
	if msgs[3].Role != "assistant" || conversation.GetContentAsString(msgs[3].Content) != "Done: 42 files." {
		t.Fatalf("final answer not persisted: %+v", msgs[3])
	}

	// Wire-history consequence: the second request must carry the preamble
	// back to the model, not just keep it in local display state.
	mu.Lock()
	defer mu.Unlock()
	if len(requestBodies) != 2 {
		t.Fatalf("expected exactly 2 requests, got %d", len(requestBodies))
	}
	secondMessages, ok := requestBodies[1]["messages"].([]any)
	if !ok {
		t.Fatalf("second request missing messages field: %#v", requestBodies[1])
	}
	foundPreamble := false
	for _, raw := range secondMessages {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if content, ok := m["content"].(string); ok && strings.Contains(content, "Let me check that.") {
			foundPreamble = true
		}
	}
	if !foundPreamble {
		t.Fatalf("second request's wire history did not carry the preamble: %#v", secondMessages)
	}

	// Render: the preamble streams into its own bubble, and streamed text
	// covers both the preamble and the final answer.
	seeds := 0
	var streamedText strings.Builder
	for _, m := range drainAllMessages(app) {
		switch v := m.(type) {
		case AddMessageMsg:
			if v.Source == "assistant" && v.Content == "" {
				seeds++
			}
		case StreamChunk:
			streamedText.WriteString(v.Text)
		}
	}
	if seeds < 2 {
		t.Fatalf("expected >=2 seeded assistant bubbles (preamble + final), got %d", seeds)
	}
	if !strings.Contains(streamedText.String(), "Let me check that.") || !strings.Contains(streamedText.String(), "Done: 42 files.") {
		t.Fatalf("expected both preamble and final answer to render, got %q", streamedText.String())
	}
}
