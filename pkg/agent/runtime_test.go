package agent

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/bus"
)

func TestNewAgent(t *testing.T) {
	b := bus.NewMemoryBus()
	defer b.Close()

	agent := NewAgent("task-123", RoleCoder, b, nil, nil, DefaultAgentConfig())

	if agent.TaskID != "task-123" {
		t.Errorf("Expected TaskID 'task-123', got %q", agent.TaskID)
	}
	if agent.Role != RoleCoder {
		t.Errorf("Expected Role RoleCoder, got %v", agent.Role)
	}
	if agent.State != StateStarting {
		t.Errorf("Expected State StateStarting, got %v", agent.State)
	}
	if agent.ID == "" {
		t.Error("Expected non-empty ID")
	}
}

func TestAgentLifecycle(t *testing.T) {
	b := bus.NewMemoryBus()
	defer b.Close()

	ctx := context.Background()
	agent := NewAgent("task-lifecycle", RoleExecutor, b, nil, nil, DefaultAgentConfig())

	// Subscribe to status updates
	var statusReceived atomic.Int32
	sub, err := b.Subscribe(ctx, "buckley.agent.*.status", func(msg *bus.Message) []byte {
		statusReceived.Add(1)
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer sub.Unsubscribe()

	// Start the agent
	if err := agent.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Should be active now
	if agent.State != StateActive {
		t.Errorf("Expected StateActive, got %v", agent.State)
	}

	// Wait for status publish
	time.Sleep(50 * time.Millisecond)
	if statusReceived.Load() == 0 {
		t.Error("Expected to receive status update")
	}

	// Cancel
	agent.Cancel()

	if agent.State != StateResolved {
		t.Errorf("Expected StateResolved after cancel, got %v", agent.State)
	}
}

func TestAgentMessageHandlers(t *testing.T) {
	b := bus.NewMemoryBus()
	defer b.Close()

	ctx := context.Background()
	agent := NewAgent("task-handlers", RolePlanner, b, nil, nil, DefaultAgentConfig())

	var received atomic.Int32

	// Register handler before starting
	agent.OnMessage(MsgTypeTask, func(ctx context.Context, msg AgentMessage) error {
		received.Add(1)
		return nil
	})

	if err := agent.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer agent.Cancel()

	// Send a message to the agent
	msg := AgentMessage{
		ID:      "msg-1",
		From:    "test",
		To:      agent.ID,
		Type:    MsgTypeTask,
		Content: json.RawMessage(`{"description": "test task"}`),
	}

	msgData, _ := json.Marshal(msg)
	if err := b.Publish(ctx, agent.InboxSubject(), msgData); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	if received.Load() != 1 {
		t.Errorf("Expected 1 message received, got %d", received.Load())
	}
}

func TestAgentSendTo(t *testing.T) {
	b := bus.NewMemoryBus()
	defer b.Close()

	ctx := context.Background()

	agent1 := NewAgent("task-send", RoleCoder, b, nil, nil, DefaultAgentConfig())
	agent2 := NewAgent("task-send", RoleReviewer, b, nil, nil, DefaultAgentConfig())

	var received atomic.Int32
	agent2.OnMessage(MsgTypeHandoff, func(ctx context.Context, msg AgentMessage) error {
		received.Add(1)
		if msg.From != agent1.ID {
			t.Errorf("Expected From %q, got %q", agent1.ID, msg.From)
		}
		return nil
	})

	if err := agent1.Start(ctx); err != nil {
		t.Fatalf("Start agent1 failed: %v", err)
	}
	defer agent1.Cancel()

	if err := agent2.Start(ctx); err != nil {
		t.Fatalf("Start agent2 failed: %v", err)
	}
	defer agent2.Cancel()

	// Agent1 sends to Agent2
	if err := agent1.SendTo(ctx, agent2.ID, MsgTypeHandoff, map[string]string{"code": "main.go"}); err != nil {
		t.Fatalf("SendTo failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	if received.Load() != 1 {
		t.Errorf("Expected 1 message received, got %d", received.Load())
	}
}

func TestAgentMetadata(t *testing.T) {
	b := bus.NewMemoryBus()
	defer b.Close()

	agent := NewAgent("task-meta", RoleResearcher, b, nil, nil, DefaultAgentConfig())

	agent.SetMetadata("branch", "feature-x")
	agent.SetMetadata("priority", "high")

	if got := agent.GetMetadata("branch"); got != "feature-x" {
		t.Errorf("Expected 'feature-x', got %q", got)
	}
	if got := agent.GetMetadata("priority"); got != "high" {
		t.Errorf("Expected 'high', got %q", got)
	}
	if got := agent.GetMetadata("nonexistent"); got != "" {
		t.Errorf("Expected empty string for nonexistent key, got %q", got)
	}
}

func TestAgentPublishTaskEvent(t *testing.T) {
	b := bus.NewMemoryBus()
	defer b.Close()

	ctx := context.Background()
	agent := NewAgent("task-event", RoleExecutor, b, nil, nil, DefaultAgentConfig())

	// Subscribe to task events
	var eventReceived atomic.Int32
	sub, err := b.Subscribe(ctx, "buckley.task.task-event.events", func(msg *bus.Message) []byte {
		eventReceived.Add(1)
		var event map[string]any
		if err := json.Unmarshal(msg.Data, &event); err != nil {
			t.Errorf("Failed to unmarshal event: %v", err)
			return nil
		}
		if event["agent_id"] != agent.ID {
			t.Errorf("Expected agent_id %q, got %v", agent.ID, event["agent_id"])
		}
		if event["type"] != "progress" {
			t.Errorf("Expected type 'progress', got %v", event["type"])
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer sub.Unsubscribe()

	if err := agent.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer agent.Cancel()

	// Publish a task event
	if err := agent.PublishTaskEvent(ctx, "progress", map[string]any{"step": 1}); err != nil {
		t.Fatalf("PublishTaskEvent failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	if eventReceived.Load() != 1 {
		t.Errorf("Expected 1 event received, got %d", eventReceived.Load())
	}
}

func TestAgentResolve(t *testing.T) {
	b := bus.NewMemoryBus()
	defer b.Close()

	ctx := context.Background()
	agent := NewAgent("task-resolve", RoleExecutor, b, nil, nil, DefaultAgentConfig())

	// Subscribe to task events for resolve
	var resolveReceived atomic.Int32
	sub, err := b.Subscribe(ctx, "buckley.task.task-resolve.events", func(msg *bus.Message) []byte {
		var event map[string]any
		if err := json.Unmarshal(msg.Data, &event); err == nil {
			if event["type"] == "resolved" {
				resolveReceived.Add(1)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer sub.Unsubscribe()

	if err := agent.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	result := map[string]any{
		"success": true,
		"output":  "Task completed",
	}
	if err := agent.Resolve(ctx, result); err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if agent.State != StateResolved {
		t.Errorf("Expected StateResolved, got %v", agent.State)
	}

	time.Sleep(50 * time.Millisecond)

	if resolveReceived.Load() != 1 {
		t.Errorf("Expected 1 resolve event, got %d", resolveReceived.Load())
	}
}

func TestDefaultAgentConfig(t *testing.T) {
	cfg := DefaultAgentConfig()

	if cfg.Timeout != 30*time.Minute {
		t.Errorf("Expected 30m timeout, got %v", cfg.Timeout)
	}
	if cfg.HeartbeatInterval != 10*time.Second {
		t.Errorf("Expected 10s heartbeat, got %v", cfg.HeartbeatInterval)
	}
}

func TestRoleConstants(t *testing.T) {
	roles := []Role{RoleResearcher, RoleCoder, RoleReviewer, RolePlanner, RoleExecutor}
	expected := []string{"researcher", "coder", "reviewer", "planner", "executor"}

	for i, role := range roles {
		if string(role) != expected[i] {
			t.Errorf("Expected role %q, got %q", expected[i], role)
		}
	}
}

func TestStateConstants(t *testing.T) {
	states := []State{StateStarting, StateActive, StateAwaiting, StateResolving, StateResolved}
	expected := []string{"starting", "active", "awaiting", "resolving", "resolved"}

	for i, state := range states {
		if string(state) != expected[i] {
			t.Errorf("Expected state %q, got %q", expected[i], state)
		}
	}
}
