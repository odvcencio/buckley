package subagent

import (
	"context"
	"errors"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/agentcoord"
)

func TestManager_CompletionReleasesContextAndPreservesResult(t *testing.T) {
	for _, test := range []struct {
		name    string
		timeout int
		err     error
		state   State
	}{
		{name: "completed", state: StateCompleted},
		{name: "completed with deadline", timeout: 3600, state: StateCompleted},
		{name: "failed with deadline", timeout: 3600, err: errors.New("worker failed"), state: StateFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			var workerCtx context.Context
			manager := NewManager(runnerFunc(func(ctx context.Context, _ Request, _ func(int)) (string, error) {
				workerCtx = ctx
				return "retained worker evidence", test.err
			}), 1)
			t.Cleanup(func() { _ = manager.Close() })
			manager.SetLifecycleObserver(func(snapshot Snapshot) {
				if snapshotTerminal(snapshot.State) && workerCtx.Err() != nil {
					t.Error("execution context cancelled before terminal evidence was observed")
				}
			})
			run, err := manager.SpawnWithOptions(SpawnOptions{Task: "finish and release execution resources", TimeoutSeconds: test.timeout})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			finished, err := manager.Wait(ctx, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !errors.Is(workerCtx.Err(), context.Canceled) {
				t.Errorf("finished worker context = %v, want cancellation", workerCtx.Err())
			}
			if finished.State != test.state || finished.Output != "retained worker evidence" {
				t.Fatalf("cleanup changed the result: %+v", finished)
			}
			status, ok := manager.Status(run.ID)
			if !ok || status.State != test.state || status.Output != finished.Output {
				t.Fatalf("result unavailable after cleanup: %+v, %v", status, ok)
			}
			lateCancel, err := manager.Cancel(run.ID)
			if err != nil || lateCancel.State != test.state || lateCancel.Output != finished.Output {
				t.Fatalf("late cancellation changed the result: %+v, %v", lateCancel, err)
			}
		})
	}
}

func TestManager_CompletionUnblocksPendingDelivery(t *testing.T) {
	received, finish := make(chan struct{}), make(chan struct{})
	manager := NewManager(interactiveRunnerFunc(func(ctx context.Context, _ Request, _ func(int), commands <-chan CommandDelivery) (string, error) {
		select {
		case <-commands:
			close(received)
		case <-ctx.Done():
			return "", ctx.Err()
		}
		select {
		case <-finish:
			return "finished without acknowledging the message", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	run, err := manager.SpawnWithOptions(SpawnOptions{Task: "finish during live delivery", TimeoutSeconds: 3600})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	delivered := make(chan error, 1)
	message := agentcoord.Message{RunID: run.ID, To: run.ID, From: "parent", Content: "continue"}
	go func() { delivered <- manager.Deliver(ctx, run.ID, message) }()
	select {
	case <-received:
	case <-ctx.Done():
		t.Fatal("worker did not receive the command")
	}
	close(finish)
	finished, err := manager.Wait(ctx, run.ID)
	if err != nil || finished.State != StateCompleted {
		t.Fatalf("completion = %+v, %v", finished, err)
	}
	select {
	case err := <-delivered:
		if !errors.Is(err, ErrLiveDeliveryUnavailable) {
			t.Fatalf("pending delivery = %v", err)
		}
	case <-ctx.Done():
		t.Fatal("completion left delivery blocked")
	}
	if err := manager.Deliver(ctx, run.ID, message); !errors.Is(err, ErrLiveDeliveryUnavailable) {
		t.Fatalf("delivery after completion = %v", err)
	}
}
