package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/headless"
	"m31labs.dev/buckley/pkg/ipc/command"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/orchestrator"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/storage"
)

type fakeHeadlessRegistry struct {
	mu sync.Mutex

	createReq      headless.CreateSessionRequest
	createdSession *headless.SessionInfo

	sessions map[string]*headless.SessionInfo

	lastCommand command.SessionCommand
	adopted     *storage.Session
	dispatchIn  chan struct{}
	dispatchOut <-chan struct{}
	dispatchErr error
}

type durableFakeHeadlessRegistry struct {
	*fakeHeadlessRegistry

	server  *Server
	entered chan struct{}
	release chan struct{}
	err     error

	mu       sync.Mutex
	calls    int
	ledger   runledger.Store
	evidence evidence.Store
}

func (f *durableFakeHeadlessRegistry) SetDurableStores(ledger runledger.Store, store evidence.Store) error {
	if f.server != nil {
		_ = f.server.getHeadlessRegistry()
	}
	if f.entered != nil {
		select {
		case f.entered <- struct{}{}:
		default:
		}
	}
	if f.release != nil {
		<-f.release
	}
	f.mu.Lock()
	f.calls++
	err := f.err
	f.err = nil
	if err == nil {
		f.ledger = ledger
		f.evidence = store
	}
	f.mu.Unlock()
	return err
}

func (f *durableFakeHeadlessRegistry) durableSnapshot() (int, runledger.Store, evidence.Store) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.ledger, f.evidence
}

type captureEventForwarder struct {
	events []Event
}

func (f *captureEventForwarder) BroadcastEvent(event Event) {
	f.events = append(f.events, event)
}

func newFakeHeadlessRegistry() *fakeHeadlessRegistry {
	return &fakeHeadlessRegistry{
		sessions: make(map[string]*headless.SessionInfo),
	}
}

func (f *fakeHeadlessRegistry) CreateSession(req headless.CreateSessionRequest) (*headless.SessionInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createReq = req
	if f.createdSession == nil {
		now := time.Now().UTC()
		f.createdSession = &headless.SessionInfo{
			ID:         "headless-1",
			Project:    req.Project,
			Branch:     req.Branch,
			Model:      req.Model,
			State:      headless.StateIdle,
			CreatedAt:  now,
			LastActive: now,
		}
	}
	copy := *f.createdSession
	f.sessions[copy.ID] = &copy
	return f.createdSession, nil
}

func (f *fakeHeadlessRegistry) GetSession(_ string) (*headless.Runner, bool) {
	return nil, false
}

func (f *fakeHeadlessRegistry) GetSessionInfo(sessionID string) (*headless.SessionInfo, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	info, ok := f.sessions[sessionID]
	if !ok || info == nil {
		return nil, false
	}
	copy := *info
	return &copy, true
}

func (f *fakeHeadlessRegistry) ListSessions() []headless.SessionInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]headless.SessionInfo, 0, len(f.sessions))
	for _, info := range f.sessions {
		if info == nil {
			continue
		}
		out = append(out, *info)
	}
	return out
}

func (f *fakeHeadlessRegistry) RemoveSession(sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.sessions[sessionID]; !ok {
		return fmt.Errorf("session not found")
	}
	delete(f.sessions, sessionID)
	return nil
}

func (f *fakeHeadlessRegistry) DispatchCommand(cmd command.SessionCommand) error {
	f.mu.Lock()
	f.lastCommand = cmd
	entered := f.dispatchIn
	release := f.dispatchOut
	err := f.dispatchErr
	f.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if release != nil {
		<-release
	}
	return err
}

func (f *fakeHeadlessRegistry) AdoptSession(sessionID string) (*storage.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.adopted != nil {
		return f.adopted, nil
	}
	return &storage.Session{ID: sessionID}, nil
}

func (f *fakeHeadlessRegistry) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sessions)
}

func newHeadlessTestServer(t *testing.T) (*Server, *storage.Store, string) {
	t.Helper()

	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "buckley.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	planStore := orchestrator.NewFilePlanStore(filepath.Join(tmpDir, "plans"))
	server := NewServer(
		Config{ProjectRoot: tmpDir, AllowedOrigins: []string{"*"}},
		store,
		nil,
		command.NewGateway(),
		planStore,
		&config.Config{},
		nil,
		nil,
	)
	return server, store, tmpDir
}

func withScope(req *http.Request, scope string) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), principalContextKey, &requestPrincipal{
		Name:  "test",
		Scope: scope,
	}))
}

func TestHeadlessEmitterBroadcastsToHubForwarder(t *testing.T) {
	server, _, _ := newHeadlessTestServer(t)

	fwd := &captureEventForwarder{}
	server.hub.AddForwarder(fwd)

	emitter := server.NewHeadlessEmitter()
	emitter.Emit(headless.RunnerEvent{
		Type:      "runner.test",
		SessionID: "s1",
		Timestamp: time.Now().UTC(),
		Data:      map[string]any{"ok": true},
	})

	if len(fwd.events) != 1 {
		t.Fatalf("expected 1 forwarded event, got %d", len(fwd.events))
	}
	if fwd.events[0].Type != "runner.test" {
		t.Fatalf("event type=%q want runner.test", fwd.events[0].Type)
	}
	if fwd.events[0].SessionID != "s1" {
		t.Fatalf("event session=%q want s1", fwd.events[0].SessionID)
	}
}

func TestHeadlessEmitterNilHubDoesNothing(t *testing.T) {
	var e headlessEmitter
	e.Emit(headless.RunnerEvent{
		Type:      "noop",
		SessionID: "s1",
		Timestamp: time.Now().UTC(),
	})
}

func TestSetHeadlessRegistryConfiguresDurabilityBeforePublication(t *testing.T) {
	server, store, _ := newHeadlessTestServer(t)
	ledger, evidenceStore := newIPCDurableStores(t, store, "first")
	if err := server.SetDurableStores(ledger, evidenceStore); err != nil {
		t.Fatalf("SetDurableStores: %v", err)
	}

	registry := &durableFakeHeadlessRegistry{
		fakeHeadlessRegistry: newFakeHeadlessRegistry(),
		server:               server,
		entered:              make(chan struct{}, 1),
		release:              make(chan struct{}),
	}
	done := make(chan error, 1)
	go func() { done <- server.SetHeadlessRegistry(registry) }()
	<-registry.entered
	if got := server.getHeadlessRegistry(); got != nil {
		t.Fatalf("registry published before durability completed: %T", got)
	}
	close(registry.release)
	if err := <-done; err != nil {
		t.Fatalf("SetHeadlessRegistry: %v", err)
	}
	if got := server.getHeadlessRegistry(); got != registry {
		t.Fatalf("published registry = %T, want configured candidate", got)
	}
	calls, gotLedger, gotEvidence := registry.durableSnapshot()
	if calls != 1 || !sameStoreIdentity(gotLedger, ledger) || !sameStoreIdentity(gotEvidence, evidenceStore) {
		t.Fatalf("durability snapshot = calls:%d ledger:%T evidence:%T", calls, gotLedger, gotEvidence)
	}
}

func TestSetHeadlessRegistryRetriesWhenDurabilityChangesDuringConfiguration(t *testing.T) {
	server, store, _ := newHeadlessTestServer(t)
	ledgerOne, evidenceOne := newIPCDurableStores(t, store, "one")
	ledgerTwo, evidenceTwo := newIPCDurableStores(t, store, "two")
	if err := server.SetDurableStores(ledgerOne, evidenceOne); err != nil {
		t.Fatalf("SetDurableStores(first): %v", err)
	}

	registry := &durableFakeHeadlessRegistry{
		fakeHeadlessRegistry: newFakeHeadlessRegistry(),
		entered:              make(chan struct{}, 2),
		release:              make(chan struct{}),
	}
	done := make(chan error, 1)
	go func() { done <- server.SetHeadlessRegistry(registry) }()
	<-registry.entered
	if err := server.SetDurableStores(ledgerTwo, evidenceTwo); err != nil {
		t.Fatalf("SetDurableStores(second): %v", err)
	}
	close(registry.release)
	if err := <-done; err != nil {
		t.Fatalf("SetHeadlessRegistry: %v", err)
	}
	calls, gotLedger, gotEvidence := registry.durableSnapshot()
	if calls != 2 || !sameStoreIdentity(gotLedger, ledgerTwo) || !sameStoreIdentity(gotEvidence, evidenceTwo) {
		t.Fatalf("final durability snapshot = calls:%d ledger:%T evidence:%T", calls, gotLedger, gotEvidence)
	}
}

func TestSetHeadlessRegistryPropagationErrorDoesNotPublish(t *testing.T) {
	server, store, _ := newHeadlessTestServer(t)
	ledger, evidenceStore := newIPCDurableStores(t, store, "failure")
	if err := server.SetDurableStores(ledger, evidenceStore); err != nil {
		t.Fatalf("SetDurableStores: %v", err)
	}
	registry := &durableFakeHeadlessRegistry{
		fakeHeadlessRegistry: newFakeHeadlessRegistry(),
		err:                  fmt.Errorf("injected propagation failure"),
	}
	if err := server.SetHeadlessRegistry(registry); err == nil || !strings.Contains(err.Error(), "injected propagation failure") {
		t.Fatalf("SetHeadlessRegistry error = %v", err)
	}
	if got := server.getHeadlessRegistry(); got != nil {
		t.Fatalf("failed registry was published: %T", got)
	}
}

func TestSetHeadlessRegistryClearsPreconfiguredCustomDurability(t *testing.T) {
	server, store, _ := newHeadlessTestServer(t)
	ledger, evidenceStore := newIPCDurableStores(t, store, "preconfigured")
	registry := &durableFakeHeadlessRegistry{
		fakeHeadlessRegistry: newFakeHeadlessRegistry(),
		ledger:               ledger,
		evidence:             evidenceStore,
	}
	if err := server.SetHeadlessRegistry(registry); err != nil {
		t.Fatalf("SetHeadlessRegistry: %v", err)
	}
	calls, gotLedger, gotEvidence := registry.durableSnapshot()
	if calls != 1 || gotLedger != nil || gotEvidence != nil {
		t.Fatalf("canonical nil pair was not reconciled: calls:%d ledger:%T evidence:%T", calls, gotLedger, gotEvidence)
	}
	if got := server.getHeadlessRegistry(); got != registry {
		t.Fatalf("configured registry not published: %T", got)
	}
}

func TestDurabilitySettersRejectTypedNilValues(t *testing.T) {
	server, store, _ := newHeadlessTestServer(t)
	ledger, evidenceStore := newIPCDurableStores(t, store, "typed-nil")

	var typedRegistry *headless.Registry
	if err := server.SetHeadlessRegistry(typedRegistry); err == nil || !strings.Contains(err.Error(), "typed nil") {
		t.Fatalf("typed-nil registry error = %v", err)
	}
	if got := server.getHeadlessRegistry(); got != nil {
		t.Fatalf("typed-nil registry was published: %T", got)
	}

	var typedLedger *runledger.SQLiteStore
	if err := server.SetDurableStores(typedLedger, evidenceStore); err == nil || !strings.Contains(err.Error(), "typed nil") {
		t.Fatalf("typed-nil ledger error = %v", err)
	}
	var typedEvidence *evidence.SQLiteStore
	if err := server.SetDurableStores(ledger, typedEvidence); err == nil || !strings.Contains(err.Error(), "typed nil") {
		t.Fatalf("typed-nil evidence error = %v", err)
	}
	if got := server.getDurableLedger(); got != nil {
		t.Fatalf("typed-nil stores changed canonical ledger: %T", got)
	}

	server.headlessMu.Lock()
	server.durableLedger = typedLedger
	server.durableEvidence = evidenceStore
	server.headlessVersion++
	server.headlessMu.Unlock()
	capable := &durableFakeHeadlessRegistry{fakeHeadlessRegistry: newFakeHeadlessRegistry()}
	if err := server.SetHeadlessRegistry(capable); err == nil || !strings.Contains(err.Error(), "invalid canonical") {
		t.Fatalf("typed-nil canonical publication error = %v", err)
	}
	if got := server.getHeadlessRegistry(); got != nil {
		t.Fatalf("registry published beside typed-nil canonical stores: %T", got)
	}
}

func TestSetDurableStoresReconcilesCapableCustomRegistry(t *testing.T) {
	server, store, _ := newHeadlessTestServer(t)
	ledgerOne, evidenceOne := newIPCDurableStores(t, store, "custom-one")
	ledgerTwo, evidenceTwo := newIPCDurableStores(t, store, "custom-two")
	registry := &durableFakeHeadlessRegistry{fakeHeadlessRegistry: newFakeHeadlessRegistry()}
	if err := server.SetHeadlessRegistry(registry); err != nil {
		t.Fatalf("SetHeadlessRegistry: %v", err)
	}
	if err := server.SetDurableStores(ledgerOne, evidenceOne); err != nil {
		t.Fatalf("SetDurableStores(first): %v", err)
	}
	calls, gotLedger, gotEvidence := registry.durableSnapshot()
	if calls != 2 || !sameStoreIdentity(gotLedger, ledgerOne) || !sameStoreIdentity(gotEvidence, evidenceOne) {
		t.Fatalf("custom propagation = calls:%d ledger:%T evidence:%T", calls, gotLedger, gotEvidence)
	}
	if got := server.getHeadlessRegistry(); got != registry {
		t.Fatalf("custom registry not republished: %T", got)
	}

	registry.mu.Lock()
	registry.err = fmt.Errorf("injected late failure")
	registry.mu.Unlock()
	if err := server.SetDurableStores(ledgerTwo, evidenceTwo); err == nil || !strings.Contains(err.Error(), "injected late failure") {
		t.Fatalf("SetDurableStores(second) error = %v", err)
	}
	_, gotLedger, gotEvidence = registry.durableSnapshot()
	if !sameStoreIdentity(gotLedger, ledgerOne) || !sameStoreIdentity(gotEvidence, evidenceOne) {
		t.Fatalf("custom registry rollback = ledger:%T evidence:%T", gotLedger, gotEvidence)
	}
	if got := server.getDurableLedger(); !sameStoreIdentity(got, ledgerOne) {
		t.Fatalf("canonical ledger changed after propagation error: %T", got)
	}
	if got := server.getHeadlessRegistry(); got != registry {
		t.Fatalf("rolled-back registry not republished: %T", got)
	}
}

func TestSetDurableStoresReconcilesOutOfBandRegistryDrift(t *testing.T) {
	server, store, _ := newHeadlessTestServer(t)
	canonicalLedger, canonicalEvidence := newIPCDurableStores(t, store, "canonical-drift")
	driftLedger, driftEvidence := newIPCDurableStores(t, store, "out-of-band-drift")
	registry := &durableFakeHeadlessRegistry{fakeHeadlessRegistry: newFakeHeadlessRegistry()}
	if err := server.SetDurableStores(canonicalLedger, canonicalEvidence); err != nil {
		t.Fatalf("SetDurableStores(canonical): %v", err)
	}
	if err := server.SetHeadlessRegistry(registry); err != nil {
		t.Fatalf("SetHeadlessRegistry: %v", err)
	}
	if err := registry.SetDurableStores(driftLedger, driftEvidence); err != nil {
		t.Fatalf("registry.SetDurableStores(drift): %v", err)
	}

	// The server pair is unchanged, but the attached registry was changed out
	// of band. Reapplying the canonical pair must still invoke its setter.
	if err := server.SetDurableStores(canonicalLedger, canonicalEvidence); err != nil {
		t.Fatalf("SetDurableStores(reconcile): %v", err)
	}
	calls, gotLedger, gotEvidence := registry.durableSnapshot()
	if calls != 3 || !sameStoreIdentity(gotLedger, canonicalLedger) || !sameStoreIdentity(gotEvidence, canonicalEvidence) {
		t.Fatalf("reconciled registry = calls:%d ledger:%T evidence:%T", calls, gotLedger, gotEvidence)
	}
}

func TestSetDurableStoresCustomTransitionFailsClosedToConcurrentSetters(t *testing.T) {
	server, store, _ := newHeadlessTestServer(t)
	ledgerOne, evidenceOne := newIPCDurableStores(t, store, "custom-transition-one")
	ledgerTwo, evidenceTwo := newIPCDurableStores(t, store, "custom-transition-two")
	registry := &durableFakeHeadlessRegistry{fakeHeadlessRegistry: newFakeHeadlessRegistry()}
	if err := server.SetHeadlessRegistry(registry); err != nil {
		t.Fatalf("SetHeadlessRegistry: %v", err)
	}

	registry.entered = make(chan struct{}, 1)
	registry.release = make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- server.SetDurableStores(ledgerOne, evidenceOne) }()
	<-registry.entered
	if got := server.getHeadlessRegistry(); got != nil {
		t.Fatalf("registry visible during custom durability transition: %T", got)
	}
	if err := server.SetDurableStores(ledgerTwo, evidenceTwo); err == nil || !strings.Contains(err.Error(), "configuration in progress") {
		t.Fatalf("concurrent SetDurableStores error = %v", err)
	}
	if err := server.SetHeadlessRegistry(newFakeHeadlessRegistry()); err == nil || !strings.Contains(err.Error(), "configuration in progress") {
		t.Fatalf("concurrent SetHeadlessRegistry error = %v", err)
	}
	close(registry.release)
	if err := <-done; err != nil {
		t.Fatalf("SetDurableStores: %v", err)
	}
	if got := server.getHeadlessRegistry(); got != registry {
		t.Fatalf("custom registry not republished after transition: %T", got)
	}
	if got := server.getDurableLedger(); !sameStoreIdentity(got, ledgerOne) {
		t.Fatalf("canonical ledger = %T, want first transition pair", got)
	}
}

func TestSetDurableStoresRejectsIncompleteOrNonNativeLateAttachment(t *testing.T) {
	server, store, _ := newHeadlessTestServer(t)
	ledger, evidenceStore := newIPCDurableStores(t, store, "late")
	if err := server.SetDurableStores(ledger, nil); err == nil {
		t.Fatal("SetDurableStores accepted incomplete pair")
	}
	if err := server.SetHeadlessRegistry(newFakeHeadlessRegistry()); err != nil {
		t.Fatalf("SetHeadlessRegistry: %v", err)
	}
	if err := server.SetDurableStores(ledger, evidenceStore); err == nil || !strings.Contains(err.Error(), "non-capable") {
		t.Fatalf("late SetDurableStores error = %v", err)
	}
	if got := server.getDurableLedger(); got != nil {
		t.Fatalf("failed durability update published ledger %T", got)
	}
}

func TestServerDurabilitySettersAreRaceSafe(t *testing.T) {
	server, store, _ := newHeadlessTestServer(t)
	ledgerOne, evidenceOne := newIPCDurableStores(t, store, "race-one")
	ledgerTwo, evidenceTwo := newIPCDurableStores(t, store, "race-two")
	registries := []*headless.Registry{
		headless.NewRegistry(headless.RegistryConfig{}),
		headless.NewRegistry(headless.RegistryConfig{}),
	}
	pairs := []struct {
		ledger   runledger.Store
		evidence evidence.Store
	}{{ledgerOne, evidenceOne}, {ledgerTwo, evidenceTwo}}

	errs := make(chan error, 200)
	var wg sync.WaitGroup
	for worker := 0; worker < 3; worker++ {
		wg.Add(1)
		go func(offset int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				index := (i + offset) % 2
				if offset == 0 {
					if err := server.SetHeadlessRegistry(registries[index]); err != nil {
						errs <- err
					}
				} else if offset == 1 {
					pair := pairs[index]
					if err := server.SetDurableStores(pair.ledger, pair.evidence); err != nil {
						errs <- err
					}
				} else {
					_ = server.getHeadlessRegistry()
					_ = server.getDurableLedger()
				}
			}
		}(worker)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent setter: %v", err)
	}
}

func TestInitHeadlessRegistryInterleavedWithDurabilitySet(t *testing.T) {
	server, store, root := newHeadlessTestServer(t)
	appCfg := config.DefaultConfig()
	appCfg.Providers.OpenRouter.APIKey = "test-key"
	manager, err := model.NewManager(appCfg)
	if err != nil {
		t.Fatalf("model.NewManager: %v", err)
	}
	server.models = manager
	server.appConfig = appCfg
	ledger, evidenceStore := newIPCDurableStores(t, store, "init-race")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	start := make(chan struct{})
	var registry *headless.Registry
	var initErr, storesErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		registry, initErr = server.InitHeadlessRegistryWithError(ctx)
	}()
	go func() {
		defer wg.Done()
		<-start
		storesErr = server.SetDurableStores(ledger, evidenceStore)
	}()
	close(start)
	wg.Wait()
	if initErr != nil || storesErr != nil || registry == nil {
		t.Fatalf("interleaved init = registry:%T initErr:%v storesErr:%v", registry, initErr, storesErr)
	}
	t.Cleanup(registry.Stop)
	if _, err := registry.CreateSession(headless.CreateSessionRequest{Project: root}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := server.SetDurableStores(nil, nil); err == nil || !strings.Contains(err.Error(), "runner creation") {
		t.Fatalf("registry published without configured durability; clear error = %v", err)
	}
}

func newIPCDurableStores(t *testing.T, store *storage.Store, name string) (runledger.Store, evidence.Store) {
	t.Helper()
	evidenceStore, err := evidence.NewWithDB(store.DB(), filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("evidence.NewWithDB: %v", err)
	}
	ledger, err := runledger.NewWithDB(store.DB())
	if err != nil {
		t.Fatalf("runledger.NewWithDB: %v", err)
	}
	return ledger, evidenceStore
}

func TestCreateHeadlessSessionDefaultsProjectRoot(t *testing.T) {
	server, _, root := newHeadlessTestServer(t)
	registry := newFakeHeadlessRegistry()
	server.SetHeadlessRegistry(registry)

	r := chi.NewRouter()
	server.setupHeadlessRoutes(r)

	body := strings.NewReader(`{"project":"","model":"test-model"}`)
	req := httptest.NewRequest(http.MethodPost, "/headless/sessions", body)
	req.Header.Set("Content-Type", "application/json")
	req = withScope(req, storage.TokenScopeMember)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d want %d body=%s", rr.Code, http.StatusCreated, rr.Body.String())
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.createReq.Project != root {
		t.Fatalf("create project=%q want %q", registry.createReq.Project, root)
	}

	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json: %v", err)
	}
	if payload["session"] == nil {
		t.Fatalf("expected session in response")
	}
	if payload["stream"] != "/buckley.ipc.v1.BuckleyIPC/Subscribe" {
		t.Fatalf("stream=%v want %q", payload["stream"], "/buckley.ipc.v1.BuckleyIPC/Subscribe")
	}
}

func TestCreateHeadlessSessionAcceptsGitURLProjects(t *testing.T) {
	server, _, _ := newHeadlessTestServer(t)
	registry := newFakeHeadlessRegistry()
	server.SetHeadlessRegistry(registry)

	r := chi.NewRouter()
	server.setupHeadlessRoutes(r)

	body := strings.NewReader(`{"project":"https://example.com/acme/repo.git","model":"test-model"}`)
	req := httptest.NewRequest(http.MethodPost, "/headless/sessions", body)
	req.Header.Set("Content-Type", "application/json")
	req = withScope(req, storage.TokenScopeMember)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d want %d body=%s", rr.Code, http.StatusCreated, rr.Body.String())
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.createReq.Project != "https://example.com/acme/repo.git" {
		t.Fatalf("create project=%q want %q", registry.createReq.Project, "https://example.com/acme/repo.git")
	}
}

func TestListHeadlessSessionsReturnsCount(t *testing.T) {
	server, store, _ := newHeadlessTestServer(t)
	registry := newFakeHeadlessRegistry()
	registry.sessions["s1"] = &headless.SessionInfo{ID: "s1", State: headless.StateIdle, CreatedAt: time.Now().UTC(), LastActive: time.Now().UTC()}
	registry.sessions["s2"] = &headless.SessionInfo{ID: "s2", State: headless.StateIdle, CreatedAt: time.Now().UTC(), LastActive: time.Now().UTC()}
	server.SetHeadlessRegistry(registry)

	now := time.Now().UTC()
	if err := store.CreateSession(&storage.Session{ID: "s1", Principal: "test", CreatedAt: now, LastActive: now, Status: storage.SessionStatusActive}); err != nil {
		t.Fatalf("CreateSession s1: %v", err)
	}
	if err := store.CreateSession(&storage.Session{ID: "s2", Principal: "test", CreatedAt: now, LastActive: now, Status: storage.SessionStatusActive}); err != nil {
		t.Fatalf("CreateSession s2: %v", err)
	}

	r := chi.NewRouter()
	server.setupHeadlessRoutes(r)

	req := httptest.NewRequest(http.MethodGet, "/headless/sessions", nil)
	req = withScope(req, storage.TokenScopeViewer)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json: %v", err)
	}
	if got, ok := payload["count"].(float64); !ok || got != 2 {
		t.Fatalf("count=%v want 2", payload["count"])
	}
}

func TestGetHeadlessSessionIncludesMessagesAndTodos(t *testing.T) {
	server, store, _ := newHeadlessTestServer(t)
	registry := newFakeHeadlessRegistry()
	now := time.Now().UTC()
	registry.sessions["s1"] = &headless.SessionInfo{ID: "s1", Project: "/tmp", State: headless.StateIdle, CreatedAt: now, LastActive: now}
	server.SetHeadlessRegistry(registry)

	if err := store.CreateSession(&storage.Session{ID: "s1", Principal: "test", CreatedAt: now, LastActive: now, Status: storage.SessionStatusActive}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := store.SaveMessage(&storage.Message{SessionID: "s1", Role: "user", Content: "hello", Timestamp: now, Tokens: 1}); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}
	if err := store.CreateTodo(&storage.Todo{
		SessionID:  "s1",
		Content:    "ship it",
		ActiveForm: "shipping it",
		Status:     "pending",
		OrderIndex: 0,
		CreatedAt:  now,
		UpdatedAt:  now,
	}); err != nil {
		t.Fatalf("CreateTodo: %v", err)
	}

	r := chi.NewRouter()
	server.setupHeadlessRoutes(r)

	req := httptest.NewRequest(http.MethodGet, "/headless/sessions/s1", nil)
	req = withScope(req, storage.TokenScopeViewer)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var payload struct {
		Session  headless.SessionInfo `json:"session"`
		Messages []storage.Message    `json:"messages"`
		Todos    []storage.Todo       `json:"todos"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json: %v", err)
	}
	if payload.Session.ID != "s1" {
		t.Fatalf("session id=%q want s1", payload.Session.ID)
	}
	if len(payload.Messages) != 1 {
		t.Fatalf("messages=%d want 1", len(payload.Messages))
	}
	if len(payload.Todos) != 1 {
		t.Fatalf("todos=%d want 1", len(payload.Todos))
	}
}

func TestHeadlessCommandDefaultsToInput(t *testing.T) {
	server, store, _ := newHeadlessTestServer(t)
	registry := newFakeHeadlessRegistry()
	server.SetHeadlessRegistry(registry)

	now := time.Now().UTC()
	if err := store.CreateSession(&storage.Session{ID: "s1", Principal: "test", CreatedAt: now, LastActive: now, Status: storage.SessionStatusActive}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := store.SaveSessionToken("s1", "session-token"); err != nil {
		t.Fatalf("SaveSessionToken: %v", err)
	}

	r := chi.NewRouter()
	server.setupHeadlessRoutes(r)

	body := strings.NewReader(`{"content":"ls -la"}`)
	req := httptest.NewRequest(http.MethodPost, "/headless/sessions/s1/commands", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Buckley-Session-Token", "session-token")
	req = withScope(req, storage.TokenScopeMember)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status=%d want %d body=%s", rr.Code, http.StatusAccepted, rr.Body.String())
	}
	registry.mu.Lock()
	cmd := registry.lastCommand
	registry.mu.Unlock()

	if cmd.ID == "" {
		t.Fatal("command ID is empty")
	}
	if cmd.SessionID != "s1" {
		t.Fatalf("command session=%q want s1", cmd.SessionID)
	}
	if cmd.Type != "input" {
		t.Fatalf("command type=%q want input", cmd.Type)
	}
	if cmd.Content != "ls -la" {
		t.Fatalf("command content=%q want ls -la", cmd.Content)
	}
	if cmd.AcceptedBy != "test" {
		t.Fatalf("command acceptedBy=%q want authenticated principal", cmd.AcceptedBy)
	}
}

func TestHeadlessCommandDoesNotAcknowledgeBeforeDispatchCommit(t *testing.T) {
	server, store, _ := newHeadlessTestServer(t)
	release := make(chan struct{})
	registry := newFakeHeadlessRegistry()
	registry.dispatchIn = make(chan struct{}, 1)
	registry.dispatchOut = release
	if err := server.SetHeadlessRegistry(registry); err != nil {
		t.Fatalf("SetHeadlessRegistry: %v", err)
	}

	now := time.Now().UTC()
	if err := store.CreateSession(&storage.Session{ID: "s-commit", Principal: "test", CreatedAt: now, LastActive: now, Status: storage.SessionStatusActive}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := store.SaveSessionToken("s-commit", "session-token"); err != nil {
		t.Fatalf("SaveSessionToken: %v", err)
	}
	router := chi.NewRouter()
	server.setupHeadlessRoutes(router)
	req := httptest.NewRequest(http.MethodPost, "/headless/sessions/s-commit/commands", strings.NewReader(`{"type":"input","content":"committed first"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Buckley-Session-Token", "session-token")
	req = withScope(req, storage.TokenScopeMember)
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		router.ServeHTTP(recorder, req)
		close(done)
	}()
	select {
	case <-registry.dispatchIn:
	case <-time.After(5 * time.Second):
		t.Fatal("command dispatch did not reach commit boundary")
	}
	select {
	case <-done:
		t.Fatal("HTTP handler acknowledged before synchronous dispatch returned")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP handler did not finish after dispatch commit")
	}
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status=%d want %d body=%s", recorder.Code, http.StatusAccepted, recorder.Body.String())
	}
	registry.mu.Lock()
	acceptedBy := registry.lastCommand.AcceptedBy
	registry.mu.Unlock()
	if acceptedBy != "test" {
		t.Fatalf("accepted principal = %q, want test", acceptedBy)
	}
}

func TestDeleteHeadlessSessionNoContent(t *testing.T) {
	server, store, _ := newHeadlessTestServer(t)
	registry := newFakeHeadlessRegistry()
	registry.sessions["s1"] = &headless.SessionInfo{ID: "s1", State: headless.StateIdle, CreatedAt: time.Now().UTC(), LastActive: time.Now().UTC()}
	server.SetHeadlessRegistry(registry)

	now := time.Now().UTC()
	if err := store.CreateSession(&storage.Session{ID: "s1", Principal: "test", CreatedAt: now, LastActive: now, Status: storage.SessionStatusActive}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := store.SaveSessionToken("s1", "session-token"); err != nil {
		t.Fatalf("SaveSessionToken: %v", err)
	}

	r := chi.NewRouter()
	server.setupHeadlessRoutes(r)

	req := httptest.NewRequest(http.MethodDelete, "/headless/sessions/s1", nil)
	req.Header.Set("X-Buckley-Session-Token", "session-token")
	req = withScope(req, storage.TokenScopeMember)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status=%d want %d body=%s", rr.Code, http.StatusNoContent, rr.Body.String())
	}
}

func TestAdoptHeadlessSessionReturnsSession(t *testing.T) {
	server, store, _ := newHeadlessTestServer(t)
	registry := newFakeHeadlessRegistry()
	registry.adopted = &storage.Session{ID: "s1"}
	server.SetHeadlessRegistry(registry)

	now := time.Now().UTC()
	if err := store.CreateSession(&storage.Session{ID: "s1", Principal: "test", CreatedAt: now, LastActive: now, Status: storage.SessionStatusActive}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := store.SaveSessionToken("s1", "session-token"); err != nil {
		t.Fatalf("SaveSessionToken: %v", err)
	}

	r := chi.NewRouter()
	server.setupHeadlessRoutes(r)

	req := httptest.NewRequest(http.MethodPost, "/headless/sessions/s1/adopt", nil)
	req.Header.Set("X-Buckley-Session-Token", "session-token")
	req = withScope(req, storage.TokenScopeMember)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var payload struct {
		Session storage.Session `json:"session"`
		Message string          `json:"message"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json: %v", err)
	}
	if payload.Session.ID != "s1" {
		t.Fatalf("session id=%q want s1", payload.Session.ID)
	}
	if payload.Message == "" {
		t.Fatalf("expected adoption message")
	}
}

func TestInitHeadlessRegistryReturnsNilWithoutModels(t *testing.T) {
	server, _, _ := newHeadlessTestServer(t)
	if got := server.InitHeadlessRegistry(context.Background()); got != nil {
		t.Fatalf("expected nil registry without model manager")
	}
}
