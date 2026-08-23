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

func TestMergeAgentRunsDurableTerminalTruthAndDeterministicTree(t *testing.T) {
	started := time.Date(2026, time.August, 20, 9, 0, 0, 0, time.UTC)
	durable := []AgentRun{
		{ID: "completed", Status: "completed", StartedAt: started, UpdatedAt: started.Add(5 * time.Second)},
		{ID: "blocked", Status: "blocked", StartedAt: started.Add(time.Second), UpdatedAt: started.Add(5 * time.Second)},
		{ID: "detached", Status: "resumable", StartedAt: started.Add(2 * time.Second), UpdatedAt: started.Add(10 * time.Second)},
		{ID: "parent", Status: "resumable", StartedAt: started.Add(3 * time.Second), UpdatedAt: started.Add(3 * time.Second)},
		{ID: "child-b", ParentID: "parent", Status: "completed", StartedAt: started.Add(4 * time.Second)},
		{ID: "child-a", ParentID: "parent", Status: "completed", StartedAt: started.Add(4 * time.Second)},
		{ID: "orphan", ParentID: "missing-parent", Status: "completed", StartedAt: started.Add(6 * time.Second)},
	}
	live := []AgentRun{
		{ID: "completed", Status: "failed", UpdatedAt: started.Add(20 * time.Second)},
		{ID: "blocked", Status: "completed", UpdatedAt: started.Add(20 * time.Second)},
		{ID: "detached", Status: "running", UpdatedAt: started.Add(9 * time.Second)},
		{ID: "parent", Status: "running", UpdatedAt: started.Add(20 * time.Second)},
		{ID: "child-a", ParentID: "stale-parent", Status: "failed", UpdatedAt: started.Add(20 * time.Second)},
	}

	merged := MergeAgentRuns(durable, live)
	if countProjectedAgentRuns(merged) != len(durable) {
		t.Fatalf("merged duplicate count = %d, want %d: %+v", countProjectedAgentRuns(merged), len(durable), merged)
	}
	if got := findProjectedAgentRun(merged, "completed"); got == nil || got.Status != "completed" {
		t.Fatalf("completed durable truth = %+v", got)
	}
	if got := findProjectedAgentRun(merged, "blocked"); got == nil || got.Status != "blocked" {
		t.Fatalf("blocked durable truth = %+v", got)
	}
	if got := findProjectedAgentRun(merged, "detached"); got == nil || got.Status != "resumable" {
		t.Fatalf("stale live overlay advanced detached run = %+v", got)
	}
	parent := findProjectedAgentRun(merged, "parent")
	if parent == nil || parent.Status != "running" || len(parent.Children) != 2 || parent.Children[0].ID != "child-a" || parent.Children[1].ID != "child-b" {
		t.Fatalf("deterministic parent projection = %+v", parent)
	}
	if got := findProjectedAgentRun(merged, "orphan"); got == nil || got.ParentID != "missing-parent" {
		t.Fatalf("missing-parent run disappeared: %+v", got)
	}
}

func findProjectedAgentRun(runs []AgentRun, id string) *AgentRun {
	for i := range runs {
		if runs[i].ID == id {
			return &runs[i]
		}
		if found := findProjectedAgentRun(runs[i].Children, id); found != nil {
			return found
		}
	}
	return nil
}

func countProjectedAgentRuns(runs []AgentRun) int {
	count := 0
	for _, run := range runs {
		count += 1 + countProjectedAgentRuns(run.Children)
	}
	return count
}
