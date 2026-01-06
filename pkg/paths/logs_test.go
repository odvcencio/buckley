package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuckleyLogsBaseDirDefaultsToRelativePath(t *testing.T) {
	t.Setenv(EnvBuckleyLogDir, "")
	if got := BuckleyLogsBaseDir(); got != filepath.Join(".buckley", "logs") {
		t.Fatalf("unexpected base logs dir: %q", got)
	}
}

func TestBuckleyLogsBaseDirExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(EnvBuckleyLogDir, "~/buckley/logs")
	want := filepath.Join(home, "buckley", "logs")
	if got := BuckleyLogsBaseDir(); got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestBuckleyLogsBaseDirSupportsBareHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(EnvBuckleyLogDir, "~")
	if got := BuckleyLogsBaseDir(); got != home {
		t.Fatalf("expected %q, got %q", home, got)
	}
}

func TestBuckleyLogsBaseDirForWorkdirAnchorsRelative(t *testing.T) {
	t.Setenv(EnvBuckleyLogDir, "relative/logs")
	workdir := t.TempDir()
	want := filepath.Join(workdir, "relative", "logs")
	if got := BuckleyLogsBaseDirForWorkdir(workdir); got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestBuckleyLogsBaseDirForWorkdirDoesNotAnchorAbsolute(t *testing.T) {
	workdir := t.TempDir()
	abs := filepath.Join(os.TempDir(), "buckley-logs")
	t.Setenv(EnvBuckleyLogDir, abs)
	if got := BuckleyLogsBaseDirForWorkdir(workdir); got != abs {
		t.Fatalf("expected %q, got %q", abs, got)
	}
}
