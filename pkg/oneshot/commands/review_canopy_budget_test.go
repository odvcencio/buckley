package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func canopyFloodExecutable(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX shell")
	}
	path := filepath.Join(t.TempDir(), "canopy")
	script := "#!/bin/sh\nprintf '{\"base\":\"base-sha\",\"padding\":\"'\nhead -c 67108864 /dev/zero | tr '\\000' x\nprintf '\"}'\n"
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunCanopyJSON_ExactLimitAndOverflow(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX shell")
	}
	for _, extra := range []int{0, 1} {
		t.Run(fmt.Sprint(extra), func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "canopy")
			script := fmt.Sprintf("#!/bin/sh\nprintf '{\"padding\":\"'\nhead -c %d /dev/zero | tr '\\000' x\nprintf '\"}'\n", maxCanopyReviewInput-len(`{"padding":""}`)+extra)
			if err := os.WriteFile(path, []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			parent := context.Background()
			data, err := runCanopyJSON(parent, path, root, "analyze", "summary")
			if extra == 0 {
				if err != nil || len(data) != maxCanopyReviewInput || !strings.HasPrefix(string(data), `{"padding":"`) {
					t.Fatalf("exact budget changed: bytes=%d err=%v", len(data), err)
				}
			} else if !errors.Is(err, errCanopyJSONLimit) || len(data) != 0 || parent.Err() != nil {
				t.Fatalf("overflow returned partial JSON or poisoned its caller: bytes=%d err=%v", len(data), err)
			}
		})
	}
}

func TestRunCanopyJSON_RecoveryAndBoundedFailureDetails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX shell")
	}
	parent := context.Background()
	root := t.TempDir()
	if data, err := runCanopyJSON(parent, canopyFloodExecutable(t), root); !errors.Is(err, errCanopyJSONLimit) || len(data) != 0 {
		t.Fatal("oversized JSON accepted", err)
	}
	path := filepath.Join(root, "canopy")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '  {\"ok\":true}  \\n'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	data, err := runCanopyJSON(parent, path, root)
	if err != nil || string(data) != `{"ok":true}` {
		t.Fatalf("fresh command failed after overflow: %q %v", data, err)
	}
	script := "#!/bin/sh\nhead -c 8388608 /dev/zero | tr '\\000' x >&2\nprintf 'FINAL_DIAGNOSTIC' >&2\nexit 7\n"
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	data, err = runCanopyJSON(parent, path, root)
	var exit *exec.ExitError
	if len(data) != 0 || !errors.As(err, &exit) || exit.ExitCode() != 7 || len(err.Error()) > 4096 || !strings.Contains(err.Error(), "FINAL_DIAGNOSTIC") {
		t.Fatalf("failure lost its cause or retained excessive diagnostics: bytes=%d err=%v", len(data), err)
	}
	ctx, cancel := context.WithCancel(parent)
	cancel()
	if _, err := runCanopyJSON(ctx, path, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation cause lost: %v", err)
	}
}

func TestCanopyReview_OutputAllocationProfile(t *testing.T) {
	t.Setenv("CANOPY_BIN", canopyFloodExecutable(t))
	root := t.TempDir()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	result := collectCanopyReviewEvidence(context.Background(), root, "base-sha")
	runtime.ReadMemStats(&after)
	t.Logf("offered_json_payload_bytes=%d allocated_bytes=%d retained_output_bytes=%d status=%q", 64<<20, after.TotalAlloc-before.TotalAlloc, len(result.Output), result.Status)
	if result.Status != "evidence exceeded input limit" || result.Output != "" || after.TotalAlloc-before.TotalAlloc > 32<<20 {
		t.Error("review evidence capture was not bounded")
	}
}

func TestCanopyProject_OutputAllocationProfile(t *testing.T) {
	executable := canopyFloodExecutable(t)
	root := t.TempDir()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	data, err := runCanopyJSON(context.Background(), executable, root, "analyze", "summary", "--json", ".")
	runtime.ReadMemStats(&after)
	t.Logf("offered_json_payload_bytes=%d allocated_bytes=%d retained_output_bytes=%d rejected=%t", 64<<20, after.TotalAlloc-before.TotalAlloc, len(data), err != nil)
	if err == nil || len(data) != 0 || after.TotalAlloc-before.TotalAlloc > 32<<20 {
		t.Error("project evidence capture was not bounded")
	}
}
