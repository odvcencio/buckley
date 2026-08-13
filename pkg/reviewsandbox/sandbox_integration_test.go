package reviewsandbox

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCodexSandboxBoundary(t *testing.T) {
	if os.Getenv("BUCKLEY_TEST_CODEX_SANDBOX") != "1" {
		t.Skip("set BUCKLEY_TEST_CODEX_SANDBOX=1 to exercise the installed Codex OS sandbox")
	}
	codexCommand := strings.TrimSpace(os.Getenv("BUCKLEY_TEST_CODEX_COMMAND"))
	if codexCommand != "" && !filepath.IsAbs(codexCommand) {
		t.Fatalf("BUCKLEY_TEST_CODEX_COMMAND must be absolute, got %q", codexCommand)
	}
	if codexCommand == "" {
		if _, err := trustedLookPath("codex"); err != nil {
			t.Skipf("trusted Codex installation not found; set BUCKLEY_TEST_CODEX_COMMAND to an absolute executable: %v", err)
		}
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/reviewboundary\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	source := `package reviewboundary

import (
    "net"
    "os"
    "path/filepath"
    "testing"
    "time"
)

func TestBoundary(t *testing.T) {
	if err := os.WriteFile("source-mutation", []byte("disposable"), 0600); err != nil {
		t.Fatalf("disposable verification directory was not writable: %v", err)
	}
	logDir := filepath.Join(".buckley", "logs")
	if err := os.MkdirAll(logDir, 0700); err != nil {
		t.Fatalf("repo-relative log directory was not writable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "review.jsonl"), []byte("ok"), 0600); err != nil {
		t.Fatalf("repo-relative log was not writable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(` + strconv.Quote(root) + `, "source-mutation"), []byte("forbidden"), 0600); err == nil {
		t.Fatal("immutable snapshot root was writable")
	}
    marker := filepath.Join(os.TempDir(), "private-temp-write")
    if err := os.WriteFile(marker, []byte("ok"), 0600); err != nil {
        t.Fatalf("private temp directory was not writable: %v", err)
    }
    connection, err := net.DialTimeout("tcp", "1.1.1.1:53", 250*time.Millisecond)
    if err == nil {
        connection.Close()
        t.Fatal("sandbox unexpectedly had direct network access")
    }
}
`
	if err := os.WriteFile(filepath.Join(root, "boundary_test.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}

	executor := NewExecutorWithCodexCommand(codexCommand)
	trusted := executor.lookPath
	executor.lookPath = func(name string) (string, error) {
		if name == "bwrap" {
			return "", os.ErrNotExist
		}
		return trusted(name)
	}
	result := executor.Verify(context.Background(), Request{
		SnapshotRoot: root,
		Kind:         KindTest,
		Language:     LanguageGo,
		Pattern:      "^TestBoundary$",
		Timeout:      2 * time.Minute,
	})
	if result.Status != StatusPass || result.ExitCode != 0 {
		t.Fatalf("sandbox boundary probe failed: status=%s exit=%d error=%s\nstdout:\n%s\nstderr:\n%s", result.Status, result.ExitCode, result.Error, result.Stdout, result.Stderr)
	}
	if _, err := os.Stat(filepath.Join(root, "source-mutation")); !os.IsNotExist(err) {
		t.Fatalf("sandbox mutated source snapshot: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".buckley")); !os.IsNotExist(err) {
		t.Fatalf("sandbox created repo-relative state in source snapshot: %v", err)
	}
	if strings.TrimSpace(result.Pattern) != "^TestBoundary$" {
		t.Fatalf("trusted pattern not retained: %q", result.Pattern)
	}
}

// TestNativeGoSandboxBoundary proves the native (non-Codex) Go verification
// path enforces the same read-only-source, private-writable-temp, and
// no-network invariants as the Codex-launched sandbox. It exercises the real
// bubblewrap launcher end to end; it does not mock lookPath or run.
func TestNativeGoSandboxBoundary(t *testing.T) {
	if _, err := trustedLookPath("bwrap"); err != nil {
		t.Skipf("trusted bubblewrap installation not found: %v", err)
	}
	if _, err := trustedLookPath("go"); err != nil {
		t.Skipf("trusted Go toolchain not found: %v", err)
	}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/nativeboundary\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	source := `package nativeboundary

import (
    "net"
    "os"
    "path/filepath"
    "testing"
    "time"
)

func TestBoundary(t *testing.T) {
	if err := os.WriteFile("source-mutation", []byte("disposable"), 0600); err != nil {
		t.Fatalf("disposable verification directory was not writable: %v", err)
	}
	logDir := filepath.Join(".buckley", "logs")
	if err := os.MkdirAll(logDir, 0700); err != nil {
		t.Fatalf("repo-relative log directory was not writable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "review.jsonl"), []byte("ok"), 0600); err != nil {
		t.Fatalf("repo-relative log was not writable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(` + strconv.Quote(root) + `, "source-mutation"), []byte("forbidden"), 0600); err == nil {
		t.Fatal("immutable snapshot root was writable")
	}
    marker := filepath.Join(os.TempDir(), "private-temp-write")
    if err := os.WriteFile(marker, []byte("ok"), 0600); err != nil {
        t.Fatalf("private temp directory was not writable: %v", err)
    }
    // The private runtime directory must be the only writable location. A
    // hardcoded write to the shared /tmp root (outside os.TempDir(), which
    // this sandbox redirects to the private runtime dir) must fail.
    if err := os.WriteFile("/tmp/native-sandbox-escape-probe", []byte("forbidden"), 0600); err == nil {
        t.Fatal("the shared /tmp root was writable outside the private runtime directory")
    }
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("isolated loopback listener was unavailable: %v", err)
	}
	listener.Close()
    connection, err := net.DialTimeout("tcp", "1.1.1.1:53", 250*time.Millisecond)
    if err == nil {
        connection.Close()
        t.Fatal("sandbox unexpectedly had direct network access")
    }
}
`
	if err := os.WriteFile(filepath.Join(root, "boundary_test.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}

	result := NewExecutorWithCodexCommand("").Verify(context.Background(), Request{
		SnapshotRoot: root,
		Kind:         KindTest,
		Language:     LanguageGo,
		Pattern:      "^TestBoundary$",
		Timeout:      2 * time.Minute,
	})
	if result.Status != StatusPass || result.ExitCode != 0 {
		t.Fatalf("native sandbox boundary probe failed: status=%s exit=%d error=%s\nstdout:\n%s\nstderr:\n%s", result.Status, result.ExitCode, result.Error, result.Stdout, result.Stderr)
	}
	if _, err := os.Stat(filepath.Join(root, "source-mutation")); !os.IsNotExist(err) {
		t.Fatalf("sandbox mutated source snapshot: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".buckley")); !os.IsNotExist(err) {
		t.Fatalf("sandbox created repo-relative state in source snapshot: %v", err)
	}
}
