//go:build linux

package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func mcpProcessRunning(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		// An opened proc entry can disappear with ESRCH during reaping.
		return !os.IsNotExist(err) && !errors.Is(err, syscall.ESRCH)
	}
	end := strings.LastIndexByte(string(data), ')')
	return end < 0 || len(data) <= end+2 || (data[end+2] != 'Z' && data[end+2] != 'X')
}

func TestCommandTransport_CloseStopsProcessGroup(t *testing.T) {
	sibling := exec.Command(fakeServerBin, "-mode=idle")
	if err := sibling.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sibling.Process.Kill(); _ = sibling.Wait() })
	path := filepath.Join(t.TempDir(), "pids")
	transport := &commandTransport{command: exec.Command(fakeServerBin, "-mode=stubborn", "-pid-file="+path), limit: 1024, grace: 25 * time.Millisecond}
	connection, err := transport.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	var parent, child int
	deadline := time.Now().Add(5 * time.Second)
	for child == 0 && time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil {
			_, _ = fmt.Sscanf(string(data), "%d %d", &parent, &child)
		}
		time.Sleep(time.Millisecond)
	}
	if child == 0 {
		t.Fatal("fixture did not start descendant")
	}
	if !mcpProcessRunning(parent) || !mcpProcessRunning(child) {
		t.Fatal("fixture not live before close")
	}
	started := time.Now()
	closed := make(chan struct{})
	go func() { _ = connection.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		_ = signalMCPProcess(transport.command, true)
		t.Fatal("close remained blocked")
	}
	deadline = time.Now().Add(time.Second)
	for (mcpProcessRunning(parent) || mcpProcessRunning(child)) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	parentRunning, childRunning, siblingRunning := mcpProcessRunning(parent), mcpProcessRunning(child), mcpProcessRunning(sibling.Process.Pid)
	if parentRunning || childRunning || !siblingRunning {
		t.Fatalf("process ownership cleanup failed: parent_running=%v descendant_running=%v sibling_running=%v", parentRunning, childRunning, siblingRunning)
	}
	t.Logf("parent_stopped=true descendant_stopped=true sibling_alive=true close_ms=%d", time.Since(started).Milliseconds())
}

func TestCommandTransport_StartFailureClosesPipes(t *testing.T) {
	connect := func() {
		transport := &commandTransport{command: exec.Command(filepath.Join(t.TempDir(), "missing")), limit: 1024}
		if _, err := transport.Connect(context.Background()); err == nil {
			t.Fatal("missing command started")
		}
	}
	connect()
	before, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	for range 32 {
		connect()
	}
	after, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) > len(before) {
		t.Fatalf("file descriptors leaked: before=%d after=%d", len(before), len(after))
	}
	t.Logf("failed_starts=32 fd_before=%d fd_after=%d", len(before), len(after))
}
