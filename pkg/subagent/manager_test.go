package subagent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/persona"
	"m31labs.dev/buckley/pkg/telemetry"
)

type runnerFunc func(context.Context, Request, func(int)) (string, error)

func (f runnerFunc) Run(ctx context.Context, request Request, started func(int)) (string, error) {
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
