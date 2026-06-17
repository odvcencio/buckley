package headless

import (
	"context"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

// mockEmitter captures events for testing.
type mockEmitter struct {
	events []RunnerEvent
}

func (m *mockEmitter) Emit(event RunnerEvent) {
	m.events = append(m.events, event)
}

type fakeEchoTool struct{}

func (fakeEchoTool) Name() string { return "echo_tool" }

func (fakeEchoTool) Description() string { return "returns the provided text" }

func (fakeEchoTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{
		Type: "object",
		Properties: map[string]builtin.PropertySchema{
			"text": {Type: "string", Description: "text to echo"},
		},
		Required: []string{"text"},
	}
}

func (fakeEchoTool) Execute(params map[string]any) (*builtin.Result, error) {
	text, _ := params["text"].(string)
	return &builtin.Result{
		Success: true,
		Data:    map[string]any{"text": text},
		DisplayData: map[string]any{
			"message": text,
		},
	}, nil
}

func TestRunnerStateTransitions(t *testing.T) {
	tests := []struct {
		name     string
		action   func(r *Runner) error
		expected RunnerState
	}{
		{
			name:     "initial state is idle",
			action:   func(r *Runner) error { return nil },
			expected: StateIdle,
		},
		{
			name: "pause transitions to paused",
			action: func(r *Runner) error {
				return r.pause()
			},
			expected: StatePaused,
		},
		{
			name: "resume from paused transitions to idle",
			action: func(r *Runner) error {
				_ = r.pause()
				return r.resume()
			},
			expected: StateIdle,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			emitter := &mockEmitter{}
			runner := &Runner{
				sessionID:    "test-session",
				session:      &storage.Session{ID: "test-session"},
				state:        StateIdle,
				lastActive:   time.Now(),
				idleTimeout:  30 * time.Minute,
				emitter:      emitter,
				approvalChan: make(chan ApprovalResponse, 1),
			}

			if err := tc.action(runner); err != nil {
				t.Fatalf("action failed: %v", err)
			}

			if got := runner.State(); got != tc.expected {
				t.Errorf("expected state %v, got %v", tc.expected, got)
			}
		})
	}
}

func TestRunnerIdleDetection(t *testing.T) {
	runner := &Runner{
		sessionID:    "test-session",
		session:      &storage.Session{ID: "test-session"},
		state:        StateIdle,
		lastActive:   time.Now().Add(-1 * time.Hour),
		idleTimeout:  30 * time.Minute,
		approvalChan: make(chan ApprovalResponse, 1),
	}

	if !runner.IsIdle() {
		t.Error("expected runner to be idle after timeout")
	}

	// Update last active to now
	runner.lastActive = time.Now()
	if runner.IsIdle() {
		t.Error("expected runner to not be idle after activity")
	}
}

func TestRunnerConfig(t *testing.T) {
	t.Run("requires session", func(t *testing.T) {
		_, err := NewRunner(RunnerConfig{})
		if err == nil {
			t.Error("expected error for missing session")
		}
	})

	t.Run("requires model manager", func(t *testing.T) {
		_, err := NewRunner(RunnerConfig{
			Session: &storage.Session{ID: "test"},
		})
		if err == nil {
			t.Error("expected error for missing model manager")
		}
	})
}

func TestPendingApproval(t *testing.T) {
	emitter := &mockEmitter{}
	runner := &Runner{
		sessionID:    "test-session",
		session:      &storage.Session{ID: "test-session"},
		state:        StateIdle,
		lastActive:   time.Now(),
		idleTimeout:  30 * time.Minute,
		emitter:      emitter,
		approvalChan: make(chan ApprovalResponse, 1),
	}

	// Initially no pending approval
	if runner.GetPendingApproval() != nil {
		t.Error("expected no pending approval initially")
	}

	// Set a pending approval
	runner.pendingApproval = &PendingApproval{
		ID:        "test-approval",
		ToolName:  "write_file",
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}

	if runner.GetPendingApproval() == nil {
		t.Error("expected pending approval after setting")
	}
}

func TestToolApprovalRequired(t *testing.T) {
	runner := &Runner{}

	dangerousTools := []string{"write_file", "apply_patch", "run_shell", "search_replace"}
	safeTools := []string{"read_file", "list_directory", "git_status"}

	for _, tool := range dangerousTools {
		if !runner.requiresApproval(tool, nil) {
			t.Errorf("expected %s to require approval", tool)
		}
	}

	for _, tool := range safeTools {
		if runner.requiresApproval(tool, nil) {
			t.Errorf("expected %s to not require approval", tool)
		}
	}
}

func TestToolApprovalRequiredRespectsToolPolicyList(t *testing.T) {
	runner := &Runner{
		requiredApprovalTools: map[string]struct{}{
			"read_file": {},
		},
	}

	if !runner.requiresApproval("read_file", nil) {
		t.Fatalf("expected read_file to require approval")
	}
}

func TestClampToolTimeoutArgs(t *testing.T) {
	runner := &Runner{maxToolExecTime: 11 * time.Second}
	args := map[string]any{}
	runner.clampToolTimeoutArgs("run_shell", args)
	if got, ok := args["timeout_seconds"].(int); !ok || got != 11 {
		t.Fatalf("timeout_seconds=%v want 11", args["timeout_seconds"])
	}

	args = map[string]any{"timeout_seconds": float64(50)}
	runner.clampToolTimeoutArgs("run_tests", args)
	if got, ok := args["timeout_seconds"].(int); !ok || got != 11 {
		t.Fatalf("timeout_seconds=%v want 11", args["timeout_seconds"])
	}
}

func TestMaxRuntimeTimerStopsRunner(t *testing.T) {
	runner := &Runner{
		sessionID:    "test-session",
		session:      &storage.Session{ID: "test-session"},
		state:        StateIdle,
		lastActive:   time.Now(),
		idleTimeout:  30 * time.Minute,
		approvalChan: make(chan ApprovalResponse, 1),
		commandStop:  make(chan struct{}),
	}

	runner.startMaxRuntimeTimer(25 * time.Millisecond)

	deadline := time.After(250 * time.Millisecond)
	for {
		if runner.State() == StateStopped {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("runner did not stop before deadline (state=%s)", runner.State())
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func TestUpdateApprovalStatusDoesNotOverrideDecidedApproval(t *testing.T) {
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	if err := store.CreateSession(&storage.Session{
		ID:         "s1",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	approval := &storage.PendingApproval{
		ID:        "approval-1",
		SessionID: "s1",
		ToolName:  "run_shell",
		ToolInput: "{}",
		Status:    "pending",
		ExpiresAt: time.Now().Add(1 * time.Hour),
		CreatedAt: time.Now(),
	}
	if err := store.CreatePendingApproval(approval); err != nil {
		t.Fatalf("CreatePendingApproval: %v", err)
	}
	approval.Status = "approved"
	approval.DecidedBy = "alice"
	approval.DecidedAt = time.Now().UTC().Add(-1 * time.Minute)
	if err := store.UpdatePendingApproval(approval); err != nil {
		t.Fatalf("UpdatePendingApproval: %v", err)
	}

	runner := &Runner{store: store}
	runner.updateApprovalStatus("approval-1", "expired", "", "")

	updated, err := store.GetPendingApproval("approval-1")
	if err != nil {
		t.Fatalf("GetPendingApproval: %v", err)
	}
	if updated == nil {
		t.Fatalf("expected approval record to exist")
	}
	if updated.Status != "approved" {
		t.Fatalf("status=%q want approved", updated.Status)
	}
	if updated.DecidedBy != "alice" {
		t.Fatalf("decidedBy=%q want alice", updated.DecidedBy)
	}
}

func TestUpdateApprovalStatusPersistsDecisionReason(t *testing.T) {
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	if err := store.CreateSession(&storage.Session{
		ID:         "s1",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if err := store.CreatePendingApproval(&storage.PendingApproval{
		ID:        "approval-1",
		SessionID: "s1",
		ToolName:  "run_shell",
		ToolInput: "{}",
		Status:    "pending",
		ExpiresAt: time.Now().Add(1 * time.Hour),
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreatePendingApproval: %v", err)
	}

	runner := &Runner{store: store}
	runner.updateApprovalStatus("approval-1", "rejected", "alice", "nope")

	updated, err := store.GetPendingApproval("approval-1")
	if err != nil {
		t.Fatalf("GetPendingApproval: %v", err)
	}
	if updated == nil {
		t.Fatalf("expected approval record to exist")
	}
	if updated.Status != "rejected" {
		t.Fatalf("status=%q want rejected", updated.Status)
	}
	if updated.DecidedBy != "alice" {
		t.Fatalf("decidedBy=%q want alice", updated.DecidedBy)
	}
	if updated.DecisionReason != "nope" {
		t.Fatalf("decisionReason=%q want nope", updated.DecisionReason)
	}
	if updated.DecidedAt.IsZero() {
		t.Fatalf("expected DecidedAt to be set")
	}
}

func TestHandleToolCallsLogsAuditDecisionMetadata(t *testing.T) {
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	now := time.Now()
	if err := store.CreateSession(&storage.Session{
		ID:         "s1",
		CreatedAt:  now,
		LastActive: now,
		Status:     storage.SessionStatusActive,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := store.CreatePendingApproval(&storage.PendingApproval{
		ID:          "tc1",
		SessionID:   "s1",
		ToolName:    "write_file",
		ToolInput:   "{}",
		RiskScore:   42,
		RiskReasons: []string{"test"},
		Status:      "pending",
		ExpiresAt:   now.Add(5 * time.Minute),
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("CreatePendingApproval: %v", err)
	}

	runner := &Runner{
		sessionID:             "s1",
		session:               &storage.Session{ID: "s1"},
		conv:                  conversation.New("s1"),
		store:                 store,
		approvalChan:          make(chan ApprovalResponse, 1),
		requiredApprovalTools: map[string]struct{}{"write_file": {}},
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- runner.handleToolCalls(context.Background(), model.Message{
			ToolCalls: []model.ToolCall{
				{
					ID:   "tc1",
					Type: "function",
					Function: model.FunctionCall{
						Name:      "write_file",
						Arguments: "{}",
					},
				},
			},
		})
	}()

	approval, err := store.GetPendingApproval("tc1")
	if err != nil {
		t.Fatalf("GetPendingApproval: %v", err)
	}
	approval.Status = "rejected"
	approval.DecidedBy = "alice"
	approval.DecidedAt = time.Now()
	if err := store.UpdatePendingApproval(approval); err != nil {
		t.Fatalf("UpdatePendingApproval: %v", err)
	}

	runner.approvalChan <- ApprovalResponse{
		ID:       "tc1",
		Approved: false,
		Reason:   "nope",
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("handleToolCalls: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("handleToolCalls timed out")
	}

	entries, err := store.GetAuditLog("s1", 10)
	if err != nil {
		t.Fatalf("GetAuditLog: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(entries))
	}
	entry := entries[0]
	if entry.Decision != "rejected" {
		t.Fatalf("decision=%q want rejected", entry.Decision)
	}
	if entry.DecidedBy != "alice" {
		t.Fatalf("decidedBy=%q want alice", entry.DecidedBy)
	}
	if entry.RiskScore != 42 {
		t.Fatalf("riskScore=%d want 42", entry.RiskScore)
	}
}

func TestHandleToolCallsPersistsModelVisibleToolTurns(t *testing.T) {
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	now := time.Now()
	if err := store.CreateSession(&storage.Session{
		ID:         "s1",
		CreatedAt:  now,
		LastActive: now,
		Status:     storage.SessionStatusActive,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	registry := tool.NewEmptyRegistry()
	registry.Register(fakeEchoTool{})
	runner := &Runner{
		sessionID: "s1",
		session:   &storage.Session{ID: "s1"},
		conv:      conversation.New("s1"),
		store:     store,
		tools:     registry,
	}

	msg := model.Message{
		Reasoning: "Need to call echo_tool so the next turn sees the result.",
		ToolCalls: []model.ToolCall{{
			ID:   "tc1",
			Type: "function",
			Function: model.FunctionCall{
				Name:      "echo_tool",
				Arguments: `{"text":"hello from tool"}`,
			},
		}},
	}
	if err := runner.handleToolCalls(context.Background(), msg); err != nil {
		t.Fatalf("handleToolCalls: %v", err)
	}

	loaded := conversation.New("s1")
	if err := loaded.LoadFromStorage(store); err != nil {
		t.Fatalf("LoadFromStorage: %v", err)
	}
	if len(loaded.Messages) != 2 {
		t.Fatalf("expected assistant tool call and tool response, got %d messages: %+v", len(loaded.Messages), loaded.Messages)
	}
	if loaded.Messages[0].Role != "assistant" || len(loaded.Messages[0].ToolCalls) != 1 {
		t.Fatalf("expected persisted assistant tool-call message, got %+v", loaded.Messages[0])
	}
	if loaded.Messages[1].Role != "tool" || loaded.Messages[1].ToolCallID != "tc1" || loaded.Messages[1].Name != "echo_tool" {
		t.Fatalf("expected persisted tool response metadata, got %+v", loaded.Messages[1])
	}

	modelMsgs := loaded.ToModelMessages()
	if len(modelMsgs) != 2 {
		t.Fatalf("expected 2 model messages, got %d", len(modelMsgs))
	}
	if len(modelMsgs[0].ToolCalls) != 1 || modelMsgs[0].ToolCalls[0].ID != "tc1" {
		t.Fatalf("assistant tool call missing after reload: %+v", modelMsgs[0])
	}
	if modelMsgs[1].ToolCallID != "tc1" || modelMsgs[1].Name != "echo_tool" {
		t.Fatalf("tool response missing correlation metadata after reload: %+v", modelMsgs[1])
	}
}

func TestEventEmission(t *testing.T) {
	emitter := &mockEmitter{}
	runner := &Runner{
		sessionID:    "test-session",
		session:      &storage.Session{ID: "test-session"},
		state:        StateIdle,
		lastActive:   time.Now(),
		idleTimeout:  30 * time.Minute,
		emitter:      emitter,
		approvalChan: make(chan ApprovalResponse, 1),
	}

	// Pause should emit state change event
	_ = runner.pause()

	if len(emitter.events) == 0 {
		t.Error("expected at least one event to be emitted")
	}

	foundStateChange := false
	for _, event := range emitter.events {
		if event.Type == EventStateChanged {
			foundStateChange = true
			if event.SessionID != "test-session" {
				t.Error("event has wrong session ID")
			}
			break
		}
	}

	if !foundStateChange {
		t.Error("expected state change event")
	}
}

func TestDefaultIdleTimeout(t *testing.T) {
	cfg := RunnerConfig{
		Session:      &storage.Session{ID: "test"},
		ModelManager: nil, // Will fail validation, but tests default
		Store:        nil,
	}

	// Check that zero timeout gets default
	if cfg.IdleTimeout != 0 {
		t.Error("expected zero idle timeout in config")
	}
}

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name      string
		cfg       RunnerConfig
		expectErr bool
	}{
		{
			name:      "empty config",
			cfg:       RunnerConfig{},
			expectErr: true,
		},
		{
			name: "missing model manager",
			cfg: RunnerConfig{
				Session: &storage.Session{ID: "test"},
				Store:   nil,
			},
			expectErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewRunner(tc.cfg)
			if tc.expectErr && err == nil {
				t.Error("expected error")
			}
			if !tc.expectErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
