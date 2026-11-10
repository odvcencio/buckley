package goalrunner

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"m31labs.dev/buckley/pkg/durability"
	"m31labs.dev/buckley/pkg/goalloop"
)

func TestResolver_RunnerCacheBoundsAndReloads(t *testing.T) {
	t.Parallel()
	builds := make(map[string]int)
	resolver := NewResolver(func(_ context.Context, runID string) (*Runner, error) {
		builds[runID]++
		return &Runner{runID: runID}, nil
	})
	ctx := context.Background()
	var first *Runner
	for i := 0; i < DefaultRunnerCacheSize; i++ {
		runner, err := resolver.runner(ctx, fmt.Sprintf("run-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = runner
		}
	}
	if runner, err := resolver.runner(ctx, "run-0"); err != nil || runner != first {
		t.Fatalf("cache hit = %p, %v", runner, err)
	}
	if _, err := resolver.runner(ctx, "extra"); err != nil {
		t.Fatal(err)
	}
	if len(resolver.cache) != DefaultRunnerCacheSize || resolver.order.Len() != DefaultRunnerCacheSize {
		t.Fatalf("cache grew beyond ceiling: map=%d list=%d", len(resolver.cache), resolver.order.Len())
	}
	if _, present := resolver.cache["run-1"]; present {
		t.Fatal("least recently used goal was retained")
	}
	if runner, err := resolver.runner(ctx, "run-0"); err != nil || runner != first || builds["run-0"] != 1 {
		t.Fatal("recently used goal was rebuilt")
	}
	if runner, err := resolver.runner(ctx, "run-1"); err != nil || runner.runID != "run-1" || builds["run-1"] != 2 {
		t.Fatal("evicted goal could not reload")
	}
}

func TestResolver_RunnerCacheConcurrentEviction(t *testing.T) {
	t.Parallel()
	resolver := NewResolver(func(_ context.Context, runID string) (*Runner, error) {
		return &Runner{runID: runID}, nil
	})
	var workers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			for i := 0; i < 128; i++ {
				runID := fmt.Sprintf("run-%d", (i+worker)%64)
				runner, err := resolver.runner(context.Background(), runID)
				if err != nil || runner.runID != runID {
					t.Errorf("concurrent reload for %s: runner=%+v err=%v", runID, runner, err)
					return
				}
			}
		}(worker)
	}
	workers.Wait()
	if len(resolver.cache) != DefaultRunnerCacheSize || resolver.order.Len() != DefaultRunnerCacheSize {
		t.Fatalf("concurrent eviction corrupted cache: map=%d list=%d", len(resolver.cache), resolver.order.Len())
	}
}

func TestResolver_FinalizeGoalReleasesCachedRunner(t *testing.T) {
	for _, tc := range []struct {
		name       string
		complete   bool
		incomplete bool
		failure    string
		invalid    bool
	}{
		{name: "completed", complete: true},
		{name: "failed", failure: "worker failed"},
		{name: "bounded_yield", incomplete: true},
		{name: "pending_report"},
		{name: "finalization_error", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			engine := &scriptedEngine{outcomes: []goalloop.TurnOutcome{{
				Rounds: 1, Completed: true, CompletedEvidenceID: "ev_done", Summary: "done",
			}}}
			runner, intake := newTestRunner(t, engine)
			ctx := context.Background()
			if tc.complete {
				taskID := intake.Tasks[0].TaskID
				seed, err := runner.ResumeSeed(ctx, intake.RunID, taskID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := runner.RunTurn(ctx, durability.TurnRequest{
					RunID: intake.RunID, TaskID: taskID, WorkspaceRoot: intake.Goal.WorkspaceRoot, Drive: seed.Drive,
				}); err != nil {
					t.Fatal(err)
				}
			}
			builds := 0
			resolver := NewResolver(func(ctx context.Context, runID string) (*Runner, error) {
				builds++
				goal, specs, err := runner.loop.LoadGoal(ctx, runID)
				if err != nil {
					return nil, err
				}
				return New(runner.loop, runID, runner.WorkspaceRoot(), goal, specs)
			})
			finalization := durability.GoalFinalization{
				RunID: intake.RunID, WorkspaceRoot: intake.Goal.WorkspaceRoot,
				WorkflowInstanceID: testGoalWorkflowInstanceID(intake.RunID, 0),
				Incomplete:         tc.incomplete, Failure: tc.failure,
			}
			if tc.invalid {
				finalization.WorkspaceRoot = t.TempDir()
			}
			err := resolver.FinalizeGoal(ctx, finalization)
			if tc.invalid {
				if err == nil || len(resolver.cache) != 1 {
					t.Fatalf("failed finalization lost runner: cache=%d err=%v", len(resolver.cache), err)
				}
				return
			}
			if err != nil || len(resolver.cache) != 0 || resolver.order.Len() != 0 {
				t.Fatalf("finalization retained runner: cache=%d err=%v", len(resolver.cache), err)
			}
			next, err := resolver.NextTask(ctx, durability.NextTaskRequest{RunID: intake.RunID})
			if err != nil || builds != 2 || next.Done != tc.complete {
				t.Fatalf("reload after finalization: next=%+v builds=%d err=%v", next, builds, err)
			}
			if err := resolver.FinalizeGoal(ctx, finalization); err != nil {
				t.Fatalf("replayed finalization: %v", err)
			}
		})
	}
}
