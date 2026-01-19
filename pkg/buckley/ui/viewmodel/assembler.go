package viewmodel

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/orchestrator"
	"github.com/odvcencio/buckley/pkg/storage"
)

const defaultMessageLimit = 120
const defaultSessionLimit = 50

// Assembler builds renderer-friendly view state snapshots.
type Assembler struct {
	store     *storage.Store
	plans     orchestrator.PlanStore
	workflow  *orchestrator.WorkflowManager
	runtime   *RuntimeStateTracker
	msgLimit  int
	sessLimit int
}

// NewAssembler constructs a view state assembler.
func NewAssembler(store *storage.Store, plans orchestrator.PlanStore, workflow *orchestrator.WorkflowManager) *Assembler {
	return &Assembler{
		store:     store,
		plans:     plans,
		workflow:  workflow,
		msgLimit:  defaultMessageLimit,
		sessLimit: defaultSessionLimit,
	}
}

// WithMessageLimit overrides the default message limit per transcript page.
func (a *Assembler) WithMessageLimit(limit int) *Assembler {
	if limit > 0 {
		a.msgLimit = limit
	}
	return a
}

// WithRuntimeTracker sets the runtime state tracker for live telemetry data.
func (a *Assembler) WithRuntimeTracker(tracker *RuntimeStateTracker) *Assembler {
	a.runtime = tracker
	return a
}

// BuildViewState returns a multi-session snapshot for renderers.
func (a *Assembler) BuildViewState(ctx context.Context) (*State, error) {
	if a.store == nil {
		return nil, nil
	}

	sessions, err := a.store.ListSessions(a.sessLimit)
	if err != nil {
		return nil, err
	}

	result := &State{
		Sessions:    make([]SessionState, 0, len(sessions)),
		GeneratedAt: time.Now(),
	}

	for _, sess := range sessions {
		state, err := a.BuildSessionState(ctx, sess.ID)
		if err != nil || state == nil {
			continue
		}
		result.Sessions = append(result.Sessions, *state)
	}

	return result, nil
}

// BuildSessionState assembles a single session snapshot.
func (a *Assembler) BuildSessionState(_ context.Context, sessionID string) (*SessionState, error) {
	if a.store == nil || sessionID == "" {
		return nil, nil
	}

	sess, err := a.store.GetSession(sessionID)
	if err != nil || sess == nil {
		return nil, err
	}

	messages, err := a.store.GetMessages(sessionID, a.msgLimit, 0)
	if err != nil {
		return nil, err
	}
	todos, _ := a.store.GetTodos(sessionID)

	var plan *PlanSnapshot
	if a.plans != nil {
		if planID, err := a.store.GetSessionPlanID(sessionID); err == nil && planID != "" {
			if loaded, err := a.plans.LoadPlan(planID); err == nil && loaded != nil {
				plan = toPlanSnapshot(loaded)
			}
		}
	}

	paused, reason, question, pauseAt := a.workflowPause()
	status := SessionStatus{
		State:        sess.Status,
		Paused:       paused,
		AwaitingUser: paused || reason != "" || question != "",
		Reason:       reason,
		Question:     question,
		LastUpdated:  pauseAt,
	}
	workflow := WorkflowStatus{
		Phase:         a.workflowPhase(),
		ActiveAgent:   a.workflowAgent(),
		Paused:        paused,
		AwaitingUser:  status.AwaitingUser,
		PauseReason:   reason,
		PauseQuestion: question,
	}
	if !pauseAt.IsZero() {
		workflow.PauseAt = &pauseAt
	}

	state := &SessionState{
		ID:         sess.ID,
		Title:      deriveTitle(sess),
		Status:     status,
		Workflow:   workflow,
		Transcript: toTranscript(messages, a.msgLimit),
		Todos:      toTodos(todos),
		Plan:       plan,
		Metrics: Metrics{
			TotalTokens: sess.TotalTokens,
			TotalCost:   sess.TotalCost,
		},
		Activity: a.workflowActivity(),
	}

	// Add runtime state from telemetry tracker
	if a.runtime != nil {
		isStreaming, tools, files, touches := a.runtime.GetRuntimeState(sess.ID)
		state.IsStreaming = isStreaming
		state.ActiveToolCalls = tools
		state.RecentFiles = files
		state.ActiveTouches = touches
	}

	return state, nil
}

func (a *Assembler) workflowPause() (bool, string, string, time.Time) {
	if a.workflow == nil {
		return false, "", "", time.Time{}
	}
	return a.workflow.GetPauseInfo()
}

func (a *Assembler) workflowPhase() string {
	if a.workflow == nil {
		return ""
	}
	return string(a.workflow.GetCurrentPhase())
}

func (a *Assembler) workflowAgent() string {
	if a.workflow == nil {
		return ""
	}
	return a.workflow.GetActiveAgent()
}

func (a *Assembler) workflowActivity() []string {
	if a.workflow == nil {
		return nil
	}
	return a.workflow.GetActivitySummaries()
}

func deriveTitle(sess *storage.Session) string {
	if sess == nil {
		return ""
	}
	for _, candidate := range []string{sess.ProjectPath, sess.GitRepo, sess.GitBranch} {
		if strings.TrimSpace(candidate) != "" {
			return candidate
		}
	}
	return sess.ID
}

func toTranscript(messages []storage.Message, limit int) TranscriptPage {
	result := TranscriptPage{
		Messages:   make([]Message, 0, len(messages)),
		HasMore:    false,
		NextOffset: len(messages),
	}
	for _, msg := range messages {
		result.Messages = append(result.Messages, Message{
			ID:          strconv.FormatInt(msg.ID, 10),
			Role:        msg.Role,
			Content:     msg.Content,
			ContentType: msg.ContentType,
			Reasoning:   msg.Reasoning,
			Tokens:      msg.Tokens,
			Timestamp:   msg.Timestamp,
			IsSummary:   msg.IsSummary,
		})
	}
	if limit > 0 && len(messages) == limit {
		result.HasMore = true
	}
	return result
}

func toTodos(items []storage.Todo) []Todo {
	result := make([]Todo, 0, len(items))
	for _, todo := range items {
		view := Todo{
			ID:         todo.ID,
			Content:    todo.Content,
			ActiveForm: todo.ActiveForm,
			Status:     todo.Status,
			Error:      strings.TrimSpace(todo.ErrorMessage),
		}
		if todo.CompletedAt != nil {
			view.CompletedAt = *todo.CompletedAt
		}
		result = append(result, view)
	}
	return result
}

func toPlanSnapshot(plan *orchestrator.Plan) *PlanSnapshot {
	if plan == nil {
		return nil
	}
	tasks := make([]PlanTask, 0, len(plan.Tasks))
	summary := TaskSummary{}
	for _, task := range plan.Tasks {
		tasks = append(tasks, PlanTask{
			ID:     task.ID,
			Title:  task.Title,
			Status: planTaskStatusLabel(task.Status),
			Type:   string(task.Type),
		})
		switch task.Status {
		case orchestrator.TaskCompleted:
			summary.Completed++
		case orchestrator.TaskFailed:
			summary.Failed++
		default:
			summary.Pending++
		}
		summary.Total++
	}

	return &PlanSnapshot{
		ID:          plan.ID,
		FeatureName: plan.FeatureName,
		Description: plan.Description,
		Tasks:       tasks,
		Progress:    summary,
	}
}

// planTaskStatusLabel converts orchestrator task statuses into stable string labels for the UI.
func planTaskStatusLabel(status orchestrator.TaskStatus) string {
	switch status {
	case orchestrator.TaskPending:
		return "pending"
	case orchestrator.TaskInProgress:
		return "in_progress"
	case orchestrator.TaskCompleted:
		return "completed"
	case orchestrator.TaskFailed:
		return "failed"
	case orchestrator.TaskSkipped:
		return "skipped"
	default:
		return "pending"
	}
}
