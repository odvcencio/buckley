package viewmodel

import (
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/telemetry"
)

func TestRuntimeStateTracker_SubagentLifecycleBuildsStableTree(t *testing.T) {
	tracker := NewRuntimeStateTracker(nil)
	started := time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)

	tracker.handleEvent(telemetry.Event{
		Type:      telemetry.EventSubagentSpawned,
		Timestamp: started,
		SessionID: "session-1",
		TaskID:    "agent-parent",
		Data: map[string]any{
			"agent_id":          "agent-parent",
			"parent_session_id": "session-1",
			"agent":             "reviewer",
			"persona":           "review",
			"model":             "example/frontier",
			"state":             "running",
			"task":              "review the repository",
		},
	})
	// SessionID is intentionally empty: manager telemetry also carries the
	// canonical parent session in the payload, which must route the child.
	tracker.handleEvent(telemetry.Event{
		Type:      telemetry.EventSubagentSpawned,
		Timestamp: started.Add(time.Second),
		TaskID:    "agent-child",
		Data: map[string]any{
			"agent_id":          "agent-child",
			"parent_session_id": "session-1",
			"parent_run_id":     "agent-parent",
			"agent":             "researcher",
			"persona":           "research",
			"model":             "example/cheap",
			"task":              "trace call sites",
		},
	})
	tracker.handleEvent(telemetry.Event{
		Type:      telemetry.EventSubagentCompleted,
		Timestamp: started.Add(2 * time.Second),
		SessionID: "session-1",
		TaskID:    "agent-child",
	})
	tracker.handleEvent(telemetry.Event{
		Type:      telemetry.EventSubagentState,
		Timestamp: started.Add(3 * time.Second),
		SessionID: "session-1",
		TaskID:    "agent-child",
		Data:      map[string]any{"state": "running"},
	})

	runs := tracker.GetAgentRuns("session-1")
	if len(runs) != 1 {
		t.Fatalf("root agent runs = %+v, want one", runs)
	}
	parent := runs[0]
	if parent.ID != "agent-parent" || parent.ParentID != "session-1" || parent.ParentSessionID != "session-1" {
		t.Fatalf("parent projection = %+v", parent)
	}
	if parent.Status != "running" || parent.Agent != "reviewer" || parent.Persona != "review" || parent.Model != "example/frontier" || parent.Task != "review the repository" {
		t.Fatalf("parent metadata = %+v", parent)
	}
	if len(parent.Children) != 1 {
		t.Fatalf("parent children = %+v, want one", parent.Children)
	}
	child := parent.Children[0]
	if child.ID != "agent-child" || child.ParentID != "agent-parent" || child.Status != "completed" {
		t.Fatalf("child projection = %+v", child)
	}
	if child.Persona != "research" || child.Model != "example/cheap" || child.Task != "trace call sites" {
		t.Fatalf("terminal event discarded child metadata: %+v", child)
	}

	// Callers receive a detached tree and cannot mutate tracker state.
	runs[0].Children[0].Task = "mutated"
	again := tracker.GetAgentRuns("session-1")
	if got := again[0].Children[0].Task; got != "trace call sites" {
		t.Fatalf("GetAgentRuns returned aliased state: %q", got)
	}
}

func TestRuntimeStateTracker_SubagentProjectionIsDeterministicAndSessionScoped(t *testing.T) {
	tracker := NewRuntimeStateTracker(nil)
	started := time.Date(2026, time.August, 12, 13, 0, 0, 0, time.UTC)

	tracker.handleEvent(telemetry.Event{Type: telemetry.EventSubagentSpawned, Timestamp: started.Add(time.Second), SessionID: "session-1", TaskID: "agent-b", Data: map[string]any{"state": "running"}})
	tracker.handleEvent(telemetry.Event{Type: telemetry.EventSubagentFailed, Timestamp: started, SessionID: "session-1", TaskID: "agent-a", Data: map[string]any{"state": "running", "task": "failed task"}})
	tracker.handleEvent(telemetry.Event{Type: telemetry.EventSubagentCancelled, Timestamp: started, SessionID: "session-2", TaskID: "agent-other", Data: map[string]any{"state": "canceled"}})

	runs := tracker.GetAgentRuns("session-1")
	if len(runs) != 2 || runs[0].ID != "agent-a" || runs[1].ID != "agent-b" {
		t.Fatalf("agent run order = %+v, want agent-a then agent-b", runs)
	}
	if runs[0].Status != "failed" {
		t.Fatalf("terminal event status = %q, want failed", runs[0].Status)
	}
	other := tracker.GetAgentRuns("session-2")
	if len(other) != 1 || other[0].ID != "agent-other" || other[0].Status != "cancelled" {
		t.Fatalf("session-2 projection = %+v", other)
	}
}

func TestRuntimeStateTracker_SubagentProjectionRetainsCyclicParents(t *testing.T) {
	tracker := NewRuntimeStateTracker(nil)
	tracker.handleEvent(telemetry.Event{Type: telemetry.EventSubagentSpawned, SessionID: "session-1", TaskID: "agent-a", Data: map[string]any{"parent_run_id": "agent-b"}})
	tracker.handleEvent(telemetry.Event{Type: telemetry.EventSubagentSpawned, SessionID: "session-1", TaskID: "agent-b", Data: map[string]any{"parent_run_id": "agent-a"}})

	runs := tracker.GetAgentRuns("session-1")
	if countProjectedAgentRuns(runs) != 2 {
		t.Fatalf("cyclic parent projection lost an agent: %+v", runs)
	}
}

func countProjectedAgentRuns(runs []AgentRun) int {
	count := 0
	for _, run := range runs {
		count += 1 + countProjectedAgentRuns(run.Children)
	}
	return count
}
