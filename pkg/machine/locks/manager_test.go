package locks

import (
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/telemetry"
)

func newTestHub() *telemetry.Hub {
	return telemetry.NewHubWithConfig(&telemetry.Config{
		EventQueueSize:        1000,
		BatchSize:             1,
		FlushInterval:         100 * time.Millisecond,
		RateLimit:             10000,
		SubscriberChannelSize: 64,
	})
}

func TestManager_ReadWriteSemantics(t *testing.T) {
	hub := newTestHub()
	defer hub.Close()
	mgr := NewManager(hub)

	if err := mgr.AcquireRead("a", "foo.go"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.AcquireRead("b", "foo.go"); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		done <- mgr.AcquireWriteWithTimeout("c", "foo.go", 500*time.Millisecond)
	}()

	time.Sleep(50 * time.Millisecond)
	mgr.ReleaseRead("a", "foo.go")
	mgr.ReleaseRead("b", "foo.go")

	if err := <-done; err != nil {
		t.Fatalf("writer should acquire after readers release: %v", err)
	}
	mgr.ReleaseWrite("c", "foo.go")
}

func TestManager_WriterBlocksReader(t *testing.T) {
	hub := newTestHub()
	defer hub.Close()
	mgr := NewManager(hub)

	if err := mgr.AcquireWrite("a", "foo.go"); err != nil {
		t.Fatal(err)
	}

	err := mgr.AcquireReadWithTimeout("b", "foo.go", 100*time.Millisecond)
	if err == nil {
		t.Error("expected timeout error while writer holds lock")
	}

	mgr.ReleaseWrite("a", "foo.go")

	// Now should succeed
	if err := mgr.AcquireRead("b", "foo.go"); err != nil {
		t.Fatalf("reader should acquire after writer releases: %v", err)
	}
	mgr.ReleaseRead("b", "foo.go")
}

func TestManager_UserLockNoTimeout(t *testing.T) {
	hub := newTestHub()
	defer hub.Close()
	mgr := NewManager(hub, WithUserAgentID("user"))

	if err := mgr.AcquireWrite("user", "foo.go"); err != nil {
		t.Fatal(err)
	}

	err := mgr.AcquireReadWithTimeout("agent-1", "foo.go", 100*time.Millisecond)
	if err == nil {
		t.Error("expected timeout error while user holds lock")
	}

	mgr.ReleaseWrite("user", "foo.go")
}

func TestManager_PublishesAcquiredEvent(t *testing.T) {
	hub := newTestHub()
	defer hub.Close()
	ch, unsub := hub.Subscribe()
	defer unsub()
	mgr := NewManager(hub)

	mgr.AcquireWrite("a", "foo.go")

	select {
	case evt := <-ch:
		if evt.Type != telemetry.EventMachineLockAcquired {
			t.Errorf("type = %s, want machine.lock.acquired", evt.Type)
		}
		if evt.Data["agent_id"] != "a" {
			t.Errorf("agent_id = %v, want a", evt.Data["agent_id"])
		}
		if evt.Data["path"] != "foo.go" {
			t.Errorf("path = %v, want foo.go", evt.Data["path"])
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}

	mgr.ReleaseWrite("a", "foo.go")
}

func TestManager_PublishesReleasedEvent(t *testing.T) {
	hub := newTestHub()
	defer hub.Close()
	ch, unsub := hub.Subscribe()
	defer unsub()
	mgr := NewManager(hub)
	mgr.AcquireWrite("a", "foo.go")

	// Drain the acquired event
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("timeout draining acquired event")
	}

	mgr.ReleaseWrite("a", "foo.go")

	select {
	case evt := <-ch:
		if evt.Type != telemetry.EventMachineLockReleased {
			t.Errorf("type = %s, want machine.lock.released", evt.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestManager_ReleaseAll(t *testing.T) {
	hub := newTestHub()
	defer hub.Close()
	mgr := NewManager(hub)

	mgr.AcquireWrite("a", "foo.go")
	mgr.AcquireRead("a", "bar.go")
	mgr.ReleaseAll("a")

	snap := mgr.Snapshot()
	if len(snap) != 0 {
		t.Errorf("expected empty snapshot after ReleaseAll, got %v", snap)
	}
}

func TestManager_Snapshot(t *testing.T) {
	hub := newTestHub()
	defer hub.Close()
	mgr := NewManager(hub)

	mgr.AcquireWrite("a", "foo.go")
	mgr.AcquireRead("b", "bar.go")

	snap := mgr.Snapshot()
	if snap["foo.go"].Writer != "a" {
		t.Errorf("foo.go writer = %s, want a", snap["foo.go"].Writer)
	}
	if len(snap["bar.go"].Readers) != 1 || snap["bar.go"].Readers[0] != "b" {
		t.Errorf("bar.go readers = %v, want [b]", snap["bar.go"].Readers)
	}

	mgr.ReleaseWrite("a", "foo.go")
	mgr.ReleaseRead("b", "bar.go")
}

func TestManager_NilHub(t *testing.T) {
	mgr := NewManager(nil)
	if err := mgr.AcquireWrite("a", "foo.go"); err != nil {
		t.Fatal(err)
	}
	mgr.ReleaseWrite("a", "foo.go")
}
