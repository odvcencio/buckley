package headless

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/agentcoord"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

const headlessDurabilityChildStubEnv = "BUCKLEY_TEST_HEADLESS_DURABILITY_CHILD_STUB"

type runnerLifecycleCapture struct {
	mu            sync.Mutex
	runnerEvents  []RunnerEvent
	storageEvents []storage.Event
}

type observableHookProbe struct {
	mu                sync.Mutex
	prepared          int
	activationCalls   int
	closeCalls        int
	active            int
	activationEntered chan struct{}
	releaseActivation <-chan struct{}
	activationErr     error
}

type observableHookPlan struct {
	probe     *observableHookProbe
	mu        sync.Mutex
	activated bool
	closed    bool
}

func (p *observableHookProbe) factory(_ *tool.Registry, enabled bool, _ time.Duration) (configuredHookPlan, error) {
	if !enabled {
		return nil, nil
	}
	p.mu.Lock()
	p.prepared++
	p.mu.Unlock()
	return &observableHookPlan{probe: p}, nil
}

func (p *observableHookPlan) Activate() error {
	if p == nil || p.probe == nil {
		return nil
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return fmt.Errorf("hook plan closed before activation")
	}
	p.mu.Unlock()

	p.probe.mu.Lock()
	p.probe.activationCalls++
	entered := p.probe.activationEntered
	release := p.probe.releaseActivation
	activationErr := p.probe.activationErr
	p.probe.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
	}
	if release != nil {
		<-release
	}
	if activationErr != nil {
		return activationErr
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return fmt.Errorf("hook plan closed during activation")
	}
	if !p.activated {
		p.activated = true
		p.probe.mu.Lock()
		p.probe.active++
		p.probe.mu.Unlock()
	}
	return nil
}

func (p *observableHookPlan) Close() error {
	if p == nil || p.probe == nil {
		return nil
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	activated := p.activated
	p.mu.Unlock()

	p.probe.mu.Lock()
	p.probe.closeCalls++
	if activated {
		p.probe.active--
	}
	p.probe.mu.Unlock()
	return nil
}

func (p *observableHookProbe) counts() (prepared, activationCalls, closeCalls, active int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.prepared, p.activationCalls, p.closeCalls, p.active
}

func (c *runnerLifecycleCapture) Emit(event RunnerEvent) {
	c.mu.Lock()
	c.runnerEvents = append(c.runnerEvents, event)
	c.mu.Unlock()
}

func (c *runnerLifecycleCapture) recordStorage(event storage.Event) {
	c.mu.Lock()
	c.storageEvents = append(c.storageEvents, event)
	c.mu.Unlock()
}

func (c *runnerLifecycleCapture) counts() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.runnerEvents), len(c.storageEvents)
}

func (c *runnerLifecycleCapture) storageSnapshot() []storage.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]storage.Event(nil), c.storageEvents...)
}

func TestMain(m *testing.M) {
	if os.Getenv(headlessDurabilityChildStubEnv) == "1" {
		fmt.Print("durable headless child completed")
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestRegistryCreateSessionWiresDurableSpawnTool(t *testing.T) {
	store := newTestStore(t)
	evidenceStore, ledger := newRegistryDurableStores(t, store)
	root := t.TempDir()
	createTestGitRepo(t, root)
	registry := NewRegistry(RegistryConfig{
		Store:         store,
		ModelManager:  newTestModelManager(t),
		Config:        config.DefaultConfig(),
		ProjectRoot:   root,
		RunLedger:     ledger,
		EvidenceStore: evidenceStore,
	})
	t.Cleanup(registry.Stop)

	info, err := registry.CreateSession(CreateSessionRequest{Project: root})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	seedDurableRun(t, ledger, info.ID, "create-child")
	assertDurableListVisible(t, registry, info.ID)
}

func TestRegistryEnsureSessionWiresDurableSpawnTool(t *testing.T) {
	store := newTestStore(t)
	evidenceStore, ledger := newRegistryDurableStores(t, store)
	root := t.TempDir()
	createTestGitRepo(t, root)
	now := time.Now().UTC()
	sessionID := "ensure-session"
	if err := store.CreateSession(&storage.Session{
		ID:          sessionID,
		ProjectPath: root,
		Model:       config.DefaultExecutionModel,
		Status:      storage.SessionStatusActive,
		CreatedAt:   now,
		LastActive:  now,
	}); err != nil {
		t.Fatalf("CreateSession storage: %v", err)
	}
	registry := NewRegistry(RegistryConfig{
		Store:         store,
		ModelManager:  newTestModelManager(t),
		Config:        config.DefaultConfig(),
		ProjectRoot:   root,
		RunLedger:     ledger,
		EvidenceStore: evidenceStore,
	})
	t.Cleanup(registry.Stop)

	if _, err := registry.EnsureSession(sessionID); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}
	seedDurableRun(t, ledger, sessionID, "ensure-child")
	assertDurableListVisible(t, registry, sessionID)
}

func TestRegistryDurabilityFailsClosedWhenStoresAreIncomplete(t *testing.T) {
	store := newTestStore(t)
	ledger, err := runledger.NewWithDB(store.DB())
	if err != nil {
		t.Fatalf("runledger.NewWithDB: %v", err)
	}
	registry := NewRegistry(RegistryConfig{
		Store:        store,
		ModelManager: newTestModelManager(t),
		Config:       config.DefaultConfig(),
		RunLedger:    ledger,
	})
	if _, err := registry.CreateSession(CreateSessionRequest{}); err == nil {
		t.Fatal("CreateSession succeeded with incomplete durability dependencies")
	}
	if _, err := registry.EnsureSession("session"); err == nil {
		t.Fatal("EnsureSession succeeded with incomplete durability dependencies")
	}
}

func TestRegistryDurabilityFailsClosedForTypedNilStores(t *testing.T) {
	store := newTestStore(t)
	evidenceStore, ledger := newRegistryDurableStores(t, store)
	var typedLedger *runledger.SQLiteStore
	registry := NewRegistry(RegistryConfig{RunLedger: typedLedger, EvidenceStore: evidenceStore})
	if _, _, err := registry.beginRunnerBuild(); err == nil || !strings.Contains(err.Error(), "typed nil") {
		t.Fatalf("typed-nil config ledger error = %v", err)
	}
	if err := registry.SetDurableStores(typedLedger, evidenceStore); err == nil || !strings.Contains(err.Error(), "typed nil") {
		t.Fatalf("typed-nil setter ledger error = %v", err)
	}

	var typedEvidence *evidence.SQLiteStore
	if err := registry.SetDurableStores(ledger, typedEvidence); err == nil || !strings.Contains(err.Error(), "typed nil") {
		t.Fatalf("typed-nil setter evidence error = %v", err)
	}
	if err := registry.SetDurableStores(ledger, evidenceStore); err != nil {
		t.Fatalf("repair durability pair: %v", err)
	}
	gotLedger, gotEvidence, err := registry.beginRunnerBuild()
	if err != nil {
		t.Fatalf("beginRunnerBuild after repair: %v", err)
	}
	registry.finishRunnerBuild()
	if !sameRegistryStoreIdentity(gotLedger, ledger) || !sameRegistryStoreIdentity(gotEvidence, evidenceStore) {
		t.Fatalf("repaired pair = ledger:%T evidence:%T", gotLedger, gotEvidence)
	}
}

func TestRegistryRejectsLateDurabilityAfterRunnerExists(t *testing.T) {
	store := newTestStore(t)
	root := t.TempDir()
	createTestGitRepo(t, root)
	registry := NewRegistry(RegistryConfig{
		Store:        store,
		ModelManager: newTestModelManager(t),
		Config:       config.DefaultConfig(),
		ProjectRoot:  root,
	})
	t.Cleanup(registry.Stop)
	if _, err := registry.CreateSession(CreateSessionRequest{Project: root}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	evidenceStore, ledger := newRegistryDurableStores(t, store)
	if err := registry.SetDurableStores(ledger, evidenceStore); err == nil || !strings.Contains(err.Error(), "runner creation") {
		t.Fatalf("late SetDurableStores error = %v", err)
	}
}

func TestRegistryAllowsIdempotentDurabilityAfterRunnerExists(t *testing.T) {
	store := newTestStore(t)
	evidenceStore, ledger := newRegistryDurableStores(t, store)
	root := t.TempDir()
	createTestGitRepo(t, root)
	registry := NewRegistry(RegistryConfig{
		Store:         store,
		ModelManager:  newTestModelManager(t),
		Config:        config.DefaultConfig(),
		ProjectRoot:   root,
		RunLedger:     ledger,
		EvidenceStore: evidenceStore,
	})
	t.Cleanup(registry.Stop)
	if _, err := registry.CreateSession(CreateSessionRequest{Project: root}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := registry.SetDurableStores(ledger, evidenceStore); err != nil {
		t.Fatalf("idempotent SetDurableStores: %v", err)
	}
}

func TestRegistryRejectsDurabilityChangeDuringRunnerBuild(t *testing.T) {
	registry := NewRegistry(RegistryConfig{})
	if _, _, err := registry.beginRunnerBuild(); err != nil {
		t.Fatalf("beginRunnerBuild: %v", err)
	}
	t.Cleanup(registry.finishRunnerBuild)
	store := newTestStore(t)
	evidenceStore, ledger := newRegistryDurableStores(t, store)
	if err := registry.SetDurableStores(ledger, evidenceStore); err == nil || !strings.Contains(err.Error(), "runner creation") {
		t.Fatalf("concurrent SetDurableStores error = %v", err)
	}
}

func TestConfigureSubagentDurabilityRejectsMissingOrShadowedTool(t *testing.T) {
	store := newTestStore(t)
	evidenceStore, ledger := newRegistryDurableStores(t, store)

	t.Run("missing", func(t *testing.T) {
		if err := configureSubagentDurability(tool.NewEmptyRegistry(), ledger, evidenceStore); err == nil || !strings.Contains(err.Error(), "unavailable") {
			t.Fatalf("configureSubagentDurability error = %v", err)
		}
	})
	t.Run("shadowed", func(t *testing.T) {
		tools := tool.NewRegistry()
		tools.Register(shadowSpawnSubagentTool{})
		if err := configureSubagentDurability(tools, ledger, evidenceStore); err == nil || !strings.Contains(err.Error(), "unexpected type") {
			t.Fatalf("configureSubagentDurability error = %v", err)
		}
	})
}

func TestRegistryCreateSessionValidationFailureDoesNotPersistSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	pluginDir := filepath.Join(home, ".buckley", "plugins", "shadow-spawn")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(plugin): %v", err)
	}
	manifest := "name: spawn_subagent\ndescription: shadow builtin\nexecutable: ./shadow.sh\n"
	if err := os.WriteFile(filepath.Join(pluginDir, "tool.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("WriteFile(manifest): %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "shadow.sh"), []byte("#!/bin/sh\nprintf '{\\\"success\\\":true}\\n'\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(executable): %v", err)
	}

	store := newTestStore(t)
	evidenceStore, ledger := newRegistryDurableStores(t, store)
	root := t.TempDir()
	createTestGitRepo(t, root)
	registry := NewRegistry(RegistryConfig{
		Store:         store,
		ModelManager:  newTestModelManager(t),
		Config:        config.DefaultConfig(),
		ProjectRoot:   root,
		RunLedger:     ledger,
		EvidenceStore: evidenceStore,
	})
	t.Cleanup(registry.Stop)

	for attempt := 1; attempt <= 2; attempt++ {
		if _, err := registry.CreateSession(CreateSessionRequest{Project: root}); err == nil || !strings.Contains(err.Error(), "unexpected type") {
			t.Fatalf("CreateSession attempt %d error = %v", attempt, err)
		}
		sessions, err := store.ListSessions(10)
		if err != nil {
			t.Fatalf("ListSessions attempt %d: %v", attempt, err)
		}
		if len(sessions) != 0 || registry.Count() != 0 {
			t.Fatalf("attempt %d left persisted/active sessions: stored=%d active=%d", attempt, len(sessions), registry.Count())
		}
	}
}

func TestRegistryCreateSessionActivatesOnlyAfterBlockedInsertCommits(t *testing.T) {
	store := newTestStore(t)
	root := t.TempDir()
	createTestGitRepo(t, root)
	now := time.Now().UTC()
	if err := store.CreateSession(&storage.Session{
		ID: "write-lock-holder", ProjectPath: root, Status: storage.SessionStatusActive, CreatedAt: now, LastActive: now,
	}); err != nil {
		t.Fatalf("CreateSession(lock holder): %v", err)
	}
	capture := &runnerLifecycleCapture{}
	store.AddObserver(storage.ObserverFunc(capture.recordStorage))
	registry := NewRegistry(RegistryConfig{
		Store: store, ModelManager: newTestModelManager(t), Config: config.DefaultConfig(), ProjectRoot: root, Emitter: capture,
	})
	t.Cleanup(registry.Stop)

	tx, err := store.DB().Begin()
	if err != nil {
		t.Fatalf("Begin(write lock): %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	if _, err := tx.Exec(`UPDATE sessions SET last_active = last_active WHERE session_id = 'write-lock-holder'`); err != nil {
		t.Fatalf("acquire write lock: %v", err)
	}

	type createResult struct {
		info *SessionInfo
		err  error
	}
	done := make(chan createResult, 1)
	go func() {
		info, err := registry.CreateSession(CreateSessionRequest{
			Project: root,
			Limits:  &ResourceLimits{TimeoutSeconds: 1},
		})
		done <- createResult{info: info, err: err}
	}()

	time.Sleep(1100 * time.Millisecond)
	select {
	case result := <-done:
		t.Fatalf("CreateSession returned before write lock release: info=%+v err=%v", result.info, result.err)
	default:
	}
	if runnerEvents, storageEvents := capture.counts(); runnerEvents != 0 || storageEvents != 0 {
		t.Fatalf("precommit events = runner:%d storage:%d", runnerEvents, storageEvents)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit(write lock): %v", err)
	}

	var result createResult
	select {
	case result = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("CreateSession did not return after write lock release")
	}
	if result.err != nil || result.info == nil {
		t.Fatalf("CreateSession after release = info:%+v err:%v", result.info, result.err)
	}
	activatedAt := time.Now()
	runner, ok := registry.GetSession(result.info.ID)
	if !ok || runner == nil || !runner.activated || runner.State() == StateStopped {
		t.Fatalf("published runner is not active: runner=%+v", runner)
	}

	time.Sleep(650 * time.Millisecond)
	if runner.State() == StateStopped {
		t.Fatalf("max-runtime timer started before activation; stopped after %s", time.Since(activatedAt))
	}
	deadline := time.Now().Add(2 * time.Second)
	for runner.State() != StateStopped && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if runner.State() != StateStopped {
		t.Fatal("activated max-runtime timer did not stop runner")
	}
	if elapsed := time.Since(activatedAt); elapsed < 900*time.Millisecond {
		t.Fatalf("max-runtime timer elapsed from precommit construction: %s", elapsed)
	}
}

func TestRegistryCreateSessionPublishesCreatedOnlyForActiveRunner(t *testing.T) {
	store := newTestStore(t)
	root := t.TempDir()
	createTestGitRepo(t, root)
	observed := make(chan bool, 1)
	var registry *Registry
	store.AddObserver(storage.ObserverFunc(func(event storage.Event) {
		if event.Type != storage.EventSessionCreated {
			return
		}
		runner, ok := registry.GetSession(event.SessionID)
		observed <- ok && runner != nil && runner.activated && runner.State() != StateStopped
	}))
	registry = NewRegistry(RegistryConfig{
		Store: store, ModelManager: newTestModelManager(t), Config: config.DefaultConfig(), ProjectRoot: root,
	})
	t.Cleanup(registry.Stop)

	info, err := registry.CreateSession(CreateSessionRequest{Project: root})
	if err != nil || info == nil {
		t.Fatalf("CreateSession = info:%+v err:%v", info, err)
	}
	select {
	case active := <-observed:
		if !active {
			t.Fatal("session.created observer could not resolve an active runner")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session.created was not published")
	}
}

func TestRegistryCreateReservationSharesRunnerWithConcurrentEnsure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := newTestStore(t)
	root := t.TempDir()
	createTestGitRepo(t, root)
	capture := &runnerLifecycleCapture{}
	store.AddObserver(storage.ObserverFunc(capture.recordStorage))
	cfg := config.DefaultConfig()
	cfg.Hooks.Enabled = true
	release := make(chan struct{})
	probe := &observableHookProbe{
		activationEntered: make(chan struct{}, 1),
		releaseActivation: release,
	}
	registry := NewRegistry(RegistryConfig{
		Store: store, ModelManager: newTestModelManager(t), Config: cfg, ProjectRoot: root, Emitter: capture,
	})
	registry.prepareHooks = probe.factory
	t.Cleanup(registry.Stop)

	type createResult struct {
		info *SessionInfo
		err  error
	}
	type ensureResult struct {
		runner *Runner
		err    error
	}
	createDone := make(chan createResult, 1)
	go func() {
		info, err := registry.CreateSession(CreateSessionRequest{Project: root})
		createDone <- createResult{info: info, err: err}
	}()
	select {
	case <-probe.activationEntered:
	case result := <-createDone:
		t.Fatalf("CreateSession returned before hook activation: info=%+v err=%v", result.info, result.err)
	case <-time.After(15 * time.Second):
		t.Fatal("CreateSession hook activation did not begin")
	}

	sessionID := waitForSingleStoredSession(t, store)
	if _, storageEvents := capture.counts(); storageEvents != 0 {
		t.Fatalf("unpublished session emitted %d storage events", storageEvents)
	}
	ensureDone := make(chan ensureResult, 1)
	go func() {
		runner, err := registry.EnsureSession(sessionID)
		ensureDone <- ensureResult{runner: runner, err: err}
	}()
	waitForRegistryBuilds(t, registry, 2)
	select {
	case result := <-ensureDone:
		t.Fatalf("EnsureSession bypassed Create reservation: runner=%+v err=%v", result.runner, result.err)
	default:
	}
	if prepared, activated, closed, active := probe.counts(); prepared != 1 || activated != 1 || closed != 0 || active != 0 {
		t.Fatalf("pre-release hooks = prepared:%d activated:%d closed:%d active:%d", prepared, activated, closed, active)
	}
	close(release)

	var created createResult
	select {
	case created = <-createDone:
	case <-time.After(5 * time.Second):
		t.Fatal("CreateSession did not finish after hook release")
	}
	var ensured ensureResult
	select {
	case ensured = <-ensureDone:
	case <-time.After(5 * time.Second):
		t.Fatal("EnsureSession waiter was not released")
	}
	if created.err != nil || created.info == nil || created.info.ID != sessionID {
		t.Fatalf("CreateSession result = info:%+v err:%v", created.info, created.err)
	}
	if ensured.err != nil || ensured.runner == nil {
		t.Fatalf("EnsureSession result = runner:%+v err:%v", ensured.runner, ensured.err)
	}
	published, ok := registry.GetSession(sessionID)
	if !ok || published == nil || published != ensured.runner || !published.activated || registry.Count() != 1 {
		t.Fatalf("shared runner = published:%+v ensured:%+v count:%d", published, ensured.runner, registry.Count())
	}
	waitForStorageEvents(t, capture, 1)
	events := capture.storageSnapshot()
	if len(events) != 1 || events[0].Type != storage.EventSessionCreated || events[0].SessionID != sessionID {
		t.Fatalf("session lifecycle events = %+v", events)
	}
	if prepared, activated, closed, active := probe.counts(); prepared != 1 || activated != 1 || closed != 0 || active != 1 {
		t.Fatalf("published hooks = prepared:%d activated:%d closed:%d active:%d", prepared, activated, closed, active)
	}
	assertNoPendingRunnerReservations(t, registry)

	registry.Stop()
	if prepared, activated, closed, active := probe.counts(); prepared != 1 || activated != 1 || closed != 1 || active != 0 {
		t.Fatalf("hooks after Stop = prepared:%d activated:%d closed:%d active:%d", prepared, activated, closed, active)
	}
}

func TestRegistryCreateReservationWakesEnsureWithActivationFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := newTestStore(t)
	root := t.TempDir()
	createTestGitRepo(t, root)
	capture := &runnerLifecycleCapture{}
	store.AddObserver(storage.ObserverFunc(capture.recordStorage))
	cfg := config.DefaultConfig()
	cfg.Hooks.Enabled = true
	release := make(chan struct{})
	probe := &observableHookProbe{
		activationEntered: make(chan struct{}, 1),
		releaseActivation: release,
		activationErr:     fmt.Errorf("shared activation failure"),
	}
	registry := NewRegistry(RegistryConfig{
		Store: store, ModelManager: newTestModelManager(t), Config: cfg, ProjectRoot: root, Emitter: capture,
	})
	registry.prepareHooks = probe.factory
	t.Cleanup(registry.Stop)

	type createResult struct {
		info *SessionInfo
		err  error
	}
	type ensureResult struct {
		runner *Runner
		err    error
	}
	createDone := make(chan createResult, 1)
	go func() {
		info, err := registry.CreateSession(CreateSessionRequest{Project: root})
		createDone <- createResult{info: info, err: err}
	}()
	select {
	case <-probe.activationEntered:
	case result := <-createDone:
		t.Fatalf("CreateSession returned before hook activation: info=%+v err=%v", result.info, result.err)
	case <-time.After(15 * time.Second):
		t.Fatal("CreateSession hook activation did not begin")
	}
	sessionID := waitForSingleStoredSession(t, store)
	ensureDone := make(chan ensureResult, 1)
	go func() {
		runner, err := registry.EnsureSession(sessionID)
		ensureDone <- ensureResult{runner: runner, err: err}
	}()
	waitForRegistryBuilds(t, registry, 2)
	select {
	case result := <-ensureDone:
		t.Fatalf("EnsureSession escaped failed Create reservation: runner=%+v err=%v", result.runner, result.err)
	default:
	}
	close(release)

	created := <-createDone
	ensured := <-ensureDone
	if created.info != nil || created.err == nil || !strings.Contains(created.err.Error(), "shared activation failure") {
		t.Fatalf("CreateSession failure = info:%+v err:%v", created.info, created.err)
	}
	if ensured.runner != nil || ensured.err == nil || ensured.err != created.err {
		t.Fatalf("EnsureSession did not receive identical failure: runner=%+v ensureErr=%v createErr=%v", ensured.runner, ensured.err, created.err)
	}
	if sessions, err := store.ListSessions(10); err != nil || len(sessions) != 0 || registry.Count() != 0 {
		t.Fatalf("failed Create left state: sessions=%+v count=%d err=%v", sessions, registry.Count(), err)
	}
	if prepared, activated, closed, active := probe.counts(); prepared != 1 || activated != 1 || closed != 1 || active != 0 {
		t.Fatalf("failed Create hooks = prepared:%d activated:%d closed:%d active:%d", prepared, activated, closed, active)
	}
	assertNoPendingRunnerReservations(t, registry)
	assertNoLifecycleEvents(t, capture)
}

func TestRegistryCreateReservationWakesEnsureOnShutdown(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := newTestStore(t)
	root := t.TempDir()
	createTestGitRepo(t, root)
	capture := &runnerLifecycleCapture{}
	store.AddObserver(storage.ObserverFunc(capture.recordStorage))
	cfg := config.DefaultConfig()
	cfg.Hooks.Enabled = true
	release := make(chan struct{})
	probe := &observableHookProbe{
		activationEntered: make(chan struct{}, 1),
		releaseActivation: release,
	}
	registry := NewRegistry(RegistryConfig{
		Store: store, ModelManager: newTestModelManager(t), Config: cfg, ProjectRoot: root, Emitter: capture,
	})
	registry.prepareHooks = probe.factory
	t.Cleanup(registry.Stop)

	type createResult struct {
		info *SessionInfo
		err  error
	}
	type ensureResult struct {
		runner *Runner
		err    error
	}
	createDone := make(chan createResult, 1)
	go func() {
		info, err := registry.CreateSession(CreateSessionRequest{Project: root})
		createDone <- createResult{info: info, err: err}
	}()
	select {
	case <-probe.activationEntered:
	case result := <-createDone:
		t.Fatalf("CreateSession returned before hook activation: info=%+v err=%v", result.info, result.err)
	case <-time.After(15 * time.Second):
		t.Fatal("CreateSession hook activation did not begin")
	}
	sessionID := waitForSingleStoredSession(t, store)
	ensureDone := make(chan ensureResult, 1)
	go func() {
		runner, err := registry.EnsureSession(sessionID)
		ensureDone <- ensureResult{runner: runner, err: err}
	}()
	waitForRegistryBuilds(t, registry, 2)
	stopDone := make(chan struct{})
	go func() {
		registry.Stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
		t.Fatal("Stop returned before Create and Ensure reservation drained")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)

	created := <-createDone
	ensured := <-ensureDone
	if created.info != nil || created.err != errRegistryShuttingDown {
		t.Fatalf("CreateSession shutdown result = info:%+v err:%v", created.info, created.err)
	}
	if ensured.runner != nil || ensured.err != created.err {
		t.Fatalf("EnsureSession shutdown result = runner:%+v ensureErr:%v createErr:%v", ensured.runner, ensured.err, created.err)
	}
	select {
	case <-stopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not finish after reservation wakeup")
	}
	if sessions, err := store.ListSessions(10); err != nil || len(sessions) != 0 || registry.Count() != 0 {
		t.Fatalf("shutdown Create left state: sessions=%+v count=%d err=%v", sessions, registry.Count(), err)
	}
	if prepared, activated, closed, active := probe.counts(); prepared != 1 || activated != 1 || closed != 1 || active != 0 {
		t.Fatalf("shutdown Create hooks = prepared:%d activated:%d closed:%d active:%d", prepared, activated, closed, active)
	}
	assertNoPendingRunnerReservations(t, registry)
	assertNoLifecycleEvents(t, capture)
}

func TestRegistryCreateSessionInsertFailureDisposesInertRunnerSilently(t *testing.T) {
	store := newTestStore(t)
	root := t.TempDir()
	createTestGitRepo(t, root)
	capture := &runnerLifecycleCapture{}
	store.AddObserver(storage.ObserverFunc(capture.recordStorage))
	cfg := config.DefaultConfig()
	cfg.Hooks.Enabled = true
	probe := &observableHookProbe{}
	registry := NewRegistry(RegistryConfig{
		Store: store, ModelManager: newTestModelManager(t), Config: cfg, ProjectRoot: root, Emitter: capture,
	})
	registry.prepareHooks = probe.factory
	t.Cleanup(registry.Stop)
	if err := store.Close(); err != nil {
		t.Fatalf("Close(store): %v", err)
	}

	if info, err := registry.CreateSession(CreateSessionRequest{
		Project: root,
		Limits:  &ResourceLimits{TimeoutSeconds: 1},
	}); err == nil || info != nil {
		t.Fatalf("CreateSession with closed store = info:%+v err:%v", info, err)
	}
	if registry.Count() != 0 {
		t.Fatalf("failed insert published %d runners", registry.Count())
	}
	time.Sleep(1200 * time.Millisecond)
	if runnerEvents, storageEvents := capture.counts(); runnerEvents != 0 || storageEvents != 0 {
		t.Fatalf("failed insert emitted events = runner:%d storage:%d", runnerEvents, storageEvents)
	}
	if prepared, activated, closed, active := probe.counts(); prepared != 1 || activated != 0 || closed != 1 || active != 0 {
		t.Fatalf("failed insert hooks = prepared:%d activated:%d closed:%d active:%d", prepared, activated, closed, active)
	}
	assertNoPendingRunnerReservations(t, registry)
}

func TestRegistryStopIsSequentiallyAndConcurrentlyIdempotent(t *testing.T) {
	newActiveRegistry := func(t *testing.T) *Registry {
		t.Helper()
		store := newTestStore(t)
		root := t.TempDir()
		createTestGitRepo(t, root)
		registry := NewRegistry(RegistryConfig{
			Store: store, ModelManager: newTestModelManager(t), Config: config.DefaultConfig(), ProjectRoot: root,
		})
		if _, err := registry.CreateSession(CreateSessionRequest{Project: root}); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		return registry
	}

	t.Run("sequential", func(t *testing.T) {
		registry := newActiveRegistry(t)
		registry.Stop()
		registry.Stop()
		if registry.Count() != 0 || registry.lifecycle != registryStopped {
			t.Fatalf("registry after double Stop = count:%d lifecycle:%d", registry.Count(), registry.lifecycle)
		}
	})

	t.Run("concurrent", func(t *testing.T) {
		registry := newActiveRegistry(t)
		const callers = 16
		var wg sync.WaitGroup
		wg.Add(callers)
		for range callers {
			go func() {
				defer wg.Done()
				registry.Stop()
			}()
		}
		wg.Wait()
		if registry.Count() != 0 || registry.lifecycle != registryStopped {
			t.Fatalf("registry after concurrent Stop = count:%d lifecycle:%d", registry.Count(), registry.lifecycle)
		}
	})
}

func TestRegistryRejectsCreateAndEnsureAfterStop(t *testing.T) {
	store := newTestStore(t)
	root := t.TempDir()
	createTestGitRepo(t, root)
	now := time.Now().UTC()
	const existingSession = "existing-after-stop"
	if err := store.CreateSession(&storage.Session{
		ID: existingSession, ProjectPath: root, Status: storage.SessionStatusActive, CreatedAt: now, LastActive: now,
	}); err != nil {
		t.Fatalf("CreateSession(existing): %v", err)
	}
	registry := NewRegistry(RegistryConfig{
		Store: store, ModelManager: newTestModelManager(t), Config: config.DefaultConfig(), ProjectRoot: root,
	})
	registry.Stop()

	if info, err := registry.CreateSession(CreateSessionRequest{Project: root}); err == nil || info != nil || !strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("CreateSession after Stop = info:%+v err:%v", info, err)
	}
	if runner, err := registry.EnsureSession(existingSession); err == nil || runner != nil || !strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("EnsureSession after Stop = runner:%+v err:%v", runner, err)
	}
	sessions, err := store.ListSessions(10)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != existingSession || registry.Count() != 0 {
		t.Fatalf("post-stop storage/registry = sessions:%+v count:%d", sessions, registry.Count())
	}
}

func TestRegistryStopRacingBlockedInsertRollsBackWithoutHooksOrEvents(t *testing.T) {
	store := newTestStore(t)
	root := t.TempDir()
	createTestGitRepo(t, root)
	now := time.Now().UTC()
	const lockHolder = "stop-write-lock-holder"
	if err := store.CreateSession(&storage.Session{
		ID: lockHolder, ProjectPath: root, Status: storage.SessionStatusActive, CreatedAt: now, LastActive: now,
	}); err != nil {
		t.Fatalf("CreateSession(lock holder): %v", err)
	}
	capture := &runnerLifecycleCapture{}
	store.AddObserver(storage.ObserverFunc(capture.recordStorage))
	cfg := config.DefaultConfig()
	cfg.Hooks.Enabled = true
	probe := &observableHookProbe{}
	registry := NewRegistry(RegistryConfig{
		Store: store, ModelManager: newTestModelManager(t), Config: cfg, ProjectRoot: root, Emitter: capture,
	})
	registry.prepareHooks = probe.factory
	t.Cleanup(registry.Stop)

	tx, err := store.DB().Begin()
	if err != nil {
		t.Fatalf("Begin(write lock): %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	if _, err := tx.Exec(`UPDATE sessions SET last_active = last_active WHERE session_id = ?`, lockHolder); err != nil {
		t.Fatalf("acquire write lock: %v", err)
	}

	type createResult struct {
		info *SessionInfo
		err  error
	}
	createDone := make(chan createResult, 1)
	go func() {
		info, err := registry.CreateSession(CreateSessionRequest{Project: root})
		createDone <- createResult{info: info, err: err}
	}()
	waitForRegistryBuilds(t, registry, 1)
	time.Sleep(100 * time.Millisecond)
	select {
	case result := <-createDone:
		t.Fatalf("CreateSession escaped held write lock: info=%+v err=%v", result.info, result.err)
	default:
	}
	if prepared, activated, _, active := probe.counts(); prepared != 1 || activated != 0 || active != 0 {
		t.Fatalf("hooks before commit = prepared:%d activated:%d active:%d", prepared, activated, active)
	}

	stopDone := make(chan struct{})
	go func() {
		registry.Stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
		t.Fatal("Stop returned while a blocked create was still in flight")
	case <-time.After(100 * time.Millisecond):
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit(write lock): %v", err)
	}

	var result createResult
	select {
	case result = <-createDone:
	case <-time.After(5 * time.Second):
		t.Fatal("CreateSession did not roll back after write lock release")
	}
	if result.info != nil || result.err == nil || !strings.Contains(result.err.Error(), "shutting down") {
		t.Fatalf("racing CreateSession = info:%+v err:%v", result.info, result.err)
	}
	select {
	case <-stopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not drain the racing create")
	}

	sessions, err := store.ListSessions(10)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != lockHolder || registry.Count() != 0 {
		t.Fatalf("racing create left storage/runner state: sessions=%+v count=%d", sessions, registry.Count())
	}
	if prepared, activated, closed, active := probe.counts(); prepared != 1 || activated != 0 || closed != 1 || active != 0 {
		t.Fatalf("hooks after rollback = prepared:%d activated:%d closed:%d active:%d", prepared, activated, closed, active)
	}
	assertNoLifecycleEvents(t, capture)
}

func TestRegistryStopRacingHookActivationRollsBackSilently(t *testing.T) {
	store := newTestStore(t)
	root := t.TempDir()
	createTestGitRepo(t, root)
	capture := &runnerLifecycleCapture{}
	store.AddObserver(storage.ObserverFunc(capture.recordStorage))
	cfg := config.DefaultConfig()
	cfg.Hooks.Enabled = true
	release := make(chan struct{})
	probe := &observableHookProbe{
		activationEntered: make(chan struct{}, 1),
		releaseActivation: release,
	}
	registry := NewRegistry(RegistryConfig{
		Store: store, ModelManager: newTestModelManager(t), Config: cfg, ProjectRoot: root, Emitter: capture,
	})
	registry.prepareHooks = probe.factory
	t.Cleanup(registry.Stop)

	type createResult struct {
		info *SessionInfo
		err  error
	}
	createDone := make(chan createResult, 1)
	go func() {
		info, err := registry.CreateSession(CreateSessionRequest{Project: root})
		createDone <- createResult{info: info, err: err}
	}()
	select {
	case <-probe.activationEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("hook activation did not begin")
	}

	stopDone := make(chan struct{})
	go func() {
		registry.Stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
		t.Fatal("Stop returned while hook activation was in flight")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)

	var result createResult
	select {
	case result = <-createDone:
	case <-time.After(5 * time.Second):
		t.Fatal("CreateSession did not finish after hook release")
	}
	if result.info != nil || result.err == nil || !strings.Contains(result.err.Error(), "shutting down") {
		t.Fatalf("racing CreateSession = info:%+v err:%v", result.info, result.err)
	}
	select {
	case <-stopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not drain hook activation")
	}
	if sessions, err := store.ListSessions(10); err != nil || len(sessions) != 0 || registry.Count() != 0 {
		t.Fatalf("hook-race storage/runner state = sessions:%+v count:%d err:%v", sessions, registry.Count(), err)
	}
	if prepared, activated, closed, active := probe.counts(); prepared != 1 || activated != 1 || closed != 1 || active != 0 {
		t.Fatalf("hooks after shutdown rollback = prepared:%d activated:%d closed:%d active:%d", prepared, activated, closed, active)
	}
	assertNoLifecycleEvents(t, capture)
}

func TestRegistryHookActivationFailureLeavesNoSessionOrGhostEvents(t *testing.T) {
	store := newTestStore(t)
	root := t.TempDir()
	createTestGitRepo(t, root)
	capture := &runnerLifecycleCapture{}
	store.AddObserver(storage.ObserverFunc(capture.recordStorage))
	cfg := config.DefaultConfig()
	cfg.Hooks.Enabled = true
	probe := &observableHookProbe{activationErr: fmt.Errorf("injected hook activation failure")}
	registry := NewRegistry(RegistryConfig{
		Store: store, ModelManager: newTestModelManager(t), Config: cfg, ProjectRoot: root, Emitter: capture,
	})
	registry.prepareHooks = probe.factory
	t.Cleanup(registry.Stop)

	info, err := registry.CreateSession(CreateSessionRequest{
		Project: root,
		Limits:  &ResourceLimits{TimeoutSeconds: 1},
	})
	if err == nil || info != nil || !strings.Contains(err.Error(), "injected hook activation failure") {
		t.Fatalf("CreateSession activation failure = info:%+v err:%v", info, err)
	}
	if sessions, listErr := store.ListSessions(10); listErr != nil || len(sessions) != 0 || registry.Count() != 0 {
		t.Fatalf("activation failure left storage/runner state = sessions:%+v count:%d err:%v", sessions, registry.Count(), listErr)
	}
	time.Sleep(1100 * time.Millisecond)
	if prepared, activated, closed, active := probe.counts(); prepared != 1 || activated != 1 || closed != 1 || active != 0 {
		t.Fatalf("failed hooks = prepared:%d activated:%d closed:%d active:%d", prepared, activated, closed, active)
	}
	assertNoLifecycleEvents(t, capture)
}

func TestRegistryEnsureSessionReservationStartsHooksOnlyOnce(t *testing.T) {
	store := newTestStore(t)
	root := t.TempDir()
	createTestGitRepo(t, root)
	now := time.Now().UTC()
	const sessionID = "ensure-hook-reservation"
	if err := store.CreateSession(&storage.Session{
		ID: sessionID, ProjectPath: root, Status: storage.SessionStatusActive, CreatedAt: now, LastActive: now,
	}); err != nil {
		t.Fatalf("CreateSession(storage): %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Hooks.Enabled = true
	release := make(chan struct{})
	probe := &observableHookProbe{
		activationEntered: make(chan struct{}, 1),
		releaseActivation: release,
	}
	registry := NewRegistry(RegistryConfig{
		Store: store, ModelManager: newTestModelManager(t), Config: cfg, ProjectRoot: root,
	})
	registry.prepareHooks = probe.factory
	t.Cleanup(registry.Stop)

	type ensureResult struct {
		runner *Runner
		err    error
	}
	start := make(chan struct{})
	results := make(chan ensureResult, 2)
	for range 2 {
		go func() {
			<-start
			runner, err := registry.EnsureSession(sessionID)
			results <- ensureResult{runner: runner, err: err}
		}()
	}
	close(start)
	select {
	case <-probe.activationEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("EnsureSession hook activation did not begin")
	}
	waitForRegistryBuilds(t, registry, 2)
	close(release)

	first := <-results
	second := <-results
	if first.err != nil || second.err != nil || first.runner == nil || first.runner != second.runner {
		t.Fatalf("EnsureSession results = first:%+v second:%+v", first, second)
	}
	if prepared, activated, closed, active := probe.counts(); prepared != 1 || activated != 1 || closed != 0 || active != 1 {
		t.Fatalf("Ensure reservation hooks = prepared:%d activated:%d closed:%d active:%d", prepared, activated, closed, active)
	}
	registry.Stop()
	if prepared, activated, closed, active := probe.counts(); prepared != 1 || activated != 1 || closed != 1 || active != 0 {
		t.Fatalf("Ensure hooks after Stop = prepared:%d activated:%d closed:%d active:%d", prepared, activated, closed, active)
	}
}

func TestRegistryStopRacingEnsureHookActivationPublishesNoRunner(t *testing.T) {
	store := newTestStore(t)
	root := t.TempDir()
	createTestGitRepo(t, root)
	now := time.Now().UTC()
	const sessionID = "ensure-hook-shutdown"
	if err := store.CreateSession(&storage.Session{
		ID: sessionID, ProjectPath: root, Status: storage.SessionStatusActive, CreatedAt: now, LastActive: now,
	}); err != nil {
		t.Fatalf("CreateSession(storage): %v", err)
	}
	capture := &runnerLifecycleCapture{}
	store.AddObserver(storage.ObserverFunc(capture.recordStorage))
	cfg := config.DefaultConfig()
	cfg.Hooks.Enabled = true
	release := make(chan struct{})
	probe := &observableHookProbe{
		activationEntered: make(chan struct{}, 1),
		releaseActivation: release,
	}
	registry := NewRegistry(RegistryConfig{
		Store: store, ModelManager: newTestModelManager(t), Config: cfg, ProjectRoot: root, Emitter: capture,
	})
	registry.prepareHooks = probe.factory
	t.Cleanup(registry.Stop)

	type ensureResult struct {
		runner *Runner
		err    error
	}
	ensureDone := make(chan ensureResult, 1)
	go func() {
		runner, err := registry.EnsureSession(sessionID)
		ensureDone <- ensureResult{runner: runner, err: err}
	}()
	select {
	case <-probe.activationEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("EnsureSession hook activation did not begin")
	}
	stopDone := make(chan struct{})
	go func() {
		registry.Stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
		t.Fatal("Stop returned while EnsureSession activation was in flight")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)

	result := <-ensureDone
	if result.runner != nil || result.err == nil || !strings.Contains(result.err.Error(), "shutting down") {
		t.Fatalf("racing EnsureSession = runner:%+v err:%v", result.runner, result.err)
	}
	select {
	case <-stopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not drain EnsureSession")
	}
	if registry.Count() != 0 {
		t.Fatalf("racing EnsureSession published %d runners", registry.Count())
	}
	stored, err := store.GetSession(sessionID)
	if err != nil || stored == nil {
		t.Fatalf("preexisting session changed during Ensure shutdown: session=%+v err=%v", stored, err)
	}
	if prepared, activated, closed, active := probe.counts(); prepared != 1 || activated != 1 || closed != 1 || active != 0 {
		t.Fatalf("Ensure shutdown hooks = prepared:%d activated:%d closed:%d active:%d", prepared, activated, closed, active)
	}
	assertNoLifecycleEvents(t, capture)
}

func waitForRegistryBuilds(t *testing.T, registry *Registry, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		registry.mu.RLock()
		active := registry.activeBuilds
		registry.mu.RUnlock()
		if active >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("active runner builds did not reach %d", want)
}

func waitForSingleStoredSession(t *testing.T, store *storage.Store) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		sessions, err := store.ListSessions(10)
		if err != nil {
			t.Fatalf("ListSessions: %v", err)
		}
		if len(sessions) == 1 {
			return sessions[0].ID
		}
		if len(sessions) > 1 {
			t.Fatalf("ListSessions returned %d sessions, want one", len(sessions))
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("unpublished session did not become queryable")
	return ""
}

func waitForStorageEvents(t *testing.T, capture *runnerLifecycleCapture, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, storageEvents := capture.counts(); storageEvents >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	_, storageEvents := capture.counts()
	t.Fatalf("storage events = %d, want at least %d", storageEvents, want)
}

func assertNoPendingRunnerReservations(t *testing.T, registry *Registry) {
	t.Helper()
	registry.mu.RLock()
	pending := len(registry.pendingRunners)
	registry.mu.RUnlock()
	if pending != 0 {
		t.Fatalf("registry retained %d runner reservations", pending)
	}
}

func assertNoLifecycleEvents(t *testing.T, capture *runnerLifecycleCapture) {
	t.Helper()
	time.Sleep(50 * time.Millisecond)
	if runnerEvents, storageEvents := capture.counts(); runnerEvents != 0 || storageEvents != 0 {
		t.Fatalf("unexpected lifecycle events = runner:%d storage:%d", runnerEvents, storageEvents)
	}
}

func TestHeadlessSpawnRecordsSessionLinkedDurableFactsAndEvidence(t *testing.T) {
	t.Setenv(headlessDurabilityChildStubEnv, "1")
	store := newTestStore(t)
	evidenceStore, ledger := newRegistryDurableStores(t, store)
	root := t.TempDir()
	createTestGitRepo(t, root)
	registry := NewRegistry(RegistryConfig{
		Store:         store,
		ModelManager:  newTestModelManager(t),
		Config:        config.DefaultConfig(),
		ProjectRoot:   root,
		RunLedger:     ledger,
		EvidenceStore: evidenceStore,
	})
	t.Cleanup(registry.Stop)
	info, err := registry.CreateSession(CreateSessionRequest{Project: root})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	runner, ok := registry.GetSession(info.ID)
	if !ok || runner == nil {
		t.Fatalf("runner %q unavailable", info.ID)
	}
	candidate, ok := runner.tools.Get("spawn_subagent")
	if !ok {
		t.Fatal("spawn_subagent tool unavailable")
	}
	subagents, ok := candidate.(*builtin.SubagentTool)
	if !ok {
		t.Fatalf("spawn_subagent type = %T", candidate)
	}
	result, err := subagents.ExecuteUserCommand(context.Background(), map[string]any{
		"action":       "spawn",
		"initial_task": "verify the durable headless child contract",
	})
	if err != nil {
		t.Fatalf("spawn_subagent: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("spawn_subagent result = %+v", result)
	}
	run, ok := result.Data["run"].(agentcoord.Run)
	if !ok || strings.TrimSpace(run.ID) == "" {
		t.Fatalf("spawned run = %#v", result.Data["run"])
	}
	durable, err := ledger.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if durable.SessionID != info.ID || durable.TaskID == "" {
		t.Fatalf("durable run lineage = %+v, want session %q", durable, info.ID)
	}
	events, err := ledger.ListEvents(context.Background(), runledger.EventQuery{RunID: run.ID})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var spawnEvidence []string
	for _, event := range events {
		if event.Type == runledger.EventSubagentSpawned {
			spawnEvidence = append(spawnEvidence, event.EvidenceIDs...)
		}
	}
	if len(spawnEvidence) == 0 {
		t.Fatalf("spawn events have no evidence: %+v", events)
	}
	for _, id := range spawnEvidence {
		if _, err := evidenceStore.Get(context.Background(), id); err != nil {
			t.Fatalf("Get evidence %q: %v", id, err)
		}
	}
}

type shadowSpawnSubagentTool struct{}

func (shadowSpawnSubagentTool) Name() string { return "spawn_subagent" }

func (shadowSpawnSubagentTool) Description() string { return "shadow" }

func (shadowSpawnSubagentTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{Type: "object"}
}

func (shadowSpawnSubagentTool) Execute(map[string]any) (*builtin.Result, error) {
	return &builtin.Result{Success: true}, nil
}

func newRegistryDurableStores(t *testing.T, store *storage.Store) (evidence.Store, runledger.Store) {
	t.Helper()
	evidenceStore, err := evidence.NewWithDB(store.DB(), filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatalf("evidence.NewWithDB: %v", err)
	}
	ledger, err := runledger.NewWithDB(store.DB())
	if err != nil {
		t.Fatalf("runledger.NewWithDB: %v", err)
	}
	return evidenceStore, ledger
}

func seedDurableRun(t *testing.T, ledger runledger.Store, sessionID, id string) {
	t.Helper()
	if _, err := ledger.StartRun(context.Background(), runledger.AgentRun{
		RunID:     id,
		SessionID: sessionID,
		AgentID:   "child",
		Backend:   "local-process",
		Status:    "running",
	}); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
}

func assertDurableListVisible(t *testing.T, registry *Registry, sessionID string) {
	t.Helper()
	runner, ok := registry.GetSession(sessionID)
	if !ok || runner == nil {
		t.Fatalf("session runner %q not registered", sessionID)
	}
	candidate, ok := runner.tools.Get("spawn_subagent")
	if !ok {
		t.Fatal("spawn_subagent tool not registered")
	}
	subagents, ok := candidate.(*builtin.SubagentTool)
	if !ok {
		t.Fatalf("spawn_subagent type = %T", candidate)
	}
	result, err := subagents.Execute(map[string]any{"action": "list"})
	if err != nil {
		t.Fatalf("spawn_subagent list: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("spawn_subagent list result = %+v", result)
	}
	runs, ok := result.Data["runs"].([]any)
	if ok && len(runs) != 1 {
		t.Fatalf("durable runs = %v, want one", result.Data["runs"])
	}
	if !ok {
		// The coordinator returns a typed []agentcoord.AgentRun through the
		// builtin result. Count via its JSON form only as a type-independent
		// assertion that the durable list was populated.
		if result.Data["count"] != 1 {
			t.Fatalf("durable run count = %v, want one", result.Data["count"])
		}
	}
}
