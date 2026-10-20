package builtin_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/encoding/toon"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestPatchFileTool_BoundedNativeFailure(t *testing.T) {
	requireGNUPatch(t)
	for _, limit := range []int{builtin.DefaultPatchOutputBytes, 128} {
		t.Run(strconv.Itoa(limit), func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "sample.txt")
			if err := os.WriteFile(path, []byte("present\n"), 0640); err != nil {
				t.Fatal(err)
			}
			patch := strings.Repeat("--- sample.txt\n+++ sample.txt\n@@ -1 +1 @@\n-absent\n+updated\n", 512)
			tool := &builtin.PatchFileTool{}
			tool.SetWorkDir(root)
			tool.SetMaxOutputBytes(limit)
			result, err := tool.Execute(map[string]any{"patch": patch})
			if err != nil || result.Success || result.Data["output_truncated"] != true || result.Data["output_bytes"].(int64) <= int64(limit) || !strings.Contains(result.Error, "hunk FAILED") || !strings.Contains(result.Error, "bytes total") || len(result.Error) > limit+128 || !utf8.ValidString(result.Error) {
				t.Fatalf("bounded native failure: %+v %v", result, err)
			}
			for _, useToon := range []bool{false, true} {
				text, err := toon.New(useToon).MarshalString(result)
				if err != nil || len(text) > 8<<10 {
					t.Fatalf("result bytes=%d error=%v", len(text), err)
				}
			}
			contents, err := os.ReadFile(path)
			if err != nil || string(contents) != "present\n" {
				t.Fatalf("preflight changed source: %q %v", contents, err)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0640 {
				t.Fatalf("changed source mode: %v %v", info, err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 1 {
				t.Fatalf("patch artifacts: %v %v", entries, err)
			}
		})
	}
}

func TestPatchFileTool_BoundedCommandCapture(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix command fixture")
	}
	for _, mode := range []string{"success", "preflight failure", "apply failure", "invalid utf8"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			if err := os.Mkdir(bin, 0755); err != nil {
				t.Fatal(err)
			}
			script := `#!/bin/sh
case " $* " in
 *" --dry-run "*) if test "$PATCH_TEST_MODE" != 'preflight failure'; then exit 0; fi;;
esac
printf 'actionable diagnostic\n'
if test "$PATCH_TEST_MODE" = 'invalid utf8'; then
 /usr/bin/head -c 2097152 /dev/zero | /usr/bin/tr '\000' '\377'
else
 /usr/bin/head -c 1048576 /dev/zero | /usr/bin/tr '\000' x
 /usr/bin/head -c 1048576 /dev/zero | /usr/bin/tr '\000' y >&2
fi
if test "$PATCH_TEST_MODE" = success; then exit 0; fi
exit 1
`
			if err := os.WriteFile(filepath.Join(bin, "patch"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin)
			t.Setenv("PATCH_TEST_MODE", mode)
			tool := &builtin.PatchFileTool{}
			tool.SetWorkDir(root)
			result, err := tool.Execute(map[string]any{"patch": "--- sample.txt\n+++ sample.txt\n@@ -1 +1 @@\n-old\n+new\n"})
			total := int64((2 << 20) + len("actionable diagnostic\n"))
			if err != nil || result.Success != (mode == "success") || result.Data["output_bytes"] != total || result.Data["output_truncated"] != true {
				t.Fatalf("command result: %+v %v", result, err)
			}
			text := result.Error
			if result.Success {
				text = result.Data["message"].(string)
			}
			if !utf8.ValidString(text) || len(text) > builtin.DefaultPatchOutputBytes+128 || !strings.Contains(text, "actionable diagnostic") || !strings.Contains(text, fmt.Sprintf("%d bytes total", total)) {
				t.Fatalf("bounded output: bytes=%d %q", len(text), text)
			}
		})
	}
}

func TestPatchFileTool_CancellationKillsDescendants(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix process fixture")
	}
	for _, mode := range []string{"preflight", "apply", "inherited writer"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			pidPath := filepath.Join(root, "child.pid")
			script := `#!/bin/sh
case " $* " in
 *" --dry-run "*) if test "$PATCH_TEST_MODE" = apply; then exit 0; fi;;
esac
/bin/sleep 30 &
printf '%s' "$!" > "$PATCH_TEST_PID"
if test "$PATCH_TEST_MODE" = 'inherited writer'; then exit 0; fi
wait
`
			if err := os.WriteFile(filepath.Join(root, "patch"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", root)
			t.Setenv("PATCH_TEST_MODE", mode)
			t.Setenv("PATCH_TEST_PID", pidPath)
			ctx := context.Background()
			if mode != "inherited writer" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 2*time.Second)
				defer cancel()
			}
			tool := &builtin.PatchFileTool{}
			tool.SetWorkDir(root)
			start := time.Now()
			result, err := tool.ExecuteWithContext(ctx, map[string]any{"patch": "--- sample.txt\n+++ sample.txt\n@@ -1 +1 @@\n-old\n+new\n"})
			if err != nil || result.Success || result.Error == "" || time.Since(start) > 6*time.Second {
				t.Fatalf("unbounded command lifetime: %+v %v elapsed=%v", result, err, time.Since(start))
			}
			if mode != "inherited writer" && !strings.Contains(result.Error, "deadline exceeded") {
				t.Fatalf("cancellation not surfaced: %s", result.Error)
			}
			pid, err := os.ReadFile(pidPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = exec.Command("/bin/kill", "-KILL", string(pid)).Run() })
			for deadline := time.Now().Add(time.Second); ; {
				state, err := exec.Command("/bin/ps", "-p", string(pid), "-o", "stat=").Output()
				if err != nil || strings.HasPrefix(strings.TrimSpace(string(state)), "Z") {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("patch descendant survived: pid=%s state=%q", pid, state)
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}
