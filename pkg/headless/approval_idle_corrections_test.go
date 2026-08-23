package headless

import (
	"context"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/sessionexec"
	"m31labs.dev/buckley/pkg/storage"
)

func TestDurableApproval_TimerUsesCanonicalPendingBeforeAuthoritativeExpiry(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	sessionID := "durable-approval-authoritative-expiry"
	if err := store.CreateSession(&storage.Session{
		ID: sessionID, Principal: "alice", Status: storage.SessionStatusActive,
		CreatedAt: now, LastActive: now,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	localExpiry := now.Add(120 * time.Millisecond)
	local := &PendingApproval{
		ID: "approval-authoritative-expiry", ToolName: "run_shell",
		ToolArgs:  map[string]any{"command": "go test ./..."},
		CreatedAt: now, ExpiresAt: localExpiry,
	}
	candidate := &storage.PendingApproval{
		ID: local.ID, SessionID: sessionID, ToolName: local.ToolName,
		ToolInput: `{"command":"go test ./..."}`, Status: "pending",
		CreatedAt: now, ExpiresAt: localExpiry,
	}
	runner := &Runner{
		sessionID: sessionID, store: store, durable: true,
		approvalChan: make(chan ApprovalResponse, 1), state: StateProcessing,
	}

	type result struct {
		approved bool
		err      error
	}
	resultCh := make(chan result, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		approved, err := runner.waitForDurableApproval(ctx, local, candidate)
		resultCh <- result{approved: approved, err: err}
	}()

	waitForPendingApproval(t, store, candidate.ID)
	deadline := time.Now().Add(time.Second)
	for runner.GetPendingApproval() == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if runner.GetPendingApproval() == nil {
		t.Fatal("runner did not expose pending approval")
	}

	// Move only the canonical row's expiry into the future after the runner
	// captured its local expiry. The local timer must reconcile the still
	// pending row and keep waiting rather than returning a premature timeout.
	authoritativeExpiry := time.Now().UTC().Add(900 * time.Millisecond)
	if _, err := store.DB().Exec(
		`UPDATE pending_approvals SET expires_at = ? WHERE id = ? AND session_id = ?`,
		authoritativeExpiry.Format(time.RFC3339Nano), candidate.ID, sessionID,
	); err != nil {
		t.Fatalf("move canonical expiry: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	if _, _, err := store.DecidePendingApproval(
		candidate.ID, sessionID, "approved", "alice", "", time.Now().UTC(),
	); err != nil {
		t.Fatalf("DecidePendingApproval: %v", err)
	}
	select {
	case runner.approvalChan <- ApprovalResponse{ID: candidate.ID, Approved: true}:
	default:
	}

	select {
	case got := <-resultCh:
		if got.err != nil || !got.approved {
			t.Fatalf("canonical approval result = approved:%v err:%v", got.approved, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("durable approval did not observe canonical decision")
	}
}

func TestRunnerResumeWithActiveCommandRemainsProcessingAndNonIdle(t *testing.T) {
	runner := &Runner{
		sessionID:       "resume-active-command",
		state:           StatePaused,
		activeCommandID: "blocked-provider",
		lastActive:      time.Now().Add(-time.Hour),
		idleTimeout:     time.Minute,
	}

	if err := runner.resume(); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if got := runner.State(); got != StateProcessing {
		t.Fatalf("state after resume = %s, want %s", got, StateProcessing)
	}
	if runner.IsIdle() {
		t.Fatal("runner with active provider command must not be idle-eligible")
	}
}

func TestRunnerResumeCommandDoesNotCountItselfAsActive(t *testing.T) {
	runner := &Runner{
		sessionID:       "resume-control-command",
		state:           StatePaused,
		activeCommandID: "resume-command",
		lastActive:      time.Now().Add(-time.Hour),
		idleTimeout:     time.Minute,
	}

	if err := runner.resumeForCommand("resume-command"); err != nil {
		t.Fatalf("resumeForCommand: %v", err)
	}
	if got := runner.State(); got != StateIdle {
		t.Fatalf("state after resume = %s, want %s", got, StateIdle)
	}
	if runner.IsIdle() {
		t.Fatal("resume activity must refresh lastActive")
	}
}

func TestRunnerIsIdleRejectsDurableBuffer(t *testing.T) {
	runner := &Runner{
		sessionID:        "idle-buffer",
		state:            StateIdle,
		lastActive:       time.Now().Add(-time.Hour),
		idleTimeout:      time.Minute,
		durableBuffering: true,
	}
	if runner.IsIdle() {
		t.Fatal("durable transcript buffering must not be idle-eligible")
	}

	runner.durableBuffering = false
	runner.durableBuffer = []sessionexec.TranscriptEntry{{Ordinal: 0, Role: "assistant", Content: "pending"}}
	if runner.IsIdle() {
		t.Fatal("durable transcript buffer must not be idle-eligible")
	}
}
