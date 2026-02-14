package main

import "testing"

func TestOneshotMinimalOutputEnabled_RespectsQuietFlag(t *testing.T) {
	prevQuiet := cliFlags.quiet
	t.Cleanup(func() {
		cliFlags.quiet = prevQuiet
	})
	cliFlags.quiet = true
	t.Setenv(oneshotMinimalOutputEnv, "0")

	if !oneshotMinimalOutputEnabled() {
		t.Fatalf("expected minimal output to be enabled when quiet flag is set")
	}
}

func TestOneshotMinimalOutputEnabled_RespectsEnv(t *testing.T) {
	prevQuiet := cliFlags.quiet
	t.Cleanup(func() {
		cliFlags.quiet = prevQuiet
	})
	cliFlags.quiet = false
	t.Setenv(oneshotMinimalOutputEnv, "1")

	if !oneshotMinimalOutputEnabled() {
		t.Fatalf("expected minimal output to be enabled when env var is true")
	}
}

func TestOneshotMinimalOutputEnabled_DefaultFalse(t *testing.T) {
	prevQuiet := cliFlags.quiet
	t.Cleanup(func() {
		cliFlags.quiet = prevQuiet
	})
	cliFlags.quiet = false
	t.Setenv(oneshotMinimalOutputEnv, "")

	if oneshotMinimalOutputEnabled() {
		t.Fatalf("expected minimal output to be disabled by default")
	}
}
