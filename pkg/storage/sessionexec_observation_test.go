package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/sessionexec"
)

func newSessionExecObservationStores(t *testing.T, sessionID string) (*Store, *Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sessionexec-observation.db")
	first, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	if _, err := first.db.Exec(`INSERT INTO sessions (session_id, project_path, created_at)
		VALUES (?, '/observation/test', CURRENT_TIMESTAMP)`, sessionID); err != nil {
		t.Fatal(err)
	}
	second, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	return first, second
}

func acceptObservationCommand(t *testing.T, store *Store, sessionID, commandID, commandType, content string) sessionexec.Receipt {
	t.Helper()
	receipt, err := store.Accept(context.Background(), sessionexec.AcceptRequest{
		SessionID: sessionID, CommandID: commandID, Type: commandType,
		Content: content, AcceptedBy: "observer-principal",
	})
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func claimObservationCommand(t *testing.T, store *Store, sessionID string, lane sessionexec.Lane) sessionexec.Command {
	t.Helper()
	command, err := store.ClaimNext(context.Background(), sessionexec.ClaimRequest{
		SessionID: sessionID, Lane: lane, Owner: "observer-worker", LeaseDuration: 5 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	return command
}

func completeObservationCommand(t *testing.T, store *Store, command sessionexec.Command, state sessionexec.State, errorCode string) {
	t.Helper()
	_, err := store.Complete(context.Background(), command.Lease, sessionexec.Completion{
		State: state, ErrorCode: errorCode, Error: "private-terminal-detail",
		Outcome: sessionexec.Outcome{Code: "private-outcome", EvidenceIDs: []string{"private-evidence"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
}

func sessionExecObservationDBNow(t *testing.T, store *Store) int64 {
	t.Helper()
	var now int64
	if err := store.db.QueryRow(`SELECT ` + sessionExecNowMillisSQL).Scan(&now); err != nil {
		t.Fatal(err)
	}
	return now
}

func TestSessionExecObservation_LegacySessionIsNonmutatingAndArtifactsFailIntegrity(t *testing.T) {
	store, _ := newSessionExecObservationStores(t, "observation-legacy")
	ctx := context.Background()

	snapshot, err := store.GetExecutionSnapshot(ctx, "observation-legacy", 10)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Initialized || snapshot.SessionID != "observation-legacy" ||
		snapshot.Summary.SessionID != "observation-legacy" || snapshot.ObservedAt.IsZero() {
		t.Fatalf("unexpected legacy snapshot: %+v", snapshot)
	}
	var executionRows int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM session_execution_state
		WHERE session_id = ?`, "observation-legacy").Scan(&executionRows); err != nil {
		t.Fatal(err)
	}
	if executionRows != 0 {
		t.Fatalf("legacy observation initialized %d execution rows", executionRows)
	}
	page, err := store.ListCommandStatuses(ctx, sessionexec.CommandStatusQuery{SessionID: "observation-legacy"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Commands) != 0 || page.HasMore || page.Next != 0 {
		t.Fatalf("unexpected legacy page: %+v", page)
	}
	if _, err := store.GetCommandStatus(ctx, "observation-legacy", "missing-command"); !errors.Is(err, sessionexec.ErrNotFound) {
		t.Fatalf("missing legacy command error = %v", err)
	}

	receipt := acceptObservationCommand(t, store, "observation-legacy", "orphan-command", "input", "private-input")
	if _, err := store.db.Exec(`DELETE FROM session_execution_state WHERE session_id = ?`, "observation-legacy"); err != nil {
		t.Fatal(err)
	}
	if got, err := store.GetExecutionSnapshot(ctx, "observation-legacy", 10); !errors.Is(err, sessionexec.ErrIdempotencyConflict) || !reflect.DeepEqual(got, sessionexec.ExecutionSnapshot{}) {
		t.Fatalf("artifact snapshot = %+v, err=%v", got, err)
	}
	if got, err := store.ListCommandStatuses(ctx, sessionexec.CommandStatusQuery{SessionID: "observation-legacy"}); !errors.Is(err, sessionexec.ErrIdempotencyConflict) || !reflect.DeepEqual(got, sessionexec.CommandStatusPage{}) {
		t.Fatalf("artifact page = %+v, err=%v", got, err)
	}
	if got, err := store.GetCommandStatus(ctx, "observation-legacy", receipt.CommandID); !errors.Is(err, sessionexec.ErrIdempotencyConflict) || !reflect.DeepEqual(got, sessionexec.CommandStatus{}) {
		t.Fatalf("artifact command = %+v, err=%v", got, err)
	}
}

func TestSessionExecObservation_UninitializedTranscriptArtifactFailsIntegrity(t *testing.T) {
	store, _ := newSessionExecObservationStores(t, "observation-transcript-orphan")
	conn, err := store.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys = OFF`); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `INSERT INTO session_command_transcript (
		session_id, command_id, generation, ordinal, message_id, entry_json, entry_digest
	) VALUES (?, 'missing-command', 0, 0, NULL, '{}', ?)`,
		"observation-transcript-orphan", strings.Repeat("0", 64)); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	assertSessionExecObservationReadersFail(t, store, "observation-transcript-orphan", "missing-command", sessionexec.ErrIdempotencyConflict)
}

func TestSessionExecObservation_SafeTextScalarIsAvailableAcrossReopenedStores(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "observation-scalar.db")
	check := func(store *Store) {
		t.Helper()
		var valid, invalidUTF8, invalidControl, allowedNewline int
		statement := `SELECT ` + sessionExecObservationSafeTextFunction + `('safe text'),
			` + sessionExecObservationSafeTextFunction + `(CAST(X'80' AS TEXT)),
			` + sessionExecObservationSafeTextFunction + `(char(1)),
			` + sessionExecObservationSafeTextFunction + `(char(10))`
		if err := store.db.QueryRow(statement).Scan(&valid, &invalidUTF8, &invalidControl, &allowedNewline); err != nil {
			t.Fatal(err)
		}
		if valid != 1 || invalidUTF8 != 0 || invalidControl != 0 || allowedNewline != 1 {
			t.Fatalf("safe-text scalar results = %d,%d,%d,%d", valid, invalidUTF8, invalidControl, allowedNewline)
		}
	}
	first, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	check(first)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	check(second)
}

func TestSessionExecObservation_CommandStatesSummaryAndSafeProjection(t *testing.T) {
	store, _ := newSessionExecObservationStores(t, "observation-states")
	terminal := []sessionexec.State{
		sessionexec.StateSucceeded,
		sessionexec.StateFailed,
		sessionexec.StateBlocked,
		sessionexec.StateInterrupted,
		sessionexec.StateCancelled,
	}
	for index, state := range terminal {
		acceptObservationCommand(t, store, "observation-states", fmt.Sprintf("terminal-%d", index), "input", "private-content-"+string(state))
		command := claimObservationCommand(t, store, "observation-states", sessionexec.LaneWork)
		completeObservationCommand(t, store, command, state, "terminal_"+string(state))
	}
	acceptObservationCommand(t, store, "observation-states", "running-command", "input", "private-running-content")
	running := claimObservationCommand(t, store, "observation-states", sessionexec.LaneWork)
	steer := acceptObservationCommand(t, store, "observation-states", "accepted-control", "steer", "private-steer-content")
	approval := acceptObservationCommand(t, store, "observation-states", "accepted-approval", "approval", "")

	snapshot, err := store.GetExecutionSnapshot(context.Background(), "observation-states", 100)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Initialized || snapshot.ExecutionState.Mode != sessionexec.ExecutionModeHeadless ||
		snapshot.ExecutionState.SessionID != snapshot.SessionID {
		t.Fatalf("unexpected execution state: %+v", snapshot)
	}
	wantSummary := sessionexec.Summary{
		SessionID: "observation-states", Total: 8, Accepted: 2, Running: 1,
		Succeeded: 1, Failed: 1, Blocked: 1, Interrupted: 1, Cancelled: 1,
		WorkPending: 1, ControlPending: 1, LastSequence: approval.Sequence,
	}
	if !reflect.DeepEqual(snapshot.Summary, wantSummary) {
		t.Fatalf("summary = %+v, want %+v", snapshot.Summary, wantSummary)
	}
	if len(snapshot.RecentCommands) != 8 {
		t.Fatalf("recent commands = %d", len(snapshot.RecentCommands))
	}
	for index, status := range snapshot.RecentCommands {
		if status.Sequence != int64(index+1) {
			t.Fatalf("recent order[%d] = sequence %d", index, status.Sequence)
		}
		if status.Type == "" || status.Lane == "" || status.AcceptedAt.IsZero() {
			t.Fatalf("incomplete status[%d]: %+v", index, status)
		}
	}
	runningStatus := snapshot.RecentCommands[5]
	if runningStatus.CommandID != running.CommandID || runningStatus.State != sessionexec.StateRunning ||
		runningStatus.Attempt != 1 || runningStatus.StartedAt == nil || runningStatus.FinishedAt != nil {
		t.Fatalf("running projection = %+v", runningStatus)
	}
	steerStatus := snapshot.RecentCommands[6]
	if steerStatus.CommandID != steer.CommandID || steerStatus.State != sessionexec.StateAccepted ||
		steerStatus.TargetCommandID != running.CommandID || steerStatus.StartedAt != nil || steerStatus.Attempt != 0 {
		t.Fatalf("accepted control projection = %+v", steerStatus)
	}
	approvalStatus := snapshot.RecentCommands[7]
	if approvalStatus.CommandID != approval.CommandID || approvalStatus.Lane != sessionexec.LaneControl ||
		approvalStatus.State != sessionexec.StateAccepted {
		t.Fatalf("accepted approval projection = %+v", approvalStatus)
	}

	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(payload)
	for _, prohibited := range []string{
		"private-content", "private-running-content", "private-steer-content",
		"observer-principal", "observer-worker", "private-terminal-detail",
		"private-outcome", "private-evidence", `"acceptedBy"`, `"inputDigest"`,
		`"error"`, `"outcome"`, `"leaseOwner"`, `"leaseGeneration"`,
	} {
		if strings.Contains(encoded, prohibited) {
			t.Fatalf("safe snapshot contains prohibited value/key %q: %s", prohibited, encoded)
		}
	}
}

func TestSessionExecObservation_CanonicalCancellationOriginsProject(t *testing.T) {
	for _, test := range []struct {
		name       string
		initialize func(*testing.T, *Store, string) sessionexec.Receipt
		wantMode   sessionexec.ExecutionMode
		wantReason string
	}{
		{
			name: "cancel pending",
			initialize: func(t *testing.T, store *Store, sessionID string) sessionexec.Receipt {
				receipt := acceptObservationCommand(t, store, sessionID, "cancel-pending-command", "input", "private")
				if count, err := store.CancelPending(context.Background(), sessionID, "operator_cancelled"); err != nil || count != 1 {
					t.Fatalf("cancel pending count=%d err=%v", count, err)
				}
				return receipt
			},
			wantMode:   sessionexec.ExecutionModeHeadless,
			wantReason: "operator_cancelled",
		},
		{
			name: "quiesce",
			initialize: func(t *testing.T, store *Store, sessionID string) sessionexec.Receipt {
				receipt := acceptObservationCommand(t, store, sessionID, "quiesced-command", "input", "private")
				if result, err := store.QuiesceSession(context.Background(), sessionID, sessionexec.ExecutionModeDetached, "operator_detached"); err != nil || result.Cancelled != 1 {
					t.Fatalf("quiesce result=%+v err=%v", result, err)
				}
				return receipt
			},
			wantMode:   sessionexec.ExecutionModeDetached,
			wantReason: "operator_detached",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			sessionID := "observation-origin-" + strings.ReplaceAll(test.name, " ", "-")
			store, _ := newSessionExecObservationStores(t, sessionID)
			receipt := test.initialize(t, store, sessionID)

			snapshot, err := store.GetExecutionSnapshot(context.Background(), sessionID, 1)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.ExecutionState.Mode != test.wantMode || snapshot.Summary.Cancelled != 1 ||
				len(snapshot.RecentCommands) != 1 || snapshot.RecentCommands[0].CommandID != receipt.CommandID ||
				snapshot.RecentCommands[0].State != sessionexec.StateCancelled ||
				snapshot.RecentCommands[0].ErrorCode != test.wantReason {
				t.Fatalf("canonical cancellation snapshot = %+v", snapshot)
			}
			page, err := store.ListCommandStatuses(context.Background(), sessionexec.CommandStatusQuery{SessionID: sessionID})
			if err != nil || len(page.Commands) != 1 || page.Commands[0].State != sessionexec.StateCancelled {
				t.Fatalf("canonical cancellation page = %+v, err=%v", page, err)
			}
			status, err := store.GetCommandStatus(context.Background(), sessionID, receipt.CommandID)
			if err != nil || status.State != sessionexec.StateCancelled || status.ErrorCode != test.wantReason {
				t.Fatalf("canonical cancellation status = %+v, err=%v", status, err)
			}
			var state, errorCode, outcome string
			var digest, completedBy, completionGeneration any
			if err := store.db.QueryRow(`SELECT state, error_code, outcome_json, completion_digest,
				completed_by, completion_lease_generation FROM session_commands
				WHERE session_id = ? AND command_id = ?`, sessionID, receipt.CommandID).
				Scan(&state, &errorCode, &outcome, &digest, &completedBy, &completionGeneration); err != nil {
				t.Fatal(err)
			}
			if state != string(sessionexec.StateCancelled) || errorCode != test.wantReason || outcome != "{}" ||
				completedBy != nil || completionGeneration != nil {
				t.Fatalf("canonical cancellation row state=%q code=%q outcome=%q digest=%v owner=%v generation=%v",
					state, errorCode, outcome, digest, completedBy, completionGeneration)
			}
			if test.wantMode == sessionexec.ExecutionModeHeadless {
				_, _, wantDigest, err := sessionexec.CompletionDigest(sessionexec.Completion{
					State: sessionexec.StateCancelled, ErrorCode: test.wantReason,
				}, nil, 0)
				if err != nil {
					t.Fatal(err)
				}
				if digest != wantDigest {
					t.Fatalf("CancelPending digest=%v, want %q", digest, wantDigest)
				}
			} else if digest != nil {
				t.Fatalf("QuiesceSession retained completion digest %v", digest)
			}
		})
	}
}

func TestSessionExecObservation_AllEntryPointsMaterializeExpiredEffectsAcrossStores(t *testing.T) {
	for _, entryPoint := range []string{"snapshot", "list", "get"} {
		t.Run(entryPoint, func(t *testing.T) {
			sessionID := "observation-expiry-" + entryPoint
			first, second := newSessionExecObservationStores(t, sessionID)
			acceptObservationCommand(t, first, sessionID, "effect-command", "input", "private-effect-content")
			command := claimObservationCommand(t, first, sessionID, sessionexec.LaneWork)
			permit, err := first.BeginEffect(context.Background(), sessionexec.EffectRequest{
				Lease: command.Lease, EffectID: "expiring-effect", Kind: sessionexec.EffectKindModel,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := first.db.Exec(`UPDATE session_effect_permits
				SET created_at_ms = 0, expires_at_ms = 1
				WHERE session_id = ? AND command_id = ? AND generation = ? AND effect_id = ?`,
				sessionID, command.CommandID, command.Generation, permit.EffectID); err != nil {
				t.Fatal(err)
			}

			var status sessionexec.CommandStatus
			var observedAt time.Time
			switch entryPoint {
			case "snapshot":
				snapshot, err := second.GetExecutionSnapshot(context.Background(), sessionID, 1)
				if err != nil {
					t.Fatal(err)
				}
				if len(snapshot.RecentCommands) != 1 || len(snapshot.AttentionEffects) != 1 {
					t.Fatalf("materialized snapshot = %+v", snapshot)
				}
				status = snapshot.RecentCommands[0]
				observedAt = snapshot.ObservedAt
				if snapshot.Summary.Blocked != 1 || snapshot.EffectSummary.Ambiguous != 1 ||
					snapshot.AttentionEffects[0].State != sessionexec.EffectStateAmbiguous {
					t.Fatalf("materialized snapshot summary = %+v", snapshot)
				}
			case "list":
				page, err := second.ListCommandStatuses(context.Background(), sessionexec.CommandStatusQuery{SessionID: sessionID})
				if err != nil {
					t.Fatal(err)
				}
				if len(page.Commands) != 1 {
					t.Fatalf("materialized page = %+v", page)
				}
				status = page.Commands[0]
			case "get":
				status, err = second.GetCommandStatus(context.Background(), sessionID, command.CommandID)
				if err != nil {
					t.Fatal(err)
				}
			}
			if status.State != sessionexec.StateBlocked || status.ErrorCode != "ambiguous_effect" ||
				status.FinishedAt == nil || status.EffectSummary.Ambiguous != 1 || len(status.Effects) != 1 ||
				status.Effects[0].State != sessionexec.EffectStateAmbiguous || status.Effects[0].AmbiguousAt == nil {
				t.Fatalf("materialized status = %+v", status)
			}
			if !observedAt.IsZero() && (!status.FinishedAt.Equal(observedAt) ||
				!status.Effects[0].AmbiguousAt.Equal(observedAt)) {
				t.Fatalf("one DB time not preserved: observed=%v status=%+v", observedAt, status)
			}

			var effectState, commandState string
			var ambiguousAt int64
			if err := first.db.QueryRow(`SELECT e.state, e.ambiguous_at_ms, c.state
				FROM session_effect_permits e JOIN session_commands c
				  ON c.session_id = e.session_id AND c.command_id = e.command_id
				WHERE e.session_id = ? AND e.effect_id = ?`, sessionID, permit.EffectID).
				Scan(&effectState, &ambiguousAt, &commandState); err != nil {
				t.Fatal(err)
			}
			if effectState != string(sessionexec.EffectStateAmbiguous) || commandState != string(sessionexec.StateBlocked) {
				t.Fatalf("persisted materialization = effect %q command %q", effectState, commandState)
			}
			replayed, err := first.GetCommandStatus(context.Background(), sessionID, command.CommandID)
			if err != nil {
				t.Fatal(err)
			}
			if replayed.Effects[0].AmbiguousAt.UnixMilli() != ambiguousAt {
				t.Fatalf("materialization repeated: got %v want %d", replayed.Effects[0].AmbiguousAt, ambiguousAt)
			}
		})
	}
}

func TestSessionExecObservation_LateEndAfterAmbiguityRemainsValid(t *testing.T) {
	store, observer := newSessionExecObservationStores(t, "observation-late-end")
	acceptObservationCommand(t, store, "observation-late-end", "late-end-command", "input", "private")
	command := claimObservationCommand(t, store, "observation-late-end", sessionexec.LaneWork)
	permit, err := store.BeginEffect(context.Background(), sessionexec.EffectRequest{
		Lease: command.Lease, EffectID: "late-end-effect", Kind: sessionexec.EffectKindTool,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE session_effect_permits SET created_at_ms = 0, expires_at_ms = 1
		WHERE session_id = ? AND command_id = ? AND effect_id = ?`,
		command.SessionID, command.CommandID, permit.EffectID); err != nil {
		t.Fatal(err)
	}
	materialized, err := observer.GetCommandStatus(context.Background(), command.SessionID, command.CommandID)
	if err != nil || materialized.State != sessionexec.StateBlocked || len(materialized.Effects) != 1 ||
		materialized.Effects[0].State != sessionexec.EffectStateAmbiguous {
		t.Fatalf("materialized status = %+v, err=%v", materialized, err)
	}
	if err := store.EndEffect(context.Background(), permit); err != nil {
		t.Fatal(err)
	}

	snapshot, err := observer.GetExecutionSnapshot(context.Background(), command.SessionID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Summary.Blocked != 1 || snapshot.EffectSummary.Ended != 1 ||
		len(snapshot.AttentionEffects) != 0 || len(snapshot.RecentCommands) != 1 ||
		len(snapshot.RecentCommands[0].Effects) != 1 ||
		snapshot.RecentCommands[0].Effects[0].State != sessionexec.EffectStateEnded ||
		snapshot.RecentCommands[0].Effects[0].AmbiguousAt == nil ||
		snapshot.RecentCommands[0].Effects[0].EndedAt == nil ||
		snapshot.RecentCommands[0].Effects[0].EndedAt.Before(*snapshot.RecentCommands[0].Effects[0].AmbiguousAt) {
		t.Fatalf("late-end snapshot = %+v", snapshot)
	}
}

func TestSessionExecObservation_ConcurrentStoresMaterializeExpiryOnce(t *testing.T) {
	first, second := newSessionExecObservationStores(t, "observation-concurrent-expiry")
	acceptObservationCommand(t, first, "observation-concurrent-expiry", "concurrent-command", "input", "private")
	command := claimObservationCommand(t, first, "observation-concurrent-expiry", sessionexec.LaneWork)
	permit, err := first.BeginEffect(context.Background(), sessionexec.EffectRequest{
		Lease: command.Lease, EffectID: "concurrent-effect", Kind: sessionexec.EffectKindModel,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.db.Exec(`UPDATE session_effect_permits SET created_at_ms = 0, expires_at_ms = 1
		WHERE session_id = ? AND command_id = ? AND effect_id = ?`,
		command.SessionID, command.CommandID, permit.EffectID); err != nil {
		t.Fatal(err)
	}

	type result struct {
		snapshot sessionexec.ExecutionSnapshot
		err      error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for _, store := range []*Store{first, second} {
		store := store
		go func() {
			<-start
			snapshot, err := store.GetExecutionSnapshot(context.Background(), command.SessionID, 1)
			results <- result{snapshot: snapshot, err: err}
		}()
	}
	close(start)
	left := <-results
	right := <-results
	for index, got := range []result{left, right} {
		if got.err != nil {
			t.Fatalf("observer %d: %v", index, got.err)
		}
		if len(got.snapshot.RecentCommands) != 1 || len(got.snapshot.AttentionEffects) != 1 ||
			got.snapshot.RecentCommands[0].State != sessionexec.StateBlocked ||
			got.snapshot.AttentionEffects[0].State != sessionexec.EffectStateAmbiguous {
			t.Fatalf("observer %d snapshot = %+v", index, got.snapshot)
		}
	}
	leftAmbiguous := left.snapshot.AttentionEffects[0].AmbiguousAt
	rightAmbiguous := right.snapshot.AttentionEffects[0].AmbiguousAt
	if leftAmbiguous == nil || rightAmbiguous == nil || !leftAmbiguous.Equal(*rightAmbiguous) {
		t.Fatalf("concurrent ambiguity times differ: left=%v right=%v", leftAmbiguous, rightAmbiguous)
	}
}

func TestSessionExecObservation_MaterializationRollsBackOnIntegrityFailure(t *testing.T) {
	store, observer := newSessionExecObservationStores(t, "observation-rollback")
	acceptObservationCommand(t, store, "observation-rollback", "rollback-command", "input", "private")
	command := claimObservationCommand(t, store, "observation-rollback", sessionexec.LaneWork)
	permit, err := store.BeginEffect(context.Background(), sessionexec.EffectRequest{
		Lease: command.Lease, EffectID: "rollback-effect", Kind: sessionexec.EffectKindTool,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE session_effect_permits SET created_at_ms = 0, expires_at_ms = 1
		WHERE session_id = ? AND command_id = ? AND effect_id = ?`,
		command.SessionID, command.CommandID, permit.EffectID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE session_commands SET run_id = 'run_tampered'
		WHERE session_id = ? AND command_id = ?`, command.SessionID, command.CommandID); err != nil {
		t.Fatal(err)
	}

	got, err := observer.GetExecutionSnapshot(context.Background(), command.SessionID, 1)
	if !errors.Is(err, sessionexec.ErrIdempotencyConflict) || !reflect.DeepEqual(got, sessionexec.ExecutionSnapshot{}) {
		t.Fatalf("corrupt materialization = %+v, err=%v", got, err)
	}
	var effectState, commandState string
	var ambiguousAt any
	if err := store.db.QueryRow(`SELECT e.state, e.ambiguous_at_ms, c.state
		FROM session_effect_permits e JOIN session_commands c
		  ON c.session_id = e.session_id AND c.command_id = e.command_id
		WHERE e.session_id = ? AND e.effect_id = ?`, command.SessionID, permit.EffectID).
		Scan(&effectState, &ambiguousAt, &commandState); err != nil {
		t.Fatal(err)
	}
	if effectState != string(sessionexec.EffectStateActive) || ambiguousAt != nil || commandState != string(sessionexec.StateRunning) {
		t.Fatalf("failed observation committed partial materialization: effect=%q ambiguous=%v command=%q", effectState, ambiguousAt, commandState)
	}
}

func TestSessionExecObservation_EffectStatesAndSafeProjection(t *testing.T) {
	store, _ := newSessionExecObservationStores(t, "observation-effect-states")
	acceptObservationCommand(t, store, "observation-effect-states", "blocked-command", "input", "private-blocked")
	blocked := claimObservationCommand(t, store, "observation-effect-states", sessionexec.LaneWork)
	ambiguousPermit, err := store.BeginEffect(context.Background(), sessionexec.EffectRequest{
		Lease: blocked.Lease, EffectID: "ambiguous-effect", Kind: sessionexec.EffectKindModel,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginEffect(context.Background(), sessionexec.EffectRequest{
		Lease: blocked.Lease, EffectID: "active-effect", Kind: sessionexec.EffectKindModel,
	}); err != nil {
		t.Fatal(err)
	}
	endedPermit, err := store.BeginEffect(context.Background(), sessionexec.EffectRequest{
		Lease: blocked.Lease, EffectID: "ended-effect", Kind: sessionexec.EffectKindTool,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EndEffect(context.Background(), endedPermit); err != nil {
		t.Fatal(err)
	}
	if duplicate, err := store.BeginEffect(context.Background(), ambiguousPermit.EffectRequest); !errors.Is(err, sessionexec.ErrEffectAmbiguous) ||
		!duplicate.Duplicate || duplicate.State != sessionexec.EffectStateAmbiguous {
		t.Fatalf("duplicate ambiguity permit=%+v err=%v", duplicate, err)
	}

	snapshot, err := store.GetExecutionSnapshot(context.Background(), "observation-effect-states", 10)
	if err != nil {
		t.Fatal(err)
	}
	wantEffects := sessionexec.EffectSummary{Total: 3, Active: 1, Ambiguous: 1, Ended: 1}
	if !reflect.DeepEqual(snapshot.EffectSummary, wantEffects) || len(snapshot.AttentionEffects) != 2 || snapshot.AttentionEffectsTruncated {
		t.Fatalf("effect projection = %+v", snapshot)
	}
	if len(snapshot.RecentCommands) != 1 || snapshot.RecentCommands[0].CommandID != blocked.CommandID ||
		snapshot.RecentCommands[0].State != sessionexec.StateBlocked {
		t.Fatalf("blocked projection = %+v", snapshot.RecentCommands)
	}
	if got := snapshot.RecentCommands[0].EffectSummary; got.Active != 1 || got.Ambiguous != 1 || got.Ended != 1 || got.Total != 3 {
		t.Fatalf("blocked effect summary = %+v", got)
	}
	encodedBytes, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(encodedBytes)
	for _, prohibited := range []string{"observer-worker", `"resolvedBy"`, `"resolutionReason"`} {
		if strings.Contains(encoded, prohibited) {
			t.Fatalf("safe effect projection contains %q: %s", prohibited, encoded)
		}
	}
}

func TestSessionExecObservation_ResolvedEffectRequiresAndProjectsQuiescence(t *testing.T) {
	store, _ := newSessionExecObservationStores(t, "observation-resolved")
	acceptObservationCommand(t, store, "observation-resolved", "resolved-command", "input", "private")
	command := claimObservationCommand(t, store, "observation-resolved", sessionexec.LaneWork)
	permit, err := store.BeginEffect(context.Background(), sessionexec.EffectRequest{
		Lease: command.Lease, EffectID: "resolved-effect", Kind: sessionexec.EffectKindTool,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE session_effect_permits SET created_at_ms = 0, expires_at_ms = 1
		WHERE session_id = ? AND command_id = ? AND effect_id = ?`,
		command.SessionID, command.CommandID, permit.EffectID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetCommandStatus(context.Background(), command.SessionID, command.CommandID); err != nil {
		t.Fatal(err)
	}
	quiesceCtx, cancelQuiesce := context.WithTimeout(context.Background(), 50*time.Millisecond)
	if _, err := store.QuiesceSession(quiesceCtx, command.SessionID, sessionexec.ExecutionModeDetached, "operator_detached"); err != nil && !errors.Is(err, sessionexec.ErrQuiescenceIncomplete) {
		cancelQuiesce()
		t.Fatal(err)
	}
	cancelQuiesce()
	resolved, err := store.ResolveAmbiguousEffect(context.Background(), sessionexec.EffectResolutionRequest{
		SessionID: command.SessionID, CommandID: command.CommandID, Generation: command.Generation,
		EffectID: permit.EffectID, Actor: "private-resolver", Reason: "private resolution body",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.State != sessionexec.EffectStateResolved {
		t.Fatalf("resolved permit = %+v", resolved)
	}
	snapshot, err := store.GetExecutionSnapshot(context.Background(), command.SessionID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ExecutionState.Mode != sessionexec.ExecutionModeDetached || snapshot.EffectSummary.Resolved != 1 ||
		len(snapshot.RecentCommands) != 1 || snapshot.RecentCommands[0].State != sessionexec.StateBlocked ||
		snapshot.RecentCommands[0].EffectSummary.Resolved != 1 {
		t.Fatalf("resolved snapshot = %+v", snapshot)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-resolver", "private resolution body"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("resolved projection contains %q: %s", secret, encoded)
		}
	}
}

func TestSessionExecObservation_AmbiguityOriginAllowsSameFenceActiveSibling(t *testing.T) {
	store, _ := newSessionExecObservationStores(t, "observation-ambiguity-sibling")
	acceptObservationCommand(t, store, "observation-ambiguity-sibling", "sibling-command", "input", "private")
	command := claimObservationCommand(t, store, "observation-ambiguity-sibling", sessionexec.LaneWork)
	first, err := store.BeginEffect(context.Background(), sessionexec.EffectRequest{
		Lease: command.Lease, EffectID: "effect-a", Kind: sessionexec.EffectKindTool,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginEffect(context.Background(), sessionexec.EffectRequest{
		Lease: command.Lease, EffectID: "effect-b", Kind: sessionexec.EffectKindModel,
	}); err != nil {
		t.Fatal(err)
	}
	duplicate, err := store.BeginEffect(context.Background(), first.EffectRequest)
	if !errors.Is(err, sessionexec.ErrEffectAmbiguous) || !duplicate.Duplicate || duplicate.State != sessionexec.EffectStateAmbiguous {
		t.Fatalf("duplicate permit=%+v err=%v", duplicate, err)
	}

	snapshot, err := store.GetExecutionSnapshot(context.Background(), command.SessionID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Summary.Blocked != 1 || snapshot.EffectSummary.Active != 1 ||
		snapshot.EffectSummary.Ambiguous != 1 || len(snapshot.AttentionEffects) != 2 ||
		len(snapshot.RecentCommands) != 1 || snapshot.RecentCommands[0].State != sessionexec.StateBlocked {
		t.Fatalf("ambiguity sibling snapshot = %+v", snapshot)
	}
}

func TestSessionExecObservation_QuiesceOriginRetainsConsistentBlockingFence(t *testing.T) {
	store, _ := newSessionExecObservationStores(t, "observation-quiesce-blocking")
	acceptObservationCommand(t, store, "observation-quiesce-blocking", "quiesce-command", "input", "private")
	command := claimObservationCommand(t, store, "observation-quiesce-blocking", sessionexec.LaneWork)
	for _, effectID := range []string{"quiesce-a", "quiesce-b"} {
		if _, err := store.BeginEffect(context.Background(), sessionexec.EffectRequest{
			Lease: command.Lease, EffectID: effectID, Kind: sessionexec.EffectKindTool,
		}); err != nil {
			t.Fatal(err)
		}
	}
	quiesceCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	result, err := store.QuiesceSession(quiesceCtx, command.SessionID, sessionexec.ExecutionModeDetached, "operator_detached")
	cancel()
	if !errors.Is(err, sessionexec.ErrQuiescenceIncomplete) || result.Cancelled != 1 {
		t.Fatalf("quiesce result=%+v err=%v", result, err)
	}
	snapshot, err := store.GetExecutionSnapshot(context.Background(), command.SessionID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ExecutionState.Mode != sessionexec.ExecutionModeDetached || snapshot.Summary.Cancelled != 1 ||
		snapshot.EffectSummary.Active != 2 || len(snapshot.AttentionEffects) != 2 {
		t.Fatalf("quiesce blocking snapshot = %+v", snapshot)
	}
}

func TestSessionExecObservation_CompleteOriginRejectsEffectsAfterQuiesce(t *testing.T) {
	for _, state := range []sessionexec.EffectState{
		sessionexec.EffectStateEnded,
		sessionexec.EffectStateAmbiguous,
		sessionexec.EffectStateResolved,
	} {
		t.Run(string(state), func(t *testing.T) {
			sessionID := "observation-complete-effect-" + string(state)
			store, _ := newSessionExecObservationStores(t, sessionID)
			acceptObservationCommand(t, store, sessionID, "complete-command", "input", "private")
			command := claimObservationCommand(t, store, sessionID, sessionexec.LaneWork)
			completeObservationCommand(t, store, command, sessionexec.StateSucceeded, "")
			if _, err := store.QuiesceSession(context.Background(), sessionID, sessionexec.ExecutionModeDetached, "operator_detached"); err != nil {
				t.Fatal(err)
			}
			now := sessionExecObservationDBNow(t, store)
			expires := command.Lease.ExpiresAt.UnixMilli()
			created := now
			var ambiguousAt, endedAt, resolvedAt, resolver, reason any
			switch state {
			case sessionexec.EffectStateEnded:
				endedAt = now
			case sessionexec.EffectStateAmbiguous:
				expires, created, ambiguousAt = 1, 0, now
			case sessionexec.EffectStateResolved:
				expires, created = 1, 0
				ambiguousAt, resolvedAt, resolver, reason = int64(1), now, "private-resolver", "private reason"
			}
			if _, err := store.db.Exec(`INSERT INTO session_effect_permits (
				session_id, command_id, generation, effect_id, kind, lease_owner,
				lease_generation, state, expires_at_ms, created_at_ms, ambiguous_at_ms,
				ended_at_ms, resolved_at_ms, resolved_by, resolution_reason
			) VALUES (?, ?, ?, 'injected-effect', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				sessionID, command.CommandID, command.Generation, sessionexec.EffectKindTool,
				command.Lease.Owner, command.Lease.LeaseGeneration, state, expires, created,
				ambiguousAt, endedAt, resolvedAt, resolver, reason); err != nil {
				t.Fatal(err)
			}
			assertSessionExecObservationReadersFail(t, store, sessionID, command.CommandID, sessionexec.ErrIdempotencyConflict)
		})
	}
}

func TestSessionExecObservation_PriorAttemptEndedEffectSurvivesReleaseAndReclaim(t *testing.T) {
	store, _ := newSessionExecObservationStores(t, "observation-prior-attempt")
	acceptObservationCommand(t, store, "observation-prior-attempt", "prior-command", "input", "private")
	first := claimObservationCommand(t, store, "observation-prior-attempt", sessionexec.LaneWork)
	ended, err := store.BeginEffect(context.Background(), sessionexec.EffectRequest{
		Lease: first.Lease, EffectID: "prior-ended", Kind: sessionexec.EffectKindTool,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EndEffect(context.Background(), ended); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Release(context.Background(), first.Lease); err != nil {
		t.Fatal(err)
	}
	second := claimObservationCommand(t, store, first.SessionID, sessionexec.LaneWork)
	if second.Lease.LeaseGeneration != first.Lease.LeaseGeneration+1 {
		t.Fatalf("reclaimed lease generation=%d, want %d", second.Lease.LeaseGeneration, first.Lease.LeaseGeneration+1)
	}
	if _, err := store.BeginEffect(context.Background(), sessionexec.EffectRequest{
		Lease: second.Lease, EffectID: "current-active", Kind: sessionexec.EffectKindModel,
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.GetExecutionSnapshot(context.Background(), first.SessionID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Summary.Running != 1 || snapshot.EffectSummary.Ended != 1 ||
		snapshot.EffectSummary.Active != 1 || len(snapshot.RecentCommands) != 1 ||
		snapshot.RecentCommands[0].Attempt != 2 {
		t.Fatalf("prior-attempt snapshot = %+v", snapshot)
	}
}

func TestSessionExecObservation_CommandEffectDetailsAreDeterministicallyTruncated(t *testing.T) {
	store, _ := newSessionExecObservationStores(t, "observation-effect-details")
	acceptObservationCommand(t, store, "observation-effect-details", "details-command", "input", "private")
	command := claimObservationCommand(t, store, "observation-effect-details", sessionexec.LaneWork)
	for index := 0; index < sessionexec.MaxCommandStatusEffects+1; index++ {
		permit, err := store.BeginEffect(context.Background(), sessionexec.EffectRequest{
			Lease: command.Lease, EffectID: fmt.Sprintf("effect-%03d", index), Kind: sessionexec.EffectKindTool,
		})
		if err != nil {
			t.Fatalf("begin effect %d: %v", index, err)
		}
		if err := store.EndEffect(context.Background(), permit); err != nil {
			t.Fatalf("end effect %d: %v", index, err)
		}
	}

	status, err := store.GetCommandStatus(context.Background(), command.SessionID, command.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if status.EffectSummary.Total != sessionexec.MaxCommandStatusEffects+1 ||
		status.EffectSummary.Ended != sessionexec.MaxCommandStatusEffects+1 ||
		len(status.Effects) != sessionexec.MaxCommandStatusEffects || !status.EffectsTruncated {
		t.Fatalf("truncated effect status = %+v", status)
	}
	for index, effect := range status.Effects {
		want := fmt.Sprintf("effect-%03d", index)
		if effect.EffectID != want {
			t.Fatalf("effect[%d] = %q, want %q", index, effect.EffectID, want)
		}
	}
}

func TestSessionExecObservation_BlockingEffectCapFailsClosed(t *testing.T) {
	store, _ := newSessionExecObservationStores(t, "observation-effect-cap")
	acceptObservationCommand(t, store, "observation-effect-cap", "cap-command", "input", "private")
	command := claimObservationCommand(t, store, "observation-effect-cap", sessionexec.LaneWork)
	for index := 0; index < sessionexec.MaxActiveEffectPermits; index++ {
		if _, err := store.BeginEffect(context.Background(), sessionexec.EffectRequest{
			Lease: command.Lease, EffectID: fmt.Sprintf("active-%03d", index), Kind: sessionexec.EffectKindModel,
		}); err != nil {
			t.Fatalf("begin active effect %d: %v", index, err)
		}
	}
	if _, err := store.BeginEffect(context.Background(), sessionexec.EffectRequest{
		Lease: command.Lease, EffectID: "active-over-api-cap", Kind: sessionexec.EffectKindModel,
	}); !errors.Is(err, sessionexec.ErrEffectPermitLimit) {
		t.Fatalf("API over-cap error = %v", err)
	}
	snapshot, err := store.GetExecutionSnapshot(context.Background(), command.SessionID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.AttentionEffects) != sessionexec.MaxAttentionEffects || snapshot.AttentionEffectsTruncated {
		t.Fatalf("canonical attention projection = %+v", snapshot)
	}
	now := sessionExecObservationDBNow(t, store)
	if _, err := store.db.Exec(`INSERT INTO session_effect_permits (
		session_id, command_id, generation, effect_id, kind, lease_owner,
		lease_generation, state, expires_at_ms, created_at_ms
	) VALUES (?, ?, ?, 'active-corrupt-over-cap', ?, ?, ?, ?, ?, ?)`,
		command.SessionID, command.CommandID, command.Generation, sessionexec.EffectKindModel,
		command.Lease.Owner, command.Lease.LeaseGeneration, sessionexec.EffectStateActive,
		command.Lease.ExpiresAt.UnixMilli(), now); err != nil {
		t.Fatal(err)
	}
	if got, err := store.GetExecutionSnapshot(context.Background(), command.SessionID, 1); !errors.Is(err, sessionexec.ErrEffectPermitConflict) || !reflect.DeepEqual(got, sessionexec.ExecutionSnapshot{}) {
		t.Fatalf("corrupt attention cap = %+v, err=%v", got, err)
	}
}

func TestSessionExecObservation_PaginationAndRecentTailAreStable(t *testing.T) {
	store, _ := newSessionExecObservationStores(t, "observation-pagination")
	const total = 105
	commandIDs := make([]string, 0, total)
	for index := 1; index <= total; index++ {
		commandID := fmt.Sprintf("page-command-%03d", index)
		acceptObservationCommand(t, store, "observation-pagination", commandID, "input", fmt.Sprintf("private-%03d", index))
		commandIDs = append(commandIDs, commandID)
	}
	if _, err := store.db.Exec(`UPDATE session_commands SET accepted_at_ms = 0
		WHERE session_id = ?`, "observation-pagination"); err != nil {
		t.Fatal(err)
	}

	snapshot, err := store.GetExecutionSnapshot(context.Background(), "observation-pagination", 3)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Summary.Total != total || snapshot.Summary.Accepted != total ||
		snapshot.Summary.WorkPending != total || snapshot.Summary.LastSequence != total {
		t.Fatalf("pagination snapshot summary = %+v", snapshot.Summary)
	}
	if len(snapshot.RecentCommands) != 3 {
		t.Fatalf("recent tail length = %d", len(snapshot.RecentCommands))
	}
	for index, status := range snapshot.RecentCommands {
		wantSequence := int64(total - 2 + index)
		if status.Sequence != wantSequence || status.CommandID != commandIDs[wantSequence-1] ||
			!status.AcceptedAt.Equal(time.UnixMilli(0).UTC()) {
			t.Fatalf("recent[%d] = %+v", index, status)
		}
	}
	emptyRecent, err := store.GetExecutionSnapshot(context.Background(), "observation-pagination", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(emptyRecent.RecentCommands) != 0 {
		t.Fatalf("zero recent returned %d commands", len(emptyRecent.RecentCommands))
	}

	var sequences []int64
	after := int64(0)
	for pageIndex := 0; ; pageIndex++ {
		page, err := store.ListCommandStatuses(context.Background(), sessionexec.CommandStatusQuery{
			SessionID: "observation-pagination", States: []sessionexec.State{sessionexec.StateAccepted},
			AfterSequence: after,
		})
		if err != nil {
			t.Fatal(err)
		}
		wantLength := sessionexec.DefaultCommandStatusLimit
		if pageIndex == 2 {
			wantLength = 5
		}
		if len(page.Commands) != wantLength {
			t.Fatalf("page %d length = %d, want %d", pageIndex, len(page.Commands), wantLength)
		}
		for _, status := range page.Commands {
			sequences = append(sequences, status.Sequence)
		}
		if page.Next != page.Commands[len(page.Commands)-1].Sequence {
			t.Fatalf("page %d next = %d, last = %d", pageIndex, page.Next, page.Commands[len(page.Commands)-1].Sequence)
		}
		if !page.HasMore {
			break
		}
		after = page.Next
	}
	if len(sequences) != total {
		t.Fatalf("paginated sequences = %d", len(sequences))
	}
	for index, sequence := range sequences {
		if sequence != int64(index+1) {
			t.Fatalf("paginated sequence[%d] = %d", index, sequence)
		}
	}

	empty, err := store.ListCommandStatuses(context.Background(), sessionexec.CommandStatusQuery{
		SessionID: "observation-pagination", States: []sessionexec.State{sessionexec.StateRunning},
		AfterSequence: 37, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Commands) != 0 || empty.HasMore || empty.Next != 37 {
		t.Fatalf("empty filtered page = %+v", empty)
	}
	status, err := store.GetCommandStatus(context.Background(), "observation-pagination", commandIDs[72])
	if err != nil {
		t.Fatal(err)
	}
	if status.Sequence != 73 || status.CommandID != commandIDs[72] || status.State != sessionexec.StateAccepted {
		t.Fatalf("single status = %+v", status)
	}
}

func execSessionExecObservationUnchecked(t *testing.T, store *Store, statement string, args ...any) {
	t.Helper()
	store.db.SetMaxOpenConns(1)
	store.db.SetMaxIdleConns(1)
	conn, err := store.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `PRAGMA ignore_check_constraints = ON`); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), statement, args...); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertSessionExecObservationReadersFail(
	t *testing.T,
	store *Store,
	sessionID, commandID string,
	want error,
) {
	t.Helper()
	if got, err := store.GetExecutionSnapshot(context.Background(), sessionID, 100); !errors.Is(err, want) || !reflect.DeepEqual(got, sessionexec.ExecutionSnapshot{}) {
		t.Fatalf("snapshot = %+v, err=%v, want %v", got, err, want)
	}
	if got, err := store.ListCommandStatuses(context.Background(), sessionexec.CommandStatusQuery{SessionID: sessionID}); !errors.Is(err, want) || !reflect.DeepEqual(got, sessionexec.CommandStatusPage{}) {
		t.Fatalf("page = %+v, err=%v, want %v", got, err, want)
	}
	if got, err := store.GetCommandStatus(context.Background(), sessionID, commandID); !errors.Is(err, want) || !reflect.DeepEqual(got, sessionexec.CommandStatus{}) {
		t.Fatalf("command = %+v, err=%v, want %v", got, err, want)
	}
}

func TestSessionExecObservation_CommandTamperFailsWithoutPartialProjection(t *testing.T) {
	tests := []struct {
		name      string
		unchecked bool
		update    string
		args      []any
	}{
		{name: "run identity", update: `run_id = 'run_tampered'`},
		{name: "turn identity", update: `turn_id = 'turn_tampered'`},
		{name: "generation", update: `generation = 1`},
		{name: "sequence", update: `sequence = 99`},
		{name: "input digest", update: `input_digest = ?`, args: []any{strings.Repeat("0", 64)}},
		{name: "unknown type", unchecked: true, update: `command_type = 'unknown'`},
		{name: "unknown lane", unchecked: true, update: `lane = 'unknown'`},
		{name: "unknown state", unchecked: true, update: `state = 'unknown'`},
		{name: "negative accepted", unchecked: true, update: `accepted_at_ms = -1`},
		{name: "attempt without start", update: `attempt = 1`},
		{name: "attempt lease generation mismatch", update: `accepted_at_ms = 0, started_at_ms = 0, attempt = 1, lease_generation = 2`},
		{name: "terminal without finish", update: `accepted_at_ms = 0, started_at_ms = 0, attempt = 1, state = 'succeeded', outcome_json = '{}'`},
		{name: "accepted with finish", update: `accepted_at_ms = 0, completed_at_ms = 0, outcome_json = '{}'`},
		{name: "invalid error code", update: `accepted_at_ms = 0, started_at_ms = 0, attempt = 1, state = 'failed', completed_at_ms = 0, outcome_json = '{}', error_code = 'bad code'`},
		{name: "invalid outcome", update: `accepted_at_ms = 0, started_at_ms = 0, attempt = 1, state = 'failed', completed_at_ms = 0, outcome_json = '{'`},
		{name: "noncanonical outcome", update: `accepted_at_ms = 0, started_at_ms = 0, attempt = 1, state = 'failed', completed_at_ms = 0, outcome_json = '{"evidenceIds":["z","a"]}'`},
		{name: "unknown outcome field", update: `accepted_at_ms = 0, started_at_ms = 0, attempt = 1, state = 'failed', completed_at_ms = 0, outcome_json = '{"secret":"private"}'`},
		{name: "terminal without attempt", update: `accepted_at_ms = 0, state = 'succeeded', completed_at_ms = 0, outcome_json = '{}'`},
		{name: "invalid completion digest", update: `accepted_at_ms = 0, started_at_ms = 0, attempt = 1, state = 'failed', completed_at_ms = 0, outcome_json = '{}', completion_digest = ?`, args: []any{strings.Repeat("z", 64)}},
		{name: "uppercase completion digest", update: `accepted_at_ms = 0, started_at_ms = 0, attempt = 1, lease_generation = 1, state = 'failed', completed_at_ms = 0, outcome_json = '{}', completion_digest = ?, completed_by = 'observer-worker', completion_lease_generation = 1`, args: []any{strings.Repeat("A", 64)}},
		{name: "empty nonnull error text", update: `accepted_at_ms = 0, started_at_ms = 0, attempt = 1, lease_generation = 1, state = 'failed', completed_at_ms = 0, outcome_json = '{}', completion_digest = ?, completed_by = 'observer-worker', completion_lease_generation = 1, error_text = ''`, args: []any{strings.Repeat("0", 64)}},
		{name: "invalid completed owner", update: `accepted_at_ms = 0, started_at_ms = 0, attempt = 1, lease_generation = 1, state = 'failed', completed_at_ms = 0, outcome_json = '{}', completion_digest = ?, completed_by = 'bad owner', completion_lease_generation = 1`, args: []any{strings.Repeat("0", 64)}},
		{name: "mismatched completed generation", update: `accepted_at_ms = 0, started_at_ms = 0, attempt = 1, lease_generation = 1, state = 'failed', completed_at_ms = 0, outcome_json = '{}', completion_digest = ?, completed_by = 'observer-worker', completion_lease_generation = 2`, args: []any{strings.Repeat("0", 64)}},
		{name: "invalid error utf8", update: `accepted_at_ms = 0, started_at_ms = 0, attempt = 1, state = 'failed', completed_at_ms = 0, outcome_json = '{}', error_text = CAST(X'80' AS TEXT)`},
		{name: "unfenced succeeded", update: `accepted_at_ms = 0, started_at_ms = 0, attempt = 1, lease_generation = 1, state = 'succeeded', completed_at_ms = 0, outcome_json = '{}'`},
		{name: "unfenced ambiguity without permit", update: `accepted_at_ms = 0, started_at_ms = 0, attempt = 1, lease_generation = 1, state = 'blocked', completed_at_ms = 0, error_code = 'ambiguous_effect', outcome_json = '{}'`},
		{name: "cancel pending wrong digest", update: `accepted_at_ms = 0, state = 'cancelled', completed_at_ms = 0, error_code = 'cancelled', outcome_json = '{}', completion_digest = ?`, args: []any{strings.Repeat("0", 64)}},
		{name: "headless quiesce origin", update: `accepted_at_ms = 0, state = 'cancelled', completed_at_ms = 0, error_code = 'session_detached', outcome_json = '{}'`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sessionID := "observation-command-tamper-" + strings.ReplaceAll(test.name, " ", "-")
			store, _ := newSessionExecObservationStores(t, sessionID)
			acceptObservationCommand(t, store, sessionID, "valid-first", "input", "private-first")
			receipt := acceptObservationCommand(t, store, sessionID, "corrupt-second", "input", "private-second")
			statement := `UPDATE session_commands SET ` + test.update + ` WHERE session_id = ? AND command_id = ?`
			args := append(append([]any(nil), test.args...), sessionID, receipt.CommandID)
			if test.unchecked {
				execSessionExecObservationUnchecked(t, store, statement, args...)
			} else if _, err := store.db.Exec(statement, args...); err != nil {
				t.Fatal(err)
			}
			assertSessionExecObservationReadersFail(t, store, sessionID, receipt.CommandID, sessionexec.ErrIdempotencyConflict)
		})
	}
}

func TestSessionExecObservation_HistoricalAggregateRejectsStructuralCorruptionOutsideRecentUnion(t *testing.T) {
	const commandCount = sessionexec.MaxRecentCommandStatuses + 1
	tests := []struct {
		name      string
		unchecked bool
		update    string
		args      []any
	}{
		{name: "completed owner grammar", update: `completed_by = 'bad owner'`},
		{name: "invalid outcome json", update: `outcome_json = '{'`},
		{name: "oversized outcome json", unchecked: true, update: `outcome_json = ?`, args: []any{`"` + strings.Repeat("x", sessionexec.MaxOutcomeJSONBytes) + `"`}},
		{name: "invalid utf8 error", update: `error_text = CAST(X'80' AS TEXT)`},
		{name: "oversized error", unchecked: true, update: `error_text = ?`, args: []any{strings.Repeat("x", sessionexec.MaxErrorTextBytes+1)}},
		{name: "control error", update: `error_text = 'bad' || char(1) || 'error'`},
		{name: "foreground generation", unchecked: true, update: `generation = 1`},
		{name: "attempt range", unchecked: true, update: `attempt = ?, lease_generation = ?, completion_lease_generation = ?`, args: []any{
			sessionexec.MaxCommandAttempts + 1, sessionexec.MaxCommandAttempts + 1, sessionexec.MaxCommandAttempts + 1,
		}},
		{name: "foreground task", unchecked: true, update: `task_id = 'other-task'`},
		{name: "running lease owner grammar", update: `state = 'running', lease_owner = 'bad owner',
			lease_expires_at_ms = accepted_at_ms + 60000, heartbeat_at_ms = accepted_at_ms,
			completed_at_ms = NULL, error_code = NULL, error_text = NULL, outcome_json = NULL,
			completion_digest = NULL, completed_by = NULL, completion_lease_generation = NULL`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sessionID := "observation-historical-" + strings.ReplaceAll(test.name, " ", "-")
			store, _ := newSessionExecObservationStores(t, sessionID)
			var oldest sessionexec.Receipt
			for index := 0; index < commandCount; index++ {
				receipt := acceptObservationCommand(t, store, sessionID, fmt.Sprintf("historical-%03d", index), "input", "private")
				if index == 0 {
					oldest = receipt
				}
			}
			if _, err := store.db.Exec(`UPDATE session_commands SET
				state = 'succeeded', attempt = 1, lease_generation = 1,
				started_at_ms = accepted_at_ms, completed_at_ms = accepted_at_ms,
				error_code = NULL, error_text = NULL, outcome_json = '{}',
				completion_digest = ?, completed_by = 'aggregate-worker', completion_lease_generation = 1
				WHERE session_id = ?`, strings.Repeat("0", 64), sessionID); err != nil {
				t.Fatal(err)
			}
			baseline, err := store.GetExecutionSnapshot(context.Background(), sessionID, sessionexec.MaxRecentCommandStatuses)
			if err != nil || baseline.Summary.Total != commandCount || len(baseline.RecentCommands) != sessionexec.MaxRecentCommandStatuses {
				var mode string
				var generation, updatedAt, databaseNow, firstAccepted, lastAccepted int64
				var reason any
				stateErr := store.db.QueryRow(`SELECT mode, generation, reason_code, updated_at_ms, `+sessionExecNowMillisSQL+`
					FROM session_execution_state WHERE session_id = ?`, sessionID).
					Scan(&mode, &generation, &reason, &updatedAt, &databaseNow)
				commandErr := store.db.QueryRow(`SELECT MIN(accepted_at_ms), MAX(accepted_at_ms)
					FROM session_commands WHERE session_id = ?`, sessionID).Scan(&firstAccepted, &lastAccepted)
				t.Fatalf("baseline snapshot total=%d recent=%d err=%v; state mode=%q generation=%d reason=%v updated=%d dbnow=%d wall=%d first=%d last=%d stateRead=%v commandRead=%v",
					baseline.Summary.Total, len(baseline.RecentCommands), err, mode, generation, reason, updatedAt,
					databaseNow, time.Now().UnixMilli(), firstAccepted, lastAccepted, stateErr, commandErr)
			}

			statement := `UPDATE session_commands SET ` + test.update + ` WHERE session_id = ? AND command_id = ?`
			args := append(append([]any(nil), test.args...), sessionID, oldest.CommandID)
			if test.unchecked {
				execSessionExecObservationUnchecked(t, store, statement, args...)
			} else if _, err := store.db.Exec(statement, args...); err != nil {
				t.Fatal(err)
			}
			assertSessionExecObservationReadersFail(t, store, sessionID, oldest.CommandID, sessionexec.ErrIdempotencyConflict)
		})
	}
}

func TestSessionExecObservation_TargetMustReferenceEarlierWorkCommand(t *testing.T) {
	for _, test := range []struct {
		name          string
		target        string
		controlBefore bool
		setup         func(*testing.T, *Store, string) string
	}{
		{name: "missing", target: "missing-target"},
		{name: "later", setup: func(t *testing.T, store *Store, sessionID string) string {
			return acceptObservationCommand(t, store, sessionID, "later-target", "input", "private-later").CommandID
		}},
		{name: "control", controlBefore: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			sessionID := "observation-target-" + test.name
			store, _ := newSessionExecObservationStores(t, sessionID)
			target := test.target
			if test.controlBefore {
				target = acceptObservationCommand(t, store, sessionID, "earlier-control", "approval", "").CommandID
			}
			acceptObservationCommand(t, store, sessionID, "running-target", "input", "private-running")
			claimObservationCommand(t, store, sessionID, sessionexec.LaneWork)
			steer := acceptObservationCommand(t, store, sessionID, "target-signal", "steer", "private-steer")
			if test.setup != nil {
				target = test.setup(t, store, sessionID)
			}
			digest, err := sessionexec.InputDigest(sessionexec.AcceptRequest{
				SessionID: sessionID, CommandID: steer.CommandID, Type: "steer",
				Content: "private-steer", AcceptedBy: "observer-principal",
			}, steer.Identity, sessionexec.LaneWork, target)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(`UPDATE session_commands SET target_command_id = ?, input_digest = ?
				WHERE session_id = ? AND command_id = ?`, target, digest, sessionID, steer.CommandID); err != nil {
				t.Fatal(err)
			}
			assertSessionExecObservationReadersFail(t, store, sessionID, steer.CommandID, sessionexec.ErrIdempotencyConflict)
		})
	}
}

func TestSessionExecObservation_ExecutionStateTamperFailsWithoutPartialProjection(t *testing.T) {
	tests := []struct {
		name      string
		unchecked bool
		update    string
		args      []any
	}{
		{name: "unknown mode", unchecked: true, update: `mode = 'unknown'`},
		{name: "generation over bound", unchecked: true, update: `generation = ?`, args: []any{sessionexec.MaxCommandSequence + 1}},
		{name: "headless generation", update: `generation = 1`},
		{name: "headless reason", update: `reason_code = 'tampered'`},
		{name: "quiesced zero generation", update: `mode = 'detached', generation = 0, reason_code = 'detached'`},
		{name: "quiesced missing reason", update: `mode = 'detached', generation = 1, reason_code = NULL`},
		{name: "invalid reason", update: `mode = 'detached', generation = 1, reason_code = 'bad reason'`},
		{name: "negative update", unchecked: true, update: `updated_at_ms = -1`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sessionID := "observation-state-tamper-" + strings.ReplaceAll(test.name, " ", "-")
			store, _ := newSessionExecObservationStores(t, sessionID)
			receipt := acceptObservationCommand(t, store, sessionID, "state-command", "input", "private")
			statement := `UPDATE session_execution_state SET ` + test.update + ` WHERE session_id = ?`
			args := append(append([]any(nil), test.args...), sessionID)
			if test.unchecked {
				execSessionExecObservationUnchecked(t, store, statement, args...)
			} else if _, err := store.db.Exec(statement, args...); err != nil {
				t.Fatal(err)
			}
			assertSessionExecObservationReadersFail(t, store, sessionID, receipt.CommandID, sessionexec.ErrIdempotencyConflict)
		})
	}
}

func TestSessionExecObservation_EffectTamperFailsWithoutPartialProjection(t *testing.T) {
	type corruptEffect struct {
		generation      int
		kind            sessionexec.EffectKind
		owner           string
		leaseGeneration int64
		state           sessionexec.EffectState
		expires         func(sessionexec.Command, int64) int64
		created         func(sessionexec.Command, int64) int64
		ambiguous       func(int64) any
		ended           func(int64) any
		resolved        func(int64) any
		resolvedBy      any
		reason          any
	}
	future := func(_ sessionexec.Command, now int64) int64 { return now + int64(time.Hour/time.Millisecond) }
	leaseExpiry := func(command sessionexec.Command, _ int64) int64 { return command.Lease.ExpiresAt.UnixMilli() }
	nowValue := func(_ sessionexec.Command, now int64) int64 { return now }
	epoch := func(_ sessionexec.Command, _ int64) int64 { return 1 }
	atNow := func(now int64) any { return now }
	atEpoch := func(_ int64) any { return int64(1) }
	tests := []struct {
		name      string
		unchecked bool
		want      error
		effect    corruptEffect
	}{
		{name: "generation mismatch", want: sessionexec.ErrIdempotencyConflict, effect: corruptEffect{
			generation: 1, kind: sessionexec.EffectKindModel, owner: "observer-worker", leaseGeneration: 1,
			state: sessionexec.EffectStateEnded, expires: leaseExpiry, created: nowValue, ended: atNow,
		}},
		{name: "unknown kind", unchecked: true, want: sessionexec.ErrEffectPermitConflict, effect: corruptEffect{
			kind: "unknown", owner: "observer-worker", leaseGeneration: 1,
			state: sessionexec.EffectStateEnded, expires: leaseExpiry, created: nowValue, ended: atNow,
		}},
		{name: "unknown state", unchecked: true, want: sessionexec.ErrEffectPermitConflict, effect: corruptEffect{
			kind: sessionexec.EffectKindTool, owner: "observer-worker", leaseGeneration: 1,
			state: "unknown", expires: leaseExpiry, created: nowValue,
		}},
		{name: "creation after expiry", want: sessionexec.ErrEffectPermitConflict, effect: corruptEffect{
			kind: sessionexec.EffectKindTool, owner: "observer-worker", leaseGeneration: 1,
			state: sessionexec.EffectStateEnded, expires: epoch, created: future, ended: atNow,
		}},
		{name: "ended before creation", want: sessionexec.ErrEffectPermitConflict, effect: corruptEffect{
			kind: sessionexec.EffectKindTool, owner: "observer-worker", leaseGeneration: 1,
			state: sessionexec.EffectStateEnded, expires: future, created: future, ended: atNow,
		}},
		{name: "ended before ambiguity", want: sessionexec.ErrEffectPermitConflict, effect: corruptEffect{
			kind: sessionexec.EffectKindTool, owner: "observer-worker", leaseGeneration: 1,
			state: sessionexec.EffectStateEnded, expires: leaseExpiry,
			created: func(sessionexec.Command, int64) int64 { return 0 }, ambiguous: atNow, ended: atEpoch,
		}},
		{name: "resolved before expiry", want: sessionexec.ErrEffectPermitConflict, effect: corruptEffect{
			kind: sessionexec.EffectKindTool, owner: "observer-worker", leaseGeneration: 1,
			state: sessionexec.EffectStateResolved, expires: future,
			created: func(sessionexec.Command, int64) int64 { return 0 }, ambiguous: atEpoch,
			resolved: atNow, resolvedBy: "private-resolver", reason: "private reason",
		}},
		{name: "active owner mismatch", want: sessionexec.ErrIdempotencyConflict, effect: corruptEffect{
			kind: sessionexec.EffectKindModel, owner: "other-worker", leaseGeneration: 1,
			state: sessionexec.EffectStateActive, expires: leaseExpiry, created: nowValue,
		}},
		{name: "active lease generation ahead", want: sessionexec.ErrIdempotencyConflict, effect: corruptEffect{
			kind: sessionexec.EffectKindModel, owner: "observer-worker", leaseGeneration: 2,
			state: sessionexec.EffectStateActive, expires: leaseExpiry, created: nowValue,
		}},
		{name: "active expiry mismatch", want: sessionexec.ErrIdempotencyConflict, effect: corruptEffect{
			kind: sessionexec.EffectKindModel, owner: "observer-worker", leaseGeneration: 1,
			state: sessionexec.EffectStateActive,
			expires: func(command sessionexec.Command, _ int64) int64 {
				return command.Lease.ExpiresAt.Add(time.Second).UnixMilli()
			},
			created: nowValue,
		}},
		{name: "ambiguous on running", want: sessionexec.ErrIdempotencyConflict, effect: corruptEffect{
			kind: sessionexec.EffectKindModel, owner: "observer-worker", leaseGeneration: 1,
			state: sessionexec.EffectStateAmbiguous, expires: epoch, created: func(sessionexec.Command, int64) int64 { return 0 }, ambiguous: atEpoch,
		}},
		{name: "ended ambiguity on running", want: sessionexec.ErrIdempotencyConflict, effect: corruptEffect{
			kind: sessionexec.EffectKindTool, owner: "observer-worker", leaseGeneration: 1,
			state: sessionexec.EffectStateEnded, expires: epoch, created: func(sessionexec.Command, int64) int64 { return 0 }, ambiguous: atEpoch, ended: atNow,
		}},
		{name: "resolved on running", want: sessionexec.ErrIdempotencyConflict, effect: corruptEffect{
			kind: sessionexec.EffectKindTool, owner: "observer-worker", leaseGeneration: 1,
			state: sessionexec.EffectStateResolved, expires: epoch, created: func(sessionexec.Command, int64) int64 { return 0 },
			ambiguous: atEpoch, resolved: atNow, resolvedBy: "private-resolver", reason: "private reason",
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sessionID := "observation-effect-tamper-" + strings.ReplaceAll(test.name, " ", "-")
			store, _ := newSessionExecObservationStores(t, sessionID)
			acceptObservationCommand(t, store, sessionID, "effect-command", "input", "private")
			command := claimObservationCommand(t, store, sessionID, sessionexec.LaneWork)
			valid, err := store.BeginEffect(context.Background(), sessionexec.EffectRequest{
				Lease: command.Lease, EffectID: "a-valid", Kind: sessionexec.EffectKindTool,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.EndEffect(context.Background(), valid); err != nil {
				t.Fatal(err)
			}
			now := sessionExecObservationDBNow(t, store)
			effect := test.effect
			statement := `INSERT INTO session_effect_permits (
				session_id, command_id, generation, effect_id, kind, lease_owner,
				lease_generation, state, expires_at_ms, created_at_ms, ambiguous_at_ms,
				ended_at_ms, resolved_at_ms, resolved_by, resolution_reason
			) VALUES (?, ?, ?, 'z-corrupt', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
			args := []any{
				sessionID, command.CommandID, effect.generation, effect.kind, effect.owner,
				effect.leaseGeneration, effect.state, effect.expires(command, now), effect.created(command, now),
				nil, nil, nil, effect.resolvedBy, effect.reason,
			}
			if effect.ambiguous != nil {
				args[9] = effect.ambiguous(now)
			}
			if effect.ended != nil {
				args[10] = effect.ended(now)
			}
			if effect.resolved != nil {
				args[11] = effect.resolved(now)
			}
			if test.unchecked {
				execSessionExecObservationUnchecked(t, store, statement, args...)
			} else if _, err := store.db.Exec(statement, args...); err != nil {
				t.Fatal(err)
			}
			assertSessionExecObservationReadersFail(t, store, sessionID, command.CommandID, test.want)
		})
	}
}

func TestSessionExecObservation_CancelPendingOriginCannotBeReclassifiedByLaterQuiesce(t *testing.T) {
	tests := []struct {
		name      string
		state     sessionexec.EffectState
		ambiguous bool
		ended     bool
		resolved  bool
	}{
		{name: "active", state: sessionexec.EffectStateActive},
		{name: "ambiguous", state: sessionexec.EffectStateAmbiguous, ambiguous: true},
		{name: "resolved", state: sessionexec.EffectStateResolved, ambiguous: true, resolved: true},
		{name: "ended-with-ambiguity", state: sessionexec.EffectStateEnded, ambiguous: true, ended: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sessionID := "observation-headless-cancelled-" + test.name
			store, _ := newSessionExecObservationStores(t, sessionID)
			acceptObservationCommand(t, store, sessionID, "cancelled-effect-command", "input", "private")
			command := claimObservationCommand(t, store, sessionID, sessionexec.LaneWork)
			if _, err := store.Release(context.Background(), command.Lease); err != nil {
				t.Fatal(err)
			}
			if count, err := store.CancelPending(context.Background(), sessionID, "operator_cancelled"); err != nil || count != 1 {
				t.Fatalf("cancel pending count=%d err=%v", count, err)
			}
			if _, err := store.QuiesceSession(context.Background(), sessionID, sessionexec.ExecutionModeDetached, "operator_detached"); err != nil {
				t.Fatal(err)
			}
			now := sessionExecObservationDBNow(t, store)
			expires := now + int64(time.Hour/time.Millisecond)
			created := now
			var ambiguousAt, endedAt, resolvedAt, resolvedBy, resolutionReason any
			if test.ambiguous {
				expires = now
				ambiguousAt = now
			}
			if test.ended {
				endedAt = now
			}
			if test.resolved {
				resolvedAt = now
				resolvedBy = "private-resolver"
				resolutionReason = "private resolution"
			}
			if _, err := store.db.Exec(`INSERT INTO session_effect_permits (
				session_id, command_id, generation, effect_id, kind, lease_owner,
				lease_generation, state, expires_at_ms, created_at_ms, ambiguous_at_ms,
				ended_at_ms, resolved_at_ms, resolved_by, resolution_reason
			) VALUES (?, ?, ?, 'cancelled-effect', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				sessionID, command.CommandID, command.Generation, sessionexec.EffectKindTool,
				command.Lease.Owner, command.Lease.LeaseGeneration, test.state, expires, created,
				ambiguousAt, endedAt, resolvedAt, resolvedBy, resolutionReason); err != nil {
				t.Fatal(err)
			}
			assertSessionExecObservationReadersFail(t, store, sessionID, command.CommandID, sessionexec.ErrIdempotencyConflict)
		})
	}
}

func TestSessionExecObservation_CancelPendingRetainsPriorAttemptNonambiguousEndedEffect(t *testing.T) {
	const sessionID = "observation-cancelled-prior-ended"
	store, _ := newSessionExecObservationStores(t, sessionID)
	acceptObservationCommand(t, store, sessionID, "cancelled-prior-command", "input", "private")
	command := claimObservationCommand(t, store, sessionID, sessionexec.LaneWork)
	permit, err := store.BeginEffect(context.Background(), sessionexec.EffectRequest{
		Lease: command.Lease, EffectID: "prior-ended", Kind: sessionexec.EffectKindTool,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EndEffect(context.Background(), permit); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Release(context.Background(), command.Lease); err != nil {
		t.Fatal(err)
	}
	if count, err := store.CancelPending(context.Background(), sessionID, "operator_cancelled"); err != nil || count != 1 {
		t.Fatalf("cancel pending count=%d err=%v", count, err)
	}
	if _, err := store.QuiesceSession(context.Background(), sessionID, sessionexec.ExecutionModeDetached, "operator_detached"); err != nil {
		t.Fatal(err)
	}

	snapshot, err := store.GetExecutionSnapshot(context.Background(), sessionID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Summary.Cancelled != 1 || snapshot.EffectSummary.Ended != 1 ||
		len(snapshot.AttentionEffects) != 0 || len(snapshot.RecentCommands) != 1 ||
		snapshot.RecentCommands[0].EffectSummary.Ended != 1 {
		t.Fatalf("cancelled prior-ended snapshot = %+v", snapshot)
	}
	page, err := store.ListCommandStatuses(context.Background(), sessionexec.CommandStatusQuery{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	status, err := store.GetCommandStatus(context.Background(), sessionID, command.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Commands) != 1 || page.Commands[0].EffectSummary.Ended != 1 ||
		status.State != sessionexec.StateCancelled || status.EffectSummary.Ended != 1 ||
		len(status.Effects) != 1 || status.Effects[0].State != sessionexec.EffectStateEnded ||
		status.Effects[0].AmbiguousAt != nil {
		t.Fatalf("cancelled prior-ended page=%+v status=%+v", page, status)
	}
}

func TestSessionExecObservation_CoherentFutureTimestampsSurviveClockRollback(t *testing.T) {
	const sessionID = "observation-coherent-future"
	store, observer := newSessionExecObservationStores(t, sessionID)
	acceptObservationCommand(t, store, sessionID, "future-command", "input", "private")
	command := claimObservationCommand(t, store, sessionID, sessionexec.LaneWork)
	permit, err := store.BeginEffect(context.Background(), sessionexec.EffectRequest{
		Lease: command.Lease, EffectID: "future-effect", Kind: sessionexec.EffectKindTool,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EndEffect(context.Background(), permit); err != nil {
		t.Fatal(err)
	}

	dbNow := sessionExecObservationDBNow(t, store)
	future := dbNow + int64(time.Hour/time.Millisecond)
	if _, err := store.db.Exec(`UPDATE session_execution_state SET updated_at_ms = ? WHERE session_id = ?`, future+3, sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE session_commands SET accepted_at_ms = ?, started_at_ms = ?,
		heartbeat_at_ms = ?, lease_expires_at_ms = ? WHERE session_id = ? AND command_id = ?`,
		future, future+1, future+2, future+2000, sessionID, command.CommandID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE session_effect_permits SET created_at_ms = ?, ended_at_ms = ?,
		expires_at_ms = ? WHERE session_id = ? AND command_id = ? AND effect_id = ?`,
		future+1, future+2, future+1000, sessionID, command.CommandID, permit.EffectID); err != nil {
		t.Fatal(err)
	}

	type persistedTimes struct {
		execution, accepted, started, heartbeat, commandExpiry int64
		created, ended, effectExpiry                           int64
	}
	readTimes := func() persistedTimes {
		t.Helper()
		var result persistedTimes
		if err := store.db.QueryRow(`SELECT execution.updated_at_ms, command.accepted_at_ms,
			command.started_at_ms, command.heartbeat_at_ms, command.lease_expires_at_ms,
			effect.created_at_ms, effect.ended_at_ms, effect.expires_at_ms
			FROM session_execution_state execution
			JOIN session_commands command ON command.session_id = execution.session_id
			JOIN session_effect_permits effect ON effect.session_id = command.session_id
				AND effect.command_id = command.command_id
			WHERE execution.session_id = ? AND command.command_id = ? AND effect.effect_id = ?`,
			sessionID, command.CommandID, permit.EffectID).Scan(
			&result.execution, &result.accepted, &result.started, &result.heartbeat, &result.commandExpiry,
			&result.created, &result.ended, &result.effectExpiry,
		); err != nil {
			t.Fatal(err)
		}
		return result
	}
	wantTimes := readTimes()

	snapshot, err := observer.GetExecutionSnapshot(context.Background(), sessionID, 1)
	if err != nil {
		t.Fatal(err)
	}
	page, err := observer.ListCommandStatuses(context.Background(), sessionexec.CommandStatusQuery{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	status, err := observer.GetCommandStatus(context.Background(), sessionID, command.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.ObservedAt.Before(time.UnixMilli(future).UTC()) ||
		snapshot.ExecutionState.UpdatedAt.UnixMilli() != future+3 ||
		len(snapshot.RecentCommands) != 1 || snapshot.RecentCommands[0].AcceptedAt.UnixMilli() != future ||
		len(page.Commands) != 1 || page.Commands[0].StartedAt == nil || page.Commands[0].StartedAt.UnixMilli() != future+1 ||
		status.StartedAt == nil || status.StartedAt.UnixMilli() != future+1 || len(status.Effects) != 1 ||
		status.Effects[0].CreatedAt.UnixMilli() != future+1 || status.Effects[0].EndedAt == nil ||
		status.Effects[0].EndedAt.UnixMilli() != future+2 {
		t.Fatalf("future snapshot=%+v page=%+v status=%+v", snapshot, page, status)
	}
	if gotTimes := readTimes(); gotTimes != wantTimes {
		t.Fatalf("observation mutated coherent future timestamps: got=%+v want=%+v", gotTimes, wantTimes)
	}
}

func TestSessionExecObservation_FullRetainedEffectCapIsScanned(t *testing.T) {
	store, _ := newSessionExecObservationStores(t, "observation-retained-cap")
	acceptObservationCommand(t, store, "observation-retained-cap", "retained-command", "input", "private")
	command := claimObservationCommand(t, store, "observation-retained-cap", sessionexec.LaneWork)
	now := sessionExecObservationDBNow(t, store)
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < sessionexec.MaxEffectPermitsPerSession; index++ {
		if _, err := tx.Exec(`INSERT INTO session_effect_permits (
			session_id, command_id, generation, effect_id, kind, lease_owner,
			lease_generation, state, expires_at_ms, created_at_ms, ended_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			command.SessionID, command.CommandID, command.Generation, fmt.Sprintf("retained-%03d", index),
			sessionexec.EffectKindTool, command.Lease.Owner, command.Lease.LeaseGeneration,
			sessionexec.EffectStateEnded, command.Lease.ExpiresAt.UnixMilli(), now, now); err != nil {
			_ = tx.Rollback()
			t.Fatalf("insert retained effect %d: %v", index, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	status, err := store.GetCommandStatus(context.Background(), command.SessionID, command.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if status.EffectSummary.Total != sessionexec.MaxEffectPermitsPerSession ||
		status.EffectSummary.Ended != sessionexec.MaxEffectPermitsPerSession ||
		len(status.Effects) != sessionexec.MaxCommandStatusEffects || !status.EffectsTruncated {
		t.Fatalf("retained cap projection = %+v", status)
	}
	if _, err := store.db.Exec(`INSERT INTO session_effect_permits (
		session_id, command_id, generation, effect_id, kind, lease_owner,
		lease_generation, state, expires_at_ms, created_at_ms, ended_at_ms
	) VALUES (?, ?, ?, 'retained-over-cap', ?, ?, ?, ?, ?, ?, ?)`,
		command.SessionID, command.CommandID, command.Generation, sessionexec.EffectKindTool,
		command.Lease.Owner, command.Lease.LeaseGeneration, sessionexec.EffectStateEnded,
		command.Lease.ExpiresAt.UnixMilli(), now, now); err != nil {
		t.Fatal(err)
	}
	assertSessionExecObservationReadersFail(t, store, command.SessionID, command.CommandID, sessionexec.ErrEffectPermitConflict)
}

func TestSessionExecObservation_SnapshotEnvelopeLoadsStayBoundedAndWriterProgresses(t *testing.T) {
	const (
		sessionID   = "observation-bounded-snapshot"
		commandRows = 12
		recentLimit = 3
	)
	store, writer := newSessionExecObservationStores(t, sessionID)
	privateContent := strings.Repeat("x", sessionexec.MaxContentBytes)
	permitOwners := make(map[string]struct{})
	for index := 0; index < commandRows; index++ {
		commandID := fmt.Sprintf("bounded-command-%02d", index)
		acceptObservationCommand(t, store, sessionID, commandID, "input", privateContent)
		command := claimObservationCommand(t, store, sessionID, sessionexec.LaneWork)
		if index < 4 || index >= commandRows-2 {
			permit, err := store.BeginEffect(context.Background(), sessionexec.EffectRequest{
				Lease: command.Lease, EffectID: fmt.Sprintf("bounded-effect-%02d", index), Kind: sessionexec.EffectKindTool,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.EndEffect(context.Background(), permit); err != nil {
				t.Fatal(err)
			}
			permitOwners[command.CommandID] = struct{}{}
		}
		if _, err := store.Release(context.Background(), command.Lease); err != nil {
			t.Fatal(err)
		}
		if count, err := store.CancelPending(context.Background(), sessionID, "bounded_cancelled"); err != nil || count != 1 {
			t.Fatalf("cancel command %d: count=%d err=%v", index, count, err)
		}
	}

	aggregateRead := make(chan struct{})
	releaseObservation := make(chan struct{})
	envelopesLoaded := 0
	trace := &sessionExecObservationTrace{
		envelopeLoaded: func() { envelopesLoaded++ },
		aggregateRead: func() {
			close(aggregateRead)
			<-releaseObservation
		},
	}
	observationCtx := context.WithValue(context.Background(), sessionExecObservationTraceKey{}, trace)
	type snapshotResult struct {
		snapshot sessionexec.ExecutionSnapshot
		err      error
	}
	snapshotDone := make(chan snapshotResult, 1)
	go func() {
		snapshot, err := store.GetExecutionSnapshot(observationCtx, sessionID, recentLimit)
		snapshotDone <- snapshotResult{snapshot: snapshot, err: err}
	}()
	<-aggregateRead

	type writerResult struct {
		receipt sessionexec.Receipt
		err     error
	}
	writerStarted := make(chan struct{})
	writerDone := make(chan writerResult, 1)
	writerCtx, cancelWriter := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelWriter()
	go func() {
		close(writerStarted)
		receipt, err := writer.Accept(writerCtx, sessionexec.AcceptRequest{
			SessionID: sessionID, CommandID: "writer-progress-command", Type: "input",
			Content: "writer progress", AcceptedBy: "observer-principal",
		})
		writerDone <- writerResult{receipt: receipt, err: err}
	}()
	<-writerStarted
	close(releaseObservation)

	observed := <-snapshotDone
	if observed.err != nil {
		t.Fatal(observed.err)
	}
	written := <-writerDone
	if written.err != nil || written.receipt.CommandID != "writer-progress-command" {
		t.Fatalf("concurrent writer receipt=%+v err=%v", written.receipt, written.err)
	}
	if observed.snapshot.Summary.Total != commandRows || len(observed.snapshot.RecentCommands) != recentLimit {
		t.Fatalf("bounded snapshot summary=%+v recent=%d", observed.snapshot.Summary, len(observed.snapshot.RecentCommands))
	}
	uniqueUnion := recentLimit
	for commandID := range permitOwners {
		foundRecent := false
		for _, command := range observed.snapshot.RecentCommands {
			if command.CommandID == commandID {
				foundRecent = true
				break
			}
		}
		if !foundRecent {
			uniqueUnion++
		}
	}
	if envelopesLoaded != uniqueUnion || envelopesLoaded > recentLimit+len(permitOwners) {
		t.Fatalf("full envelopes loaded=%d, want deduplicated union=%d (recent=%d owners=%d)",
			envelopesLoaded, uniqueUnion, recentLimit, len(permitOwners))
	}
	payload, err := json.Marshal(observed.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), privateContent[:1024]) {
		t.Fatal("bounded snapshot retained private command content")
	}
	var total int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM session_commands WHERE session_id = ?`, sessionID).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != commandRows+1 {
		t.Fatalf("post-observation command total=%d, want %d", total, commandRows+1)
	}
}

func TestSessionExecObservation_SnapshotQueryShapesAreBounded(t *testing.T) {
	aggregate := strings.ToLower(sessionExecObservationAggregateSQL)
	for _, prohibited := range []string{" content", "input_digest", "accepted_by"} {
		if strings.Contains(aggregate, prohibited) {
			t.Fatalf("aggregate query loads private envelope column %q", prohibited)
		}
	}
	recent := strings.ToLower(sessionExecObservationRecentSQL)
	if !strings.Contains(recent, "order by sequence desc, command_id desc limit ?") {
		t.Fatalf("recent query is not descending and bounded: %s", sessionExecObservationRecentSQL)
	}
	one := strings.ToLower(sessionExecObservationOneSQL)
	if !strings.Contains(one, "session_id = ? and command_id = ?") {
		t.Fatalf("permit-owner query is not exact: %s", sessionExecObservationOneSQL)
	}

	loaded := make(map[string]sessionExecObservedCommand, sessionexec.MaxRecentCommandStatuses)
	for index := 0; index < sessionexec.MaxRecentCommandStatuses; index++ {
		commandID := fmt.Sprintf("recent-%03d", index)
		loaded[commandID] = sessionExecObservedCommand{status: sessionexec.CommandStatus{
			Identity: sessionexec.Identity{CommandID: commandID},
		}}
	}
	permits := make([]sessionexec.EffectPermit, 0, sessionexec.MaxEffectPermitsPerSession)
	for index := 0; index < sessionexec.MaxEffectPermitsPerSession; index++ {
		owner := fmt.Sprintf("owner-%03d", index)
		permits = append(permits, sessionexec.EffectPermit{EffectRequest: sessionexec.EffectRequest{
			Lease: sessionexec.LeaseRef{CommandID: owner}, EffectID: fmt.Sprintf("effect-%03d", index),
		}})
	}
	owners, err := sessionExecObservationMissingPermitOwners(permits, loaded)
	if err != nil {
		t.Fatal(err)
	}
	for index := 1; index < len(owners); index++ {
		if owners[index-1] >= owners[index] {
			t.Fatalf("permit-owner identities are not sorted and unique: %q then %q", owners[index-1], owners[index])
		}
	}
	if len(owners) != sessionexec.MaxEffectPermitsPerSession ||
		len(loaded)+len(owners) != sessionexec.MaxRecentCommandStatuses+sessionexec.MaxEffectPermitsPerSession {
		t.Fatalf("exact authentication union: recent=%d owners=%d", len(loaded), len(owners))
	}
	permits[len(permits)-2].Lease.CommandID = "owner-000"
	permits[len(permits)-1].Lease.CommandID = "recent-000"
	deduped, err := sessionExecObservationMissingPermitOwners(permits, loaded)
	if err != nil {
		t.Fatal(err)
	}
	if len(deduped) != sessionexec.MaxEffectPermitsPerSession-2 {
		t.Fatalf("deduplicated permit owners=%d, want %d", len(deduped), sessionexec.MaxEffectPermitsPerSession-2)
	}
	for index := 1; index < len(deduped); index++ {
		if deduped[index-1] >= deduped[index] {
			t.Fatalf("deduplicated owners are not deterministic: %q then %q", deduped[index-1], deduped[index])
		}
	}
}

func TestSessionExecObservation_BusyDeadlineIsBoundedAndLeavesStateReadable(t *testing.T) {
	writer, observer := newSessionExecObservationStores(t, "observation-busy")
	observer.db.SetMaxOpenConns(1)
	observer.db.SetMaxIdleConns(1)
	receipt := acceptObservationCommand(t, writer, "observation-busy", "busy-command", "input", "private")
	holder, err := writer.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holder.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		_ = holder.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = holder.ExecContext(context.Background(), `ROLLBACK`)
		_ = holder.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	start := time.Now()
	got, err := observer.GetExecutionSnapshot(ctx, "observation-busy", 1)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) || !reflect.DeepEqual(got, sessionexec.ExecutionSnapshot{}) {
		t.Fatalf("busy observation = %+v, err=%v", got, err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("busy observation exceeded bounded deadline: %v", elapsed)
	}
	if _, err := holder.ExecContext(context.Background(), `ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	if err := holder.Close(); err != nil {
		t.Fatal(err)
	}

	snapshot, err := observer.GetExecutionSnapshot(context.Background(), "observation-busy", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.RecentCommands) != 1 || snapshot.RecentCommands[0].CommandID != receipt.CommandID ||
		snapshot.RecentCommands[0].State != sessionexec.StateAccepted {
		t.Fatalf("post-busy snapshot = %+v", snapshot)
	}
	poolConn, err := observer.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var busyTimeout int64
	if err := poolConn.QueryRowContext(context.Background(), `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		_ = poolConn.Close()
		t.Fatal(err)
	}
	if busyTimeout != sqliteWALBusyTimeout.Milliseconds() {
		_ = poolConn.Close()
		t.Fatalf("reused connection busy_timeout = %d, want %d", busyTimeout, sqliteWALBusyTimeout.Milliseconds())
	}
	if _, err := poolConn.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		_ = poolConn.Close()
		t.Fatalf("reused connection retained transaction/lock: %v", err)
	}
	if _, err := poolConn.ExecContext(context.Background(), `ROLLBACK`); err != nil {
		_ = poolConn.Close()
		t.Fatal(err)
	}
	if err := poolConn.Close(); err != nil {
		t.Fatal(err)
	}

	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if got, err := observer.GetExecutionSnapshot(cancelled, "observation-busy", 1); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, sessionexec.ExecutionSnapshot{}) {
		t.Fatalf("pre-cancelled observation = %+v, err=%v", got, err)
	}
}
