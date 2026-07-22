package subagent

import (
	"context"
	"errors"
	"testing"
	"time"

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
