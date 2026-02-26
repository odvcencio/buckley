// Package tui provides the integrated terminal user interface for Buckley.
// This file implements machine event subscription for the TUI runner.

package tui

import (
	"context"

	buckleywidgets "github.com/odvcencio/buckley/pkg/buckley/ui/widgets"
	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/fluffyui/runtime"
)

// subscribeToMachineEvents subscribes to the telemetry Hub and routes machine
// events to TUI signals. Must be called after r.app is initialized.
func (r *Runner) subscribeToMachineEvents(hub *telemetry.Hub) {
	if hub == nil || r.app == nil {
		return
	}

	ch, unsub := hub.Subscribe()
	r.machineUnsub = unsub

	go func() {
		for evt := range ch {
			r.handleMachineEvent(evt)
		}
	}()
}

// stopMachineSubscription unsubscribes from the telemetry Hub.
func (r *Runner) stopMachineSubscription() {
	if r == nil {
		return
	}
	if r.machineUnsub != nil {
		r.machineUnsub()
		r.machineUnsub = nil
	}
}

func (r *Runner) handleMachineEvent(evt telemetry.Event) {
	if r.app == nil || r.state == nil {
		return
	}

	switch evt.Type {
	case telemetry.EventMachineSpawned:
		r.onMachineSpawned(evt.Data)
	case telemetry.EventMachineState:
		r.onMachineStateChange(evt.Data)
	case telemetry.EventMachineCompleted, telemetry.EventMachineFailed:
		r.onMachineTerminal(evt.Data)
	case telemetry.EventMachineLockAcquired:
		r.onLockAcquired(evt.Data)
	case telemetry.EventMachineLockReleased:
		r.onLockReleased(evt.Data)
	}
}

func (r *Runner) onMachineSpawned(data map[string]any) {
	agentID, _ := data["agent_id"].(string)
	modality, _ := data["modality"].(string)
	parentID, _ := data["parent_id"].(string)
	task, _ := data["task"].(string)

	if agentID == "" {
		return
	}

	_ = r.app.Call(context.Background(), func(_ *runtime.App) error {
		agents := r.state.MachineAgents.Get()
		agents = append(agents, buckleywidgets.AgentSummary{
			ID:       agentID,
			State:    "idle",
			Modality: modality,
			ParentID: parentID,
			Task:     task,
		})
		r.state.MachineAgents.Set(agents)
		return nil
	})
}

func (r *Runner) onMachineStateChange(data map[string]any) {
	agentID, _ := data["agent_id"].(string)
	toState, _ := data["to"].(string)

	if agentID == "" {
		return
	}

	_ = r.app.Call(context.Background(), func(_ *runtime.App) error {
		agents := r.state.MachineAgents.Get()
		cloned := make([]buckleywidgets.AgentSummary, len(agents))
		copy(cloned, agents)
		for i, a := range cloned {
			if a.ID == agentID {
				cloned[i].State = toState
				r.state.MachineAgents.Set(cloned)
				return nil
			}
		}
		return nil
	})
}

func (r *Runner) onMachineTerminal(data map[string]any) {
	agentID, _ := data["agent_id"].(string)
	if agentID == "" {
		return
	}

	_ = r.app.Call(context.Background(), func(_ *runtime.App) error {
		agents := r.state.MachineAgents.Get()
		filtered := make([]buckleywidgets.AgentSummary, 0, len(agents))
		for _, a := range agents {
			if a.ID != agentID {
				filtered = append(filtered, a)
			}
		}
		r.state.MachineAgents.Set(filtered)
		return nil
	})
}

func (r *Runner) onLockAcquired(data map[string]any) {
	agentID, _ := data["agent_id"].(string)
	path, _ := data["path"].(string)
	mode, _ := data["mode"].(string)

	if path == "" {
		return
	}

	_ = r.app.Call(context.Background(), func(_ *runtime.App) error {
		locks := r.state.MachineFileLocks.Get()
		locks = append(locks, buckleywidgets.FileLockSummary{
			Path:   path,
			Holder: agentID,
			Mode:   mode,
		})
		r.state.MachineFileLocks.Set(locks)
		return nil
	})
}

func (r *Runner) onLockReleased(data map[string]any) {
	agentID, _ := data["agent_id"].(string)
	path, _ := data["path"].(string)

	_ = r.app.Call(context.Background(), func(_ *runtime.App) error {
		locks := r.state.MachineFileLocks.Get()
		filtered := make([]buckleywidgets.FileLockSummary, 0, len(locks))
		for _, l := range locks {
			if !(l.Path == path && l.Holder == agentID) {
				filtered = append(filtered, l)
			}
		}
		r.state.MachineFileLocks.Set(filtered)
		return nil
	})
}
