package taskstate

import (
	"fmt"
	"strings"
)

// RenderMarkdown produces the checkpoint's Markdown++ view (section 15.3;
// the morning report in the goal-loop design renders the same shape).
// Output is deterministic for a given state: same sections, same order.
// The JSON state stays canonical; this view is for humans and for the
// evidence store (kind "checkpoint").
func RenderMarkdown(s CheckpointState) string {
	var b strings.Builder

	b.WriteString("---\n")
	b.WriteString("type: buckley-task-checkpoint\n")
	fmt.Fprintf(&b, "schema: %s\n", SchemaVersion)
	fmt.Fprintf(&b, "task_id: %s\n", s.TaskID)
	if s.GoalID != "" {
		fmt.Fprintf(&b, "goal_id: %s\n", s.GoalID)
	}
	fmt.Fprintf(&b, "status: %s\n", s.Status)
	if s.Spend.BudgetUSD > 0 {
		fmt.Fprintf(&b, "spend_usd: %.2f / %.2f\n", s.Spend.USD, s.Spend.BudgetUSD)
	} else if s.Spend.USD > 0 {
		fmt.Fprintf(&b, "spend_usd: %.2f\n", s.Spend.USD)
	}
	b.WriteString("---\n")

	if strings.TrimSpace(s.Summary) != "" {
		b.WriteString("\n" + strings.TrimSpace(s.Summary) + "\n")
	}

	if len(s.Completed) > 0 {
		b.WriteString("\n# Completed (evidence-linked)\n")
		for _, item := range s.Completed {
			if item.EvidenceID != "" {
				fmt.Fprintf(&b, "- [x] %s (`%s`)\n", item.Text, item.EvidenceID)
			} else {
				fmt.Fprintf(&b, "- [x] %s (unverified)\n", item.Text)
			}
		}
	}

	if len(s.Checks) > 0 {
		b.WriteString("\n# Verification\n")
		b.WriteString("| Check | Scope | Status | Evidence |\n")
		b.WriteString("|---|---|---:|---|\n")
		for _, v := range s.Checks {
			evidence := "—"
			if v.EvidenceID != "" {
				evidence = "`" + v.EvidenceID + "`"
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", v.Check, v.Scope, v.Status, evidence)
		}
	}

	if s.Blocker != nil {
		b.WriteString("\n# Parked\n")
		line := fmt.Sprintf("- blocked: %s", s.Blocker.Reason)
		if s.Blocker.Needs != "" {
			line += " — needs: " + s.Blocker.Needs
		}
		if s.Blocker.RetryAfter != nil {
			line += " — retry after " + s.Blocker.RetryAfter.UTC().Format("2006-01-02T15:04:05Z")
		}
		b.WriteString(line + "\n")
	}

	if len(s.Questions) > 0 {
		b.WriteString("\n# Questions for you\n")
		for i, q := range s.Questions {
			line := fmt.Sprintf("%d. %s", i+1, q.Text)
			if len(q.BlockingTasks) > 0 {
				line += fmt.Sprintf(" (blocks %s)", strings.Join(q.BlockingTasks, ", "))
			}
			b.WriteString(line + "\n")
		}
	}

	if len(s.NextActions) > 0 {
		b.WriteString("\n# Next actions\n")
		for i, a := range s.NextActions {
			line := fmt.Sprintf("%d. %s", i+1, a.Text)
			if a.Kind != "" {
				line += " [" + a.Kind + "]"
			}
			b.WriteString(line + "\n")
		}
	}

	if len(s.Files) > 0 {
		b.WriteString("\n# Files\n")
		for _, f := range s.Files {
			b.WriteString("- " + f + "\n")
		}
	}

	return b.String()
}
