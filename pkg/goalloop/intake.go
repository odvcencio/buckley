package goalloop

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/taskstate"
)

var (
	errEmptyStatement = errors.New("goalloop: goal statement is required")
	errNoLedger       = errors.New("goalloop: run ledger is required")
	errNoCheckpoints  = errors.New("goalloop: checkpoint manager is required")
	errNoEngine       = errors.New("goalloop: turn engine is required")
)

// goalBackend labels the goal loop's runs in the ledger so status and
// list queries can tell goal runs apart from other agent runs.
const goalBackend = "goalloop"

// TaskRecord is one decomposed task after intake: its ledger identity,
// its spec, and its initial checkpoint.
type TaskRecord struct {
	TaskID       string
	Spec         TaskSpec
	CheckpointID string
}

// Intake is the durable result of Start: everything the design's one
// confirmation screen shows before the loop detaches.
type Intake struct {
	RunID      string
	GoalTaskID string
	Goal       Goal
	Tasks      []TaskRecord
}

// Start performs goal intake and decomposition (design section 5.1): it
// creates the goal's root run, records the goal as a root task.created
// event, decomposes via the Planner (or a single task when none is
// wired), records each child task with its acceptance criteria, and
// writes each task's initial checkpoint. The tree is reconstructable
// from run events alone; checkpoints add the resume path.
func (l *Loop) Start(ctx context.Context, goal Goal) (*Intake, error) {
	if err := goal.Validate(); err != nil {
		return nil, err
	}

	specs, err := l.decompose(ctx, goal)
	if err != nil {
		return nil, fmt.Errorf("goalloop: decompose: %w", err)
	}

	run, err := l.ledger.StartRun(ctx, runledger.AgentRun{
		SessionID: l.sessionID,
		Backend:   goalBackend,
		Budget:    goalBudget(goal),
	})
	if err != nil {
		return nil, fmt.Errorf("goalloop: start run: %w", err)
	}

	goalTaskID := newTaskID("goal")
	if _, err := l.ledger.Append(ctx, runledger.Event{
		Type:      runledger.EventTaskCreated,
		Timestamp: time.Now().UTC(),
		SessionID: l.sessionID,
		RunID:     run.RunID,
		TaskID:    goalTaskID,
		Payload: map[string]any{
			"kind":                "goal",
			"statement":           goal.Statement,
			"acceptance_criteria": goal.AcceptanceCriteria,
			"constraints":         goal.Constraints,
			"posture":             goal.Posture,
			"approval_mode":       goal.ApprovalMode,
		},
	}); err != nil {
		return nil, fmt.Errorf("goalloop: record goal: %w", err)
	}

	intake := &Intake{RunID: run.RunID, GoalTaskID: goalTaskID, Goal: goal}
	for i, spec := range specs {
		taskID := newTaskID(fmt.Sprintf("task-%03d", i+1))
		if _, err := l.ledger.Append(ctx, runledger.Event{
			Type:      runledger.EventTaskCreated,
			Timestamp: time.Now().UTC(),
			SessionID: l.sessionID,
			RunID:     run.RunID,
			TaskID:    taskID,
			Payload: map[string]any{
				"kind":                "task",
				"goal_task_id":        goalTaskID,
				"title":               spec.Title,
				"description":         spec.Description,
				"acceptance_criteria": spec.AcceptanceCriteria,
				"priority":            spec.Priority,
				"claims":              spec.Claims,
			},
		}); err != nil {
			return nil, fmt.Errorf("goalloop: record task %q: %w", spec.Title, err)
		}

		cp, err := l.checkpoints.Save(ctx, taskstate.SaveInput{
			State: taskstate.CheckpointState{
				Schema:  taskstate.SchemaVersion,
				TaskID:  taskID,
				GoalID:  goalTaskID,
				Status:  taskstate.StatusPending,
				Summary: spec.Title,
				NextActions: []taskstate.NextAction{
					{Text: "Start: " + spec.Title, Kind: "explore"},
				},
			},
			Reason:    taskstate.TriggerDecisionRecorded,
			SessionID: l.sessionID,
			RunID:     run.RunID,
		})
		if err != nil {
			return nil, fmt.Errorf("goalloop: initial checkpoint for %q: %w", spec.Title, err)
		}
		intake.Tasks = append(intake.Tasks, TaskRecord{
			TaskID:       taskID,
			Spec:         spec,
			CheckpointID: cp.CheckpointID,
		})
	}
	return intake, nil
}

// decompose runs the Planner, or falls back to one task carrying the
// whole goal. An empty plan is an error: a goal with no work is not
// runnable, and silently inventing a task would hide a planner defect.
func (l *Loop) decompose(ctx context.Context, goal Goal) ([]TaskSpec, error) {
	if l.planner == nil {
		return []TaskSpec{{
			Title:              goal.Statement,
			AcceptanceCriteria: goal.AcceptanceCriteria,
			Priority:           1,
		}}, nil
	}
	specs, err := l.planner.Decompose(ctx, goal)
	if err != nil {
		return nil, err
	}
	if len(specs) == 0 {
		return nil, errors.New("planner returned no tasks")
	}
	for i, spec := range specs {
		if strings.TrimSpace(spec.Title) == "" {
			return nil, fmt.Errorf("planner task %d has no title", i)
		}
	}
	return specs, nil
}

func goalBudget(goal Goal) map[string]any {
	budget := map[string]any{}
	if goal.BudgetUSD > 0 {
		budget["usd"] = goal.BudgetUSD
	}
	if goal.Posture != "" {
		budget["posture"] = goal.Posture
	}
	if !goal.Deadline.IsZero() {
		budget["deadline"] = goal.Deadline.UTC().Format(time.RFC3339)
	}
	if len(budget) == 0 {
		return nil
	}
	return budget
}

func newTaskID(label string) string {
	return label + "-" + strings.ToLower(ulid.Make().String())
}
