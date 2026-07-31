package storage

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestIPCEventStoreReplay(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now().UTC()
	for _, event := range []IPCEvent{
		{ID: "01j00000000000000000000001", SessionID: "s1", Type: "command.started", Payload: json.RawMessage(`{"commandId":"one"}`), CreatedAt: now},
		{ID: "01j00000000000000000000002", SessionID: "s1", Type: "tool.started", Payload: json.RawMessage(`{"toolName":"read_file"}`), CreatedAt: now.Add(time.Millisecond)},
		{ID: "01j00000000000000000000003", SessionID: "s2", Type: "command.started", Payload: json.RawMessage(`{}`), CreatedAt: now},
	} {
		if err := store.SaveIPCEvent(event); err != nil {
			t.Fatalf("SaveIPCEvent: %v", err)
		}
	}

	events, err := store.ListIPCEventsAfter("s1", "01j00000000000000000000001", 10)
	if err != nil {
		t.Fatalf("ListIPCEventsAfter: %v", err)
	}
	if len(events) != 1 || events[0].ID != "01j00000000000000000000002" {
		t.Fatalf("events=%+v want only second s1 event", events)
	}
	if string(events[0].Payload) != `{"toolName":"read_file"}` {
		t.Fatalf("payload=%s", events[0].Payload)
	}
}
