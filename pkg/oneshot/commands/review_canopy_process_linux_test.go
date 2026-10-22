//go:build linux

package commands

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func canopyProcessRunning(pid int) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	end := strings.LastIndexByte(string(data), ')')
	fields := strings.Fields(string(data[end+1:]))
	return len(fields) > 0 && fields[0] != "Z" && fields[0] != "X"
}

func canopyProcessPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("fixture did not record a descendant PID")
	return 0
}

func TestRunCanopyJSON_CancellationCleansOnlyItsCommandTree(t *testing.T) {
	sibling := exec.Command("sleep", "60")
	if err := sibling.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sibling.Process.Kill(); _ = sibling.Wait() })
	for _, mode := range []string{"cancel", "overflow", "exited", "failed", "index_failure", "index_overflow"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			scratch := t.TempDir()
			t.Setenv("TMPDIR", scratch)
			pidFile := filepath.Join(root, "child.pid")
			t.Setenv("CANOPY_TEST_CHILD_PID", pidFile)
			script := "#!/bin/sh\nsleep 60 &\necho $! > \"$CANOPY_TEST_CHILD_PID\"\n"
			switch mode {
			case "cancel":
				script += "wait\n"
			case "overflow":
				script += "head -c 67108864 /dev/zero | tr '\\000' x\nwait\n"
			case "exited":
				script += "printf '{\"ok\":true}'\nexit 0\n"
			case "failed":
				script += "exit 7\n"
			case "index_failure":
				script += "printf '{}'\nexit 7\n"
			case "index_overflow":
				script += "head -c 67108865 /dev/zero\nwait\n"
			}
			path := filepath.Join(root, "canopy")
			if err := os.WriteFile(path, []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			started := time.Now()
			go func() {
				if mode == "index_failure" || mode == "index_overflow" {
					_, _, err := buildCanopyProjectIndex(ctx, path, root)
					done <- err
					return
				}
				_, err := runCanopyJSON(ctx, path, root)
				done <- err
			}()
			pid := canopyProcessPID(t, pidFile)
			defer func() { _ = syscall.Kill(pid, syscall.SIGKILL) }()
			if mode == "cancel" {
				if !canopyProcessRunning(pid) {
					t.Fatal("fixture child was not alive before cancellation")
				}
				cancel()
			}
			var err error
			select {
			case err = <-done:
			case <-time.After(4 * time.Second):
				cancel()
				t.Fatal("Canopy command did not stop")
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation cause: %v", err)
			}
			if mode == "overflow" && !errors.Is(err, errCanopyJSONLimit) {
				t.Fatalf("lost budget cause: %v", err)
			}
			if mode == "index_overflow" && !errors.Is(err, errCanopyIndexLimit) {
				t.Fatalf("lost index budget cause: %v", err)
			}
			if mode == "exited" && !errors.Is(err, exec.ErrWaitDelay) {
				t.Fatalf("lost pipe-wait cause: %v", err)
			}
			if mode == "failed" || mode == "index_failure" {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 7 {
					t.Fatalf("lost exit cause: %v", err)
				}
			}
			deadline := time.Now().Add(time.Second)
			for canopyProcessRunning(pid) && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			stopped := !canopyProcessRunning(pid)
			siblingAlive := canopyProcessRunning(sibling.Process.Pid)
			if !stopped || !siblingAlive {
				t.Fatalf("process cleanup escaped its scope: child_stopped=%t sibling_alive=%t", stopped, siblingAlive)
			}
			if mode == "index_failure" || mode == "index_overflow" {
				entries, err := os.ReadDir(scratch)
				if err != nil || len(entries) != 0 {
					t.Fatalf("failed index build left its temporary directory: %v, %v", entries, err)
				}
			}
			t.Logf("mode=%s elapsed_ms=%d descendant_stopped=%t sibling_alive=%t cause=%v", mode, time.Since(started).Milliseconds(), stopped, siblingAlive, err)
		})
	}
}
