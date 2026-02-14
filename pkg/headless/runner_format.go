package headless

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/orchestrator"
)

func formatPlanSummary(plan *orchestrator.Plan, cfg *config.Config) string {
	if plan == nil {
		return "Plan unavailable."
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("✓ Created plan: %s\n", plan.FeatureName))
	b.WriteString(fmt.Sprintf("Plan ID: %s\n", plan.ID))
	b.WriteString(fmt.Sprintf("Tasks: %d\n", len(plan.Tasks)))

	if cfg != nil && strings.TrimSpace(cfg.Artifacts.PlanningDir) != "" {
		base := strings.TrimRight(cfg.Artifacts.PlanningDir, string(filepath.Separator))
		b.WriteString(fmt.Sprintf("Plan file: %s\n", filepath.Join(base, plan.ID+".md")))
	}

	b.WriteString("\nTasks:\n")
	for i, task := range plan.Tasks {
		b.WriteString(fmt.Sprintf("  %d. %s\n", i+1, task.Title))
	}
	return b.String()
}

func planTaskStatus(status orchestrator.TaskStatus) string {
	switch status {
	case orchestrator.TaskPending:
		return "[ ]"
	case orchestrator.TaskInProgress:
		return "[→]"
	case orchestrator.TaskCompleted:
		return "[✓]"
	case orchestrator.TaskFailed:
		return "[✗]"
	case orchestrator.TaskSkipped:
		return "[-]"
	default:
		return "[?]"
	}
}

func formatWorkflowStatus(wf *orchestrator.WorkflowManager) string {
	if wf == nil {
		return "Workflow manager not initialized."
	}
	phase := string(wf.GetCurrentPhase())
	if phase == "" {
		phase = "unknown"
	}
	agent := wf.GetActiveAgent()
	if strings.TrimSpace(agent) == "" {
		agent = "N/A"
	}

	var b strings.Builder
	b.WriteString("Workflow Status\n")
	b.WriteString(fmt.Sprintf("  Phase: %s\n", phase))
	b.WriteString(fmt.Sprintf("  Active Agent: %s\n", agent))

	if paused, reason, question, at := wf.GetPauseInfo(); paused {
		if reason == "" {
			reason = "Awaiting user input"
		}
		if question == "" {
			question = "Confirm how to proceed"
		}
		when := ""
		if !at.IsZero() {
			when = fmt.Sprintf(" (since %s)", at.Format("15:04:05"))
		}
		b.WriteString(fmt.Sprintf("  Status: PAUSED%s\n", when))
		b.WriteString(fmt.Sprintf("    Reason: %s\n", reason))
		b.WriteString(fmt.Sprintf("    Action: %s\n", question))
	} else {
		b.WriteString("  Status: Running\n")
	}

	return b.String()
}

func formatWorkflowPhases(phases []orchestrator.TaskPhase) string {
	if len(phases) == 0 {
		return "No task phases configured."
	}
	var b strings.Builder
	b.WriteString("Task Phases:\n")
	for _, phase := range phases {
		b.WriteString(fmt.Sprintf("- %s (%s)\n", phase.Title(), phase.Stage))
		desc := strings.TrimSpace(phase.Description)
		if desc != "" {
			b.WriteString(fmt.Sprintf("    • %s\n", desc))
		}
		if len(phase.Targets) > 0 {
			for _, target := range phase.Targets {
				b.WriteString(fmt.Sprintf("    → %s\n", target))
			}
		}
	}
	return b.String()
}

func truncateOutput(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// getMessageContent extracts string content from a message content field.
func getMessageContent(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []model.ContentPart:
		var texts []string
		for _, part := range v {
			if part.Type == "text" && part.Text != "" {
				texts = append(texts, part.Text)
			}
		}
		return strings.Join(texts, "\n")
	default:
		return fmt.Sprintf("%v", v)
	}
}
