package machine

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/machine/locks"
	"github.com/odvcencio/buckley/pkg/telemetry"
)

// --- Mock implementations ---

type mockModelCaller struct {
	mu        sync.Mutex
	calls     int
	responses []Event // events to return in sequence
}

func (m *mockModelCaller) Call(_ context.Context, action CallModel) (Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := m.calls
	m.calls++
	if idx < len(m.responses) {
		return m.responses[idx], nil
	}
	return ModelCompleted{Content: "default response", FinishReason: "end_turn"}, nil
}

type mockToolExecutor struct {
	mu       sync.Mutex
	calls    int
	results  []ToolsCompleted
}

func (m *mockToolExecutor) Execute(_ context.Context, calls []ToolCallRequest) ToolsCompleted {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := m.calls
	m.calls++
	if idx < len(m.results) {
		return m.results[idx]
	}
	var results []ToolCallResult
	for _, c := range calls {
		results = append(results, ToolCallResult{ID: c.ID, Name: c.Name, Success: true})
	}
	return ToolsCompleted{Results: results}
}

type mockCompactor struct{}

func (m *mockCompactor) Compact(_ context.Context, action Compact) (CompactionCompleted, error) {
	return CompactionCompleted{TokensSaved: 10000}, nil
}

// --- Tests ---

func TestRuntime_ClassicConversation(t *testing.T) {
	hub := telemetry.NewHubWithConfig(&telemetry.Config{
		EventQueueSize: 1000, BatchSize: 1, FlushInterval: 100 * time.Millisecond,
		RateLimit: 10000, SubscriberChannelSize: 64,
	})
	defer hub.Close()

	model := &mockModelCaller{
		responses: []Event{
			ModelCompleted{Content: "Hello!", FinishReason: "end_turn"},
		},
	}
	tools := &mockToolExecutor{}
	lockMgr := locks.NewManager(hub)

	rt := NewRuntime(RuntimeConfig{
		Hub:          hub,
		ModelClient:  model,
		ToolExecutor: tools,
		LockManager:  lockMgr,
		Compactor:    &mockCompactor{},
	})

	result, err := rt.Run(context.Background(), "agent-1", Classic, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "Hello!" {
		t.Errorf("content = %q, want %q", result.Content, "Hello!")
	}
	if result.FinalState != Done {
		t.Errorf("final state = %s, want done", result.FinalState)
	}
}

func TestRuntime_ClassicWithToolUse(t *testing.T) {
	hub := telemetry.NewHubWithConfig(&telemetry.Config{
		EventQueueSize: 1000, BatchSize: 1, FlushInterval: 100 * time.Millisecond,
		RateLimit: 10000, SubscriberChannelSize: 64,
	})
	defer hub.Close()

	model := &mockModelCaller{
		responses: []Event{
			ModelCompleted{
				FinishReason: "tool_use",
				ToolCalls: []ToolCallRequest{
					{ID: "tc1", Name: "edit_file", Paths: []string{"foo.go"}, Mode: LockWrite},
				},
			},
			ModelCompleted{Content: "Done editing!", FinishReason: "end_turn"},
		},
	}
	tools := &mockToolExecutor{
		results: []ToolsCompleted{
			{Results: []ToolCallResult{{ID: "tc1", Name: "edit_file", Success: true, Result: "edited"}}},
		},
	}
	lockMgr := locks.NewManager(hub)

	rt := NewRuntime(RuntimeConfig{
		Hub:          hub,
		ModelClient:  model,
		ToolExecutor: tools,
		LockManager:  lockMgr,
		Compactor:    &mockCompactor{},
	})

	result, err := rt.Run(context.Background(), "agent-1", Classic, "edit foo.go")
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "Done editing!" {
		t.Errorf("content = %q, want %q", result.Content, "Done editing!")
	}
	if model.calls != 2 {
		t.Errorf("model calls = %d, want 2", model.calls)
	}
	if tools.calls != 1 {
		t.Errorf("tool calls = %d, want 1", tools.calls)
	}
}

func TestRuntime_ClassicContextCancellation(t *testing.T) {
	hub := telemetry.NewHubWithConfig(&telemetry.Config{
		EventQueueSize: 1000, BatchSize: 1, FlushInterval: 100 * time.Millisecond,
		RateLimit: 10000, SubscriberChannelSize: 64,
	})
	defer hub.Close()

	model := &mockModelCaller{
		responses: []Event{
			// Simulate slow model call by never returning tool_use
			ModelCompleted{
				FinishReason: "tool_use",
				ToolCalls:    []ToolCallRequest{{ID: "tc1", Name: "slow_tool"}},
			},
		},
	}
	// Tool executor blocks forever
	slowTools := &blockingToolExecutor{}
	lockMgr := locks.NewManager(hub)

	rt := NewRuntime(RuntimeConfig{
		Hub:          hub,
		ModelClient:  model,
		ToolExecutor: slowTools,
		LockManager:  lockMgr,
		Compactor:    &mockCompactor{},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	result, err := rt.Run(ctx, "agent-1", Classic, "slow operation")
	if err == nil {
		t.Fatal("expected error from context cancellation")
	}
	if result != nil && result.FinalState == Done {
		t.Error("should not complete successfully on cancellation")
	}
}

type blockingToolExecutor struct{}

func (b *blockingToolExecutor) Execute(ctx context.Context, calls []ToolCallRequest) ToolsCompleted {
	<-ctx.Done()
	return ToolsCompleted{}
}

func TestRuntime_MaxIterations(t *testing.T) {
	hub := telemetry.NewHubWithConfig(&telemetry.Config{
		EventQueueSize: 1000, BatchSize: 1, FlushInterval: 100 * time.Millisecond,
		RateLimit: 10000, SubscriberChannelSize: 64,
	})
	defer hub.Close()

	// Model always returns tool_use — infinite loop
	model := &mockModelCaller{
		responses: []Event{}, // will use default tool_use
	}
	// Override to always return tool_use
	infiniteModel := &infiniteToolModel{}
	tools := &mockToolExecutor{}
	lockMgr := locks.NewManager(hub)

	rt := NewRuntime(RuntimeConfig{
		Hub:           hub,
		ModelClient:   infiniteModel,
		ToolExecutor:  tools,
		LockManager:   lockMgr,
		Compactor:     &mockCompactor{},
		MaxIterations: 5,
	})

	_ = model // unused, using infiniteModel
	result, err := rt.Run(context.Background(), "agent-1", Classic, "loop forever")
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalState != Error {
		t.Errorf("final state = %s, want error (max iterations)", result.FinalState)
	}
}

type infiniteToolModel struct {
	mu    sync.Mutex
	calls int
}

func (m *infiniteToolModel) Call(_ context.Context, _ CallModel) (Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return ModelCompleted{
		FinishReason: "tool_use",
		ToolCalls:    []ToolCallRequest{{ID: fmt.Sprintf("tc%d", m.calls), Name: "think"}},
	}, nil
}

func TestRuntime_Steering(t *testing.T) {
	hub := telemetry.NewHubWithConfig(&telemetry.Config{
		EventQueueSize: 1000, BatchSize: 1, FlushInterval: 100 * time.Millisecond,
		RateLimit: 10000, SubscriberChannelSize: 64,
	})
	defer hub.Close()

	model := &steeringCapture{
		onCall: func(cm CallModel) Event {
			return ModelCompleted{Content: "ok", FinishReason: "end_turn"}
		},
	}
	lockMgr := locks.NewManager(hub)

	rt := NewRuntime(RuntimeConfig{
		Hub:         hub,
		ModelClient: model,
		LockManager: lockMgr,
		Compactor:   &mockCompactor{},
	})

	// Send steering before running
	rt.Steer("agent-1", "use JWT")

	result, err := rt.Run(context.Background(), "agent-1", Classic, "build auth")
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "ok" {
		t.Errorf("content = %q, want ok", result.Content)
	}
	// Note: steering is consumed on second model call, but our mock only gets called once
	// The machine queues steering, then the first call model should include it if we
	// steer before the machine processes it. For this test we just verify run completes.
}

func TestRuntime_DelegateToSubAgents(t *testing.T) {
	hub := telemetry.NewHubWithConfig(&telemetry.Config{
		EventQueueSize: 1000, BatchSize: 1, FlushInterval: 100 * time.Millisecond,
		RateLimit: 10000, SubscriberChannelSize: 64,
	})
	defer hub.Close()

	// Model for child agents: just return a result immediately
	model := &mockModelCaller{
		responses: []Event{
			ModelCompleted{Content: "task-a done", FinishReason: "end_turn"},
			ModelCompleted{Content: "task-b done", FinishReason: "end_turn"},
		},
	}
	tools := &mockToolExecutor{}
	lockMgr := locks.NewManager(hub)

	rt := NewRuntime(RuntimeConfig{
		Hub:          hub,
		ModelClient:  model,
		ToolExecutor: tools,
		LockManager:  lockMgr,
		Compactor:    &mockCompactor{},
	})

	act := DelegateToSubAgents{
		Tasks: []SubAgentTask{
			{Task: "task-a", Modality: Classic, Spec: "do task a"},
			{Task: "task-b", Modality: Classic, Spec: "do task b"},
		},
	}

	parent := NewObservable("parent", Classic, hub)
	event, err := rt.executeDelegation(context.Background(), parent, act)
	if err != nil {
		t.Fatal(err)
	}

	completed, ok := event.(SubAgentsCompleted)
	if !ok {
		t.Fatalf("expected SubAgentsCompleted, got %T", event)
	}

	if len(completed.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(completed.Results))
	}

	for _, result := range completed.Results {
		if !result.Success {
			t.Errorf("agent %s failed: %s", result.AgentID, result.Summary)
		}
	}
}

func TestRuntime_DelegateWithBudget(t *testing.T) {
	hub := telemetry.NewHubWithConfig(&telemetry.Config{
		EventQueueSize: 1000, BatchSize: 1, FlushInterval: 100 * time.Millisecond,
		RateLimit: 10000, SubscriberChannelSize: 64,
	})
	defer hub.Close()

	// Slow model that blocks until context cancelled
	slowModel := &blockingModelCaller{}
	lockMgr := locks.NewManager(hub)

	rt := NewRuntime(RuntimeConfig{
		Hub:          hub,
		ModelClient:  slowModel,
		LockManager:  lockMgr,
		Compactor:    &mockCompactor{},
	})

	act := DelegateToSubAgents{
		Tasks: []SubAgentTask{
			{Task: "slow-task", Modality: Classic, Spec: "do something slow", Budget: Budget{MaxWallTime: 1}},
		},
	}

	parent := NewObservable("parent", Classic, hub)
	event, err := rt.executeDelegation(context.Background(), parent, act)
	if err != nil {
		t.Fatal(err)
	}

	completed := event.(SubAgentsCompleted)
	if len(completed.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(completed.Results))
	}

	// Should fail due to timeout
	if completed.Results[0].Success {
		t.Error("expected slow task to fail due to budget timeout")
	}
}

type blockingModelCaller struct{}

func (b *blockingModelCaller) Call(ctx context.Context, _ CallModel) (Event, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestRuntime_ReviewSubAgentOutput(t *testing.T) {
	rt := &Runtime{cfg: RuntimeConfig{}, steering: make(map[string]string)}

	// All pass
	event := rt.executeReview(ReviewSubAgentOutput{
		Results: []SubAgentResult{
			{AgentID: "a", Success: true},
			{AgentID: "b", Success: true},
		},
	})
	result := event.(ReviewResult)
	if !result.Passed {
		t.Error("expected review to pass when all agents succeed")
	}

	// One fails
	event = rt.executeReview(ReviewSubAgentOutput{
		Results: []SubAgentResult{
			{AgentID: "a", Success: true},
			{AgentID: "b", Success: false},
		},
	})
	result = event.(ReviewResult)
	if result.Passed {
		t.Error("expected review to fail when an agent fails")
	}
	if result.Reason == "" {
		t.Error("expected reason to be set")
	}
}

type steeringCapture struct {
	onCall func(CallModel) Event
}

func (s *steeringCapture) Call(_ context.Context, action CallModel) (Event, error) {
	return s.onCall(action), nil
}
