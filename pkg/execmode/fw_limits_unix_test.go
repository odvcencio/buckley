//go:build !windows

package execmode

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestFWTranspiler_Helper(t *testing.T) {
	if os.Getenv("FW_TEST_BINARY") == "" {
		t.Skip("subprocess fixture only")
	}
	args := flag.Args()
	if len(args) != 2 || args[0] != "emit" {
		os.Exit(2)
	}
	info, err := os.Stat(args[1])
	if err != nil {
		os.Exit(2)
	}
	if err := os.WriteFile(os.Getenv("FW_TEST_MARKER"), []byte(strconv.FormatInt(info.Size(), 10)), 0600); err != nil {
		os.Exit(2)
	}
	switch os.Getenv("FW_TEST_SCENARIO") {
	case "output", "exact":
		size, _ := strconv.Atoi(os.Getenv("FW_TEST_OUTPUT_BYTES"))
		header := "package main\nfunc main(){}\n"
		fmt.Print(header)
		block := strings.Repeat(" ", 16<<10)
		for remaining := size - len(header); remaining > 0; {
			n := min(remaining, len(block))
			fmt.Print(block[:n])
			remaining -= n
		}
	case "stderr":
		fmt.Fprint(os.Stderr, "fake fw diagnostic\n"+strings.Repeat("d", 1<<20))
		os.Exit(2)
	case "empty_error":
		os.Exit(2)
	case "timeout":
		child := exec.Command("sleep", "2")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		time.Sleep(time.Hour)
	default:
		fmt.Print("not a Go program")
	}
	os.Exit(0)
}

func fakeFWTranspiler(t *testing.T, scenario string) (*Runner, string, string) {
	t.Helper()
	bin := t.TempDir()
	// Runtime Unix socket paths must stay below the platform path limit.
	scratch, err := os.MkdirTemp("", "fw-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(scratch) })
	t.Setenv("TMPDIR", scratch)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FW_TEST_BINARY", os.Args[0])
	t.Setenv("FW_TEST_SCENARIO", scenario)
	marker := filepath.Join(bin, "called")
	t.Setenv("FW_TEST_MARKER", marker)
	script := "#!/bin/sh\nexec \"$FW_TEST_BINARY\" -test.run='^TestFWTranspiler_Helper$' -test.timeout=10s -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "ferrous-wheel"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(t.TempDir(), &recordingSink{}, DefaultTimeout, WithoutIsolation())
	if err != nil {
		t.Fatal(err)
	}
	return runner, marker, scratch
}

func TestRunnerFW_RejectsOversizedInputBeforeTranspiling(t *testing.T) {
	runner, marker, scratch := fakeFWTranspiler(t, "input")
	_, err := runner.RunFW(context.Background(), strings.Repeat("x", MaxSourceBytes+1))
	if err == nil || !strings.Contains(err.Error(), "fw source exceeds") {
		t.Fatalf("error=%v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("transpiler ran: %v", err)
	}
	if entries, err := os.ReadDir(scratch); err != nil || len(entries) != 0 {
		t.Fatalf("scratch entries=%d err=%v", len(entries), err)
	}
}

func TestRunnerFW_GeneratedSourceBudget(t *testing.T) {
	for _, tc := range []struct {
		name string
		size int
	}{
		{"exact", MaxSourceBytes},
		{"one_over", MaxSourceBytes + 1},
		{"flood", 8 << 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner, _, _ := fakeFWTranspiler(t, "output")
			t.Setenv("FW_TEST_OUTPUT_BYTES", strconv.Itoa(tc.size))
			result, err := runner.RunFW(context.Background(), "source")
			if tc.size == MaxSourceBytes {
				if err != nil || result.ExitCode != 0 {
					t.Fatalf("exact-limit execution=%+v err=%v", result, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "generated Go exceeds") {
				t.Fatalf("size=%d error=%v", tc.size, err)
			}
		})
	}
}

func TestRunnerFW_DiagnosticBudget(t *testing.T) {
	runner, _, _ := fakeFWTranspiler(t, "stderr")
	_, err := runner.RunFW(context.Background(), "source")
	if err == nil || !strings.Contains(err.Error(), "fake fw diagnostic") || !strings.Contains(err.Error(), "stderr truncated") || len(err.Error()) > (8<<10)+256 {
		t.Fatalf("diagnostic budget failed: error bytes=%d", len(fmt.Sprint(err)))
	}
}

func TestRunnerFW_EmptyDiagnosticRetainsExitStatus(t *testing.T) {
	runner, _, _ := fakeFWTranspiler(t, "empty_error")
	_, err := runner.RunFW(context.Background(), "source")
	if err == nil || !strings.Contains(err.Error(), "exit status 2") {
		t.Fatalf("error=%v", err)
	}
}

func TestRunnerFW_TimeoutIncludesTranspilation(t *testing.T) {
	runner, _, _ := fakeFWTranspiler(t, "timeout")
	runner.timeout = 200 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	_, err := runner.RunFW(ctx, "source")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("elapsed=%s error=%v", time.Since(start), err)
	}
}

func TestRunnerFW_ParentCancellation(t *testing.T) {
	runner, marker, _ := fakeFWTranspiler(t, "input")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := runner.RunFW(ctx, "source")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("transpiler ran after cancellation: %v", err)
	}
}
