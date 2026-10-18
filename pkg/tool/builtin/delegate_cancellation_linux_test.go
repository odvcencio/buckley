//go:build linux

package builtin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/subagent"
)

func TestBuckleySubagentRunner_ShutdownRetainsOutputAndCleansDescendants(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline", "close", "graceful", "force"} {
		t.Run(mode, func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)
			root := t.TempDir()
			script := filepath.Join(root, "worker.sh")
			source := "#!/bin/sh\n"
			switch mode {
			case "graceful":
				source += "trap 'printf cleaned > cleanup; exit 0' TERM\n"
			case "force":
				source += "trap '' TERM\n"
			}
			source += "printf 'retained worker evidence\\n'\nsleep 30 &\nprintf '%s' \"$!\" > descendant.pid\nwait\n"
			if err := os.WriteFile(script, []byte(source), 0700); err != nil {
				t.Fatal(err)
			}
			manager := subagent.NewManager(&buckleySubagentRunner{command: script, workDir: root}, 1)
			t.Cleanup(func() { _ = manager.Close() })
			opts := subagent.SpawnOptions{Task: "wait for cancellation"}
			if mode == "deadline" {
				opts.TimeoutSeconds = 1
			}
			run, err := manager.SpawnWithOptions(opts)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var descendant int
			for descendant == 0 {
				data, _ := os.ReadFile(filepath.Join(root, "descendant.pid"))
				descendant, _ = strconv.Atoi(string(data))
				if ctx.Err() != nil {
					t.Fatal("worker did not start its descendant")
				}
				time.Sleep(time.Millisecond)
			}
			// Also clean up on the unfixed path, where Wait is stuck on the
			// descendant's inherited output pipe after killing only its parent.
			t.Cleanup(func() { _ = syscall.Kill(descendant, syscall.SIGKILL) })
			closed := make(chan error, 1)
			switch mode {
			case "cancel", "graceful", "force":
				if _, err := manager.Cancel(run.ID); err != nil {
					t.Fatal(err)
				}
			case "close":
				go func() { closed <- manager.Close() }()
			}
			finished, err := manager.Wait(ctx, run.ID)
			if err != nil {
				t.Fatalf("worker shutdown remained blocked: %v", err)
			}
			wantState := subagent.StateCancelled
			if mode == "deadline" {
				wantState = subagent.StateFailed
				if !strings.Contains(finished.Error, "elapsed-time limit exceeded") {
					t.Fatalf("deadline classification lost: %+v", finished)
				}
			}
			if finished.State != wantState || !strings.Contains(finished.Output, "retained worker evidence") {
				t.Fatalf("terminal result lost state or evidence: %+v", finished)
			}
			if mode == "graceful" {
				if _, err := os.Stat(filepath.Join(root, "cleanup")); err != nil {
					t.Fatalf("worker did not get a shutdown grace period: %v", err)
				}
			}
			if mode == "close" {
				select {
				case err := <-closed:
					if err != nil {
						t.Fatal(err)
					}
				case <-ctx.Done():
					t.Fatal("manager close remained blocked")
				}
			}
			for {
				data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", descendant))
				if os.IsNotExist(err) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				// A killed orphan can remain a zombie until the host reaps it.
				fields := strings.Fields(string(data[strings.LastIndexByte(string(data), ')')+1:]))
				if len(fields) > 0 && (fields[0] == "Z" || fields[0] == "X") {
					break
				}
				if ctx.Err() != nil {
					t.Fatal("worker descendant survived shutdown")
				}
				time.Sleep(time.Millisecond)
			}
			files, err := os.ReadDir(tmp)
			if err != nil || len(files) != 0 {
				t.Fatalf("worker temporary files remain: %v, %v", files, err)
			}
		})
	}
}
