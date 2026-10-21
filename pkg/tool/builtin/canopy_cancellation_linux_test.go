//go:build linux

package builtin

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestCanopyRunner_CancellationCleansDescendants(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BUCKLEY_TEST_CANCEL_HELPER", "1")
	t.Setenv("BUCKLEY_CANOPY_TEST_BINARY", binary)
	siblingPath := filepath.Join(t.TempDir(), "sibling.pid")
	sibling := exec.Command(binary, "-test.run=^TestRunTestsCancellationProcess$", "--", "child", siblingPath)
	sibling.Env = os.Environ()
	if err := sibling.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sibling.Process.Kill(); _ = sibling.Wait() })
	if pid := cancellationProbePID(t, siblingPath); pid != sibling.Process.Pid {
		t.Fatal("sibling PID mismatch")
	}
	for _, mode := range []string{"cancel", "deadline", "exited_parent", "failed_parent"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			pidPath := filepath.Join(root, "descendant.pid")
			script := "#!/bin/sh\nexec \"$BUCKLEY_CANOPY_TEST_BINARY\" -test.run=^TestRunTestsCancellationProcess$ -- parent \"$PWD/descendant.pid\"\n"
			if mode == "exited_parent" || mode == "failed_parent" {
				script = "#!/bin/sh\nprintf 'parent exited with inherited pipes\\n'\n\"$BUCKLEY_CANOPY_TEST_BINARY\" -test.run=^TestRunTestsCancellationProcess$ -- child \"$PWD/descendant.pid\" &\n"
				if mode == "failed_parent" {
					script += "exit 7\n"
				} else {
					script += "exit 0\n"
				}
			}
			newCanopyStub(t, script)
			ctx, cancel := context.WithCancel(context.Background())
			if mode == "deadline" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
			}
			defer cancel()
			runner := &canopyRunner{}
			runner.SetWorkDir(root)
			done := make(chan error, 1)
			started := time.Now()
			go func() {
				_, _, err := runner.run(ctx, "search", "outline", "source.go")
				done <- err
			}()
			pid := cancellationProbePID(t, pidPath)
			defer func() { _ = syscall.Kill(pid, syscall.SIGKILL) }()
			if !cancellationProbeRunning(pid) {
				t.Fatal("descendant was not running before shutdown")
			}
			if mode == "cancel" {
				cancel()
			}
			if mode == "cancel" || mode == "deadline" {
				<-ctx.Done()
			}
			returned := true
			var runErr error
			select {
			case runErr = <-done:
			case <-time.After(3 * time.Second):
				returned = false
				t.Error("canopy remained blocked on a descendant's inherited pipes")
				_ = syscall.Kill(pid, syscall.SIGKILL)
				select {
				case runErr = <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("canopy remained blocked after fixture cleanup")
				}
			}
			want := ctx.Err()
			if mode == "exited_parent" {
				want = exec.ErrWaitDelay
			}
			if mode == "failed_parent" {
				var exit *exec.ExitError
				if !errors.As(runErr, &exit) || exit.ExitCode() != 7 {
					t.Errorf("exit cause lost: %v", runErr)
				}
			} else if !errors.Is(runErr, want) {
				t.Errorf("shutdown cause lost: got %v, want %v", runErr, want)
			}
			deadline := time.Now().Add(time.Second)
			for cancellationProbeRunning(pid) && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			stopped := !cancellationProbeRunning(pid)
			siblingAlive := cancellationProbeRunning(sibling.Process.Pid)
			if !stopped || !siblingAlive {
				t.Errorf("process isolation failed: descendant_stopped=%t sibling_alive=%t", stopped, siblingAlive)
			}
			t.Logf("mode=%s returned_before_fixture_cleanup=%t descendant_stopped=%t sibling_alive=%t elapsed_ms=%d cause=%v", mode, returned, stopped, siblingAlive, time.Since(started).Milliseconds(), runErr)
		})
	}
}

func TestCanopyRunner_CommandFailureRetainsCause(t *testing.T) {
	newCanopyStub(t, "#!/bin/sh\nprintf 'invalid query' >&2\nexit 7\n")
	_, _, err := (&canopyRunner{}).run(context.Background(), "search", "outline", "source.go")
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatalf("exit cause lost: %v", err)
	}
	if got := err.Error(); got != "canopy command failed: invalid query: exit status 7" {
		t.Fatalf("diagnostics lost: %s", got)
	}
}
