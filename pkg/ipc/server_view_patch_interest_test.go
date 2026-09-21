package ipc

import (
	"bytes"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ipcpb "m31labs.dev/buckley/pkg/ipc/proto"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/ui/viewmodel"
)

func TestServerBroadcastViewPatch_SkipsSnapshotWithoutInterestedConsumer(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "buckley.db"))
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{
		hub:           NewHub(),
		viewAssembler: viewmodel.NewAssembler(store, nil, nil),
	}
	service := NewGRPCService(server)
	server.hub.AddForwarder(service)
	t.Cleanup(service.Close)
	service.subscribersMu.Lock()
	service.subscribers["telemetry-only"] = &eventSubscriber{
		filter: &ipcpb.SubscribeRequest{EventTypes: []string{"telemetry.*"}},
	}
	service.subscribersMu.Unlock()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	server.logger = log.New(&output, "", 0)

	server.broadcastViewPatch("session-1")

	if strings.Contains(output.String(), "view patch failed") {
		t.Fatalf("snapshot queried closed store without a consumer: %s", output.String())
	}
}

func TestServerBroadcastViewPatch_DeliversToMatchingSubscriber(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "buckley.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now()
	if err := store.CreateSession(&storage.Session{
		ID: "session-1", Principal: "operator", CreatedAt: now, LastActive: now,
		Status: storage.SessionStatusActive,
	}); err != nil {
		t.Fatal(err)
	}
	server := &Server{
		hub:           NewHub(),
		viewAssembler: viewmodel.NewAssembler(store, nil, nil),
		logger:        log.New(&bytes.Buffer{}, "", 0),
	}
	service := NewGRPCService(server)
	server.hub.AddForwarder(service)
	t.Cleanup(service.Close)
	subscriber := &eventSubscriber{
		operator: true,
		filter: &ipcpb.SubscribeRequest{
			SessionId:  "session-1",
			EventTypes: []string{"view.patch"},
		},
		events: make(chan *ipcpb.Event, 1),
	}
	service.subscribersMu.Lock()
	service.subscribers["view"] = subscriber
	service.subscribersMu.Unlock()

	server.broadcastViewPatch("session-1")

	select {
	case event := <-subscriber.events:
		if event.GetType() != "view.patch" || event.GetSessionId() != "session-1" {
			t.Fatalf("event = %+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("matching subscriber did not receive view.patch")
	}
}
