package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBuildCanopyProjectIndex_BoundedStreamAndCleanup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX shell")
	}
	for _, tc := range []struct {
		name     string
		bytes    int
		exit     int
		canceled bool
	}{
		{"small", 32, 0, false},
		{"exact_limit", maxCanopyProjectIndexBytes, 0, false},
		{"overflow", maxCanopyProjectIndexBytes + 1, 0, false},
		{"command_failure", 32, 7, false},
		{"canceled", 32, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, scratch := t.TempDir(), t.TempDir()
			t.Setenv("TMPDIR", scratch)
			helper := filepath.Join(root, "canopy")
			script := fmt.Sprintf("#!/bin/sh\n[ \"$#\" -eq 6 ] && [ \"$1:$2:$3:$4:$5:$6\" = 'index:build:--json:--out::.' ] || exit 63\nhead -c %d /dev/zero\nexit %d\n", tc.bytes, tc.exit)
			if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			path, cleanup, err := buildCanopyProjectIndex(parent, helper, root)
			switch {
			case tc.canceled:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("lost cancellation: %v", err)
				}
			case tc.bytes > maxCanopyProjectIndexBytes:
				if !errors.Is(err, errCanopyIndexLimit) || parent.Err() != nil {
					t.Fatalf("overflow = %v, parent = %v", err, parent.Err())
				}
			case tc.exit != 0:
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != tc.exit {
					t.Fatalf("lost exit status: %v", err)
				}
			default:
				if err != nil || cleanup == nil {
					t.Fatalf("build = %q, %v", path, err)
				}
				t.Cleanup(cleanup)
				info, err := os.Stat(path)
				if err != nil || info.Size() != int64(tc.bytes) || info.Mode().Perm() != 0600 {
					t.Fatalf("cache = %v, %v", info, err)
				}
				cleanup()
			}
			if err != nil && (path != "" || cleanup != nil) {
				t.Fatal("failed build exposed an incomplete index")
			}
			entries, err := os.ReadDir(scratch)
			if err != nil || len(entries) != 0 {
				t.Fatalf("temporary index remains: %v, %v", entries, err)
			}
		})
	}
}

func TestCollectCanopyProjectSummary_IndexOverflowDoesNotReparse(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX shell")
	}
	root, scratch := t.TempDir(), t.TempDir()
	t.Setenv("TMPDIR", scratch)
	t.Setenv("CANOPY_INDEX_TEST_CALLS", filepath.Join(root, "calls"))
	helper := filepath.Join(root, "canopy")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s:%%s\\n' \"$1\" \"$2\" >> \"$CANOPY_INDEX_TEST_CALLS\"\nhead -c %d /dev/zero\n", maxCanopyProjectIndexBytes+1)
	if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CANOPY_BIN", helper)
	output, status := collectCanopyProjectSummary(context.Background(), root)
	if output != "" || !strings.Contains(status, "64 MiB disk budget") {
		t.Fatalf("overflow = %q, %q", output, status)
	}
	calls, err := os.ReadFile(filepath.Join(root, "calls"))
	if err != nil || string(calls) != "index:build\n" {
		t.Fatalf("overflow triggered extra analysis: %q, %v", calls, err)
	}
	entries, err := os.ReadDir(scratch)
	if err != nil || len(entries) != 0 {
		t.Fatalf("overflow left a partial index: %v, %v", entries, err)
	}
}

type canopyFailingWriter struct {
	err error
}

func (w canopyFailingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestCanopyBoundedWriter_WriteFailureCancelsCommand(t *testing.T) {
	for _, want := range []error{os.ErrPermission, io.ErrShortWrite} {
		t.Run(want.Error(), func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			writeErr := want
			if want == io.ErrShortWrite {
				writeErr = nil
			}
			writer := &canopyBoundedWriter{writer: canopyFailingWriter{writeErr}, remaining: 8, cancel: cancel, limitErr: errCanopyIndexLimit}
			n, err := writer.Write([]byte("index"))
			if n != 0 || !errors.Is(err, want) || !errors.Is(context.Cause(ctx), want) {
				t.Fatalf("write = %d, %v; cause = %v", n, err, context.Cause(ctx))
			}
		})
	}
}
