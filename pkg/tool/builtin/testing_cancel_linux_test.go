//go:build linux

package builtin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// This helper creates a real descendant inheriting the test runner's pipes.
// The bounded sleep is a final safety net if a regression defeats cleanup.
func TestRunTestsCancellationProcess(t *testing.T) {
	if os.Getenv("BUCKLEY_TEST_CANCEL_HELPER") != "1" {
		return
	}
	role, pidPath := os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
	if role == "child" {
		if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(60 * time.Second)
		return
	}
	fmt.Fprintln(os.Stdout, "partial test output")
	child := exec.Command(os.Args[0], "-test.run=^TestRunTestsCancellationProcess$", "--", "child", pidPath)
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	if err := child.Run(); err != nil {
		t.Fatal(err)
	}
}

func cancellationProbePID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(string(data)); err == nil && pid > 1 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("probe did not become ready: %s", path)
	return 0
}

func cancellationProbeRunning(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return !os.IsNotExist(err) && !errors.Is(err, syscall.ESRCH)
	}
	// A killed child can briefly remain a zombie until its new parent reaps it.
	end := strings.LastIndexByte(string(data), ')')
	return end < 0 || len(data) <= end+2 || (data[end+2] != 'Z' && data[end+2] != 'X')
}

func TestRunTestsTool_CancellationStopsDescendant(t *testing.T) {
	siblingPath := filepath.Join(t.TempDir(), "sibling.pid")
	sibling := exec.Command(os.Args[0], "-test.run=^TestRunTestsCancellationProcess$", "--", "child", siblingPath)
	sibling.Env = append(os.Environ(), "BUCKLEY_TEST_CANCEL_HELPER=1")
	if err := sibling.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sibling.Process.Kill(); _ = sibling.Wait() })
	if pid := cancellationProbePID(t, siblingPath); pid != sibling.Process.Pid {
		t.Fatal("sibling PID mismatch")
	}
	for _, tc := range []struct {
		framework string
		deadline  bool
	}{{"go", false}, {"jest", false}, {"pytest", false}, {"cargo", false}, {"go", true}} {
		t.Run(fmt.Sprintf("%s/deadline=%t", tc.framework, tc.deadline), func(t *testing.T) {
			root := t.TempDir()
			pidPath := filepath.Join(root, "descendant.pid")
			oldExec := execCommandContext
			t.Cleanup(func() { execCommandContext = oldExec })
			execCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunTestsCancellationProcess$", "--", "parent", pidPath)
				cmd.Env = append(os.Environ(), "BUCKLEY_TEST_CANCEL_HELPER=1")
				return cmd
			}
			ctx, cancel := context.WithCancel(context.Background())
			if tc.deadline {
				ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
			}
			defer cancel()
			tool := &RunTestsTool{}
			tool.SetWorkDir(root)
			type outcome struct {
				output string
				exit   int
				err    error
			}
			done := make(chan outcome, 1)
			go func() {
				output, exit, _, _, err := tool.runTestsForFramework(ctx, tc.framework, ".", "", false, false)
				done <- outcome{output, exit, err}
			}()
			defer func() {
				cancel()
				if data, err := os.ReadFile(pidPath); err == nil {
					if pid, err := strconv.Atoi(string(data)); err == nil && pid > 1 && cancellationProbeRunning(pid) {
						_ = syscall.Kill(pid, syscall.SIGKILL)
					}
				}
			}()
			pid := cancellationProbePID(t, pidPath)
			if !cancellationProbeRunning(pid) {
				t.Fatal("descendant was not live before cancellation")
			}
			if !tc.deadline {
				cancel()
			}
			<-ctx.Done()
			var result outcome
			select {
			case result = <-done:
			case <-time.After(3 * time.Second):
				t.Error("run_tests remained blocked on a descendant's output pipe after cancellation")
				_ = syscall.Kill(pid, syscall.SIGKILL)
				select {
				case result = <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("probe did not exit after cleanup")
				}
			}
			if result.exit == 0 || !errors.Is(result.err, ctx.Err()) {
				t.Errorf("canceled run lost failure/cause: exit=%d err=%v want=%v", result.exit, result.err, ctx.Err())
			}
			if !strings.Contains(result.output, "partial test output") {
				t.Error("partial command output lost")
			}
			deadline := time.Now().Add(time.Second)
			running := cancellationProbeRunning(pid)
			for running && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
				running = cancellationProbeRunning(pid)
			}
			if running {
				stat, statErr := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
				t.Errorf("descendant still running after run_tests returned: pid=%d stat=%q err=%v", pid, stat, statErr)
			}
			if !cancellationProbeRunning(sibling.Process.Pid) {
				t.Error("cancellation killed a process outside the command group")
			}
		})
	}
}
