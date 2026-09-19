package subagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/agentcoord"
	"m31labs.dev/buckley/pkg/persona"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/telemetry"
)

type runnerFunc func(context.Context, Request, func(int)) (string, error)

func (f runnerFunc) Run(ctx context.Context, request Request, started func(int)) (string, error) {
	return f(ctx, request, started)
}

type typedNilRunner struct{}

func (*typedNilRunner) Run(context.Context, Request, func(int)) (string, error) {
	panic("typed-nil runner must not execute")
}

type interactiveRunnerFunc func(context.Context, Request, func(int), <-chan CommandDelivery) (string, error)

func (f interactiveRunnerFunc) Run(ctx context.Context, request Request, started func(int)) (string, error) {
	return f(ctx, request, started, nil)
}

func (f interactiveRunnerFunc) RunInteractive(ctx context.Context, request Request, started func(int), commands <-chan CommandDelivery) (string, error) {
	return f(ctx, request, started, commands)
}

type capturedRunnerFunc func(context.Context, Request, func(int)) (CapturedOutput, error)

func (f capturedRunnerFunc) Run(ctx context.Context, request Request, started func(int)) (string, error) {
	capture, err := f(ctx, request, started)
	return capture.Preview, err
}

func (f capturedRunnerFunc) RunCaptured(ctx context.Context, request Request, started func(int)) (CapturedOutput, error) {
	return f(ctx, request, started)
}

func TestManager_SpawnTracksParentAndCompletion(t *testing.T) {
	manager := NewManager(runnerFunc(func(_ context.Context, request Request, started func(int)) (string, error) {
		if request.ParentSessionID != "parent-1" || request.Agent != "reviewer" || request.Task != "inspect this" {
			t.Fatalf("unexpected request: %+v", request)
		}
		started(42)
		return "complete output", nil
	}), 2)
	manager.SetTelemetry(nil, "parent-1")
	t.Cleanup(func() { _ = manager.Close() })

	spawned, err := manager.Spawn("reviewer", "daily", "inspect this", 30)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	finished, err := manager.Wait(context.Background(), spawned.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if finished.State != StateCompleted || finished.ParentSessionID != "parent-1" || finished.Task != "inspect this" || finished.PID != 42 || finished.Output != "complete output" {
		t.Fatalf("unexpected snapshot: %+v", finished)
	}
}

func TestManager_TypedNilRunnerFailsClosed(t *testing.T) {
	var runner *typedNilRunner
	manager := NewManager(runner, 1)
	if _, err := manager.SpawnWithOptions(SpawnOptions{Task: "do not execute"}); err == nil || !strings.Contains(err.Error(), "manager is unavailable") {
		t.Fatalf("typed-nil runner spawn = %v", err)
	}
}

func TestManager_HeartbeatJoinPrecedesTerminalClassification(t *testing.T) {
	heartbeatEntered := make(chan struct{})
	var heartbeatCalls atomic.Int32
	manager := NewManager(runnerFunc(func(context.Context, Request, func(int)) (string, error) {
		<-heartbeatEntered
		return "runner returned", nil
	}), 1)
	manager.SetHeartbeatObserver(func(ctx context.Context, _ Snapshot) error {
		if heartbeatCalls.Add(1) == 1 {
			return nil
		}
		close(heartbeatEntered)
		<-ctx.Done()
		return runledger.ErrAttachmentStale
	}, time.Millisecond)
	t.Cleanup(func() { _ = manager.Close() })

	spawned, err := manager.SpawnWithOptions(SpawnOptions{
		ID: "run-heartbeat-barrier", SessionID: "session-heartbeat-barrier", Task: "race terminal heartbeat",
		AttemptID: "attempt-heartbeat-barrier", LeaseGeneration: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	finished, err := manager.Wait(context.Background(), spawned.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.State != StateFailed || !strings.Contains(finished.Error, runledger.ErrAttachmentStale.Error()) {
		t.Fatalf("terminal snapshot = %+v, want durability failure", finished)
	}
}

func TestManager_InitialHeartbeatFailurePreventsRunnerLaunch(t *testing.T) {
	var runnerCalls atomic.Int32
	var terminalEvents atomic.Int32
	manager := NewManager(runnerFunc(func(context.Context, Request, func(int)) (string, error) {
		runnerCalls.Add(1)
		return "must not run", nil
	}), 1)
	manager.SetLifecycleObserver(func(snapshot Snapshot) {
		if snapshotTerminal(snapshot.State) {
			terminalEvents.Add(1)
		}
	})
	manager.SetHeartbeatObserver(func(context.Context, Snapshot) error {
		return runledger.ErrAttachmentExpired
	}, time.Millisecond)
	t.Cleanup(func() { _ = manager.Close() })

	spawned, err := manager.SpawnWithOptions(SpawnOptions{
		ID: "run-initial-heartbeat", SessionID: "session-initial-heartbeat", Task: "fenced before launch",
		AttemptID: "attempt-initial-heartbeat", LeaseGeneration: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	finished, err := manager.Wait(context.Background(), spawned.ID)
	if err != nil {
		t.Fatal(err)
	}
	if runnerCalls.Load() != 0 {
		t.Fatalf("runner calls = %d, want zero after initial renewal failure", runnerCalls.Load())
	}
	if terminalEvents.Load() != 1 {
		t.Fatalf("terminal lifecycle callbacks = %d, want one", terminalEvents.Load())
	}
	if finished.State != StateFailed || !strings.Contains(finished.Error, runledger.ErrAttachmentExpired.Error()) {
		t.Fatalf("terminal snapshot = %+v, want initial heartbeat failure", finished)
	}
}

func TestManager_InitialHeartbeatDelayedPastIntervalWithEndedTaskDoesNotStartPeriodicLoop(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	current := &run{
		ctx:    ctx,
		cancel: cancel,
		snapshot: Snapshot{
			ID: "run-delayed-initial-heartbeat", SessionID: "session-delayed-initial-heartbeat",
			AttemptID: "attempt-delayed-initial-heartbeat", LeaseGeneration: 1,
			State: StateRunning,
		},
	}
	var heartbeatCalls atomic.Int32
	manager := NewManager(runnerFunc(func(context.Context, Request, func(int)) (string, error) {
		return "must not run", nil
	}), 1)
	manager.SetHeartbeatObserver(func(context.Context, Snapshot) error {
		if heartbeatCalls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return nil
	}, 2*time.Millisecond)

	type heartbeatStart struct {
		stop func() error
		err  error
	}
	started := make(chan heartbeatStart, 1)
	go func() {
		stop, err := manager.startHeartbeat(current)
		started <- heartbeatStart{stop: stop, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("initial heartbeat did not start")
	}
	time.Sleep(10 * time.Millisecond)
	cancel()
	close(release)

	var result heartbeatStart
	select {
	case result = <-started:
	case <-time.After(time.Second):
		t.Fatal("initial heartbeat did not return")
	}
	if result.err != nil {
		t.Fatalf("start heartbeat: %v", result.err)
	}
	// Deliberately delay Stop: an armed periodic loop would renew again here.
	time.Sleep(10 * time.Millisecond)
	if got := heartbeatCalls.Load(); got != 1 {
		t.Fatalf("heartbeat calls = %d, want exactly one", got)
	}
	if err := result.stop(); err != nil {
		t.Fatalf("stop heartbeat: %v", err)
	}
}

func TestManager_TerminalHeartbeatCancellationPreservesRunnerClassification(t *testing.T) {
	tests := []struct {
		name      string
		runnerErr error
		wantState State
	}{
		{name: "completed", wantState: StateCompleted},
		{name: "failed", runnerErr: errors.New("runner failed exactly"), wantState: StateFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			heartbeatEntered := make(chan struct{})
			var heartbeatCalls atomic.Int32
			manager := NewManager(runnerFunc(func(context.Context, Request, func(int)) (string, error) {
				<-heartbeatEntered
				return "runner result", test.runnerErr
			}), 1)
			manager.SetHeartbeatObserver(func(ctx context.Context, _ Snapshot) error {
				if heartbeatCalls.Add(1) == 1 {
					return nil
				}
				close(heartbeatEntered)
				<-ctx.Done()
				return ctx.Err()
			}, time.Millisecond)
			t.Cleanup(func() { _ = manager.Close() })

			spawned, err := manager.SpawnWithOptions(SpawnOptions{
				ID: "run-terminal-cancel-" + test.name, SessionID: "session-terminal-cancel", Task: "classify runner",
				AttemptID: "attempt-terminal-cancel-" + test.name, LeaseGeneration: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			finished, err := manager.Wait(context.Background(), spawned.ID)
			if err != nil {
				t.Fatal(err)
			}
			if finished.State != test.wantState {
				t.Fatalf("terminal snapshot = %+v, want state %s", finished, test.wantState)
			}
			if test.runnerErr != nil && !strings.Contains(finished.Error, test.runnerErr.Error()) {
				t.Fatalf("runner failure was replaced: %+v", finished)
			}
			if strings.Contains(finished.Error, context.Canceled.Error()) {
				t.Fatalf("normal heartbeat stop overrode runner classification: %+v", finished)
			}
		})
	}
}

func TestManager_IndependentRenewalDeadlineSurvivesTerminalStop(t *testing.T) {
	failureInevitable := make(chan struct{})
	var heartbeatCalls atomic.Int32
	manager := NewManager(runnerFunc(func(context.Context, Request, func(int)) (string, error) {
		<-failureInevitable
		return "runner completed", nil
	}), 1)
	manager.SetHeartbeatObserver(func(ctx context.Context, _ Snapshot) error {
		if heartbeatCalls.Add(1) == 1 {
			return nil
		}
		close(failureInevitable)
		<-ctx.Done()
		return context.DeadlineExceeded
	}, time.Millisecond)
	t.Cleanup(func() { _ = manager.Close() })

	spawned, err := manager.SpawnWithOptions(SpawnOptions{
		ID: "run-renewal-deadline-race", SessionID: "session-renewal-deadline-race", Task: "preserve renewal failure",
		AttemptID: "attempt-renewal-deadline-race", LeaseGeneration: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	finished, err := manager.Wait(context.Background(), spawned.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.State != StateFailed || !strings.Contains(finished.Error, context.DeadlineExceeded.Error()) {
		t.Fatalf("terminal snapshot = %+v, want substantive renewal deadline", finished)
	}
}

func TestManager_OutputSpoolCeilingFailsExplicitlyAndCleansUp(t *testing.T) {
	spoolPath := filepath.Join(t.TempDir(), "child-output.log")
	if err := os.WriteFile(spoolPath, []byte(strings.Repeat("x", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(capturedRunnerFunc(func(context.Context, Request, func(int)) (CapturedOutput, error) {
		return CapturedOutput{
			Preview:       "bounded preview",
			SpoolPath:     spoolPath,
			ObservedBytes: 1024,
			CapturedBytes: 64,
			LimitBytes:    64,
			Truncated:     true,
		}, nil
	}), 1)
	spoolVisible := make(chan bool, 1)
	manager.SetLifecycleObserver(func(snapshot Snapshot) {
		if snapshotTerminal(snapshot.State) {
			_, err := os.Stat(snapshot.OutputSpoolPath)
			spoolVisible <- err == nil
		}
	})
	t.Cleanup(func() { _ = manager.Close() })

	spawned, err := manager.Spawn("reviewer", "", "produce a large report", 0)
	if err != nil {
		t.Fatal(err)
	}
	finished, err := manager.Wait(context.Background(), spawned.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.State != StateFailed || !finished.OutputTruncated || finished.OutputBytes != 1024 || finished.CapturedBytes != 64 {
		t.Fatalf("finished = %+v", finished)
	}
	if !strings.Contains(finished.Error, "64-byte disk ceiling") || !strings.Contains(finished.Error, "result is incomplete") {
		t.Fatalf("error = %q", finished.Error)
	}
	if !<-spoolVisible {
		t.Fatal("terminal observer could not read the output spool")
	}
	if _, err := os.Stat(spoolPath); !os.IsNotExist(err) {
		t.Fatalf("output spool was not removed after observation: %v", err)
	}
}

func TestManager_UsesStrictestExplicitElapsedLimit(t *testing.T) {
	deadlines := make(chan time.Duration, 1)
	manager := NewManager(runnerFunc(func(ctx context.Context, request Request, _ func(int)) (string, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			deadlines <- 0
		} else {
			deadlines <- time.Until(deadline)
		}
		if request.TimeoutSeconds != 30 || request.Budget.MaxElapsedSecond != 10 {
			t.Fatalf("request limits = %+v", request)
		}
		return "done", nil
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })

	spawned, err := manager.SpawnWithOptions(SpawnOptions{
		Task:           "inspect",
		TimeoutSeconds: 30,
		Budget:         agentcoord.Budget{MaxElapsedSecond: 10},
	})
	if err != nil {
		t.Fatalf("SpawnWithOptions: %v", err)
	}
	remaining := <-deadlines
	if remaining <= 9*time.Second || remaining > 10*time.Second {
		t.Fatalf("deadline remaining = %s, want strict 10s limit", remaining)
	}
	if _, err := manager.Wait(context.Background(), spawned.ID); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestManager_ZeroElapsedLimitsRemainUnbounded(t *testing.T) {
	deadlinePresent := make(chan bool, 1)
	manager := NewManager(runnerFunc(func(ctx context.Context, request Request, _ func(int)) (string, error) {
		_, ok := ctx.Deadline()
		deadlinePresent <- ok
		if request.TimeoutSeconds != 0 || request.Budget.MaxElapsedSecond != 0 {
			t.Fatalf("request limits = %+v", request)
		}
		return "done", nil
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })

	spawned, err := manager.SpawnWithOptions(SpawnOptions{Task: "inspect"})
	if err != nil {
		t.Fatalf("SpawnWithOptions: %v", err)
	}
	if <-deadlinePresent {
		t.Fatal("zero child elapsed limits unexpectedly created a deadline")
	}
	if _, err := manager.Wait(context.Background(), spawned.ID); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestManager_ElapsedLimitIsFailedNotUserCancelled(t *testing.T) {
	manager := NewManager(runnerFunc(func(ctx context.Context, _ Request, started func(int)) (string, error) {
		started(12)
		<-ctx.Done()
		return "", ctx.Err()
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })

	spawned, err := manager.SpawnWithOptions(SpawnOptions{
		Task:   "bounded",
		Budget: agentcoord.Budget{MaxElapsedSecond: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	finished, err := manager.Wait(context.Background(), spawned.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if finished.State != StateFailed || !strings.Contains(finished.Error, "elapsed-time limit exceeded") {
		t.Fatalf("finished = %+v, want failed elapsed-limit result", finished)
	}
}

func TestManager_CancelStopsOnlyRequestedChild(t *testing.T) {
	manager := NewManager(runnerFunc(func(ctx context.Context, _ Request, started func(int)) (string, error) {
		started(7)
		<-ctx.Done()
		return "", ctx.Err()
	}), 2)
	t.Cleanup(func() { _ = manager.Close() })

	first, err := manager.Spawn("one", "", "first", 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Spawn("two", "", "second", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Cancel(first.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	cancelled, err := manager.Wait(context.Background(), first.ID)
	if err != nil {
		t.Fatalf("Wait cancelled: %v", err)
	}
	if cancelled.State != StateCancelled {
		t.Fatalf("cancelled state = %s", cancelled.State)
	}
	if running, _ := manager.Status(second.ID); running.State != StateRunning {
		t.Fatalf("second state = %s, want running", running.State)
	}
	_, _ = manager.Cancel(second.ID)
}

func TestManager_DeliverAcknowledgesInteractiveRunner(t *testing.T) {
	received := make(chan agentcoord.Message, 1)
	manager := NewManager(interactiveRunnerFunc(func(ctx context.Context, _ Request, started func(int), commands <-chan CommandDelivery) (string, error) {
		started(8)
		select {
		case delivery := <-commands:
			received <- delivery.Message
			delivery.Acknowledge(nil)
		case <-ctx.Done():
			return "", ctx.Err()
		}
		<-ctx.Done()
		return "", ctx.Err()
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })

	run, err := manager.Spawn("reviewer", "", "inspect", 0)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	message := agentcoord.Message{ID: "msg-live", RunID: run.ID, To: run.ID, From: "parent", Content: "inspect the caller"}
	if err := manager.Deliver(context.Background(), run.ID, message); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	select {
	case got := <-received:
		if got.ID != message.ID || got.Content != message.Content {
			t.Fatalf("delivered message = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("interactive runner did not receive command")
	}
	_, _ = manager.Cancel(run.ID)
}

func TestManager_PublishesLifecycleTelemetry(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()
	events, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	manager := NewManager(runnerFunc(func(_ context.Context, _ Request, _ func(int)) (string, error) {
		return "", errors.New("boom")
	}), 1)
	manager.SetTelemetry(hub, "parent-telemetry")
	t.Cleanup(func() { _ = manager.Close() })

	spawned, err := manager.Spawn("reviewer", "", "fail", 30)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Wait(context.Background(), spawned.ID); err != nil {
		t.Fatal(err)
	}

	want := []telemetry.EventType{telemetry.EventSubagentSpawned, telemetry.EventSubagentFailed}
	for _, eventType := range want {
		select {
		case event := <-events:
			if event.Type != eventType || event.SessionID != "parent-telemetry" || event.TaskID != spawned.ID {
				t.Fatalf("event = %+v, want type=%s", event, eventType)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s", eventType)
		}
	}
}

// TestManager_PublishesRedactedOutput guards against subagent output
// reaching telemetry unredacted: the manager bounded it but never sanitized
// it before this fix.
func TestManager_PublishesRedactedOutput(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()
	events, unsubscribe := hub.Subscribe()
	defer unsubscribe()

	const secretOutput = `{"api_key": "sk-super-secret-value", "note": "done"}`
	manager := NewManager(runnerFunc(func(_ context.Context, _ Request, _ func(int)) (string, error) {
		return secretOutput, nil
	}), 1)
	manager.SetTelemetry(hub, "parent-telemetry")
	t.Cleanup(func() { _ = manager.Close() })

	spawned, err := manager.Spawn("reviewer", "", "leak secrets", 30)
	if err != nil {
		t.Fatal(err)
	}
	finished, err := manager.Wait(context.Background(), spawned.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The snapshot API itself still carries the raw output for in-process
	// consumers; only the telemetry copy is redacted.
	if !strings.Contains(finished.Output, "sk-super-secret-value") {
		t.Fatalf("snapshot output unexpectedly redacted: %q", finished.Output)
	}

	for {
		select {
		case event := <-events:
			if event.Type != telemetry.EventSubagentCompleted {
				continue
			}
			output, _ := event.Data["output"].(string)
			if strings.Contains(output, "sk-super-secret-value") {
				t.Fatalf("secret leaked into subagent telemetry: %s", output)
			}
			if !strings.Contains(output, "[REDACTED]") {
				t.Fatalf("expected redaction marker in telemetry output: %s", output)
			}
			return
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for completion event")
		}
	}
}

func TestManager_PublishesExecutionContractTelemetry(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()
	events, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	manager := NewManager(runnerFunc(func(_ context.Context, _ Request, _ func(int)) (string, error) {
		return "done", nil
	}), 1)
	manager.SetTelemetry(hub, "parent")
	t.Cleanup(func() { _ = manager.Close() })
	spawned, err := manager.SpawnWithOptions(SpawnOptions{
		Agent: "worker", ParentRunID: "parent-run", Task: "inspect", Model: "example/model", Tier: persona.TierExecute, Effort: "medium", StepCap: 7,
		AllowedTools: []string{"read_file", "find_files"}, WorkspaceClaims: []string{"pkg/subagent"}, Isolation: "worktree", OutputSchema: "buckley.artifact/v1", ApprovalPosture: "safe",
	})
	if err != nil {
		t.Fatalf("SpawnWithOptions: %v", err)
	}
	if _, err := manager.Wait(context.Background(), spawned.ID); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	for {
		select {
		case event := <-events:
			if event.Type != telemetry.EventSubagentCompleted {
				continue
			}
			for key, want := range map[string]any{"parent_run_id": "parent-run", "state": "completed", "model": "example/model", "tier": "execute", "effort": "medium", "step_cap": 7, "allowed_tool_count": 2, "isolation": "worktree", "output_schema": "buckley.artifact/v1", "approval_posture": "safe"} {
				if got := event.Data[key]; got != want {
					t.Fatalf("telemetry %s = %#v, want %#v: %+v", key, got, want, event.Data)
				}
			}
			claims, ok := event.Data["workspace_claims"].([]string)
			if !ok || len(claims) != 1 || claims[0] != "pkg/subagent" {
				t.Fatalf("workspace claims = %#v", event.Data["workspace_claims"])
			}
			return
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for subagent completion telemetry")
		}
	}
}

// TestManager_SpawnWithOptionsPersonaPinnedResolvesModel covers item 3's
// first scenario: a persona-pinned spawn resolves the pinned model, and
// threads it through to the Runner's Request and the returned Snapshot.
func TestManager_SpawnWithOptionsPersonaPinnedResolvesModel(t *testing.T) {
	registry := persona.NewRegistry()
	registry.Add(persona.Persona{Name: "tiller-worker", Model: "sonnet", Prompt: "You are tiller-worker."})

	var captured Request
	manager := NewManager(runnerFunc(func(_ context.Context, request Request, started func(int)) (string, error) {
		captured = request
		started(1)
		return "done", nil
	}), 2)
	manager.SetPersonaContext(registry, persona.Persona{Name: "root", Tier: persona.TierReason})
	t.Cleanup(func() { _ = manager.Close() })

	spawned, err := manager.SpawnWithOptions(SpawnOptions{
		Agent: "reviewer", Task: "inspect this", TimeoutSeconds: 30, Persona: "tiller-worker",
	})
	if err != nil {
		t.Fatalf("SpawnWithOptions: %v", err)
	}
	if spawned.Model != "sonnet" || spawned.Tier != persona.TierExecute || spawned.Persona != "tiller-worker" {
		t.Fatalf("snapshot = %+v, want model=sonnet tier=execute persona=tiller-worker", spawned)
	}
	if _, err := manager.Wait(context.Background(), spawned.ID); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if captured.Model != "sonnet" || captured.Tier != persona.TierExecute {
		t.Fatalf("request = %+v, want model=sonnet tier=execute", captured)
	}
	if captured.SystemPrompt != "You are tiller-worker." {
		t.Fatalf("request.SystemPrompt = %q, want persona prompt prepended as system context", captured.SystemPrompt)
	}
	if captured.Task != "inspect this" {
		t.Fatalf("request.Task = %q, want the task instruction unmodified", captured.Task)
	}
}

// TestManager_SpawnWithOptionsUnpinnedFallsBackUnderReasonParent covers item
// 3's second scenario: an unpinned subagent spawned under a reason-tier
// parent falls back to the default tier instead of inheriting reason.
func TestManager_SpawnWithOptionsUnpinnedFallsBackUnderReasonParent(t *testing.T) {
	registry := persona.NewRegistry()
	registry.Add(persona.Persona{Name: "general-purpose"}) // no explicit pin

	var captured Request
	manager := NewManager(runnerFunc(func(_ context.Context, request Request, started func(int)) (string, error) {
		captured = request
		started(1)
		return "done", nil
	}), 2)
	manager.SetPersonaContext(registry, persona.Persona{Name: "root", Model: "fable"}) // reason tier
	t.Cleanup(func() { _ = manager.Close() })

	spawned, err := manager.SpawnWithOptions(SpawnOptions{
		Task: "do something", Persona: "general-purpose",
	})
	if err != nil {
		t.Fatalf("SpawnWithOptions: %v", err)
	}
	if spawned.Tier != persona.DefaultTier {
		t.Fatalf("snapshot.Tier = %q, want DefaultTier %q", spawned.Tier, persona.DefaultTier)
	}
	if spawned.Model != "" {
		t.Fatalf("snapshot.Model = %q, want empty (unpinned persona resolves no model)", spawned.Model)
	}
	if _, err := manager.Wait(context.Background(), spawned.ID); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if captured.Tier != persona.DefaultTier {
		t.Fatalf("request.Tier = %q, want DefaultTier %q (never inherit parent's reason tier)", captured.Tier, persona.DefaultTier)
	}
}

// TestManager_SpawnWithOptionsEscalationErrorSurfaces covers item 3's third
// scenario: a persona pinning a tier higher than the manager's parent
// persona is denied with a typed *persona.EscalationError, not spawned.
func TestManager_SpawnWithOptionsEscalationErrorSurfaces(t *testing.T) {
	registry := persona.NewRegistry()
	registry.Add(persona.Persona{Name: "tiller-architect", Model: "fable"}) // reason tier

	manager := NewManager(runnerFunc(func(_ context.Context, _ Request, _ func(int)) (string, error) {
		t.Fatalf("runner should not be invoked when escalation is denied")
		return "", nil
	}), 2)
	manager.SetPersonaContext(registry, persona.Persona{Name: "tiller-worker", Model: "sonnet"}) // execute tier
	t.Cleanup(func() { _ = manager.Close() })

	spawned, err := manager.SpawnWithOptions(SpawnOptions{
		Task: "escalate please", Persona: "tiller-architect",
	})
	if err == nil {
		t.Fatalf("SpawnWithOptions: expected an escalation error, got snapshot %+v", spawned)
	}
	var escalationErr *persona.EscalationError
	if !errors.As(err, &escalationErr) {
		t.Fatalf("err = %v, want *persona.EscalationError", err)
	}
	if escalationErr.ParentTier != persona.TierExecute || escalationErr.ChildTier != persona.TierReason {
		t.Fatalf("escalationErr = %+v, want ParentTier=execute ChildTier=reason", escalationErr)
	}
	if len(manager.List()) != 0 {
		t.Fatalf("List() = %v, want no run recorded for a denied spawn", manager.List())
	}
}

// TestManager_SpawnWithOptionsUnknownPersonaIsAnError confirms a named
// persona that is not registered fails the spawn rather than silently
// falling back to a persona-free run.
func TestManager_SpawnWithOptionsUnknownPersonaIsAnError(t *testing.T) {
	manager := NewManager(runnerFunc(func(_ context.Context, _ Request, _ func(int)) (string, error) {
		t.Fatalf("runner should not be invoked for an unresolvable persona")
		return "", nil
	}), 2)
	manager.SetPersonaContext(persona.NewRegistry(), persona.Persona{Name: "root"})
	t.Cleanup(func() { _ = manager.Close() })

	if _, err := manager.SpawnWithOptions(SpawnOptions{Task: "do something", Persona: "missing"}); err == nil {
		t.Fatalf("SpawnWithOptions: expected an error for an unresolvable persona")
	}
}

// TestManager_SpawnLegacyBehaviorUnaffectedByPersonaContext confirms Spawn's
// original positional signature keeps working, resolving no persona, even
// when SetPersonaContext has been configured (the additive path only
// activates when the caller opts in via SpawnOptions.Persona).
func TestManager_SpawnLegacyBehaviorUnaffectedByPersonaContext(t *testing.T) {
	registry := persona.NewRegistry()
	registry.Add(persona.Persona{Name: "tiller-worker", Model: "sonnet"})

	var captured Request
	manager := NewManager(runnerFunc(func(_ context.Context, request Request, started func(int)) (string, error) {
		captured = request
		started(1)
		return "done", nil
	}), 2)
	manager.SetPersonaContext(registry, persona.Persona{Name: "root", Model: "fable"})
	t.Cleanup(func() { _ = manager.Close() })

	spawned, err := manager.Spawn("reviewer", "daily", "inspect this", 30)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if spawned.Persona != "" || spawned.Model != "" || spawned.Tier != "" {
		t.Fatalf("snapshot = %+v, want zero persona fields for a legacy Spawn call", spawned)
	}
	if _, err := manager.Wait(context.Background(), spawned.ID); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if captured.Persona != "" || captured.Model != "" || captured.SystemPrompt != "" {
		t.Fatalf("request = %+v, want zero persona fields for a legacy Spawn call", captured)
	}
}
