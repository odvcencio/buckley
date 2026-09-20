package ipc

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ipcpb "m31labs.dev/buckley/pkg/ipc/proto"
	"m31labs.dev/buckley/pkg/storage"
)

func TestGRPCServiceClose_WaitsForActiveForwarder(t *testing.T) {
	service := NewGRPCService(nil)
	t.Cleanup(service.Close)

	subscriber := &eventSubscriber{
		operator: true,
		filter:   &ipcpb.SubscribeRequest{},
		events:   make(chan *ipcpb.Event, 1),
	}
	service.subscribersMu.Lock()
	service.subscribers["listener"] = subscriber
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(service.subscribersMu.Unlock) }
	t.Cleanup(release)
	service.BroadcastEvent(Event{
		Type: string(storage.EventSessionCreated), SessionID: "active",
		Payload: storage.Session{ID: "active", Principal: "alice"},
	})
	deadline := time.Now().Add(2 * time.Second)
	for service.sessionOwner("active") != "alice" {
		if time.Now().After(deadline) {
			t.Fatal("event forwarder did not begin handling the event")
		}
		time.Sleep(time.Millisecond)
	}

	closed := make(chan struct{})
	go func() {
		service.Close()
		close(closed)
	}()
	awaitViewPatchSignal(t, service.done, "Close did not signal shutdown")
	select {
	case <-closed:
		t.Fatal("Close returned while the event forwarder was still active")
	case <-time.After(20 * time.Millisecond):
	}
	release()
	awaitViewPatchSignal(t, closed, "Close did not wait for the forwarder to exit")
	select {
	case event := <-subscriber.events:
		if event.GetSessionId() != "active" {
			t.Fatalf("forwarded event = %v, want active session", event)
		}
	default:
		t.Fatal("Close returned before the active event reached its subscriber")
	}
}

func TestGRPCServiceClose_ConcurrentCalls(t *testing.T) {
	service := NewGRPCService(nil)
	t.Cleanup(service.Close)
	var callers sync.WaitGroup
	start := make(chan struct{})
	for range 32 {
		callers.Go(func() {
			<-start
			service.BroadcastEvent(Event{Type: "server.concurrent"})
			service.Close()
			service.BroadcastEvent(Event{Type: "server.late"})
		})
	}
	close(start)
	done := make(chan struct{})
	go func() {
		callers.Wait()
		close(done)
	}()
	awaitViewPatchSignal(t, done, "concurrent Close calls did not return")
}

func TestGRPCServiceBroadcastEvent_AfterClose(t *testing.T) {
	service := NewGRPCService(nil)
	service.Close()
	for range cap(service.eventCh) + 1 {
		service.BroadcastEvent(Event{Type: "server.late"})
	}
	if queued := len(service.eventCh); queued != 0 {
		t.Errorf("queued %d events after shutdown", queued)
	}
	if dropped := atomic.LoadInt64(&service.broadcastDrops); dropped != 0 {
		t.Errorf("recorded %d slow-consumer drops after shutdown", dropped)
	}
}

func TestServerStart_StopsEventForwarderOnExit(t *testing.T) {
	for _, scenario := range []string{"canceled", "bind_failure"} {
		t.Run(scenario, func(t *testing.T) {
			store, err := storage.New(t.TempDir() + "/buckley.db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = listener.Close() })
			server := NewServer(Config{BindAddress: listener.Addr().String()}, store, nil, nil, nil, nil, nil, nil)
			t.Cleanup(server.waitForViewPatches)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			t.Cleanup(cancel)
			if scenario == "canceled" {
				cancel()
			}
			err = server.Start(ctx)
			if scenario == "bind_failure" && err == nil {
				t.Fatal("Start succeeded on an occupied port")
			}
			if server.grpcService == nil {
				t.Fatal("Start did not create the gRPC service")
			}
			t.Cleanup(server.grpcService.Close)
			select {
			case <-server.grpcService.done:
			default:
				t.Fatal("Start returned with its event forwarder still running")
			}
		})
	}
}
