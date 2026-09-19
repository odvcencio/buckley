package artifactv1

import (
	"m31labs.dev/buckley/pkg/agentcoord"
)

// FromSubagentRun converts the shared child-agent contract directly, so every
// local, ACP, or future provider-native coordinator gets the same report.
func FromSubagentRun(run agentcoord.AgentRun) (Artifact, error) {
	title := "Subagent: " + run.ID
	if run.Task.Agent != "" {
		title = "Subagent " + run.Task.Agent + ": " + run.ID
	}
	summary := run.Result.Summary
	if summary == "" {
		summary = "subagent is " + string(run.State)
	}
	result := Artifact{
		SchemaVersion: SchemaVersion,
		Kind:          KindSubagentResult,
		Status:        agentRunStatus(run.State),
		Title:         title,
		Summary:       summary,
		Metadata: map[string]string{
			"run_id":            run.ID,
			"parent_run_id":     run.ParentRunID,
			"parent_session_id": run.ParentSessionID,
			"adapter":           run.Adapter,
			"model":             run.Task.Model,
			"tier":              run.Task.Tier,
		},
		Blocks: []Block{{Kind: BlockOperationSummary, Operation: &OperationSummary{
			Operation: "subagent",
			Status:    string(run.State),
			Detail:    run.Result.Error,
		}}},
	}
	for _, evidenceID := range run.Result.EvidenceRefs {
		if validIdentifier(evidenceID) {
			result.EvidenceRefs = append(result.EvidenceRefs, EvidenceRef{ID: evidenceID})
		}
	}
	if run.Result.Error != "" {
		result.Diagnostics = append(result.Diagnostics, Diagnostic{Level: "error", Code: "subagent.result", Message: run.Result.Error})
	}
	if run.State == agentcoord.AgentRunResumable || run.State == agentcoord.AgentRunBlocked {
		result.IncompleteReasons = append(result.IncompleteReasons, "subagent requires resume or operator intervention")
	}
	return NormalizeAndValidate(result)
}

func agentRunStatus(state agentcoord.AgentRunState) ArtifactStatus {
	switch state {
	case agentcoord.AgentRunCompleted:
		return StatusCompleted
	case agentcoord.AgentRunFailed, agentcoord.AgentRunCancelled:
		return StatusFailed
	case agentcoord.AgentRunBlocked:
		return StatusBlocked
	case agentcoord.AgentRunQueued, agentcoord.AgentRunRunning:
		return StatusInProgress
	default:
		return StatusIncomplete
	}
}
