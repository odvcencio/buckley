package telemetry

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestPersister_PersistsHubEvents(t *testing.T) {
	hub := newTestHub()
	defer hub.Close()

	dbPath := filepath.Join(t.TempDir(), "events.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	p := NewPersister(hub, store, "session-1")
	defer p.Stop()

	hub.Publish(Event{
		Type: EventMachineState,
		Data: map[string]any{"from": "idle", "to": "calling_model"},
	})
	hub.Publish(Event{
		Type: EventMachineToolStart,
		Data: map[string]any{"tool": "edit_file"},
	})

	// Wait for hub to deliver + persister to flush
	time.Sleep(200 * time.Millisecond)
	p.Flush()

	ctx := context.Background()
	events, err := store.Read(ctx, "session-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("persisted %d events, want 2", len(events))
	}
	if events[0].Type != EventMachineState {
		t.Errorf("event[0].Type = %s, want machine.state", events[0].Type)
	}
}

func TestPersister_BatchesWrites(t *testing.T) {
	hub := NewHubWithConfig(&Config{
		EventQueueSize:        1000,
		BatchSize:             10,
		FlushInterval:         10 * time.Millisecond,
		RateLimit:             10000,
		SubscriberChannelSize: 256,
	})
	defer hub.Close()

	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	p := NewPersister(hub, store, "session-1")
	defer p.Stop()

	for i := 0; i < 50; i++ {
		hub.Publish(Event{
			Type: EventMachineState,
			Data: map[string]any{"i": i},
		})
	}

	// Wait for everything to flush
	time.Sleep(300 * time.Millisecond)
	p.Flush()

	ctx := context.Background()
	events, err := store.Read(ctx, "session-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 50 {
		t.Fatalf("persisted %d events, want 50", len(events))
	}
}

func TestPersister_GracefulStop(t *testing.T) {
	hub := newTestHub()
	defer hub.Close()

	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	p := NewPersister(hub, store, "session-1")

	hub.Publish(Event{Type: EventMachineSpawned, Data: map[string]any{"id": "a"}})

	// Wait for hub delivery
	time.Sleep(200 * time.Millisecond)

	p.Stop() // should flush remaining events

	ctx := context.Background()
	events, err := store.Read(ctx, "session-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 1 {
		t.Fatal("expected at least 1 persisted event after graceful stop")
	}
}
