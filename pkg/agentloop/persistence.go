package agentloop

import (
	"encoding/json"
	"fmt"
	"strings"
)

const PersistenceInstruction = `Continue until the full requested task is complete and the completion contract passes. A progress summary or a next step is not completion. Use run_verification or a recognized shell test/build command after the last workspace change. If progress requires a human, return only this structured result:
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

func continuationInstruction(err error, previous string) string {
	return fmt.Sprintf("The task is not complete. Unmet completion criterion: %s.\nYour previous response and next step:\n%s\n\nContinue that work now. %s",
		err, truncateIncompleteNoticeText(previous, 8000), PersistenceInstruction)
}
