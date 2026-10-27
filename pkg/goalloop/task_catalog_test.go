package goalloop

import (
	"context"
	"reflect"
	"testing"

	"m31labs.dev/buckley/pkg/runledger"
)

func TestFirstCreatedByTask_SparseHistory(t *testing.T) {
	first := runledger.Event{RunID: "run", Sequence: 1, Type: runledger.EventTaskCreated, TaskID: "task", Payload: map[string]any{"title": "first"}}
	got, err := firstCreatedByTask("run", []runledger.Event{
		first,
		{RunID: "run", Sequence: 1000000000, Type: runledger.EventTaskCreated, TaskID: "task", Payload: map[string]any{"title": "replacement"}},
		{RunID: "run", Sequence: 1000000001, Type: runledger.EventTaskCreated},
		{RunID: "run", Sequence: 1000000002, Type: runledger.EventDurableApprovalWaiting, TaskID: "approval-only"},
	})
	if err != nil || len(got) != 1 || !reflect.DeepEqual(got["task"], first) {
		t.Fatalf("first task creation was not preserved: %+v, %v", got, err)
	}
	for _, tc := range []struct {
		name   string
		events []runledger.Event
	}{
		{name: "foreign_run", events: []runledger.Event{{RunID: "other", Sequence: 1, Type: runledger.EventTaskCreated}}},
		{name: "duplicate_sequence", events: []runledger.Event{first, first}},
		{name: "decreasing_sequence", events: []runledger.Event{first, {RunID: "run", Sequence: 0}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := firstCreatedByTask("run", tc.events); err == nil {
				t.Fatal("corrupt selected event stream was accepted")
			}
		})
	}
}

type unfilteredWorklistLedger struct{ runledger.Store }

func (s unfilteredWorklistLedger) ListEvents(ctx context.Context, q runledger.EventQuery) ([]runledger.Event, error) {
	q.Types = nil
	return s.Store.ListEvents(ctx, q)
}

func TestLoop_WorklistProjectionMatchesFullHistory(t *testing.T) {
	loop, ledger, runID := worklistHistory(t, 100)
	ctx := context.Background()
	queue, err := loop.BuildQueue(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	// A later creation event must not retitle or reprioritize an existing task.
	if _, err := ledger.Append(ctx, runledger.Event{RunID: runID, TaskID: queue[0].TaskID,
		Type: runledger.EventTaskCreated, Payload: map[string]any{"kind": "task", "title": "replacement", "priority": -1}}); err != nil {
		t.Fatal(err)
	}
	projectedQueue, err := loop.BuildQueue(ctx, runID)
	if err != nil || !reflect.DeepEqual(projectedQueue, queue) {
		t.Fatalf("duplicate creation changed queue: %+v, %v", projectedQueue, err)
	}
	projectedReport, err := loop.Report(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	full := *loop
	full.ledger = unfilteredWorklistLedger{ledger}
	fullQueue, err := full.BuildQueue(ctx, runID)
	if err != nil || !reflect.DeepEqual(projectedQueue, fullQueue) {
		t.Fatalf("queue differs with full history: %+v, %v", fullQueue, err)
	}
	fullReport, err := full.Report(ctx, runID)
	if err != nil || !reflect.DeepEqual(projectedReport, fullReport) || RenderReport(projectedReport) != RenderReport(fullReport) {
		t.Fatalf("report differs with full history: %+v, %v", fullReport, err)
	}
	goal, specs, err := loop.LoadGoal(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	fullGoal, fullSpecs, err := full.LoadGoal(ctx, runID)
	if err != nil || !reflect.DeepEqual(goal, fullGoal) || !reflect.DeepEqual(specs, fullSpecs) {
		t.Fatalf("restored goal or task specifications differ with full history: %+v, %v", fullGoal, err)
	}
}
