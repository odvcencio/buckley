//go:build !windows

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/model"
)

func TestReviewCommandContext_SignalRemovesSnapshot(t *testing.T) {
	if root := os.Getenv("BUCKLEY_TEST_SIGNAL_REPO"); root != "" {
		ctx, cancel := newReviewCommandContext(time.Now(), time.Minute)
		defer cancel()
		snapshot, err := model.CaptureReviewSnapshot(ctx, root, model.ReviewSnapshotPolicy{Mode: model.ReviewSnapshotHead})
		if err != nil {
			t.Fatal(err)
		}
		dir, cleanup, err := model.PrepareReviewWorkspace(ctx, snapshot)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		if err := os.WriteFile(os.Getenv("BUCKLEY_TEST_SIGNAL_READY"), []byte(dir), 0o600); err != nil {
			t.Fatal(err)
		}
		<-ctx.Done()
		return
	}
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "initial"}} {
		if output, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git: %s: %v", output, err)
		}
	}
	for _, sig := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			ready := filepath.Join(t.TempDir(), "ready")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReviewCommandContext_SignalRemovesSnapshot$")
			cmd.Env = append(os.Environ(), "BUCKLEY_TEST_SIGNAL_REPO="+repo, "BUCKLEY_TEST_SIGNAL_READY="+ready)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill() })
			var dir []byte
			for len(dir) == 0 {
				if ctx.Err() != nil {
					_ = cmd.Wait()
					t.Fatal("child did not create snapshot")
				}
				dir, _ = os.ReadFile(ready)
				time.Sleep(time.Millisecond)
			}
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Dir(string(dir))); !os.IsNotExist(err) {
				t.Fatalf("snapshot remains after %v: %v", sig, err)
			}
		})
	}
}
