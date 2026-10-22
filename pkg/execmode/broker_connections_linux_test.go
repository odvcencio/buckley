package execmode

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBroker_ConnectionLimitAndSaturatedClose(t *testing.T) {
	countFDs := func() int {
		t.Helper()
		fds, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Fatal(err)
		}
		return len(fds)
	}
	broker, err := NewBroker(newWorkspace(t), &recordingSink{})
	if err != nil {
		t.Fatal(err)
	}
	if err := broker.Start(filepath.Join(t.TempDir(), "caps.sock")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(broker.Close)
	before := countFDs()
	maxConnections := DefaultCapabilityCallLimit + 1
	clients := 2 * maxConnections
	for i := 0; i < clients; i++ {
		conn, err := net.DialTimeout("unix", broker.SocketPath(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
	}
	want := before + clients + maxConnections
	deadline := time.Now().Add(2 * time.Second)
	for countFDs() < want && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if got := countFDs(); got != want {
		t.Fatalf("file descriptors=%d want=%d (clients=%d, accepted cap=%d)", got, want, clients, maxConnections)
	}
	closed := make(chan struct{})
	go func() {
		broker.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close stalled with a saturated accept queue")
	}
}
