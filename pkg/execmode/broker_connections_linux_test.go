package execmode

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBroker_ConnectionLimitAndSaturatedClose(t *testing.T) {
	countSockets := func(path string) int {
		t.Helper()
		table, err := os.ReadFile("/proc/net/unix")
		if err != nil {
			t.Fatal(err)
		}
		inodes := map[string]bool{}
		for _, line := range strings.Split(string(table), "\n") {
			prefix, ok := strings.CutSuffix(line, path)
			if !ok || len(prefix) == 0 || prefix[len(prefix)-1] != ' ' {
				continue
			}
			fields := strings.Fields(prefix)
			if len(fields) == 7 {
				inodes["socket:["+fields[6]+"]"] = true
			}
		}
		fds, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join("/proc/self/fd", fd.Name()))
			if err == nil && inodes[target] {
				count++
			}
		}
		return count
	}
	broker, err := NewBroker(newWorkspace(t), &recordingSink{})
	if err != nil {
		t.Fatal(err)
	}
	if err := broker.Start(filepath.Join(t.TempDir(), "caps.sock")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(broker.Close)
	maxConnections := DefaultCapabilityCallLimit + 1
	clients := 2 * maxConnections
	for i := 0; i < clients; i++ {
		conn, err := net.DialTimeout("unix", broker.SocketPath(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
	}
	want := 1 + maxConnections // listener plus accepted server connections
	deadline := time.Now().Add(2 * time.Second)
	for countSockets(broker.SocketPath()) < want && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if got := countSockets(broker.SocketPath()); got != want {
		t.Fatalf("broker sockets=%d want=%d (accepted cap=%d)", got, want, maxConnections)
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
