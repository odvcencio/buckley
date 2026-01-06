package storage

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestNew_CreatesPrivateSQLiteFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode bits are not stable on Windows")
	}

	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "nested", "buckley.db")

	store, err := New(dbPath)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_ = store.Close()

	info, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat db: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("db perms = %o, want 600", got)
	}

	dirInfo, err := os.Stat(filepath.Dir(dbPath))
	if err != nil {
		t.Fatalf("stat db dir: %v", err)
	}
	if got := dirInfo.Mode().Perm() & 0o077; got != 0 {
		t.Fatalf("db dir perms include group/other bits: %o", dirInfo.Mode().Perm())
	}
}
