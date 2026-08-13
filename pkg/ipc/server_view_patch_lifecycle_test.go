package ipc

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/ui/viewmodel"
)

type blockingViewPatchForwarder struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	events  []Event
}

func (f *blockingViewPatchForwarder) BroadcastEvent(event Event) {
	f.once.Do(func() { close(f.started) })
	<-f.release
	f.mu.Lock()
	f.events = append(f.events, event)
	f.mu.Unlock()
}

func (f *blockingViewPatchForwarder) eventCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events)
}

type countingViewPatchForwarder struct {
	events atomic.Int64
}

func (f *countingViewPatchForwarder) BroadcastEvent(Event) {
	f.events.Add(1)
}

func TestServerViewPatchShutdownRejectsLateStorageEvents(t *testing.T) {
	for iteration := 0; iteration < 100; iteration++ {
		forwarder := &blockingViewPatchForwarder{
			started: make(chan struct{}),
			release: make(chan struct{}),
		}
		hub := NewHub()
		hub.AddForwarder(forwarder)
		server := &Server{hub: hub}

		firstDone := make(chan struct{})
		go func() {
			defer close(firstDone)
			server.onStorageEvent(storage.Event{Type: storage.EventSessionUpdated, SessionID: "first"})
		}()
		awaitViewPatchSignal(t, forwarder.started, "first storage event did not start")

		waitDone := make(chan struct{})
		go func() {
			server.waitForViewPatches()
			close(waitDone)
		}()
		awaitViewPatchClosing(t, server)

		lateDone := make(chan struct{})
		go func() {
			server.onStorageEvent(storage.Event{Type: storage.EventSessionUpdated, SessionID: "late"})
			close(lateDone)
		}()
		awaitViewPatchSignal(t, lateDone, "late storage event was admitted during shutdown")

		close(forwarder.release)
		awaitViewPatchSignal(t, firstDone, "admitted storage event did not finish")
		awaitViewPatchSignal(t, waitDone, "view patch shutdown did not drain admitted work")

		server.onStorageEvent(storage.Event{Type: storage.EventSessionUpdated, SessionID: "after"})
		if got := forwarder.eventCount(); got != 1 {
			t.Fatalf("iteration %d: forwarded %d storage events, want only the admitted event", iteration, got)
		}
	}
}

func TestServerViewPatchShutdownRejectsDelayedStoreObserver(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "buckley.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	forwarder := &countingViewPatchForwarder{}
	hub := NewHub()
	hub.AddForwarder(forwarder)
	server := &Server{
		hub:           hub,
		viewAssembler: viewmodel.NewAssembler(store, nil, nil),
	}

	observerStarted := make(chan struct{})
	releaseObserver := make(chan struct{})
	observerDone := make(chan struct{})
	store.AddObserver(storage.ObserverFunc(func(event storage.Event) {
		close(observerStarted)
		<-releaseObserver
		server.onStorageEvent(event)
		close(observerDone)
	}))

	now := time.Now()
	if err := store.CreateSession(&storage.Session{
		ID:         "delayed-observer",
		CreatedAt:  now,
		LastActive: now,
		Status:     storage.SessionStatusActive,
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	awaitViewPatchSignal(t, observerStarted, "storage observer did not start")

	server.waitForViewPatches()
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	close(releaseObserver)
	awaitViewPatchSignal(t, observerDone, "delayed storage observer did not finish")

	if got := forwarder.events.Load(); got != 0 {
		t.Fatalf("forwarded %d events after shutdown, want 0", got)
	}
}

func awaitViewPatchClosing(t *testing.T, server *Server) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		server.viewPatchMu.Lock()
		closing := server.viewPatchClosing
		server.viewPatchMu.Unlock()
		if closing {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("view patch shutdown did not close admission")
}

func awaitViewPatchSignal(t *testing.T, ch <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal(message)
	}
}
