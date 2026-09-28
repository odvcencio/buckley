package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEvalCommitOfflinePassesOnBuiltInCases(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := runEvalCommitCommand(nil); err != nil {
		t.Fatalf("built-in cases must pass: %v", err)
	}
	if err := runEvalCommitCommand([]string{"--case", "synth-rename-go", "--json"}); err != nil {
		t.Fatal(err)
	}
	if err := runEvalCommitCommand([]string{"--case", "no-such-case"}); err == nil {
		t.Fatal("unknown case must fail")
	}
}

func TestEvalCommitFailsWhenAGoldenLeaks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	diff := "diff --git a/a.yaml b/a.yaml\n--- a/a.yaml\n+++ b/a.yaml\n@@\n-owner: zorblax-prod\n+owner: example-prod\n"
	os.WriteFile(filepath.Join(dir, "bad.diff"), []byte(diff), 0o644)
	os.WriteFile(filepath.Join(dir, "bad.json"), []byte(`{"name":"bad","kind":"rename","diff":"bad.diff","planted":["zorblax"],"golden":"update: rename zorblax-prod\n\n- Done.\n"}`), 0o644)
	if err := runEvalCommitCommand([]string{"--cases", dir}); err == nil {
		t.Fatal("a golden message that leaks must fail the gate")
	}
}
