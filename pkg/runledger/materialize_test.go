package runledger

import (
	"context"
	"testing"
	"time"
)

func TestMaterializeRun_StatusAndTasks(t *testing.T) {
	base := time.Now().UTC()
	events := []Event{
		{RunID: "run-1", Sequence: 1, Type: EventRunStarted, Timestamp: base},
		{RunID: "run-1", Sequence: 2, Type: EventTaskCreated, TaskID: "task-1", Timestamp: base.Add(time.Minute)},
		{RunID: "run-1", Sequence: 3, Type: EventToolStarted, TaskID: "task-1", Timestamp: base.Add(2 * time.Minute)},
		{RunID: "run-1", Sequence: 4, Type: EventTaskCompleted, TaskID: "task-1", Timestamp: base.Add(3 * time.Minute)},
		{RunID: "run-1", Sequence: 5, Type: EventRunCompleted, Timestamp: base.Add(4 * time.Minute)},
	}

	state, err := MaterializeRun("run-1", events)
	if err != nil {
		t.Fatalf("MaterializeRun() error = %v", err)
	}
	if state.Status != "completed" {
		t.Fatalf("Status = %q, want completed", state.Status)
	}
	if len(state.SequenceGaps) != 0 {
		t.Fatalf("SequenceGaps = %v, want none", state.SequenceGaps)
	}
	if state.EventCount != 5 {
		t.Fatalf("EventCount = %d, want 5", state.EventCount)
	}
	task, ok := state.Tasks["task-1"]
	if !ok {
		t.Fatalf("expected task-1 to be materialized")
	}
	if task.Status != "completed" {
		t.Fatalf("task status = %q, want completed", task.Status)
	}
	if len(task.Events) != 3 {
		t.Fatalf("task events = %d, want 3 (created, tool.started, completed)", len(task.Events))
	}
}

func TestMaterializeRun_ToleratesUnknownEventTypes(t *testing.T) {
	events := []Event{
		{RunID: "run-1", Sequence: 1, Type: EventRunStarted},
		{RunID: "run-1", Sequence: 2, Type: "future.event_type_from_a_newer_schema", TaskID: "task-1"},
		{RunID: "run-1", Sequence: 3, Type: EventRunCompleted},
	}
	state, err := MaterializeRun("run-1", events)
	if err != nil {
		t.Fatalf("MaterializeRun() error = %v, want nil (unknown event types must be tolerated)", err)
	}
	if state.Status != "completed" {
		t.Fatalf("Status = %q, want completed", state.Status)
	}
	if state.EventCount != 3 {
		t.Fatalf("EventCount = %d, want 3", state.EventCount)
	}
	if task := state.Tasks["task-1"]; task == nil || len(task.Events) != 1 {
		t.Fatalf("expected the unknown event to still be preserved on its task: %+v", state.Tasks["task-1"])
	}
}

func TestMaterializeRun_DetectsSequenceGaps(t *testing.T) {
	events := []Event{
		{RunID: "run-1", Sequence: 1, Type: EventRunStarted},
		{RunID: "run-1", Sequence: 4, Type: EventRunCompleted},
	}
	state, err := MaterializeRun("run-1", events)
	if err != nil {
		t.Fatalf("MaterializeRun() error = %v", err)
	}
	if len(state.SequenceGaps) != 2 {
		t.Fatalf("SequenceGaps = %v, want [2, 3]", state.SequenceGaps)
	}
	if state.SequenceGaps[0] != 2 || state.SequenceGaps[1] != 3 {
		t.Fatalf("SequenceGaps = %v, want [2, 3]", state.SequenceGaps)
	}
}

func TestMaterializeRun_DetectsGapAtStart(t *testing.T) {
	events := []Event{
		{RunID: "run-1", Sequence: 3, Type: EventRunStarted},
	}
	state, err := MaterializeRun("run-1", events)
	if err != nil {
		t.Fatalf("MaterializeRun() error = %v", err)
	}
	if len(state.SequenceGaps) != 2 || state.SequenceGaps[0] != 1 || state.SequenceGaps[1] != 2 {
		t.Fatalf("SequenceGaps = %v, want [1, 2]", state.SequenceGaps)
	}
}

func TestMaterializeRun_FailsClosedOnOutOfOrderEvents(t *testing.T) {
	events := []Event{
		{RunID: "run-1", Sequence: 2, Type: EventTaskCreated},
		{RunID: "run-1", Sequence: 1, Type: EventRunStarted},
	}
	if _, err := MaterializeRun("run-1", events); err == nil {
		t.Fatalf("expected an error for out-of-order events")
	}
}

func TestMaterializeRun_RejectsForeignRunEvents(t *testing.T) {
	events := []Event{
		{RunID: "run-1", Sequence: 1, Type: EventRunStarted},
		{RunID: "run-2", Sequence: 2, Type: EventRunStarted},
	}
	if _, err := MaterializeRun("run-1", events); err == nil {
		t.Fatalf("expected an error for an event belonging to a different run")
	}
}

func TestMaterializeGoalTree_ReconstructsHierarchy(t *testing.T) {
	base := time.Now().UTC()
	root := AgentRun{RunID: "run-root", SessionID: "sess-1", Status: "running", StartedAt: base}
	childA := AgentRun{RunID: "run-child-a", SessionID: "sess-1", ParentRunID: "run-root", StartedAt: base.Add(time.Minute)}
	childB := AgentRun{RunID: "run-child-b", SessionID: "sess-1", ParentRunID: "run-root", StartedAt: base.Add(2 * time.Minute)}
	grandchild := AgentRun{RunID: "run-grandchild", SessionID: "sess-1", ParentRunID: "run-child-a", StartedAt: base.Add(3 * time.Minute)}

	runs := []AgentRun{grandchild, childB, root, childA} // deliberately out of hierarchical order
	events := map[string][]Event{
		"run-root":       {{RunID: "run-root", Sequence: 1, Type: EventRunStarted}},
		"run-child-a":    {{RunID: "run-child-a", Sequence: 1, Type: EventRunStarted, TaskID: "investigate"}},
		"run-child-b":    {{RunID: "run-child-b", Sequence: 1, Type: EventRunStarted, TaskID: "review"}},
		"run-grandchild": {{RunID: "run-grandchild", Sequence: 1, Type: EventRunStarted}},
	}

	tree, err := MaterializeGoalTree("run-root", runs, events)
	if err != nil {
		t.Fatalf("MaterializeGoalTree() error = %v", err)
	}
	if tree.Run.RunID != "run-root" {
		t.Fatalf("tree root = %q, want run-root", tree.Run.RunID)
	}
	if len(tree.Children) != 2 {
		t.Fatalf("root has %d children, want 2", len(tree.Children))
	}
	if tree.Children[0].Run.RunID != "run-child-a" || tree.Children[1].Run.RunID != "run-child-b" {
		t.Fatalf("children not in StartedAt order: %q, %q", tree.Children[0].Run.RunID, tree.Children[1].Run.RunID)
	}
	if len(tree.Children[0].Children) != 1 || tree.Children[0].Children[0].Run.RunID != "run-grandchild" {
		t.Fatalf("expected run-grandchild nested under run-child-a: %+v", tree.Children[0].Children)
	}
	if tree.Children[0].State.Tasks["investigate"] == nil {
		t.Fatalf("expected child-a's task state to be materialized")
	}
}

func TestMaterializeGoalTree_RootNotFound(t *testing.T) {
	if _, err := MaterializeGoalTree("run-missing", nil, nil); err == nil {
		t.Fatalf("expected an error when the root run is not among the supplied runs")
	}
}

func TestLoadGoalTree_FromStore(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	root, err := store.StartRun(ctx, AgentRun{SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("StartRun(root) error = %v", err)
	}
	child, err := store.StartRun(ctx, AgentRun{SessionID: "sess-1", ParentRunID: root.RunID})
	if err != nil {
		t.Fatalf("StartRun(child) error = %v", err)
	}
	if _, err := store.Append(ctx, Event{RunID: root.RunID, Type: EventRunStarted}); err != nil {
		t.Fatalf("Append(root) error = %v", err)
	}
	if _, err := store.Append(ctx, Event{RunID: child.RunID, Type: EventRunStarted, TaskID: "investigate"}); err != nil {
		t.Fatalf("Append(child) error = %v", err)
	}
	if _, err := store.Append(ctx, Event{RunID: child.RunID, Type: EventRunCompleted, TaskID: "investigate"}); err != nil {
		t.Fatalf("Append(child completed) error = %v", err)
	}

	tree, err := LoadGoalTree(ctx, store, root.RunID)
	if err != nil {
		t.Fatalf("LoadGoalTree() error = %v", err)
	}
	if len(tree.Children) != 1 || tree.Children[0].Run.RunID != child.RunID {
		t.Fatalf("LoadGoalTree() children = %+v, want [%s]", tree.Children, child.RunID)
	}
	if tree.Children[0].State.Status != "completed" {
		t.Fatalf("child materialized status = %q, want completed", tree.Children[0].State.Status)
	}
}
