package events

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestAgentRegisteredEvent(t *testing.T) {
	event := NewAgentRegisteredEvent("agent-1", []string{"read_files"}, "localhost:50051")

	assert.Equal(t, "agent.registered", event.Type)
	assert.Equal(t, "agent-1", event.Data["agent_id"])
	assert.Equal(t, []string{"read_files"}, event.Data["capabilities"])
	assert.Equal(t, "localhost:50051", event.Data["endpoint"])
	assert.NotZero(t, event.Timestamp)
}

func TestAgentUnregisteredEvent(t *testing.T) {
	event := NewAgentUnregisteredEvent("agent-1", "shutdown")

	assert.Equal(t, "agent.unregistered", event.Type)
	assert.Equal(t, "agent-1", event.Data["agent_id"])
	assert.Equal(t, "shutdown", event.Data["reason"])
	assert.NotZero(t, event.Timestamp)
}

func TestTaskCreatedEvent(t *testing.T) {
	event := NewTaskCreatedEvent("task-123", "plan-456", "agent-789")

	assert.Equal(t, "task.created", event.Type)
	assert.Equal(t, "task-123", event.Data["task_id"])
	assert.Equal(t, "plan-456", event.Data["plan_id"])
	assert.Equal(t, "agent-789", event.Data["agent_id"])
	assert.NotZero(t, event.Timestamp)
}

func TestTaskProgressEvent(t *testing.T) {
	event := NewTaskProgressEvent("task-123", 50, "halfway done")

	assert.Equal(t, "task.progress", event.Type)
	assert.Equal(t, "task-123", event.Data["task_id"])
	assert.Equal(t, 50, event.Data["progress"])
	assert.Equal(t, "halfway done", event.Data["message"])
	assert.NotZero(t, event.Timestamp)
}

func TestTaskCompletedEvent(t *testing.T) {
	result := map[string]string{
		"status": "success",
		"output": "completed successfully",
	}
	event := NewTaskCompletedEvent("task-123", result)

	assert.Equal(t, "task.completed", event.Type)
	assert.Equal(t, "task-123", event.Data["task_id"])
	assert.Equal(t, result, event.Data["result"])
	assert.NotZero(t, event.Timestamp)
}

func TestContextHandleCreatedEvent(t *testing.T) {
	event := NewContextHandleCreatedEvent("handle-abc", "file", int64(1024))

	assert.Equal(t, "context.handle_created", event.Type)
	assert.Equal(t, "handle-abc", event.Data["handle_id"])
	assert.Equal(t, "file", event.Data["type"])
	assert.Equal(t, int64(1024), event.Data["size"])
	assert.NotZero(t, event.Timestamp)
}

func TestCapabilityGrantedEvent(t *testing.T) {
	expiresAt := time.Now().Add(24 * time.Hour)
	event := NewCapabilityGrantedEvent("grant-xyz", "agent-123", []string{"read_files", "write_files"}, expiresAt)

	assert.Equal(t, "capability.granted", event.Type)
	assert.Equal(t, "grant-xyz", event.Data["grant_id"])
	assert.Equal(t, "agent-123", event.Data["agent_id"])
	assert.Equal(t, []string{"read_files", "write_files"}, event.Data["capabilities"])
	assert.Equal(t, expiresAt, event.Data["expires_at"])
	assert.NotZero(t, event.Timestamp)
}
