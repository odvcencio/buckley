package telemetry

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestSQLiteStore_AppendAndRead(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "events.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	events := []Event{
		{Type: EventMachineState, Timestamp: time.Now(), SessionID: "s1", Data: map[string]any{"from": "idle", "to": "calling_model"}},
		{Type: EventMachineToolStart, Timestamp: time.Now(), SessionID: "s1", Data: map[string]any{"tool": "edit_file"}},
	}

	if err := store.Append(ctx, "s1", events); err != nil {
		t.Fatal(err)
	}

	got, err := store.Read(ctx, "s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2", len(got))
	}
	if got[0].Type != EventMachineState {
		t.Errorf("event[0].Type = %s, want machine.state", got[0].Type)
	}
	if got[1].Data["tool"] != "edit_file" {
		t.Error("event[1] missing tool data")
	}
}

func TestSQLiteStore_ReadByType(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "events.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.Append(ctx, "s1", []Event{
		{Type: EventMachineState, Timestamp: time.Now(), SessionID: "s1"},
		{Type: EventMachineToolStart, Timestamp: time.Now(), SessionID: "s1"},
		{Type: EventMachineState, Timestamp: time.Now(), SessionID: "s1"},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := store.ReadByType(ctx, "s1", EventMachineState)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2 state events", len(got))
	}
}

func TestSQLiteStore_ReadFromVersion(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "events.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := store.Append(ctx, "s1", []Event{
			{Type: EventMachineState, Timestamp: time.Now(), SessionID: "s1", Data: map[string]any{"i": i}},
		}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := store.Read(ctx, "s1", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d events from version 3, want 3", len(got))
	}
}

func TestSQLiteStore_InMemory(t *testing.T) {
	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.Append(ctx, "s1", []Event{
		{Type: EventMachineState, Timestamp: time.Now(), SessionID: "s1"},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := store.Read(ctx, "s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
}

func TestSQLiteStore_MultipleStreams(t *testing.T) {
	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	store.Append(ctx, "s1", []Event{{Type: EventMachineState, Timestamp: time.Now()}})
	store.Append(ctx, "s2", []Event{{Type: EventMachineSpawned, Timestamp: time.Now()}})
	store.Append(ctx, "s1", []Event{{Type: EventMachineCompleted, Timestamp: time.Now()}})

	s1, _ := store.Read(ctx, "s1", 0)
	s2, _ := store.Read(ctx, "s2", 0)

	if len(s1) != 2 {
		t.Errorf("s1 has %d events, want 2", len(s1))
	}
	if len(s2) != 1 {
		t.Errorf("s2 has %d events, want 1", len(s2))
	}
}

func TestSQLiteStore_PreservesAllFields(t *testing.T) {
	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	ts := time.Now().Truncate(time.Second) // SQLite second precision
	event := Event{
		Type:      EventMachineState,
		Timestamp: ts,
		SessionID: "sess-1",
		PlanID:    "plan-1",
		TaskID:    "task-1",
		Data:      map[string]any{"key": "value", "num": float64(42)},
	}

	store.Append(ctx, "stream", []Event{event})
	got, _ := store.Read(ctx, "stream", 0)

	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	e := got[0]
	if e.Type != event.Type {
		t.Errorf("Type = %s, want %s", e.Type, event.Type)
	}
	if e.SessionID != event.SessionID {
		t.Errorf("SessionID = %s, want %s", e.SessionID, event.SessionID)
	}
	if e.PlanID != event.PlanID {
		t.Errorf("PlanID = %s, want %s", e.PlanID, event.PlanID)
	}
	if e.TaskID != event.TaskID {
		t.Errorf("TaskID = %s, want %s", e.TaskID, event.TaskID)
	}
	if e.Data["key"] != "value" {
		t.Errorf("Data[key] = %v, want value", e.Data["key"])
	}
	if e.Data["num"] != float64(42) {
		t.Errorf("Data[num] = %v, want 42", e.Data["num"])
	}
}

func TestSQLiteStore_EmptyRead(t *testing.T) {
	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	got, err := store.Read(ctx, "nonexistent", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %d events, want 0", len(got))
	}
}
