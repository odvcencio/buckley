package builtin

import (
	"context"
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
)

func TestPostEditDiagnostics_BoundedCaptureAndScratchCleanup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix compiler fixture")
	}
	for _, mode := range []string{"failure", "success", "deadline", "inherited writer"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			module := filepath.Join(root, "module")
			bin := filepath.Join(root, "bin")
			scratch := filepath.Join(root, "scratch")
			for _, dir := range []string{module, bin, scratch} {
				if err := os.Mkdir(dir, 0755); err != nil {
					t.Fatal(err)
				}
			}
			writeFile(t, filepath.Join(module, "go.mod"), "module fixture\n\ngo 1.26\n")
			path := filepath.Join(module, "source.go")
			writeFile(t, path, "package fixture\n")
			writeFile(t, filepath.Join(scratch, "keep"), "unrelated scratch data\n")
			childPID := filepath.Join(root, "child.pid")
			script := `#!/bin/sh
test "$1" = build || exit 98
test "$GOTMPDIR" = "$TMPDIR" || exit 97
test "$TMP" = "$TMPDIR" || exit 96
test "$TEMP" = "$TMPDIR" || exit 95
printf 'compiler scratch\n' > "$GOTMPDIR/generated-object"
case "$DIAG_TEST_MODE" in
  deadline|"inherited writer")
    /bin/sleep 30 &
    printf '%s' "$!" > "$DIAG_TEST_CHILD_PID"
    if test "$DIAG_TEST_MODE" = deadline; then /bin/sleep 30; fi
    exit 1;;
esac
printf 'source.go:2: undefined: missingSymbol\n'
/usr/bin/head -c 16777216 /dev/zero | /usr/bin/tr '\000' x
printf 'stderr marker\n' >&2
/usr/bin/head -c 16777216 /dev/zero | /usr/bin/tr '\000' y >&2
if test "$DIAG_TEST_MODE" = success; then exit 0; fi
exit 1
`
			goPath := filepath.Join(bin, "go")
			writeFile(t, goPath, script)
			if err := os.Chmod(goPath, 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin)
			t.Setenv("GOTMPDIR", scratch)
			t.Setenv("DIAG_TEST_MODE", mode)
			t.Setenv("DIAG_TEST_CHILD_PID", childPID)
			t.Setenv(postEditDiagnosticsEnv, "on")
			ctx := context.Background()
			if mode == "deadline" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 2*time.Second)
				defer cancel()
			}
			got := postEditDiagnostics(ctx, path)
			switch mode {
			case "success":
				if got != "" {
					t.Fatalf("clean build reported diagnostics: %q", got)
				}
			case "failure":
				total := (32 << 20) + len("source.go:2: undefined: missingSymbol\n") + len("stderr marker\n")
				if !strings.Contains(got, "undefined: missingSymbol") || !strings.Contains(got, strconv.Itoa(total)+" bytes total") || len(got) > postEditDiagnosticsMaxBytes {
					t.Fatalf("bounded compiler output: len=%d text=%q", len(got), got)
				}
			case "deadline":
				if !strings.Contains(got, "deadline exceeded") {
					t.Fatalf("cancellation diagnostic: %q", got)
				}
			case "inherited writer":
				if got == "" {
					t.Fatal("inherited output writer should report an incomplete probe")
				}
			}
			entries, err := os.ReadDir(scratch)
			if err != nil || len(entries) != 1 || entries[0].Name() != "keep" {
				t.Fatalf("scratch not cleaned or unrelated file removed: entries=%v error=%v", entries, err)
			}
			if mode == "deadline" || mode == "inherited writer" {
				pid, err := os.ReadFile(childPID)
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
						t.Fatalf("compiler descendant survived: pid=%s state=%q", pid, state)
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
		})
	}
}

func TestBoundDiagnostics_UTF8AndByteBudget(t *testing.T) {
	for _, text := range []string{"short diagnostic", strings.Repeat("界", 1000), strings.Repeat("\xffx", 2000)} {
		got := boundDiagnostics(text, int64(len(text)))
		if len(got) > postEditDiagnosticsMaxBytes || !utf8.ValidString(got) {
			t.Fatalf("invalid bounded text: bytes=%d valid=%v", len(got), utf8.ValidString(got))
		}
		if len(text) <= postEditDiagnosticsMaxBytes && got != text {
			t.Fatalf("short diagnostic changed: %q", got)
		}
	}
}

func TestWriteFileTool_CompactStoredResult(t *testing.T) {
	dir := t.TempDir()
	tool := &WriteFileTool{}
	tool.SetWorkDir(dir)
	content := strings.Repeat("source payload\n", 100000)
	r, e := tool.Execute(map[string]any{"path": "large.txt", "content": content})
	if e != nil || !r.Success || r.Data["content"] != nil || r.Data["size"] != len(content) {
		t.Fatalf("write result: %+v %v", r, e)
	}
	for _, useToon := range []bool{false, true} {
		wire, err := toon.New(useToon).MarshalString(r)
		if err != nil || len(wire) > 4096 || strings.Contains(wire, "source payload") {
			t.Fatalf("stored result is not compact: bytes=%d err=%v", len(wire), err)
		}
	}
	written, err := os.ReadFile(filepath.Join(dir, "large.txt"))
	if err != nil || string(written) != content {
		t.Fatal("write contents changed")
	}
}
