package runledger

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/agentcoord"
	"m31labs.dev/buckley/pkg/storage"
)

func newMonitorStores(t *testing.T) (*SQLiteStore, *SQLiteStore) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "monitor.db")
	first, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	return first, second
}

func startMonitorRun(t *testing.T, store *SQLiteStore, run AgentRun) AgentRun {
	t.Helper()
	created, _, err := store.EnsureRunContract(
		context.Background(), run, "digest-"+run.RunID, "evidence-"+run.RunID,
	)
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func startMonitorForegroundRun(t *testing.T, store *SQLiteStore, run AgentRun) AgentRun {
	t.Helper()
	created, err := store.StartRun(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func attachMonitorRun(t *testing.T, store *SQLiteStore, run AgentRun, attemptID string) agentcoord.AttachmentLease {
	t.Helper()
	lease, err := store.Attach(context.Background(), agentcoord.AttachmentRequest{
		SessionID: run.SessionID, RunID: run.RunID, ParentRunID: run.ParentRunID,
		TaskID: run.TaskID, AttemptID: attemptID, LeaseDuration: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	return lease
}

func enqueueMonitorSteer(t *testing.T, store *SQLiteStore, sessionID, runID, id string) agentcoord.Message {
	t.Helper()
	message, created, err := store.EnqueueOperatorSteer(context.Background(), agentcoord.Message{
		MessageID: id, SessionID: sessionID, RunID: runID, To: runID,
		IdempotencyKey: id, ContentRef: "secret-ref-" + id,
		ContentDigest: "secret-digest-" + id, Content: "secret-body-" + id,
		Preview: "secret-preview-" + id, ByteCount: 17,
	})
	if err != nil || !created {
		t.Fatalf("enqueue monitor steer: created=%t err=%v", created, err)
	}
	return message
}

func enqueueMonitorPeerMessage(
	t *testing.T,
	store *SQLiteStore,
	target AgentRun,
	source AgentRun,
	sourceLease agentcoord.AttachmentLease,
	id string,
) agentcoord.Message {
	t.Helper()
	message, created, err := store.Enqueue(context.Background(), agentcoord.Message{
		MessageID: id, SessionID: target.SessionID, RunID: target.RunID,
		ParentRunID: target.ParentRunID, TaskID: target.TaskID,
		From: source.RunID, To: target.RunID, Kind: "message",
		SourceAttemptID: sourceLease.AttemptID, SourceLeaseGeneration: sourceLease.LeaseGeneration,
		IdempotencyKey: id, ContentRef: "secret-ref-" + id,
		ContentDigest: "secret-digest-" + id, Content: "secret-body-" + id, ByteCount: 19,
	})
	if err != nil || !created {
		t.Fatalf("enqueue peer monitor message: created=%t err=%v", created, err)
	}
	return message
}

func TestMonitor_FreshQueuedRoutineAndEmptyMailbox(t *testing.T) {
	store, _ := newMonitorStores(t)
	started := time.Date(2026, 8, 20, 1, 2, 3, 123456789, time.UTC)
	startMonitorRun(t, store, AgentRun{
		RunID: "run-queued", SessionID: "session-queued", TaskID: "task-queued",
		AgentID: "agent-queued", ModelID: "model-queued", ProviderID: "provider-queued",
		Backend: "backend-queued", Status: "queued", StartedAt: started,
	})

	status, err := store.GetRoutineStatus(context.Background(), "session-queued", "run-queued")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != agentcoord.RunQueued || status.Attempt.State != agentcoord.AttemptNone ||
		status.Mailbox != (agentcoord.MailboxSummary{}) || !status.StartedAt.Equal(started) {
		t.Fatalf("queued status = %+v", status)
	}
	page, err := store.ListRoutineStatuses(context.Background(), agentcoord.RoutineQuery{SessionID: "session-queued"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Routines) != 1 || page.Routines[0] != status || page.HasMore || page.Next != "" {
		t.Fatalf("queued page = %+v", page)
	}
	mailbox, err := store.ListMailboxStatuses(context.Background(), agentcoord.MailboxStatusQuery{
		SessionID: "session-queued", RunID: "run-queued",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(mailbox.Messages) != 0 || mailbox.HasMore || mailbox.Next != 0 {
		t.Fatalf("empty mailbox = %+v", mailbox)
	}
}

func TestMonitor_AttachmentLifecycleAndCrossStoreExpiry(t *testing.T) {
	first, second := newMonitorStores(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 20, 2, 0, 0, 0, time.UTC)

	freshRun := startMonitorRun(t, first, AgentRun{
		RunID: "run-fresh", SessionID: "session-attempt", TaskID: "task-fresh",
		Status: "queued", StartedAt: base,
	})
	freshLease := attachMonitorRun(t, first, freshRun, "attempt-fresh")
	fresh, err := second.GetRoutineStatus(ctx, freshRun.SessionID, freshRun.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.State != agentcoord.RunRunning || fresh.Attempt.State != agentcoord.AttemptAttached ||
		fresh.Attempt.Number != 1 || fresh.Attempt.DetachedAt != nil ||
		!fresh.Attempt.AttachedAt.Equal(freshLease.AttachedAt) {
		t.Fatalf("fresh status = %+v", fresh)
	}

	expiredRun := startMonitorRun(t, first, AgentRun{
		RunID: "run-expired", SessionID: "session-attempt", TaskID: "task-expired",
		Status: "running", StartedAt: base.Add(time.Second),
	})
	expiredLease := attachMonitorRun(t, first, expiredRun, "attempt-expired")
	if _, err := first.db.Exec(`UPDATE agent_run_attempts
		SET attached_at = '2000-01-01T00:00:00Z', heartbeat_at = '2000-01-01T00:00:01Z',
			lease_expires_at = '2000-01-01T00:00:02.000000000Z'
		WHERE attempt_id = ?`, expiredLease.AttemptID); err != nil {
		t.Fatal(err)
	}
	expired, err := second.GetRoutineStatus(ctx, expiredRun.SessionID, expiredRun.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if expired.State != agentcoord.RunResumable || expired.Attempt.State != agentcoord.AttemptExpired || expired.Attempt.Number != 1 {
		t.Fatalf("expired status = %+v", expired)
	}
	var storedState string
	if err := first.db.QueryRow(`SELECT state FROM agent_run_attempts WHERE attempt_id = ?`, expiredLease.AttemptID).Scan(&storedState); err != nil {
		t.Fatal(err)
	}
	if storedState != agentcoord.AttachmentExpired {
		t.Fatalf("materialized attachment state = %q", storedState)
	}

	detachedRun := startMonitorRun(t, first, AgentRun{
		RunID: "run-detached", SessionID: "session-attempt", TaskID: "task-detached",
		Status: "running", StartedAt: base.Add(2 * time.Second),
	})
	detachedLease := attachMonitorRun(t, first, detachedRun, "attempt-detached")
	if err := first.Detach(ctx, agentcoord.AttachmentDetachRequest{
		SessionID: detachedRun.SessionID, RunID: detachedRun.RunID,
		AttemptID: detachedLease.AttemptID, LeaseGeneration: detachedLease.LeaseGeneration,
	}); err != nil {
		t.Fatal(err)
	}
	detached, err := second.GetRoutineStatus(ctx, detachedRun.SessionID, detachedRun.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if detached.State != agentcoord.RunResumable || detached.Attempt.State != agentcoord.AttemptDetached || detached.Attempt.DetachedAt == nil {
		t.Fatalf("detached status = %+v", detached)
	}
}

func TestMonitor_RoutinePaginationAndParentFilter(t *testing.T) {
	store, _ := newMonitorStores(t)
	ctx := context.Background()
	started := time.Date(2026, 8, 20, 3, 4, 5, 987654321, time.UTC)
	parent := startMonitorRun(t, store, AgentRun{
		RunID: "run-parent", SessionID: "session-page", Status: "queued", StartedAt: started.Add(-time.Second),
	})
	for _, runID := range []string{"run-a", "run-b", "run-c"} {
		startMonitorRun(t, store, AgentRun{
			RunID: runID, SessionID: parent.SessionID, ParentRunID: parent.RunID,
			Status: "queued", StartedAt: started,
		})
	}
	first, err := store.ListRoutineStatuses(ctx, agentcoord.RoutineQuery{
		SessionID: parent.SessionID, ParentRunID: parent.RunID, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{first.Routines[0].RunID, first.Routines[1].RunID}; !reflect.DeepEqual(got, []string{"run-c", "run-b"}) || !first.HasMore || first.Next == "" {
		t.Fatalf("first page = %+v", first)
	}
	second, err := store.ListRoutineStatuses(ctx, agentcoord.RoutineQuery{
		SessionID: parent.SessionID, ParentRunID: parent.RunID, Before: first.Next, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Routines) != 1 || second.Routines[0].RunID != "run-a" || second.HasMore || second.Next != "" {
		t.Fatalf("second page = %+v", second)
	}
	if _, err := store.ListRoutineStatuses(ctx, agentcoord.RoutineQuery{
		SessionID: "session-foreign", ParentRunID: parent.RunID,
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign parent error = %v, want ErrNotFound", err)
	}
}

func TestMonitor_RoutineCursorExcludesNewerInsertBetweenPages(t *testing.T) {
	store, _ := newMonitorStores(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 20, 3, 30, 0, 0, time.UTC)
	for index, runID := range []string{"run-page-old", "run-page-middle", "run-page-new"} {
		startMonitorRun(t, store, AgentRun{
			RunID: runID, SessionID: "session-page-insert", Status: "queued",
			StartedAt: base.Add(time.Duration(index) * time.Second),
		})
	}
	first, err := store.ListRoutineStatuses(ctx, agentcoord.RoutineQuery{SessionID: "session-page-insert", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{first.Routines[0].RunID, first.Routines[1].RunID}; !reflect.DeepEqual(got, []string{"run-page-new", "run-page-middle"}) {
		t.Fatalf("first page IDs=%v", got)
	}
	startMonitorRun(t, store, AgentRun{
		RunID: "run-page-newer-after-cursor", SessionID: "session-page-insert", Status: "queued",
		StartedAt: base.Add(10 * time.Second),
	})
	second, err := store.ListRoutineStatuses(ctx, agentcoord.RoutineQuery{
		SessionID: "session-page-insert", Before: first.Next, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Routines) != 1 || second.Routines[0].RunID != "run-page-old" {
		t.Fatalf("second page=%+v", second)
	}
	seen := map[string]bool{}
	for _, status := range append(first.Routines, second.Routines...) {
		if seen[status.RunID] {
			t.Fatalf("duplicate routine %s across cursor pages", status.RunID)
		}
		seen[status.RunID] = true
	}
	if seen["run-page-newer-after-cursor"] {
		t.Fatal("newer between-page insert crossed the before cursor")
	}
}

func TestMonitor_ContractedRoutineScopeWithForegroundParentAnchor(t *testing.T) {
	store, _ := newMonitorStores(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 20, 3, 45, 0, 0, time.UTC)
	root := startMonitorForegroundRun(t, store, AgentRun{
		RunID: "run-foreground-root", SessionID: "session-contracted-scope",
		Status: "running", StartedAt: base,
	})
	queued := startMonitorRun(t, store, AgentRun{
		RunID: "run-contracted-queued", SessionID: root.SessionID, ParentRunID: root.RunID,
		TaskID: "task-contracted-queued", Status: "queued", StartedAt: base.Add(time.Second),
	})
	attached := startMonitorRun(t, store, AgentRun{
		RunID: "run-contracted-attached", SessionID: root.SessionID, ParentRunID: root.RunID,
		TaskID: "task-contracted-attached", Status: "queued", StartedAt: base.Add(2 * time.Second),
	})
	attachMonitorRun(t, store, attached, "attempt-contracted-attached")

	if status, err := store.GetRoutineStatus(ctx, root.SessionID, root.RunID); !errors.Is(err, ErrNotFound) || !reflect.DeepEqual(status, agentcoord.RoutineStatus{}) {
		t.Fatalf("foreground root status=%+v err=%v", status, err)
	}
	unfiltered, err := store.ListRoutineStatuses(ctx, agentcoord.RoutineQuery{SessionID: root.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if len(unfiltered.Routines) != 2 || unfiltered.Routines[0].RunID != attached.RunID ||
		unfiltered.Routines[0].State != agentcoord.RunRunning ||
		unfiltered.Routines[1].RunID != queued.RunID || unfiltered.Routines[1].State != agentcoord.RunQueued {
		t.Fatalf("unfiltered contracted routines=%+v", unfiltered)
	}
	first, err := store.ListRoutineStatuses(ctx, agentcoord.RoutineQuery{
		SessionID: root.SessionID, ParentRunID: root.RunID, Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Routines) != 1 || first.Routines[0].RunID != attached.RunID || !first.HasMore || first.Next == "" {
		t.Fatalf("foreground parent first page=%+v", first)
	}
	second, err := store.ListRoutineStatuses(ctx, agentcoord.RoutineQuery{
		SessionID: root.SessionID, ParentRunID: root.RunID, Before: first.Next, Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Routines) != 1 || second.Routines[0].RunID != queued.RunID || second.HasMore {
		t.Fatalf("foreground parent second page=%+v", second)
	}
}

func TestMonitor_TwoPhaseRoutineObservationProjectsMutableTransitionAndConflictsOnSelectionDrift(t *testing.T) {
	t.Run("mutable lifecycle transition", func(t *testing.T) {
		store, _ := newMonitorStores(t)
		run := startMonitorRun(t, store, AgentRun{
			RunID: "run-phase-lifecycle", SessionID: "session-phase", Status: "queued",
			StartedAt: time.Date(2026, 8, 20, 3, 50, 0, 0, time.UTC),
		})
		ended := run.StartedAt.Add(time.Minute)
		page, err := store.listRoutineStatuses(context.Background(), agentcoord.RoutineQuery{
			SessionID: run.SessionID,
		}, func() {
			if _, updateErr := store.db.Exec(`UPDATE agent_runs SET status='completed', ended_at=? WHERE run_id=?`,
				sqliteTimestamp(ended), run.RunID); updateErr != nil {
				t.Fatalf("update lifecycle between phases: %v", updateErr)
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Routines) != 1 || page.Routines[0].State != agentcoord.RunCompleted ||
			page.Routines[0].FinishedAt == nil || !page.Routines[0].FinishedAt.Equal(ended) {
			t.Fatalf("updated lifecycle page=%+v", page)
		}
	})

	t.Run("immutable cursor key drift", func(t *testing.T) {
		store, _ := newMonitorStores(t)
		run := startMonitorRun(t, store, AgentRun{
			RunID: "run-phase-drift", SessionID: "session-phase", Status: "queued",
			StartedAt: time.Date(2026, 8, 20, 3, 51, 0, 0, time.UTC),
		})
		page, err := store.listRoutineStatuses(context.Background(), agentcoord.RoutineQuery{
			SessionID: run.SessionID,
		}, func() {
			if _, updateErr := store.db.Exec(`UPDATE agent_runs SET started_at=? WHERE run_id=?`,
				sqliteTimestamp(run.StartedAt.Add(time.Hour)), run.RunID); updateErr != nil {
				t.Fatalf("update cursor key between phases: %v", updateErr)
			}
		})
		if !errors.Is(err, agentcoord.ErrMonitorConflict) || !reflect.DeepEqual(page, agentcoord.RoutineStatusPage{}) {
			t.Fatalf("selection drift page=%+v err=%v", page, err)
		}
	})
}

func TestMonitor_OperatorMailboxLifecyclePaginationAndProjectionSafety(t *testing.T) {
	store, _ := newMonitorStores(t)
	ctx := context.Background()
	run := startMonitorRun(t, store, AgentRun{
		RunID: "run-mailbox", SessionID: "session-mailbox-monitor", TaskID: "task-mailbox",
		Status: "queued", StartedAt: time.Date(2026, 8, 20, 4, 0, 0, 0, time.UTC),
	})
	lease := attachMonitorRun(t, store, run, "attempt-mailbox")
	messages := []agentcoord.Message{
		enqueueMonitorSteer(t, store, run.SessionID, run.RunID, "message-queued"),
		enqueueMonitorSteer(t, store, run.SessionID, run.RunID, "message-processed"),
		enqueueMonitorSteer(t, store, run.SessionID, run.RunID, "message-dead"),
		enqueueMonitorSteer(t, store, run.SessionID, run.RunID, "message-claimed"),
	}
	claimed, err := store.Claim(ctx, agentcoord.MailboxClaimRequest{
		SessionID: run.SessionID, RunID: run.RunID, Owner: "worker-mailbox",
		AttemptID: lease.AttemptID, LeaseGeneration: lease.LeaseGeneration, Limit: 4,
	})
	if err != nil || len(claimed) != 4 {
		t.Fatalf("claim = %d err=%v", len(claimed), err)
	}
	ack := func(message agentcoord.Message) agentcoord.MailboxAckRequest {
		return agentcoord.MailboxAckRequest{
			SessionID: run.SessionID, RunID: run.RunID, MessageID: message.MessageID,
			Owner: "worker-mailbox", AttemptID: lease.AttemptID, LeaseGeneration: lease.LeaseGeneration,
		}
	}
	if err := store.Nack(ctx, agentcoord.MailboxNackRequest{MailboxAckRequest: ack(messages[0])}); err != nil {
		t.Fatal(err)
	}
	if err := store.Ack(ctx, ack(messages[1])); err != nil {
		t.Fatal(err)
	}
	if err := store.Nack(ctx, agentcoord.MailboxNackRequest{MailboxAckRequest: ack(messages[2]), DeadLetter: true, Reason: "secret-error"}); err != nil {
		t.Fatal(err)
	}

	routine, err := store.GetRoutineStatus(ctx, run.SessionID, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if routine.Mailbox != (agentcoord.MailboxSummary{Queued: 1, Claimed: 1, Processed: 1, DeadLetter: 1, LastSequence: 4}) {
		t.Fatalf("mailbox summary = %+v", routine.Mailbox)
	}
	first, err := store.ListMailboxStatuses(ctx, agentcoord.MailboxStatusQuery{
		SessionID: run.SessionID, RunID: run.RunID, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Messages) != 2 || !first.HasMore || first.Next != 2 {
		t.Fatalf("first mailbox page = %+v", first)
	}
	second, err := store.ListMailboxStatuses(ctx, agentcoord.MailboxStatusQuery{
		SessionID: run.SessionID, RunID: run.RunID, AfterSequence: first.Next, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Messages) != 2 || second.HasMore || second.Next != 4 {
		t.Fatalf("second mailbox page = %+v", second)
	}
	for _, status := range append(first.Messages, second.Messages...) {
		if status.Direction != agentcoord.MailboxFromOperator || status.PeerRunID != "" {
			t.Fatalf("operator projection = %+v", status)
		}
	}
	filtered, err := store.ListMailboxStatuses(ctx, agentcoord.MailboxStatusQuery{
		SessionID: run.SessionID, RunID: run.RunID,
		States: []agentcoord.MailboxState{agentcoord.MailboxProcessed, agentcoord.MailboxDeadLetter},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Messages) != 2 || filtered.Messages[0].State != agentcoord.MailboxProcessed ||
		filtered.Messages[1].State != agentcoord.MailboxDeadLetter {
		t.Fatalf("filtered mailbox = %+v", filtered)
	}
	empty, err := store.ListMailboxStatuses(ctx, agentcoord.MailboxStatusQuery{
		SessionID: run.SessionID, RunID: run.RunID, AfterSequence: agentcoord.MaxMonitorSequence,
	})
	if err != nil || len(empty.Messages) != 0 || empty.Next != agentcoord.MaxMonitorSequence || empty.HasMore {
		t.Fatalf("maximum sequence page=%+v err=%v", empty, err)
	}
	encoded, err := json.Marshal(struct {
		Routine agentcoord.RoutineStatus       `json:"routine"`
		Pages   []agentcoord.MailboxStatusPage `json:"pages"`
	}{routine, []agentcoord.MailboxStatusPage{first, second}})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret-ref", "secret-digest", "secret-body", "secret-preview", "secret-error", lease.AttemptID, "worker-mailbox"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("safe projection leaked %q: %s", secret, encoded)
		}
	}
}

func TestMonitor_MailboxDirectionsAndCrossSessionSpoofFailClosed(t *testing.T) {
	store, _ := newMonitorStores(t)
	ctx := context.Background()
	started := time.Date(2026, 8, 20, 5, 0, 0, 0, time.UTC)
	parent := startMonitorRun(t, store, AgentRun{
		RunID: "run-direction-parent", SessionID: "session-direction", TaskID: "task-parent",
		Status: "running", StartedAt: started,
	})
	child := startMonitorRun(t, store, AgentRun{
		RunID: "run-direction-child", SessionID: parent.SessionID, ParentRunID: parent.RunID,
		TaskID: "task-child", Status: "running", StartedAt: started.Add(time.Second),
	})
	parentLease := attachMonitorRun(t, store, parent, "attempt-direction-parent")
	childLease := attachMonitorRun(t, store, child, "attempt-direction-child")
	enqueueMonitorPeerMessage(t, store, child, parent, parentLease, "message-from-parent")
	enqueueMonitorPeerMessage(t, store, parent, child, childLease, "message-from-child")
	enqueueMonitorSteer(t, store, child.SessionID, child.RunID, "message-from-operator")

	childPage, err := store.ListMailboxStatuses(ctx, agentcoord.MailboxStatusQuery{
		SessionID: child.SessionID, RunID: child.RunID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(childPage.Messages) != 2 ||
		childPage.Messages[0].Direction != agentcoord.MailboxFromParent || childPage.Messages[0].PeerRunID != parent.RunID ||
		childPage.Messages[1].Direction != agentcoord.MailboxFromOperator || childPage.Messages[1].PeerRunID != "" {
		t.Fatalf("child mailbox directions = %+v", childPage.Messages)
	}
	parentPage, err := store.ListMailboxStatuses(ctx, agentcoord.MailboxStatusQuery{
		SessionID: parent.SessionID, RunID: parent.RunID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(parentPage.Messages) != 1 || parentPage.Messages[0].Direction != agentcoord.MailboxFromChild ||
		parentPage.Messages[0].PeerRunID != child.RunID {
		t.Fatalf("parent mailbox directions = %+v", parentPage.Messages)
	}

	foreign := startMonitorRun(t, store, AgentRun{
		RunID: "run-direction-foreign", SessionID: "session-direction-foreign",
		Status: "running", StartedAt: started,
	})
	foreignLease := attachMonitorRun(t, store, foreign, "attempt-direction-foreign")
	if _, err := store.db.Exec(`UPDATE agent_mailbox
		SET from_id = ?, kind = 'message', source_attempt_id = ?, source_lease_generation = ?
		WHERE message_id = 'message-from-operator'`, foreign.RunID, foreignLease.AttemptID, foreignLease.LeaseGeneration); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListMailboxStatuses(ctx, agentcoord.MailboxStatusQuery{
		SessionID: child.SessionID, RunID: child.RunID,
	})
	if !errors.Is(err, agentcoord.ErrMonitorIntegrity) || !reflect.DeepEqual(page, agentcoord.MailboxStatusPage{}) {
		t.Fatalf("cross-session spoof page=%+v err=%v", page, err)
	}
	if _, err := store.db.Exec(`UPDATE agent_mailbox SET from_id = NULL WHERE message_id = 'message-from-operator'`); err != nil {
		t.Fatal(err)
	}
	page, err = store.ListMailboxStatuses(ctx, agentcoord.MailboxStatusQuery{
		SessionID: child.SessionID, RunID: child.RunID,
	})
	if !errors.Is(err, agentcoord.ErrMonitorIntegrity) || !reflect.DeepEqual(page, agentcoord.MailboxStatusPage{}) {
		t.Fatalf("NULL source page=%+v err=%v", page, err)
	}
}

func TestMonitor_MailboxClaimExpiryMaterializesAndRedeliversAcrossStores(t *testing.T) {
	first, second := newMonitorStores(t)
	ctx := context.Background()
	run := startMonitorRun(t, first, AgentRun{
		RunID: "run-mailbox-expiry", SessionID: "session-mailbox-expiry",
		Status: "running", StartedAt: time.Date(2026, 8, 20, 6, 0, 0, 0, time.UTC),
	})
	lease := attachMonitorRun(t, first, run, "attempt-mailbox-expiry")
	message := enqueueMonitorSteer(t, first, run.SessionID, run.RunID, "message-mailbox-expiry")
	claimed, err := first.Claim(ctx, agentcoord.MailboxClaimRequest{
		SessionID: run.SessionID, RunID: run.RunID, Owner: "worker-expired",
		AttemptID: lease.AttemptID, LeaseGeneration: lease.LeaseGeneration,
		MessageID: message.MessageID, LeaseDuration: time.Minute,
	})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %d err=%v", len(claimed), err)
	}
	if _, err := first.db.Exec(`UPDATE agent_mailbox
		SET created_at = '2000-01-01T00:00:00Z', claimed_at = '2000-01-01T00:00:01Z',
			lease_expires_at = '2000-01-01T00:00:02.000000000Z'
		WHERE message_id = ?`, message.MessageID); err != nil {
		t.Fatal(err)
	}
	page, err := second.ListMailboxStatuses(ctx, agentcoord.MailboxStatusQuery{
		SessionID: run.SessionID, RunID: run.RunID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].State != agentcoord.MailboxQueued {
		t.Fatalf("materialized page = %+v", page)
	}
	var state string
	var owner, attempt sql.NullString
	var generation int64
	if err := first.db.QueryRow(`SELECT state, lease_owner, attempt_id, lease_generation
		FROM agent_mailbox WHERE message_id = ?`, message.MessageID).Scan(&state, &owner, &attempt, &generation); err != nil {
		t.Fatal(err)
	}
	if state != agentcoord.MessageQueued || owner.Valid || attempt.Valid || generation != 0 {
		t.Fatalf("materialized row state=%q owner=%+v attempt=%+v generation=%d", state, owner, attempt, generation)
	}
	redelivered, err := first.Claim(ctx, agentcoord.MailboxClaimRequest{
		SessionID: run.SessionID, RunID: run.RunID, Owner: "worker-redelivery",
		AttemptID: lease.AttemptID, LeaseGeneration: lease.LeaseGeneration,
		MessageID: message.MessageID,
	})
	if err != nil || len(redelivered) != 1 || redelivered[0].AttemptCount != 2 {
		t.Fatalf("redelivery = %+v err=%v", redelivered, err)
	}
}

func TestMonitor_TerminalAndIdentityTamperReturnsNoPartialProjection(t *testing.T) {
	store, _ := newMonitorStores(t)
	ctx := context.Background()
	started := time.Date(2026, 8, 20, 7, 0, 0, 0, time.UTC)
	run := startMonitorRun(t, store, AgentRun{
		RunID: "run-tamper", SessionID: "session-tamper", TaskID: "task-tamper",
		Status: "completed", StartedAt: started,
	})
	status, err := store.GetRoutineStatus(ctx, run.SessionID, run.RunID)
	if !errors.Is(err, agentcoord.ErrMonitorIntegrity) || !reflect.DeepEqual(status, agentcoord.RoutineStatus{}) {
		t.Fatalf("terminal timestamp tamper status=%+v err=%v", status, err)
	}
	if _, err := store.db.Exec(`UPDATE agent_runs SET status = 'queued', ended_at = NULL WHERE run_id = ?`, run.RunID); err != nil {
		t.Fatal(err)
	}
	message := enqueueMonitorSteer(t, store, run.SessionID, run.RunID, "message-task-tamper")
	if _, err := store.db.Exec(`UPDATE agent_mailbox SET task_id = 'different-task' WHERE message_id = ?`, message.MessageID); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListRoutineStatuses(ctx, agentcoord.RoutineQuery{SessionID: run.SessionID})
	if !errors.Is(err, agentcoord.ErrMonitorIntegrity) || !reflect.DeepEqual(page, agentcoord.RoutineStatusPage{}) {
		t.Fatalf("task identity tamper page=%+v err=%v", page, err)
	}
}

func TestMonitorMigrationV20_IndexesUpgradeIdempotenceAndQueryPlans(t *testing.T) {
	store, _ := newMonitorStores(t)
	for _, index := range []string{"idx_agent_runs_monitor_session", "idx_agent_runs_monitor_parent"} {
		var found int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&found); err != nil || found != 1 {
			t.Fatalf("index %s count=%d err=%v", index, found, err)
		}
	}
	if err := addAgentRunMonitorIndexes(store.db); err != nil {
		t.Fatalf("idempotent index migration: %v", err)
	}
	var mailboxIndex string
	if err := store.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name='idx_agent_mailbox_lease'`).Scan(&mailboxIndex); err != nil {
		t.Fatal(err)
	}
	compactIndex := strings.Join(strings.Fields(mailboxIndex), " ")
	if !strings.Contains(compactIndex, "(session_id, run_id, state, lease_expires_at)") {
		t.Fatalf("mailbox lease index=%q", mailboxIndex)
	}

	plans := []struct {
		name, where, index string
		args               []any
	}{
		{name: "session", where: "r.session_id = ?", index: "idx_agent_runs_monitor_session", args: []any{"session-plan"}},
		{name: "parent", where: "r.session_id = ? AND r.parent_run_id = ?", index: "idx_agent_runs_monitor_parent", args: []any{"session-plan", "run-parent"}},
	}
	for _, test := range plans {
		t.Run(test.name, func(t *testing.T) {
			rows, err := store.db.Query(`EXPLAIN QUERY PLAN SELECT r.run_id FROM agent_runs r WHERE `+test.where+`
				ORDER BY `+runStartedAtEpochKey+` DESC, `+runStartedAtFractionKey+` DESC, r.run_id DESC LIMIT 51`, test.args...)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var detail strings.Builder
			for rows.Next() {
				var id, parent, unused int
				var line string
				if err := rows.Scan(&id, &parent, &unused, &line); err != nil {
					t.Fatal(err)
				}
				detail.WriteString(line)
			}
			if !strings.Contains(detail.String(), test.index) {
				t.Fatalf("query plan %q does not use %s", detail.String(), test.index)
			}
		})
	}

	if _, err := store.db.Exec(`DROP INDEX idx_agent_runs_monitor_session; DROP INDEX idx_agent_runs_monitor_parent;
		DROP INDEX idx_agent_mailbox_lease;
		CREATE INDEX idx_agent_mailbox_lease ON agent_mailbox(session_id, run_id, lease_expires_at);
		DELETE FROM runledger_schema_migrations WHERE version >= 20`); err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(store.db); err != nil {
		t.Fatal(err)
	}
	var restored int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM runledger_schema_migrations WHERE version IN (20,21)`).Scan(&restored); err != nil || restored != 2 {
		t.Fatalf("v20/v21 records=%d err=%v", restored, err)
	}
}

func TestMonitorMigrationV20_FreshAndV19UpgradeParity(t *testing.T) {
	dir := t.TempDir()
	fresh, err := New(filepath.Join(dir, "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()

	upgradePath := filepath.Join(dir, "upgrade.db")
	legacyDB, err := sql.Open("sqlite", upgradePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.MigrateSQLite(legacyDB, "runledger_schema_migrations", migrations[:19]); err != nil {
		_ = legacyDB.Close()
		t.Fatal(err)
	}
	if err := legacyDB.Close(); err != nil {
		t.Fatal(err)
	}
	legacyDB, err = sql.Open("sqlite", upgradePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacyDB.Exec(`DROP INDEX idx_agent_mailbox_lease;
		CREATE INDEX idx_agent_mailbox_lease ON agent_mailbox(session_id, run_id, lease_expires_at)`); err != nil {
		_ = legacyDB.Close()
		t.Fatal(err)
	}
	if err := legacyDB.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := New(upgradePath)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()

	readIndexes := func(t *testing.T, db *sql.DB) map[string]string {
		t.Helper()
		rows, err := db.Query(`SELECT name, sql FROM sqlite_master WHERE type='index'
			AND name IN ('idx_agent_runs_monitor_session','idx_agent_runs_monitor_parent','idx_agent_mailbox_lease') ORDER BY name`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		result := map[string]string{}
		for rows.Next() {
			var name, definition string
			if err := rows.Scan(&name, &definition); err != nil {
				t.Fatal(err)
			}
			result[name] = definition
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return result
	}
	freshIndexes, upgradeIndexes := readIndexes(t, fresh.db), readIndexes(t, upgraded.db)
	if len(freshIndexes) != 3 || !reflect.DeepEqual(freshIndexes, upgradeIndexes) {
		t.Fatalf("fresh indexes=%v upgrade indexes=%v", freshIndexes, upgradeIndexes)
	}
	for label, db := range map[string]*sql.DB{"fresh": fresh.db, "upgrade": upgraded.db} {
		var versions, v20 int
		if err := db.QueryRow(`SELECT COUNT(*), SUM(version=20) FROM runledger_schema_migrations`).Scan(&versions, &v20); err != nil {
			t.Fatal(err)
		}
		if versions != len(migrations) || v20 != 1 {
			t.Fatalf("%s migration versions=%d v20=%d", label, versions, v20)
		}
	}
}

func TestMonitor_RelationshipAndLifecycleTamperMatrix(t *testing.T) {
	tests := []struct {
		name   string
		seed   func(*testing.T, *SQLiteStore) (string, string)
		mutate func(*testing.T, *SQLiteStore, string, string)
	}{
		{
			name: "missing parent",
			seed: func(t *testing.T, store *SQLiteStore) (string, string) {
				run := startMonitorRun(t, store, AgentRun{RunID: "run-missing-parent", SessionID: "session-rel", Status: "queued"})
				return run.SessionID, run.RunID
			},
			mutate: func(t *testing.T, store *SQLiteStore, _, runID string) {
				if _, err := store.db.Exec(`UPDATE agent_runs SET parent_run_id='run-does-not-exist' WHERE run_id=?`, runID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "cross session parent",
			seed: func(t *testing.T, store *SQLiteStore) (string, string) {
				startMonitorRun(t, store, AgentRun{RunID: "run-foreign-parent", SessionID: "session-foreign-parent", Status: "queued"})
				run := startMonitorRun(t, store, AgentRun{RunID: "run-cross-parent", SessionID: "session-rel", Status: "queued"})
				return run.SessionID, run.RunID
			},
			mutate: func(t *testing.T, store *SQLiteStore, _, runID string) {
				if _, err := store.db.Exec(`UPDATE agent_runs SET parent_run_id='run-foreign-parent' WHERE run_id=?`, runID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "terminal with current attachment",
			seed: func(t *testing.T, store *SQLiteStore) (string, string) {
				run := startMonitorRun(t, store, AgentRun{RunID: "run-terminal-attached", SessionID: "session-rel", Status: "running"})
				attachMonitorRun(t, store, run, "attempt-terminal-attached")
				return run.SessionID, run.RunID
			},
			mutate: func(t *testing.T, store *SQLiteStore, _, runID string) {
				if _, err := store.db.Exec(`UPDATE agent_runs SET status='completed', ended_at=? WHERE run_id=?`, sqliteTimestamp(time.Now().UTC()), runID); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, _ := newMonitorStores(t)
			sessionID, runID := test.seed(t, store)
			test.mutate(t, store, sessionID, runID)
			status, err := store.GetRoutineStatus(context.Background(), sessionID, runID)
			if !errors.Is(err, agentcoord.ErrMonitorIntegrity) || !reflect.DeepEqual(status, agentcoord.RoutineStatus{}) {
				t.Fatalf("status=%+v err=%v", status, err)
			}
		})
	}
}

func TestMonitor_UnlinkedPeerAndMailboxShapeTamperFailClosed(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *SQLiteStore, agentcoord.Message, agentcoord.AttachmentLease)
	}{
		{
			name: "unlinked source with valid fence",
			mutate: func(t *testing.T, store *SQLiteStore, message agentcoord.Message, _ agentcoord.AttachmentLease) {
				source := startMonitorRun(t, store, AgentRun{RunID: "run-unlinked-source", SessionID: message.SessionID, Status: "running"})
				lease := attachMonitorRun(t, store, source, "attempt-unlinked-source")
				if _, err := store.db.Exec(`UPDATE agent_mailbox SET from_id=?, kind='message',
					source_attempt_id=?, source_lease_generation=? WHERE message_id=?`,
					source.RunID, lease.AttemptID, lease.LeaseGeneration, message.MessageID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "queued with lease",
			mutate: func(t *testing.T, store *SQLiteStore, message agentcoord.Message, lease agentcoord.AttachmentLease) {
				if _, err := store.db.Exec(`UPDATE agent_mailbox SET lease_owner='forged', attempt_id=?,
					lease_generation=?, lease_expires_at=? WHERE message_id=?`, lease.AttemptID,
					lease.LeaseGeneration, sqliteLeaseTimestamp(time.Now().Add(time.Minute)), message.MessageID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "claimed zero attempts",
			mutate: func(t *testing.T, store *SQLiteStore, message agentcoord.Message, lease agentcoord.AttachmentLease) {
				claimed, err := store.Claim(context.Background(), agentcoord.MailboxClaimRequest{
					SessionID: message.SessionID, RunID: message.RunID, MessageID: message.MessageID,
					Owner: "worker-shape", AttemptID: lease.AttemptID, LeaseGeneration: lease.LeaseGeneration,
				})
				if err != nil || len(claimed) != 1 {
					t.Fatalf("claim=%+v err=%v", claimed, err)
				}
				if _, err := store.db.Exec(`UPDATE agent_mailbox SET attempt_count=0 WHERE message_id=?`, message.MessageID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "processed without timestamp",
			mutate: func(t *testing.T, store *SQLiteStore, message agentcoord.Message, lease agentcoord.AttachmentLease) {
				claimed, err := store.Claim(context.Background(), agentcoord.MailboxClaimRequest{
					SessionID: message.SessionID, RunID: message.RunID, MessageID: message.MessageID,
					Owner: "worker-shape", AttemptID: lease.AttemptID, LeaseGeneration: lease.LeaseGeneration,
				})
				if err != nil || len(claimed) != 1 {
					t.Fatalf("claim=%+v err=%v", claimed, err)
				}
				if _, err := store.db.Exec(`UPDATE agent_mailbox SET state='processed', processed_at=NULL WHERE message_id=?`, message.MessageID); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, _ := newMonitorStores(t)
			run := startMonitorRun(t, store, AgentRun{RunID: "run-shape", SessionID: "session-shape", Status: "running"})
			lease := attachMonitorRun(t, store, run, "attempt-shape")
			message := enqueueMonitorSteer(t, store, run.SessionID, run.RunID, "message-shape")
			test.mutate(t, store, message, lease)
			page, err := store.ListMailboxStatuses(context.Background(), agentcoord.MailboxStatusQuery{
				SessionID: run.SessionID, RunID: run.RunID,
			})
			if !errors.Is(err, agentcoord.ErrMonitorIntegrity) || !reflect.DeepEqual(page, agentcoord.MailboxStatusPage{}) {
				t.Fatalf("page=%+v err=%v", page, err)
			}
		})
	}
}

func TestMonitor_MailboxRequiresContractedStructurallyValidTarget(t *testing.T) {
	store, _ := newMonitorStores(t)
	ctx := context.Background()
	root := startMonitorForegroundRun(t, store, AgentRun{
		RunID: "run-uncontracted-mailbox", SessionID: "session-uncontracted-mailbox", Status: "queued",
	})
	enqueueMonitorSteer(t, store, root.SessionID, root.RunID, "message-uncontracted-mailbox")
	page, err := store.ListMailboxStatuses(ctx, agentcoord.MailboxStatusQuery{SessionID: root.SessionID, RunID: root.RunID})
	if !errors.Is(err, ErrNotFound) || !reflect.DeepEqual(page, agentcoord.MailboxStatusPage{}) {
		t.Fatalf("uncontracted mailbox page=%+v err=%v", page, err)
	}

	run := startMonitorRun(t, store, AgentRun{
		RunID: "run-invalid-mailbox-target", SessionID: root.SessionID,
		ParentRunID: root.RunID, TaskID: "task-invalid-mailbox-target", Status: "queued",
	})
	enqueueMonitorSteer(t, store, run.SessionID, run.RunID, "message-invalid-mailbox-target")
	if _, err := store.db.Exec(`UPDATE agent_runs SET ended_at=? WHERE run_id=?`, sqliteTimestamp(time.Now().UTC()), run.RunID); err != nil {
		t.Fatal(err)
	}
	page, err = store.ListMailboxStatuses(ctx, agentcoord.MailboxStatusQuery{SessionID: run.SessionID, RunID: run.RunID})
	if !errors.Is(err, agentcoord.ErrMonitorIntegrity) || !reflect.DeepEqual(page, agentcoord.MailboxStatusPage{}) {
		t.Fatalf("structurally invalid target page=%+v err=%v", page, err)
	}
}

func TestMonitor_OffPageMailboxAttemptAndTerminalLeaseTamperReturnsZero(t *testing.T) {
	for _, test := range []struct {
		name, update string
	}{
		{name: "queued negative attempt count", update: `state='queued', lease_owner=NULL, lease_expires_at=NULL,
			attempt_id=NULL, lease_generation=0, claimed_at=NULL, processed_at=NULL, attempt_count=-1`},
		{name: "processed at claim expiry", update: `processed_at=lease_expires_at`},
		{name: "dead letter after claim expiry", update: `state='dead_letter', processed_at=NULL,
			dead_lettered_at=datetime(lease_expires_at, '+1 second')`},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, _ := newMonitorStores(t)
			ctx := context.Background()
			run := startMonitorRun(t, store, AgentRun{RunID: "run-off-page", SessionID: "session-off-page", Status: "running"})
			lease := attachMonitorRun(t, store, run, "attempt-off-page")
			first := enqueueMonitorSteer(t, store, run.SessionID, run.RunID, "message-off-page-first")
			second := enqueueMonitorSteer(t, store, run.SessionID, run.RunID, "message-off-page-second")
			claimed, err := store.Claim(ctx, agentcoord.MailboxClaimRequest{
				SessionID: run.SessionID, RunID: run.RunID, Owner: "worker-off-page",
				AttemptID: lease.AttemptID, LeaseGeneration: lease.LeaseGeneration, Limit: 2,
			})
			if err != nil || len(claimed) != 2 {
				t.Fatalf("claim=%+v err=%v", claimed, err)
			}
			ack := agentcoord.MailboxAckRequest{
				SessionID: run.SessionID, RunID: run.RunID, MessageID: first.MessageID,
				Owner: "worker-off-page", AttemptID: lease.AttemptID, LeaseGeneration: lease.LeaseGeneration,
			}
			if err := store.Ack(ctx, ack); err != nil {
				t.Fatal(err)
			}
			ack.MessageID = second.MessageID
			if err := store.Ack(ctx, ack); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(`UPDATE agent_mailbox SET `+test.update+` WHERE message_id=?`, second.MessageID); err != nil {
				t.Fatal(err)
			}
			page, err := store.ListMailboxStatuses(ctx, agentcoord.MailboxStatusQuery{
				SessionID: run.SessionID, RunID: run.RunID, Limit: 1,
			})
			if !errors.Is(err, agentcoord.ErrMonitorIntegrity) || !reflect.DeepEqual(page, agentcoord.MailboxStatusPage{}) {
				t.Fatalf("off-page tamper page=%+v err=%v", page, err)
			}
		})
	}
}

func TestMonitor_WallClockRollbackPreservesCausalFutureHistory(t *testing.T) {
	store, _ := newMonitorStores(t)
	ctx := context.Background()
	future := time.Date(2099, 1, 2, 3, 4, 5, 0, time.UTC)
	run := startMonitorRun(t, store, AgentRun{
		RunID: "run-future", SessionID: "session-future", Status: "running", StartedAt: future,
	})
	lease := attachMonitorRun(t, store, run, "attempt-future")
	if _, err := store.db.Exec(`UPDATE agent_run_attempts SET attached_at=?, heartbeat_at=?, lease_expires_at=?
		WHERE attempt_id=?`, sqliteTimestamp(future), sqliteTimestamp(future.Add(time.Second)),
		sqliteLeaseTimestamp(future.Add(time.Minute)), lease.AttemptID); err != nil {
		t.Fatal(err)
	}
	message := enqueueMonitorSteer(t, store, run.SessionID, run.RunID, "message-future")
	if _, err := store.db.Exec(`UPDATE agent_mailbox SET created_at=? WHERE message_id=?`, sqliteTimestamp(future), message.MessageID); err != nil {
		t.Fatal(err)
	}
	status, err := store.GetRoutineStatus(ctx, run.SessionID, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if !status.StartedAt.Equal(future) || status.Attempt.State != agentcoord.AttemptAttached ||
		!status.Attempt.AttachedAt.Equal(future) {
		t.Fatalf("future routine = %+v", status)
	}
	page, err := store.ListMailboxStatuses(ctx, agentcoord.MailboxStatusQuery{SessionID: run.SessionID, RunID: run.RunID})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || !page.Messages[0].CreatedAt.Equal(future) || page.Messages[0].ByteCount != message.ByteCount {
		t.Fatalf("future mailbox = %+v", page)
	}

	expiredRun := startMonitorRun(t, store, AgentRun{
		RunID: "run-future-expired", SessionID: run.SessionID, Status: "running", StartedAt: future,
	})
	expiredLease := attachMonitorRun(t, store, expiredRun, "attempt-future-expired")
	if _, err := store.db.Exec(`UPDATE agent_run_attempts SET state='expired', attached_at=?, heartbeat_at=?, lease_expires_at=?
		WHERE attempt_id=?`, sqliteTimestamp(future), sqliteTimestamp(future.Add(time.Second)),
		sqliteLeaseTimestamp(future.Add(time.Minute)), expiredLease.AttemptID); err != nil {
		t.Fatal(err)
	}
	expired, err := store.GetRoutineStatus(ctx, expiredRun.SessionID, expiredRun.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if expired.State != agentcoord.RunResumable || expired.Attempt.State != agentcoord.AttemptExpired ||
		!expired.Attempt.LeaseExpiresAt.Equal(future.Add(time.Minute)) {
		t.Fatalf("future monotonic expired attempt = %+v", expired)
	}
}

func TestMonitor_MaterializationCapacityFailsBeforeMutation(t *testing.T) {
	store, second := newMonitorStores(t)
	run := startMonitorRun(t, store, AgentRun{RunID: "run-capacity", SessionID: "session-capacity", Status: "running"})
	lease := attachMonitorRun(t, store, run, "attempt-capacity")
	const rows = 20_000
	_, err := store.db.Exec(`WITH RECURSIVE counter(value) AS (
		SELECT 1 UNION ALL SELECT value + 1 FROM counter WHERE value < ?
	) INSERT INTO agent_mailbox (
		message_id, session_id, run_id, idempotency_key, attempt_id, lease_generation,
		source_lease_generation, sequence, schema_version, from_id, to_id, kind,
		content_ref, content_digest, envelope_digest, media_type, byte_count, state,
		lease_owner, lease_expires_at, attempt_count, created_at, claimed_at
	) SELECT printf('message-cap-%06d', value), ?, ?, printf('key-cap-%06d', value), ?, ?,
		0, value, ?, 'operator', ?, 'steer', printf('private-ref-%06d', value),
		'private-digest', ?, 'application/octet-stream', 1, 'claimed', 'worker-cap',
		'2000-01-01T00:00:02.000000000Z', 1, '2000-01-01T00:00:00Z', '2000-01-01T00:00:01Z'
	FROM counter`, rows, run.SessionID, run.RunID, lease.AttemptID, lease.LeaseGeneration,
		agentcoord.MessageSchemaVersion, run.RunID, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := make(chan struct{})
	var page agentcoord.MailboxStatusPage
	var monitorErr, writerErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		page, monitorErr = store.ListMailboxStatuses(ctx, agentcoord.MailboxStatusQuery{
			SessionID: run.SessionID, RunID: run.RunID,
		})
	}()
	go func() {
		defer wg.Done()
		<-start
		_, writerErr = second.StartRun(ctx, AgentRun{
			RunID: "run-capacity-writer", SessionID: run.SessionID, Status: "queued",
		})
	}()
	close(start)
	wg.Wait()
	err = monitorErr
	if !errors.Is(err, agentcoord.ErrMonitorCapacity) || !reflect.DeepEqual(page, agentcoord.MailboxStatusPage{}) {
		t.Fatalf("capacity page=%+v err=%v", page, err)
	}
	if writerErr != nil {
		t.Fatalf("writer blocked behind bounded capacity preflight: %v", writerErr)
	}
	var claimed int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM agent_mailbox WHERE run_id=? AND state='claimed'`, run.RunID).Scan(&claimed); err != nil {
		t.Fatal(err)
	}
	if claimed != rows {
		t.Fatalf("claimed rows after capacity failure=%d, want %d", claimed, rows)
	}
}

func TestMonitor_ExpiredMailboxPreflightQueryIsBoundedAndIndexed(t *testing.T) {
	store, _ := newMonitorStores(t)
	query := monitorExpiredMailboxCapacityQuery("?")
	if !strings.Contains(query, "SELECT COUNT(*) FROM (") || !strings.Contains(query, "LIMIT ?") {
		t.Fatalf("unbounded expiry preflight query: %s", query)
	}
	rows, err := store.db.Query(`EXPLAIN QUERY PLAN `+query,
		"session-plan", "run-plan", agentcoord.MessageClaimed,
		sqliteLeaseTimestamp(time.Now().UTC()), MonitorMaxExpiredMailboxClaims+1)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var detail strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var line string
		if err := rows.Scan(&id, &parent, &unused, &line); err != nil {
			t.Fatal(err)
		}
		detail.WriteString(line)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail.String(), "idx_agent_mailbox_lease") {
		t.Fatalf("expiry preflight query plan %q does not use lease index", detail.String())
	}
}

func TestMonitor_HighMailboxHistoryRemainsBoundedAndWriterProgresses(t *testing.T) {
	first, second := newMonitorStores(t)
	run := startMonitorRun(t, first, AgentRun{RunID: "run-history", SessionID: "session-history", Status: "queued"})
	const history = 5000
	_, err := first.db.Exec(`WITH RECURSIVE counter(value) AS (
		SELECT 1 UNION ALL SELECT value + 1 FROM counter WHERE value < ?
	) INSERT INTO agent_mailbox (
		message_id, session_id, run_id, idempotency_key, source_lease_generation,
		sequence, schema_version, from_id, to_id, kind, content_ref, content_digest,
		envelope_digest, media_type, byte_count, state, attempt_count, created_at, preview
	) SELECT printf('message-history-%06d', value), ?, ?, printf('key-history-%06d', value),
		0, value, ?, 'operator', ?, 'steer', printf('private-ref-%06d', value),
		'private-digest', ?, 'application/octet-stream', value, 'queued', 0,
		'2026-08-20T00:00:00Z', printf('private-preview-%06d', value)
	FROM counter`, history, run.SessionID, run.RunID, agentcoord.MessageSchemaVersion,
		run.RunID, strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := make(chan struct{})
	var wg sync.WaitGroup
	var status agentcoord.RoutineStatus
	var monitorErr, writerErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		status, monitorErr = first.GetRoutineStatus(ctx, run.SessionID, run.RunID)
	}()
	go func() {
		defer wg.Done()
		<-start
		_, writerErr = second.StartRun(ctx, AgentRun{RunID: "run-history-writer", SessionID: run.SessionID, Status: "queued"})
	}()
	close(start)
	wg.Wait()
	if monitorErr != nil || writerErr != nil {
		t.Fatalf("monitor error=%v writer error=%v", monitorErr, writerErr)
	}
	if status.Mailbox.Queued != history || status.Mailbox.LastSequence != history {
		t.Fatalf("history status = %+v", status)
	}
	mailbox, err := first.ListMailboxStatuses(ctx, agentcoord.MailboxStatusQuery{
		SessionID: run.SessionID, RunID: run.RunID, Limit: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(mailbox.Messages) != 3 || !mailbox.HasMore || mailbox.Next != 3 {
		t.Fatalf("bounded history mailbox page = %+v", mailbox)
	}
	encoded, err := json.Marshal(struct {
		Routine agentcoord.RoutineStatus     `json:"routine"`
		Mailbox agentcoord.MailboxStatusPage `json:"mailbox"`
	}{status, mailbox})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-ref", "private-digest", "private-preview"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("high-history projection leaked %q", secret)
		}
	}
}

func TestMonitor_LargeTerminalMailboxReadSnapshotDoesNotBlockWriter(t *testing.T) {
	first, second := newMonitorStores(t)
	run := startMonitorRun(t, first, AgentRun{
		RunID: "run-terminal-history", SessionID: "session-terminal-history", Status: "running",
	})
	lease := attachMonitorRun(t, first, run, "attempt-terminal-history")
	const history = 20_000
	_, err := first.db.Exec(`WITH RECURSIVE counter(value) AS (
		SELECT 1 UNION ALL SELECT value + 1 FROM counter WHERE value < ?
	) INSERT INTO agent_mailbox (
		message_id, session_id, run_id, idempotency_key, attempt_id, lease_generation,
		source_lease_generation, sequence, schema_version, from_id, to_id, kind,
		content_ref, content_digest, envelope_digest, media_type, byte_count, state,
		lease_owner, lease_expires_at, attempt_count, created_at, claimed_at,
		processed_at, dead_lettered_at
	) SELECT printf('message-terminal-%06d', value), ?, ?, printf('key-terminal-%06d', value), ?, ?,
		0, value, ?, 'operator', ?, 'steer', printf('private-terminal-ref-%06d', value),
		'private-terminal-digest', ?, 'application/octet-stream', 1,
		CASE WHEN value % 2 = 0 THEN 'dead_letter' ELSE 'processed' END,
		'worker-terminal', '2099-01-01T00:01:00.000000000Z', 1,
		'2099-01-01T00:00:00Z', '2099-01-01T00:00:01Z',
		CASE WHEN value % 2 = 1 THEN '2099-01-01T00:00:02Z' ELSE NULL END,
		CASE WHEN value % 2 = 0 THEN '2099-01-01T00:00:02Z' ELSE NULL END
	FROM counter`, history, run.SessionID, run.RunID, lease.AttemptID, lease.LeaseGeneration,
		agentcoord.MessageSchemaVersion, run.RunID, strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	snapshotStarted := make(chan struct{})
	releaseSnapshot := make(chan struct{})
	readerDone := make(chan struct{})
	var once sync.Once
	var page agentcoord.MailboxStatusPage
	var readerErr error
	go func() {
		defer close(readerDone)
		page, readerErr = first.listMailboxStatuses(ctx, agentcoord.MailboxStatusQuery{
			SessionID: run.SessionID, RunID: run.RunID, Limit: 1,
		}, func() {
			once.Do(func() { close(snapshotStarted) })
			<-releaseSnapshot
		})
	}()
	select {
	case <-snapshotStarted:
	case <-ctx.Done():
		t.Fatalf("read snapshot did not start: %v", ctx.Err())
	}
	writerDone := make(chan error, 1)
	go func() {
		_, writeErr := second.StartRun(ctx, AgentRun{
			RunID: "run-terminal-history-writer", SessionID: run.SessionID, Status: "queued",
		})
		writerDone <- writeErr
	}()
	select {
	case err := <-writerDone:
		if err != nil {
			t.Fatalf("writer during read snapshot: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("writer blocked behind terminal-history read snapshot")
	}
	close(releaseSnapshot)
	select {
	case <-readerDone:
	case <-ctx.Done():
		t.Fatalf("reader did not finish: %v", ctx.Err())
	}
	if readerErr != nil {
		t.Fatal(readerErr)
	}
	if len(page.Messages) != 1 || !page.HasMore || page.Next != 1 {
		t.Fatalf("terminal history page=%+v", page)
	}
}

func TestMonitor_BusyDeadlineRestoresConnectionAndRollbackDoesNotLeak(t *testing.T) {
	first, second := newMonitorStores(t)
	first.db.SetMaxOpenConns(1)
	first.db.SetMaxIdleConns(1)
	run := startMonitorRun(t, first, AgentRun{RunID: "run-busy", SessionID: "session-busy", Status: "queued"})
	if _, err := first.db.Exec(`PRAGMA busy_timeout=1234`); err != nil {
		t.Fatal(err)
	}
	lock, err := second.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := lock.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	page, err := first.ListRoutineStatuses(ctx, agentcoord.RoutineQuery{SessionID: run.SessionID})
	if !errors.Is(err, context.DeadlineExceeded) || !reflect.DeepEqual(page, agentcoord.RoutineStatusPage{}) {
		t.Fatalf("busy page=%+v err=%v", page, err)
	}
	if _, err := lock.ExecContext(context.Background(), `ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	var busy int
	if err := first.db.QueryRow(`PRAGMA busy_timeout`).Scan(&busy); err != nil || busy != 1234 {
		t.Fatalf("busy_timeout=%d err=%v, want 1234", busy, err)
	}
	if _, err := first.GetRoutineStatus(context.Background(), run.SessionID, run.RunID); err != nil {
		t.Fatalf("pool reuse after timeout: %v", err)
	}

	rollbackErr := errors.New("force monitor rollback")
	err = first.withMonitorWriteTransaction(context.Background(), func(db *monitorConn, _ time.Time) error {
		if _, err := db.exec(`UPDATE agent_runs SET status='failed', ended_at=? WHERE run_id=?`,
			sqliteTimestamp(time.Now().UTC()), run.RunID); err != nil {
			return err
		}
		return rollbackErr
	})
	if !errors.Is(err, rollbackErr) {
		t.Fatalf("rollback error=%v", err)
	}
	var state string
	var ended sql.NullString
	if err := first.db.QueryRow(`SELECT status, ended_at FROM agent_runs WHERE run_id=?`, run.RunID).Scan(&state, &ended); err != nil {
		t.Fatal(err)
	}
	if state != "queued" || ended.Valid {
		t.Fatalf("rolled back row state=%q ended=%+v", state, ended)
	}
}

func TestMonitor_CursorCanonicalizationRejectsDuplicateOrUnsafeFields(t *testing.T) {
	for _, payload := range [][]byte{
		[]byte("v1\x002026-08-20T00:00:00Z\x00run\x00duplicate"),
		[]byte("v1\x002026-08-20T00:00:00.000000000Z\x00run"),
		append([]byte("v1\x002026-08-20T00:00:00Z\x00run-"), 0xff),
	} {
		cursor := base64.RawURLEncoding.EncodeToString(payload)
		if _, _, err := agentcoord.DecodeRoutineCursor(cursor); !errors.Is(err, agentcoord.ErrMonitorValidation) {
			t.Fatalf("cursor %q error=%v", cursor, err)
		}
	}
}
