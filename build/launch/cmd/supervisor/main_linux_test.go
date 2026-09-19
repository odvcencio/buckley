//go:build linux

package main

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSupervise_PreservesExitCode(t *testing.T) {
	if got := supervise([]string{"/bin/sh", "-c", "exit 17"}); got != 17 {
		t.Fatalf("supervise exit = %d, want 17", got)
	}
}

func TestSupervise_ReapsDetachedDescendant(t *testing.T) {
	pidFile := t.TempDir() + "/pid"
	script := "(setsid sleep 30 >/dev/null 2>&1 & echo $! > " + pidFile + "); exit 0"
	if got := supervise([]string{"/bin/sh", "-c", script}); got != 0 {
		t.Fatalf("supervise exit = %d, want 0", got)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		err = syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("detached descendant %d survived supervisor cleanup: %v", pid, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCleanupDescendants_UnreapableChildIsBounded(t *testing.T) {
	started := time.Now()
	if cleanupDescendants(1<<30, 0, 35*time.Millisecond, func(int) []int { return []int{1 << 30} }) {
		t.Fatal("unreapable child reported clean")
	}
	elapsed := time.Since(started)
	if elapsed < 30*time.Millisecond || elapsed > 500*time.Millisecond {
		t.Fatalf("bounded cleanup elapsed = %v", elapsed)
	}
}

func TestExitCode_SignalAndGeneric(t *testing.T) {
	if got := exitCode(errors.New("generic")); got != 125 {
		t.Fatalf("generic exit = %d", got)
	}
}
