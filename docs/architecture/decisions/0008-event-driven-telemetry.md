# ADR 0008: Event-Driven Telemetry

## Status

Accepted

## Context

Buckley needs observability for:
- TUI progress displays
- IPC clients (web UI, IDE plugins)
- Debugging and logging
- Cost tracking

Requirements:
- Decouple event producers from consumers
- Support multiple simultaneous subscribers
- Non-blocking event delivery
- Type-safe event definitions

Options considered:
1. **Direct callbacks** - Tight coupling, hard to manage multiple listeners
2. **Channel per consumer** - Works but manual wiring
3. **Pub/sub hub** - Centralized, decoupled, scalable

## Decision

Implement a telemetry hub with typed events and fan-out:

```go
type EventType string

const (
    EventTaskStarted       EventType = "task.started"
    EventTaskCompleted     EventType = "task.completed"
    EventToolStarted       EventType = "tool.started"
    EventShellCommandFailed EventType = "shell.failed"
    // ... etc
)

type Event struct {
    Type      EventType      `json:"type"`
    Timestamp time.Time      `json:"timestamp"`
    SessionID string         `json:"sessionId,omitempty"`
    TaskID    string         `json:"taskId,omitempty"`
    Data      map[string]any `json:"data,omitempty"`
}

type Hub struct {
    mu          sync.RWMutex
    subscribers map[chan Event]struct{}
}

// Non-blocking publish - drops if subscriber buffer full
func (h *Hub) Publish(event Event) {
    h.mu.RLock()
    defer h.mu.RUnlock()
    for ch := range h.subscribers {
        select {
        case ch <- event:
        default:  // Drop if full
        }
    }
}
```

### Event Categories

| Category | Events | Use Case |
|----------|--------|----------|
| Task | started, completed, failed | Progress tracking |
| Tool | started, completed, failed | Activity feed |
| Shell | started, completed, failed | Command audit |
| Cost | updated | Budget monitoring |
| Model | stream_start, stream_end | Streaming UI |

## Consequences

### Positive
- Clean decoupling between producers and consumers
- Multiple subscribers without coordination
- Non-blocking prevents slow consumers from blocking producers
- Typed events enable compile-time safety

### Negative
- Events may be dropped if consumer too slow
- Memory overhead for buffered channels
- No guaranteed delivery (acceptable for telemetry)

### Usage
```go
hub := telemetry.NewHub()
ch, unsub := hub.Subscribe()
defer unsub()

go func() {
    for event := range ch {
        // Handle event
    }
}()
```
