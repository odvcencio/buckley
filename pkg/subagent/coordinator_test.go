package subagent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/agentcoord"
	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/persona"
	"m31labs.dev/buckley/pkg/runledger"
)

type flakyAppendLedger struct {
	*runledger.SQLiteStore
	failType string
	failures atomic.Int32
}

func (l *flakyAppendLedger) Append(ctx context.Context, event runledger.Event) (runledger.Event, error) {
	if event.Type == l.failType {
		for remaining := l.failures.Load(); remaining > 0; remaining = l.failures.Load() {
			if l.failures.CompareAndSwap(remaining, remaining-1) {
				return runledger.Event{}, fmt.Errorf("injected append failure for %s", event.Type)
			}
		}
	}
	return l.SQLiteStore.Append(ctx, event)
}

type losingAttachmentStore struct {
	*runledger.SQLiteStore
	heartbeats atomic.Int32
}

type terminalizingAttachmentStore struct {
	*runledger.SQLiteStore
	heartbeats atomic.Int32
}

type observedHeartbeat struct {
	sequence int32
	lease    agentcoord.AttachmentLease
}

type observingAttachmentStore struct {
	*runledger.SQLiteStore
	successful      atomic.Int32
	configuredLease atomic.Int64
	virtualNow      atomic.Int64
	virtualExpiry   atomic.Int64
	renewed         chan observedHeartbeat
}

func (s *observingAttachmentStore) Attach(ctx context.Context, request agentcoord.AttachmentRequest) (agentcoord.AttachmentLease, error) {
	configured := request.LeaseDuration
	if configured <= 0 {
		configured = runledger.AttachmentDefaultLease
	}
	s.configuredLease.Store(int64(configured))
	s.virtualNow.Store(0)
	s.virtualExpiry.Store(int64(configured))
	request.LeaseDuration = runledger.AttachmentDefaultLease
	return s.SQLiteStore.Attach(ctx, request)
}

func (s *observingAttachmentStore) Heartbeat(ctx context.Context, request agentcoord.AttachmentHeartbeatRequest) (agentcoord.AttachmentLease, error) {
	configured := time.Duration(s.configuredLease.Load())
	if configured <= 0 || request.LeaseDuration != configured {
		return agentcoord.AttachmentLease{}, fmt.Errorf("heartbeat lease = %s, want configured %s", request.LeaseDuration, configured)
	}
	advance := configured / 4
	if advance <= 0 {
		advance = time.Nanosecond
	}
	now := time.Duration(s.virtualNow.Add(int64(advance)))
	if expiry := time.Duration(s.virtualExpiry.Load()); expiry > 0 && now >= expiry {
		return agentcoord.AttachmentLease{}, runledger.ErrAttachmentExpired
	}
	request.LeaseDuration = runledger.AttachmentDefaultLease
	lease, err := s.SQLiteStore.Heartbeat(ctx, request)
	if err != nil {
		return agentcoord.AttachmentLease{}, err
	}
	expires := now + configured
	s.virtualExpiry.Store(int64(expires))
	lease.HeartbeatAt = time.Unix(0, int64(now)).UTC()
	lease.LeaseExpiresAt = time.Unix(0, int64(expires)).UTC()
	sequence := s.successful.Add(1)
	select {
	case s.renewed <- observedHeartbeat{sequence: sequence, lease: lease}:
	default:
	}
	return lease, nil
}

type contendedAttachmentStore struct {
	*runledger.SQLiteStore
	locker  *sql.DB
	calls   atomic.Int32
	blocked chan struct{}
	release chan struct{}
}

func (s *contendedAttachmentStore) Heartbeat(ctx context.Context, request agentcoord.AttachmentHeartbeatRequest) (agentcoord.AttachmentLease, error) {
	if s.calls.Add(1) != 3 {
		return s.SQLiteStore.Heartbeat(ctx, request)
	}
	tx, err := s.locker.BeginTx(context.Background(), nil)
	if err != nil {
		return agentcoord.AttachmentLease{}, err
	}
	if _, err := tx.ExecContext(context.Background(), `UPDATE agent_runs SET status = status WHERE run_id = ?`, request.RunID); err != nil {
		_ = tx.Rollback()
		return agentcoord.AttachmentLease{}, err
	}
	close(s.blocked)
	released := make(chan struct{})
	go func() {
		commit := false
		select {
		case <-s.release:
			commit = true
		case <-ctx.Done():
		}
		if commit {
			_ = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
		close(released)
	}()
	lease, heartbeatErr := s.SQLiteStore.Heartbeat(ctx, request)
	<-released
	return lease, heartbeatErr
}

type gatedAttachmentStore struct {
	*runledger.SQLiteStore
	calls   atomic.Int32
	blocked chan struct{}
	release chan struct{}
}

func (s *gatedAttachmentStore) Heartbeat(ctx context.Context, request agentcoord.AttachmentHeartbeatRequest) (agentcoord.AttachmentLease, error) {
	if s.calls.Add(1) != 3 {
		return s.SQLiteStore.Heartbeat(ctx, request)
	}
	close(s.blocked)
	select {
	case <-s.release:
		return s.SQLiteStore.Heartbeat(ctx, request)
	case <-ctx.Done():
		return agentcoord.AttachmentLease{}, ctx.Err()
	}
}

type initialHeartbeatFailureStore struct {
	*runledger.SQLiteStore
	calls   atomic.Int32
	blocked chan struct{}
	release chan struct{}
	err     error
}

func (s *initialHeartbeatFailureStore) Heartbeat(ctx context.Context, request agentcoord.AttachmentHeartbeatRequest) (agentcoord.AttachmentLease, error) {
	if s.calls.Add(1) != 3 {
		return s.SQLiteStore.Heartbeat(ctx, request)
	}
	close(s.blocked)
	select {
	case <-s.release:
		return agentcoord.AttachmentLease{}, s.err
	case <-ctx.Done():
		return agentcoord.AttachmentLease{}, ctx.Err()
	}
}

type toggleRalphSink struct {
	fail  atomic.Bool
	calls atomic.Int32
}

func (s *toggleRalphSink) WriteEvent(context.Context, runledger.Event) error {
	s.calls.Add(1)
	if s.fail.Load() {
		return errors.New("injected terminal ralph delivery failure")
	}
	return nil
}

func (s *losingAttachmentStore) Heartbeat(ctx context.Context, request agentcoord.AttachmentHeartbeatRequest) (agentcoord.AttachmentLease, error) {
	if s.heartbeats.Add(1) <= 3 {
		return s.SQLiteStore.Heartbeat(ctx, request)
	}
	deadline := time.Now().Add(time.Second)
	for {
		err := s.SQLiteStore.Detach(context.Background(), agentcoord.AttachmentDetachRequest{
			SessionID: request.SessionID, RunID: request.RunID, AttemptID: request.AttemptID,
			LeaseGeneration: request.LeaseGeneration, Reason: "injected ownership loss",
		})
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	return agentcoord.AttachmentLease{}, runledger.ErrAttachmentStale
}

func (s *terminalizingAttachmentStore) Heartbeat(ctx context.Context, request agentcoord.AttachmentHeartbeatRequest) (agentcoord.AttachmentLease, error) {
	if s.heartbeats.Add(1) <= 3 {
		return s.SQLiteStore.Heartbeat(ctx, request)
	}
	endedAt := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.SQLiteStore.DB().ExecContext(ctx, `
		UPDATE agent_runs
		SET status = 'completed', ended_at = ?, outcome_json = '{"summary":"external terminal"}'
		WHERE run_id = ? AND session_id = ? AND ended_at IS NULL
	`, endedAt, request.RunID, request.SessionID)
	if err != nil {
		return agentcoord.AttachmentLease{}, err
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		return agentcoord.AttachmentLease{}, fmt.Errorf("external terminalization rows = %d, err = %v", rows, err)
	}
	return agentcoord.AttachmentLease{}, runledger.ErrAttachmentTerminal
}

func TestCoordinator_SpawnThreadsResolvedPersonaContract(t *testing.T) {
	requests := make(chan Request, 1)
	manager := NewManager(runnerFunc(func(_ context.Context, request Request, started func(int)) (string, error) {
		started(42)
		requests <- request
		return "complete", nil
	}), 2)
	manager.SetPersonaContext(persona.NewRegistry(), persona.Persona{Name: "root", Tier: persona.TierReason})
	manager.personas.Add(persona.Persona{
		Name:         "worker",
		Model:        "sonnet",
		Prompt:       "Use the worker protocol.",
		AllowedTools: []string{"read_file"},
		StepCap:      5,
	})
	t.Cleanup(func() { _ = manager.Close() })

	coordinator := NewCoordinator(manager)
	run, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{
		RunID:           "run-persona",
		ID:              "task-persona",
		ParentRunID:     "run-root",
		ParentSessionID: "session-1",
		Agent:           "reviewer",
		Task:            "inspect this",
		Persona:         "worker",
		Model:           "should-be-overridden",
		Tier:            "reason",
		SystemPrompt:    "should-be-overridden",
		AllowedTools:    []string{"read_file", "write_file"},
		StepCap:         20,
		Effort:          "high",
		WorkspaceClaims: []string{"pkg/subagent"},
		Isolation:       "worktree",
		OutputSchema:    "buckley.artifact/v1",
		ApprovalPosture: "safe",
		TimeoutSeconds:  40,
		Budget: agentcoord.Budget{
			MaxToolCalls:     13,
			MaxModelRequests: 8,
			MaxElapsedSecond: 35,
			MaxCostUSD:       0.75,
		},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if run.ID != "run-persona" || run.ParentRunID != "run-root" || run.ParentSessionID != "session-1" {
		t.Fatalf("run identity = %+v", run)
	}

	select {
	case request := <-requests:
		if request.ID != "run-persona" || request.TaskID != "task-persona" || request.ParentRunID != "run-root" {
			t.Fatalf("request identity = %+v", request)
		}
		if request.Model != "sonnet" || request.Tier != persona.TierExecute || request.SystemPrompt != "Use the worker protocol." || request.StepCap != 5 {
			t.Fatalf("resolved persona contract = %+v", request)
		}
		if got := strings.Join(request.AllowedTools, ","); got != "read_file" {
			t.Fatalf("AllowedTools = %q, want read_file", got)
		}
		if request.Effort != "high" || request.Isolation != "worktree" || request.OutputSchema != "buckley.artifact/v1" || request.ApprovalPosture != "safe" {
			t.Fatalf("execution constraints = %+v", request)
		}
		if request.TimeoutSeconds != 40 || request.Budget.MaxToolCalls != 13 || request.Budget.MaxModelRequests != 8 || request.Budget.MaxElapsedSecond != 35 || request.Budget.MaxCostUSD != 0.75 {
			t.Fatalf("execution limits = %+v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for runner request")
	}
	if _, err := coordinator.Wait(context.Background(), run.ID); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestCoordinator_AdmissionHardMaximaBoundOmittedTaskLimits(t *testing.T) {
	requests := make(chan Request, 1)
	manager := NewManager(runnerFunc(func(_ context.Context, request Request, _ func(int)) (string, error) {
		requests <- request
		return "done", nil
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	coordinator := NewCoordinator(manager, WithAdmissionPolicy(AdmissionPolicyFunc(func(context.Context, agentcoord.AgentTaskSpec) (AdmissionDecision, error) {
		return AdmissionDecision{Allowed: true, TimeoutSeconds: 120, StepCap: 15}, nil
	})))

	run, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{RunID: "run-unbounded", Task: "inspect"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	select {
	case request := <-requests:
		if request.TimeoutSeconds != 120 || request.StepCap != 15 || request.Budget.MaxElapsedSecond != 120 || request.Budget.MaxModelRequests != 15 {
			t.Fatalf("admission maxima did not bound omitted child limits: %+v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for runner request")
	}
	if _, err := coordinator.Wait(context.Background(), run.ID); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestCoordinator_TypedNilAdmissionPolicyFailsClosed(t *testing.T) {
	manager := NewManager(runnerFunc(func(context.Context, Request, func(int)) (string, error) {
		t.Fatal("typed-nil policy allowed runner launch")
		return "", nil
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	var policy AdmissionPolicyFunc
	coordinator := NewCoordinator(manager, WithAdmissionPolicy(policy))
	if _, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{Task: "do not launch"}); err == nil || !strings.Contains(err.Error(), "policy is unavailable") {
		t.Fatalf("typed-nil admission = %v", err)
	}
}

func TestCoordinator_RejectsNonFiniteCostBeforeAdmissionOrLaunch(t *testing.T) {
	launched := false
	manager := NewManager(runnerFunc(func(_ context.Context, _ Request, _ func(int)) (string, error) {
		launched = true
		return "", nil
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	admitted := false
	coordinator := NewCoordinator(manager, WithAdmissionPolicy(AdmissionPolicyFunc(func(context.Context, agentcoord.AgentTaskSpec) (AdmissionDecision, error) {
		admitted = true
		return AdmissionDecision{Allowed: true}, nil
	})))

	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		_, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{
			Task:   "inspect",
			Budget: agentcoord.Budget{MaxCostUSD: value},
		})
		if err == nil || !strings.Contains(err.Error(), "max_cost_usd must be finite") {
			t.Fatalf("Spawn(MaxCostUSD=%v) error = %v", value, err)
		}
	}
	if admitted || launched {
		t.Fatalf("invalid budget crossed boundary: admitted=%t launched=%t", admitted, launched)
	}
}

func TestCoordinator_ClaimsBlockOverlapBeforeSecondWorkerStarts(t *testing.T) {
	started := make(chan struct{}, 1)
	manager := NewManager(runnerFunc(func(ctx context.Context, _ Request, startedPID func(int)) (string, error) {
		startedPID(7)
		started <- struct{}{}
		<-ctx.Done()
		return "", ctx.Err()
	}), 2)
	t.Cleanup(func() { _ = manager.Close() })
	coordinator := NewCoordinator(manager)

	first, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{
		RunID:           "run-first",
		ParentSessionID: "session-claims",
		Task:            "change pkg",
		WorkspaceClaims: []string{"pkg"},
	})
	if err != nil {
		t.Fatalf("spawn first: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first worker did not start")
	}
	_, err = coordinator.Spawn(context.Background(), agentcoord.TaskSpec{
		RunID:           "run-second",
		ParentSessionID: "session-claims",
		Task:            "change subpackage",
		WorkspaceClaims: []string{"pkg/subagent"},
	})
	if err == nil || !strings.Contains(err.Error(), "workspace claim conflict") {
		t.Fatalf("overlapping spawn error = %v, want claim conflict", err)
	}
	if _, err := coordinator.Cancel(context.Background(), first.ID, "test complete"); err != nil {
		t.Fatalf("cancel first: %v", err)
	}
	if _, err := coordinator.Wait(context.Background(), first.ID); err != nil {
		t.Fatalf("wait first: %v", err)
	}
}

func TestCoordinator_SteerQueuesReadableMailbox(t *testing.T) {
	manager := NewManager(runnerFunc(func(ctx context.Context, _ Request, started func(int)) (string, error) {
		started(9)
		<-ctx.Done()
		return "", ctx.Err()
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	coordinator := NewCoordinator(manager)
	run, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{RunID: "run-mail", Task: "investigate"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	message, err := coordinator.Steer(context.Background(), run.ID, "focus on the failing search")
	if err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if message.Delivery != "queued" || message.Kind != "steer" {
		t.Fatalf("steer message = %+v", message)
	}
	messages, err := coordinator.Messages(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Content != "focus on the failing search" || messages[0].Delivery != "queued" {
		t.Fatalf("messages = %+v", messages)
	}
	_, _ = coordinator.Cancel(context.Background(), run.ID, "test complete")
}

func TestCoordinator_DurableSteerUsesExplicitOperatorPath(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "durable-steer.db")
	evidenceStore, err := evidence.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = evidenceStore.Close() })
	ledger, err := runledger.NewWithDB(evidenceStore.DB())
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan agentcoord.Message, 1)
	manager := NewManager(interactiveRunnerFunc(func(ctx context.Context, _ Request, started func(int), commands <-chan CommandDelivery) (string, error) {
		started(97)
		select {
		case delivery := <-commands:
			received <- delivery.Message
			delivery.Acknowledge(nil)
		case <-ctx.Done():
			return "", ctx.Err()
		}
		<-ctx.Done()
		return "", ctx.Err()
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	coordinator := NewCoordinator(manager, WithRunLedger(ledger), WithEvidence(evidenceStore))
	run, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{
		RunID: "run-durable-steer", ParentSessionID: "session-durable-steer", Task: "accept operator direction",
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := coordinator.Steer(context.Background(), run.ID, "inspect the durable seam")
	if err != nil || message.Delivery != "delivered" || message.From != "operator" || message.Kind != "steer" {
		t.Fatalf("durable Steer = %+v, %v", message, err)
	}
	select {
	case delivered := <-received:
		if delivered.Content != "inspect the durable seam" {
			t.Fatalf("delivered Steer = %+v", delivered)
		}
	case <-time.After(time.Second):
		t.Fatal("durable Steer was not delivered")
	}
	retried, err := coordinator.Steer(context.Background(), run.ID, "inspect the durable seam")
	if err != nil || retried.ID != message.ID || retried.IdempotencyKey != message.IdempotencyKey {
		t.Fatalf("durable omitted-id Steer retry = %+v, %v; want id=%q key=%q", retried, err, message.ID, message.IdempotencyKey)
	}
	_, _ = coordinator.Cancel(context.Background(), run.ID, "done")
}

func TestCoordinator_DurableSteerRetryAfterNackUsesAuthorizedCurrentFence(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "durable-steer-nack.db")
	evidenceStore, err := evidence.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = evidenceStore.Close() })
	ledger, err := runledger.NewWithDB(evidenceStore.DB())
	if err != nil {
		t.Fatal(err)
	}
	var deliveries atomic.Int32
	received := make(chan agentcoord.Message, 1)
	manager := NewManager(interactiveRunnerFunc(func(ctx context.Context, _ Request, started func(int), commands <-chan CommandDelivery) (string, error) {
		started(98)
		for {
			select {
			case delivery := <-commands:
				if deliveries.Add(1) == 1 {
					delivery.Acknowledge(errors.New("injected transient delivery failure"))
					continue
				}
				received <- delivery.Message
				delivery.Acknowledge(nil)
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	coordinator := NewCoordinator(manager, WithRunLedger(ledger), WithEvidence(evidenceStore))
	run, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{
		RunID: "run-durable-steer-nack", ParentSessionID: "session-durable-steer-nack", Task: "retry operator direction",
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := coordinator.Steer(context.Background(), run.ID, "retry this direction")
	if err != nil || first.State != agentcoord.MessageQueued {
		t.Fatalf("first transient Steer = %+v, %v", first, err)
	}
	retried, err := coordinator.Steer(context.Background(), run.ID, "retry this direction")
	if err != nil || retried.ID != first.ID || retried.IdempotencyKey != first.IdempotencyKey || retried.State != agentcoord.MessageProcessed {
		t.Fatalf("retried Steer = %+v, %v; first=%+v", retried, err, first)
	}
	select {
	case delivered := <-received:
		if delivered.ID != first.ID || delivered.AttemptID != run.AttemptID || delivered.LeaseGeneration != run.LeaseGeneration {
			t.Fatalf("retried delivery = %+v; run=%+v", delivered, run)
		}
	case <-time.After(time.Second):
		t.Fatal("retried durable Steer was not delivered")
	}
	if deliveries.Load() != 2 {
		t.Fatalf("live delivery attempts = %d, want 2", deliveries.Load())
	}
	messages, err := ledger.List(context.Background(), agentcoord.MailboxQuery{
		SessionID: run.SessionID, RunID: run.ID,
	})
	if err != nil || len(messages) != 1 || messages[0].State != agentcoord.MessageProcessed || messages[0].AttemptCount != 2 ||
		messages[0].AttemptID != run.AttemptID || messages[0].LeaseGeneration != run.LeaseGeneration {
		t.Fatalf("durable mailbox after Nack retry = %+v, %v", messages, err)
	}
	_, _ = coordinator.Cancel(context.Background(), run.ID, "done")
}

func TestCoordinator_DurableParentSendRetryAfterNackUsesAuthorizedCurrentFence(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "durable-send-nack.db")
	evidenceStore, err := evidence.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = evidenceStore.Close() })
	ledger, err := runledger.NewWithDB(evidenceStore.DB())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := ledger.StartRun(ctx, runledger.AgentRun{
		RunID: "run-send-nack-parent", SessionID: "session-send-nack", Status: "running",
	}); err != nil {
		t.Fatal(err)
	}
	parentLease, err := ledger.Attach(ctx, agentcoord.AttachmentRequest{
		SessionID: "session-send-nack", RunID: "run-send-nack-parent", AttemptID: "attempt-send-nack-parent", LeaseDuration: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	var deliveries atomic.Int32
	manager := NewManager(interactiveRunnerFunc(func(ctx context.Context, _ Request, started func(int), commands <-chan CommandDelivery) (string, error) {
		started(102)
		for {
			select {
			case delivery := <-commands:
				if deliveries.Add(1) == 1 {
					delivery.Acknowledge(errors.New("injected transient parent delivery failure"))
					continue
				}
				delivery.Acknowledge(nil)
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	coordinator := NewCoordinator(manager, WithRunLedger(ledger), WithEvidence(evidenceStore))
	child, err := coordinator.Spawn(ctx, agentcoord.TaskSpec{
		RunID: "run-send-nack-child", ParentRunID: "run-send-nack-parent", ParentSessionID: "session-send-nack", Task: "retry parent direction",
	})
	if err != nil {
		t.Fatal(err)
	}
	message := agentcoord.Message{
		RunID: child.ID, To: child.ID, From: "run-send-nack-parent", Content: "retry parent direction",
		SourceAttemptID: parentLease.AttemptID, SourceLeaseGeneration: parentLease.LeaseGeneration,
	}
	first, err := coordinator.Send(ctx, message)
	if err != nil || first.State != agentcoord.MessageQueued {
		t.Fatalf("first parent Send = %+v, %v", first, err)
	}
	retried, err := coordinator.Send(ctx, message)
	if err != nil || retried.ID != first.ID || retried.IdempotencyKey != first.IdempotencyKey || retried.State != agentcoord.MessageProcessed {
		t.Fatalf("retried parent Send = %+v, %v; first=%+v", retried, err, first)
	}
	if deliveries.Load() != 2 {
		t.Fatalf("parent live delivery attempts = %d, want 2", deliveries.Load())
	}
	_, _ = coordinator.Cancel(ctx, child.ID, "done")
}

func TestCoordinator_DurableSteerRetryUsesReplacementAttachmentFence(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "durable-steer-replacement.db")
	evidenceStore, err := evidence.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = evidenceStore.Close() })
	ledger, err := runledger.NewWithDB(evidenceStore.DB())
	if err != nil {
		t.Fatal(err)
	}
	firstDelivery := make(chan agentcoord.Message, 1)
	firstManager := NewManager(interactiveRunnerFunc(func(ctx context.Context, _ Request, started func(int), commands <-chan CommandDelivery) (string, error) {
		started(99)
		select {
		case delivery := <-commands:
			firstDelivery <- delivery.Message
			delivery.Acknowledge(errors.New("injected transient delivery failure"))
		case <-ctx.Done():
			return "", ctx.Err()
		}
		<-ctx.Done()
		return "", ctx.Err()
	}), 1)
	t.Cleanup(func() { _ = firstManager.Close() })
	spec := agentcoord.TaskSpec{
		RunID: "run-durable-steer-replacement", ParentSessionID: "session-durable-steer-replacement", Task: "reattach and retry direction",
	}
	firstCoordinator := NewCoordinator(firstManager, WithRunLedger(ledger), WithEvidence(evidenceStore), WithAttachmentLease(5*time.Second), WithHeartbeatInterval(time.Second))
	firstRun, err := firstCoordinator.Spawn(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	first, err := firstCoordinator.Steer(context.Background(), firstRun.ID, "retry after replacement")
	if err != nil || first.State != agentcoord.MessageQueued {
		t.Fatalf("first transient Steer = %+v, %v", first, err)
	}
	select {
	case <-firstDelivery:
	case <-time.After(time.Second):
		t.Fatal("first attachment did not receive transient delivery")
	}
	firstManager.SetLifecycleObserver(nil)
	if err := ledger.Detach(context.Background(), agentcoord.AttachmentDetachRequest{
		SessionID: firstRun.SessionID, RunID: firstRun.ID, AttemptID: firstRun.AttemptID,
		LeaseGeneration: firstRun.LeaseGeneration, Reason: "simulate process loss",
	}); err != nil {
		t.Fatal(err)
	}
	if err := firstManager.Close(); err != nil {
		t.Fatal(err)
	}

	replacementDelivery := make(chan agentcoord.Message, 1)
	replacementManager := NewManager(interactiveRunnerFunc(func(ctx context.Context, _ Request, started func(int), commands <-chan CommandDelivery) (string, error) {
		started(100)
		for {
			select {
			case delivery := <-commands:
				replacementDelivery <- delivery.Message
				delivery.Acknowledge(nil)
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
	}), 1)
	t.Cleanup(func() { _ = replacementManager.Close() })
	replacementCoordinator := NewCoordinator(replacementManager, WithRunLedger(ledger), WithEvidence(evidenceStore), WithAttachmentLease(5*time.Second), WithHeartbeatInterval(time.Second))
	replacementRun, err := replacementCoordinator.Spawn(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if replacementRun.LeaseGeneration <= firstRun.LeaseGeneration || replacementRun.AttemptID == firstRun.AttemptID {
		t.Fatalf("replacement attachment = %+v; first=%+v", replacementRun, firstRun)
	}
	if _, err := replacementCoordinator.send(context.Background(), agentcoord.Message{
		RunID: firstRun.ID, To: firstRun.ID, Content: "retry after replacement",
		AttemptID: firstRun.AttemptID, LeaseGeneration: firstRun.LeaseGeneration,
	}, messageAuthorityOperator); err == nil {
		t.Fatal("stale attachment fence delivered after replacement")
	}
	select {
	case stale := <-replacementDelivery:
		t.Fatalf("replacement runner received stale delivery: %+v", stale)
	default:
	}
	retried, err := replacementCoordinator.Steer(context.Background(), replacementRun.ID, "retry after replacement")
	if err != nil || retried.ID != first.ID || retried.IdempotencyKey != first.IdempotencyKey || retried.State != agentcoord.MessageProcessed {
		t.Fatalf("replacement retry = %+v, %v; first=%+v", retried, err, first)
	}
	select {
	case delivered := <-replacementDelivery:
		if delivered.AttemptID != replacementRun.AttemptID || delivered.LeaseGeneration != replacementRun.LeaseGeneration {
			t.Fatalf("replacement delivery fence = %+v; run=%+v", delivered, replacementRun)
		}
	case <-time.After(time.Second):
		t.Fatal("replacement attachment did not receive retry")
	}
	messages, err := ledger.List(context.Background(), agentcoord.MailboxQuery{
		SessionID: replacementRun.SessionID, RunID: replacementRun.ID,
	})
	if err != nil || len(messages) != 1 || messages[0].State != agentcoord.MessageProcessed || messages[0].AttemptCount != 2 ||
		messages[0].AttemptID != replacementRun.AttemptID || messages[0].LeaseGeneration != replacementRun.LeaseGeneration {
		t.Fatalf("replacement durable mailbox = %+v, %v", messages, err)
	}
	_, _ = replacementCoordinator.Cancel(context.Background(), replacementRun.ID, "done")
}

func TestCoordinator_ImplicitMessageKeyUsesEffectiveStoredPreview(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "implicit-preview.db")
	evidenceStore, err := evidence.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = evidenceStore.Close() })
	ledger, err := runledger.NewWithDB(evidenceStore.DB())
	if err != nil {
		t.Fatal(err)
	}
	var deliveries atomic.Int32
	manager := NewManager(interactiveRunnerFunc(func(ctx context.Context, _ Request, started func(int), commands <-chan CommandDelivery) (string, error) {
		started(101)
		for {
			select {
			case delivery := <-commands:
				deliveries.Add(1)
				delivery.Acknowledge(nil)
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	coordinator := NewCoordinator(manager, WithRunLedger(ledger), WithEvidence(evidenceStore))
	run, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{
		RunID: "run-implicit-preview", ParentSessionID: "session-implicit-preview", Task: "canonicalize previews",
	})
	if err != nil {
		t.Fatal(err)
	}
	send := func(content, preview string) agentcoord.Message {
		t.Helper()
		message, sendErr := coordinator.send(context.Background(), agentcoord.Message{
			RunID: run.ID, To: run.ID, Content: content, Preview: preview,
		}, messageAuthorityOperator)
		if sendErr != nil {
			t.Fatalf("send content=%q preview=%q: %v", content, preview, sendErr)
		}
		return message
	}
	first := send("same body", "preview one")
	exactRetry := send("same body", "preview one")
	differentPreview := send("same body", "preview two")
	derived := send("derive this preview", "")
	explicitDerived := send("derive this preview", runledger.CanonicalMailboxPreview("", "derive this preview"))
	if first.ID != exactRetry.ID || first.IdempotencyKey != exactRetry.IdempotencyKey {
		t.Fatalf("exact implicit retry diverged: first=%+v retry=%+v", first, exactRetry)
	}
	if first.ID == differentPreview.ID || first.IdempotencyKey == differentPreview.IdempotencyKey {
		t.Fatalf("effective preview drift did not change implicit identity: first=%+v different=%+v", first, differentPreview)
	}
	if derived.ID != explicitDerived.ID || derived.IdempotencyKey != explicitDerived.IdempotencyKey {
		t.Fatalf("omitted and derived previews diverged: omitted=%+v explicit=%+v", derived, explicitDerived)
	}
	if deliveries.Load() != 3 {
		t.Fatalf("live deliveries = %d, want 3 unique immutable envelopes", deliveries.Load())
	}
	messages, err := ledger.List(context.Background(), agentcoord.MailboxQuery{
		SessionID: run.SessionID, RunID: run.ID,
	})
	if err != nil || len(messages) != 3 {
		t.Fatalf("implicit preview mailbox = %+v, %v", messages, err)
	}
	_, _ = coordinator.Cancel(context.Background(), run.ID, "done")
}

func TestCoordinator_SendDeliversLiveAndPersistsDelivery(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ledger.db")
	evidenceStore, err := evidence.New(dbPath)
	if err != nil {
		t.Fatalf("open evidence: %v", err)
	}
	t.Cleanup(func() { _ = evidenceStore.Close() })
	ledger, err := runledger.NewWithDB(evidenceStore.DB())
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	received := make(chan agentcoord.Message, 1)
	manager := NewManager(interactiveRunnerFunc(func(ctx context.Context, _ Request, started func(int), commands <-chan CommandDelivery) (string, error) {
		started(10)
		select {
		case delivery := <-commands:
			received <- delivery.Message
			delivery.Acknowledge(nil)
		case <-ctx.Done():
			return "", ctx.Err()
		}
		<-ctx.Done()
		return "", ctx.Err()
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	coordinator := NewCoordinator(manager, WithRunLedger(ledger), WithEvidence(evidenceStore))
	if _, err := ledger.StartRun(context.Background(), runledger.AgentRun{RunID: "run-live-parent", SessionID: "session-live-mail", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	parentLease, err := ledger.Attach(context.Background(), agentcoord.AttachmentRequest{
		SessionID: "session-live-mail", RunID: "run-live-parent", AttemptID: "attempt-live-parent", LeaseDuration: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{RunID: "run-live-mail", ParentRunID: "run-live-parent", ParentSessionID: "session-live-mail", Task: "investigate"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	message, err := coordinator.Send(context.Background(), agentcoord.Message{
		RunID: run.ID, To: run.ID, From: "run-live-parent", Kind: "message", Content: "check the hot path",
		SourceAttemptID: parentLease.AttemptID, SourceLeaseGeneration: parentLease.LeaseGeneration,
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if message.Delivery != "delivered" {
		t.Fatalf("delivery = %q, want delivered", message.Delivery)
	}
	select {
	case got := <-received:
		if got.Content != "check the hot path" {
			t.Fatalf("received = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("interactive runner did not receive command")
	}
	messages, err := coordinator.Messages(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Delivery != "delivered" || messages[0].Content != "check the hot path" {
		t.Fatalf("messages = %+v", messages)
	}
	_, _ = coordinator.Cancel(context.Background(), run.ID, "test complete")
}

func TestCoordinator_DurableLifecycleStoresEvidenceAndSurvivesWorkerLoss(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ledger.db")
	evidenceStore, err := evidence.New(dbPath)
	if err != nil {
		t.Fatalf("open evidence: %v", err)
	}
	t.Cleanup(func() { _ = evidenceStore.Close() })
	ledger, err := runledger.NewWithDB(evidenceStore.DB())
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}

	manager := NewManager(runnerFunc(func(_ context.Context, _ Request, started func(int)) (string, error) {
		started(11)
		return "durable report", nil
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	coordinator := NewCoordinator(manager, WithRunLedger(ledger), WithEvidence(evidenceStore))
	run, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{
		RunID:           "run-durable",
		ID:              "task-durable",
		ParentSessionID: "session-durable",
		ParentRunID:     "run-parent",
		Agent:           "reviewer",
		Task:            "review the patch",
		WorkspaceClaims: []string{"pkg/subagent"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	completed, err := coordinator.Wait(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if completed.State != agentcoord.RunCompleted || completed.Result.Summary != "durable report" || len(completed.Result.EvidenceRefs) < 2 {
		t.Fatalf("completed run = %+v", completed)
	}
	var typedArtifact []byte
	for _, evidenceID := range completed.Result.EvidenceRefs {
		object, getErr := evidenceStore.Get(context.Background(), evidenceID)
		if getErr != nil || object.MediaType != artifactv1.MediaType {
			continue
		}
		typedArtifact = object.InlineBody
		break
	}
	if len(typedArtifact) == 0 {
		t.Fatalf("completed evidence = %v, want a typed artifact", completed.Result.EvidenceRefs)
	}
	artifact, _, err := artifactv1.DecodeProviderOutput(context.Background(), typedArtifact, artifactv1.OutputNativeJSONSchema, artifactv1.DecodeOptions{})
	if err != nil || artifact.Kind != artifactv1.KindSubagentResult || artifact.Status != artifactv1.StatusCompleted {
		t.Fatalf("stored subagent artifact = %+v, %v", artifact, err)
	}
	durable, err := ledger.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if durable.Status != string(agentcoord.RunCompleted) || durable.ParentRunID != "run-parent" {
		t.Fatalf("durable row = %+v", durable)
	}
	events, err := ledger.ListEvents(context.Background(), runledger.EventQuery{RunID: run.ID})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if !hasEvent(events, runledger.EventSubagentSpawned) || !hasEvent(events, runledger.EventSubagentCompleted) || !hasEvent(events, runledger.EventSubagentClaimed) || !hasEvent(events, runledger.EventSubagentReleased) {
		t.Fatalf("durable events = %+v", events)
	}

	if _, err := ledger.StartRun(context.Background(), runledger.AgentRun{
		RunID:     "run-lost-worker",
		SessionID: "session-durable",
		Backend:   "local-process",
		Status:    "running",
	}); err != nil {
		t.Fatalf("seed lost-worker run: %v", err)
	}
	recovered := NewCoordinator(nil, WithRunLedger(ledger), WithEvidence(evidenceStore))
	lost, err := recovered.Status(context.Background(), "run-lost-worker")
	if err != nil {
		t.Fatalf("Status after worker loss: %v", err)
	}
	if lost.State != agentcoord.RunResumable {
		t.Fatalf("lost worker state = %q, want resumable", lost.State)
	}
	queued, err := recovered.Steer(context.Background(), "run-lost-worker", "continue after reattach")
	if err != nil {
		t.Fatalf("Send after worker loss: %v", err)
	}
	if queued.Delivery != "queued" {
		t.Fatalf("delivery = %q, want durable queued", queued.Delivery)
	}
	messages, err := recovered.Messages(context.Background(), "run-lost-worker")
	if err != nil || len(messages) != 1 || messages[0].Content != "continue after reattach" {
		t.Fatalf("recovered mailbox = %+v, %v", messages, err)
	}
}

func TestCoordinator_DurableReportRecoversOutputBeyondSnapshotPreview(t *testing.T) {
	root := t.TempDir()
	evidenceStore, err := evidence.New(filepath.Join(root, "large-report.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = evidenceStore.Close() })
	ledger, err := runledger.NewWithDB(evidenceStore.DB())
	if err != nil {
		t.Fatal(err)
	}
	const sentinel = "MIDDLE-FINDING-MUST-SURVIVE"
	payload := strings.Repeat("a", 200*1024) + sentinel + strings.Repeat("z", 200*1024)
	spoolPath := filepath.Join(root, "subagent-report.log")
	if err := os.WriteFile(spoolPath, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(capturedRunnerFunc(func(context.Context, Request, func(int)) (CapturedOutput, error) {
		return CapturedOutput{
			Preview:       boundedOutput(payload),
			SpoolPath:     spoolPath,
			ObservedBytes: int64(len(payload)),
			CapturedBytes: int64(len(payload)),
			LimitBytes:    DefaultOutputSpoolLimit,
		}, nil
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	coordinator := NewCoordinator(manager, WithRunLedger(ledger), WithEvidence(evidenceStore))

	run, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{
		RunID:           "run-large-report",
		ID:              "task-large-report",
		ParentSessionID: "session-large-report",
		Agent:           "reviewer",
		Task:            "produce an exhaustive report",
	})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := coordinator.Wait(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != agentcoord.RunCompleted {
		t.Fatalf("completed = %+v", completed)
	}
	if strings.Contains(completed.Result.Summary, sentinel) {
		t.Fatal("test sentinel unexpectedly fit in the bounded snapshot preview")
	}
	found := false
	for _, evidenceID := range completed.Result.EvidenceRefs {
		object, getErr := evidenceStore.Get(context.Background(), evidenceID)
		if getErr == nil && object.MediaType == "application/json" && strings.Contains(string(object.InlineBody), sentinel) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("middle finding was absent from durable evidence refs %v", completed.Result.EvidenceRefs)
	}
	if _, err := os.Stat(spoolPath); !os.IsNotExist(err) {
		t.Fatalf("manager did not clean the consumed spool: %v", err)
	}
}

func TestCoordinator_HeartbeatKeepsLongRunOwnedPastLease(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "heartbeat.db")
	evidenceStore, err := evidence.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = evidenceStore.Close() })
	ledger, err := runledger.NewWithDB(evidenceStore.DB())
	if err != nil {
		t.Fatal(err)
	}
	attachments := &observingAttachmentStore{SQLiteStore: ledger, renewed: make(chan observedHeartbeat, 128)}
	runnerStarted := make(chan struct{})
	releaseRunner := make(chan struct{})
	manager := NewManager(runnerFunc(func(ctx context.Context, _ Request, started func(int)) (string, error) {
		if got := attachments.successful.Load(); got < 3 {
			return "", fmt.Errorf("runner started after only %d successful renewals", got)
		}
		started(91)
		close(runnerStarted)
		select {
		case <-releaseRunner:
			return "long run complete", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	coordinator := NewCoordinator(manager, WithRunLedger(ledger), WithEvidence(evidenceStore),
		WithAttachmentStore(attachments), WithAttachmentLease(200*time.Millisecond), WithHeartbeatInterval(10*time.Millisecond))
	run, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{
		RunID: "run-heartbeat", ParentSessionID: "session-heartbeat", Task: "run beyond one lease",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-runnerStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not start after initial heartbeat")
	}
	target := attachments.successful.Load() + 8
	var first, last agentcoord.AttachmentLease
	for last.LeaseGeneration == 0 || attachments.successful.Load() < target {
		select {
		case observed := <-attachments.renewed:
			if observed.lease.AttemptID != run.AttemptID || observed.lease.LeaseGeneration != run.LeaseGeneration {
				t.Fatalf("heartbeat %d renewed foreign lease %+v", observed.sequence, observed.lease)
			}
			if first.LeaseGeneration == 0 {
				first = observed.lease
			}
			last = observed.lease
		case <-time.After(5 * time.Second):
			t.Fatalf("observed %d successful renewals, want at least %d", attachments.successful.Load(), target)
		}
	}
	if !last.LeaseExpiresAt.After(first.LeaseExpiresAt) || !last.HeartbeatAt.After(first.HeartbeatAt) {
		t.Fatalf("lease did not advance across observed renewals: first=%+v last=%+v", first, last)
	}
	current, err := ledger.Current(context.Background(), run.SessionID, run.ID)
	if err != nil || current.AttemptID != run.AttemptID || current.LeaseGeneration != run.LeaseGeneration {
		t.Fatalf("current attachment = %+v, %v", current, err)
	}
	close(releaseRunner)
	finished, err := coordinator.Wait(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.State != agentcoord.RunCompleted || finished.Result.Summary != "long run complete" {
		t.Fatalf("finished = %+v", finished)
	}
	durable, err := ledger.GetRun(context.Background(), run.ID)
	if err != nil || durable.EndedAt == nil || durable.Status != "completed" {
		t.Fatalf("durable = %+v, %v", durable, err)
	}
}

func TestCoordinator_InitialHeartbeatWaitsForSQLiteContentionBeforeRunner(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "heartbeat-contention.db")
	evidenceStore, err := evidence.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = evidenceStore.Close() })
	ledger, err := runledger.NewWithDB(evidenceStore.DB())
	if err != nil {
		t.Fatal(err)
	}
	locker, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(0)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = locker.Close() })
	attachments := &contendedAttachmentStore{
		SQLiteStore: ledger,
		locker:      locker,
		blocked:     make(chan struct{}),
		release:     make(chan struct{}),
	}
	var runnerCalls atomic.Int32
	runnerStarted := make(chan struct{})
	manager := NewManager(runnerFunc(func(context.Context, Request, func(int)) (string, error) {
		runnerCalls.Add(1)
		close(runnerStarted)
		return "completed after contention", nil
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	coordinator := NewCoordinator(manager, WithRunLedger(ledger), WithEvidence(evidenceStore),
		WithAttachmentStore(attachments), WithAttachmentLease(2*time.Second), WithHeartbeatInterval(50*time.Millisecond))
	run, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{
		RunID: "run-heartbeat-contention", ParentSessionID: "session-heartbeat-contention", Task: "wait for exact renewal",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-attachments.blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("initial heartbeat did not encounter the held SQLite writer")
	}
	if runnerCalls.Load() != 0 {
		t.Fatalf("runner calls while initial heartbeat is contended = %d, want zero", runnerCalls.Load())
	}
	close(attachments.release)
	select {
	case <-runnerStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not start after SQLite contention cleared")
	}
	finished, err := coordinator.Wait(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.State != agentcoord.RunCompleted || finished.Result.Summary != "completed after contention" || runnerCalls.Load() != 1 {
		t.Fatalf("finished = %+v runner calls=%d", finished, runnerCalls.Load())
	}
}

func TestCoordinator_InitialHeartbeatSQLiteContentionTimesOutWithoutRunner(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "heartbeat-contention-timeout.db")
	evidenceStore, err := evidence.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = evidenceStore.Close() })
	ledger, err := runledger.NewWithDB(evidenceStore.DB())
	if err != nil {
		t.Fatal(err)
	}
	locker, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(0)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = locker.Close() })
	attachments := &contendedAttachmentStore{
		SQLiteStore: ledger,
		locker:      locker,
		blocked:     make(chan struct{}),
		release:     make(chan struct{}),
	}
	var runnerCalls atomic.Int32
	manager := NewManager(runnerFunc(func(context.Context, Request, func(int)) (string, error) {
		runnerCalls.Add(1)
		return "must not run", nil
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	coordinator := NewCoordinator(manager, WithRunLedger(ledger), WithEvidence(evidenceStore),
		WithAttachmentStore(attachments), WithAttachmentLease(100*time.Millisecond), WithHeartbeatInterval(10*time.Millisecond))
	run, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{
		RunID: "run-heartbeat-contention-timeout", ParentSessionID: "session-heartbeat-contention-timeout", Task: "fail closed on blocked renewal",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-attachments.blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("initial heartbeat did not encounter the held SQLite writer")
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	finished, err := coordinator.Wait(waitCtx, run.ID)
	close(attachments.release)
	if err != nil {
		t.Fatal(err)
	}
	if runnerCalls.Load() != 0 || finished.State != agentcoord.RunFailed || !strings.Contains(finished.Result.Error, context.DeadlineExceeded.Error()) {
		t.Fatalf("finished = %+v runner calls=%d", finished, runnerCalls.Load())
	}
	if strings.Contains(finished.Result.Error, errHeartbeatRenewalTimeout.Error()) {
		t.Fatalf("private renewal cause leaked into public result: %q", finished.Result.Error)
	}
	durable, err := ledger.GetRun(context.Background(), run.ID)
	if err != nil || durable.EndedAt != nil {
		t.Fatalf("timed-out renewal mutated canonical terminal state = %+v, %v", durable, err)
	}
}

func TestCoordinator_CancelDuringBlockedInitialRenewalSkipsRunner(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "heartbeat-contention-cancel.db")
	evidenceStore, err := evidence.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = evidenceStore.Close() })
	ledger, err := runledger.NewWithDB(evidenceStore.DB())
	if err != nil {
		t.Fatal(err)
	}
	attachments := &gatedAttachmentStore{SQLiteStore: ledger, blocked: make(chan struct{}), release: make(chan struct{})}
	var runnerCalls atomic.Int32
	manager := NewManager(runnerFunc(func(context.Context, Request, func(int)) (string, error) {
		runnerCalls.Add(1)
		return "must not run", nil
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	coordinator := NewCoordinator(manager, WithRunLedger(ledger), WithEvidence(evidenceStore),
		WithAttachmentStore(attachments), WithAttachmentLease(2*time.Second), WithHeartbeatInterval(50*time.Millisecond))
	run, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{
		RunID: "run-heartbeat-contention-cancel", ParentSessionID: "session-heartbeat-contention-cancel", Task: "cancel before launch",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-attachments.blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("initial heartbeat did not block")
	}
	if _, err := manager.Cancel(run.ID); err != nil {
		t.Fatal(err)
	}
	close(attachments.release)
	finished, err := coordinator.Wait(context.Background(), run.ID)
	if err != nil || runnerCalls.Load() != 0 || finished.State != agentcoord.RunCancelled {
		t.Fatalf("finished = %+v, %v runner calls=%d", finished, err, runnerCalls.Load())
	}
}

func TestCoordinator_DeadlineDuringBlockedInitialRenewalSkipsRunner(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "heartbeat-contention-deadline.db")
	evidenceStore, err := evidence.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = evidenceStore.Close() })
	ledger, err := runledger.NewWithDB(evidenceStore.DB())
	if err != nil {
		t.Fatal(err)
	}
	attachments := &gatedAttachmentStore{SQLiteStore: ledger, blocked: make(chan struct{}), release: make(chan struct{})}
	var runnerCalls atomic.Int32
	manager := NewManager(runnerFunc(func(context.Context, Request, func(int)) (string, error) {
		runnerCalls.Add(1)
		return "must not run", nil
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	coordinator := NewCoordinator(manager, WithRunLedger(ledger), WithEvidence(evidenceStore),
		WithAttachmentStore(attachments), WithAttachmentLease(4*time.Second), WithHeartbeatInterval(100*time.Millisecond))
	run, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{
		RunID: "run-heartbeat-contention-deadline", ParentSessionID: "session-heartbeat-contention-deadline", Task: "deadline before launch",
		TimeoutSeconds: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-attachments.blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("initial heartbeat did not block")
	}
	manager.mu.RLock()
	deadlineDone := manager.runs[run.ID].ctx.Done()
	manager.mu.RUnlock()
	select {
	case <-deadlineDone:
	case <-time.After(5 * time.Second):
		t.Fatal("task deadline did not fire")
	}
	close(attachments.release)
	finished, err := coordinator.Wait(context.Background(), run.ID)
	if err != nil || runnerCalls.Load() != 0 || finished.State != agentcoord.RunFailed || !strings.Contains(finished.Result.Error, "elapsed-time limit") {
		t.Fatalf("finished = %+v, %v runner calls=%d", finished, err, runnerCalls.Load())
	}
	if got := attachments.calls.Load(); got != 3 {
		t.Fatalf("heartbeat calls = %d, want exactly three", got)
	}
	if strings.Contains(finished.Result.Error, "durability heartbeat") || strings.Contains(finished.Result.Error, sql.ErrTxDone.Error()) {
		t.Fatalf("deadline classification leaked heartbeat failure: %q", finished.Result.Error)
	}
}

func TestCoordinator_InitialHeartbeatFailureOutranksConcurrentTaskExit(t *testing.T) {
	failures := []struct {
		name string
		err  error
	}{
		{name: "stale", err: runledger.ErrAttachmentStale},
		{name: "expired", err: runledger.ErrAttachmentExpired},
		{name: "terminal", err: runledger.ErrAttachmentTerminal},
		{name: "renewal timeout", err: fmt.Errorf("renewal timed out: %w", context.DeadlineExceeded)},
		{name: "busy", err: errors.New("database is busy")},
	}
	exits := []string{"cancel", "deadline"}
	for _, exitMode := range exits {
		for _, failure := range failures {
			t.Run(exitMode+"/"+failure.name, func(t *testing.T) {
				suffix := strings.NewReplacer(" ", "-", "/", "-").Replace(exitMode + "-" + failure.name)
				dbPath := filepath.Join(t.TempDir(), "heartbeat-initial-race.db")
				evidenceStore, err := evidence.New(dbPath)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = evidenceStore.Close() })
				ledger, err := runledger.NewWithDB(evidenceStore.DB())
				if err != nil {
					t.Fatal(err)
				}
				attachments := &initialHeartbeatFailureStore{
					SQLiteStore: ledger,
					blocked:     make(chan struct{}),
					release:     make(chan struct{}),
					err:         failure.err,
				}
				var runnerCalls atomic.Int32
				manager := NewManager(runnerFunc(func(context.Context, Request, func(int)) (string, error) {
					runnerCalls.Add(1)
					return "must not run", nil
				}), 1)
				t.Cleanup(func() { _ = manager.Close() })
				coordinator := NewCoordinator(manager, WithRunLedger(ledger), WithEvidence(evidenceStore),
					WithAttachmentStore(attachments), WithAttachmentLease(4*time.Second), WithHeartbeatInterval(100*time.Millisecond))
				run, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{
					RunID: "run-initial-race-" + suffix, ParentSessionID: "session-initial-race-" + suffix,
					Task: "initial heartbeat race", WorkspaceClaims: []string{"pkg/initial-race-" + suffix}, TimeoutSeconds: 1,
				})
				if err != nil {
					t.Fatal(err)
				}
				select {
				case <-attachments.blocked:
				case <-time.After(5 * time.Second):
					t.Fatal("initial heartbeat did not block")
				}
				if exitMode == "cancel" {
					if _, err := manager.Cancel(run.ID); err != nil {
						t.Fatal(err)
					}
				} else {
					manager.mu.RLock()
					deadlineDone := manager.runs[run.ID].ctx.Done()
					manager.mu.RUnlock()
					select {
					case <-deadlineDone:
					case <-time.After(5 * time.Second):
						t.Fatal("task deadline did not fire")
					}
				}
				close(attachments.release)
				finished, err := coordinator.Wait(context.Background(), run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if runnerCalls.Load() != 0 || finished.State != agentcoord.RunFailed || !strings.Contains(finished.Result.Error, "durability heartbeat failed") {
					t.Fatalf("finished = %+v runner calls=%d", finished, runnerCalls.Load())
				}
				if attachments.calls.Load() != 3 {
					t.Fatalf("heartbeat calls = %d, want exactly three", attachments.calls.Load())
				}
				durable, err := ledger.GetRun(context.Background(), run.ID)
				if err != nil || durable.EndedAt != nil {
					t.Fatalf("initial failure mutated canonical terminal state = %+v, %v", durable, err)
				}
				claims, err := ledger.ListClaims(context.Background(), runledger.ClaimQuery{RunID: run.ID})
				if err != nil || len(claims) != 1 {
					t.Fatalf("initial failure claims = %+v, %v", claims, err)
				}
				events, err := ledger.ListEvents(context.Background(), runledger.EventQuery{
					RunID: run.ID, Types: []string{runledger.EventSubagentCompleted, runledger.EventSubagentFailed, runledger.EventSubagentReleased},
				})
				if err != nil || len(events) != 0 {
					t.Fatalf("initial failure terminal events = %+v, %v", events, err)
				}
			})
		}
	}
}

func TestCoordinator_HeartbeatTimingPreservesLeaseMargin(t *testing.T) {
	tests := []struct {
		name            string
		lease           time.Duration
		requested       time.Duration
		wantInterval    time.Duration
		wantRenewalTime time.Duration
		wantError       bool
	}{
		{name: "sub millisecond rejected", lease: 900 * time.Microsecond, requested: 0, wantError: true},
		{name: "one millisecond rejected", lease: time.Millisecond, requested: time.Second, wantError: true},
		{name: "two milliseconds rejected", lease: 2 * time.Millisecond, requested: time.Second, wantError: true},
		{name: "minimum boundary", lease: minimumAttachmentLease, requested: 0, wantInterval: minimumHeartbeatInterval, wantRenewalTime: 15 * time.Millisecond},
		{name: "normal default", lease: 30 * time.Second, requested: 10 * time.Second, wantInterval: 10 * time.Second, wantRenewalTime: 15 * time.Second},
		{name: "above one third", lease: 900 * time.Millisecond, requested: 800 * time.Millisecond, wantInterval: 300 * time.Millisecond, wantRenewalTime: 450 * time.Millisecond},
		{name: "below one third", lease: 900 * time.Millisecond, requested: 50 * time.Millisecond, wantInterval: 50 * time.Millisecond, wantRenewalTime: 450 * time.Millisecond},
		{name: "spin floor", lease: 900 * time.Millisecond, requested: time.Microsecond, wantInterval: minimumHeartbeatInterval, wantRenewalTime: 450 * time.Millisecond},
		{name: "maximum", lease: runledger.AttachmentMaxLease, requested: runledger.AttachmentMaxLease, wantInterval: runledger.AttachmentMaxLease / 3, wantRenewalTime: runledger.AttachmentMaxLease / 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			interval, timeout, err := heartbeatTimingForLease(test.lease, test.requested)
			if test.wantError {
				if err == nil || interval != 0 || timeout != 0 {
					t.Fatalf("heartbeat timing = %s/%s, %v; want rejected without timers", interval, timeout, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if interval != test.wantInterval || timeout != test.wantRenewalTime {
				t.Fatalf("heartbeat timing = %s/%s, want %s/%s", interval, timeout, test.wantInterval, test.wantRenewalTime)
			}
			if interval <= 0 || timeout <= 0 || interval+timeout >= test.lease {
				t.Fatalf("heartbeat timing escaped lease: interval=%s timeout=%s lease=%s", interval, timeout, test.lease)
			}
		})
	}
}

func TestCoordinator_CanonicalAttachmentLeaseBounds(t *testing.T) {
	tests := []struct {
		name      string
		requested time.Duration
		want      time.Duration
		wantError bool
	}{
		{name: "default", want: runledger.AttachmentDefaultLease},
		{name: "negative", requested: -time.Nanosecond, wantError: true},
		{name: "sub millisecond", requested: 500 * time.Microsecond, wantError: true},
		{name: "below minimum", requested: minimumAttachmentLease - time.Nanosecond, wantError: true},
		{name: "minimum", requested: minimumAttachmentLease, want: minimumAttachmentLease},
		{name: "maximum", requested: runledger.AttachmentMaxLease, want: runledger.AttachmentMaxLease},
		{name: "above maximum", requested: runledger.AttachmentMaxLease + time.Hour, want: runledger.AttachmentMaxLease},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := canonicalAttachmentLease(test.requested)
			if test.wantError {
				if err == nil || got != 0 {
					t.Fatalf("canonical lease = %s, %v; want rejection", got, err)
				}
				if test.requested >= 0 && test.requested < minimumAttachmentLease && !errors.Is(err, ErrAttachmentLeaseTooShort) {
					t.Fatalf("canonical lease error = %v, want ErrAttachmentLeaseTooShort", err)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("canonical lease = %s, %v; want %s", got, err, test.want)
			}
		})
	}
	if _, err := validateCanonicalAttachmentLease(runledger.AttachmentMaxLease + time.Hour); err == nil {
		t.Fatal("revalidation silently changed a previously established authority window")
	}
}

func TestCoordinator_InvalidAttachmentLeaseFailsBeforeDurableSideEffects(t *testing.T) {
	for _, lease := range []time.Duration{-time.Nanosecond, minimumAttachmentLease - time.Nanosecond} {
		lease := lease
		t.Run(lease.String(), func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "invalid-lease.db")
			evidenceStore, err := evidence.New(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = evidenceStore.Close() })
			ledger, err := runledger.NewWithDB(evidenceStore.DB())
			if err != nil {
				t.Fatal(err)
			}
			var runnerCalls atomic.Int32
			manager := NewManager(runnerFunc(func(context.Context, Request, func(int)) (string, error) {
				runnerCalls.Add(1)
				return "must not run", nil
			}), 1)
			t.Cleanup(func() { _ = manager.Close() })
			coordinator := NewCoordinator(manager, WithRunLedger(ledger), WithEvidence(evidenceStore), WithAttachmentLease(lease))

			_, err = coordinator.Spawn(context.Background(), agentcoord.TaskSpec{
				RunID: "run-invalid-lease", ParentSessionID: "session-invalid-lease", Task: "must fail before persistence",
			})
			if err == nil || !strings.Contains(err.Error(), "attachment timing") {
				t.Fatalf("Spawn error = %v, want attachment timing rejection", err)
			}
			if lease >= 0 && !errors.Is(err, ErrAttachmentLeaseTooShort) {
				t.Fatalf("Spawn error = %v, want ErrAttachmentLeaseTooShort", err)
			}
			if runnerCalls.Load() != 0 {
				t.Fatalf("runner calls = %d, want zero", runnerCalls.Load())
			}
			for _, table := range []string{"evidence_objects", "agent_runs", "agent_run_attempts"} {
				var count int
				if err := evidenceStore.DB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
					t.Fatalf("count %s: %v", table, err)
				}
				if count != 0 {
					t.Fatalf("%s rows = %d, want zero", table, count)
				}
			}
		})
	}
}

func TestCoordinator_MinimumAttachmentLeaseIsAccepted(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "minimum-lease.db")
	evidenceStore, err := evidence.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = evidenceStore.Close() })
	ledger, err := runledger.NewWithDB(evidenceStore.DB())
	if err != nil {
		t.Fatal(err)
	}
	attachments := &observingAttachmentStore{SQLiteStore: ledger, renewed: make(chan observedHeartbeat, 16)}
	manager := NewManager(runnerFunc(func(context.Context, Request, func(int)) (string, error) {
		return "minimum accepted", nil
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	coordinator := NewCoordinator(manager, WithRunLedger(ledger), WithEvidence(evidenceStore),
		WithAttachmentStore(attachments), WithAttachmentLease(minimumAttachmentLease), WithHeartbeatInterval(minimumHeartbeatInterval))
	if coordinator.configurationErr != nil || coordinator.attachmentLease != minimumAttachmentLease {
		t.Fatalf("minimum lease configuration = %s, %v", coordinator.attachmentLease, coordinator.configurationErr)
	}
	run, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{
		RunID: "run-minimum-lease", ParentSessionID: "session-minimum-lease", Task: "accept exact minimum",
	})
	if err != nil {
		t.Fatal(err)
	}
	finished, err := coordinator.Wait(context.Background(), run.ID)
	if err != nil || finished.State != agentcoord.RunCompleted {
		t.Fatalf("finished = %+v, %v", finished, err)
	}
}

func TestCoordinator_AboveMaximumLeaseMatchesPersistedExpiry(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "maximum-lease.db")
	evidenceStore, err := evidence.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = evidenceStore.Close() })
	ledger, err := runledger.NewWithDB(evidenceStore.DB())
	if err != nil {
		t.Fatal(err)
	}
	runnerStarted := make(chan struct{})
	releaseRunner := make(chan struct{})
	manager := NewManager(runnerFunc(func(ctx context.Context, _ Request, _ func(int)) (string, error) {
		close(runnerStarted)
		select {
		case <-releaseRunner:
			return "maximum accepted", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	coordinator := NewCoordinator(manager, WithRunLedger(ledger), WithEvidence(evidenceStore),
		WithAttachmentLease(runledger.AttachmentMaxLease+time.Hour))
	if coordinator.configurationErr != nil || coordinator.attachmentLease != runledger.AttachmentMaxLease {
		t.Fatalf("maximum lease configuration = %s, %v", coordinator.attachmentLease, coordinator.configurationErr)
	}
	run, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{
		RunID: "run-maximum-lease", ParentSessionID: "session-maximum-lease", Task: "persist exact maximum",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-runnerStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not start")
	}
	lease, err := ledger.Current(context.Background(), run.SessionID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := lease.LeaseExpiresAt.Sub(lease.HeartbeatAt); got != runledger.AttachmentMaxLease {
		t.Fatalf("persisted lease duration = %s, want %s", got, runledger.AttachmentMaxLease)
	}
	close(releaseRunner)
	finished, err := coordinator.Wait(context.Background(), run.ID)
	if err != nil || finished.State != agentcoord.RunCompleted {
		t.Fatalf("finished = %+v, %v", finished, err)
	}
}

func TestCoordinator_HeartbeatOwnershipLossBlocksTerminalMutationAndClaimRelease(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "heartbeat-loss.db")
	evidenceStore, err := evidence.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = evidenceStore.Close() })
	ledger, err := runledger.NewWithDB(evidenceStore.DB())
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(runnerFunc(func(ctx context.Context, _ Request, started func(int)) (string, error) {
		started(92)
		<-ctx.Done()
		return "", ctx.Err()
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	attachments := &losingAttachmentStore{SQLiteStore: ledger}
	coordinator := NewCoordinator(manager, WithRunLedger(ledger), WithEvidence(evidenceStore),
		WithAttachmentStore(attachments),
		WithAttachmentLease(time.Second), WithHeartbeatInterval(10*time.Millisecond))
	run, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{
		RunID: "run-heartbeat-loss", ParentSessionID: "session-heartbeat-loss", Task: "lose ownership",
		WorkspaceClaims: []string{"pkg/owned"},
	})
	if err != nil {
		t.Fatal(err)
	}
	finished, err := coordinator.Wait(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.State != agentcoord.RunFailed || !strings.Contains(finished.Result.Error, "attachment generation is stale") {
		t.Fatalf("finished = %+v", finished)
	}
	if attachments.heartbeats.Load() != 4 || finished.PID != 92 {
		t.Fatalf("heartbeat calls/PID = %d/%d, want fourth-call loss after runner launch", attachments.heartbeats.Load(), finished.PID)
	}
	durable, err := ledger.GetRun(context.Background(), run.ID)
	if err != nil || durable.EndedAt != nil {
		t.Fatalf("ownership-lost durable row = %+v, %v", durable, err)
	}
	claims, err := ledger.ListClaims(context.Background(), runledger.ClaimQuery{RunID: run.ID})
	if err != nil || len(claims) != 1 {
		t.Fatalf("ownership-lost claims = %+v, %v", claims, err)
	}
}

func TestCoordinator_ExternalTerminalizationStopsRunnerWithoutLocalFinalization(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "heartbeat-external-terminal.db")
	evidenceStore, err := evidence.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = evidenceStore.Close() })
	ledger, err := runledger.NewWithDB(evidenceStore.DB())
	if err != nil {
		t.Fatal(err)
	}
	attachments := &terminalizingAttachmentStore{SQLiteStore: ledger}
	runnerStarted := make(chan struct{})
	manager := NewManager(runnerFunc(func(ctx context.Context, _ Request, started func(int)) (string, error) {
		started(93)
		close(runnerStarted)
		<-ctx.Done()
		return "", ctx.Err()
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	coordinator := NewCoordinator(manager, WithRunLedger(ledger), WithEvidence(evidenceStore),
		WithAttachmentStore(attachments), WithAttachmentLease(time.Second), WithHeartbeatInterval(10*time.Millisecond))
	run, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{
		RunID: "run-external-terminal", ParentSessionID: "session-external-terminal", Task: "stop after canonical terminalization",
		WorkspaceClaims: []string{"pkg/external-owned"},
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-runnerStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not start after three successful renewals")
	}
	finished, err := coordinator.Wait(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.State != agentcoord.RunFailed || !strings.Contains(finished.Result.Error, runledger.ErrAttachmentTerminal.Error()) {
		t.Fatalf("local result = %+v, want terminal ownership failure", finished)
	}
	if calls := attachments.heartbeats.Load(); calls != 4 {
		t.Fatalf("heartbeat calls = %d, want exactly four", calls)
	}
	durable, err := ledger.GetRun(context.Background(), run.ID)
	if err != nil || durable.Status != "completed" || durable.EndedAt == nil || mapString(durable.Outcome, "summary") != "external terminal" {
		t.Fatalf("canonical row changed = %+v, %v", durable, err)
	}
	claims, err := ledger.ListClaims(context.Background(), runledger.ClaimQuery{RunID: run.ID})
	if err != nil || len(claims) != 1 {
		t.Fatalf("external-terminal claims = %+v, %v", claims, err)
	}
	events, err := ledger.ListEvents(context.Background(), runledger.EventQuery{
		RunID: run.ID, Types: []string{runledger.EventSubagentCompleted, runledger.EventSubagentFailed, runledger.EventSubagentReleased},
	})
	if err != nil || len(events) != 0 {
		t.Fatalf("local terminal events = %+v, %v", events, err)
	}
}

func TestCoordinator_LargeRunnerErrorStillFinalizesWithRawEvidence(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "large-error.db")
	evidenceStore, err := evidence.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = evidenceStore.Close() })
	ledger, err := runledger.NewWithDB(evidenceStore.DB())
	if err != nil {
		t.Fatal(err)
	}
	const tail = "large-error-tail-marker"
	rawError := strings.Repeat("runner failure detail ", 4096) + tail
	manager := NewManager(runnerFunc(func(context.Context, Request, func(int)) (string, error) {
		return "", errors.New(rawError)
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	coordinator := NewCoordinator(manager, WithRunLedger(ledger), WithEvidence(evidenceStore))
	spawned, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{
		RunID: "run-large-error", ParentSessionID: "session-large-error", Task: "fail verbosely",
		WorkspaceClaims: []string{"pkg/large-error"},
	})
	if err != nil {
		t.Fatal(err)
	}
	finished, err := coordinator.Wait(context.Background(), spawned.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.State != agentcoord.RunFailed || len(finished.Result.Error) > 1024 || finished.Result.Error == "" {
		t.Fatalf("bounded failed result = state=%s error-bytes=%d", finished.State, len(finished.Result.Error))
	}
	durable, err := ledger.GetRun(context.Background(), spawned.ID)
	if err != nil || durable.EndedAt == nil || durable.Status != string(agentcoord.RunFailed) {
		t.Fatalf("durable terminal run = %+v, %v", durable, err)
	}
	if outcomeError, _ := durable.Outcome["error"].(string); len(outcomeError) == 0 || len(outcomeError) > 1024 {
		t.Fatalf("durable outcome error bytes = %d", len(outcomeError))
	}
	claims, err := ledger.ListClaims(context.Background(), runledger.ClaimQuery{RunID: spawned.ID})
	if err != nil || len(claims) != 0 {
		t.Fatalf("terminal claims = %+v, %v", claims, err)
	}
	if _, err := ledger.Current(context.Background(), spawned.SessionID, spawned.ID); !errors.Is(err, runledger.ErrAttachmentNotFound) {
		t.Fatalf("terminal attachment = %v, want detached", err)
	}
	rawFound := false
	for _, evidenceID := range finished.Result.EvidenceRefs {
		object, getErr := evidenceStore.Get(context.Background(), evidenceID)
		if getErr == nil && strings.Contains(string(object.InlineBody), tail) {
			rawFound = true
			break
		}
	}
	if !rawFound {
		t.Fatalf("raw runner error missing from pinned evidence %v", finished.Result.EvidenceRefs)
	}
	events, err := ledger.ListEvents(context.Background(), runledger.EventQuery{RunID: spawned.ID})
	if err != nil || !hasEvent(events, runledger.EventSubagentFailed) || !hasEvent(events, runledger.EventSubagentReleased) {
		t.Fatalf("terminal audit events = %+v, %v", events, err)
	}
}

func TestCoordinator_TerminalRalphFailureKeepsCommittedLifecycleAuthoritative(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "terminal-ralph.db")
	evidenceStore, err := evidence.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = evidenceStore.Close() })
	ledger, err := runledger.NewWithDB(evidenceStore.DB())
	if err != nil {
		t.Fatal(err)
	}
	sink := &toggleRalphSink{}
	ledger.SetRalphSink(sink)
	release := make(chan struct{})
	manager := NewManager(runnerFunc(func(context.Context, Request, func(int)) (string, error) {
		<-release
		return "completed despite secondary sink outage", nil
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	coordinator := NewCoordinator(manager, WithRunLedger(ledger), WithEvidence(evidenceStore))
	spawned, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{
		RunID: "run-terminal-ralph", ParentSessionID: "session-terminal-ralph", Task: "finish durably",
		WorkspaceClaims: []string{"pkg/terminal-ralph"},
	})
	if err != nil {
		t.Fatal(err)
	}
	sink.fail.Store(true)
	close(release)
	finished, err := coordinator.Wait(context.Background(), spawned.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.State != agentcoord.RunCompleted {
		t.Fatalf("committed terminal projection = %+v", finished)
	}
	durable, err := ledger.GetRun(context.Background(), spawned.ID)
	if err != nil || durable.EndedAt == nil || durable.Status != string(agentcoord.RunCompleted) {
		t.Fatalf("committed durable run = %+v, %v", durable, err)
	}
	claims, err := ledger.ListClaims(context.Background(), runledger.ClaimQuery{RunID: spawned.ID})
	if err != nil || len(claims) != 0 {
		t.Fatalf("committed claims = %+v, %v", claims, err)
	}
	var failed int
	if err := ledger.DB().QueryRow(`SELECT COUNT(*) FROM run_event_ralph_outbox WHERE state = 'failed'`).Scan(&failed); err != nil || failed != 2 {
		t.Fatalf("failed terminal outbox rows=%d, err=%v", failed, err)
	}
	sink.fail.Store(false)
	ledger.SetRalphSink(sink)
	if err := ledger.DB().QueryRow(`SELECT COUNT(*) FROM run_event_ralph_outbox WHERE state <> 'delivered'`).Scan(&failed); err != nil || failed != 0 {
		t.Fatalf("recovered terminal outbox rows=%d, err=%v", failed, err)
	}
}

func TestCoordinator_SpawnRetryRepairsStableEventAndLaunchesOnce(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "spawn-retry.db")
	evidenceStore, err := evidence.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = evidenceStore.Close() })
	base, err := runledger.NewWithDB(evidenceStore.DB())
	if err != nil {
		t.Fatal(err)
	}
	ledger := &flakyAppendLedger{SQLiteStore: base, failType: runledger.EventSubagentSpawned}
	ledger.failures.Store(1)
	release := make(chan struct{})
	var launches atomic.Int32
	manager := NewManager(runnerFunc(func(ctx context.Context, _ Request, started func(int)) (string, error) {
		launches.Add(1)
		started(93)
		select {
		case <-release:
			return "recovered", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	coordinator := NewCoordinator(manager, WithRunLedger(ledger), WithEvidence(evidenceStore))
	spec := agentcoord.TaskSpec{RunID: "run-spawn-retry", ParentSessionID: "session-spawn-retry", Task: "retry stable spawn"}
	if _, err := coordinator.Spawn(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "injected append failure") {
		t.Fatalf("first Spawn = %v", err)
	}
	if launches.Load() != 0 {
		t.Fatalf("launches after failed durable phase = %d", launches.Load())
	}
	if _, err := base.GetRun(context.Background(), spec.RunID); err != nil {
		t.Fatalf("queued run was not recoverable: %v", err)
	}
	if _, err := base.Current(context.Background(), spec.ParentSessionID, spec.RunID); err != nil {
		t.Fatalf("persisted attachment was not recoverable: %v", err)
	}
	run, err := coordinator.Spawn(context.Background(), spec)
	if err != nil {
		t.Fatalf("retry Spawn: %v", err)
	}
	if _, err := coordinator.Spawn(context.Background(), spec); err != nil {
		t.Fatalf("idempotent running Spawn: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for launches.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if launches.Load() != 1 {
		t.Fatalf("launches = %d, want 1", launches.Load())
	}
	changed := spec
	changed.Task = "different task"
	if _, err := coordinator.Spawn(context.Background(), changed); !errors.Is(err, runledger.ErrRunContractConflict) {
		t.Fatalf("changed Spawn = %v, want ErrRunContractConflict", err)
	}
	close(release)
	if _, err := coordinator.Wait(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	events, err := base.ListEvents(context.Background(), runledger.EventQuery{RunID: run.ID, Types: []string{runledger.EventSubagentSpawned}})
	if err != nil || len(events) != 1 {
		t.Fatalf("spawn events = %+v, %v", events, err)
	}
}

func TestCoordinator_CrashAfterAttachmentRequiresExpiryBeforeNewProcessLaunch(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "spawn-crash.db")
	evidenceStore, err := evidence.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = evidenceStore.Close() })
	base, err := runledger.NewWithDB(evidenceStore.DB())
	if err != nil {
		t.Fatal(err)
	}
	flaky := &flakyAppendLedger{SQLiteStore: base, failType: runledger.EventSubagentSpawned}
	flaky.failures.Store(1)
	firstManager := NewManager(runnerFunc(func(context.Context, Request, func(int)) (string, error) {
		t.Fatal("first manager launched before spawn event")
		return "", nil
	}), 1)
	t.Cleanup(func() { _ = firstManager.Close() })
	spec := agentcoord.TaskSpec{RunID: "run-spawn-crash", ParentSessionID: "session-spawn-crash", Task: "recover after crash"}
	first := NewCoordinator(firstManager, WithRunLedger(flaky), WithEvidence(evidenceStore), WithAttachmentLease(200*time.Millisecond), WithHeartbeatInterval(30*time.Millisecond))
	if _, err := first.Spawn(context.Background(), spec); err == nil {
		t.Fatal("first Spawn unexpectedly succeeded")
	}
	var launches atomic.Int32
	secondManager := NewManager(runnerFunc(func(context.Context, Request, func(int)) (string, error) {
		launches.Add(1)
		return "resumed", nil
	}), 1)
	t.Cleanup(func() { _ = secondManager.Close() })
	second := NewCoordinator(secondManager, WithRunLedger(base), WithEvidence(evidenceStore), WithAttachmentLease(200*time.Millisecond), WithHeartbeatInterval(30*time.Millisecond))
	listed, err := second.List(context.Background(), agentcoord.AgentRunFilter{SessionID: spec.ParentSessionID})
	if err != nil || len(listed) != 1 || listed[0].Task.Task != spec.Task || listed[0].AttemptID == "" || listed[0].LeaseGeneration != 1 {
		t.Fatalf("restart reconstruction = %+v, %v", listed, err)
	}
	resumable, err := second.Spawn(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if resumable.State != agentcoord.RunResumable || launches.Load() != 0 {
		t.Fatalf("foreign live attachment retry = %+v launches=%d", resumable, launches.Load())
	}
	waitForDurableAttachmentExpiry(t, base, spec.ParentSessionID, spec.RunID)
	run, err := second.Spawn(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if run.LeaseGeneration != 2 {
		t.Fatalf("replacement generation = %d, want 2", run.LeaseGeneration)
	}
	if _, err := second.Wait(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	if launches.Load() != 1 {
		t.Fatalf("replacement launches = %d, want 1", launches.Load())
	}
}

func waitForDurableAttachmentExpiry(t *testing.T, store *runledger.SQLiteStore, sessionID, runID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, err := store.Current(context.Background(), sessionID, runID)
		if errors.Is(err, runledger.ErrAttachmentExpired) {
			return
		}
		if err != nil {
			t.Fatalf("wait for durable attachment expiry: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for durable attachment expiry")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCoordinator_DuplicateSendRepairsFailedAuditAppend(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "message-repair.db")
	evidenceStore, err := evidence.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = evidenceStore.Close() })
	base, err := runledger.NewWithDB(evidenceStore.DB())
	if err != nil {
		t.Fatal(err)
	}
	ledger := &flakyAppendLedger{SQLiteStore: base, failType: runledger.EventSubagentMessageSent}
	ledger.failures.Store(1)
	manager := NewManager(runnerFunc(func(ctx context.Context, _ Request, started func(int)) (string, error) {
		started(94)
		<-ctx.Done()
		return "", ctx.Err()
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	coordinator := NewCoordinator(manager, WithRunLedger(ledger), WithEvidence(evidenceStore))
	if _, err := base.StartRun(context.Background(), runledger.AgentRun{RunID: "run-message-parent", SessionID: "session-message-repair", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	parentLease, err := base.Attach(context.Background(), agentcoord.AttachmentRequest{
		SessionID: "session-message-repair", RunID: "run-message-parent", AttemptID: "attempt-message-parent", LeaseDuration: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{
		RunID: "run-message-repair", ParentRunID: "run-message-parent", ParentSessionID: "session-message-repair", Task: "receive message",
	})
	if err != nil {
		t.Fatal(err)
	}
	message := agentcoord.Message{
		ID: "msg-stable", IdempotencyKey: "logical-message", RunID: run.ID, To: run.ID,
		From: "run-message-parent", Kind: "message", Content: "repair my audit",
		SourceAttemptID: parentLease.AttemptID, SourceLeaseGeneration: parentLease.LeaseGeneration,
	}
	if _, err := coordinator.Send(context.Background(), message); err == nil || !strings.Contains(err.Error(), "injected append failure") {
		t.Fatalf("first Send = %v", err)
	}
	if _, err := coordinator.Send(context.Background(), message); err != nil {
		t.Fatalf("repair Send: %v", err)
	}
	rows, err := base.List(context.Background(), agentcoord.MailboxQuery{SessionID: run.SessionID, RunID: run.ID})
	if err != nil || len(rows) != 1 {
		t.Fatalf("mailbox rows = %+v, %v", rows, err)
	}
	if rows[0].ContentRef == "" {
		t.Fatal("visible durable message has no evidence reference")
	}
	if _, err := evidenceStore.Get(context.Background(), rows[0].ContentRef); err != nil {
		t.Fatalf("visible durable message evidence: %v", err)
	}
	var pins int
	if err := evidenceStore.DB().QueryRow(`SELECT COUNT(*) FROM evidence_pins WHERE evidence_id = ? AND reason = ?`, rows[0].ContentRef, "run:"+run.ID).Scan(&pins); err != nil || pins != 1 {
		t.Fatalf("visible durable message pins=%d, err=%v", pins, err)
	}
	events, err := base.ListEvents(context.Background(), runledger.EventQuery{RunID: run.ID, Types: []string{runledger.EventSubagentMessageSent}})
	if err != nil || len(events) != 1 {
		t.Fatalf("message events = %+v, %v", events, err)
	}
	_, _ = coordinator.Cancel(context.Background(), run.ID, "done")
}

func TestCoordinator_PublishAuthorizesExactChildAndRecordedParent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "publish-auth.db")
	evidenceStore, err := evidence.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = evidenceStore.Close() })
	ledger, err := runledger.NewWithDB(evidenceStore.DB())
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range []runledger.AgentRun{
		{RunID: "run-parent", SessionID: "session-publish", Backend: "local-process", Status: "running"},
		{RunID: "run-not-parent", SessionID: "session-publish", Backend: "local-process", Status: "running"},
	} {
		if _, err := ledger.StartRun(context.Background(), run); err != nil {
			t.Fatal(err)
		}
	}
	manager := NewManager(runnerFunc(func(ctx context.Context, _ Request, started func(int)) (string, error) {
		started(95)
		<-ctx.Done()
		return "", ctx.Err()
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	coordinator := NewCoordinator(manager, WithRunLedger(ledger), WithEvidence(evidenceStore))
	child, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{
		RunID: "run-child", ParentRunID: "run-parent", ParentSessionID: "session-publish", Task: "publish result",
	})
	if err != nil {
		t.Fatal(err)
	}
	publication := agentcoord.Message{
		RunID: "run-parent", To: "run-parent", From: child.ID, Kind: "result", Content: "child result",
		SourceAttemptID: child.AttemptID, SourceLeaseGeneration: child.LeaseGeneration,
	}
	queued, err := coordinator.Publish(context.Background(), publication)
	if err != nil || queued.RunID != "run-parent" || queued.From != child.ID || queued.SourceAttemptID != child.AttemptID {
		t.Fatalf("valid publication = %+v, %v", queued, err)
	}
	foreignTarget := publication
	foreignTarget.RunID, foreignTarget.To = "run-not-parent", "run-not-parent"
	if _, err := coordinator.Publish(context.Background(), foreignTarget); err == nil {
		t.Fatal("publication to a non-parent run succeeded")
	}
	foreignSession := publication
	foreignSession.SessionID = "session-foreign"
	if _, err := coordinator.Publish(context.Background(), foreignSession); err == nil {
		t.Fatal("cross-session publication succeeded")
	}
	stale := publication
	stale.SourceAttemptID = "attempt-foreign"
	if _, err := coordinator.Publish(context.Background(), stale); err == nil {
		t.Fatal("foreign-attempt publication succeeded")
	}
	if _, err := coordinator.Send(context.Background(), agentcoord.Message{
		SessionID: "session-foreign", RunID: child.ID, To: child.ID, Kind: "message", Content: "cross session",
	}); err == nil {
		t.Fatal("cross-session Send succeeded")
	}
	_, _ = coordinator.Cancel(context.Background(), child.ID, "done")
}

func TestCoordinator_SendAuthorizesExactCurrentRecordedParent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "send-auth.db")
	evidenceStore, err := evidence.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = evidenceStore.Close() })
	ledger, err := runledger.NewWithDB(evidenceStore.DB())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, run := range []runledger.AgentRun{
		{RunID: "run-send-parent", SessionID: "session-send", Status: "running"},
		{RunID: "run-send-nonparent", SessionID: "session-send", Status: "running"},
	} {
		if _, err := ledger.StartRun(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	parentFirst, err := ledger.Attach(ctx, agentcoord.AttachmentRequest{
		SessionID: "session-send", RunID: "run-send-parent", AttemptID: "attempt-send-parent-1", LeaseDuration: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(runnerFunc(func(ctx context.Context, _ Request, started func(int)) (string, error) {
		started(96)
		<-ctx.Done()
		return "", ctx.Err()
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	coordinator := NewCoordinator(manager, WithRunLedger(ledger), WithEvidence(evidenceStore))
	child, err := coordinator.Spawn(ctx, agentcoord.TaskSpec{
		RunID: "run-send-child", ParentRunID: "run-send-parent", ParentSessionID: "session-send", Task: "receive fenced parent message",
	})
	if err != nil {
		t.Fatal(err)
	}
	base := agentcoord.Message{
		RunID: child.ID, To: child.ID, From: "run-send-parent", Kind: "message", Content: "authorized direction",
		SourceAttemptID: parentFirst.AttemptID, SourceLeaseGeneration: parentFirst.LeaseGeneration,
	}
	cases := []struct {
		name   string
		mutate func(*agentcoord.Message)
	}{
		{name: "missing source", mutate: func(message *agentcoord.Message) { message.From = "" }},
		{name: "user spoof", mutate: func(message *agentcoord.Message) { message.From = "user" }},
		{name: "unknown source", mutate: func(message *agentcoord.Message) { message.From = "run-unknown" }},
		{name: "known nonparent", mutate: func(message *agentcoord.Message) { message.From = "run-send-nonparent" }},
		{name: "missing attempt", mutate: func(message *agentcoord.Message) { message.SourceAttemptID = "" }},
		{name: "zero generation", mutate: func(message *agentcoord.Message) { message.SourceLeaseGeneration = 0 }},
		{name: "cross session", mutate: func(message *agentcoord.Message) { message.SessionID = "session-foreign" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			message := base
			test.mutate(&message)
			if _, err := coordinator.Send(ctx, message); err == nil {
				t.Fatalf("unauthorized Send succeeded: %+v", message)
			}
		})
	}
	waitForDurableAttachmentExpiry(t, ledger, "session-send", "run-send-parent")
	parentSecond, err := ledger.Attach(ctx, agentcoord.AttachmentRequest{
		SessionID: "session-send", RunID: "run-send-parent", AttemptID: "attempt-send-parent-2", LeaseDuration: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Send(ctx, base); !errors.Is(err, runledger.ErrAttachmentStale) {
		t.Fatalf("stale parent Send = %v, want ErrAttachmentStale", err)
	}
	base.SourceAttemptID = parentSecond.AttemptID
	base.SourceLeaseGeneration = parentSecond.LeaseGeneration
	queued, err := coordinator.Send(ctx, base)
	if err != nil || queued.From != "run-send-parent" || queued.State != agentcoord.MessageQueued {
		t.Fatalf("current parent Send = %+v, %v", queued, err)
	}
	_, _ = coordinator.Cancel(ctx, child.ID, "done")
}

func TestCoordinator_TypedNilDurableLedgerFailsClosed(t *testing.T) {
	manager := NewManager(runnerFunc(func(context.Context, Request, func(int)) (string, error) {
		t.Fatal("typed-nil durable coordinator launched")
		return "", nil
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	var ledger *runledger.SQLiteStore
	coordinator := NewCoordinator(manager, WithRunLedger(ledger))
	_, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{
		RunID: "run-typed-nil", ParentSessionID: "session-typed-nil", Task: "do not launch",
	})
	if err == nil || !strings.Contains(err.Error(), "durable spawn requires") {
		t.Fatalf("typed-nil Spawn = %v", err)
	}
}

func hasEvent(events []runledger.Event, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}
