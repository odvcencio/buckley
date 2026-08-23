package headless

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/ipc/command"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/sessionexec"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

type failOnceCompletionJournal struct {
	sessionexec.Journal
	failed atomic.Bool
}

type staleHeartbeatJournal struct {
	sessionexec.Journal
	called chan<- struct{}
}

func (j staleHeartbeatJournal) Heartbeat(_ context.Context, lease sessionexec.LeaseRef, _ time.Duration) (sessionexec.LeaseRef, error) {
	select {
	case j.called <- struct{}{}:
	default:
	}
	return lease, sessionexec.ErrLeaseStale
}

type recoveryRequiredStepJournal struct {
	agentloop.DurableStepJournal
}

func (j recoveryRequiredStepJournal) BeginStep(_ context.Context, step runledger.ExecutionStep) (runledger.ExecutionStep, bool, error) {
	step.Status = runledger.StepStarted
	step.Attempt = 1
	step.ClaimGeneration = 1
	step.DispatchState = runledger.StepDispatchDispatched
	return step, false, runledger.RecoveryErrorForStep(step)
}

func (j *failOnceCompletionJournal) Complete(ctx context.Context, lease sessionexec.LeaseRef, completion sessionexec.Completion, entries []sessionexec.TranscriptEntry) (sessionexec.Receipt, error) {
	if j.failed.CompareAndSwap(false, true) {
		return sessionexec.Receipt{}, fmt.Errorf("injected completion outage")
	}
	return j.Journal.Complete(ctx, lease, completion, entries)
}

type countingEchoTool struct {
	calls *atomic.Int32
}

type blockingContextTool struct {
	entered chan<- struct{}
}

func (blockingContextTool) Name() string { return "blocking_tool" }

func (blockingContextTool) Description() string { return "blocks until cancelled" }

func (blockingContextTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{Type: "object"}
}

func (blockingContextTool) Execute(map[string]any) (*builtin.Result, error) {
	return nil, fmt.Errorf("blocking tool requires context")
}

func (t blockingContextTool) ExecuteWithContext(ctx context.Context, _ map[string]any) (*builtin.Result, error) {
	select {
	case t.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (t countingEchoTool) Name() string { return "echo_tool" }

func (t countingEchoTool) Description() string { return "returns provided text" }

func (t countingEchoTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{
		Type: "object",
		Properties: map[string]builtin.PropertySchema{
			"text": {Type: "string", Description: "text to echo"},
		},
		Required: []string{"text"},
	}
}

func (t countingEchoTool) Execute(params map[string]any) (*builtin.Result, error) {
	t.calls.Add(1)
	text, _ := params["text"].(string)
	return &builtin.Result{Success: true, DisplayData: map[string]any{"message": text}}, nil
}

func TestDurableRunner_ReplaysCompletedStepsAndCommitsTranscriptOnce(t *testing.T) {
	var requests atomic.Int32
	var continuationRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "/responses") {
			continuationRequests.Add(1)
		}
		_, _ = io.Copy(io.Discard, req.Body)
		call := requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch call {
		case 1:
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-durable-1","model":"gpt-5.4",
				"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_durable_echo","type":"function","function":{"name":"echo_tool","arguments":"{\"text\":\"once\"}"}}]},"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}
			}`)
		case 2:
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-durable-2","model":"gpt-5.4",
				"choices":[{"index":0,"message":{"role":"assistant","content":"durable done"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}
			}`)
		default:
			http.Error(w, "unexpected provider replay", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(server.Close)

	store := newTestStore(t)
	sessionID := "durable-replay-session"
	now := time.Now().UTC()
	sess := &storage.Session{
		ID: sessionID, Principal: "alice", Model: "gpt-5.4",
		Status: storage.SessionStatusActive, CreatedAt: now, LastActive: now,
	}
	if err := store.CreateSession(sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	evidenceStore, ledger := newRegistryDurableStores(t, store)
	if _, err := ensureForegroundRun(context.Background(), ledger, sessionID, sess.Model); err != nil {
		t.Fatalf("ensureForegroundRun: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai"
	cfg.Models.Execution = "gpt-5.4"
	cfg.Models.ProviderContinuation = true
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("model.NewManager: %v", err)
	}
	var toolCalls atomic.Int32
	tools := tool.NewEmptyRegistry()
	tools.Register(countingEchoTool{calls: &toolCalls})
	journal := &failOnceCompletionJournal{Journal: store}
	stepJournal, ok := ledger.(agentloop.DurableStepJournal)
	if !ok {
		t.Fatalf("ledger %T does not implement DurableStepJournal", ledger)
	}
	runner, err := NewRunner(RunnerConfig{
		Session: sess, ModelManager: mgr, Tools: tools, Store: store, Config: cfg,
		CommandJournal: journal, RunLedger: ledger, EvidenceStore: evidenceStore,
		StepJournal: stepJournal, LeaseOwner: "durable-replay-owner",
		DurableTiming: &DurableTiming{
			LeaseDuration: 500 * time.Millisecond, HeartbeatInterval: 50 * time.Millisecond,
			ScanInterval: 10 * time.Millisecond, CancellationInterval: 10 * time.Millisecond,
			OperationTimeout: time.Second,
		},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	t.Cleanup(runner.Stop)

	commandID := "durable-replay-command"
	receipt, err := runner.AcceptCommand(context.Background(), command.SessionCommand{
		SessionID: sessionID, ID: commandID, Type: "input",
		Content: "please echo exactly once", AcceptedBy: "alice",
	})
	if err != nil {
		t.Fatalf("AcceptCommand: %v", err)
	}
	if receipt.CommandID != commandID || receipt.RunID != sessionexec.RunIDForSession(sessionID) ||
		receipt.TaskID != sessionexec.ForegroundTaskID || receipt.TurnID != sessionexec.TurnID(commandID, 0) {
		t.Fatalf("receipt identity = %+v", receipt.Identity)
	}
	terminal := waitForCommandState(t, journal, sessionID, commandID, sessionexec.StateSucceeded)
	if terminal.Attempt != 2 {
		t.Fatalf("terminal attempt = %d, want 2 after injected completion failure", terminal.Attempt)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("provider calls = %d, want two original rounds and no replay calls", got)
	}
	if got := toolCalls.Load(); got != 1 {
		t.Fatalf("tool calls = %d, want one", got)
	}
	if got := continuationRequests.Load(); got != 0 {
		t.Fatalf("provider continuation requests = %d, want zero", got)
	}

	reloaded := conversation.New(sessionID)
	if err := reloaded.LoadFromStorage(store); err != nil {
		t.Fatalf("LoadFromStorage: %v", err)
	}
	var users, assistants, toolMessages int
	for _, message := range reloaded.Messages {
		switch message.Role {
		case "user":
			users++
		case "assistant":
			assistants++
		case "tool":
			toolMessages++
		}
	}
	if users != 1 || assistants != 2 || toolMessages != 1 {
		t.Fatalf("durable transcript roles = user:%d assistant:%d tool:%d, want 1/2/1", users, assistants, toolMessages)
	}
}

func TestDurableRunner_AmbiguousDispatchedStepCompletesBlockedWithoutProviderCall(t *testing.T) {
	store := newTestStore(t)
	sessionID := "durable-ambiguous-session"
	now := time.Now().UTC()
	sess := &storage.Session{
		ID: sessionID, Principal: "alice", Model: config.DefaultExecutionModel,
		Status: storage.SessionStatusActive, CreatedAt: now, LastActive: now,
	}
	if err := store.CreateSession(sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	evidenceStore, ledger := newRegistryDurableStores(t, store)
	if _, err := ensureForegroundRun(context.Background(), ledger, sessionID, sess.Model); err != nil {
		t.Fatalf("ensureForegroundRun: %v", err)
	}
	baseStepJournal, ok := ledger.(agentloop.DurableStepJournal)
	if !ok {
		t.Fatalf("ledger %T does not implement DurableStepJournal", ledger)
	}
	runner, err := NewRunner(RunnerConfig{
		Session: sess, ModelManager: newTestModelManager(t), Tools: tool.NewEmptyRegistry(),
		Store: store, Config: config.DefaultConfig(), CommandJournal: store,
		RunLedger: ledger, EvidenceStore: evidenceStore,
		StepJournal: recoveryRequiredStepJournal{DurableStepJournal: baseStepJournal},
		LeaseOwner:  "durable-ambiguous-owner",
		DurableTiming: &DurableTiming{
			LeaseDuration: time.Second, HeartbeatInterval: 100 * time.Millisecond,
			ScanInterval: 10 * time.Millisecond, CancellationInterval: 10 * time.Millisecond,
			OperationTimeout: time.Second,
		},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	t.Cleanup(runner.Stop)

	commandID := "durable-ambiguous-command"
	if _, err := runner.AcceptCommand(context.Background(), command.SessionCommand{
		SessionID: sessionID, ID: commandID, Type: "input",
		Content: "must not redispatch", AcceptedBy: "alice",
	}); err != nil {
		t.Fatalf("AcceptCommand: %v", err)
	}
	receipt := waitForCommandState(t, store, sessionID, commandID, sessionexec.StateBlocked)
	if receipt.ErrorCode != "durable_recovery_required" {
		t.Fatalf("error code = %q, want durable_recovery_required", receipt.ErrorCode)
	}
	reloaded := conversation.New(sessionID)
	if err := reloaded.LoadFromStorage(store); err != nil {
		t.Fatalf("LoadFromStorage: %v", err)
	}
	if len(reloaded.Messages) != 1 || reloaded.Messages[0].Role != "user" {
		t.Fatalf("blocked transcript = %+v, want claimed user prefix only", reloaded.Messages)
	}
}

func TestDurableRunner_ControlLaneCoexistsWithWorkAndPauseStopsNewClaims(t *testing.T) {
	workEntered := make(chan struct{}, 1)
	releaseWork := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		select {
		case workEntered <- struct{}{}:
		default:
		}
		select {
		case <-releaseWork:
		case <-req.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-lanes","model":"gpt-4o",
			"choices":[{"index":0,"message":{"role":"assistant","content":"work done"},"finish_reason":"stop"}]
		}`)
	}))
	t.Cleanup(server.Close)

	runner, store := newDurableHTTPRunner(t, "durable-lanes-session", server.URL)
	workID := "durable-lanes-work"
	if _, err := runner.AcceptCommand(context.Background(), command.SessionCommand{
		SessionID: runner.sessionID, ID: workID, Type: "input", Content: "hold work", AcceptedBy: "alice",
	}); err != nil {
		t.Fatalf("accept work: %v", err)
	}
	select {
	case <-workEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("work command did not begin provider call")
	}

	pauseID := "durable-lanes-pause"
	if _, err := runner.AcceptCommand(context.Background(), command.SessionCommand{
		SessionID: runner.sessionID, ID: pauseID, Type: "pause", AcceptedBy: "alice",
	}); err != nil {
		t.Fatalf("accept pause: %v", err)
	}
	waitForCommandState(t, store, runner.sessionID, pauseID, sessionexec.StateSucceeded)
	storedSession, err := store.GetSession(runner.sessionID)
	if err != nil || storedSession == nil || storedSession.Status != storage.SessionStatusPaused {
		t.Fatalf("paused storage state = session:%+v err:%v", storedSession, err)
	}

	modelID := "durable-lanes-model"
	if _, err := runner.AcceptCommand(context.Background(), command.SessionCommand{
		SessionID: runner.sessionID, ID: modelID, Type: "model", Content: "gpt-4o", AcceptedBy: "alice",
	}); err != nil {
		t.Fatalf("accept model: %v", err)
	}
	close(releaseWork)
	waitForCommandState(t, store, runner.sessionID, workID, sessionexec.StateSucceeded)
	time.Sleep(100 * time.Millisecond)
	pending, err := store.Get(context.Background(), runner.sessionID, modelID)
	if err != nil {
		t.Fatalf("Get paused model command: %v", err)
	}
	if pending.State != sessionexec.StateAccepted {
		t.Fatalf("paused work state = %s, want accepted", pending.State)
	}

	resumeID := "durable-lanes-resume"
	if _, err := runner.AcceptCommand(context.Background(), command.SessionCommand{
		SessionID: runner.sessionID, ID: resumeID, Type: "resume", AcceptedBy: "alice",
	}); err != nil {
		t.Fatalf("accept resume: %v", err)
	}
	waitForCommandState(t, store, runner.sessionID, resumeID, sessionexec.StateSucceeded)
	waitForCommandState(t, store, runner.sessionID, modelID, sessionexec.StateSucceeded)
}

func TestDurableRunner_DuplicateSteerCannotCancelLaterCommand(t *testing.T) {
	var calls atomic.Int32
	firstEntered := make(chan struct{}, 1)
	releaseFirst := make(chan struct{})
	steerEntered := make(chan struct{}, 1)
	steerCancelled := make(chan struct{}, 1)
	releaseSteer := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch calls.Add(1) {
		case 1:
			firstEntered <- struct{}{}
			select {
			case <-releaseFirst:
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{
					"id":"chatcmpl-original","model":"gpt-4o",
					"choices":[{"index":0,"message":{"role":"assistant","content":"original"},"finish_reason":"stop"}]
				}`)
			case <-req.Context().Done():
				return
			}
		case 2:
			steerEntered <- struct{}{}
			select {
			case <-releaseSteer:
			case <-req.Context().Done():
				steerCancelled <- struct{}{}
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-steer","model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","content":"steered"},"finish_reason":"stop"}]
			}`)
		default:
			http.Error(w, "unexpected provider call", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(server.Close)

	runner, store := newDurableHTTPRunner(t, "durable-steer-session", server.URL)
	firstID := "durable-steer-first"
	if _, err := runner.AcceptCommand(context.Background(), command.SessionCommand{
		SessionID: runner.sessionID, ID: firstID, Type: "input", Content: "original", AcceptedBy: "alice",
	}); err != nil {
		t.Fatalf("accept first: %v", err)
	}
	select {
	case <-firstEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("first provider call did not begin")
	}

	steerCommand := command.SessionCommand{
		SessionID: runner.sessionID, ID: "durable-steer-command",
		Type: "steer", Content: "new direction", AcceptedBy: "alice",
	}
	accepted, err := runner.AcceptCommand(context.Background(), steerCommand)
	if err != nil {
		t.Fatalf("accept steer: %v", err)
	}
	if accepted.TargetCommandID != firstID {
		t.Fatalf("steer target = %q, want %q", accepted.TargetCommandID, firstID)
	}
	runner.mu.RLock()
	_, interrupted := runner.interruptedCommands[firstID]
	runner.mu.RUnlock()
	if !interrupted {
		t.Fatal("steer did not mark its captured target interrupted")
	}
	close(releaseFirst)
	waitForCommandState(t, store, runner.sessionID, firstID, sessionexec.StateInterrupted)
	select {
	case <-steerEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("steer did not execute after interrupted work")
	}

	duplicate, err := runner.AcceptCommand(context.Background(), steerCommand)
	if err != nil {
		t.Fatalf("duplicate steer: %v", err)
	}
	if !duplicate.Duplicate || duplicate.TargetCommandID != firstID {
		t.Fatalf("duplicate steer receipt = %+v", duplicate)
	}
	runner.mu.RLock()
	_, wronglyInterrupted := runner.interruptedCommands[steerCommand.ID]
	runner.mu.RUnlock()
	if wronglyInterrupted {
		t.Fatal("duplicate steer marked the later command interrupted")
	}
	select {
	case <-steerCancelled:
		t.Fatal("duplicate steer cancelled the later active steer command")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseSteer)
	waitForCommandState(t, store, runner.sessionID, steerCommand.ID, sessionexec.StateSucceeded)
	if got := calls.Load(); got != 2 {
		t.Fatalf("provider calls = %d, want 2", got)
	}
}

func TestDurableRunner_LostWakeIsRecoveredByScanAndDuplicateWakeDoesNotReexecute(t *testing.T) {
	runner, store := newDurableHTTPRunner(t, "durable-scan-session", "http://127.0.0.1:1")
	commandValue := command.SessionCommand{
		SessionID: runner.sessionID, ID: "durable-scan-model", Type: "model",
		Content: "gpt-4o", AcceptedBy: "alice",
	}
	accepted, err := runner.acceptDurableCommand(context.Background(), commandValue, false, false, true)
	if err != nil {
		t.Fatalf("accept without wake: %v", err)
	}
	if accepted.Duplicate {
		t.Fatal("first acceptance reported duplicate")
	}
	terminal := waitForCommandState(t, store, runner.sessionID, commandValue.ID, sessionexec.StateSucceeded)
	if terminal.Attempt != 1 {
		t.Fatalf("scan-claimed attempt = %d, want 1", terminal.Attempt)
	}
	duplicate, err := runner.AcceptCommand(context.Background(), commandValue)
	if err != nil {
		t.Fatalf("duplicate acceptance: %v", err)
	}
	if !duplicate.Duplicate || duplicate.State != sessionexec.StateSucceeded || duplicate.Attempt != 1 {
		t.Fatalf("duplicate terminal receipt = %+v", duplicate)
	}
	runner.wakeDurableLane(sessionexec.LaneWork)
	time.Sleep(100 * time.Millisecond)
	unchanged, err := store.Get(context.Background(), runner.sessionID, commandValue.ID)
	if err != nil {
		t.Fatalf("Get after duplicate wake: %v", err)
	}
	if unchanged.State != sessionexec.StateSucceeded || unchanged.Attempt != 1 {
		t.Fatalf("duplicate wake changed command = %+v", unchanged)
	}
}

func TestDurableRunner_SlashSideEffectsBlockAndReadCommandsStayReadOnly(t *testing.T) {
	runner, store := newDurableHTTPRunner(t, "durable-slash-session", "http://127.0.0.1:1")
	blockedID := "durable-slash-clear"
	if _, err := runner.AcceptCommand(context.Background(), command.SessionCommand{
		SessionID: runner.sessionID, ID: blockedID, Type: "slash", Content: "/clear", AcceptedBy: "alice",
	}); err != nil {
		t.Fatalf("accept /clear: %v", err)
	}
	blocked := waitForCommandState(t, store, runner.sessionID, blockedID, sessionexec.StateBlocked)
	if blocked.ErrorCode != "durability_not_supported" {
		t.Fatalf("/clear error code = %q", blocked.ErrorCode)
	}

	planningDir := filepath.Join(runner.session.ProjectPath, "docs", "plans")
	plansID := "durable-slash-plans"
	if _, err := runner.AcceptCommand(context.Background(), command.SessionCommand{
		SessionID: runner.sessionID, ID: plansID, Type: "slash", Content: "/plans", AcceptedBy: "alice",
	}); err != nil {
		t.Fatalf("accept /plans: %v", err)
	}
	waitForCommandState(t, store, runner.sessionID, plansID, sessionexec.StateSucceeded)
	if _, err := os.Stat(planningDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only /plans created %q: %v", planningDir, err)
	}
	reloaded := conversation.New(runner.sessionID)
	if err := reloaded.LoadFromStorage(store); err != nil {
		t.Fatalf("LoadFromStorage: %v", err)
	}
	if len(reloaded.Messages) != 1 || reloaded.Messages[0].Role != "system" ||
		!strings.Contains(conversation.GetContentAsString(reloaded.Messages[0].Content), "No saved plans") {
		t.Fatalf("/plans transcript = %+v", reloaded.Messages)
	}
}

func TestDurableRunner_StopReleasesActiveLeaseWithoutTerminalEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-stop","model":"gpt-4o",
			"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_blocking","type":"function","function":{"name":"blocking_tool","arguments":"{}"}}]},"finish_reason":"tool_calls"}]
		}`)
	}))
	t.Cleanup(server.Close)

	store := newTestStore(t)
	sessionID := "durable-stop-session"
	now := time.Now().UTC()
	sess := &storage.Session{
		ID: sessionID, Principal: "alice", Model: "gpt-4o", ProjectPath: t.TempDir(),
		Status: storage.SessionStatusActive, CreatedAt: now, LastActive: now,
	}
	if err := store.CreateSession(sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	evidenceStore, ledger := newRegistryDurableStores(t, store)
	if _, err := ensureForegroundRun(context.Background(), ledger, sessionID, sess.Model); err != nil {
		t.Fatalf("ensureForegroundRun: %v", err)
	}
	stepJournal := ledger.(agentloop.DurableStepJournal)
	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai"
	cfg.Models.Execution = "gpt-4o"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("model.NewManager: %v", err)
	}
	toolEntered := make(chan struct{}, 1)
	tools := tool.NewEmptyRegistry()
	tools.Register(blockingContextTool{entered: toolEntered})
	capture := &runnerLifecycleCapture{}
	runner, err := NewRunner(RunnerConfig{
		Session: sess, ModelManager: mgr, Tools: tools, Store: store, Config: cfg, Emitter: capture,
		CommandJournal: store, RunLedger: ledger, EvidenceStore: evidenceStore, StepJournal: stepJournal,
		LeaseOwner: "durable-stop-owner",
		DurableTiming: &DurableTiming{
			LeaseDuration: time.Second, HeartbeatInterval: 100 * time.Millisecond,
			ScanInterval: 20 * time.Millisecond, CancellationInterval: 10 * time.Millisecond,
			OperationTimeout: time.Second,
		},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	commandID := "durable-stop-command"
	if _, err := runner.AcceptCommand(context.Background(), command.SessionCommand{
		SessionID: sessionID, ID: commandID, Type: "input", Content: "block in tool", AcceptedBy: "alice",
	}); err != nil {
		t.Fatalf("AcceptCommand: %v", err)
	}
	select {
	case <-toolEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("blocking tool did not start")
	}
	stopDone := make(chan struct{})
	go func() {
		runner.Stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not join durable pumps")
	}
	receipt, err := store.Get(context.Background(), sessionID, commandID)
	if err != nil {
		t.Fatalf("Get released command: %v", err)
	}
	if receipt.State != sessionexec.StateAccepted || receipt.Attempt != 1 {
		t.Fatalf("released command = %+v, want accepted attempt 1", receipt)
	}
	capture.mu.Lock()
	events := append([]RunnerEvent(nil), capture.runnerEvents...)
	capture.mu.Unlock()
	for _, event := range events {
		switch event.Type {
		case EventCommandCompleted, EventCommandFailed, EventCommandInterrupted, EventCommandBlocked:
			t.Fatalf("Stop emitted terminal command event: %+v", event)
		}
	}
	reloaded := conversation.New(sessionID)
	if err := reloaded.LoadFromStorage(store); err != nil {
		t.Fatalf("LoadFromStorage: %v", err)
	}
	if len(reloaded.Messages) != 1 || reloaded.Messages[0].Role != "user" {
		t.Fatalf("released transcript = %+v, want claimed user only", reloaded.Messages)
	}
}

func TestDurableRunner_StaleHeartbeatDiscardsOutputWithoutTerminalEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-stale","model":"gpt-4o",
			"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_stale_block","type":"function","function":{"name":"blocking_tool","arguments":"{}"}}]},"finish_reason":"tool_calls"}]
		}`)
	}))
	t.Cleanup(server.Close)
	store := newTestStore(t)
	sessionID := "durable-stale-heartbeat"
	now := time.Now().UTC()
	sess := &storage.Session{
		ID: sessionID, Principal: "alice", Model: "gpt-4o",
		Status: storage.SessionStatusActive, CreatedAt: now, LastActive: now,
	}
	if err := store.CreateSession(sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	evidenceStore, ledger := newRegistryDurableStores(t, store)
	if _, err := ensureForegroundRun(context.Background(), ledger, sessionID, sess.Model); err != nil {
		t.Fatalf("ensureForegroundRun: %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai"
	cfg.Models.Execution = "gpt-4o"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("model.NewManager: %v", err)
	}
	toolEntered := make(chan struct{}, 1)
	tools := tool.NewEmptyRegistry()
	tools.Register(blockingContextTool{entered: toolEntered})
	heartbeat := make(chan struct{}, 1)
	journal := staleHeartbeatJournal{Journal: store, called: heartbeat}
	capture := &runnerLifecycleCapture{}
	runner, err := NewRunner(RunnerConfig{
		Session: sess, ModelManager: mgr, Tools: tools, Store: store, Config: cfg, Emitter: capture,
		CommandJournal: journal, RunLedger: ledger, EvidenceStore: evidenceStore,
		StepJournal: ledger.(agentloop.DurableStepJournal), LeaseOwner: "durable-stale-owner",
		DurableTiming: &DurableTiming{
			LeaseDuration: 3 * time.Second, HeartbeatInterval: 500 * time.Millisecond,
			ScanInterval: 20 * time.Millisecond, CancellationInterval: 10 * time.Millisecond,
			OperationTimeout: time.Second,
		},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	defer runner.Stop()
	commandID := "durable-stale-command"
	if _, err := runner.AcceptCommand(context.Background(), command.SessionCommand{
		SessionID: sessionID, ID: commandID, Type: "input", Content: "lose lease", AcceptedBy: "alice",
	}); err != nil {
		t.Fatalf("AcceptCommand: %v", err)
	}
	select {
	case <-toolEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("blocking tool did not start")
	}
	select {
	case <-heartbeat:
	case <-time.After(5 * time.Second):
		t.Fatal("heartbeat did not run")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runner.mu.RLock()
		active := runner.activeCommandID
		runner.mu.RUnlock()
		if active == "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	receipt, err := store.Get(context.Background(), sessionID, commandID)
	if err != nil {
		t.Fatalf("Get stale command: %v", err)
	}
	if receipt.State != sessionexec.StateRunning {
		t.Fatalf("stale command state = %s, want running for expiry takeover", receipt.State)
	}
	capture.mu.Lock()
	events := append([]RunnerEvent(nil), capture.runnerEvents...)
	capture.mu.Unlock()
	for _, event := range events {
		switch event.Type {
		case EventCommandCompleted, EventCommandFailed, EventCommandInterrupted, EventCommandBlocked:
			t.Fatalf("stale owner emitted terminal event: %+v", event)
		}
	}
	reloaded := conversation.New(sessionID)
	if err := reloaded.LoadFromStorage(store); err != nil {
		t.Fatalf("LoadFromStorage: %v", err)
	}
	if len(reloaded.Messages) != 1 || reloaded.Messages[0].Role != "user" {
		t.Fatalf("stale transcript = %+v, want claimed prefix only", reloaded.Messages)
	}
}

func TestRegistryDurableInitialPromptCommitsBeforeHooksAndRollsBackSilently(t *testing.T) {
	store := newTestStore(t)
	evidenceStore, ledger := newRegistryDurableStores(t, store)
	root := t.TempDir()
	createTestGitRepo(t, root)
	cfg := config.DefaultConfig()
	cfg.Hooks.Enabled = true
	release := make(chan struct{})
	probe := &observableHookProbe{
		activationEntered: make(chan struct{}, 1),
		releaseActivation: release,
		activationErr:     fmt.Errorf("injected hook activation failure"),
	}
	capture := &runnerLifecycleCapture{}
	store.AddObserver(storage.ObserverFunc(capture.recordStorage))
	registry := NewRegistry(RegistryConfig{
		Store: store, ModelManager: newTestModelManager(t), Config: cfg,
		ProjectRoot: root, Emitter: capture, RunLedger: ledger, EvidenceStore: evidenceStore,
	})
	registry.prepareHooks = probe.factory
	t.Cleanup(registry.Stop)

	type result struct {
		info *SessionInfo
		err  error
	}
	done := make(chan result, 1)
	go func() {
		info, err := registry.CreateSession(CreateSessionRequest{
			Principal: "alice", Project: root, Prompt: "durable initial prompt",
			InitialCommandID: "durable-initial-command",
		})
		done <- result{info: info, err: err}
	}()
	select {
	case <-probe.activationEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("hook activation did not begin")
	}
	sessionID := waitForSingleStoredSession(t, store)
	receipt, err := store.Get(context.Background(), sessionID, "durable-initial-command")
	if err != nil {
		t.Fatalf("initial command not committed before hooks: %v", err)
	}
	if receipt.State != sessionexec.StateAccepted || receipt.AcceptedAt.IsZero() {
		t.Fatalf("initial command before activation = %+v", receipt)
	}
	assertNoLifecycleEvents(t, capture)
	close(release)
	created := <-done
	if created.info != nil || created.err == nil || !strings.Contains(created.err.Error(), "injected hook activation failure") {
		t.Fatalf("CreateSession result = info:%+v err:%v", created.info, created.err)
	}
	if stored, err := store.GetSession(sessionID); err != nil || stored != nil {
		t.Fatalf("rolled-back session = %+v err=%v", stored, err)
	}
	if _, err := store.Get(context.Background(), sessionID, "durable-initial-command"); !errors.Is(err, sessionexec.ErrNotFound) {
		t.Fatalf("rolled-back initial command error = %v, want not found", err)
	}
	assertNoLifecycleEvents(t, capture)
}

func TestRegistryDurableInitialPromptIsAcceptedBeforeSuccessfulCreateReturns(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-initial","model":"gpt-4o",
			"choices":[{"index":0,"message":{"role":"assistant","content":"initial complete"},"finish_reason":"stop"}]
		}`)
	}))
	t.Cleanup(provider.Close)
	store := newTestStore(t)
	evidenceStore, ledger := newRegistryDurableStores(t, store)
	root := t.TempDir()
	createTestGitRepo(t, root)
	cfg := config.DefaultConfig()
	cfg.Hooks.Enabled = true
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = provider.URL
	cfg.Models.DefaultProvider = "openai"
	cfg.Models.Execution = "gpt-4o"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("model.NewManager: %v", err)
	}
	release := make(chan struct{})
	probe := &observableHookProbe{
		activationEntered: make(chan struct{}, 1), releaseActivation: release,
	}
	capture := &runnerLifecycleCapture{}
	registry := NewRegistry(RegistryConfig{
		Store: store, ModelManager: mgr, Config: cfg, ProjectRoot: root,
		Emitter: capture, RunLedger: ledger, EvidenceStore: evidenceStore,
	})
	registry.prepareHooks = probe.factory
	t.Cleanup(registry.Stop)
	type result struct {
		info *SessionInfo
		err  error
	}
	done := make(chan result, 1)
	go func() {
		info, err := registry.CreateSession(CreateSessionRequest{
			Principal: "alice", Project: root, Prompt: "successful initial prompt",
			InitialCommandID: "durable-successful-initial",
		})
		done <- result{info: info, err: err}
	}()
	select {
	case <-probe.activationEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("hook activation did not begin")
	}
	sessionID := waitForSingleStoredSession(t, store)
	receipt, err := store.Get(context.Background(), sessionID, "durable-successful-initial")
	if err != nil || receipt.State != sessionexec.StateAccepted {
		t.Fatalf("pre-return initial receipt = %+v err=%v", receipt, err)
	}
	assertNoLifecycleEvents(t, capture)
	close(release)
	created := <-done
	if created.err != nil || created.info == nil || created.info.ID != sessionID {
		t.Fatalf("CreateSession = info:%+v err:%v", created.info, created.err)
	}
	if created.info.InitialReceipt == nil || created.info.InitialReceipt.CommandID != "durable-successful-initial" ||
		created.info.InitialReceipt.SessionID != sessionID || created.info.InitialReceipt.Duplicate {
		t.Fatalf("initial receipt = %+v", created.info.InitialReceipt)
	}
	if current, ok := registry.GetSessionInfo(sessionID); !ok || current.InitialReceipt != nil {
		t.Fatalf("GetSessionInfo replayed initial receipt: info=%+v ok=%v", current, ok)
	}
	for _, listed := range registry.ListSessions() {
		if listed.ID == sessionID && listed.InitialReceipt != nil {
			t.Fatalf("ListSessions replayed initial receipt: %+v", listed.InitialReceipt)
		}
	}
	waitForCommandState(t, store, sessionID, "durable-successful-initial", sessionexec.StateSucceeded)
}

func TestRegistryDurableForegroundRunIsStableAndCorruptionFailsClosed(t *testing.T) {
	store := newTestStore(t)
	evidenceStore, ledger := newRegistryDurableStores(t, store)
	root := t.TempDir()
	createTestGitRepo(t, root)
	registry := NewRegistry(RegistryConfig{
		Store: store, ModelManager: newTestModelManager(t), Config: config.DefaultConfig(),
		ProjectRoot: root, RunLedger: ledger, EvidenceStore: evidenceStore,
	})
	t.Cleanup(registry.Stop)
	info, err := registry.CreateSession(CreateSessionRequest{Principal: "alice", Project: root})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if info.InitialReceipt != nil {
		t.Fatalf("no-prompt session returned initial receipt: %+v", info.InitialReceipt)
	}
	run, err := ledger.GetRun(context.Background(), sessionexec.RunIDForSession(info.ID))
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.SessionID != info.ID || run.TaskID != sessionexec.ForegroundTaskID ||
		run.AgentID != "headless" || run.Backend != foregroundBackend || run.Status != "running" || run.EndedAt != nil {
		t.Fatalf("foreground run = %+v", run)
	}

	corruptSessionID := "durable-corrupt-foreground"
	now := time.Now().UTC()
	if err := store.CreateSession(&storage.Session{
		ID: corruptSessionID, Principal: "alice", ProjectPath: root,
		Status: storage.SessionStatusActive, CreatedAt: now, LastActive: now,
	}); err != nil {
		t.Fatalf("CreateSession(corrupt): %v", err)
	}
	if _, err := ledger.StartRun(context.Background(), runledger.AgentRun{
		RunID: sessionexec.RunIDForSession(corruptSessionID), SessionID: corruptSessionID,
		TaskID: "wrong-task", AgentID: "headless", Backend: foregroundBackend, Status: "running",
	}); err != nil {
		t.Fatalf("StartRun(corrupt): %v", err)
	}
	if runner, err := registry.EnsureSession(corruptSessionID); runner != nil || err == nil || !strings.Contains(err.Error(), "identity mismatch") {
		t.Fatalf("EnsureSession(corrupt) = runner:%+v err:%v", runner, err)
	}
}

func TestRegistryDurableForegroundRequiresCurrentFencedStepJournal(t *testing.T) {
	store := newTestStore(t)
	evidenceStore, ledger := newRegistryDurableStores(t, store)
	root := t.TempDir()
	createTestGitRepo(t, root)
	type storeOnlyLedger struct{ runledger.Store }
	registry := NewRegistry(RegistryConfig{
		Store: store, ModelManager: newTestModelManager(t), Config: config.DefaultConfig(),
		ProjectRoot: root, RunLedger: storeOnlyLedger{Store: ledger}, EvidenceStore: evidenceStore,
	})
	if info, err := registry.CreateSession(CreateSessionRequest{Principal: "alice", Project: root}); info != nil || err == nil || !strings.Contains(err.Error(), "fenced step journal") {
		t.Fatalf("CreateSession without fenced journal = info:%+v err:%v", info, err)
	}
	if sessions, err := store.ListSessions(10); err != nil || len(sessions) != 0 {
		t.Fatalf("partial capability persisted sessions = %d err=%v", len(sessions), err)
	}
}

func TestDurableApproval_DBPollingSurvivesLostWakeAndRestart(t *testing.T) {
	dbPath := t.TempDir() + "/approval-poll.db"
	primary, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("storage.New(primary): %v", err)
	}
	t.Cleanup(func() { _ = primary.Close() })
	secondary, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("storage.New(secondary): %v", err)
	}
	t.Cleanup(func() { _ = secondary.Close() })
	sessionID := "durable-approval-session"
	now := time.Now().UTC()
	if err := primary.CreateSession(&storage.Session{
		ID: sessionID, Principal: "alice", Status: storage.SessionStatusActive,
		CreatedAt: now, LastActive: now,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	runner := &Runner{
		sessionID: sessionID, store: primary, durable: true,
		approvalChan: make(chan ApprovalResponse, 1), state: StateProcessing,
	}
	local := &PendingApproval{
		ID: "durable-approval-id", ToolName: "run_shell",
		ToolArgs:  map[string]any{"command": "go test ./..."},
		CreatedAt: now, ExpiresAt: now.Add(time.Minute),
	}
	candidate := &storage.PendingApproval{
		ID: local.ID, SessionID: sessionID, ToolName: local.ToolName,
		ToolInput: `{"command":"go test ./..."}`, RiskScore: 40,
		Status: "pending", CreatedAt: now, ExpiresAt: now.Add(time.Minute),
	}
	type decisionResult struct {
		approved bool
		err      error
	}
	result := make(chan decisionResult, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		approved, err := runner.waitForDurableApproval(ctx, local, candidate)
		result <- decisionResult{approved: approved, err: err}
	}()
	waitForPendingApproval(t, secondary, candidate.ID)
	if _, _, err := secondary.DecidePendingApproval(candidate.ID, sessionID, "approved", "alice", "", time.Now().UTC()); err != nil {
		t.Fatalf("DecidePendingApproval(second store): %v", err)
	}
	select {
	case got := <-result:
		if got.err != nil || !got.approved {
			t.Fatalf("lost-wake decision = approved:%v err:%v", got.approved, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("durable approval polling did not observe external decision")
	}

	// A fresh runner first reads the already-committed decision and returns
	// without requiring the old process-local wake channel.
	restarted := &Runner{
		sessionID: sessionID, store: primary, durable: true,
		approvalChan: make(chan ApprovalResponse, 1), state: StateProcessing,
	}
	approved, err := restarted.waitForDurableApproval(context.Background(), &PendingApproval{
		ID: local.ID, ToolName: local.ToolName, ToolArgs: local.ToolArgs,
	}, candidate)
	if err != nil || !approved {
		t.Fatalf("restart decision = approved:%v err:%v", approved, err)
	}
}

func waitForPendingApproval(t *testing.T, store *storage.Store, id string) *storage.PendingApproval {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		approval, err := store.GetPendingApproval(id)
		if err != nil {
			t.Fatalf("GetPendingApproval: %v", err)
		}
		if approval != nil {
			return approval
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pending approval %q was not created", id)
	return nil
}

func newDurableHTTPRunner(t *testing.T, sessionID, baseURL string) (*Runner, *storage.Store) {
	t.Helper()
	return newDurableHTTPRunnerConfigured(t, sessionID, baseURL, nil)
}

func newDurableHTTPRunnerConfigured(t *testing.T, sessionID, baseURL string, configure func(*RunnerConfig, *storage.Store)) (*Runner, *storage.Store) {
	t.Helper()
	store := newTestStore(t)
	now := time.Now().UTC()
	sess := &storage.Session{
		ID: sessionID, Principal: "alice", Model: "gpt-4o", ProjectPath: t.TempDir(),
		Status: storage.SessionStatusActive, CreatedAt: now, LastActive: now,
	}
	if err := store.CreateSession(sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	evidenceStore, ledger := newRegistryDurableStores(t, store)
	if _, err := ensureForegroundRun(context.Background(), ledger, sessionID, sess.Model); err != nil {
		t.Fatalf("ensureForegroundRun: %v", err)
	}
	stepJournal, ok := ledger.(agentloop.DurableStepJournal)
	if !ok {
		t.Fatalf("ledger %T does not implement DurableStepJournal", ledger)
	}
	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = baseURL
	cfg.Models.DefaultProvider = "openai"
	cfg.Models.Execution = "gpt-4o"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("model.NewManager: %v", err)
	}
	runnerConfig := RunnerConfig{
		Session: sess, ModelManager: mgr, Tools: tool.NewEmptyRegistry(), Store: store, Config: cfg,
		CommandJournal: store, RunLedger: ledger, EvidenceStore: evidenceStore, StepJournal: stepJournal,
		LeaseOwner: "owner-" + sessionID,
		DurableTiming: &DurableTiming{
			LeaseDuration: 5 * time.Second, HeartbeatInterval: 100 * time.Millisecond,
			ScanInterval: 20 * time.Millisecond, CancellationInterval: 10 * time.Millisecond,
			OperationTimeout: time.Second,
		},
	}
	if configure != nil {
		configure(&runnerConfig, store)
	}
	runner, err := NewRunner(runnerConfig)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	t.Cleanup(runner.Stop)
	return runner, store
}

func waitForCommandState(t *testing.T, journal sessionexec.Journal, sessionID, commandID string, want sessionexec.State) sessionexec.Receipt {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		receipt, err := journal.Get(context.Background(), sessionID, commandID)
		if err == nil && receipt.State == want {
			return receipt
		}
		if err != nil && !errorsIsSessionExecNotFound(err) {
			t.Fatalf("Get command: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	receipt, err := journal.Get(context.Background(), sessionID, commandID)
	t.Fatalf("command did not reach %s: receipt=%+v err=%v", want, receipt, err)
	return sessionexec.Receipt{}
}

func errorsIsSessionExecNotFound(err error) bool {
	return errors.Is(err, sessionexec.ErrNotFound)
}
