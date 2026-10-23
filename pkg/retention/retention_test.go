package retention

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/replay"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/sessionexec"
	"m31labs.dev/buckley/pkg/storage"
)

func testPolicy() Policy {
	p := Policy{HistoryTTL: 30 * 24 * time.Hour, MaxCompletedSessions: 100, MaxCompletedRuns: 1000, Evidence: evidence.DefaultRetentionPolicy()}
	p.Evidence.OtherTTL = p.Evidence.EphemeralTTL
	return p
}

func newRetentionStores(t *testing.T) (*storage.Store, *runledger.SQLiteStore, *evidence.SQLiteStore, *Cleaner) {
	t.Helper()
	dir := t.TempDir()
	store, err := storage.New(filepath.Join(dir, "buckley.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ev, err := evidence.NewWithDB(store.DB(), filepath.Join(dir, "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := runledger.NewWithDB(store.DB())
	if err != nil {
		t.Fatal(err)
	}
	cleaner, err := New(store.DB(), ev)
	if err != nil {
		t.Fatal(err)
	}
	return store, ledger, ev, cleaner
}

func addHistory(t *testing.T, store *storage.Store, ledger *runledger.SQLiteStore, ev *evidence.SQLiteStore, id, status string, at time.Time) (string, sessionexec.Command, []sessionexec.TranscriptEntry, evidence.Object) {
	t.Helper()
	ctx := context.Background()
	if err := store.CreateSession(&storage.Session{ID: id, Status: status, CreatedAt: at, LastActive: at, CompletedAt: &at}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Accept(ctx, sessionexec.AcceptRequest{SessionID: id, CommandID: "command", Type: "input", Content: "read the fixture", AcceptedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	command, err := store.ClaimNext(ctx, sessionexec.ClaimRequest{SessionID: id, Lane: sessionexec.LaneWork, Owner: "test", LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	entries := []sessionexec.TranscriptEntry{
		{Ordinal: 1, Role: "assistant", ToolCalls: `[{"id":"call","type":"function","function":{"name":"read_file","arguments":"{}"}}]`, Reasoning: "retained reasoning", Tokens: 4},
		{Ordinal: 2, Role: "tool", Content: "fixture contents", ToolCallID: "call", Name: "read_file", Tokens: 3},
		{Ordinal: 3, Role: "assistant", Content: "done", Tokens: 1},
	}
	if _, err := store.Complete(ctx, command.Lease, sessionexec.Completion{State: sessionexec.StateSucceeded}, entries); err != nil {
		t.Fatal(err)
	}
	// Completion updates activity. Freeze this history at its simulated age.
	if _, err := store.DB().Exec(`UPDATE sessions SET status = ?, last_active = ?, completed_at = ? WHERE session_id = ?`, status, at.UTC().Format(time.RFC3339Nano), at, id); err != nil {
		t.Fatal(err)
	}
	run, err := ledger.StartRun(ctx, runledger.AgentRun{RunID: command.RunID, SessionID: id, StartedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	obj, err := ev.Put(ctx, evidence.Object{Kind: evidence.KindToolResult, MediaType: "text/plain", InlineBody: bytes.Repeat([]byte(id), 12*1024), CreatedAt: at,
		Metadata: map[string]any{evidence.MetaRunID: run.RunID, evidence.MetaSessionID: id}})
	if err != nil {
		t.Fatal(err)
	}
	step, _, err := ledger.BeginStep(ctx, runledger.ExecutionStep{RunID: run.RunID, StepID: "tool", Kind: "tool", InputDigest: "input"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.CompleteStep(ctx, run.RunID, step.StepID, obj.ID, obj.ContentSHA256, at); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Append(ctx, runledger.Event{RunID: run.RunID, Type: runledger.EventToolCompleted,
		Payload: map[string]any{"step_id": step.StepID, "attempt": 1, "input_digest": "input", "output_evidence_id": obj.ID}, EvidenceIDs: []string{obj.ID}}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.EndRun(ctx, run.RunID, "completed", at, nil); err != nil {
		t.Fatal(err)
	}
	return run.RunID, command, entries, obj
}

func TestClean_ExpiresWholeHistoryPreservesResumeAndReplay(t *testing.T) {
	store, ledger, ev, cleaner := newRetentionStores(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-40 * 24 * time.Hour)
	_, _, _, expired := addHistory(t, store, ledger, ev, "expired", "completed", old)
	if err := ev.Pin(ctx, expired.ID, "user:keep"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id, status string
		at         time.Time
	}{
		{"active", "active", old}, {"paused", "paused", old}, {"recent", "completed", now.Add(-time.Hour)},
	} {
		addHistory(t, store, ledger, ev, tc.id, tc.status, tc.at)
	}
	var completionBefore, completionAfter string
	if err := store.DB().QueryRow(`SELECT CAST(completed_at AS TEXT) FROM sessions WHERE session_id='expired'`).Scan(&completionBefore); err != nil {
		t.Fatal(err)
	}
	preview, err := cleaner.Clean(ctx, testPolicy(), now, true)
	if err != nil || preview.Sessions != 1 || preview.Runs != 1 {
		t.Fatalf("preview=%+v, err=%v", preview, err)
	}
	if _, err := store.GetSession("expired"); err != nil {
		t.Fatalf("dry run removed history: %v", err)
	}
	if err := store.DB().QueryRow(`SELECT CAST(completed_at AS TEXT) FROM sessions WHERE session_id='expired'`).Scan(&completionAfter); err != nil || completionAfter != completionBefore {
		t.Fatalf("dry run modified completion time: %v", err)
	}
	report, err := cleaner.Clean(ctx, testPolicy(), now, false)
	if err != nil || report.Sessions != 1 || report.Runs != 1 {
		t.Fatalf("cleanup=%+v, err=%v", report, err)
	}
	for _, table := range []string{"sessions", "messages", "session_commands", "session_command_transcript", "ipc_events", "agent_runs"} {
		var count int
		if err := store.DB().QueryRow("SELECT COUNT(*) FROM " + table + " WHERE session_id = 'expired'").Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
	if _, err := ev.Get(ctx, expired.ID); err != nil {
		t.Fatalf("user pin lost: %v", err)
	}
	for _, id := range []string{"active", "paused", "recent"} {
		conv := conversation.New(id)
		if err := conv.LoadFromStorage(store); err != nil {
			t.Fatal(err)
		}
		if len(conv.Messages) != 4 || conv.Messages[1].Reasoning != "retained reasoning" || len(conv.Messages[1].ToolCalls) != 1 || conv.Messages[2].ToolCallID != "call" || conv.Messages[2].Content != "fixture contents" {
			t.Fatalf("resume %s: %+v", id, conv.Messages)
		}
		replayReport, err := replay.Verify(ctx, ledger, ledger, ev, sessionexec.RunIDForSession(id))
		if err != nil || !replayReport.Valid || replayReport.EvidenceCount != 1 {
			t.Fatalf("replay %s: %+v err=%v", id, replayReport, err)
		}
	}
	report, err = cleaner.Clean(ctx, testPolicy(), now, false)
	if err != nil || report.Sessions != 0 || report.Runs != 0 {
		t.Fatalf("second pass=%+v err=%v", report, err)
	}
}

func TestClean_FullBudgetRecoversAndRetainedHistoryReopens(t *testing.T) {
	store, ledger, ev, cleaner := newRetentionStores(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, status := range []string{"active", "paused", "completed"} {
		addHistory(t, store, ledger, ev, status, status, now.Add(-40*24*time.Hour))
	}
	const limit = 16 << 20
	if err := storage.ConfigureStorageBudget(ctx, store.DB(), limit); err != nil {
		t.Fatal(err)
	}
	var full error
	for range 128 {
		full = store.SaveMessage(&storage.Message{SessionID: "completed", Role: "assistant", Content: strings.Repeat("retired transcript ", 8192), Timestamp: now})
		if full != nil {
			break
		}
	}
	if !storage.IsStorageBudgetError(full) {
		t.Fatalf("storage did not stop at its budget: %v", full)
	}
	before, err := storage.ReadStorageBudget(ctx, store.DB())
	if err != nil {
		t.Fatal(err)
	}
	report, err := cleaner.Clean(ctx, testPolicy(), now.Add(40*24*time.Hour), false)
	if err != nil || report.Sessions != 1 || report.Runs != 1 || report.Storage.LimitBytes != limit || report.Storage.UsedBytes >= before.UsedBytes {
		t.Fatalf("full-budget cleanup: before=%+v report=%+v err=%v", before, report, err)
	}
	var path string
	if err := store.DB().QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := storage.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	ledger, err = runledger.NewWithDB(reopened.DB())
	if err != nil {
		t.Fatal(err)
	}
	ev, err = evidence.NewWithDB(reopened.DB(), filepath.Join(filepath.Dir(path), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"active", "paused"} {
		conv := conversation.New(id)
		if err := conv.LoadFromStorage(reopened); err != nil || len(conv.Messages) != 4 || conv.Messages[2].ToolCallID != "call" {
			t.Fatalf("resume %s: messages=%+v err=%v", id, conv.Messages, err)
		}
		if result, err := replay.Verify(ctx, ledger, ledger, ev, sessionexec.RunIDForSession(id)); err != nil || !result.Valid {
			t.Fatalf("replay %s: %+v err=%v", id, result, err)
		}
	}
	if err := reopened.SaveMessage(&storage.Message{SessionID: "active", Role: "user", Content: "continue", Timestamp: now}); err != nil {
		t.Fatalf("cleanup did not restore writes: %v", err)
	}
}

func TestClean_PreservesUnfinishedWork(t *testing.T) {
	for _, scenario := range []string{"accepted command", "started step", "blocked run", "live child", "live parent", "pending approval"} {
		t.Run(scenario, func(t *testing.T) {
			store, ledger, ev, cleaner := newRetentionStores(t)
			now := time.Now().UTC()
			old := now.Add(-200 * 24 * time.Hour)
			runID, _, _, obj := addHistory(t, store, ledger, ev, "protected", "completed", old)
			ctx := context.Background()
			var err error
			switch scenario {
			case "accepted command":
				_, err = store.Accept(ctx, sessionexec.AcceptRequest{SessionID: "protected", CommandID: "unfinished", Type: "input", Content: "continue", AcceptedBy: "test"})
			case "started step":
				_, _, err = ledger.BeginStep(ctx, runledger.ExecutionStep{RunID: runID, StepID: "unfinished", Kind: "tool", InputDigest: "input"})
			case "blocked run":
				_, err = store.DB().Exec(`UPDATE agent_runs SET status = 'blocked' WHERE run_id = ?`, runID)
			case "live child":
				_, err = ledger.StartRun(ctx, runledger.AgentRun{RunID: "child", SessionID: "other", ParentRunID: runID})
			case "live parent":
				_, err = ledger.StartRun(ctx, runledger.AgentRun{RunID: "parent", SessionID: "other"})
				if err == nil {
					_, err = store.DB().Exec(`UPDATE agent_runs SET parent_run_id = 'parent' WHERE run_id = ?`, runID)
				}
			case "pending approval":
				_, err = store.DB().Exec(`INSERT INTO pending_approvals(id,session_id,tool_name,tool_input,expires_at) VALUES('approval','protected','write_file','{}',?)`, now)
			}
			if err != nil {
				t.Fatal(err)
			}
			report, err := cleaner.Clean(ctx, testPolicy(), now, false)
			if err != nil || report.Sessions != 0 || report.Runs != 0 {
				t.Fatalf("report=%+v err=%v", report, err)
			}
			if _, err := ev.Get(ctx, obj.ID); err != nil {
				t.Fatalf("protected evidence lost: %v", err)
			}
		})
	}
}

func TestClean_MissingParentDoesNotProtectCompletedHistory(t *testing.T) {
	store, ledger, ev, cleaner := newRetentionStores(t)
	now := time.Now().UTC()
	runID, _, _, _ := addHistory(t, store, ledger, ev, "expired", "completed", now.Add(-40*24*time.Hour))
	if _, err := store.DB().Exec(`UPDATE agent_runs SET parent_run_id='missing-parent' WHERE run_id=?`, runID); err != nil {
		t.Fatal(err)
	}
	report, err := cleaner.Clean(context.Background(), testPolicy(), now, false)
	if err != nil || report.Sessions != 1 || report.Runs != 1 {
		t.Fatalf("missing parent prevented cleanup: %+v err=%v", report, err)
	}
}

func TestClean_StandaloneLedgerCountAndPins(t *testing.T) {
	ev, err := evidence.New(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ev.Close() })
	ledger, err := runledger.NewWithDB(ev.DB())
	if err != nil {
		t.Fatal(err)
	}
	cleaner, err := New(ev.DB(), ev)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("run-%d", i)
		at := now.Add(-time.Duration(4-i) * time.Hour)
		if _, err := ledger.StartRun(ctx, runledger.AgentRun{RunID: id, SessionID: "goal-worker", StartedAt: at}); err != nil {
			t.Fatal(err)
		}
		if _, err := ev.Put(ctx, evidence.Object{Kind: evidence.KindSubagentTask, MediaType: "text/plain", InlineBody: []byte(id), CreatedAt: now.Add(-40 * 24 * time.Hour), Metadata: map[string]any{evidence.MetaRunID: id}}); err != nil {
			t.Fatal(err)
		}
		if err := ledger.EndRun(ctx, id, "completed", at, nil); err != nil {
			t.Fatal(err)
		}
	}
	policy := testPolicy()
	policy.HistoryTTL = 0
	policy.MaxCompletedRuns = 2
	report, err := cleaner.Clean(ctx, policy, now, false)
	if err != nil || report.Runs != 2 || report.Evidence != 2 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	for i := 0; i < 4; i++ {
		_, err := ledger.GetRun(ctx, fmt.Sprintf("run-%d", i))
		if i < 2 && !errors.Is(err, runledger.ErrNotFound) || i >= 2 && err != nil {
			t.Fatalf("run-%d: %v", i, err)
		}
	}
}

func TestClean_RollbackKeepsRunAndPins(t *testing.T) {
	store, ledger, ev, cleaner := newRetentionStores(t)
	now := time.Now().UTC()
	runID, _, _, obj := addHistory(t, store, ledger, ev, "rollback", "completed", now.Add(-40*24*time.Hour))
	if _, err := store.DB().Exec(`CREATE TRIGGER fail_retention BEFORE DELETE ON sessions BEGIN SELECT RAISE(ABORT, 'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := cleaner.Clean(context.Background(), testPolicy(), now, false); err == nil {
		t.Fatal("expected cleanup failure")
	}
	if _, err := ledger.GetRun(context.Background(), runID); err != nil {
		t.Fatalf("run removed despite rollback: %v", err)
	}
	var pins int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM evidence_pins WHERE evidence_id=?`, obj.ID).Scan(&pins); err != nil || pins < 1 {
		t.Fatalf("pins=%d err=%v", pins, err)
	}
	if _, err := ev.Get(context.Background(), obj.ID); err != nil {
		t.Fatal(err)
	}
}

func TestClean_CountLimitExpiresWholeSessionsAndReclaimsEvidence(t *testing.T) {
	store, ledger, ev, cleaner := newRetentionStores(t)
	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		addHistory(t, store, ledger, ev, fmt.Sprintf("session-%d", i), "completed", now.Add(-time.Duration(5-i)*24*time.Hour))
	}
	policy := testPolicy()
	policy.HistoryTTL = 0
	policy.MaxCompletedSessions = 2
	policy.Evidence.EphemeralTTL = time.Hour
	report, err := cleaner.Clean(context.Background(), policy, now, false)
	if err != nil || report.Sessions != 3 || report.Runs != 3 || report.Evidence != 3 || report.Blobs != 3 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	var remaining int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&remaining); err != nil || remaining != 2 {
		t.Fatalf("remaining=%d err=%v", remaining, err)
	}
	for _, id := range []string{"session-3", "session-4"} {
		if report, err := replay.Verify(context.Background(), ledger, ledger, ev, sessionexec.RunIDForSession(id)); err != nil || !report.Valid {
			t.Fatalf("retained replay=%+v err=%v", report, err)
		}
	}
}

func TestClean_LegacyUnpinnedReferencesRemainReplayable(t *testing.T) {
	store, ledger, ev, cleaner := newRetentionStores(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-400 * 24 * time.Hour)
	runID, _, _, output := addHistory(t, store, ledger, ev, "legacy", "paused", old)
	if _, err := ev.ReleaseByReason(ctx, "run:"+runID); err != nil {
		t.Fatal(err)
	}
	request, err := ev.Put(ctx, evidence.Object{Kind: evidence.KindToolRequest, InlineBody: []byte("old request"), CreatedAt: old})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Append(ctx, runledger.Event{RunID: runID, Type: runledger.EventToolRequested, Payload: map[string]any{"step_id": "tool", "attempt": 1, "input_digest": "input", "request_evidence_id": request.ID}}); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := ev.Put(ctx, evidence.Object{Kind: evidence.KindCheckpoint, InlineBody: []byte("old checkpoint"), CreatedAt: old})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.CreateTaskCheckpoint(ctx, runledger.TaskCheckpoint{TaskID: "legacy-task", RunID: runID, SessionID: "legacy", Status: "parked", Reason: "resume", MarkdownEvidenceID: checkpoint.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := cleaner.Clean(ctx, testPolicy(), now, false); err != nil {
		t.Fatal(err)
	}
	for _, obj := range []evidence.Object{request, output, checkpoint} {
		if got, err := ev.Get(ctx, obj.ID); err != nil || !bytes.Equal(got.InlineBody, obj.InlineBody) {
			t.Fatalf("legacy evidence %s: %v", obj.Kind, err)
		}
	}
	if report, err := replay.Verify(ctx, ledger, ledger, ev, runID); err != nil || !report.Valid {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestClean_ContractedRunCascadesWithoutRemovingSharedEvidence(t *testing.T) {
	store, ledger, ev, cleaner := newRetentionStores(t)
	now := time.Now().UTC()
	runID, _, _, obj := addHistory(t, store, ledger, ev, "contracted", "completed", now.Add(-40*24*time.Hour))
	if _, err := store.DB().Exec(`INSERT INTO agent_run_contracts(run_id,session_id,input_digest,task_evidence_id,created_at) VALUES(?,'contracted',?,?,?)`, runID, obj.ContentSHA256, obj.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`INSERT INTO launch_envelopes(run_id,session_id,schema_version,profile_id,profile_version,profile_digest,envelope_digest,envelope_json,created_at)
		VALUES(?,'contracted','buckley.launch.envelope.v1','test','buckley.launch.profile.v1',?,?,'{}',?)`, runID, obj.ContentSHA256, obj.ContentSHA256, now); err != nil {
		t.Fatal(err)
	}
	if err := ev.Pin(context.Background(), obj.ID, "user:keep"); err != nil {
		t.Fatal(err)
	}
	report, err := cleaner.Clean(context.Background(), testPolicy(), now, false)
	if err != nil || report.Runs != 1 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	for _, table := range []string{"agent_run_contracts", "launch_envelopes"} {
		var count int
		if err := store.DB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
	if _, err := ev.Get(context.Background(), obj.ID); err != nil {
		t.Fatal(err)
	}
}

func TestClean_RetainedRunProtectsCheckpointInAnotherSession(t *testing.T) {
	store, ledger, ev, cleaner := newRetentionStores(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-40 * 24 * time.Hour)
	addHistory(t, store, ledger, ev, "old-session", "completed", old)
	runID, _, _, obj := addHistory(t, store, ledger, ev, "active-session", "active", old)
	if _, err := ledger.CreateTaskCheckpoint(ctx, runledger.TaskCheckpoint{TaskID: "cross-session", RunID: runID, SessionID: "old-session", Status: "completed", Reason: "resume", MarkdownEvidenceID: obj.ID}); err != nil {
		t.Fatal(err)
	}
	report, err := cleaner.Clean(ctx, testPolicy(), now, false)
	if err != nil || report.Sessions != 0 || report.Runs != 0 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if _, err := ledger.LatestTaskCheckpoint(ctx, "cross-session"); err != nil {
		t.Fatalf("retained checkpoint lost: %v", err)
	}
}

// The dry run must name what the real run removes. Evidence is only released
// once its runs are pruned, so a preview that counts runs alone reports zero.
func TestClean_DryRunReportsEvidenceReleasedByPrunedRuns(t *testing.T) {
	store, ledger, ev, cleaner := newRetentionStores(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-40 * 24 * time.Hour)
	for _, id := range []string{"old-a", "old-b"} {
		addHistory(t, store, ledger, ev, id, "completed", old)
	}
	addHistory(t, store, ledger, ev, "recent", "completed", now.Add(-time.Hour))

	preview, err := cleaner.Clean(ctx, testPolicy(), now, true)
	if err != nil {
		t.Fatal(err)
	}
	var objects int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM evidence_objects`).Scan(&objects); err != nil || objects != 3 {
		t.Fatalf("dry run changed evidence: %d objects, err=%v", objects, err)
	}
	real, err := cleaner.Clean(ctx, testPolicy(), now, false)
	if err != nil {
		t.Fatal(err)
	}
	if real.Evidence != 2 {
		t.Fatalf("real run removed %d evidence objects, want 2 (fixture is wrong)", real.Evidence)
	}
	if preview.Evidence != real.Evidence || preview.Blobs != real.Blobs || preview.Runs != real.Runs || preview.Sessions != real.Sessions {
		t.Fatalf("preview %+v differs from real run %+v", preview, real)
	}
	if preview.EvidenceBytes <= 0 {
		t.Fatalf("preview reports no bytes freed: %+v", preview)
	}
}
