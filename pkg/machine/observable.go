package machine

import (
	"time"

	"github.com/odvcencio/buckley/pkg/telemetry"
)

// Observable wraps a Machine and publishes state transitions to the telemetry Hub.
type Observable struct {
	*Machine
	hub *telemetry.Hub
}

// NewObservable creates a Machine that publishes events to the Hub.
func NewObservable(id string, modality Modality, hub *telemetry.Hub) *Observable {
	o := &Observable{
		Machine: New(id, modality),
		hub:     hub,
	}
	o.publish(telemetry.EventMachineSpawned, map[string]any{
		"agent_id": id,
		"modality": modality.String(),
	})
	return o
}

// NewObservableWithParent creates a child Machine with parent tracking.
func NewObservableWithParent(id string, modality Modality, parentID, task, model string, hub *telemetry.Hub) *Observable {
	o := &Observable{
		Machine: New(id, modality),
		hub:     hub,
	}
	o.publish(telemetry.EventMachineSpawned, map[string]any{
		"agent_id":  id,
		"parent_id": parentID,
		"modality":  modality.String(),
		"task":      task,
		"model":     model,
	})
	return o
}

// Transition wraps Machine.Transition and publishes state changes.
func (o *Observable) Transition(event Event) (State, []Action) {
	prev := o.Machine.State()
	next, actions := o.Machine.Transition(event)

	if next != prev {
		o.publish(telemetry.EventMachineState, map[string]any{
			"agent_id": o.Machine.ID(),
			"from":     prev.String(),
			"to":       next.String(),
		})
	}

	for _, a := range actions {
		switch act := a.(type) {
		case EmitResult:
			o.publish(telemetry.EventMachineCompleted, map[string]any{
				"agent_id":    o.Machine.ID(),
				"result":      act.Content,
				"tokens_used": act.TokensUsed,
			})
		case EmitError:
			errMsg := ""
			if act.Err != nil {
				errMsg = act.Err.Error()
			}
			o.publish(telemetry.EventMachineFailed, map[string]any{
				"agent_id":       o.Machine.ID(),
				"error":          errMsg,
				"retry_strategy": act.RetryStrategy,
			})
		}
	}

	return next, actions
}

func (o *Observable) publish(eventType telemetry.EventType, data map[string]any) {
	if o.hub == nil {
		return
	}
	o.hub.Publish(telemetry.Event{
		Type:      eventType,
		Timestamp: time.Now(),
		Data:      data,
	})
}
