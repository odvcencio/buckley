package orchestrator

import "m31labs.dev/buckley/pkg/agentcoord"

// The coordination contract is dependency-free in pkg/agentcoord so local
// process adapters can implement it without importing orchestration's legacy
// tool wiring. These aliases keep the domain port available from the
// orchestrator package while preserving that acyclic dependency direction.
type (
	AgentRunState     = agentcoord.RunState
	AgentBudget       = agentcoord.Budget
	AgentTaskSpec     = agentcoord.TaskSpec
	AgentResult       = agentcoord.Result
	AgentRun          = agentcoord.Run
	AgentRunFilter    = agentcoord.RunFilter
	AgentMessage      = agentcoord.Message
	AgentClaimRequest = agentcoord.ClaimRequest
	AgentClaimResult  = agentcoord.ClaimResult
	AgentCoordinator  = agentcoord.Coordinator
)

const (
	AgentRunQueued    = agentcoord.RunQueued
	AgentRunRunning   = agentcoord.RunRunning
	AgentRunCompleted = agentcoord.RunCompleted
	AgentRunFailed    = agentcoord.RunFailed
	AgentRunCancelled = agentcoord.RunCancelled
	AgentRunBlocked   = agentcoord.RunBlocked
	AgentRunResumable = agentcoord.RunResumable
)
