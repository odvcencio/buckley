package headless

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/ipc/command"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/sessionexec"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

type observedForegroundLedger struct {
	runledger.Store
	runledger.FencedStepJournal
	started chan<- runledger.AgentRun
	release <-chan struct{}
	endErr  error
}

type blockingEffectJournal struct {
	sessionexec.Journal
	kind    sessionexec.EffectKind
	began   chan<- sessionexec.EffectPermit
	release <-chan struct{}
}

type duplicateEffectJournal struct {
	sessionexec.Journal
	kind     sessionexec.EffectKind
	original chan<- sessionexec.EffectPermit
}

type cancellationIgnoringTool struct {
	entered chan<- struct{}
	release <-chan struct{}
	calls   *atomic.Int32
}

func (t cancellationIgnoringTool) Name() string { return "ignoring_tool" }

func (t cancellationIgnoringTool) Description() string { return "waits without honoring cancellation" }

func (t cancellationIgnoringTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{Type: "object"}
}

func (t cancellationIgnoringTool) Execute(map[string]any) (*builtin.Result, error) {
	return t.ExecuteWithContext(context.Background(), nil)
}

func (t cancellationIgnoringTool) ExecuteWithContext(context.Context, map[string]any) (*builtin.Result, error) {
	t.calls.Add(1)
	select {
	case t.entered <- struct{}{}:
	default:
	}
	<-t.release
	return &builtin.Result{Success: true}, nil
}

func (j *duplicateEffectJournal) BeginEffect(ctx context.Context, request sessionexec.EffectRequest) (sessionexec.EffectPermit, error) {
	permit, err := j.Journal.BeginEffect(ctx, request)
	if err != nil || request.Kind != j.kind {
		return permit, err
	}
	if j.original != nil {
		j.original <- permit
	}
	return j.Journal.BeginEffect(ctx, request)
}

func (j *blockingEffectJournal) BeginEffect(ctx context.Context, request sessionexec.EffectRequest) (sessionexec.EffectPermit, error) {
	permit, err := j.Journal.BeginEffect(ctx, request)
	if err != nil || request.Kind != j.kind {
		return permit, err
	}
	if j.began != nil {
		j.began <- permit
	}
	if j.release != nil {
		<-j.release
	}
	return permit, nil
}

func reopenHeadlessTestStore(t *testing.T, store *storage.Store) *storage.Store {
	t.Helper()
	var sequence int
	var name, path string
	if err := store.DB().QueryRow(`PRAGMA database_list`).Scan(&sequence, &name, &path); err != nil {
		t.Fatal(err)
	}
	reopened, err := storage.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	return reopened
}

func (l *observedForegroundLedger) StartRun(ctx context.Context, run runledger.AgentRun) (runledger.AgentRun, error) {
	if run.TaskID == sessionexec.ForegroundTaskID {
		if l.started != nil {
			l.started <- run
		}
		if l.release != nil {
			select {
			case <-ctx.Done():
				return runledger.AgentRun{}, ctx.Err()
			case <-l.release:
			}
		}
	}
	return l.Store.StartRun(ctx, run)
}

func (l *observedForegroundLedger) EndRun(ctx context.Context, runID, status string, endedAt time.Time, outcome map[string]any) error {
	if l.endErr != nil {
		return l.endErr
	}
	return l.Store.EndRun(ctx, runID, status, endedAt, outcome)
}

func TestValidateForegroundRun_AllowsOnlyLiveRunning(t *testing.T) {
	want := runledger.AgentRun{
		RunID: "foreground-run", SessionID: "foreground-session", TaskID: sessionexec.ForegroundTaskID,
		AgentID: "headless", Backend: foregroundBackend, Status: "running",
	}
	if _, err := validateForegroundRun(want, want); err != nil {
		t.Fatalf("live running run rejected: %v", err)
	}
	ended := time.Now().UTC()
	tests := []runledger.AgentRun{
		{Status: ""}, {Status: "queued"}, {Status: "succeeded"}, {Status: "completed"},
		{Status: "failed"}, {Status: "blocked"}, {Status: "cancelled"}, {Status: "unknown"},
		{Status: "running", EndedAt: &ended}, {Status: "RUNNING"},
	}
	for _, mutation := range tests {
		got := want
		got.Status = mutation.Status
		got.EndedAt = mutation.EndedAt
		if _, err := validateForegroundRun(got, want); err == nil {
			t.Fatalf("accepted non-live foreground run: status=%q ended=%v", got.Status, got.EndedAt)
		}
	}
}

func TestRegistryCreateSession_ActivationFailureCancelsForegroundRun(t *testing.T) {
	store := newTestStore(t)
	evidenceStore, baseLedger := newRegistryDurableStores(t, store)
	fenced, ok := baseLedger.(runledger.FencedStepJournal)
	if !ok {
		t.Fatal("test ledger does not implement fenced step journal")
	}
	started := make(chan runledger.AgentRun, 1)
	ledger := &observedForegroundLedger{Store: baseLedger, FencedStepJournal: fenced, started: started}
	root := t.TempDir()
	createTestGitRepo(t, root)
	registry := NewRegistry(RegistryConfig{
		Store: store, ModelManager: newTestModelManager(t), Config: config.DefaultConfig(),
		ProjectRoot: root, RunLedger: ledger, EvidenceStore: evidenceStore,
	})
	registry.activateRunner = func(*Runner) error { return errors.New("forced activation failure") }
	t.Cleanup(registry.Stop)

	if info, err := registry.CreateSession(CreateSessionRequest{Principal: "alice", Project: root}); info != nil || err == nil || !strings.Contains(err.Error(), "forced activation failure") {
		t.Fatalf("CreateSession = info:%+v err:%v", info, err)
	}
	created := <-started
	if session, err := store.GetSession(created.SessionID); err != nil || session != nil {
		t.Fatalf("rolled-back session = %+v, %v", session, err)
	}
	run, err := baseLedger.GetRun(context.Background(), created.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "cancelled" || run.EndedAt == nil {
		t.Fatalf("orphan foreground run = %+v", run)
	}
}

func TestRegistryCreateSession_RollbackRetainsSessionWhenForegroundCancellationFails(t *testing.T) {
	store := newTestStore(t)
	evidenceStore, baseLedger := newRegistryDurableStores(t, store)
	fenced, ok := baseLedger.(runledger.FencedStepJournal)
	if !ok {
		t.Fatal("test ledger does not implement fenced step journal")
	}
	started := make(chan runledger.AgentRun, 1)
	ledger := &observedForegroundLedger{
		Store: baseLedger, FencedStepJournal: fenced, started: started,
		endErr: errors.New("forced foreground cancellation failure"),
	}
	root := t.TempDir()
	createTestGitRepo(t, root)
	registry := NewRegistry(RegistryConfig{
		Store: store, ModelManager: newTestModelManager(t), Config: config.DefaultConfig(),
		ProjectRoot: root, RunLedger: ledger, EvidenceStore: evidenceStore,
	})
	registry.activateRunner = func(*Runner) error { return errors.New("forced activation failure") }
	t.Cleanup(registry.Stop)

	if info, err := registry.CreateSession(CreateSessionRequest{Principal: "alice", Project: root}); info != nil || err == nil ||
		!strings.Contains(err.Error(), "retain unpublished session after foreground run cancellation failed") {
		t.Fatalf("CreateSession = info:%+v err:%v", info, err)
	}
	created := <-started
	retained, err := store.GetSession(created.SessionID)
	if err != nil || retained == nil {
		t.Fatalf("retained session = %+v, %v", retained, err)
	}
	state, err := store.GetExecutionState(context.Background(), created.SessionID)
	if err != nil || state.Mode != sessionexec.ExecutionModeDetached {
		t.Fatalf("retained execution state = %+v, %v", state, err)
	}
	run, err := baseLedger.GetRun(context.Background(), created.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "running" || run.EndedAt != nil {
		t.Fatalf("retained foreground run = %+v", run)
	}
}

func TestRegistryCreateSession_RollbackDeleteFailureLeavesTerminalRun(t *testing.T) {
	store := newTestStore(t)
	evidenceStore, baseLedger := newRegistryDurableStores(t, store)
	fenced, ok := baseLedger.(runledger.FencedStepJournal)
	if !ok {
		t.Fatal("test ledger does not implement fenced step journal")
	}
	if _, err := store.DB().Exec(`CREATE TRIGGER fail_unpublished_session_delete
		BEFORE DELETE ON sessions
		BEGIN SELECT RAISE(FAIL, 'forced unpublished delete failure'); END`); err != nil {
		t.Fatal(err)
	}
	started := make(chan runledger.AgentRun, 1)
	ledger := &observedForegroundLedger{Store: baseLedger, FencedStepJournal: fenced, started: started}
	root := t.TempDir()
	createTestGitRepo(t, root)
	registry := NewRegistry(RegistryConfig{
		Store: store, ModelManager: newTestModelManager(t), Config: config.DefaultConfig(),
		ProjectRoot: root, RunLedger: ledger, EvidenceStore: evidenceStore,
	})
	registry.activateRunner = func(*Runner) error { return errors.New("forced activation failure") }
	t.Cleanup(registry.Stop)

	if info, err := registry.CreateSession(CreateSessionRequest{Principal: "alice", Project: root}); info != nil || err == nil ||
		!strings.Contains(err.Error(), "rollback unpublished session") {
		t.Fatalf("CreateSession = info:%+v err:%v", info, err)
	}
	created := <-started
	retained, err := store.GetSession(created.SessionID)
	if err != nil || retained == nil {
		t.Fatalf("retained session = %+v, %v", retained, err)
	}
	state, err := store.GetExecutionState(context.Background(), created.SessionID)
	if err != nil || state.Mode != sessionexec.ExecutionModeDetached {
		t.Fatalf("retained execution state = %+v, %v", state, err)
	}
	run, err := baseLedger.GetRun(context.Background(), created.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "cancelled" || run.EndedAt == nil {
		t.Fatalf("foreground run after delete failure = %+v", run)
	}
}

func TestRegistryCreateSession_StopDuringStartRunCancelsForegroundRun(t *testing.T) {
	store := newTestStore(t)
	evidenceStore, baseLedger := newRegistryDurableStores(t, store)
	fenced, ok := baseLedger.(runledger.FencedStepJournal)
	if !ok {
		t.Fatal("test ledger does not implement fenced step journal")
	}
	started := make(chan runledger.AgentRun, 1)
	release := make(chan struct{})
	ledger := &observedForegroundLedger{Store: baseLedger, FencedStepJournal: fenced, started: started, release: release}
	root := t.TempDir()
	createTestGitRepo(t, root)
	registry := NewRegistry(RegistryConfig{
		Store: store, ModelManager: newTestModelManager(t), Config: config.DefaultConfig(),
		ProjectRoot: root, RunLedger: ledger, EvidenceStore: evidenceStore,
	})

	createDone := make(chan error, 1)
	go func() {
		_, err := registry.CreateSession(CreateSessionRequest{Principal: "alice", Project: root})
		createDone <- err
	}()
	created := <-started
	stopDone := make(chan struct{})
	go func() {
		registry.Stop()
		close(stopDone)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for registry.accepting() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if registry.accepting() {
		t.Fatal("registry did not enter shutdown while StartRun was blocked")
	}
	close(release)
	if err := <-createDone; !errors.Is(err, errRegistryShuttingDown) {
		t.Fatalf("CreateSession error = %v", err)
	}
	select {
	case <-stopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("registry Stop did not drain blocked CreateSession")
	}
	if session, err := store.GetSession(created.SessionID); err != nil || session != nil {
		t.Fatalf("rolled-back session = %+v, %v", session, err)
	}
	run, err := baseLedger.GetRun(context.Background(), created.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "cancelled" || run.EndedAt == nil {
		t.Fatalf("shutdown orphan foreground run = %+v", run)
	}
}

func TestRegistryRemoveSession_QuiescesAcrossStoresAndRestart(t *testing.T) {
	path := t.TempDir() + "/registry-quiesce.db"
	firstStore, err := storage.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = firstStore.Close() })
	secondStore, err := storage.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondStore.Close() })
	firstEvidence, firstLedger := newRegistryDurableStores(t, firstStore)
	secondEvidence, secondLedger := newRegistryDurableStores(t, secondStore)
	root := t.TempDir()
	createTestGitRepo(t, root)
	cfg := config.DefaultConfig()
	first := NewRegistry(RegistryConfig{
		Store: firstStore, ModelManager: newTestModelManager(t), Config: cfg,
		ProjectRoot: root, RunLedger: firstLedger, EvidenceStore: firstEvidence,
	})
	second := NewRegistry(RegistryConfig{
		Store: secondStore, ModelManager: newTestModelManager(t), Config: cfg,
		ProjectRoot: root, RunLedger: secondLedger, EvidenceStore: secondEvidence,
	})
	t.Cleanup(first.Stop)
	t.Cleanup(second.Stop)
	const sessionID = "registry-cross-store-quiesce"
	now := time.Now().UTC()
	if err := firstStore.CreateSession(&storage.Session{
		ID: sessionID, Principal: "alice", ProjectPath: root, Status: storage.SessionStatusPaused,
		CreatedAt: now, LastActive: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := firstStore.Accept(context.Background(), sessionexec.AcceptRequest{
		SessionID: sessionID, CommandID: "remove-active", Type: "input", Content: "active", AcceptedBy: "alice",
	}); err != nil {
		t.Fatal(err)
	}
	active, err := secondStore.ClaimNext(context.Background(), sessionexec.ClaimRequest{
		SessionID: sessionID, Lane: sessionexec.LaneWork, Owner: "other-process", LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := firstStore.Accept(context.Background(), sessionexec.AcceptRequest{
		SessionID: sessionID, CommandID: "remove-queued", Type: "input", Content: "queued", AcceptedBy: "alice",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := first.EnsureSession(sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := second.EnsureSession(sessionID); err != nil {
		t.Fatal(err)
	}
	if err := first.RemoveSession(sessionID); err != nil {
		t.Fatal(err)
	}
	state, err := secondStore.GetExecutionState(context.Background(), sessionID)
	if err != nil || state.Mode != sessionexec.ExecutionModeDetached {
		t.Fatalf("execution state = %+v, %v", state, err)
	}
	for _, commandID := range []string{active.CommandID, "remove-queued"} {
		receipt, err := secondStore.Get(context.Background(), sessionID, commandID)
		if err != nil || receipt.State != sessionexec.StateCancelled {
			t.Fatalf("quiesced receipt %s = %+v, %v", commandID, receipt, err)
		}
	}
	if _, err := secondStore.Accept(context.Background(), sessionexec.AcceptRequest{
		SessionID: sessionID, CommandID: "remove-after", Type: "input", Content: "no", AcceptedBy: "alice",
	}); !errors.Is(err, sessionexec.ErrSessionQuiesced) {
		t.Fatalf("cross-store accept after RemoveSession = %v", err)
	}
	second.Stop()
	restarted := NewRegistry(RegistryConfig{
		Store: secondStore, ModelManager: newTestModelManager(t), Config: cfg,
		ProjectRoot: root, RunLedger: secondLedger, EvidenceStore: secondEvidence,
	})
	t.Cleanup(restarted.Stop)
	if runner, err := restarted.EnsureSession(sessionID); runner != nil || !errors.Is(err, sessionexec.ErrSessionQuiesced) {
		t.Fatalf("EnsureSession after explicit remove = runner:%+v err:%v", runner, err)
	}
}

func TestRegistryAdoptSession_QuiescesDurableExecution(t *testing.T) {
	store := newTestStore(t)
	evidenceStore, ledger := newRegistryDurableStores(t, store)
	root := t.TempDir()
	createTestGitRepo(t, root)
	registry := NewRegistry(RegistryConfig{
		Store: store, ModelManager: newTestModelManager(t), Config: config.DefaultConfig(),
		ProjectRoot: root, RunLedger: ledger, EvidenceStore: evidenceStore,
	})
	t.Cleanup(registry.Stop)
	info, err := registry.CreateSession(CreateSessionRequest{Principal: "alice", Project: root})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetSessionStatus(info.ID, storage.SessionStatusPaused); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Accept(context.Background(), sessionexec.AcceptRequest{
		SessionID: info.ID, CommandID: "adopt-queued", Type: "input", Content: "queued", AcceptedBy: "alice",
	}); err != nil {
		t.Fatal(err)
	}
	adopted, err := registry.AdoptSession(info.ID)
	if err != nil || adopted == nil || adopted.ID != info.ID {
		t.Fatalf("AdoptSession = %+v, %v", adopted, err)
	}
	state, err := store.GetExecutionState(context.Background(), info.ID)
	if err != nil || state.Mode != sessionexec.ExecutionModeAdopted || state.ReasonCode != "session_adopted" {
		t.Fatalf("adopted execution state = %+v, %v", state, err)
	}
	receipt, err := store.Get(context.Background(), info.ID, "adopt-queued")
	if err != nil || receipt.State != sessionexec.StateCancelled {
		t.Fatalf("adopted command = %+v, %v", receipt, err)
	}
	if runner, err := registry.EnsureSession(info.ID); runner != nil || !errors.Is(err, sessionexec.ErrSessionQuiesced) {
		t.Fatalf("EnsureSession after adopt = runner:%+v err:%v", runner, err)
	}
}

func TestRegistryRemoteRemoveAndAdoptQuiesceWithoutLocalRunner(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode sessionexec.ExecutionMode
		act  func(*Registry, string) error
	}{
		{
			name: "remove", mode: sessionexec.ExecutionModeDetached,
			act: func(registry *Registry, sessionID string) error { return registry.RemoveSession(sessionID) },
		},
		{
			name: "adopt", mode: sessionexec.ExecutionModeAdopted,
			act: func(registry *Registry, sessionID string) error {
				_, err := registry.AdoptSession(sessionID)
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := t.TempDir() + "/remote-quiesce.db"
			firstStore, err := storage.New(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = firstStore.Close() })
			secondStore, err := storage.New(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = secondStore.Close() })
			firstEvidence, firstLedger := newRegistryDurableStores(t, firstStore)
			secondEvidence, secondLedger := newRegistryDurableStores(t, secondStore)
			root := t.TempDir()
			createTestGitRepo(t, root)
			cfg := config.DefaultConfig()
			first := NewRegistry(RegistryConfig{
				Store: firstStore, ModelManager: newTestModelManager(t), Config: cfg,
				ProjectRoot: root, RunLedger: firstLedger, EvidenceStore: firstEvidence,
			})
			second := NewRegistry(RegistryConfig{
				Store: secondStore, ModelManager: newTestModelManager(t), Config: cfg,
				ProjectRoot: root, RunLedger: secondLedger, EvidenceStore: secondEvidence,
			})
			t.Cleanup(first.Stop)
			t.Cleanup(second.Stop)

			sessionID := "remote-" + tc.name + "-session"
			now := time.Now().UTC()
			if err := firstStore.CreateSession(&storage.Session{
				ID: sessionID, Principal: "alice", ProjectPath: root,
				Status: storage.SessionStatusActive, CreatedAt: now, LastActive: now,
			}); err != nil {
				t.Fatal(err)
			}
			acceptInput := sessionexec.AcceptRequest{
				SessionID: sessionID, CommandID: "remote-active", Type: "input",
				Content: "active elsewhere", AcceptedBy: "alice",
			}
			if _, err := firstStore.Accept(context.Background(), acceptInput); err != nil {
				t.Fatal(err)
			}
			active, err := firstStore.ClaimNext(context.Background(), sessionexec.ClaimRequest{
				SessionID: sessionID, Lane: sessionexec.LaneWork,
				Owner: "remote-worker", LeaseDuration: time.Minute,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := firstStore.SetSessionStatus(sessionID, storage.SessionStatusPaused); err != nil {
				t.Fatal(err)
			}
			if _, err := first.EnsureSession(sessionID); err != nil {
				t.Fatal(err)
			}
			if _, ok := second.GetSession(sessionID); ok {
				t.Fatal("remote registry unexpectedly had a local runner")
			}

			if err := tc.act(second, sessionID); err != nil {
				t.Fatal(err)
			}
			state, err := firstStore.GetExecutionState(context.Background(), sessionID)
			if err != nil || state.Mode != tc.mode {
				t.Fatalf("execution state = %+v, %v", state, err)
			}
			receipt, err := firstStore.Get(context.Background(), sessionID, active.CommandID)
			if err != nil || receipt.State != sessionexec.StateCancelled {
				t.Fatalf("remote active command = %+v, %v", receipt, err)
			}
			if _, err := firstStore.Accept(context.Background(), sessionexec.AcceptRequest{
				SessionID: sessionID, CommandID: "remote-after", Type: "input",
				Content: "must fail", AcceptedBy: "alice",
			}); !errors.Is(err, sessionexec.ErrSessionQuiesced) {
				t.Fatalf("Accept after remote quiesce = %v", err)
			}
			if _, err := firstStore.ClaimNext(context.Background(), sessionexec.ClaimRequest{
				SessionID: sessionID, Lane: sessionexec.LaneWork,
				Owner: "later-worker", LeaseDuration: time.Minute,
			}); !errors.Is(err, sessionexec.ErrSessionQuiesced) {
				t.Fatalf("Claim after remote quiesce = %v", err)
			}
			if _, err := firstStore.BeginEffect(context.Background(), sessionexec.EffectRequest{
				Lease: active.Lease, EffectID: "remote-after-effect", Kind: sessionexec.EffectKindModel,
			}); !errors.Is(err, sessionexec.ErrSessionQuiesced) {
				t.Fatalf("BeginEffect after remote quiesce = %v", err)
			}
			restarted := NewRegistry(RegistryConfig{
				Store: secondStore, ModelManager: newTestModelManager(t), Config: cfg,
				ProjectRoot: root, RunLedger: secondLedger, EvidenceStore: secondEvidence,
			})
			t.Cleanup(restarted.Stop)
			if runner, err := restarted.EnsureSession(sessionID); runner != nil || !errors.Is(err, sessionexec.ErrSessionQuiesced) {
				t.Fatalf("Ensure after remote quiesce = runner:%+v err:%v", runner, err)
			}
		})
	}
}

func TestDurableRunner_TransientTranscriptLoadReleasesForSingleRetry(t *testing.T) {
	var providerCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		providerCalls.Add(1)
		_, _ = io.Copy(io.Discard, request.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-transcript-retry","model":"gpt-4o",
			"choices":[{"index":0,"message":{"role":"assistant","content":"loaded once"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`)
	}))
	t.Cleanup(server.Close)

	store := newTestStore(t)
	const sessionID = "durable-transcript-load-retry"
	now := time.Now().UTC()
	sess := &storage.Session{
		ID: sessionID, Principal: "alice", Model: "gpt-4o", ProjectPath: t.TempDir(),
		Status: storage.SessionStatusActive, CreatedAt: now, LastActive: now,
	}
	if err := store.CreateSession(sess); err != nil {
		t.Fatal(err)
	}
	evidenceStore, ledger := newRegistryDurableStores(t, store)
	if _, err := ensureForegroundRun(context.Background(), ledger, sessionID, sess.Model); err != nil {
		t.Fatal(err)
	}
	stepJournal, ok := ledger.(agentloop.DurableStepJournal)
	if !ok {
		t.Fatal("test ledger does not implement durable step journal")
	}
	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai"
	cfg.Models.Execution = "gpt-4o"
	manager, err := model.NewManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var loads atomic.Int32
	runner, err := NewRunner(RunnerConfig{
		Session: sess, ModelManager: manager, Tools: tool.NewEmptyRegistry(), Store: store, Config: cfg,
		CommandJournal: store, RunLedger: ledger, EvidenceStore: evidenceStore, StepJournal: stepJournal,
		LeaseOwner: "transcript-retry-owner",
		DurableTiming: &DurableTiming{
			LeaseDuration: time.Second, HeartbeatInterval: 100 * time.Millisecond,
			ScanInterval: 10 * time.Millisecond, CancellationInterval: 10 * time.Millisecond,
			OperationTimeout: time.Second,
		},
		TranscriptLoader: func(conv *conversation.Conversation, store *storage.Store) error {
			if loads.Add(1) == 1 {
				return errors.New("temporary transcript read outage")
			}
			return conv.LoadFromStorage(store)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runner.Stop)
	const commandID = "transcript-retry-command"
	if _, err := runner.AcceptCommand(context.Background(), command.SessionCommand{
		SessionID: sessionID, ID: commandID, Type: "input", Content: "execute once", AcceptedBy: "alice",
	}); err != nil {
		t.Fatal(err)
	}
	receipt := waitForCommandState(t, store, sessionID, commandID, sessionexec.StateSucceeded)
	if receipt.Attempt != 2 || loads.Load() != 2 || providerCalls.Load() != 1 {
		t.Fatalf("retry receipt=%+v loads=%d provider=%d", receipt, loads.Load(), providerCalls.Load())
	}
	messages, err := store.GetAllMessages(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Role != "user" || messages[1].Role != "assistant" {
		t.Fatalf("retry transcript = %+v", messages)
	}
}

func TestDurableRunner_QuiesceBeforeToolDispatchPreventsLaterSideEffects(t *testing.T) {
	permitBegan := make(chan sessionexec.EffectPermit, 1)
	releasePermit := make(chan struct{})
	providerEntered := make(chan struct{}, 1)
	releaseProvider := make(chan struct{})
	var providerCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		call := providerCalls.Add(1)
		_, _ = io.Copy(io.Discard, request.Body)
		w.Header().Set("Content-Type", "application/json")
		if call == 1 {
			providerEntered <- struct{}{}
			<-releaseProvider
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-quiesced-tool","model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"provider-call-after-quiesce","type":"function","function":{"name":"echo_tool","arguments":"{}"}}]},"finish_reason":"tool_calls"}]
			}`)
			return
		}
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-unexpected-round","model":"gpt-4o",
			"choices":[{"index":0,"message":{"role":"assistant","content":"unexpected"},"finish_reason":"stop"}]
		}`)
	}))
	t.Cleanup(server.Close)

	runner, store := newDurableHTTPRunnerConfigured(t, "durable-quiesce-before-tool", server.URL,
		func(cfg *RunnerConfig, store *storage.Store) {
			cfg.DurableTiming.CancellationInterval = time.Hour
			cfg.DurableTiming.HeartbeatInterval = time.Hour
			cfg.CommandJournal = &blockingEffectJournal{
				Journal: store, kind: sessionexec.EffectKindModel, began: permitBegan, release: releasePermit,
			}
		})
	secondStore := reopenHeadlessTestStore(t, store)
	var toolCalls atomic.Int32
	runner.tools.Register(countingEchoTool{calls: &toolCalls})
	const commandID = "quiesce-before-tool-command"
	if _, err := runner.AcceptCommand(context.Background(), command.SessionCommand{
		SessionID: runner.sessionID, ID: commandID, Type: "input",
		Content: "request a tool", AcceptedBy: "alice",
	}); err != nil {
		t.Fatal(err)
	}
	var modelPermit sessionexec.EffectPermit
	select {
	case modelPermit = <-permitBegan:
	case <-time.After(5 * time.Second):
		t.Fatal("model effect permit was not acquired")
	}
	wantModelEffectID := agentloop.StableStepID(
		sessionexec.RunIDForSession(runner.sessionID), sessionexec.ForegroundTaskID,
		sessionexec.TurnID(commandID, 0), 1, "model", 0,
	)
	if modelPermit.EffectID != wantModelEffectID || modelPermit.Kind != sessionexec.EffectKindModel {
		t.Fatalf("model permit identity = %q/%q, want %q/%q", modelPermit.EffectID, modelPermit.Kind, wantModelEffectID, sessionexec.EffectKindModel)
	}
	type quiesceResult struct {
		value sessionexec.QuiesceResult
		err   error
	}
	quiesced := make(chan quiesceResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		value, err := secondStore.QuiesceSession(ctx, runner.sessionID, sessionexec.ExecutionModeDetached, "test_detached")
		quiesced <- quiesceResult{value: value, err: err}
	}()
	deadline := time.Now().Add(time.Second)
	for {
		state, err := store.GetExecutionState(context.Background(), runner.sessionID)
		if err == nil && state.Mode == sessionexec.ExecutionModeDetached {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("model quiesce gate did not close: %+v, %v", state, err)
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case got := <-quiesced:
		t.Fatalf("quiesce returned while model permit was held: %+v", got)
	case <-time.After(30 * time.Millisecond):
	}
	if providerCalls.Load() != 0 {
		t.Fatal("provider started before the acquired permit was released")
	}
	close(releasePermit)
	select {
	case <-providerEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("permitted provider call did not start")
	}
	select {
	case got := <-quiesced:
		t.Fatalf("quiesce returned while provider effect was active: %+v", got)
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseProvider)
	gotQuiesce := <-quiesced
	if gotQuiesce.err != nil || gotQuiesce.value.State.Mode != sessionexec.ExecutionModeDetached {
		t.Fatalf("model quiesce = %+v, %v", gotQuiesce.value, gotQuiesce.err)
	}

	completionDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(completionDeadline) {
		runner.mu.RLock()
		active := runner.activeCommandID
		runner.mu.RUnlock()
		if active == "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	runner.mu.RLock()
	active := runner.activeCommandID
	runner.mu.RUnlock()
	if active != "" {
		t.Fatalf("quiesced command remained active: %q", active)
	}
	if got := toolCalls.Load(); got != 0 {
		t.Fatalf("tool calls after quiesce = %d, want zero", got)
	}
	if got := providerCalls.Load(); got != 1 {
		t.Fatalf("provider calls = %d, want only the pre-quiesce call", got)
	}
	if _, err := secondStore.BeginEffect(context.Background(), sessionexec.EffectRequest{
		Lease: modelPermit.Lease, EffectID: "model-after-quiesce", Kind: sessionexec.EffectKindModel,
	}); !errors.Is(err, sessionexec.ErrSessionQuiesced) {
		t.Fatalf("BeginEffect after completed quiesce = %v", err)
	}
	receipt, err := store.Get(context.Background(), runner.sessionID, commandID)
	if err != nil || receipt.State != sessionexec.StateCancelled {
		t.Fatalf("quiesced command = %+v, %v", receipt, err)
	}
}

func TestDurableRunner_QuiesceWaitsForPermittedToolEffect(t *testing.T) {
	var providerCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		providerCalls.Add(1)
		_, _ = io.Copy(io.Discard, request.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-tool-permit","model":"gpt-4o",
			"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"provider-tool-permit","type":"function","function":{"name":"echo_tool","arguments":"{}"}}]},"finish_reason":"tool_calls"}]
		}`)
	}))
	t.Cleanup(server.Close)

	permitBegan := make(chan sessionexec.EffectPermit, 1)
	releasePermit := make(chan struct{})
	runner, store := newDurableHTTPRunnerConfigured(t, "durable-tool-permit-quiesce", server.URL,
		func(cfg *RunnerConfig, store *storage.Store) {
			cfg.DurableTiming.CancellationInterval = time.Hour
			cfg.DurableTiming.HeartbeatInterval = time.Hour
			cfg.CommandJournal = &blockingEffectJournal{
				Journal: store, kind: sessionexec.EffectKindTool, began: permitBegan, release: releasePermit,
			}
		})
	secondStore := reopenHeadlessTestStore(t, store)
	var toolCalls atomic.Int32
	runner.tools.Register(countingEchoTool{calls: &toolCalls})
	const commandID = "tool-permit-command"
	if _, err := runner.AcceptCommand(context.Background(), command.SessionCommand{
		SessionID: runner.sessionID, ID: commandID, Type: "input",
		Content: "invoke one tool", AcceptedBy: "alice",
	}); err != nil {
		t.Fatal(err)
	}
	var toolPermit sessionexec.EffectPermit
	select {
	case toolPermit = <-permitBegan:
	case <-time.After(5 * time.Second):
		t.Fatal("tool effect permit was not acquired")
	}
	wantToolEffectID := agentloop.StableStepID(
		sessionexec.RunIDForSession(runner.sessionID), sessionexec.ForegroundTaskID,
		sessionexec.TurnID(commandID, 0), 1, "tool", 0,
	)
	if toolPermit.EffectID != wantToolEffectID || toolPermit.Kind != sessionexec.EffectKindTool {
		t.Fatalf("tool permit identity = %q/%q, want %q/%q", toolPermit.EffectID, toolPermit.Kind, wantToolEffectID, sessionexec.EffectKindTool)
	}
	quiesced := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err := secondStore.QuiesceSession(ctx, runner.sessionID, sessionexec.ExecutionModeAdopted, "tool_effect_test")
		quiesced <- err
	}()
	deadline := time.Now().Add(time.Second)
	for {
		state, err := store.GetExecutionState(context.Background(), runner.sessionID)
		if err == nil && state.Mode == sessionexec.ExecutionModeAdopted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("tool quiesce gate did not close: %+v, %v", state, err)
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case err := <-quiesced:
		t.Fatalf("quiesce returned while tool permit was held: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	if toolCalls.Load() != 0 {
		t.Fatal("tool ran before its acquired permit was released")
	}
	close(releasePermit)
	if err := <-quiesced; err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for toolCalls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if toolCalls.Load() != 1 || providerCalls.Load() != 1 {
		t.Fatalf("effects after permit release: tools=%d providers=%d", toolCalls.Load(), providerCalls.Load())
	}
	if _, err := secondStore.BeginEffect(context.Background(), sessionexec.EffectRequest{
		Lease: toolPermit.Lease, EffectID: "tool-after-quiesce", Kind: sessionexec.EffectKindTool,
	}); !errors.Is(err, sessionexec.ErrSessionQuiesced) {
		t.Fatalf("BeginEffect after tool quiesce = %v", err)
	}
}

func TestDurableRunner_DuplicateEffectNeverInvokesProviderOrTool(t *testing.T) {
	tests := []struct {
		name              string
		duplicateKind     sessionexec.EffectKind
		providerResponse  string
		wantProviderCalls int32
		wantStepKind      string
	}{
		{
			name:              "model",
			duplicateKind:     sessionexec.EffectKindModel,
			providerResponse:  `{"id":"unused","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"unused"},"finish_reason":"stop"}]}`,
			wantProviderCalls: 0,
			wantStepKind:      "model",
		},
		{
			name:              "tool",
			duplicateKind:     sessionexec.EffectKindTool,
			providerResponse:  `{"id":"tool-duplicate","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"provider-tool-duplicate","type":"function","function":{"name":"echo_tool","arguments":"{\"text\":\"never\"}"}}]},"finish_reason":"tool_calls"}]}`,
			wantProviderCalls: 1,
			wantStepKind:      "tool",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var providerCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				providerCalls.Add(1)
				_, _ = io.Copy(io.Discard, request.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, test.providerResponse)
			}))
			t.Cleanup(server.Close)

			original := make(chan sessionexec.EffectPermit, 1)
			runner, store := newDurableHTTPRunnerConfigured(t, "durable-duplicate-"+test.name, server.URL,
				func(cfg *RunnerConfig, store *storage.Store) {
					cfg.CommandJournal = &duplicateEffectJournal{
						Journal: store, kind: test.duplicateKind, original: original,
					}
				})
			var toolCalls atomic.Int32
			runner.tools.Register(countingEchoTool{calls: &toolCalls})
			commandID := "duplicate-" + test.name + "-command"
			if _, err := runner.AcceptCommand(context.Background(), command.SessionCommand{
				SessionID: runner.sessionID, ID: commandID, Type: "input",
				Content: "exercise duplicate effect", AcceptedBy: "alice",
			}); err != nil {
				t.Fatal(err)
			}
			permit := <-original
			wantEffectID := agentloop.StableStepID(
				sessionexec.RunIDForSession(runner.sessionID), sessionexec.ForegroundTaskID,
				sessionexec.TurnID(commandID, 0), 1, test.wantStepKind, 0,
			)
			if permit.EffectID != wantEffectID || permit.State != sessionexec.EffectStateActive {
				t.Fatalf("original permit = %+v, want effect %q", permit, wantEffectID)
			}
			receipt := waitForCommandState(t, store, runner.sessionID, commandID, sessionexec.StateBlocked)
			if receipt.ErrorCode != "ambiguous_effect" {
				t.Fatalf("blocked receipt = %+v", receipt)
			}
			if got := providerCalls.Load(); got != test.wantProviderCalls {
				t.Fatalf("provider calls = %d, want %d", got, test.wantProviderCalls)
			}
			if got := toolCalls.Load(); got != 0 {
				t.Fatalf("tool calls = %d, want zero", got)
			}
			if err := store.EndEffect(context.Background(), permit); err != nil {
				t.Fatalf("end original ambiguous permit: %v", err)
			}
		})
	}
}

func TestDurableRunner_CancellationIgnoringEffectPastExpiryRetainsQuiesceBarrier(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"ignoring-tool","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"provider-ignoring-tool","type":"function","function":{"name":"ignoring_tool","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
	}))
	t.Cleanup(server.Close)

	permitBegan := make(chan sessionexec.EffectPermit, 1)
	releasePermit := make(chan struct{})
	var permitReleaseOnce sync.Once
	t.Cleanup(func() { permitReleaseOnce.Do(func() { close(releasePermit) }) })
	runner, store := newDurableHTTPRunnerConfigured(t, "durable-ignoring-effect", server.URL,
		func(cfg *RunnerConfig, store *storage.Store) {
			cfg.DurableTiming.HeartbeatInterval = time.Hour
			cfg.DurableTiming.CancellationInterval = time.Hour
			cfg.DurableTiming.ScanInterval = time.Hour
			cfg.CommandJournal = &blockingEffectJournal{
				Journal: store, kind: sessionexec.EffectKindTool, began: permitBegan, release: releasePermit,
			}
		})
	secondStore := reopenHeadlessTestStore(t, store)
	toolEntered := make(chan struct{}, 1)
	releaseTool := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseTool) }) })
	var toolCalls atomic.Int32
	runner.tools.Register(cancellationIgnoringTool{entered: toolEntered, release: releaseTool, calls: &toolCalls})
	const commandID = "ignoring-effect-command"
	if _, err := runner.AcceptCommand(context.Background(), command.SessionCommand{
		SessionID: runner.sessionID, ID: commandID, Type: "input",
		Content: "invoke the ignoring tool", AcceptedBy: "alice",
	}); err != nil {
		t.Fatal(err)
	}
	var permit sessionexec.EffectPermit
	select {
	case permit = <-permitBegan:
	case <-time.After(5 * time.Second):
		receipt, err := store.Get(context.Background(), runner.sessionID, commandID)
		t.Fatalf("tool effect permit was not acquired: receipt=%+v err=%v", receipt, err)
	}
	var shortExpiry int64
	if err := store.DB().QueryRow(`SELECT CAST(strftime('%s','now') AS INTEGER) * 1000 +
		CAST(substr(strftime('%f','now'), 4, 3) AS INTEGER) + 100`).Scan(&shortExpiry); err != nil {
		t.Fatal(err)
	}
	tx, err := store.DB().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE session_commands SET lease_expires_at_ms = ?
		WHERE session_id = ? AND command_id = ?`, shortExpiry, runner.sessionID, commandID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE session_effect_permits SET expires_at_ms = ?
		WHERE session_id = ? AND command_id = ? AND effect_id = ?`,
		shortExpiry, runner.sessionID, commandID, permit.EffectID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	permitReleaseOnce.Do(func() { close(releasePermit) })
	select {
	case <-toolEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation-ignoring tool did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	quiesced, err := secondStore.QuiesceSession(ctx, runner.sessionID, sessionexec.ExecutionModeDetached, "ignoring_effect")
	cancel()
	if !errors.Is(err, sessionexec.ErrQuiescenceIncomplete) || quiesced.State.Mode != sessionexec.ExecutionModeDetached {
		t.Fatalf("quiesce with expired running effect = %+v, %v", quiesced, err)
	}
	if wait := time.Until(time.UnixMilli(shortExpiry)) + 30*time.Millisecond; wait > 0 {
		time.Sleep(wait)
	}
	if _, err := store.Release(context.Background(), permit.Lease); !errors.Is(err, sessionexec.ErrEffectAmbiguous) {
		t.Fatalf("Release with ambiguous effect = %v", err)
	}
	if _, err := store.Complete(context.Background(), permit.Lease, sessionexec.Completion{State: sessionexec.StateSucceeded}, nil); !errors.Is(err, sessionexec.ErrEffectAmbiguous) {
		t.Fatalf("Complete with ambiguous effect = %v", err)
	}
	releaseOnce.Do(func() { close(releaseTool) })
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var state string
		err := store.DB().QueryRow(`SELECT state FROM session_effect_permits
			WHERE session_id = ? AND command_id = ? AND effect_id = ?`,
			runner.sessionID, commandID, permit.EffectID).Scan(&state)
		if err == nil && state == string(sessionexec.EffectStateEnded) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := secondStore.QuiesceSession(context.Background(), runner.sessionID, sessionexec.ExecutionModeDetached, "ignoring_effect"); err != nil {
		t.Fatalf("quiesce after exact EndEffect = %v", err)
	}
	if got := toolCalls.Load(); got != 1 {
		t.Fatalf("ignoring tool calls = %d, want one", got)
	}
}
