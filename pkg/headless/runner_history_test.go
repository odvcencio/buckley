package headless

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

type headlessStateTool struct {
	name     string
	metadata tool.ToolMetadata
	execute  func() (*builtin.Result, error)
}

func (t headlessStateTool) Name() string        { return t.name }
func (t headlessStateTool) Description() string { return t.name }
func (t headlessStateTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{Type: "object"}
}
func (t headlessStateTool) Execute(map[string]any) (*builtin.Result, error) {
	if t.execute != nil {
		return t.execute()
	}
	return &builtin.Result{Success: true}, nil
}
func (t headlessStateTool) Metadata() tool.ToolMetadata { return t.metadata }
func (t headlessStateTool) TrustedVerification() bool   { return t.metadata.Verification }

// TestRunner_SecondRequestCarriesToolCallAndResult is the cross-round
// transcript invariant: after round one dispatches a tool, round two's
// request must contain the assistant tool-call message and its tool
// result, or the model loops on stale history.
func TestRunner_SecondRequestCarriesToolCallAndResult(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) == 1 {
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-1","model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_hist_1","type":"function","function":{"name":"echo_tool","arguments":"{\"text\":\"hi\"}"}}]},"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
			}`)
			return
		}
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-2","model":"gpt-4o",
			"choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`)
	}))
	defer server.Close()

	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai"

	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	store, err := storage.New(t.TempDir() + "/history.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSession("session-h"); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}

	registry := tool.NewEmptyRegistry()
	registry.Register(fakeEchoTool{})

	runner := &Runner{
		sessionID:     "session-h",
		session:       &storage.Session{ID: "session-h"},
		conv:          conversation.New("session-h"),
		store:         store,
		config:        cfg,
		modelManager:  mgr,
		tools:         registry,
		modelOverride: "gpt-4o",
		approvalChan:  make(chan ApprovalResponse, 1),
	}

	if err := runner.processUserInput("please echo hi"); err != nil {
		t.Fatalf("processUserInput: %v", err)
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

	// Persistence: a fresh conversation loaded from storage must contain
	// exactly one assistant tool-call message, its tool result, and one
	// final assistant message (no duplicates).
	reloaded := conversation.New("session-h")
	if err := reloaded.LoadFromStorage(store); err != nil {
		t.Fatalf("reload conversation: %v", err)
	}
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

func TestRunner_ToolOfferPolicyRejectsSurpriseCallsForRouteSelectedKnownToollessModel(t *testing.T) {
	executions := 0
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "application/json")
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
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-1","model":"o1-mini",
				"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_surprise","type":"function","function":{"name":"echo_tool","arguments":"{\"text\":\"should not run\"}"}}]},"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
			}`)
			return
		}
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-2","model":"o1-mini",
			"choices":[{"index":0,"message":{"role":"assistant","content":"I could not run that tool."},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`)
	}))
	defer server.Close()

	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai"
	cfg.Models.Execution = "alias-o1-mini"
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
	store := newTestStore(t)
	if err := store.EnsureSession("toolless-surprise"); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}
	registry := tool.NewEmptyRegistry()
	registry.Register(headlessStateTool{
		name: "echo_tool",
		execute: func() (*builtin.Result, error) {
			executions++
			return &builtin.Result{Success: true}, nil
		},
	})
	runner := &Runner{
		sessionID:     "toolless-surprise",
		session:       &storage.Session{ID: "toolless-surprise"},
		conv:          conversation.New("toolless-surprise"),
		store:         store,
		config:        cfg,
		modelManager:  mgr,
		tools:         registry,
		modelOverride: "alias-o1-mini",
		approvalChan:  make(chan ApprovalResponse, 1),
	}

	err = runner.processUserInput("echo this")
	var incomplete *agentloop.IncompleteTurnError
	if !errors.As(err, &incomplete) {
		t.Fatalf("processUserInput error = %T %v, want *agentloop.IncompleteTurnError", err, err)
	}
	if incomplete.FinishReason != agentloop.FinishReasonInvalidCompletion || incomplete.Code != "unoffered_tool_call" {
		t.Fatalf("incomplete result = %+v, want invalid unoffered-tool-call completion", incomplete)
	}
	if executions != 0 {
		t.Fatalf("tool executions = %d, want 0", executions)
	}
	if len(bodies) != 1 {
		t.Fatalf("model requests = %d, want only the unsafe response-producing request", len(bodies))
	}
	var toolHistory, toolResults, preservedDrafts int
	for _, msg := range runner.conv.Messages {
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			toolHistory++
		}
		if msg.Role == "tool" {
			toolResults++
		}
		if msg.Role == "assistant" && msg.IsTruncated && strings.Contains(conversation.GetContentAsString(msg.Content), "unexecuted tool call") {
			preservedDrafts++
		}
	}
	if toolHistory != 0 || toolResults != 0 {
		t.Fatalf("tool history=%d tool results=%d, want no ordinary history mutation after unoffered call", toolHistory, toolResults)
	}
	if preservedDrafts != 1 {
		t.Fatalf("preserved incomplete drafts = %d, want one public unexecuted-call draft", preservedDrafts)
	}
}

func TestRunner_ToolOfferPolicyKeepsUnknownModelEligible(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Models.DefaultProvider = "openai"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	registry := tool.NewEmptyRegistry()
	registry.Register(fakeEchoTool{})
	runner := &Runner{
		sessionID:    "unknown-tools",
		conv:         conversation.New("unknown-tools"),
		config:       cfg,
		modelManager: mgr,
		tools:        registry,
	}
	runner.conv.AddUserMessage("hello")

	req := runner.buildRawChatRequest("future-openai-tool-model")
	if len(req.Tools) != 1 || req.ToolChoice != "auto" {
		t.Fatalf("unknown model request tools=%d tool_choice=%q, want one schema with auto", len(req.Tools), req.ToolChoice)
	}
}

func TestRunner_CompletionContractRepairsPrematureFinalAfterMutation(t *testing.T) {
	root := createTestGitRepo(t, t.TempDir())
	trackedPath := filepath.Join(root, "test.txt")
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "application/json")
		switch len(bodies) {
		case 1:
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-1","model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_write","type":"function","function":{"name":"write_fixture","arguments":"{}"}}]},"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
			}`)
		case 2:
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-2","model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","content":"premature final"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
			}`)
		case 3:
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-3","model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_tests","type":"function","function":{"name":"run_tests","arguments":"{}"}}]},"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
			}`)
		default:
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-4","model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","content":"verified final"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
			}`)
		}
	}))
	defer server.Close()

	runner := newHeadlessContractTestRunner(t, server.URL, root, tool.NewEmptyRegistry())
	runner.tools.Register(headlessStateTool{
		name:     "write_fixture",
		metadata: tool.ToolMetadata{Category: tool.CategoryFilesystem, Impact: tool.ImpactModifying},
		execute: func() (*builtin.Result, error) {
			return &builtin.Result{Success: true}, os.WriteFile(trackedPath, []byte("after\n"), 0o644)
		},
	})
	runner.tools.Register(headlessStateTool{
		name:     "run_tests",
		metadata: tool.ToolMetadata{Category: tool.CategoryTesting, Impact: tool.ImpactReadOnly, Verification: true},
	})

	if err := runner.processUserInput("make a change"); err != nil {
		t.Fatalf("processUserInput: %v", err)
	}
	if len(bodies) != 4 {
		t.Fatalf("model requests = %d, want edit, rejected final, verify repair, accepted final", len(bodies))
	}
	if !strings.Contains(bodies[2], "workspace changed after the last successful verification") {
		t.Fatalf("third request missing completion repair instruction: %s", bodies[2])
	}
	var finals []string
	for _, msg := range runner.conv.ToModelMessages() {
		if msg.Role == "assistant" && len(msg.ToolCalls) == 0 {
			if text := model.ExtractTextContentOrEmpty(msg.Content); text != "" {
				finals = append(finals, text)
			}
		}
	}
	if len(finals) != 1 || finals[0] != "verified final" {
		t.Fatalf("persisted assistant finals = %v, want accepted final only", finals)
	}
}

func TestRunner_CompletionContractExhaustionReturnsIncompleteWithoutPersistingRejectedFinal(t *testing.T) {
	root := createTestGitRepo(t, t.TempDir())
	trackedPath := filepath.Join(root, "test.txt")
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "application/json")
		switch len(bodies) {
		case 1:
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-1","model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_write","type":"function","function":{"name":"write_fixture","arguments":"{}"}}]},"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
			}`)
		default:
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-final","model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","content":"still unverified"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
			}`)
		}
	}))
	defer server.Close()

	runner := newHeadlessContractTestRunner(t, server.URL, root, tool.NewEmptyRegistry())
	runner.tools.Register(headlessStateTool{
		name:     "write_fixture",
		metadata: tool.ToolMetadata{Category: tool.CategoryFilesystem, Impact: tool.ImpactModifying},
		execute: func() (*builtin.Result, error) {
			return &builtin.Result{Success: true}, os.WriteFile(trackedPath, []byte("after\n"), 0o644)
		},
	})

	err := runner.processUserInput("make a change")
	var incomplete *agentloop.IncompleteTurnError
	if !errors.As(err, &incomplete) || !strings.Contains(err.Error(), "missing successful verification after the latest workspace change") {
		t.Fatalf("processUserInput error = %v, want completion-contract incomplete", err)
	}
	if !strings.Contains(bodies[2], "workspace changed after the last successful verification") {
		t.Fatalf("third request missing completion repair instruction: %s", bodies[2])
	}
	for _, msg := range runner.conv.ToModelMessages() {
		if msg.Role == "assistant" && len(msg.ToolCalls) == 0 &&
			model.ExtractTextContentOrEmpty(msg.Content) == "still unverified" {
			t.Fatalf("rejected final was persisted in conversation: %+v", runner.conv.ToModelMessages())
		}
		if msg.Role == "system" && strings.Contains(model.ExtractTextContentOrEmpty(msg.Content), "still unverified") {
			t.Fatalf("rejected draft leaked into system model history: %+v", msg)
		}
	}
	reloaded := conversation.New(runner.sessionID)
	if err := reloaded.LoadFromStorage(runner.store); err != nil {
		t.Fatalf("LoadFromStorage: %v", err)
	}
	var systemNotices, incompleteDrafts int
	for _, msg := range reloaded.Messages {
		text := conversation.GetContentAsString(msg.Content)
		if msg.Role == "system" && strings.Contains(text, "Incomplete result:") {
			systemNotices++
			if strings.Contains(text, "still unverified") {
				t.Fatalf("reloaded system notice includes rejected draft: %+v", msg)
			}
		}
		if msg.Role == "assistant" && msg.IsTruncated && strings.Contains(text, "Preserved draft (incomplete):\nstill unverified") {
			incompleteDrafts++
		}
	}
	if systemNotices != 1 || incompleteDrafts != 1 {
		t.Fatalf("reloaded incomplete persistence notices=%d drafts=%d messages=%+v", systemNotices, incompleteDrafts, reloaded.Messages)
	}
}

func TestRunner_IncompleteTurnWithoutDraftPersistsNoticeOnly(t *testing.T) {
	store := newTestStore(t)
	sessionID := "headless-empty-incomplete"
	if err := store.EnsureSession(sessionID); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}
	runner := &Runner{
		sessionID: sessionID,
		conv:      conversation.New(sessionID),
		store:     store,
	}

	notice, ok := runner.persistIncompleteTurnNotice(&agentloop.IncompleteTurnError{
		Code:   string(agentloop.CompletionMissingObservableChange),
		Reason: "task requires observable workspace change",
	}, "")
	if !ok || !strings.Contains(notice.Message, "Incomplete result:") {
		t.Fatalf("persistIncompleteTurnNotice = %+v, %v", notice, ok)
	}
	reloaded := conversation.New(sessionID)
	if err := reloaded.LoadFromStorage(store); err != nil {
		t.Fatalf("LoadFromStorage: %v", err)
	}
	var systemNotices, incompleteDrafts int
	for _, msg := range reloaded.Messages {
		text := conversation.GetContentAsString(msg.Content)
		if msg.Role == "system" && strings.Contains(text, "Incomplete result:") {
			systemNotices++
		}
		if msg.Role == "assistant" && msg.IsTruncated && strings.Contains(text, "Preserved draft (incomplete):") {
			incompleteDrafts++
		}
	}
	if systemNotices != 1 || incompleteDrafts != 0 {
		t.Fatalf("reloaded empty incomplete notices=%d drafts=%d messages=%+v", systemNotices, incompleteDrafts, reloaded.Messages)
	}
}

func TestRunner_CompletionContractUnobservableMutationFailsClosed(t *testing.T) {
	root := t.TempDir()
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "application/json")
		switch len(bodies) {
		case 1:
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-1","model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_write","type":"function","function":{"name":"write_fixture","arguments":"{}"}}]},"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
			}`)
		default:
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-final","model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","content":"unobservable final"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
			}`)
		}
	}))
	defer server.Close()

	runner := newHeadlessContractTestRunner(t, server.URL, root, tool.NewEmptyRegistry())
	runner.tools.Register(headlessStateTool{
		name:     "write_fixture",
		metadata: tool.ToolMetadata{Category: tool.CategoryFilesystem, Impact: tool.ImpactModifying},
		execute: func() (*builtin.Result, error) {
			return &builtin.Result{Success: true}, os.WriteFile(filepath.Join(root, "test.txt"), []byte("after\n"), 0o644)
		},
	})

	err := runner.processUserInput("make a change")
	var incomplete *agentloop.IncompleteTurnError
	if !errors.As(err, &incomplete) || !strings.Contains(err.Error(), "workspace state could not be observed") {
		t.Fatalf("processUserInput error = %v, want unobservable workspace incomplete", err)
	}
	if len(bodies) != 3 {
		t.Fatalf("model requests = %d, want edit, rejected final, exhausted final", len(bodies))
	}
	if !strings.Contains(bodies[2], "Buckley could not observe workspace state before and after a tool that may affect completion evidence") {
		t.Fatalf("third request missing state-observation repair instruction: %s", bodies[2])
	}
	for _, msg := range runner.conv.ToModelMessages() {
		if msg.Role == "assistant" && len(msg.ToolCalls) == 0 &&
			model.ExtractTextContentOrEmpty(msg.Content) == "unobservable final" {
			t.Fatalf("unobservable rejected final was persisted: %+v", runner.conv.ToModelMessages())
		}
	}
}

func TestRunner_CompletionContractFailedVerificationExhaustsIncomplete(t *testing.T) {
	root := createTestGitRepo(t, t.TempDir())
	trackedPath := filepath.Join(root, "test.txt")
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "application/json")
		switch len(bodies) {
		case 1:
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-1","model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_write","type":"function","function":{"name":"write_fixture","arguments":"{}"}}]},"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
			}`)
		case 2:
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-2","model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_tests","type":"function","function":{"name":"run_tests","arguments":"{}"}}]},"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
			}`)
		default:
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-final","model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","content":"failed verification final"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
			}`)
		}
	}))
	defer server.Close()

	runner := newHeadlessContractTestRunner(t, server.URL, root, tool.NewEmptyRegistry())
	runner.tools.Register(headlessStateTool{
		name:     "write_fixture",
		metadata: tool.ToolMetadata{Category: tool.CategoryFilesystem, Impact: tool.ImpactModifying},
		execute: func() (*builtin.Result, error) {
			return &builtin.Result{Success: true}, os.WriteFile(trackedPath, []byte("after\n"), 0o644)
		},
	})
	runner.tools.Register(headlessStateTool{
		name:     "run_tests",
		metadata: tool.ToolMetadata{Category: tool.CategoryTesting, Impact: tool.ImpactReadOnly, Verification: true},
		execute: func() (*builtin.Result, error) {
			return &builtin.Result{Success: false, Error: "tests failed"}, nil
		},
	})

	err := runner.processUserInput("make a change and test")
	var incomplete *agentloop.IncompleteTurnError
	if !errors.As(err, &incomplete) || !strings.Contains(err.Error(), "latest verification after the final workspace change did not pass") {
		t.Fatalf("processUserInput error = %v, want failed-verification incomplete", err)
	}
	if len(bodies) != 4 {
		t.Fatalf("model requests = %d, want edit, failed verification, repair final, exhausted final", len(bodies))
	}
	if !strings.Contains(bodies[3], "latest verification after the final workspace change failed") {
		t.Fatalf("fourth request missing failed-verification repair instruction: %s", bodies[3])
	}
	for _, msg := range runner.conv.ToModelMessages() {
		if msg.Role == "assistant" && len(msg.ToolCalls) == 0 &&
			model.ExtractTextContentOrEmpty(msg.Content) == "failed verification final" {
			t.Fatalf("rejected failed-verification final was persisted: %+v", runner.conv.ToModelMessages())
		}
	}
}

func TestRunner_DispatchMalformedRunTestsDoesNotExecuteOrVerify(t *testing.T) {
	executions := 0
	registry := tool.NewEmptyRegistry()
	registry.Register(headlessStateTool{
		name:     "run_tests",
		metadata: tool.ToolMetadata{Category: tool.CategoryTesting, Impact: tool.ImpactReadOnly, Verification: true},
		execute: func() (*builtin.Result, error) {
			executions++
			return &builtin.Result{Success: true}, nil
		},
	})
	runner := &Runner{sessionID: "malformed-run-tests", tools: registry}
	outcomes, err := runner.dispatchToolCalls(context.Background(), []model.ToolCall{{
		ID: "call-bad", Function: model.FunctionCall{Name: "run_tests", Arguments: `{"`},
	}})
	if err != nil {
		t.Fatalf("dispatchToolCalls: %v", err)
	}
	if executions != 0 {
		t.Fatalf("executions = %d, want 0", executions)
	}
	if len(outcomes) != 1 || outcomes[0].Success || outcomes[0].VerificationObserved || outcomes[0].StateObserved || outcomes[0].StateChanged {
		t.Fatalf("outcomes = %+v, want failed control outcome without facts", outcomes)
	}
}

func TestRunner_DispatchMalformedMutatorDoesNotExecute(t *testing.T) {
	executions := 0
	registry := tool.NewEmptyRegistry()
	registry.Register(headlessStateTool{
		name:     "write_fixture",
		metadata: tool.ToolMetadata{Category: tool.CategoryFilesystem, Impact: tool.ImpactModifying},
		execute: func() (*builtin.Result, error) {
			executions++
			return &builtin.Result{Success: true}, nil
		},
	})
	runner := &Runner{sessionID: "malformed-mutator", tools: registry}
	outcomes, err := runner.dispatchToolCalls(context.Background(), []model.ToolCall{{
		ID: "call-bad", Function: model.FunctionCall{Name: "write_fixture", Arguments: `{oops`},
	}})
	if err != nil {
		t.Fatalf("dispatchToolCalls: %v", err)
	}
	if executions != 0 {
		t.Fatalf("executions = %d, want 0", executions)
	}
	if len(outcomes) != 1 || outcomes[0].Success || outcomes[0].StateObserved || outcomes[0].StateChanged {
		t.Fatalf("outcomes = %+v, want failed control outcome without state facts", outcomes)
	}
}

func newHeadlessContractTestRunner(t *testing.T, baseURL, root string, registry *tool.Registry) *Runner {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = baseURL
	cfg.Models.DefaultProvider = "openai"
	cfg.Models.Execution = "gpt-4o"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	store := newTestStore(t)
	sessionID := "headless-contract-" + filepath.Base(root)
	if err := store.EnsureSession(sessionID); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}
	if registry == nil {
		registry = tool.NewEmptyRegistry()
	}
	return &Runner{
		sessionID:     sessionID,
		session:       &storage.Session{ID: sessionID, ProjectPath: root},
		conv:          conversation.New(sessionID),
		store:         store,
		config:        cfg,
		modelManager:  mgr,
		tools:         registry,
		modelOverride: "gpt-4o",
		approvalChan:  make(chan ApprovalResponse, 1),
	}
}
