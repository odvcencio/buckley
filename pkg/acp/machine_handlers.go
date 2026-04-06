package acp

import "context"

// MachineHandlers are callbacks for Buckley machine extension methods.
// These are optional — if nil, the corresponding ACP method returns an error.
type MachineHandlers struct {
	// OnSpawnAgent spawns a new agent machine.
	OnSpawnAgent func(ctx context.Context, params *SpawnAgentParams) (*SpawnAgentResult, error)

	// OnSteerAgent injects steering content into a running agent.
	OnSteerAgent func(ctx context.Context, params *SteerAgentParams) error

	// OnListAgents returns currently active agents.
	OnListAgents func(ctx context.Context, params *ListAgentsParams) (*ListAgentsResult, error)

	// OnEscalateMode changes a running agent's modality.
	OnEscalateMode func(ctx context.Context, params *EscalateModeParams) (*EscalateModeResult, error)
}

// handleMachineSpawnAgent handles "machine/spawn_agent".
func (a *Agent) handleMachineSpawnAgent(ctx context.Context, req *Request) {
	params, err := ParseParams[SpawnAgentParams](req)
	if err != nil {
		_ = a.transport.SendError(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
		return
	}

	if a.machineHandlers.OnSpawnAgent == nil {
		_ = a.transport.SendError(req.ID, ErrCodeMethodNotFound, "spawn_agent not configured", nil)
		return
	}

	result, err := a.machineHandlers.OnSpawnAgent(ctx, params)
	if err != nil {
		_ = a.transport.SendError(req.ID, ErrCodeInternal, "spawn_agent failed", err.Error())
		return
	}

	_ = a.transport.SendResponse(req.ID, result)
}

// handleMachineSteerAgent handles "machine/steer_agent".
func (a *Agent) handleMachineSteerAgent(ctx context.Context, req *Request) {
	params, err := ParseParams[SteerAgentParams](req)
	if err != nil {
		_ = a.transport.SendError(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
		return
	}

	if a.machineHandlers.OnSteerAgent == nil {
		_ = a.transport.SendError(req.ID, ErrCodeMethodNotFound, "steer_agent not configured", nil)
		return
	}

	if err := a.machineHandlers.OnSteerAgent(ctx, params); err != nil {
		_ = a.transport.SendError(req.ID, ErrCodeInternal, "steer_agent failed", err.Error())
		return
	}

	_ = a.transport.SendResponse(req.ID, SteerAgentResult{})
}

// handleMachineListAgents handles "machine/list_agents".
func (a *Agent) handleMachineListAgents(ctx context.Context, req *Request) {
	params, err := ParseParams[ListAgentsParams](req)
	if err != nil {
		_ = a.transport.SendError(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
		return
	}

	if a.machineHandlers.OnListAgents == nil {
		_ = a.transport.SendError(req.ID, ErrCodeMethodNotFound, "list_agents not configured", nil)
		return
	}

	result, err := a.machineHandlers.OnListAgents(ctx, params)
	if err != nil {
		_ = a.transport.SendError(req.ID, ErrCodeInternal, "list_agents failed", err.Error())
		return
	}

	_ = a.transport.SendResponse(req.ID, result)
}

// handleMachineEscalateMode handles "machine/escalate_mode".
func (a *Agent) handleMachineEscalateMode(ctx context.Context, req *Request) {
	params, err := ParseParams[EscalateModeParams](req)
	if err != nil {
		_ = a.transport.SendError(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
		return
	}

	if a.machineHandlers.OnEscalateMode == nil {
		_ = a.transport.SendError(req.ID, ErrCodeMethodNotFound, "escalate_mode not configured", nil)
		return
	}

	result, err := a.machineHandlers.OnEscalateMode(ctx, params)
	if err != nil {
		_ = a.transport.SendError(req.ID, ErrCodeInternal, "escalate_mode failed", err.Error())
		return
	}

	_ = a.transport.SendResponse(req.ID, result)
}
