package acp

import (
	"strings"
	"sync"

	"m31labs.dev/buckley/v2/pkg/telemetry"
)

// MachineBridge subscribes to the telemetry Hub and translates machine events
// into ACP session/update notifications sent via the Agent's transport.
type MachineBridge struct {
	agent     *Agent
	sessionID string

	// allowFullPayloads controls whether full tool call arguments/results
	// riding along on telemetry events are forwarded to the (network) ACP
	// client. Defaults to false: key-name redaction can't protect file
	// contents or other unstructured tool output in those fields.
	allowFullPayloads bool

	mu     sync.Mutex
	closed bool
	unsub  func()
	wg     sync.WaitGroup
}

// NewMachineBridge creates a bridge that forwards machine events from the Hub
// to the ACP client as session/update notifications. allowFullPayloads must
// be false unless the operator has explicitly opted in to full tool
// arguments/results leaving the process over this transport.
func NewMachineBridge(agent *Agent, hub *telemetry.Hub, sessionID string, allowFullPayloads bool) *MachineBridge {
	b := &MachineBridge{
		agent:             agent,
		sessionID:         sessionID,
		allowFullPayloads: allowFullPayloads,
	}

	ch, unsub := hub.Subscribe()
	b.unsub = unsub

	b.wg.Add(1)
	go b.run(ch)
	return b
}

// Close stops the bridge and unsubscribes from the Hub.
func (b *MachineBridge) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	if b.unsub != nil {
		b.unsub()
	}
	b.mu.Unlock()
	b.wg.Wait()
}

func (b *MachineBridge) run(ch <-chan telemetry.Event) {
	defer b.wg.Done()
	for evt := range ch {
		b.mu.Lock()
		closed := b.closed
		b.mu.Unlock()
		if closed {
			return
		}

		evt = b.prepareEvent(evt)
		update, ok := b.translate(evt)
		if !ok {
			continue
		}

		if b.agent.transport != nil {
			// Notification errors are intentionally ignored: the bridge is
			// best-effort and a send failure (e.g. closed transport) should
			// not stop event processing for other subscribers.
			_ = b.agent.transport.SendNotification("session/update", SessionUpdateNotification{
				SessionID: b.sessionID,
				Update:    update,
			})
		}
	}
}

// prepareEvent applies network payload gating before further processing.
// translate() doesn't currently read the arguments/result fields for any
// event type, but this keeps the bridge safe by construction if a future
// translate() case starts surfacing tool payload data to the ACP client.
func (b *MachineBridge) prepareEvent(evt telemetry.Event) telemetry.Event {
	if !b.allowFullPayloads {
		return telemetry.StripToolPayloads(evt)
	}
	return evt
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
				"event":      "completed",
				"agentId":    dataStr(evt.Data, "agent_id"),
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
