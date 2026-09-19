package headless

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestRunner_TypedCanceledInterruptionPersistsIncompleteNotice(t *testing.T) {
	root := createTestGitRepo(t, t.TempDir())
	trackedPath := filepath.Join(root, "test.txt")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-1","model":"gpt-4o",
			"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[
				{"id":"call_write_prefix","type":"function","function":{"name":"prefix_mutation","arguments":"{}"}},
				{"id":"call_blocked_suffix","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"later.txt\",\"content\":\"later\"}"}}
			]},"finish_reason":"tool_calls"}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`)
	}))
	defer server.Close()

	registry := tool.NewEmptyRegistry()
	var cancel context.CancelFunc
	registry.Register(headlessStateTool{
		name:     "prefix_mutation",
		metadata: tool.ToolMetadata{Category: tool.CategoryFilesystem, Impact: tool.ImpactModifying},
		execute: func() (*builtin.Result, error) {
			if err := os.WriteFile(trackedPath, []byte("after\n"), 0o644); err != nil {
				return nil, err
			}
			cancel()
			return &builtin.Result{Success: true, Data: map[string]any{"result": "prefix changed"}}, nil
		},
	})
	emitter := &mockEmitter{}
	runner := newHeadlessContractTestRunner(t, server.URL, root, registry)
	runner.emitter = emitter

	ctx, cancelFunc := context.WithCancel(context.Background())
	cancel = cancelFunc
	defer cancelFunc()
	err := runner.runConversationLoopForCommand(ctx, nil, "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runConversationLoopForCommand error = %v, want context canceled", err)
	}
	var incomplete *agentloop.IncompleteTurnError
	if !errors.As(err, &incomplete) || incomplete.Code != agentloop.IncompleteToolRoundInterrupted {
		t.Fatalf("runConversationLoopForCommand error = %v, want typed tool-round interruption", err)
	}

	var incompleteWarnings, modelFailures int
	for _, event := range emitter.events {
		if event.Type == EventWarning && event.Data["code"] == agentloop.IncompleteToolRoundInterrupted {
			incompleteWarnings++
			if got, _ := event.Data["message"].(string); strings.Contains(got, "context canceled") {
				t.Fatalf("warning leaked raw cancellation: %q", got)
			}
			if got, _ := event.Data["nextAction"].(string); !strings.Contains(got, "Inspect and reconcile") {
				t.Fatalf("nextAction = %q, want inspect/reconcile guidance", got)
			}
		}
		if event.Type == EventError {
			if msg, _ := event.Data["message"].(string); strings.Contains(msg, "model call failed") {
				modelFailures++
			}
		}
	}
	if incompleteWarnings != 1 || modelFailures != 0 {
		t.Fatalf("events warnings=%d modelFailures=%d all=%+v", incompleteWarnings, modelFailures, emitter.events)
	}

	reloaded := conversation.New(runner.sessionID)
	if err := reloaded.LoadFromStorage(runner.store); err != nil {
		t.Fatalf("LoadFromStorage: %v", err)
	}
	var persistedNotices int
	for _, msg := range reloaded.Messages {
		text := conversation.GetContentAsString(msg.Content)
		if msg.Role == "system" && strings.Contains(text, "Incomplete result:") {
			persistedNotices++
			if strings.Contains(text, "context canceled") {
				t.Fatalf("persisted notice leaked raw cancellation: %+v", msg)
			}
			if !strings.Contains(text, "Inspect and reconcile") {
				t.Fatalf("persisted notice missing next action: %+v", msg)
			}
		}
	}
	if persistedNotices != 1 {
		t.Fatalf("persisted notices = %d, want 1; messages=%+v", persistedNotices, reloaded.Messages)
	}
}

func TestRunner_RawCancellationDoesNotEmitIncompleteNotice(t *testing.T) {
	root := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("raw pre-run cancellation should not call the model")
	}))
	defer server.Close()
	emitter := &mockEmitter{}
	runner := newHeadlessContractTestRunner(t, server.URL, root, tool.NewEmptyRegistry())
	runner.emitter = emitter

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := runner.runConversationLoopForCommand(ctx, nil, "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runConversationLoopForCommand error = %v, want context canceled", err)
	}
	for _, event := range emitter.events {
		if event.Type == EventWarning && event.Data["code"] == agentloop.IncompleteToolRoundInterrupted {
			t.Fatalf("raw cancellation emitted incomplete notice: %+v", emitter.events)
		}
		if event.Type == EventError {
			t.Fatalf("raw cancellation emitted error event: %+v", emitter.events)
		}
	}
}
