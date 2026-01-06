package main

import (
	"path/filepath"
	"testing"
)

func TestResolveDBPathDefaultsToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(envBuckleyDBPath, "")
	t.Setenv(envBuckleyDataDir, "")

	got, err := resolveDBPath()
	if err != nil {
		t.Fatalf("resolveDBPath: %v", err)
	}
	want := filepath.Join(home, ".buckley", "buckley.db")
	if got != want {
		t.Fatalf("dbPath=%q want %q", got, want)
	}
}

func TestResolveDBPathHonorsBuckleyDBPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	t.Setenv(envBuckleyDBPath, "~/custom/buckley.db")
	t.Setenv(envBuckleyDataDir, "~/ignored")

	got, err := resolveDBPath()
	if err != nil {
		t.Fatalf("resolveDBPath: %v", err)
	}
	want := filepath.Join(home, "custom", "buckley.db")
	if got != want {
		t.Fatalf("dbPath=%q want %q", got, want)
	}
}

func TestResolveDBPathHonorsBuckleyDataDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(envBuckleyDBPath, "")
	t.Setenv(envBuckleyDataDir, "~/data")

	got, err := resolveDBPath()
	if err != nil {
		t.Fatalf("resolveDBPath: %v", err)
	}
	want := filepath.Join(home, "data", "buckley.db")
	if got != want {
		t.Fatalf("dbPath=%q want %q", got, want)
	}
}

func TestResolveACPEventsDBPathDefaultsToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(envBuckleyACPEventsDBPath, "")
	t.Setenv(envBuckleyDataDir, "")

	got, err := resolveACPEventsDBPath()
	if err != nil {
		t.Fatalf("resolveACPEventsDBPath: %v", err)
	}
	want := filepath.Join(home, ".buckley", "buckley-acp-events.db")
	if got != want {
		t.Fatalf("acpEventsDBPath=%q want %q", got, want)
	}
}

func TestResolveACPEventsDBPathHonorsExplicitPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(envBuckleyACPEventsDBPath, "~/data/acp.db")
	t.Setenv(envBuckleyDataDir, "~/ignored")

	got, err := resolveACPEventsDBPath()
	if err != nil {
		t.Fatalf("resolveACPEventsDBPath: %v", err)
	}
	want := filepath.Join(home, "data", "acp.db")
	if got != want {
		t.Fatalf("acpEventsDBPath=%q want %q", got, want)
	}
}

func TestResolveACPEventsDBPathHonorsBuckleyDataDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(envBuckleyACPEventsDBPath, "")
	t.Setenv(envBuckleyDataDir, "~/data")

	got, err := resolveACPEventsDBPath()
	if err != nil {
		t.Fatalf("resolveACPEventsDBPath: %v", err)
	}
	want := filepath.Join(home, "data", "buckley-acp-events.db")
	if got != want {
		t.Fatalf("acpEventsDBPath=%q want %q", got, want)
	}
}
