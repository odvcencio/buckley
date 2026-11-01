package main

import (
	"path/filepath"
	"testing"
)

// isolateBuckleyHome points every Buckley state path a test could reach (the
// config and trust files under ~/.buckley, the data directory, and the ACP
// event database) at a temporary home, so no test opens the operator's own.
func isolateBuckleyHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(envBuckleyDataDir, "")
	t.Setenv(envBuckleyACPEventsDBPath, filepath.Join(home, "acp-events.db"))
	return home
}
