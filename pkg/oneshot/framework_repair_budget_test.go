package oneshot

import "testing"

// A repair must never have less room than the attempt it repairs.
//
// AgentRunner derives a completion budget from ReasoningMaxTokens when no
// explicit budget is set. Text repair shrinks the reasoning ceiling, so deriving
// the budget after the shrink handed the repair a smaller allowance than the
// attempt that produced the rejected output. The model could not restate the
// prior review plus the section the gate demanded, so it dropped one mandatory
// section to add another and the loop oscillated instead of converging.
func TestTextRepairKeepsAtLeastTheOriginalCompletionBudget(t *testing.T) {
	base := AgentExecutionOpts{ReasoningMaxTokens: 8192}
	original := reviewAgentOutputTokenLimit(base.ReasoningMaxTokens)

	repair, err := agentPhaseAttemptOptions(base, agentValidationRetryText, 1, 0)
	if err != nil {
		t.Fatal(err)
	}

	effective := repair.MaxOutputTokens
	if effective <= 0 {
		effective = reviewAgentOutputTokenLimit(repair.ReasoningMaxTokens)
	}
	if effective < original {
		t.Fatalf("repair completion budget = %d, want at least the original %d (reasoning shrank %d -> %d)",
			effective, original, base.ReasoningMaxTokens, repair.ReasoningMaxTokens)
	}
}

// An explicit budget is authoritative and must survive the repair untouched.
func TestTextRepairPreservesAnExplicitCompletionBudget(t *testing.T) {
	base := AgentExecutionOpts{ReasoningMaxTokens: 8192, MaxOutputTokens: 32768}

	repair, err := agentPhaseAttemptOptions(base, agentValidationRetryText, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if repair.MaxOutputTokens != 32768 {
		t.Fatalf("repair MaxOutputTokens = %d, want the explicit 32768", repair.MaxOutputTokens)
	}
}

// The first attempt is not a repair and must not be rewritten.
func TestFirstAttemptKeepsBaseOptions(t *testing.T) {
	base := AgentExecutionOpts{ReasoningMaxTokens: 8192}

	first, err := agentPhaseAttemptOptions(base, agentValidationRetryText, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if first.ReasoningMaxTokens != base.ReasoningMaxTokens {
		t.Fatalf("first attempt ReasoningMaxTokens = %d, want %d", first.ReasoningMaxTokens, base.ReasoningMaxTokens)
	}
	if first.MaxOutputTokens != base.MaxOutputTokens {
		t.Fatalf("first attempt MaxOutputTokens = %d, want %d", first.MaxOutputTokens, base.MaxOutputTokens)
	}
}
