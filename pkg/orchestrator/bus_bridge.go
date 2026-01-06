package orchestrator

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/odvcencio/buckley/pkg/bus"
	"github.com/odvcencio/buckley/pkg/telemetry"
)

// TelemetryBusBridge forwards telemetry events to the MessageBus.
// This enables distributed agents and IPC clients to observe orchestrator activity.
type TelemetryBusBridge struct {
	telemetryHub *telemetry.Hub
	messageBus   bus.MessageBus
	eventCh      <-chan telemetry.Event
	unsubscribe  func()
	cancel       context.CancelFunc
	wg           sync.WaitGroup
}

// NewTelemetryBusBridge creates a bridge from telemetry hub to message bus.
func NewTelemetryBusBridge(th *telemetry.Hub, mb bus.MessageBus) *TelemetryBusBridge {
	eventCh, unsub := th.Subscribe()
	return &TelemetryBusBridge{
		telemetryHub: th,
		messageBus:   mb,
		eventCh:      eventCh,
		unsubscribe:  unsub,
	}
}

// Start begins forwarding telemetry events to the message bus.
func (b *TelemetryBusBridge) Start(ctx context.Context) {
	ctx, b.cancel = context.WithCancel(ctx)
	b.wg.Add(1)
	go b.forwardLoop(ctx)
}

// Stop ceases forwarding and cleans up subscriptions.
func (b *TelemetryBusBridge) Stop() {
	if b.cancel != nil {
		b.cancel()
	}
	if b.unsubscribe != nil {
		b.unsubscribe()
	}
	b.wg.Wait()
}

func (b *TelemetryBusBridge) forwardLoop(ctx context.Context) {
	defer b.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-b.eventCh:
			if !ok {
				return
			}
			b.publishEvent(ctx, event)
		}
	}
}

func (b *TelemetryBusBridge) publishEvent(ctx context.Context, event telemetry.Event) {
	// Build subject based on event type and IDs
	subject := buildSubject(event)

	// Serialize the event
	payload := map[string]any{
		"type":      string(event.Type),
		"timestamp": event.Timestamp,
	}
	if event.SessionID != "" {
		payload["session_id"] = event.SessionID
	}
	if event.PlanID != "" {
		payload["plan_id"] = event.PlanID
	}
	if event.TaskID != "" {
		payload["task_id"] = event.TaskID
	}
	if event.Data != nil {
		payload["data"] = event.Data
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return
	}

	_ = b.messageBus.Publish(ctx, subject, data)
}

func buildSubject(event telemetry.Event) string {
	base := "buckley.orchestrator"

	// Add plan/task context if available
	if event.PlanID != "" {
		base += ".plan." + event.PlanID
	}
	if event.TaskID != "" {
		base += ".task." + event.TaskID
	}

	// Add event type
	base += "." + string(event.Type)

	return base
}

// BusEventEmitter implements a telemetry-compatible event emitter that writes to MessageBus.
// Use this when you want to emit events directly without going through the telemetry hub.
type BusEventEmitter struct {
	bus       bus.MessageBus
	sessionID string
	planID    string
}

// NewBusEventEmitter creates an emitter for orchestrator events.
func NewBusEventEmitter(mb bus.MessageBus, sessionID, planID string) *BusEventEmitter {
	return &BusEventEmitter{
		bus:       mb,
		sessionID: sessionID,
		planID:    planID,
	}
}

// Emit publishes an event to the message bus.
func (e *BusEventEmitter) Emit(ctx context.Context, eventType telemetry.EventType, taskID string, data map[string]any) error {
	event := telemetry.Event{
		Type:      eventType,
		SessionID: e.sessionID,
		PlanID:    e.planID,
		TaskID:    taskID,
		Data:      data,
	}

	payload := map[string]any{
		"type":       string(event.Type),
		"session_id": e.sessionID,
		"plan_id":    e.planID,
	}
	if taskID != "" {
		payload["task_id"] = taskID
	}
	if data != nil {
		payload["data"] = data
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	subject := buildSubject(event)
	return e.bus.Publish(ctx, subject, jsonData)
}

// EmitTaskStarted publishes a task.started event.
func (e *BusEventEmitter) EmitTaskStarted(ctx context.Context, taskID, taskName string) error {
	return e.Emit(ctx, telemetry.EventTaskStarted, taskID, map[string]any{
		"name": taskName,
	})
}

// EmitTaskCompleted publishes a task.completed event.
func (e *BusEventEmitter) EmitTaskCompleted(ctx context.Context, taskID string, output string, tokensUsed int) error {
	return e.Emit(ctx, telemetry.EventTaskCompleted, taskID, map[string]any{
		"output":      output,
		"tokens_used": tokensUsed,
	})
}

// EmitTaskFailed publishes a task.failed event.
func (e *BusEventEmitter) EmitTaskFailed(ctx context.Context, taskID, errMsg string) error {
	return e.Emit(ctx, telemetry.EventTaskFailed, taskID, map[string]any{
		"error": errMsg,
	})
}

// EmitBuilderStarted publishes a builder.started event.
func (e *BusEventEmitter) EmitBuilderStarted(ctx context.Context, taskID string) error {
	return e.Emit(ctx, telemetry.EventBuilderStarted, taskID, nil)
}

// EmitBuilderCompleted publishes a builder.completed event.
func (e *BusEventEmitter) EmitBuilderCompleted(ctx context.Context, taskID string, artifacts []string) error {
	return e.Emit(ctx, telemetry.EventBuilderCompleted, taskID, map[string]any{
		"artifacts": artifacts,
	})
}

// EmitResearchStarted publishes a research.started event.
func (e *BusEventEmitter) EmitResearchStarted(ctx context.Context, query string) error {
	return e.Emit(ctx, telemetry.EventResearchStarted, "", map[string]any{
		"query": query,
	})
}

// EmitResearchCompleted publishes a research.completed event.
func (e *BusEventEmitter) EmitResearchCompleted(ctx context.Context, summary string) error {
	return e.Emit(ctx, telemetry.EventResearchCompleted, "", map[string]any{
		"summary": summary,
	})
}
