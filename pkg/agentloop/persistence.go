package agentloop

import (
	"encoding/json"
	"fmt"
	"strings"
)

const PersistenceInstruction = `Continue until the full requested task is complete and the completion contract passes. A progress summary or a next step is not completion. Use run_verification or a recognized shell test/build command after the last workspace change. Do not add or delete scratch files just to satisfy that check. If the workspace has no test or build command, or no check can run, say in your final answer that the change is unverified and why. If progress requires a human, return only this structured result:
{"status":"BLOCKED","kind":"credentials|owner_decision|missing_external_input","reason":"specific obstacle","required_input":"what the human must supply"}
Choose one kind. A failed test, an unfinished step, a tool error with a workaround, or a long task is not a human blocker. Continue with the tools available.`

type blockedResult struct {
	Status        string `json:"status"`
	Kind          string `json:"kind"`
	Reason        string `json:"reason"`
	RequiredInput string `json:"required_input"`
}

func parseBlockedResult(text string) (blockedResult, bool) {
	var result blockedResult
	if json.Unmarshal([]byte(strings.TrimSpace(text)), &result) != nil || result.Status != "BLOCKED" ||
		strings.TrimSpace(result.Reason) == "" || strings.TrimSpace(result.RequiredInput) == "" {
		return result, false
	}
	switch result.Kind {
	case "credentials", "owner_decision", "missing_external_input":
		return result, true
	default:
		return result, false
	}
}

// verificationContinuationHint tells the model how to satisfy a verification
// criterion and what to do when nothing can verify the change.
const verificationContinuationHint = "To verify, call run_tests, or run_verification with one accepted command such as go test ./..., make check, npm test, cargo test, or pytest. Ad hoc run_shell checks such as test, grep, and git diff --check do not count. If no such command applies here, or the check cannot run, say in your final answer that the change is unverified and why. Do not create scratch test files"

func continuationInstruction(err error, previous string) string {
	criterion := err.Error()
	if isVerificationContractError(err) {
		criterion += ". " + verificationContinuationHint
	}
	return fmt.Sprintf("The task is not complete. Unmet completion criterion: %s.\nYour previous response and next step:\n%s\n\nContinue that work now. %s",
		criterion, truncateIncompleteNoticeText(previous, 8000), PersistenceInstruction)
}

// noCheckConfirmationInstruction asks the model, once, whether its work is
// finished when the harness found no check that could verify it.
func noCheckConfirmationInstruction(reason, previous string) string {
	return fmt.Sprintf("The harness found nothing that can verify your change: %s.\nYour previous response and next step:\n%s\n\nIf your work is finished, reply now with your final report and say that the change is unverified. If work remains, continue it now. Do not create scratch test files.",
		reason, truncateIncompleteNoticeText(previous, 8000))
}
