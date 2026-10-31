package main

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/commitmsg"
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
	if !opts.contextTrailer {
		t.Fatal("contextTrailer = false, want true by default")
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

// TestParseCommitCommandOptions_FlagsAfterFiles covers Go's flag package,
// which stops reading flags at the first file. A flag written after one used to
// be handed to git as another file name, so `buckley commit main.go -C /other`
// never reached /other.
func TestParseCommitCommandOptions_FlagsAfterFiles(t *testing.T) {
	t.Setenv(envCommitBackend, "")
	t.Setenv(envOneshotBackend, "")

	rejected := []struct {
		name     string
		args     []string
		wantFlag string
	}{
		{"-C after a file", []string{"main.go", "-C", "/other"}, `"-C"`},
		{"a boolean flag after the flags and a file", []string{"--yes", "main.go", "--dry-run"}, `"--dry-run"`},
		{"a flag after two files", []string{"a.go", "b.go", "-paths", "x"}, `"-paths"`},
	}
	for _, tc := range rejected {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			_, err := parseCommitCommandOptions(tc.args)
			if err == nil {
				t.Fatalf("parseCommitCommandOptions(%q) = nil error, want a rejection", tc.args)
			}
			for _, want := range []string{tc.wantFlag, "before", `"--"`} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %s", err, want)
				}
			}
		})
	}

	accepted := []struct {
		name      string
		args      []string
		wantFiles []string
	}{
		{"files after the flags", []string{"--yes", "-C", "/repo", "a.go", "b.go"}, []string{"a.go", "b.go"}},
		{"no files", []string{"--yes"}, nil},
		{"anything after -- is a file, even when it starts with a dash", []string{"--yes", "--", "-odd.go", "--dry-run"}, []string{"-odd.go", "--dry-run"}},
		{"a lone dash is a file name", []string{"a.go", "-"}, []string{"a.go", "-"}},
	}
	for _, tc := range accepted {
		t.Run("accepts "+tc.name, func(t *testing.T) {
			opts, err := parseCommitCommandOptions(tc.args)
			if err != nil {
				t.Fatalf("parseCommitCommandOptions(%q): %v", tc.args, err)
			}
			if len(opts.filesToStage) != len(tc.wantFiles) || (len(tc.wantFiles) > 0 && !reflect.DeepEqual(opts.filesToStage, tc.wantFiles)) {
				t.Fatalf("filesToStage = %#v, want %#v", opts.filesToStage, tc.wantFiles)
			}
		})
	}
}

func TestParseCommitCommandOptionsCanDisableContextTrailer(t *testing.T) {
	t.Setenv(envCommitBackend, "codex")
	opts, err := parseCommitCommandOptions([]string{"-context-trailer=false"})
	if err != nil {
		t.Fatalf("parseCommitCommandOptions() error = %v", err)
	}
	if opts.contextTrailer {
		t.Fatal("contextTrailer = true, want false")
	}
}

func TestCollectStagedChangeMetadataIsOpaque(t *testing.T) {
	repo := setupTwoAreaRepo(t)
	oldWd, _ := os.Getwd()
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	metadata, err := collectStagedChangeMetadata(nil)
	if err != nil {
		t.Fatalf("collectStagedChangeMetadata: %v", err)
	}
	if !metadata.Valid() || metadata.Files != 2 || metadata.Insertions != 2 || metadata.Deletions != 0 {
		t.Fatalf("unexpected metadata: %+v", metadata)
	}
	trailer := commitmsg.AppendChangeMetadata("add: staged files\n", metadata)
	if strings.Contains(trailer, "a/file.go") || strings.Contains(trailer, "package a") {
		t.Fatalf("metadata leaked staged details: %q", trailer)
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
