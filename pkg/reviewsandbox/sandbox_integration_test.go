package reviewsandbox

import (
	"context"
	"os"
	"path/filepath"
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
    if err := os.WriteFile("source-mutation", []byte("forbidden"), 0600); err == nil {
        t.Fatal("immutable source directory was writable")
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

	result := NewExecutorWithCodexCommand(codexCommand).Verify(context.Background(), Request{
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
	if strings.TrimSpace(result.Pattern) != "^TestBoundary$" {
		t.Fatalf("trusted pattern not retained: %q", result.Pattern)
	}
}
