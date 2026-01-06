package events

import "time"

const (
	EventTypeAgentRegistered       = "agent.registered"
	EventTypeAgentUnregistered     = "agent.unregistered"
	EventTypeTaskCreated           = "task.created"
	EventTypeTaskProgress          = "task.progress"
	EventTypeTaskCompleted         = "task.completed"
	EventTypeContextHandleCreated  = "context.handle_created"
	EventTypeSessionContextUpdated = "session.context_updated"
	EventTypeCapabilityGranted     = "capability.granted"
)

// NewAgentRegisteredEvent creates an agent registered event
func NewAgentRegisteredEvent(agentID string, capabilities []string, endpoint string) Event {
	return Event{
		Type: EventTypeAgentRegistered,
		Data: map[string]interface{}{
			"agent_id":     agentID,
			"capabilities": capabilities,
			"endpoint":     endpoint,
		},
		Timestamp: time.Now(),
	}
}

// NewAgentUnregisteredEvent creates an agent unregistered event
func NewAgentUnregisteredEvent(agentID string, reason string) Event {
	return Event{
		Type: EventTypeAgentUnregistered,
		Data: map[string]interface{}{
			"agent_id": agentID,
			"reason":   reason,
		},
		Timestamp: time.Now(),
	}
}

// NewTaskCreatedEvent creates a task created event
func NewTaskCreatedEvent(taskID, planID, agentID string) Event {
	return Event{
		Type: EventTypeTaskCreated,
		Data: map[string]interface{}{
			"task_id":  taskID,
			"plan_id":  planID,
			"agent_id": agentID,
		},
		Timestamp: time.Now(),
	}
}

// NewTaskProgressEvent creates a task progress event
func NewTaskProgressEvent(taskID string, progress int, message string) Event {
	return Event{
		Type: EventTypeTaskProgress,
		Data: map[string]interface{}{
			"task_id":  taskID,
			"progress": progress,
			"message":  message,
		},
		Timestamp: time.Now(),
	}
}

// NewTaskCompletedEvent creates a task completed event
func NewTaskCompletedEvent(taskID string, result interface{}) Event {
	return Event{
		Type: EventTypeTaskCompleted,
		Data: map[string]interface{}{
			"task_id": taskID,
			"result":  result,
		},
		Timestamp: time.Now(),
	}
}

// NewContextHandleCreatedEvent creates a context handle created event
func NewContextHandleCreatedEvent(handleID, contextType string, size int64) Event {
	return Event{
		Type: EventTypeContextHandleCreated,
		Data: map[string]interface{}{
			"handle_id": handleID,
			"type":      contextType,
			"size":      size,
		},
		Timestamp: time.Now(),
	}
}

// NewCapabilityGrantedEvent creates a capability granted event
func NewCapabilityGrantedEvent(grantID, agentID string, capabilities []string, expiresAt time.Time) Event {
	return Event{
		Type: EventTypeCapabilityGranted,
		Data: map[string]interface{}{
			"grant_id":     grantID,
			"agent_id":     agentID,
			"capabilities": capabilities,
			"expires_at":   expiresAt,
		},
		Timestamp: time.Now(),
	}
}
