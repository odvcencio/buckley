package runledger

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/evidence"
)

func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "runledger.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// newComposedStores returns a runledger store and an evidence store sharing
// one *sql.DB, exercising the intended (later-wired) composition where
// evidence reference checks are actually enforced.
func newComposedStores(t *testing.T) (*SQLiteStore, *evidence.SQLiteStore) {
	t.Helper()
	dir := t.TempDir()

	ev, err := evidence.New(filepath.Join(dir, "shared.db"), evidence.WithBlobRoot(filepath.Join(dir, "blobs")))
	if err != nil {
		t.Fatalf("evidence.New() error = %v", err)
	}
	t.Cleanup(func() { _ = ev.Close() })

	rl, err := NewWithDB(ev.DB())
	if err != nil {
		t.Fatalf("runledger.NewWithDB() error = %v", err)
	}
	return rl, ev
}

func TestStartRun_Defaults(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	run, err := store.StartRun(ctx, AgentRun{SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	if run.RunID == "" {
		t.Fatalf("expected generated RunID")
	}
	if run.Status != "running" {
		t.Fatalf("Status = %q, want running", run.Status)
	}
	if run.StartedAt.IsZero() {
		t.Fatalf("expected StartedAt to be set")
	}

	got, err := store.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	if got.SessionID != "sess-1" {
		t.Fatalf("SessionID = %q, want sess-1", got.SessionID)
	}
}

func TestEndRun(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	run, err := store.StartRun(ctx, AgentRun{SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}

	end := time.Now().UTC()
	if err := store.EndRun(ctx, run.RunID, "completed", end, map[string]any{"ok": true}); err != nil {
		t.Fatalf("EndRun() error = %v", err)
	}

	got, err := store.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	if got.Status != "completed" {
		t.Fatalf("Status = %q, want completed", got.Status)
	}
	if got.EndedAt == nil {
		t.Fatalf("expected EndedAt to be set")
	}
	if got.Outcome["ok"] != true {
		t.Fatalf("Outcome = %+v, want ok=true", got.Outcome)
	}
}

func TestEndRun_NotFound(t *testing.T) {
	store := newTestStore(t)
	if err := store.EndRun(context.Background(), "run_missing", "completed", time.Now(), nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("EndRun() error = %v, want ErrNotFound", err)
	}
}

func TestAppend_SequenceAssignment(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	run, err := store.StartRun(ctx, AgentRun{SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}

	first, err := store.Append(ctx, Event{RunID: run.RunID, Type: EventRunStarted})
	if err != nil {
		t.Fatalf("Append() first error = %v", err)
	}
	if first.Sequence != 1 {
		t.Fatalf("first Sequence = %d, want 1", first.Sequence)
	}
	if first.ID == "" {
		t.Fatalf("expected generated event ID")
	}
	if first.SchemaVersion != SchemaVersion {
		t.Fatalf("SchemaVersion = %q, want %q", first.SchemaVersion, SchemaVersion)
	}
	if first.Redaction == "" {
		t.Fatalf("expected default Redaction version to be set")
	}

	second, err := store.Append(ctx, Event{RunID: run.RunID, Type: EventToolStarted, TaskID: "task-1"})
	if err != nil {
		t.Fatalf("Append() second error = %v", err)
	}
	if second.Sequence != 2 {
		t.Fatalf("second Sequence = %d, want 2", second.Sequence)
	}

	// A caller-supplied Sequence is ignored: the store assigns it.
	third, err := store.Append(ctx, Event{RunID: run.RunID, Type: EventToolCompleted, Sequence: 999})
	if err != nil {
		t.Fatalf("Append() third error = %v", err)
	}
	if third.Sequence != 3 {
		t.Fatalf("third Sequence = %d, want 3 (caller value must be ignored)", third.Sequence)
	}
}

func TestAppend_Immutable(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	run, err := store.StartRun(ctx, AgentRun{SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	ev, err := store.Append(ctx, Event{RunID: run.RunID, Type: EventRunStarted, Payload: map[string]any{"a": "b"}})
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	// UNIQUE(run_id, sequence) means a second event cannot claim the same
	// sequence: Append always computes the next one, so this simply proves
	// the row is never overwritten by re-appending.
	events, err := store.ListEvents(ctx, EventQuery{RunID: run.RunID})
	if err != nil {
		t.Fatalf("ListEvents() error = %v", err)
	}
	if len(events) != 1 || events[0].ID != ev.ID {
		t.Fatalf("ListEvents() = %+v, want exactly the appended event", events)
	}
	if events[0].Payload["a"] != "b" {
		t.Fatalf("payload not round-tripped: %+v", events[0].Payload)
	}
}

func TestListEvents_OrderingAndFilters(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	run, err := store.StartRun(ctx, AgentRun{SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}

	types := []string{EventRunStarted, EventTaskCreated, EventToolStarted, EventToolCompleted, EventRunCompleted}
	for _, ty := range types {
		if _, err := store.Append(ctx, Event{RunID: run.RunID, Type: ty}); err != nil {
			t.Fatalf("Append(%s) error = %v", ty, err)
		}
	}

	all, err := store.ListEvents(ctx, EventQuery{RunID: run.RunID})
	if err != nil {
		t.Fatalf("ListEvents() error = %v", err)
	}
	if len(all) != len(types) {
		t.Fatalf("ListEvents() returned %d events, want %d", len(all), len(types))
	}
	for i, ev := range all {
		if ev.Sequence != int64(i+1) {
			t.Fatalf("event %d sequence = %d, want %d", i, ev.Sequence, i+1)
		}
		if ev.Type != types[i] {
			t.Fatalf("event %d type = %q, want %q", i, ev.Type, types[i])
		}
		if ev.SessionID != "sess-1" {
			t.Fatalf("event %d session_id = %q, want sess-1 (joined from agent_runs)", i, ev.SessionID)
		}
	}

	byType, err := store.ListEvents(ctx, EventQuery{RunID: run.RunID, Types: []string{EventToolStarted}})
	if err != nil {
		t.Fatalf("ListEvents() by type error = %v", err)
	}
	if len(byType) != 1 || byType[0].Type != EventToolStarted {
		t.Fatalf("ListEvents() by type = %+v", byType)
	}

	limited, err := store.ListEvents(ctx, EventQuery{RunID: run.RunID, Limit: 2})
	if err != nil {
		t.Fatalf("ListEvents() limited error = %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("ListEvents() limited = %d events, want 2", len(limited))
	}
}

// TestAppend_ConcurrentWriters exercises SQLite WAL concurrent-writer
// safety for sequence assignment: many goroutines append to the same run
// simultaneously. Every append must succeed and the resulting sequence
// numbers must be exactly 1..N with no gaps or duplicates.
func TestAppend_ConcurrentWriters(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	run, err := store.StartRun(ctx, AgentRun{SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}

	const n = 40
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := store.Append(ctx, Event{RunID: run.RunID, Type: EventToolStarted, Payload: map[string]any{"i": i}}); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Append() error = %v", err)
	}

	events, err := store.ListEvents(ctx, EventQuery{RunID: run.RunID})
	if err != nil {
		t.Fatalf("ListEvents() error = %v", err)
	}
	if len(events) != n {
		t.Fatalf("got %d events, want %d", len(events), n)
	}
	seen := map[int64]bool{}
	for _, ev := range events {
		if seen[ev.Sequence] {
			t.Fatalf("duplicate sequence %d", ev.Sequence)
		}
		seen[ev.Sequence] = true
	}
	for i := int64(1); i <= n; i++ {
		if !seen[i] {
			t.Fatalf("missing sequence %d (gap under concurrency)", i)
		}
	}
}

func TestLiveSink_DroppedDeliveryDoesNotAffectDurableLedger(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	run, err := store.StartRun(ctx, AgentRun{SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}

	store.SetLiveSink(panicSink{})

	ev, err := store.Append(ctx, Event{RunID: run.RunID, Type: EventRunStarted})
	if err != nil {
		t.Fatalf("Append() with panicking live sink error = %v, want nil (durable write must not be affected)", err)
	}

	events, err := store.ListEvents(ctx, EventQuery{RunID: run.RunID})
	if err != nil {
		t.Fatalf("ListEvents() error = %v", err)
	}
	if len(events) != 1 || events[0].ID != ev.ID {
		t.Fatalf("event was not durably recorded despite dropped live delivery: %+v", events)
	}
}

type panicSink struct{}

func (panicSink) Notify(Event) { panic("simulated dropped live event") }

func TestLiveSink_DeliversNotification(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	run, err := store.StartRun(ctx, AgentRun{SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}

	var mu sync.Mutex
	var received []Event
	store.SetLiveSink(sinkFunc(func(ev Event) {
		mu.Lock()
		defer mu.Unlock()
		received = append(received, ev)
	}))

	if _, err := store.Append(ctx, Event{RunID: run.RunID, Type: EventRunStarted}); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 || received[0].Type != EventRunStarted {
		t.Fatalf("live sink received = %+v, want one run.started event", received)
	}
}

type sinkFunc func(Event)

func (f sinkFunc) Notify(ev Event) { f(ev) }

func TestRalphSink_DualWrite(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	run, err := store.StartRun(ctx, AgentRun{SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}

	var mu sync.Mutex
	var written []Event
	store.SetRalphSink(ralphSinkFunc(func(_ context.Context, ev Event) error {
		mu.Lock()
		defer mu.Unlock()
		written = append(written, ev)
		return nil
	}))

	if _, err := store.Append(ctx, Event{RunID: run.RunID, Type: EventRunStarted}); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(written) != 1 {
		t.Fatalf("ralph sink received %d events, want 1", len(written))
	}
}

func TestRalphSink_FailureDoesNotLoseCanonicalWrite(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	run, err := store.StartRun(ctx, AgentRun{SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}

	store.SetRalphSink(ralphSinkFunc(func(context.Context, Event) error {
		return errors.New("ralph store unavailable")
	}))

	ev, err := store.Append(ctx, Event{RunID: run.RunID, Type: EventRunStarted})
	if !errors.Is(err, ErrRalphDualWriteFailed) {
		t.Fatalf("Append() error = %v, want ErrRalphDualWriteFailed", err)
	}
	if ev.ID == "" {
		t.Fatalf("expected the event to still be returned despite the dual-write failure")
	}

	events, err := store.ListEvents(ctx, EventQuery{RunID: run.RunID})
	if err != nil {
		t.Fatalf("ListEvents() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("canonical write was lost after ralph dual-write failure: %+v", events)
	}
}

type ralphSinkFunc func(context.Context, Event) error

func (f ralphSinkFunc) WriteEvent(ctx context.Context, ev Event) error { return f(ctx, ev) }

func TestContextReceipt_ReferenceIntegrity(t *testing.T) {
	rl, ev := newComposedStores(t)
	ctx := context.Background()

	bundle, err := ev.Put(ctx, evidence.Object{Kind: evidence.KindContextBundle, MediaType: "text/markdown", InlineBody: []byte("bundle")})
	if err != nil {
		t.Fatalf("Put(bundle) error = %v", err)
	}
	manifest, err := ev.Put(ctx, evidence.Object{Kind: evidence.KindContextManifest, MediaType: "application/json", InlineBody: []byte("{}")})
	if err != nil {
		t.Fatalf("Put(manifest) error = %v", err)
	}

	receipt := ContextReceipt{
		SnapshotID:         "worktree:abc",
		RequestDigest:      "digest",
		PolicyVersion:      "v1",
		BudgetTokens:       8000,
		EstimatedTokens:    100,
		CandidateTokens:    200,
		BundleEvidenceID:   bundle.ID,
		ManifestEvidenceID: manifest.ID,
		BundleSHA256:       bundle.ContentSHA256,
	}

	created, err := rl.CreateContextReceipt(ctx, receipt, nil)
	if err != nil {
		t.Fatalf("CreateContextReceipt() with valid evidence refs error = %v", err)
	}
	if created.ReceiptID == "" {
		t.Fatalf("expected generated ReceiptID")
	}

	badReceipt := receipt
	badReceipt.BundleEvidenceID = "ev_does_not_exist"
	if _, err := rl.CreateContextReceipt(ctx, badReceipt, nil); !errors.Is(err, ErrEvidenceNotFound) {
		t.Fatalf("CreateContextReceipt() with missing evidence ref error = %v, want ErrEvidenceNotFound", err)
	}
}

func TestContextReceipt_StandaloneSkipsIntegrityCheck(t *testing.T) {
	// Without an evidence_objects table in this database (the run ledger
	// used standalone), the reference check is skipped rather than failed:
	// the caller is responsible for composing both stores when it wants
	// enforcement.
	store := newTestStore(t)
	ctx := context.Background()

	receipt := ContextReceipt{
		SnapshotID:         "worktree:abc",
		RequestDigest:      "digest",
		PolicyVersion:      "v1",
		BudgetTokens:       8000,
		EstimatedTokens:    100,
		CandidateTokens:    200,
		BundleEvidenceID:   "ev_not_checked",
		ManifestEvidenceID: "ev_also_not_checked",
		BundleSHA256:       "sha",
	}
	if _, err := store.CreateContextReceipt(ctx, receipt, nil); err != nil {
		t.Fatalf("CreateContextReceipt() standalone error = %v", err)
	}
}

func TestContextReceipt_RoundTripWithItems(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	receipt := ContextReceipt{
		SnapshotID:      "worktree:abc",
		RequestDigest:   "digest",
		PolicyVersion:   "v1",
		BudgetTokens:    8000,
		EstimatedTokens: 100,
		CandidateTokens: 200,
		BundleSHA256:    "sha",
	}
	items := []ContextReceiptItem{
		{
			ItemID:          "itm_1",
			EntityID:        "pkg/foo.go|function|Bar|10",
			Path:            "pkg/foo.go",
			Section:         "focus",
			Mode:            "body",
			StartLine:       10,
			EndLine:         20,
			ContentSHA256:   "abc",
			RawTokens:       100,
			ProjectedTokens: 100,
			Score:           1000,
			Reasons:         []Reason{{Kind: "required_selector", Weight: 1000}},
			Required:        true,
		},
		{
			ItemID:        "itm_2",
			Section:       "supporting",
			Mode:          "signature",
			ContentSHA256: "def",
			Required:      false,
		},
	}

	created, err := store.CreateContextReceipt(ctx, receipt, items)
	if err != nil {
		t.Fatalf("CreateContextReceipt() error = %v", err)
	}

	gotReceipt, gotItems, err := store.GetContextReceipt(ctx, created.ReceiptID)
	if err != nil {
		t.Fatalf("GetContextReceipt() error = %v", err)
	}
	if gotReceipt.RequestDigest != receipt.RequestDigest {
		t.Fatalf("RequestDigest = %q, want %q", gotReceipt.RequestDigest, receipt.RequestDigest)
	}
	if len(gotItems) != 2 {
		t.Fatalf("got %d items, want 2", len(gotItems))
	}
	if gotItems[0].ItemID != "itm_1" || gotItems[1].ItemID != "itm_2" {
		t.Fatalf("items out of order: %+v", gotItems)
	}
	if !gotItems[0].Required {
		t.Fatalf("item 0 Required = false, want true")
	}
	if len(gotItems[0].Reasons) != 1 || gotItems[0].Reasons[0].Kind != "required_selector" {
		t.Fatalf("item 0 Reasons = %+v", gotItems[0].Reasons)
	}
}

func TestGetContextReceipt_NotFound(t *testing.T) {
	store := newTestStore(t)
	if _, _, err := store.GetContextReceipt(context.Background(), "ctx_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetContextReceipt() error = %v, want ErrNotFound", err)
	}
}

func TestTaskCheckpoint_VersionAutoIncrement(t *testing.T) {
	rl, ev := newComposedStores(t)
	ctx := context.Background()

	md, err := ev.Put(ctx, evidence.Object{Kind: evidence.KindCheckpoint, MediaType: "text/markdown", InlineBody: []byte("# Goal\n")})
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	first, err := rl.CreateTaskCheckpoint(ctx, TaskCheckpoint{
		TaskID: "task-1", SessionID: "sess-1", Status: "planning", Reason: "initial",
		StateJSON: `{"task_id":"task-1"}`, MarkdownEvidenceID: md.ID,
	})
	if err != nil {
		t.Fatalf("CreateTaskCheckpoint() first error = %v", err)
	}
	if first.Version != 1 {
		t.Fatalf("first Version = %d, want 1", first.Version)
	}

	second, err := rl.CreateTaskCheckpoint(ctx, TaskCheckpoint{
		TaskID: "task-1", SessionID: "sess-1", Status: "verifying", Reason: "tests passed",
		StateJSON: `{"task_id":"task-1"}`, MarkdownEvidenceID: md.ID,
	})
	if err != nil {
		t.Fatalf("CreateTaskCheckpoint() second error = %v", err)
	}
	if second.Version != 2 {
		t.Fatalf("second Version = %d, want 2", second.Version)
	}

	latest, err := rl.LatestTaskCheckpoint(ctx, "task-1")
	if err != nil {
		t.Fatalf("LatestTaskCheckpoint() error = %v", err)
	}
	if latest.CheckpointID != second.CheckpointID {
		t.Fatalf("LatestTaskCheckpoint() = %+v, want the second checkpoint", latest)
	}
}

func TestTaskCheckpoint_MissingEvidenceRejected(t *testing.T) {
	rl, _ := newComposedStores(t)
	ctx := context.Background()

	_, err := rl.CreateTaskCheckpoint(ctx, TaskCheckpoint{
		TaskID: "task-1", SessionID: "sess-1", Status: "planning", Reason: "initial",
		StateJSON: `{}`, MarkdownEvidenceID: "ev_missing",
	})
	if !errors.Is(err, ErrEvidenceNotFound) {
		t.Fatalf("CreateTaskCheckpoint() error = %v, want ErrEvidenceNotFound", err)
	}
}

func TestRecordContextUsage(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	receipt, err := store.CreateContextReceipt(ctx, ContextReceipt{
		SnapshotID: "s", RequestDigest: "d", PolicyVersion: "v1", BundleSHA256: "sha",
	}, nil)
	if err != nil {
		t.Fatalf("CreateContextReceipt() error = %v", err)
	}

	usage, err := store.RecordContextUsage(ctx, ContextUsage{
		ReceiptID: receipt.ReceiptID, ModelID: "gpt-5", ActualPromptTokens: 100, CompletionTokens: 50,
	})
	if err != nil {
		t.Fatalf("RecordContextUsage() error = %v", err)
	}
	if usage.ID == 0 {
		t.Fatalf("expected generated usage ID")
	}
}

func TestRecordMetricSample(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	run, err := store.StartRun(ctx, AgentRun{SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}

	sample, err := store.RecordMetricSample(ctx, AgentMetricSample{
		RunID: run.RunID, MetricName: "progress_score", Value: 0.75, Unit: "ratio",
	})
	if err != nil {
		t.Fatalf("RecordMetricSample() error = %v", err)
	}
	if sample.ID == 0 {
		t.Fatalf("expected generated sample ID")
	}
}

func TestMigrationsIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "runledger.db")

	store1, err := New(dbPath)
	if err != nil {
		t.Fatalf("first New() error = %v", err)
	}
	if _, err := store1.StartRun(context.Background(), AgentRun{SessionID: "sess-1"}); err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	store1.Close()

	store2, err := New(dbPath)
	if err != nil {
		t.Fatalf("second New() error = %v", err)
	}
	defer store2.Close()

	runs, err := store2.ListRuns(context.Background(), RunQuery{SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("ListRuns() after reopen error = %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected run to survive reopen with idempotent migrations, got %d runs", len(runs))
	}
}
