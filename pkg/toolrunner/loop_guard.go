package toolrunner

import (
	"strings"

	"m31labs.dev/buckley/pkg/agentloop"
)

func newRunnerLoopGovernor(maxRounds int) *agentloop.Governor {
	config := agentloop.DefaultConfig()
	if maxRounds > 0 {
		config.MaxRounds = maxRounds
		config.MaxToolCalls = maxRunnerInt(24, maxRounds*4)
	}
	return agentloop.New(config)
}

func applyRunnerLoopGuard(governor *agentloop.Governor, record ToolCallRecord, modelResult string) (string, agentloop.Decision) {
	if governor == nil {
		return modelResult, agentloop.Decision{}
	}
	decision := governor.Observe(record.Name, record.Arguments, modelResult, record.Success)
	if strings.TrimSpace(decision.Nudge) != "" {
		modelResult += "\n\n" + decision.Nudge
	}
	return modelResult, decision
}

func runnerLoopGuardMessage(reason string) string {
	return agentloop.GuardStopMessage(reason)
}

func maxRunnerInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
