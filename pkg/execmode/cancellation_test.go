package execmode

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunnerCancellation_BeforeSetup(t *testing.T) {
	runner, err := NewRunner(t.TempDir(), &recordingSink{}, time.Minute, WithoutIsolation())
	if err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(t.TempDir(), "not-created")
	t.Setenv("TMPDIR", scratch)
	t.Setenv("TMP", scratch)
	t.Setenv("TEMP", scratch)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name string
		run  func(context.Context, string) (Result, error)
	}{
		{"go", runner.Run},
		{"fw", runner.RunFW},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tc.run(ctx, "package main\nfunc main(){}")
			if !errors.Is(err, context.Canceled) || result.ExitCode != -1 {
				t.Fatalf("canceled before setup: result=%+v error=%v", result, err)
			}
			if _, err := os.Stat(scratch); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("scratch created: %v", err)
			}
		})
	}
}
