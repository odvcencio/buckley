package runledger

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// TaskState is the materialized view of one task's lifecycle, folded from
// task.* events within a single run's event log (section 14.5: replay MUST
// "rebuild materialized run/task state deterministically").
type TaskState struct {
	TaskID    string
	Status    string
	UpdatedAt time.Time
	Events    []Event
}

// RunState is the materialized view of a single run, folded from its
// ordered event log. Materializing never executes tools and tolerates
// unknown event types (section 14.5's other two hard replay guarantees):
// an event whose Type is not one of the known run/task lifecycle types
// simply does not change Status, but is still preserved on its task's
// Events slice.
type RunState struct {
	RunID        string
	Status       string
	Tasks        map[string]*TaskState
	SequenceGaps []int64
	EventCount   int
}

// MaterializeRun deterministically rebuilds a RunState from events, which
// MUST already be ordered by Sequence within the run (the order
// Store.ListEvents guarantees). Rather than silently tolerate a corrupt
// ordering, MaterializeRun fails closed and returns an error while leaving
// the caller free to keep the raw events it already has (section 14.5:
// "fail closed for corrupt checkpoint state but preserve raw events").
func MaterializeRun(runID string, events []Event) (RunState, error) {
	state := RunState{RunID: runID, Tasks: map[string]*TaskState{}}

	var lastSeq int64
	for i, ev := range events {
		if ev.RunID != "" && ev.RunID != runID {
			return state, fmt.Errorf("runledger: event %s belongs to run %s, not %s", ev.ID, ev.RunID, runID)
		}

		expectedMin := lastSeq + 1
		if i == 0 {
			expectedMin = 1
		} else if ev.Sequence <= lastSeq {
			return state, fmt.Errorf("runledger: events out of order at index %d: sequence %d did not increase past %d", i, ev.Sequence, lastSeq)
		}
		for gap := expectedMin; gap < ev.Sequence; gap++ {
			state.SequenceGaps = append(state.SequenceGaps, gap)
		}
		lastSeq = ev.Sequence

		if status, ok := runStatusEvents[ev.Type]; ok {
			state.Status = status
		}

		if ev.TaskID != "" {
			task, ok := state.Tasks[ev.TaskID]
			if !ok {
				task = &TaskState{TaskID: ev.TaskID}
				state.Tasks[ev.TaskID] = task
			}
			if status, ok := taskStatusEvents[ev.Type]; ok {
				task.Status = status
			}
			task.UpdatedAt = ev.Timestamp
			task.Events = append(task.Events, ev)
		}

		state.EventCount++
	}

	return state, nil
}

// RunNode is one node of the run tree: a run, its materialized state, and
// its children reached by following AgentRun.ParentRunID (section 19.1's
// run hierarchy). The root of this tree is the closest concept this layer
// has to a "goal": the top-level run a user's request started, decomposed
// into child runs for delegated subtasks.
type RunNode struct {
	Run      AgentRun
	State    RunState
	Children []*RunNode
}

// MaterializeGoalTree builds the run tree rooted at rootRunID purely from
// already-fetched runs and events, with no I/O and no tool execution, so it
// can be unit tested and reused by any query or export path.
func MaterializeGoalTree(rootRunID string, runs []AgentRun, eventsByRun map[string][]Event) (*RunNode, error) {
	byID := make(map[string]AgentRun, len(runs))
	byParent := make(map[string][]AgentRun, len(runs))
	for _, r := range runs {
		byID[r.RunID] = r
		byParent[r.ParentRunID] = append(byParent[r.ParentRunID], r)
	}

	root, ok := byID[rootRunID]
	if !ok {
		return nil, fmt.Errorf("runledger: root run %s not found among supplied runs", rootRunID)
	}
	return buildRunNode(root, byParent, eventsByRun)
}

func buildRunNode(run AgentRun, byParent map[string][]AgentRun, eventsByRun map[string][]Event) (*RunNode, error) {
	state, err := MaterializeRun(run.RunID, eventsByRun[run.RunID])
	if err != nil {
		return nil, err
	}
	node := &RunNode{Run: run, State: state}

	children := append([]AgentRun(nil), byParent[run.RunID]...)
	sort.Slice(children, func(i, j int) bool { return children[i].StartedAt.Before(children[j].StartedAt) })

	for _, child := range children {
		childNode, err := buildRunNode(child, byParent, eventsByRun)
		if err != nil {
			return nil, err
		}
		node.Children = append(node.Children, childNode)
	}
	return node, nil
}

// LoadGoalTree fetches rootRunID and every descendant run, plus each run's
// event log, through store, then materializes the full tree. It is a thin,
// read-only composition of Store.GetRun / ListRuns / ListEvents; it never
// executes tools.
func LoadGoalTree(ctx context.Context, store Store, rootRunID string) (*RunNode, error) {
	run, err := store.GetRun(ctx, rootRunID)
	if err != nil {
		return nil, err
	}
	events, err := store.ListEvents(ctx, EventQuery{RunID: rootRunID})
	if err != nil {
		return nil, err
	}
	state, err := MaterializeRun(rootRunID, events)
	if err != nil {
		return nil, err
	}
	node := &RunNode{Run: run, State: state}

	children, err := store.ListRuns(ctx, RunQuery{ParentRunID: rootRunID})
	if err != nil {
		return nil, err
	}
	for _, child := range children {
		childNode, err := LoadGoalTree(ctx, store, child.RunID)
		if err != nil {
			return nil, err
		}
		node.Children = append(node.Children, childNode)
	}
	return node, nil
}
