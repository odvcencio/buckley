package main

import (
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/oneshot/commands"
)

func TestParseCommitCommandOptions(t *testing.T) {
	t.Setenv(envCommitBackend, "")
	t.Setenv(envOneshotBackend, "")
	t.Setenv("BUCKLEY_USE_GRAFT", "")
	t.Setenv("BUCKLEY_MINIMAL_OUTPUT", "")

	opts, err := parseCommitCommandOptions([]string{
		"-dry-run",
		"-yes",
		"-push=false",
		"-verbose",
		"-minimal-output",
		"-trace",
		"-cost=false",
		"-model", "test/commit",
		"-backend", "codex",
		"-timeout", "15s",
		"-paths", "a",
		"-paths", "b/sub",
		"-exclusive",
		"--",
		"a/file.go",
	})
	if err != nil {
		t.Fatalf("parseCommitCommandOptions() error = %v", err)
	}

	if !opts.dryRun || !opts.yes || opts.push || !opts.verbose || !opts.trace || opts.showCost {
		t.Fatalf("unexpected bool options: %+v", opts)
	}
	if !opts.compactOutput {
		t.Fatal("compactOutput = false, want true")
	}
	if opts.useGraft {
		t.Fatal("useGraft = true, want false")
	}
	if opts.model != "test/commit" {
		t.Fatalf("model = %q, want test/commit", opts.model)
	}
	if opts.backend != oneshot.CLIBackendCodex {
		t.Fatalf("backend = %q, want codex", opts.backend)
	}
	if opts.timeout != 15*time.Second {
		t.Fatalf("timeout = %v, want 15s", opts.timeout)
	}
	if len(opts.paths) != 2 || opts.paths[0] != "a" || opts.paths[1] != "b/sub" {
		t.Fatalf("paths = %#v, want [a b/sub]", opts.paths)
	}
	if !opts.exclusive {
		t.Fatal("exclusive = false, want true")
	}
	if len(opts.filesToStage) != 1 || opts.filesToStage[0] != "a/file.go" {
		t.Fatalf("filesToStage = %#v, want [a/file.go]", opts.filesToStage)
	}
}

func TestParseCommitCommandOptionsHonorsEnvironment(t *testing.T) {
	t.Setenv(envCommitBackend, "claude")
	t.Setenv("BUCKLEY_USE_GRAFT", "1")
	t.Setenv("BUCKLEY_MINIMAL_OUTPUT", "true")

	opts, err := parseCommitCommandOptions(nil)
	if err != nil {
		t.Fatalf("parseCommitCommandOptions() error = %v", err)
	}

	if opts.backend != oneshot.CLIBackendClaude {
		t.Fatalf("backend = %q, want claude", opts.backend)
	}
	if !opts.useGraft {
		t.Fatal("useGraft = false, want true from BUCKLEY_USE_GRAFT")
	}
	if !opts.compactOutput {
		t.Fatal("compactOutput = false, want true from BUCKLEY_MINIMAL_OUTPUT")
	}
}

func TestCommitDefinition(t *testing.T) {
	if _, ok := commitDefinition(nil).(commands.CommitDefinition); !ok {
		t.Fatalf("commitDefinition(nil) = %T, want commands.CommitDefinition", commitDefinition(nil))
	}

	scoped, ok := commitDefinition([]string{"a"}).(scopedCommitDefinition)
	if !ok {
		t.Fatalf("commitDefinition(paths) = %T, want scopedCommitDefinition", commitDefinition([]string{"a"}))
	}
	if len(scoped.paths) != 1 || scoped.paths[0] != "a" {
		t.Fatalf("scoped paths = %#v, want [a]", scoped.paths)
	}
}
