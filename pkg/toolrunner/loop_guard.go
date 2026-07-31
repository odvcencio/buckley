package toolrunner

import (
	"fmt"
	"strings"

	"m31labs.dev/buckley/v2/pkg/agentloop"
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
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "the harness detected repeated tool activity without new evidence"
	}
	return fmt.Sprintf("Buckley stopped the tool loop because %s. Existing tool results remain available; use a different strategy or a narrower follow-up before continuing.", strings.TrimSuffix(reason, "."))
}

func maxRunnerInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
