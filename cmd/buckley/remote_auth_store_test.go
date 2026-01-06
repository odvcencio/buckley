package main

import (
	"path/filepath"
	"testing"
)

func TestAuthStorePathDefaultsToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(envBuckleyRemoteAuthPath, "")
	t.Setenv(envBuckleyDataDir, "")

	got, err := authStorePath()
	if err != nil {
		t.Fatalf("authStorePath: %v", err)
	}
	want := filepath.Join(home, ".buckley", "remote-auth.json")
	if got != want {
		t.Fatalf("path=%q want %q", got, want)
	}
}

func TestAuthStorePathHonorsBuckleyDataDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(envBuckleyRemoteAuthPath, "")
	t.Setenv(envBuckleyDataDir, "~/data")

	got, err := authStorePath()
	if err != nil {
		t.Fatalf("authStorePath: %v", err)
	}
	want := filepath.Join(home, "data", "remote-auth.json")
	if got != want {
		t.Fatalf("path=%q want %q", got, want)
	}
}

func TestAuthStorePathHonorsExplicitPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(envBuckleyRemoteAuthPath, "~/custom/auth.json")
	t.Setenv(envBuckleyDataDir, "~/ignored")

	got, err := authStorePath()
	if err != nil {
		t.Fatalf("authStorePath: %v", err)
	}
	want := filepath.Join(home, "custom", "auth.json")
	if got != want {
		t.Fatalf("path=%q want %q", got, want)
	}
}
