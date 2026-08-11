package artifactv1

import (
	"fmt"
	"strconv"
	"strings"

	"m31labs.dev/buckley/pkg/agentcoord"
	legacy "m31labs.dev/buckley/pkg/artifact"
	"m31labs.dev/buckley/pkg/goalloop"
)

// FromPlanning converts the legacy planning artifact into the typed v1
// contract. It is an adapter, not a change to legacy generators, so existing
// on-disk planning workflows remain compatible during rollout.
func FromPlanning(source *legacy.PlanningArtifact) (Artifact, error) {
	if source == nil {
		return Artifact{}, fmt.Errorf("migrate planning artifact: source is required")
	}
	summary := source.Context.UserGoal
	if summary == "" {
		summary = "planning artifact for " + source.Feature
	}
	result := Artifact{
		SchemaVersion: SchemaVersion,
		Kind:          KindPlan,
		Status:        legacyStatus(source.Status),
		Title:         "Plan: " + source.Feature,
		Summary:       summary,
		Metadata:      legacyMetadata(source.FilePath),
	}
	if len(source.Context.ExistingPatterns) > 0 || source.Context.ArchitectureStyle != "" {
		facts := make([]Fact, 0, len(source.Context.ExistingPatterns)+1)
		if source.Context.ArchitectureStyle != "" {
			facts = append(facts, Fact{Label: "Architecture", Value: source.Context.ArchitectureStyle})
		}
		for index, pattern := range source.Context.ExistingPatterns {
			facts = append(facts, Fact{Label: fmt.Sprintf("Pattern %d", index+1), Value: pattern})
		}
		result.Blocks = append(result.Blocks, Block{Kind: BlockFacts, Facts: facts})
	}
	if len(source.Tasks) > 0 {
		checklist := make([]ChecklistItem, 0, len(source.Tasks))
		for _, task := range source.Tasks {
			checklist = append(checklist, ChecklistItem{Text: task.Description, State: "pending", Detail: task.FilePath})
		}
		result.Blocks = append(result.Blocks, Block{Kind: BlockChecklist, Checklist: checklist})
	}
	for index, decision := range source.Decisions {
		text := decision.Rationale
		if text == "" {
			text = decision.Title
		}
		result.Blocks = append(result.Blocks, Block{Kind: BlockProse, Text: "Decision: " + decision.Title + "\n" + text})
		_ = index
	}
	for index, contract := range source.CodeContracts {
		if strings.TrimSpace(contract.Code) == "" {
			continue
		}
		result.Blocks = append(result.Blocks,
			Block{Kind: BlockHeading, Text: fmt.Sprintf("Contract %d: %s", index+1, contract.Description), Level: 2},
			Block{Kind: BlockCode, Code: &CodeBlock{Language: "go", Content: contract.Code}},
		)
	}
	for index, risk := range source.Context.ResearchRisks {
		result.Findings = append(result.Findings, Finding{
			ID:         fmt.Sprintf("research-risk-%d", index+1),
			Severity:   "medium",
			Confidence: 0.5,
			Title:      "Research risk",
			Summary:    risk,
		})
	}
	return NormalizeAndValidate(result)
}

// FromExecution converts legacy execution progress into typed operation and
// checklist blocks while preserving the old artifact's path in metadata.
func FromExecution(source *legacy.ExecutionArtifact) (Artifact, error) {
	if source == nil {
		return Artifact{}, fmt.Errorf("migrate execution artifact: source is required")
	}
	summary := fmt.Sprintf("execution progress: task %d of %d", source.CurrentTask, source.TotalTasks)
	result := Artifact{
		SchemaVersion: SchemaVersion,
		Kind:          KindExecution,
		Status:        legacyStatus(source.Status),
		Title:         "Execution: " + source.Feature,
		Summary:       summary,
		Metadata:      legacyMetadata(source.FilePath),
	}
	if source.PlanningArtifactPath != "" {
		result.Metadata["planning_artifact_path"] = source.PlanningArtifactPath
	}
	for _, progress := range source.ProgressLog {
		metrics := []Fact{{Label: "Task", Value: strconv.Itoa(progress.TaskID)}}
		if progress.Duration != "" {
			metrics = append(metrics, Fact{Label: "Duration", Value: progress.Duration})
		}
		result.Blocks = append(result.Blocks, Block{Kind: BlockOperationSummary, Operation: &OperationSummary{
			Operation: progress.Description,
			Status:    legacyOperationStatus(progress.Status),
			Detail:    progress.ImplementationNotes,
			Metrics:   metrics,
		}})
		if progress.CodeSnippet != "" {
			result.Blocks = append(result.Blocks, Block{Kind: BlockCode, Code: &CodeBlock{Language: "go", Content: progress.CodeSnippet}})
		}
	}
	if len(source.ReviewChecklist) > 0 {
		items := make([]ChecklistItem, 0, len(source.ReviewChecklist))
		for _, item := range source.ReviewChecklist {
			items = append(items, ChecklistItem{Text: item, State: "pending"})
		}
		result.Blocks = append(result.Blocks, Block{Kind: BlockChecklist, Checklist: items})
	}
	for index, deviation := range source.DeviationSummary {
		result.Findings = append(result.Findings, Finding{
			ID:             fmt.Sprintf("deviation-%d", index+1),
			Severity:       legacyImpactSeverity(deviation.Impact),
			Confidence:     0.8,
			Title:          deviation.Type + " deviation",
			Summary:        deviation.Description,
			Recommendation: deviation.Rationale,
		})
	}
	return NormalizeAndValidate(result)
}

// FromReview converts legacy review issues into top-level typed findings. The
// resulting artifact can immediately render as SARIF without parsing Markdown.
func FromReview(source *legacy.ReviewArtifact) (Artifact, error) {
	if source == nil {
		return Artifact{}, fmt.Errorf("migrate review artifact: source is required")
	}
	summary := "review artifact"
	if source.Approval != nil && source.Approval.Summary != "" {
		summary = source.Approval.Summary
	}
	result := Artifact{
		SchemaVersion: SchemaVersion,
		Kind:          KindReview,
		Status:        legacyReviewStatus(source.Status),
		Title:         "Review: " + source.Feature,
		Summary:       summary,
		Metadata:      legacyMetadata(source.FilePath),
	}
	if source.ReviewerModel != "" {
		result.Metadata["reviewer_model"] = source.ReviewerModel
	}
	if source.PlanningArtifactPath != "" {
		result.Metadata["planning_artifact_path"] = source.PlanningArtifactPath
	}
	if source.ExecutionArtifactPath != "" {
		result.Metadata["execution_artifact_path"] = source.ExecutionArtifactPath
	}
	for index, issue := range source.IssuesFound {
		finding := Finding{
			ID:             fmt.Sprintf("review-issue-%d", index+1),
			Severity:       legacyIssueSeverity(issue.Severity),
			Confidence:     0.8,
			Title:          issue.Title,
			Summary:        issue.Description,
			Recommendation: issue.Fix,
		}
		if issue.Location != "" {
			finding.Location = &Location{Path: issue.Location}
		}
		result.Findings = append(result.Findings, finding)
	}
	for index, improvement := range source.OpportunisticImprovements {
		result.NextActions = append(result.NextActions, NextAction{
			ID:          fmt.Sprintf("improvement-%d", index+1),
			Description: improvement.Suggestion,
			Priority:    improvement.Impact,
		})
	}
	return NormalizeAndValidate(result)
}

// FromGoalReport adapts the durable morning report into the artifact IR. It
// carries evidence IDs forward and marks parked work explicitly incomplete.
func FromGoalReport(source goalloop.Report) (Artifact, error) {
	title := "Goal"
	if source.Statement != "" {
		title += ": " + source.Statement
	}
	summary := source.Status
	if source.SpentUSD > 0 || source.BudgetUSD > 0 {
		summary = fmt.Sprintf("%s; spent $%.2f", source.Status, source.SpentUSD)
		if source.BudgetUSD > 0 {
			summary += fmt.Sprintf(" of $%.2f", source.BudgetUSD)
		}
	}
	result := Artifact{
		SchemaVersion: SchemaVersion,
		Kind:          KindGoal,
		Status:        goalStatus(source.Status),
		Title:         title,
		Summary:       summary,
		Metadata: map[string]string{
			"goal_run_id": source.RunID,
		},
	}
	if len(source.Spend) > 0 {
		table := Table{Headers: []string{"Task", "Spent USD"}}
		for _, spend := range source.Spend {
			title := spend.Title
			if title == "" {
				title = spend.TaskID
			}
			table.Rows = append(table.Rows, []string{title, fmt.Sprintf("%.2f", spend.USD)})
		}
		result.Blocks = append(result.Blocks, Block{Kind: BlockTable, Table: &table})
	}
	for index, completed := range source.Completed {
		evidence := EvidenceRef{}
		if validIdentifier(completed.EvidenceID) {
			evidence = EvidenceRef{ID: completed.EvidenceID, Label: completed.Text}
			result.EvidenceRefs = append(result.EvidenceRefs, evidence)
		}
		result.Blocks = append(result.Blocks, Block{Kind: BlockChecklist, Checklist: []ChecklistItem{{Text: completed.TaskID + ": " + completed.Text, State: "completed"}}})
		_ = index
	}
	for index, parked := range source.Parked {
		result.Findings = append(result.Findings, Finding{
			ID:             fmt.Sprintf("parked-%d", index+1),
			Severity:       "medium",
			Confidence:     1,
			Title:          parked.Title,
			Summary:        parked.Reason,
			Recommendation: parked.Needs,
		})
		if parked.Reason != "" {
			result.IncompleteReasons = append(result.IncompleteReasons, parked.Reason)
		}
	}
	for index, action := range source.NextActions {
		result.NextActions = append(result.NextActions, NextAction{ID: fmt.Sprintf("goal-action-%d", index+1), Description: action.Text, Priority: action.TaskID})
	}
	return NormalizeAndValidate(result)
}

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

func legacyMetadata(path string) map[string]string {
	metadata := make(map[string]string)
	if path != "" {
		metadata["legacy_path"] = path
	}
	return metadata
}

func legacyStatus(status string) ArtifactStatus {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "approved":
		return StatusCompleted
	case "in_progress", "in-progress", "pending":
		return StatusInProgress
	case "failed", "changes_requested":
		return StatusFailed
	case "blocked", "parked":
		return StatusBlocked
	case "draft":
		return StatusDraft
	default:
		return StatusIncomplete
	}
}

func legacyReviewStatus(status string) ArtifactStatus {
	if strings.EqualFold(status, "approved") || strings.EqualFold(status, "approved_with_nits") {
		return StatusCompleted
	}
	return legacyStatus(status)
}

func legacyOperationStatus(status string) string {
	if status = strings.TrimSpace(status); status != "" {
		return status
	}
	return "unknown"
}

func legacyImpactSeverity(impact string) string {
	switch strings.ToLower(strings.TrimSpace(impact)) {
	case "high":
		return "high"
	case "medium":
		return "medium"
	default:
		return "low"
	}
}

func legacyIssueSeverity(severity string) string {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "critical", "high":
		return "critical"
	case "quality", "medium":
		return "medium"
	case "low", "nit", "info":
		return "low"
	default:
		return "medium"
	}
}

func goalStatus(status string) ArtifactStatus {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed":
		return StatusCompleted
	case "parked":
		return StatusBlocked
	case "pending":
		return StatusInProgress
	case "partial":
		return StatusIncomplete
	default:
		return StatusIncomplete
	}
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
