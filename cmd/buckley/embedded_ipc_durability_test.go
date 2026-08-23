package main

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/ipc"
	"m31labs.dev/buckley/pkg/ipc/command"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/orchestrator"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/telemetry"
)

type embeddedDurabilityTestServer struct {
	setErr       error
	ledger       runledger.Store
	evidence     evidence.Store
	setCalls     atomic.Int64
	startCalls   atomic.Int64
	startEntered chan struct{}
	startDone    chan struct{}
	startFn      func(context.Context) error
	enteredOnce  sync.Once
	doneOnce     sync.Once
}

func (s *embeddedDurabilityTestServer) SetDurableStores(ledger runledger.Store, evidenceStore evidence.Store) error {
	s.setCalls.Add(1)
	if s.setErr != nil {
		return s.setErr
	}
	s.ledger = ledger
	s.evidence = evidenceStore
	return nil
}

func (s *embeddedDurabilityTestServer) Start(ctx context.Context) error {
	s.startCalls.Add(1)
	if s.startEntered != nil {
		s.enteredOnce.Do(func() { close(s.startEntered) })
	}
	if s.startFn != nil {
		return s.startFn(ctx)
	}
	<-ctx.Done()
	if s.startDone != nil {
		s.doneOnce.Do(func() { close(s.startDone) })
	}
	return ctx.Err()
}

type embeddedLifecycleController struct {
	runFn     func() error
	stopFn    func()
	stopCalls atomic.Int64
}

func (c *embeddedLifecycleController) Run() error {
	if c.runFn != nil {
		return c.runFn()
	}
	return nil
}

func (c *embeddedLifecycleController) Stop() {
	c.stopCalls.Add(1)
	if c.stopFn != nil {
		c.stopFn()
	}
}

func TestConfigureEmbeddedIPCDurability_UsesCallerOwnedSharedDB(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "embedded-shared.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := &embeddedDurabilityTestServer{}
	if err := configureEmbeddedIPCDurability(server, store); err != nil {
		t.Fatal(err)
	}
	if server.setCalls.Load() != 1 || server.ledger == nil || server.evidence == nil {
		t.Fatalf("publication calls=%d ledger=%T evidence=%T", server.setCalls.Load(), server.ledger, server.evidence)
	}
	type dbBacked interface{ DB() *sql.DB }
	ledgerDB, ledgerOK := server.ledger.(dbBacked)
	evidenceDB, evidenceOK := server.evidence.(dbBacked)
	if !ledgerOK || !evidenceOK || ledgerDB.DB() != store.DB() || evidenceDB.DB() != store.DB() {
		t.Fatalf("wrappers do not share caller DB: ledger=%T evidence=%T", server.ledger, server.evidence)
	}
	if err := store.DB().Ping(); err != nil {
		t.Fatalf("configuration closed caller DB: %v", err)
	}
}

func TestStartEmbeddedIPCServer_DurabilityFailureOrTypedNilNeverStarts(t *testing.T) {
	original := serveNewServerFn
	t.Cleanup(func() { serveNewServerFn = original })
	store, err := storage.New(filepath.Join(t.TempDir(), "embedded-failure.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := config.DefaultConfig()
	cfg.IPC.Enabled = true
	cfg.IPC.Bind = "127.0.0.1:0"
	cfg.IPC.RequireToken = false

	failing := &embeddedDurabilityTestServer{setErr: errors.New("injected publication failure")}
	serveNewServerFn = func(ipc.Config, *storage.Store, *telemetry.Hub, *command.Gateway, orchestrator.PlanStore, *config.Config, *orchestrator.WorkflowManager, *model.Manager) ipcServer {
		return failing
	}
	handle, _, err := startEmbeddedIPCServerWithGrace(cfg, store, nil, nil, nil, nil, nil, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "injected publication failure") || handle != nil ||
		failing.setCalls.Load() != 1 || failing.startCalls.Load() != 0 {
		t.Fatalf("failure result handle=%v err=%v set=%d start=%d", handle != nil, err, failing.setCalls.Load(), failing.startCalls.Load())
	}
	if err := store.DB().Ping(); err != nil {
		t.Fatalf("failure closed caller DB: %v", err)
	}

	var typedNil *embeddedDurabilityTestServer
	serveNewServerFn = func(ipc.Config, *storage.Store, *telemetry.Hub, *command.Gateway, orchestrator.PlanStore, *config.Config, *orchestrator.WorkflowManager, *model.Manager) ipcServer {
		return typedNil
	}
	handle, _, err = startEmbeddedIPCServerWithGrace(cfg, store, nil, nil, nil, nil, nil, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "server unavailable") || handle != nil {
		t.Fatalf("typed nil result handle=%v err=%v", handle != nil, err)
	}
	if err := store.DB().Ping(); err != nil {
		t.Fatalf("typed nil closed caller DB: %v", err)
	}
}

func TestInteractiveLifecycle_NormalExitStopsJoinsAndLeavesCallerStoreOpen(t *testing.T) {
	original := serveNewServerFn
	t.Cleanup(func() { serveNewServerFn = original })
	store, err := storage.New(filepath.Join(t.TempDir(), "embedded-stop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := &embeddedDurabilityTestServer{
		startEntered: make(chan struct{}),
		startDone:    make(chan struct{}),
	}
	serveNewServerFn = func(ipc.Config, *storage.Store, *telemetry.Hub, *command.Gateway, orchestrator.PlanStore, *config.Config, *orchestrator.WorkflowManager, *model.Manager) ipcServer {
		return server
	}
	cfg := config.DefaultConfig()
	cfg.IPC.Enabled = true
	cfg.IPC.Bind = "127.0.0.1:0"
	cfg.IPC.RequireToken = false
	handle, _, err := startEmbeddedIPCServerWithGrace(cfg, store, nil, nil, nil, nil, nil, time.Millisecond)
	if err != nil || handle == nil {
		t.Fatalf("start result handle=%v err=%v", handle != nil, err)
	}
	select {
	case <-server.startEntered:
	default:
		t.Fatal("server Start was not invoked after durable publication")
	}
	if server.setCalls.Load() != 1 || server.startCalls.Load() != 1 {
		t.Fatalf("set=%d start=%d", server.setCalls.Load(), server.startCalls.Load())
	}
	controllerErr := errors.New("controller failed")
	controller := &embeddedLifecycleController{runFn: func() error { return controllerErr }}
	if err := runInteractiveWithEmbeddedIPC(controller, handle); !errors.Is(err, controllerErr) {
		t.Fatalf("controller error precedence: %v", err)
	}
	select {
	case <-server.startDone:
	case <-time.After(time.Second):
		t.Fatal("embedded server did not stop")
	}
	if err := store.DB().Ping(); err != nil {
		t.Fatalf("stop closed caller-owned store: %v", err)
	}
}

func TestStartEmbeddedIPCServer_EarlyFailurePropagates(t *testing.T) {
	original := serveNewServerFn
	t.Cleanup(func() { serveNewServerFn = original })
	store, err := storage.New(filepath.Join(t.TempDir(), "embedded-early.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	earlyErr := errors.New("listen failed")
	server := &embeddedDurabilityTestServer{startFn: func(context.Context) error { return earlyErr }}
	serveNewServerFn = func(ipc.Config, *storage.Store, *telemetry.Hub, *command.Gateway, orchestrator.PlanStore, *config.Config, *orchestrator.WorkflowManager, *model.Manager) ipcServer {
		return server
	}
	cfg := config.DefaultConfig()
	cfg.IPC.Enabled = true
	cfg.IPC.Bind = "127.0.0.1:0"
	cfg.IPC.RequireToken = false
	handle, _, err := startEmbeddedIPCServerWithGrace(cfg, store, nil, nil, nil, nil, nil, 50*time.Millisecond)
	if handle != nil || !errors.Is(err, earlyErr) || server.startCalls.Load() != 1 {
		t.Fatalf("handle=%v err=%v starts=%d", handle != nil, err, server.startCalls.Load())
	}
}

func TestInteractiveLifecycle_LateIPCFailureStopsController(t *testing.T) {
	original := serveNewServerFn
	t.Cleanup(func() { serveNewServerFn = original })
	store, err := storage.New(filepath.Join(t.TempDir(), "embedded-late.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	fail := make(chan struct{})
	lateErr := errors.New("late listener failure")
	server := &embeddedDurabilityTestServer{startFn: func(ctx context.Context) error {
		select {
		case <-fail:
			return lateErr
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	serveNewServerFn = func(ipc.Config, *storage.Store, *telemetry.Hub, *command.Gateway, orchestrator.PlanStore, *config.Config, *orchestrator.WorkflowManager, *model.Manager) ipcServer {
		return server
	}
	cfg := config.DefaultConfig()
	cfg.IPC.Enabled = true
	cfg.IPC.Bind = "127.0.0.1:0"
	cfg.IPC.RequireToken = false
	handle, _, err := startEmbeddedIPCServerWithGrace(cfg, store, nil, nil, nil, nil, nil, time.Millisecond)
	if err != nil || handle == nil {
		t.Fatalf("start handle=%v err=%v", handle != nil, err)
	}
	stopped := make(chan struct{})
	var stopOnce sync.Once
	controllerClosedErr := errors.New("controller closed")
	controller := &embeddedLifecycleController{
		runFn:  func() error { <-stopped; return controllerClosedErr },
		stopFn: func() { stopOnce.Do(func() { close(stopped) }) },
	}
	close(fail)
	done := make(chan error, 1)
	go func() { done <- runInteractiveWithEmbeddedIPC(controller, handle) }()
	select {
	case err := <-done:
		if !errors.Is(err, lateErr) || controller.stopCalls.Load() != 1 {
			t.Fatalf("err=%v stop calls=%d", err, controller.stopCalls.Load())
		}
	case <-time.After(time.Second):
		t.Fatal("late IPC failure did not stop the interactive lifecycle")
	}
}

func TestInteractiveLifecycle_IPCFailureBeforeRunWinsClosedControllerError(t *testing.T) {
	ipcErr := errors.New("IPC already failed")
	handle := newEmbeddedIPCHandle(func(error) {})
	handle.finish(ipcErr)
	stopped := make(chan struct{})
	var stopOnce sync.Once
	controller := &embeddedLifecycleController{
		runFn:  func() error { <-stopped; return errors.New("controller closed") },
		stopFn: func() { stopOnce.Do(func() { close(stopped) }) },
	}
	if err := runInteractiveWithEmbeddedIPC(controller, handle); !errors.Is(err, ipcErr) {
		t.Fatalf("pre-run IPC failure precedence: %v", err)
	}
	if controller.stopCalls.Load() != 1 {
		t.Fatalf("controller stop calls=%d", controller.stopCalls.Load())
	}
}

func TestInteractiveLifecycle_ControllerFirstWinsSimultaneousIPCFailure(t *testing.T) {
	controllerErr := errors.New("controller failed independently")
	ipcErr := errors.New("IPC failed during join")
	var handle *embeddedIPCHandle
	handle = newEmbeddedIPCHandle(func(error) { handle.finish(ipcErr) })
	controller := &embeddedLifecycleController{runFn: func() error { return controllerErr }}
	if err := runInteractiveWithEmbeddedIPC(controller, handle); !errors.Is(err, controllerErr) || errors.Is(err, ipcErr) {
		t.Fatalf("controller-first precedence: %v", err)
	}
	if controller.stopCalls.Load() != 1 {
		t.Fatalf("joined IPC failure stop calls=%d", controller.stopCalls.Load())
	}
}

func TestEmbeddedIPCHandle_ConcurrentStopAndWaitAreIdempotent(t *testing.T) {
	original := serveNewServerFn
	t.Cleanup(func() { serveNewServerFn = original })
	store, err := storage.New(filepath.Join(t.TempDir(), "embedded-concurrent.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := &embeddedDurabilityTestServer{startDone: make(chan struct{})}
	serveNewServerFn = func(ipc.Config, *storage.Store, *telemetry.Hub, *command.Gateway, orchestrator.PlanStore, *config.Config, *orchestrator.WorkflowManager, *model.Manager) ipcServer {
		return server
	}
	cfg := config.DefaultConfig()
	cfg.IPC.Enabled = true
	cfg.IPC.Bind = "127.0.0.1:0"
	cfg.IPC.RequireToken = false
	handle, _, err := startEmbeddedIPCServerWithGrace(cfg, store, nil, nil, nil, nil, nil, time.Millisecond)
	if err != nil || handle == nil {
		t.Fatalf("start handle=%v err=%v", handle != nil, err)
	}
	results := make(chan error, 16)
	for i := 0; i < 8; i++ {
		go func() { results <- handle.Stop() }()
		go func() { results <- handle.Wait() }()
	}
	for i := 0; i < 16; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("concurrent result: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("concurrent Stop/Wait leaked a goroutine")
		}
	}
	if server.startCalls.Load() != 1 {
		t.Fatalf("start calls=%d", server.startCalls.Load())
	}
}
