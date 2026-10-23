package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"m31labs.dev/buckley/pkg/storage"
)

func TestGlobsOverlap(t *testing.T) {
	tests := []struct {
		name      string
		requested []string
		declared  []string
		want      bool
	}{
		{"same glob", []string{"client/**"}, []string{"client/**"}, true},
		{"requested contains declared", []string{"pkg/**"}, []string{"pkg/sub/**"}, true},
		{"declared contains requested subdirectory", []string{"pkg/sub/**"}, []string{"pkg/**"}, true},
		{"declared glob matches a requested file", []string{"pkg/sub/file.go"}, []string{"pkg/**"}, true},
		{"requested glob matches a declared file", []string{"pkg/**"}, []string{"pkg/sub/file.go"}, true},
		{"blank globs never match", []string{" "}, []string{"pkg/**", ""}, false},
		{"disjoint trees", []string{"cmd/**"}, []string{"pkg/**"}, false},
		{"sibling with a shared name prefix", []string{"pkg/sub/**"}, []string{"pkg/subway/**"}, false},
		{"nothing requested overlaps nothing declared", []string{"pkg/**"}, nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := globsOverlap(tc.requested, tc.declared); got != tc.want {
				t.Fatalf("globsOverlap(%v, %v) = %v, want %v", tc.requested, tc.declared, got, tc.want)
			}
		})
	}
}

func TestOpenVerifyLedger_UsesConfiguredStorageBudget(t *testing.T) {
	t.Setenv(envBuckleyDataDir, t.TempDir())
	old := configPath
	t.Cleanup(func() { configPath = old })
	configPath = filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("resources:\n  storage_bytes: 16777216\n"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := openVerifyLedger()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	budget, err := storage.ReadStorageBudget(context.Background(), store.DB())
	if err != nil || budget.LimitBytes != 16<<20 {
		t.Fatalf("budget=%+v err=%v", budget, err)
	}
}
