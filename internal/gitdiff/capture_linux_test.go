//go:build linux

package gitdiff

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

func TestCapture_StopsDescendantsAndPreservesErrors(t *testing.T) {
	sibling := exec.Command("sleep", "60")
	if err := sibling.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sibling.Process.Kill(); _ = sibling.Wait() })
	for _, mode := range []string{"cancel", "metadata_overflow", "exited_parent", "failed_parent"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			script := "#!/bin/sh\nsleep 60 &\nprintf '%s' \"$!\" > child.pid\n"
			switch mode {
			case "metadata_overflow":
				script += "while :; do printf 'diff --git a/a b/a\\n'; done\n"
			case "exited_parent":
				script += "exit 0\n"
			case "failed_parent":
				script += "exit 7\n"
			default:
				script += "wait\n"
			}
			if err := os.WriteFile(filepath.Join(root, "git"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			start := time.Now()
			go func() { _, err := Capture(ctx, root, "diff"); done <- err }()
			pid := 0
			deadline := time.Now().Add(3 * time.Second)
			for pid == 0 && time.Now().Before(deadline) {
				raw, _ := os.ReadFile(filepath.Join(root, "child.pid"))
				pid, _ = strconv.Atoi(string(raw))
				if pid == 0 {
					time.Sleep(5 * time.Millisecond)
				}
			}
			if pid == 0 {
				t.Fatal("descendant did not start")
			}
			defer syscall.Kill(pid, syscall.SIGKILL)
			if mode == "cancel" {
				cancel()
			}
			var err error
			select {
			case err = <-done:
			case <-time.After(5 * time.Second):
				cancel()
				t.Fatal("capture remained blocked")
			}
			if err == nil {
				t.Fatal("lost failure")
			}
			switch mode {
			case "cancel":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("lost cancellation: %v", err)
				}
			case "metadata_overflow":
				if !strings.Contains(err.Error(), "narrow the selected paths") {
					t.Fatalf("lost budget cause: %v", err)
				}
			case "exited_parent":
				if !errors.Is(err, exec.ErrWaitDelay) {
					t.Fatalf("lost wait error: %v", err)
				}
			case "failed_parent":
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 7 {
					t.Fatalf("lost exit code: %v", err)
				}
			}
			deadline = time.Now().Add(time.Second)
			for processRunning(pid) && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			if processRunning(pid) || !processRunning(sibling.Process.Pid) {
				t.Fatal("descendant cleanup or sibling isolation failed")
			}
			t.Logf("mode=%s elapsed_ms=%d descendant_stopped=true sibling_alive=true cause=%v", mode, time.Since(start).Milliseconds(), err)
		})
	}
}

func processRunning(pid int) bool {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return false
	}
	fields := strings.Fields(string(raw)[strings.LastIndexByte(string(raw), ')')+1:])
	return len(fields) > 0 && fields[0] != "Z" && fields[0] != "X"
}

func TestCapture_BoundsStderrAndPreservesExit(t *testing.T) {
	root := t.TempDir()
	script := "#!/bin/sh\nhead -c 8388608 /dev/zero | tr '\\000' x >&2\nprintf 'final diagnostic' >&2\nexit 7\n"
	if err := os.WriteFile(filepath.Join(root, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, err := Capture(context.Background(), root, "diff")
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 || len(err.Error()) > 4096 || !strings.Contains(err.Error(), "final diagnostic") {
		t.Fatalf("diagnostic/exit lost: %v", err)
	}
	t.Logf("stderr_bytes=%d rendered_error_bytes=%d exit=7 final_diagnostic=true", 8388608+len("final diagnostic"), len(err.Error()))
}
