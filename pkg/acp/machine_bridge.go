package acp

import (
	"strings"
	"sync"

	"github.com/odvcencio/buckley/pkg/telemetry"
)

// MachineBridge subscribes to the telemetry Hub and translates machine events
// into ACP session/update notifications sent via the Agent's transport.
type MachineBridge struct {
	agent     *Agent
	sessionID string

	mu     sync.Mutex
	closed bool
	unsub  func()
}

// NewMachineBridge creates a bridge that forwards machine events from the Hub
// to the ACP client as session/update notifications.
func NewMachineBridge(agent *Agent, hub *telemetry.Hub, sessionID string) *MachineBridge {
	b := &MachineBridge{
		agent:     agent,
		sessionID: sessionID,
	}

	ch, unsub := hub.Subscribe()
	b.unsub = unsub

	go b.run(ch)
	return b
}

// Close stops the bridge and unsubscribes from the Hub.
func (b *MachineBridge) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	if b.unsub != nil {
		b.unsub()
	}
}

func (b *MachineBridge) run(ch <-chan telemetry.Event) {
	for evt := range ch {
		b.mu.Lock()
		closed := b.closed
		b.mu.Unlock()
		if closed {
			return
		}

		update, ok := b.translate(evt)
		if !ok {
			continue
		}

		if b.agent.transport != nil {
			_ = b.agent.transport.SendNotification("session/update", SessionUpdateNotification{
				SessionID: b.sessionID,
				Update:    update,
			})
		}
	}
}

func (b *MachineBridge) translate(evt telemetry.Event) (SessionUpdate, bool) {
	switch evt.Type {
	case telemetry.EventMachineSpawned:
		return SessionUpdate{
			SessionUpdate: SessionUpdateMachineAgent,
			Content: map[string]any{
				"event":    "spawned",
				"agentId":  dataStr(evt.Data, "agent_id"),
				"modality": dataStr(evt.Data, "modality"),
				"parentId": dataStr(evt.Data, "parent_id"),
			},
		}, true

	case telemetry.EventMachineState:
		return SessionUpdate{
			SessionUpdate: SessionUpdateMachineState,
			Content: map[string]any{
				"agentId": dataStr(evt.Data, "agent_id"),
				"from":    dataStr(evt.Data, "from"),
				"to":      dataStr(evt.Data, "to"),
			},
		}, true

	case telemetry.EventMachineCompleted:
		return SessionUpdate{
			SessionUpdate: SessionUpdateMachineAgent,
			Content: map[string]any{
				"event":     "completed",
				"agentId":   dataStr(evt.Data, "agent_id"),
				"tokensUsed": evt.Data["tokens_used"],
			},
		}, true

	case telemetry.EventMachineFailed:
		return SessionUpdate{
			SessionUpdate: SessionUpdateMachineAgent,
			Content: map[string]any{
				"event":   "failed",
				"agentId": dataStr(evt.Data, "agent_id"),
				"error":   dataStr(evt.Data, "error"),
			},
		}, true

	case telemetry.EventMachineLockAcquired:
		return SessionUpdate{
			SessionUpdate: SessionUpdateMachineLock,
			Content: map[string]any{
				"event":   "acquired",
				"agentId": dataStr(evt.Data, "agent_id"),
				"path":    dataStr(evt.Data, "path"),
				"mode":    dataStr(evt.Data, "mode"),
			},
		}, true

	case telemetry.EventMachineLockReleased:
		return SessionUpdate{
			SessionUpdate: SessionUpdateMachineLock,
			Content: map[string]any{
				"event":   "released",
				"agentId": dataStr(evt.Data, "agent_id"),
				"path":    dataStr(evt.Data, "path"),
			},
		}, true

	case telemetry.EventMachineLockWaiting:
		return SessionUpdate{
			SessionUpdate: SessionUpdateMachineLock,
			Content: map[string]any{
				"event":   "waiting",
				"agentId": dataStr(evt.Data, "agent_id"),
				"path":    dataStr(evt.Data, "path"),
				"heldBy":  dataStr(evt.Data, "held_by"),
			},
		}, true

	default:
		return SessionUpdate{}, false
	}
}

func dataStr(data map[string]any, key string) string {
	if data == nil {
		return ""
	}
	v, ok := data[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}
