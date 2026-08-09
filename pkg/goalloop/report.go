package goalloop

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/taskstate"
)

// Report is the goal's morning report (design 7.3): the roll-up of every
// task's latest checkpoint plus the ledger's spend samples. It is built
// from durable state only, so it renders identically after a crash, a
// resume, or on a machine that never ran the goal.
type Report struct {
	RunID     string
	Statement string
	// Status is completed when every task completed, partial when some
	// did, and parked when nothing completed and at least one task is
	// parked or blocked; pending otherwise.
	Status    string
	SpentUSD  float64
	BudgetUSD float64

	Completed    []ReportCompleted
	Verification []ReportCheck
	Parked       []ReportParked
	Questions    []taskstate.Question
	Spend        []ReportSpend
	NextActions  []ReportAction
}

// ReportCompleted is one evidence-linked completed task.
type ReportCompleted struct {
	TaskID     string
	Text       string
	EvidenceID string
}

// ReportCheck is one verification row with its owning task.
type ReportCheck struct {
	TaskID string
	taskstate.VerificationEntry
}

// ReportParked is one parked or blocked task with its reason.
// AwaitingApproval is set when the task's durable workflow is holding
// an unresolved approval wait, so the report can point the operator at
// `buckley goal approve`.
type ReportParked struct {
	TaskID           string
	Title            string
	Reason           string
	Needs            string
	AwaitingApproval bool
}

// ReportSpend is one task's dollar total.
type ReportSpend struct {
	TaskID string
	Title  string
	USD    float64
}

// ReportAction is one pending next action with its owning task.
type ReportAction struct {
	TaskID string
	Text   string
}

// Report assembles the morning report for a goal run from the ledger and
// each task's latest checkpoint.
func (l *Loop) Report(ctx context.Context, runID string) (Report, error) {
	events, err := l.ledger.ListEvents(ctx, runledger.EventQuery{RunID: runID})
	if err != nil {
		return Report{}, fmt.Errorf("goalloop: list events: %w", err)
	}
	state, err := runledger.MaterializeRun(runID, events)
	if err != nil {
		return Report{}, fmt.Errorf("goalloop: materialize run: %w", err)
	}

	report := Report{RunID: runID}
	spendByTask, err := l.ledger.SumMetricByTask(ctx, runID, costUSDMetric)
	if err != nil {
		return Report{}, fmt.Errorf("goalloop: spend by task: %w", err)
	}

	// A parked task whose durable workflow holds an unresolved approval
	// wait is actionable, not stuck; the report surfaces it.
	awaitingApproval := map[string]string{}
	for _, ev := range events {
		instance := payloadString(ev.Payload, "workflow_instance_id")
		switch ev.Type {
		case runledger.EventDurableApprovalWaiting:
			awaitingApproval[ev.TaskID] = instance
		case runledger.EventDurableApprovalResolved:
			if awaitingApproval[ev.TaskID] == instance {
				delete(awaitingApproval, ev.TaskID)
			}
		}
	}

	type taskRow struct {
		id      string
		title   string
		status  string
		seq     int64
		state   *taskstate.CheckpointState
		blocker *taskstate.Blocker
	}
	var tasks []taskRow

	for taskID, task := range state.Tasks {
		created, ok := firstCreated(task.Events)
		if !ok {
			continue
		}
		switch payloadString(created.Payload, "kind") {
		case "goal":
			report.Statement = payloadString(created.Payload, "statement")
			continue
		case "task":
		default:
			continue
		}

		row := taskRow{
			id:     taskID,
			title:  payloadString(created.Payload, "title"),
			status: taskstate.StatusPending,
			seq:    created.Sequence,
		}
		if resumed, err := l.checkpoints.Resume(ctx, taskID); err == nil {
			cpState := resumed.State
			row.state = &cpState
			row.status = cpState.Status
			row.blocker = cpState.Blocker
		} else if !errors.Is(err, taskstate.ErrNoCheckpoint) {
			return Report{}, fmt.Errorf("goalloop: resume %s: %w", taskID, err)
		}
		tasks = append(tasks, row)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].seq < tasks[j].seq })

	var completedCount, parkedCount int
	for _, task := range tasks {
		report.Spend = append(report.Spend, ReportSpend{TaskID: task.id, Title: task.title, USD: spendByTask[task.id]})
		report.SpentUSD += spendByTask[task.id]
		if task.state == nil {
			continue
		}
		for _, item := range task.state.Completed {
			report.Completed = append(report.Completed, ReportCompleted{TaskID: task.id, Text: item.Text, EvidenceID: item.EvidenceID})
		}
		for _, check := range task.state.Checks {
			report.Verification = append(report.Verification, ReportCheck{TaskID: task.id, VerificationEntry: check})
		}
		report.Questions = append(report.Questions, task.state.Questions...)
		switch task.status {
		case taskstate.StatusCompleted:
			completedCount++
		case taskstate.StatusParked, taskstate.StatusBlocked:
			parkedCount++
			parked := ReportParked{TaskID: task.id, Title: task.title}
			if task.blocker != nil {
				parked.Reason = task.blocker.Reason
				parked.Needs = task.blocker.Needs
			}
			if _, waiting := awaitingApproval[task.id]; waiting {
				parked.AwaitingApproval = true
			}
			report.Parked = append(report.Parked, parked)
		}
		if task.status != taskstate.StatusCompleted {
			for _, action := range task.state.NextActions {
				report.NextActions = append(report.NextActions, ReportAction{TaskID: task.id, Text: action.Text})
			}
		}
	}

	switch {
	case len(tasks) > 0 && completedCount == len(tasks):
		report.Status = "completed"
	case completedCount > 0:
		report.Status = "partial"
	case parkedCount > 0:
		report.Status = "parked"
	default:
		report.Status = "pending"
	}

	if budget, ok := runBudgetUSD(ctx, l.ledger, runID); ok {
		report.BudgetUSD = budget
	}
	return report, nil
}

// runBudgetUSD reads the goal's dollar ceiling back from the run row.
func runBudgetUSD(ctx context.Context, ledger runledger.Store, runID string) (float64, bool) {
	run, err := ledger.GetRun(ctx, runID)
	if err != nil || run.Budget == nil {
		return 0, false
	}
	switch v := run.Budget["usd"].(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	}
	return 0, false
}

// RenderReport renders the Markdown++ morning report (design 7.3).
// Deterministic for a given report; the caller stores it as evidence or
// prints it.
func RenderReport(r Report) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("type: buckley-goal-report\n")
	fmt.Fprintf(&b, "schema: %s\n", taskstate.SchemaVersion)
	fmt.Fprintf(&b, "run_id: %s\n", r.RunID)
	fmt.Fprintf(&b, "status: %s\n", r.Status)
	if r.BudgetUSD > 0 {
		fmt.Fprintf(&b, "spend_usd: %.2f / %.2f\n", r.SpentUSD, r.BudgetUSD)
	} else if r.SpentUSD > 0 {
		fmt.Fprintf(&b, "spend_usd: %.2f\n", r.SpentUSD)
	}
	b.WriteString("---\n")

	if r.Statement != "" {
		b.WriteString("\n# Goal\n" + r.Statement + "\n")
	}

	if len(r.Completed) > 0 {
		b.WriteString("\n# Completed (evidence-linked)\n")
		for _, item := range r.Completed {
			if item.EvidenceID != "" {
				fmt.Fprintf(&b, "- [x] %s — %s (`%s`)\n", item.TaskID, item.Text, item.EvidenceID)
			} else {
				fmt.Fprintf(&b, "- [x] %s — %s (unverified)\n", item.TaskID, item.Text)
			}
		}
	}

	if len(r.Verification) > 0 {
		b.WriteString("\n# Verification\n")
		b.WriteString("| Check | Scope | Status | Evidence |\n")
		b.WriteString("|---|---|---|---|\n")
		for _, check := range r.Verification {
			evidence := "—"
			if check.EvidenceID != "" {
				evidence = "`" + check.EvidenceID + "`"
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", check.Check, check.Scope, check.Status, evidence)
		}
	}

	if len(r.Parked) > 0 {
		b.WriteString("\n# Parked\n")
		for _, parked := range r.Parked {
			line := fmt.Sprintf("- %s blocked: %s", parked.Title, parked.Reason)
			if parked.Needs != "" {
				line += " — needs: " + parked.Needs
			}
			if parked.AwaitingApproval {
				line += fmt.Sprintf(" — awaiting approval: buckley goal approve --task %s %s", parked.TaskID, r.RunID)
			}
			b.WriteString(line + "\n")
		}
	}

	if len(r.Questions) > 0 {
		b.WriteString("\n# Questions for you\n")
		for i, q := range r.Questions {
			line := fmt.Sprintf("%d. %s", i+1, q.Text)
			if len(q.BlockingTasks) > 0 {
				line += fmt.Sprintf(" (blocks %s)", strings.Join(q.BlockingTasks, ", "))
			}
			b.WriteString(line + "\n")
		}
	}

	if len(r.Spend) > 0 && r.SpentUSD > 0 {
		b.WriteString("\n# Spend by node\n")
		for _, spend := range r.Spend {
			fmt.Fprintf(&b, "- %s $%.2f\n", spend.Title, spend.USD)
		}
	}

	if len(r.NextActions) > 0 {
		b.WriteString("\n# Next actions\n")
		for i, action := range r.NextActions {
			fmt.Fprintf(&b, "%d. %s\n", i+1, action.Text)
		}
	}
	return b.String()
}
