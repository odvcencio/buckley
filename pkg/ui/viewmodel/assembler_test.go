package viewmodel

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/orchestrator"
	"github.com/odvcencio/buckley/pkg/storage"
)

func TestToPlanSnapshotNormalizesStatuses(t *testing.T) {
	plan := &orchestrator.Plan{
		ID:          "plan-123",
		FeatureName: "Feature",
		Description: "Exercise status labeling",
		Tasks: []orchestrator.Task{
			{ID: "1", Title: "pending task", Status: orchestrator.TaskPending, Type: orchestrator.TaskTypeImplementation},
			{ID: "2", Title: "in progress task", Status: orchestrator.TaskInProgress, Type: orchestrator.TaskTypeAnalysis},
			{ID: "3", Title: "done task", Status: orchestrator.TaskCompleted, Type: orchestrator.TaskTypeValidation},
			{ID: "4", Title: "failed task", Status: orchestrator.TaskFailed, Type: orchestrator.TaskTypeImplementation},
			{ID: "5", Title: "skipped task", Status: orchestrator.TaskSkipped, Type: orchestrator.TaskTypeAnalysis},
		},
	}

	snapshot := toPlanSnapshot(plan)
	if snapshot == nil {
		t.Fatalf("expected snapshot to be created")
	}
	if snapshot.FeatureName != plan.FeatureName || snapshot.Description != plan.Description {
		t.Fatalf("plan metadata not copied correctly: %+v", snapshot)
	}

	wantStatuses := []string{"pending", "in_progress", "completed", "failed", "skipped"}
	if len(snapshot.Tasks) != len(wantStatuses) {
		t.Fatalf("expected %d tasks, got %d", len(wantStatuses), len(snapshot.Tasks))
	}
	for i, want := range wantStatuses {
		if snapshot.Tasks[i].Status != want {
			t.Errorf("task %d status mismatch: want %q got %q", i, want, snapshot.Tasks[i].Status)
		}
	}

	progress := snapshot.Progress
	if progress.Completed != 1 || progress.Failed != 1 || progress.Pending != 3 || progress.Total != len(wantStatuses) {
		t.Fatalf("unexpected progress summary: %+v", progress)
	}
}

func TestDeriveTitle(t *testing.T) {
	tests := []struct {
		name     string
		session  *storage.Session
		expected string
	}{
		{
			name:     "nil session",
			session:  nil,
			expected: "",
		},
		{
			name: "project path takes precedence",
			session: &storage.Session{
				ID:          "sess-123",
				ProjectPath: "/path/to/project",
				GitRepo:     "repo",
				GitBranch:   "main",
			},
			expected: "/path/to/project",
		},
		{
			name: "git repo fallback",
			session: &storage.Session{
				ID:          "sess-123",
				ProjectPath: "",
				GitRepo:     "github.com/user/repo",
				GitBranch:   "main",
			},
			expected: "github.com/user/repo",
		},
		{
			name: "git branch fallback",
			session: &storage.Session{
				ID:          "sess-123",
				ProjectPath: "",
				GitRepo:     "",
				GitBranch:   "feature-branch",
			},
			expected: "feature-branch",
		},
		{
			name: "session ID fallback",
			session: &storage.Session{
				ID:          "sess-fallback",
				ProjectPath: "",
				GitRepo:     "",
				GitBranch:   "",
			},
			expected: "sess-fallback",
		},
		{
			name: "whitespace only treated as empty",
			session: &storage.Session{
				ID:          "sess-ws",
				ProjectPath: "   ",
				GitRepo:     "  ",
				GitBranch:   "valid",
			},
			expected: "valid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deriveTitle(tt.session)
			if got != tt.expected {
				t.Errorf("deriveTitle() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestToTranscript(t *testing.T) {
	now := time.Now()
	messages := []storage.Message{
		{ID: 1, Role: "user", Content: "hello", Timestamp: now},
		{ID: 2, Role: "assistant", Content: "hi there", Tokens: 5, Timestamp: now},
		{ID: 3, Role: "system", Content: "context", IsSummary: true, Timestamp: now},
	}

	// Test without limit
	page := toTranscript(messages, 0)
	if len(page.Messages) != 3 {
		t.Errorf("expected 3 messages, got %d", len(page.Messages))
	}
	if page.HasMore {
		t.Error("expected HasMore to be false without limit")
	}

	// Test with limit equal to message count
	page = toTranscript(messages, 3)
	if !page.HasMore {
		t.Error("expected HasMore to be true when limit equals count")
	}

	// Test message fields
	if page.Messages[0].Role != "user" {
		t.Errorf("expected role 'user', got %q", page.Messages[0].Role)
	}
	if page.Messages[1].Tokens != 5 {
		t.Errorf("expected tokens 5, got %d", page.Messages[1].Tokens)
	}
	if !page.Messages[2].IsSummary {
		t.Error("expected IsSummary to be true")
	}
}

func TestToTodos(t *testing.T) {
	completedAt := time.Now()
	items := []storage.Todo{
		{ID: 1, Content: "Task 1", ActiveForm: "doing task 1", Status: "pending"},
		{ID: 2, Content: "Task 2", ActiveForm: "doing task 2", Status: "completed", CompletedAt: &completedAt},
		{ID: 3, Content: "Task 3", ActiveForm: "doing task 3", Status: "failed", ErrorMessage: "  error occurred  "},
	}

	todos := toTodos(items)
	if len(todos) != 3 {
		t.Fatalf("expected 3 todos, got %d", len(todos))
	}

	// Check first todo
	if todos[0].Content != "Task 1" || todos[0].Status != "pending" {
		t.Errorf("first todo mismatch: %+v", todos[0])
	}

	// Check completed todo has completion time
	if todos[1].CompletedAt.IsZero() {
		t.Error("expected completed todo to have completion time")
	}

	// Check error message is trimmed
	if todos[2].Error != "error occurred" {
		t.Errorf("expected trimmed error, got %q", todos[2].Error)
	}
}

func TestPlanTaskStatusLabel(t *testing.T) {
	tests := []struct {
		name     string
		status   orchestrator.TaskStatus
		expected string
	}{
		{"pending", orchestrator.TaskPending, "pending"},
		{"in_progress", orchestrator.TaskInProgress, "in_progress"},
		{"completed", orchestrator.TaskCompleted, "completed"},
		{"failed", orchestrator.TaskFailed, "failed"},
		{"skipped", orchestrator.TaskSkipped, "skipped"},
		{"unknown_defaults_to_pending", orchestrator.TaskStatus(99), "pending"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := planTaskStatusLabel(tt.status)
			if got != tt.expected {
				t.Errorf("planTaskStatusLabel(%v) = %q, want %q", tt.status, got, tt.expected)
			}
		})
	}
}

func TestNewAssembler(t *testing.T) {
	a := NewAssembler(nil, nil, nil)
	if a == nil {
		t.Fatal("expected non-nil assembler")
	}
	if a.msgLimit != defaultMessageLimit {
		t.Errorf("expected default message limit %d, got %d", defaultMessageLimit, a.msgLimit)
	}
	if a.sessLimit != defaultSessionLimit {
		t.Errorf("expected default session limit %d, got %d", defaultSessionLimit, a.sessLimit)
	}
}

func TestWithMessageLimit(t *testing.T) {
	a := NewAssembler(nil, nil, nil)

	// Valid limit
	a = a.WithMessageLimit(50)
	if a.msgLimit != 50 {
		t.Errorf("expected limit 50, got %d", a.msgLimit)
	}

	// Zero limit should not change
	a = a.WithMessageLimit(0)
	if a.msgLimit != 50 {
		t.Errorf("expected limit to stay 50, got %d", a.msgLimit)
	}

	// Negative limit should not change
	a = a.WithMessageLimit(-10)
	if a.msgLimit != 50 {
		t.Errorf("expected limit to stay 50, got %d", a.msgLimit)
	}
}

func TestToPlanSnapshot_Nil(t *testing.T) {
	snapshot := toPlanSnapshot(nil)
	if snapshot != nil {
		t.Error("expected nil snapshot for nil plan")
	}
}

func TestBuildViewState_NilStore(t *testing.T) {
	a := NewAssembler(nil, nil, nil)
	state, err := a.BuildViewState(context.Background())
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if state != nil {
		t.Error("expected nil state for nil store")
	}
}

func TestBuildSessionState_NilStore(t *testing.T) {
	a := NewAssembler(nil, nil, nil)
	state, err := a.BuildSessionState(context.Background(), "sess-123")
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if state != nil {
		t.Error("expected nil state for nil store")
	}
}

func TestBuildSessionState_EmptySessionID(t *testing.T) {
	// Create a temp store for testing
	dir := t.TempDir()
	store, err := storage.New(dir + "/test.db")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	a := NewAssembler(store, nil, nil)
	state, err := a.BuildSessionState(context.Background(), "")
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if state != nil {
		t.Error("expected nil state for empty session ID")
	}
}

func TestBuildSessionState_SessionNotFound(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.New(dir + "/test.db")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	a := NewAssembler(store, nil, nil)
	state, err := a.BuildSessionState(context.Background(), "nonexistent-session")
	// GetSession returns nil, nil for not found
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if state != nil {
		t.Error("expected nil state for nonexistent session")
	}
}

func TestBuildSessionState_FullSession(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.New(dir + "/test.db")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Create a session
	sess := &storage.Session{
		ID:          "sess-test-full",
		ProjectPath: "/path/to/project",
		GitRepo:     "github.com/test/repo",
		GitBranch:   "main",
		Status:      "active",
		TotalTokens: 1000,
		TotalCost:   0.05,
	}
	if err := store.CreateSession(sess); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	// Add some messages
	now := time.Now()
	messages := []storage.Message{
		{SessionID: sess.ID, Role: "user", Content: "hello", Timestamp: now, Tokens: 5},
		{SessionID: sess.ID, Role: "assistant", Content: "hi there", Timestamp: now.Add(time.Second), Tokens: 10},
	}
	for _, msg := range messages {
		m := msg
		if err := store.SaveMessage(&m); err != nil {
			t.Fatalf("failed to save message: %v", err)
		}
	}

	// Add some todos
	todo := &storage.Todo{
		SessionID:  sess.ID,
		Content:    "Test task",
		ActiveForm: "Testing task",
		Status:     "pending",
	}
	if err := store.CreateTodo(todo); err != nil {
		t.Fatalf("failed to create todo: %v", err)
	}

	a := NewAssembler(store, nil, nil)
	state, err := a.BuildSessionState(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state == nil {
		t.Fatal("expected non-nil state")
	}

	// Verify session properties
	if state.ID != sess.ID {
		t.Errorf("expected ID %q, got %q", sess.ID, state.ID)
	}
	if state.Title != sess.ProjectPath {
		t.Errorf("expected title %q, got %q", sess.ProjectPath, state.Title)
	}
	if state.Status.State != "active" {
		t.Errorf("expected status 'active', got %q", state.Status.State)
	}

	// Verify metrics - tokens are aggregated from messages (5 + 10 = 15)
	if state.Metrics.TotalTokens != 15 {
		t.Errorf("expected 15 tokens, got %d", state.Metrics.TotalTokens)
	}
	// Cost is calculated based on actual API calls, not test data
	if state.Metrics.TotalCost < 0 {
		t.Errorf("expected non-negative cost, got %f", state.Metrics.TotalCost)
	}

	// Verify transcript
	if len(state.Transcript.Messages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(state.Transcript.Messages))
	}

	// Verify todos
	if len(state.Todos) != 1 {
		t.Errorf("expected 1 todo, got %d", len(state.Todos))
	}
	if len(state.Todos) > 0 && state.Todos[0].Content != "Test task" {
		t.Errorf("expected todo content 'Test task', got %q", state.Todos[0].Content)
	}
}

func TestBuildViewState_WithSessions(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.New(dir + "/test.db")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Create multiple sessions
	for i := 0; i < 3; i++ {
		sess := &storage.Session{
			ID:          "sess-" + strconv.Itoa(i),
			ProjectPath: "/path/project" + strconv.Itoa(i),
			Status:      "active",
		}
		if err := store.CreateSession(sess); err != nil {
			t.Fatalf("failed to create session %d: %v", i, err)
		}
	}

	a := NewAssembler(store, nil, nil)
	state, err := a.BuildViewState(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state == nil {
		t.Fatal("expected non-nil state")
	}

	if len(state.Sessions) != 3 {
		t.Errorf("expected 3 sessions, got %d", len(state.Sessions))
	}

	if state.GeneratedAt.IsZero() {
		t.Error("expected GeneratedAt to be set")
	}
}

func TestWorkflowHelpers_NilWorkflow(t *testing.T) {
	a := NewAssembler(nil, nil, nil)

	// workflowPause
	paused, reason, question, pauseAt := a.workflowPause()
	if paused {
		t.Error("expected paused=false for nil workflow")
	}
	if reason != "" {
		t.Error("expected empty reason for nil workflow")
	}
	if question != "" {
		t.Error("expected empty question for nil workflow")
	}
	if !pauseAt.IsZero() {
		t.Error("expected zero pauseAt for nil workflow")
	}

	// workflowPhase
	phase := a.workflowPhase()
	if phase != "" {
		t.Errorf("expected empty phase, got %q", phase)
	}

	// workflowAgent
	agent := a.workflowAgent()
	if agent != "" {
		t.Errorf("expected empty agent, got %q", agent)
	}

	// workflowActivity
	activity := a.workflowActivity()
	if activity != nil {
		t.Error("expected nil activity for nil workflow")
	}
}

func TestToTranscript_Empty(t *testing.T) {
	page := toTranscript(nil, 10)
	if len(page.Messages) != 0 {
		t.Errorf("expected 0 messages, got %d", len(page.Messages))
	}
	if page.HasMore {
		t.Error("expected HasMore=false for empty messages")
	}
	if page.NextOffset != 0 {
		t.Errorf("expected NextOffset=0, got %d", page.NextOffset)
	}
}

func TestToTranscript_FieldMapping(t *testing.T) {
	now := time.Now()
	messages := []storage.Message{
		{
			ID:          42,
			Role:        "assistant",
			Content:     "content here",
			ContentType: "text/plain",
			Reasoning:   "thinking...",
			Tokens:      100,
			Timestamp:   now,
			IsSummary:   true,
		},
	}

	page := toTranscript(messages, 10)
	if len(page.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(page.Messages))
	}

	msg := page.Messages[0]
	if msg.ID != "42" {
		t.Errorf("expected ID '42', got %q", msg.ID)
	}
	if msg.Role != "assistant" {
		t.Errorf("expected role 'assistant', got %q", msg.Role)
	}
	if msg.Content != "content here" {
		t.Errorf("expected content 'content here', got %q", msg.Content)
	}
	if msg.ContentType != "text/plain" {
		t.Errorf("expected contentType 'text/plain', got %q", msg.ContentType)
	}
	if msg.Reasoning != "thinking..." {
		t.Errorf("expected reasoning 'thinking...', got %q", msg.Reasoning)
	}
	if msg.Tokens != 100 {
		t.Errorf("expected 100 tokens, got %d", msg.Tokens)
	}
	if !msg.IsSummary {
		t.Error("expected IsSummary=true")
	}
	if !msg.Timestamp.Equal(now) {
		t.Errorf("timestamp mismatch: expected %v, got %v", now, msg.Timestamp)
	}
}

func TestToTodos_Empty(t *testing.T) {
	todos := toTodos(nil)
	if len(todos) != 0 {
		t.Errorf("expected 0 todos, got %d", len(todos))
	}
}

func TestToTodos_NilCompletedAt(t *testing.T) {
	items := []storage.Todo{
		{ID: 1, Content: "Task", Status: "pending", CompletedAt: nil},
	}

	todos := toTodos(items)
	if len(todos) != 1 {
		t.Fatalf("expected 1 todo, got %d", len(todos))
	}

	if !todos[0].CompletedAt.IsZero() {
		t.Error("expected zero CompletedAt for nil input")
	}
}

func TestToPlanSnapshot_EmptyTasks(t *testing.T) {
	plan := &orchestrator.Plan{
		ID:          "plan-empty",
		FeatureName: "Empty Feature",
		Description: "No tasks",
		Tasks:       []orchestrator.Task{},
	}

	snapshot := toPlanSnapshot(plan)
	if snapshot == nil {
		t.Fatal("expected non-nil snapshot")
	}

	if len(snapshot.Tasks) != 0 {
		t.Errorf("expected 0 tasks, got %d", len(snapshot.Tasks))
	}
	if snapshot.Progress.Total != 0 {
		t.Errorf("expected total=0, got %d", snapshot.Progress.Total)
	}
}

func TestBuildSessionState_WithPlanStore(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.New(dir + "/test.db")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Create session
	sess := &storage.Session{
		ID:     "sess-with-plan",
		Status: "active",
	}
	if err := store.CreateSession(sess); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	// Create a mock plan store
	planStore := &mockPlanStore{
		plans: map[string]*orchestrator.Plan{
			"plan-123": {
				ID:          "plan-123",
				FeatureName: "Test Feature",
				Tasks: []orchestrator.Task{
					{ID: "t1", Title: "Task 1", Status: orchestrator.TaskCompleted},
				},
			},
		},
	}

	// Associate session with plan
	if err := store.LinkSessionToPlan(sess.ID, "plan-123"); err != nil {
		t.Fatalf("failed to link session to plan: %v", err)
	}

	a := NewAssembler(store, planStore, nil)
	state, err := a.BuildSessionState(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state == nil {
		t.Fatal("expected non-nil state")
	}

	if state.Plan == nil {
		t.Fatal("expected plan to be set")
	}
	if state.Plan.ID != "plan-123" {
		t.Errorf("expected plan ID 'plan-123', got %q", state.Plan.ID)
	}
	if state.Plan.FeatureName != "Test Feature" {
		t.Errorf("expected feature name 'Test Feature', got %q", state.Plan.FeatureName)
	}
}

// mockPlanStore implements orchestrator.PlanStore for testing
type mockPlanStore struct {
	plans map[string]*orchestrator.Plan
}

func (m *mockPlanStore) SavePlan(plan *orchestrator.Plan) error {
	m.plans[plan.ID] = plan
	return nil
}

func (m *mockPlanStore) LoadPlan(planID string) (*orchestrator.Plan, error) {
	if plan, ok := m.plans[planID]; ok {
		return plan, nil
	}
	return nil, nil
}

func (m *mockPlanStore) ListPlans() ([]orchestrator.Plan, error) {
	var result []orchestrator.Plan
	for _, p := range m.plans {
		result = append(result, *p)
	}
	return result, nil
}

func (m *mockPlanStore) ReadLog(planID string, logKind string, limit int) ([]string, string, error) {
	return nil, "", nil
}
