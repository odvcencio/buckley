package oneshot

import (
	"strings"
	"testing"
)

func TestHarnessEvidenceAllowsRequiredPerPhaseModelCheck(t *testing.T) {
	prompt := formatHostAgentEvidence([]AgentToolCall{{
		ID:      "host-evidence-1",
		Name:    "run_verification",
		Success: true,
		Result:  "success: true status: PASS",
	}})
	for _, instruction := range []string{
		"This evidence remains authoritative across validation retries and the approval critic",
		"Do not repeat a successful call merely to reproduce its status",
		"When the depth contract requires model-directed verification and run_verification is enabled",
		"in the current phase even when the deterministic plan passed",
		"Harness-collected calls do not satisfy that per-phase requirement",
		"Do not rerun the entire passing plan",
	} {
		if !strings.Contains(prompt, instruction) {
			t.Errorf("harness evidence prompt omitted %q", instruction)
		}
	}
}
