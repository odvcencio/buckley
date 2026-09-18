package tui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
	"m31labs.dev/fluffyui/backend/sim"
)

type countingRepairNameTool struct {
	repairNameTool
	calls int32
}

func (t *countingRepairNameTool) Execute(params map[string]any) (*builtin.Result, error) {
	atomic.AddInt32(&t.calls, 1)
	return t.repairNameTool.Execute(params)
}

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

	if _, err := ctrl.runToolLoop(context.Background(), sess, "gpt-4o"); err != nil {
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

func TestRunToolLoop_ToolOfferPolicyRejectsSurpriseCallsForRouteSelectedKnownToollessModel(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "text/event-stream")
		if len(bodies) == 1 {
			var first map[string]any
			if err := json.Unmarshal(body, &first); err != nil {
				t.Fatalf("decode first request: %v", err)
			}
			if _, ok := first["tools"]; ok {
				t.Fatalf("first request included tools for known toolless model: %s", body)
			}
			if _, ok := first["tool_choice"]; ok {
				t.Fatalf("first request included tool_choice for known toolless model: %s", body)
			}
			writeSSEToolLoopTest(t, w,
				`{"id":"r1","model":"o1-mini","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_surprise","type":"function","function":{"name":"echo","arguments":"{\"text\":\"should not run\"}"}}]},"finish_reason":null}]}`,
				`{"id":"r1","model":"o1-mini","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			)
			return
		}
		writeSSEToolLoopTest(t, w,
			`{"id":"r2","model":"o1-mini","choices":[{"index":0,"delta":{"role":"assistant","content":"I could not run that tool."},"finish_reason":null}]}`,
			`{"id":"r2","model":"o1-mini","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		)
	}))
	defer server.Close()

	cfg := newStreamIntegrationConfig(server.URL)
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	mgr.RoutingHooks().Register(func(decision *model.RoutingDecision) *model.RoutingDecision {
		if decision != nil && decision.RequestedModel == "alias-o1-mini" {
			decision.SelectedModel = "openai/o1-mini"
		}
		return decision
	})
	app, err := NewWidgetApp(WidgetAppConfig{Backend: sim.New(80, 24)})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}
	echo := &countingRepairNameTool{repairNameTool: repairNameTool{name: "echo"}}
	registry := tool.NewEmptyRegistry()
	registry.Register(echo)
	conv := conversation.New("session-toolless")
	conv.AddUserMessage("please echo hi")
	sess := &SessionState{ID: "session-toolless", Conversation: conv, ToolRegistry: registry}
	ctrl := &Controller{app: app, cfg: cfg, modelMgr: mgr, workDir: t.TempDir()}

	result, err := ctrl.runToolLoop(context.Background(), sess, "alias-o1-mini")
	var incomplete *agentloop.IncompleteTurnError
	if !errors.As(err, &incomplete) {
		t.Fatalf("runToolLoop error = %T %v, want *agentloop.IncompleteTurnError", err, err)
	}
	if incomplete.FinishReason != agentloop.FinishReasonInvalidCompletion || incomplete.Code != "unoffered_tool_call" {
		t.Fatalf("incomplete result = %+v, want invalid unoffered-tool-call completion", incomplete)
	}
	if !strings.Contains(result.Text, "unexecuted tool call") {
		t.Fatalf("incomplete result text = %q, want retained public unexecuted-call evidence", result.Text)
	}
	if got := atomic.LoadInt32(&echo.calls); got != 0 {
		t.Fatalf("tool executions = %d, want 0", got)
	}
	if len(bodies) != 1 {
		t.Fatalf("model requests = %d, want only the unsafe response-producing request", len(bodies))
	}
	var toolHistory, toolResults int
	for _, msg := range conv.Messages {
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			toolHistory++
		}
		if msg.Role == "tool" {
			toolResults++
		}
	}
	if toolHistory != 0 || toolResults != 0 {
		t.Fatalf("tool history=%d tool results=%d, want no history mutation after unoffered call", toolHistory, toolResults)
	}
	if finals := assistantFinals(conv.Messages); len(finals) != 0 {
		t.Fatalf("persisted assistant finals = %v, want no accepted final after an unoffered call", finals)
	}
}

func writeSSEToolLoopTest(t *testing.T, w http.ResponseWriter, payloads ...string) {
	t.Helper()
	flusher, _ := w.(http.Flusher)
	for _, payload := range payloads {
		if _, err := io.WriteString(w, "data: "+payload+"\n\n"); err != nil {
			t.Fatalf("write SSE payload: %v", err)
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
	if _, err := io.WriteString(w, "data: [DONE]\n\n"); err != nil {
		t.Fatalf("write SSE done: %v", err)
	}
	if flusher != nil {
		flusher.Flush()
	}
}

func TestRunToolLoop_ContextRetryAfterToolUsesLatestCumulativeUsage(t *testing.T) {
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := atomic.AddInt32(&requests, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		write := func(payload string) {
			_, _ = io.WriteString(w, "data: "+payload+"\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
		switch request {
		case 1:
			write(`{"id":"r-tool","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_usage_1","type":"function","function":{"name":"echo","arguments":"{}"}}]},"finish_reason":null}]}`)
			write(`{"id":"r-tool","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
			write(`{"id":"r-tool","model":"gpt-4o","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":1},"completion_tokens_details":{"reasoning_tokens":2},"cache_write_tokens":3,"estimated":true}}`)
		case 2:
			write(`{"id":"r-context","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":null}],"error":{"message":"maximum context length exceeded"}}`)
		case 3:
			write(`{"id":"r-final","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"done"},"finish_reason":null}]}`)
			write(`{"id":"r-final","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
			write(`{"id":"r-final","model":"gpt-4o","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":4,"total_tokens":16,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":5},"cache_write_tokens":6}}`)
		default:
			t.Errorf("unexpected provider request %d", request)
			write(`{"id":"unexpected","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"unexpected"},"finish_reason":null}]}`)
			write(`{"id":"unexpected","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
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
	echo := &countingRepairNameTool{repairNameTool: repairNameTool{name: "echo"}}
	registry.Register(echo)
	conv := conversation.New("session-1")
	conv.AddUserMessage("use a tool, recover from context shrink, then answer")
	sess := &SessionState{ID: "session-1", Conversation: conv, ToolRegistry: registry}
	ctrl := &Controller{app: app, cfg: cfg, modelMgr: mgr, workDir: t.TempDir()}

	result, err := ctrl.runToolLoop(context.Background(), sess, "gpt-4o")
	if err != nil {
		t.Fatalf("runToolLoop error: %v", err)
	}
	if result.Text != "done" {
		t.Fatalf("result text = %q, want done", result.Text)
	}
	if got := atomic.LoadInt32(&requests); got != 3 {
		t.Fatalf("provider requests = %d, want 3", got)
	}
	if got := atomic.LoadInt32(&echo.calls); got != 1 {
		t.Fatalf("tool executions = %d, want 1", got)
	}
	if result.Usage == nil {
		t.Fatal("result usage = nil")
	}
	if result.Usage.PromptTokens != 22 || result.Usage.CompletionTokens != 9 || result.Usage.TotalTokens != 31 {
		t.Fatalf("usage = %+v, want 22 prompt / 9 completion / 31 total without double-counting cumulative retry", *result.Usage)
	}
	if result.Usage.PromptTokensDetails == nil || result.Usage.PromptTokensDetails.CachedTokens != 5 {
		t.Fatalf("prompt details = %+v, want 5 cached tokens", result.Usage.PromptTokensDetails)
	}
	if result.Usage.CompletionTokenDetails == nil || result.Usage.CompletionTokenDetails.ReasoningTokens != 7 {
		t.Fatalf("completion details = %+v, want 7 reasoning tokens", result.Usage.CompletionTokenDetails)
	}
	if result.Usage.CacheWriteTokens != 9 || !result.Usage.Estimated {
		t.Fatalf("cache/estimated usage = cache_write:%d estimated:%v, want 9/true", result.Usage.CacheWriteTokens, result.Usage.Estimated)
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

	if _, err := ctrl.runToolLoop(context.Background(), sess, "gpt-4o"); err != nil {
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

func TestRunToolLoop_CompletionContractRepairsPrematureFinalAfterMutation(t *testing.T) {
	root := createTUITestGitRepo(t)
	trackedPath := filepath.Join(root, "test.txt")
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
		switch len(bodies) {
		case 1:
			write(`{"id":"r1","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_write","type":"function","function":{"name":"write_fixture","arguments":"{}"}}]},"finish_reason":null}]}`)
			write(`{"id":"r1","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
		case 2:
			write(`{"id":"r2","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"premature final"},"finish_reason":null}]}`)
			write(`{"id":"r2","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		case 3:
			write(`{"id":"r3","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_tests","type":"function","function":{"name":"run_tests","arguments":"{}"}}]},"finish_reason":null}]}`)
			write(`{"id":"r3","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
		default:
			write(`{"id":"r4","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"verified final"},"finish_reason":null}]}`)
			write(`{"id":"r4","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
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
	registry.Register(tuiContractTool{
		name:     "write_fixture",
		metadata: tool.ToolMetadata{Category: tool.CategoryFilesystem, Impact: tool.ImpactModifying},
		execute: func(map[string]any) (*builtin.Result, error) {
			return &builtin.Result{Success: true}, os.WriteFile(trackedPath, []byte("after\n"), 0o644)
		},
	})
	registry.Register(tuiContractTool{
		name:         "run_tests",
		metadata:     tool.ToolMetadata{Category: tool.CategoryTesting, Impact: tool.ImpactReadOnly},
		verification: true,
	})
	conv := conversation.New("session-1")
	conv.AddUserMessage("make a change")
	sess := &SessionState{ID: "session-1", Conversation: conv, ToolRegistry: registry}
	ctrl := &Controller{app: app, cfg: cfg, modelMgr: mgr, workDir: root}

	result, err := ctrl.runToolLoop(context.Background(), sess, "gpt-4o")
	if err != nil {
		t.Fatalf("runToolLoop error: %v", err)
	}
	ctrl.renderStreamResponse(result)
	if len(bodies) != 4 {
		t.Fatalf("model requests = %d, want edit, rejected final, verification repair, accepted final", len(bodies))
	}
	if !strings.Contains(bodies[2], "workspace changed after the last successful verification") {
		t.Fatalf("repair request missing completion instruction: %s", bodies[2])
	}
	if result.Text != "verified final" || !result.Streamed {
		t.Fatalf("result = %+v, want streamed verified final", result)
	}

	finals := assistantFinals(conv.Messages)
	if len(finals) != 1 || finals[0] != "verified final" {
		t.Fatalf("persisted assistant finals = %v, want accepted final only", finals)
	}
	leakedDraft := 0
	checkingStatuses := 0
	acceptedFlushes := 0
	for _, msg := range drainAllMessages(app) {
		switch v := msg.(type) {
		case AddMessageMsg:
			if strings.Contains(v.Content, "premature final") {
				leakedDraft++
			}
		case StreamFlush:
			if strings.Contains(v.Text, "premature final") {
				leakedDraft++
			}
			if strings.Contains(v.Text, "verified final") {
				acceptedFlushes++
			}
		case ReplaceLastMessageMsg:
			if strings.Contains(v.Content, "premature final") {
				leakedDraft++
			}
		case StatusMsg:
			if v.Text == "Checking changes" {
				checkingStatuses++
			}
		}
	}
	if leakedDraft != 0 {
		t.Fatalf("rejected draft appeared in app transcript %d time(s)", leakedDraft)
	}
	if checkingStatuses == 0 {
		t.Fatal("repair did not surface a concise checking status")
	}
	if acceptedFlushes != 1 {
		t.Fatalf("accepted final stream flushes = %d, want 1", acceptedFlushes)
	}
}

func TestRunToolLoop_CompletionContractExhaustionMarksIncompleteDraft(t *testing.T) {
	root := createTUITestGitRepo(t)
	trackedPath := filepath.Join(root, "test.txt")
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
			write(`{"id":"r1","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_write","type":"function","function":{"name":"write_fixture","arguments":"{}"}}]},"finish_reason":null}]}`)
			write(`{"id":"r1","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
		} else {
			write(`{"id":"r2","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"still unverified"},"finish_reason":null}]}`)
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
	registry.Register(tuiContractTool{
		name:     "write_fixture",
		metadata: tool.ToolMetadata{Category: tool.CategoryFilesystem, Impact: tool.ImpactModifying},
		execute: func(map[string]any) (*builtin.Result, error) {
			return &builtin.Result{Success: true}, os.WriteFile(trackedPath, []byte("after\n"), 0o644)
		},
	})
	conv := conversation.New("session-1")
	conv.AddUserMessage("make a change")
	sess := &SessionState{ID: "session-1", Conversation: conv, ToolRegistry: registry}
	ctrl := &Controller{app: app, cfg: cfg, modelMgr: mgr, workDir: root}

	result, err := ctrl.runToolLoop(context.Background(), sess, "gpt-4o")
	var incomplete *agentloop.IncompleteTurnError
	if !errors.As(err, &incomplete) || !strings.Contains(err.Error(), "missing successful verification after the latest workspace change") {
		t.Fatalf("runToolLoop error = %v, want completion-contract incomplete", err)
	}
	if !strings.Contains(result.Text, "still unverified") {
		t.Fatalf("incomplete result text = %q, want useful rejected content preserved", result.Text)
	}
	if finals := assistantFinals(conv.Messages); len(finals) != 0 {
		t.Fatalf("persisted assistant finals = %v, want none", finals)
	}
	leakedDraft := 0
	for _, msg := range drainAllMessages(app) {
		switch v := msg.(type) {
		case AddMessageMsg:
			if strings.Contains(v.Content, "still unverified") {
				leakedDraft++
			}
		case StreamFlush:
			if strings.Contains(v.Text, "still unverified") {
				leakedDraft++
			}
		case ReplaceLastMessageMsg:
			if strings.Contains(v.Content, "still unverified") {
				leakedDraft++
			}
		}
	}
	if leakedDraft != 0 {
		t.Fatalf("rejected final draft appeared in app transcript %d time(s)", leakedDraft)
	}
}

func TestStreamResponse_IncompleteResultRendersExplicitPreservedDraft(t *testing.T) {
	root := createTUITestGitRepo(t)
	trackedPath := filepath.Join(root, "test.txt")
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
			write(`{"id":"r1","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_write","type":"function","function":{"name":"write_fixture","arguments":"{}"}}]},"finish_reason":null}]}`)
			write(`{"id":"r1","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
		} else {
			write(`{"id":"r2","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"still unverified"},"finish_reason":null}]}`)
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
	registry.Register(tuiContractTool{
		name:     "write_fixture",
		metadata: tool.ToolMetadata{Category: tool.CategoryFilesystem, Impact: tool.ImpactModifying},
		execute: func(map[string]any) (*builtin.Result, error) {
			return &builtin.Result{Success: true}, os.WriteFile(trackedPath, []byte("after\n"), 0o644)
		},
	})
	store, err := storage.New(filepath.Join(t.TempDir(), "buckley.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSession("session-1"); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}
	conv := conversation.New("session-1")
	sess := &SessionState{ID: "session-1", Conversation: conv, ToolRegistry: registry}
	ctrl := &Controller{app: app, cfg: cfg, modelMgr: mgr, store: store, workDir: root, sessions: []*SessionState{sess}}

	ctrl.streamResponse(context.Background(), "make a change", sess)

	if finals := assistantFinals(conv.Messages); len(finals) != 0 {
		t.Fatalf("persisted assistant finals = %v, want none", finals)
	}
	acceptedAssistant := 0
	incompleteDraft := 0
	streamedDraft := 0
	for _, msg := range drainAllMessages(app) {
		switch v := msg.(type) {
		case AddMessageMsg:
			if v.Source == "assistant" && strings.TrimSpace(v.Content) == "still unverified" {
				acceptedAssistant++
			}
			if v.Source == "system" && strings.Contains(v.Content, "still unverified") {
				t.Fatalf("rejected draft leaked into system notice: %q", v.Content)
			}
			if v.Source == "assistant" &&
				strings.Contains(v.Content, "Preserved draft (incomplete):") &&
				strings.Contains(v.Content, "still unverified") {
				incompleteDraft++
			}
		case StreamFlush:
			if strings.Contains(v.Text, "still unverified") {
				streamedDraft++
			}
		}
	}
	if acceptedAssistant != 0 {
		t.Fatalf("rendered rejected draft as accepted assistant %d time(s)", acceptedAssistant)
	}
	if streamedDraft != 0 {
		t.Fatalf("streamed rejected draft before incomplete label %d time(s)", streamedDraft)
	}
	if incompleteDraft != 1 {
		t.Fatalf("incomplete preserved draft renders = %d, want 1", incompleteDraft)
	}
	reloaded := conversation.New("session-1")
	if err := reloaded.LoadFromStorage(store); err != nil {
		t.Fatalf("LoadFromStorage: %v", err)
	}
	var systemNotices, reloadedDrafts int
	for _, msg := range reloaded.Messages {
		text := conversation.GetContentAsString(msg.Content)
		if msg.Role == "system" && strings.Contains(text, "Incomplete result:") {
			systemNotices++
			if strings.Contains(text, "still unverified") {
				t.Fatalf("reloaded TUI system notice includes rejected draft: %+v", msg)
			}
		}
		if msg.Role == "assistant" && msg.IsTruncated && strings.Contains(text, "Preserved draft (incomplete):\nstill unverified") {
			reloadedDrafts++
		}
	}
	if systemNotices != 1 || reloadedDrafts != 1 {
		t.Fatalf("reloaded TUI incomplete persistence notices=%d drafts=%d messages=%+v", systemNotices, reloadedDrafts, reloaded.Messages)
	}
}

func TestStreamResponse_EmptyIncompleteResultPersistsNoticeAndUsage(t *testing.T) {
	root := createTUITestGitRepo(t)
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		write := func(payload string) {
			_, _ = io.WriteString(w, "data: "+payload+"\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
		write(`{"id":"r","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`)
		write(`{"id":"r","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		write(`{"id":"r","model":"gpt-4o","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
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
	conv := conversation.New("session-1")
	sess := &SessionState{ID: "session-1", Conversation: conv}
	ctrl := &Controller{app: app, cfg: cfg, modelMgr: mgr, workDir: root, sessions: []*SessionState{sess}}

	ctrl.streamResponseWithIntent(context.Background(), "make a change", sess, agentloop.MutationIntent)

	if requests != 3 {
		t.Fatalf("model requests = %d, want initial rejected final and bounded repair exhaustion", requests)
	}
	if finals := assistantFinals(conv.Messages); len(finals) != 0 {
		t.Fatalf("persisted assistant finals = %v, want none", finals)
	}
	if !conversationHasSystemContaining(conv.Messages, "Incomplete result:") ||
		!conversationHasSystemContaining(conv.Messages, "Next:") {
		t.Fatalf("conversation missing persisted incomplete notice: %+v", roleList(conv.Messages))
	}
	rawError := 0
	incompleteNotice := 0
	preservedDraft := 0
	tokenUpdates := 0
	for _, msg := range drainAllMessages(app) {
		switch v := msg.(type) {
		case AddMessageMsg:
			if v.Source == "system" && strings.Contains(v.Content, "Error: agentloop: incomplete turn") {
				rawError++
			}
			if v.Source == "system" && strings.Contains(v.Content, "Incomplete result:") && strings.Contains(v.Content, "Next:") {
				incompleteNotice++
			}
			if strings.Contains(v.Content, "Preserved draft (incomplete):") {
				preservedDraft++
			}
		case TokensMsg:
			if v.Tokens == 15 {
				tokenUpdates++
			}
		}
	}
	if rawError != 0 {
		t.Fatalf("empty incomplete rendered raw error %d time(s)", rawError)
	}
	if incompleteNotice != 1 {
		t.Fatalf("incomplete notices = %d, want 1", incompleteNotice)
	}
	if preservedDraft != 0 {
		t.Fatalf("empty incomplete rendered preserved draft block %d time(s)", preservedDraft)
	}
	if tokenUpdates != 1 {
		t.Fatalf("usage token updates with accumulated provider usage = %d, want 1", tokenUpdates)
	}
}

func TestRunToolLoop_ReadOnlyAnswerRemainsSmooth(t *testing.T) {
	server := openAIChatStreamTestServer(t, []string{"read-only answer"})
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
	conv.AddUserMessage("explain only")
	sess := &SessionState{ID: "session-1", Conversation: conv}
	ctrl := &Controller{app: app, cfg: cfg, modelMgr: mgr, workDir: t.TempDir()}

	result, err := ctrl.runToolLoop(context.Background(), sess, "gpt-4o")
	if err != nil {
		t.Fatalf("runToolLoop error: %v", err)
	}
	if result.Text != "read-only answer" || !result.Streamed {
		t.Fatalf("result = %+v, want normal streamed read-only answer", result)
	}
	if finals := assistantFinals(conv.Messages); len(finals) != 1 || finals[0] != "read-only answer" {
		t.Fatalf("assistant finals = %v, want read-only final", finals)
	}
}

func TestRunToolLoop_MalformedVerificationCallDoesNotClearCompletionDebt(t *testing.T) {
	root := createTUITestGitRepo(t)
	trackedPath := filepath.Join(root, "test.txt")
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
		switch len(bodies) {
		case 1:
			write(`{"id":"r1","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_write","type":"function","function":{"name":"write_fixture","arguments":"{}"}}]},"finish_reason":null}]}`)
			write(`{"id":"r1","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
		case 2:
			write(`{"id":"r2","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_bad_tests","type":"function","function":{"name":"run_tests","arguments":"{\""}}]},"finish_reason":null}]}`)
			write(`{"id":"r2","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
		default:
			write(`{"id":"r3","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"bad verification final"},"finish_reason":null}]}`)
			write(`{"id":"r3","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
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
	executions := 0
	registry := tool.NewEmptyRegistry()
	registry.Register(tuiContractTool{
		name:     "write_fixture",
		metadata: tool.ToolMetadata{Category: tool.CategoryFilesystem, Impact: tool.ImpactModifying},
		execute: func(map[string]any) (*builtin.Result, error) {
			return &builtin.Result{Success: true}, os.WriteFile(trackedPath, []byte("after\n"), 0o644)
		},
	})
	registry.Register(tuiContractTool{
		name:         "run_tests",
		metadata:     tool.ToolMetadata{Category: tool.CategoryTesting, Impact: tool.ImpactReadOnly},
		verification: true,
		execute: func(map[string]any) (*builtin.Result, error) {
			executions++
			return &builtin.Result{Success: true}, nil
		},
	})
	conv := conversation.New("session-1")
	conv.AddUserMessage("make a change and verify")
	sess := &SessionState{ID: "session-1", Conversation: conv, ToolRegistry: registry}
	ctrl := &Controller{app: app, cfg: cfg, modelMgr: mgr, workDir: root}

	_, err = ctrl.runToolLoop(context.Background(), sess, "gpt-4o")
	var incomplete *agentloop.IncompleteTurnError
	if !errors.As(err, &incomplete) || !strings.Contains(err.Error(), "latest verification after the final workspace change did not pass") {
		t.Fatalf("runToolLoop error = %v, want failed-verification incomplete", err)
	}
	if executions != 0 {
		t.Fatalf("malformed verification executed %d time(s), want 0", executions)
	}
	if finals := assistantFinals(conv.Messages); len(finals) != 0 {
		t.Fatalf("persisted assistant finals = %v, want none", finals)
	}
}

func TestStreamResponse_MutationIntentNoObservableChangeExhaustsThenNextTurnReadOnly(t *testing.T) {
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
		switch len(bodies) {
		case 1:
			write(`{"id":"r1","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"mutation done"},"finish_reason":null}]}`)
			write(`{"id":"r1","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		case 2:
			write(`{"id":"r2","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"still mutation done"},"finish_reason":null}]}`)
			write(`{"id":"r2","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		default:
			write(`{"id":"r3","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"read-only answer"},"finish_reason":null}]}`)
			write(`{"id":"r3","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
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
	conv := conversation.New("session-1")
	sess := &SessionState{ID: "session-1", Conversation: conv}
	ctrl := &Controller{app: app, cfg: cfg, modelMgr: mgr, workDir: t.TempDir(), sessions: []*SessionState{sess}}

	ctrl.streamResponseWithIntent(context.Background(), "make the change", sess, agentloop.MutationIntent)
	firstMessages := drainAllMessages(app)
	if finals := assistantFinals(conv.Messages); len(finals) != 0 {
		t.Fatalf("assistant finals after mutation no-op = %v, want none", finals)
	}
	if !conversationHasSystemContaining(conv.Messages, "Incomplete result: task requires observable workspace change") {
		t.Fatalf("conversation missing persisted incomplete notice: %+v", roleList(conv.Messages))
	}
	for _, msg := range firstMessages {
		if flush, ok := msg.(StreamFlush); ok && strings.Contains(flush.Text, "mutation done") {
			t.Fatalf("mutation no-op draft streamed before acceptance: %+v", flush)
		}
	}

	ctrl.streamResponse(context.Background(), "explain only", sess)
	secondMessages := drainAllMessages(app)
	finals := assistantFinals(conv.Messages)
	if len(finals) != 1 || finals[0] != "read-only answer" {
		t.Fatalf("assistant finals after read-only turn = %v, want read-only answer", finals)
	}
	readOnlyFlushes := 0
	for _, msg := range secondMessages {
		if flush, ok := msg.(StreamFlush); ok && strings.Contains(flush.Text, "read-only answer") {
			readOnlyFlushes++
		}
	}
	if readOnlyFlushes != 1 {
		t.Fatalf("read-only answer stream flushes = %d, want 1", readOnlyFlushes)
	}
}

func TestStreamResponse_MutationIntentPassingVerifierWithoutEditKeepsDraftBuffered(t *testing.T) {
	root := createTUITestGitRepo(t)
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
		switch len(bodies) {
		case 1:
			write(`{"id":"r1","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_tests","type":"function","function":{"name":"run_tests","arguments":"{}"}}]},"finish_reason":null}]}`)
			write(`{"id":"r1","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
		case 2:
			write(`{"id":"r2","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"tests passed so mutation is done"},"finish_reason":null}]}`)
			write(`{"id":"r2","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		default:
			write(`{"id":"r3","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"still done after repair"},"finish_reason":null}]}`)
			write(`{"id":"r3","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
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
	registry.Register(tuiContractTool{
		name:         "run_tests",
		metadata:     tool.ToolMetadata{Category: tool.CategoryTesting, Impact: tool.ImpactReadOnly},
		verification: true,
	})
	conv := conversation.New("session-1")
	sess := &SessionState{ID: "session-1", Conversation: conv, ToolRegistry: registry}
	ctrl := &Controller{app: app, cfg: cfg, modelMgr: mgr, workDir: root, sessions: []*SessionState{sess}}

	ctrl.streamResponseWithIntent(context.Background(), "make a change and verify", sess, agentloop.MutationIntent)

	if len(bodies) != 3 {
		t.Fatalf("model requests = %d, want verifier, rejected final, exhausted repair final", len(bodies))
	}
	if !strings.Contains(bodies[2], "requires an observable workspace change") {
		t.Fatalf("repair request missing observable-change instruction: %s", bodies[2])
	}
	if finals := assistantFinals(conv.Messages); len(finals) != 0 {
		t.Fatalf("persisted assistant finals = %v, want none", finals)
	}
	if !conversationHasSystemContaining(conv.Messages, "Incomplete result: task requires observable workspace change") {
		t.Fatalf("conversation missing persisted incomplete notice: %+v", roleList(conv.Messages))
	}
	leakedDraft := 0
	incompleteDraft := 0
	for _, msg := range drainAllMessages(app) {
		switch v := msg.(type) {
		case AddMessageMsg:
			if v.Source == "assistant" && !strings.Contains(v.Content, "Preserved draft (incomplete):") &&
				(strings.Contains(v.Content, "tests passed so mutation is done") || strings.Contains(v.Content, "still done after repair")) {
				leakedDraft++
			}
			if v.Source == "system" && (strings.Contains(v.Content, "tests passed so mutation is done") || strings.Contains(v.Content, "still done after repair")) {
				t.Fatalf("rejected draft leaked into system notice: %q", v.Content)
			}
			if v.Source == "assistant" && strings.Contains(v.Content, "Preserved draft (incomplete):") && strings.Contains(v.Content, "still done after repair") {
				incompleteDraft++
			}
		case StreamFlush:
			if strings.Contains(v.Text, "tests passed so mutation is done") || strings.Contains(v.Text, "still done after repair") {
				leakedDraft++
			}
		case ReplaceLastMessageMsg:
			if strings.Contains(v.Content, "tests passed so mutation is done") || strings.Contains(v.Content, "still done after repair") {
				leakedDraft++
			}
		}
	}
	if leakedDraft != 0 {
		t.Fatalf("no-op mutation draft appeared as accepted transcript content %d time(s)", leakedDraft)
	}
	if incompleteDraft != 1 {
		t.Fatalf("incomplete preserved draft renders = %d, want 1", incompleteDraft)
	}
}

func TestSubmitPrompt_QueuedTaskIntentsRemainIndependent(t *testing.T) {
	app, err := NewWidgetApp(WidgetAppConfig{Backend: sim.New(80, 24)})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}
	_, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sess := &SessionState{
		ID:           "session-1",
		Conversation: conversation.New("session-1"),
		Streaming:    true,
		Cancel:       cancel,
	}
	ctrl := &Controller{app: app, sessions: []*SessionState{sess}}

	ctrl.submitPromptWithIntent("make a change", false, agentloop.MutationIntent)
	ctrl.submitPromptWithIntent("explain after", false, agentloop.ReadOnlyIntent)

	if len(sess.MessageQueue) != 2 {
		t.Fatalf("queue length = %d, want 2", len(sess.MessageQueue))
	}
	if sess.MessageQueue[0].TaskIntent != agentloop.MutationIntent {
		t.Fatalf("first queued intent = %q, want mutation", sess.MessageQueue[0].TaskIntent)
	}
	if sess.MessageQueue[1].TaskIntent != agentloop.ReadOnlyIntent {
		t.Fatalf("second queued intent = %q, want read_only", sess.MessageQueue[1].TaskIntent)
	}
}

func TestTaskCommand_RejectsInvalidTaskIntentBeforeExecution(t *testing.T) {
	app, err := NewWidgetApp(WidgetAppConfig{Backend: sim.New(80, 24)})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}
	sess := &SessionState{ID: "session-1", Conversation: conversation.New("session-1")}
	ctrl := &Controller{app: app, sessions: []*SessionState{sess}}

	ctrl.handleCommand("/task surprise hello")
	if len(sess.Conversation.Messages) != 0 || len(sess.MessageQueue) != 0 || sess.Streaming {
		t.Fatalf("invalid intent was not rejected before execution: messages=%d queue=%d streaming=%v",
			len(sess.Conversation.Messages), len(sess.MessageQueue), sess.Streaming)
	}
	found := false
	for _, msg := range drainAllMessages(app) {
		if added, ok := msg.(AddMessageMsg); ok && added.Source == "system" && strings.Contains(added.Content, "task intent must be") {
			found = true
		}
	}
	if !found {
		t.Fatal("invalid /task intent did not render a system error")
	}

	ctrl.handleCommand("/task mutation")
	if len(sess.Conversation.Messages) != 0 || len(sess.MessageQueue) != 0 || sess.Streaming {
		t.Fatalf("missing request was not rejected before execution: messages=%d queue=%d streaming=%v",
			len(sess.Conversation.Messages), len(sess.MessageQueue), sess.Streaming)
	}
}

func TestTaskCommand_QueuesWithParsedIntent(t *testing.T) {
	app, err := NewWidgetApp(WidgetAppConfig{Backend: sim.New(80, 24)})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}
	_, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sess := &SessionState{
		ID:           "session-1",
		Conversation: conversation.New("session-1"),
		Streaming:    true,
		Cancel:       cancel,
	}
	ctrl := &Controller{app: app, sessions: []*SessionState{sess}}

	ctrl.handleCommand("/task read_only explain the result")

	if len(sess.MessageQueue) != 1 {
		t.Fatalf("queue length = %d, want 1", len(sess.MessageQueue))
	}
	if sess.MessageQueue[0].Content != "explain the result" {
		t.Fatalf("queued content = %q", sess.MessageQueue[0].Content)
	}
	if sess.MessageQueue[0].TaskIntent != agentloop.ReadOnlyIntent {
		t.Fatalf("queued task intent = %q, want read_only", sess.MessageQueue[0].TaskIntent)
	}
}

func TestTaskCommand_PreservesRawRequestAfterIntentDelimiter(t *testing.T) {
	app, err := NewWidgetApp(WidgetAppConfig{Backend: sim.New(80, 24)})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}
	_, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sess := &SessionState{
		ID:           "session-1",
		Conversation: conversation.New("session-1"),
		Streaming:    true,
		Cancel:       cancel,
	}
	ctrl := &Controller{app: app, sessions: []*SessionState{sess}}

	request := "    ```go\n\tfmt.Println(\"hi\")\n    ```  "
	ctrl.handleCommand("/TASK\tmutation\n" + request)

	if len(sess.MessageQueue) != 1 {
		t.Fatalf("queue length = %d, want 1", len(sess.MessageQueue))
	}
	if sess.MessageQueue[0].Content != request {
		t.Fatalf("queued content = %q, want exact raw request %q", sess.MessageQueue[0].Content, request)
	}
	if sess.MessageQueue[0].TaskIntent != agentloop.MutationIntent {
		t.Fatalf("queued task intent = %q, want mutation", sess.MessageQueue[0].TaskIntent)
	}
}

func TestTaskCommand_AcceptsNewlineAndUnicodeWhitespaceSeparators(t *testing.T) {
	app, err := NewWidgetApp(WidgetAppConfig{Backend: sim.New(80, 24)})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}
	_, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sess := &SessionState{
		ID:           "session-1",
		Conversation: conversation.New("session-1"),
		Streaming:    true,
		Cancel:       cancel,
	}
	ctrl := &Controller{app: app, sessions: []*SessionState{sess}}

	ctrl.handleCommand("/task\nread_only\u2003explain\tthis\n")

	if len(sess.MessageQueue) != 1 {
		t.Fatalf("queue length = %d, want 1", len(sess.MessageQueue))
	}
	if sess.MessageQueue[0].Content != "explain\tthis\n" {
		t.Fatalf("queued content = %q", sess.MessageQueue[0].Content)
	}
	if sess.MessageQueue[0].TaskIntent != agentloop.ReadOnlyIntent {
		t.Fatalf("queued task intent = %q, want read_only", sess.MessageQueue[0].TaskIntent)
	}
}

type tuiContractTool struct {
	name         string
	metadata     tool.ToolMetadata
	verification bool
	execute      func(map[string]any) (*builtin.Result, error)
}

func conversationHasSystemContaining(messages []conversation.Message, text string) bool {
	for _, msg := range messages {
		if msg.Role == "system" && strings.Contains(conversation.GetContentAsString(msg.Content), text) {
			return true
		}
	}
	return false
}

func (t tuiContractTool) Name() string { return t.name }

func (t tuiContractTool) Description() string { return "contract test tool" }

func (t tuiContractTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{Type: "object"}
}

func (t tuiContractTool) Metadata() tool.ToolMetadata { return t.metadata }

func (t tuiContractTool) TrustedVerification() bool { return t.verification }

func (t tuiContractTool) Execute(params map[string]any) (*builtin.Result, error) {
	if t.execute != nil {
		return t.execute(params)
	}
	return &builtin.Result{Success: true}, nil
}

func createTUITestGitRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runTUITestGit(t, root, "init")
	runTUITestGit(t, root, "config", "user.email", "test@example.com")
	runTUITestGit(t, root, "config", "user.name", "Test User")
	path := filepath.Join(root, "test.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	runTUITestGit(t, root, "add", "test.txt")
	runTUITestGit(t, root, "commit", "-m", "initial")
	return root
}

func runTUITestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func assistantFinals(messages []conversation.Message) []string {
	var finals []string
	for _, msg := range messages {
		if msg.Role == "assistant" && len(msg.ToolCalls) == 0 && !msg.IsTruncated {
			if text := conversation.GetContentAsString(msg.Content); strings.TrimSpace(text) != "" {
				finals = append(finals, text)
			}
		}
	}
	return finals
}
