package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/commitmsg"
	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/oneshot/commands"
	"m31labs.dev/buckley/pkg/tools"
	"m31labs.dev/buckley/pkg/transparency"
)

type invalidCommitInvoker struct {
	calls int
}

func (i *invalidCommitInvoker) Invoke(context.Context, string, string, tools.Definition, *transparency.ContextAudit) (*oneshot.Result, *transparency.Trace, error) {
	i.calls++
	return &oneshot.Result{ToolCall: &tools.ToolCall{
		Name:      "generate_commit",
		Arguments: json.RawMessage(`{"action":"invalid","subject":"bad","body":[]}`),
	}}, nil, nil
}

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

func TestParseCommitCommandOptions_APIPreservesExplicitModelAndDefaultPush(t *testing.T) {
	t.Setenv(envCommitBackend, "")
	t.Setenv(envOneshotBackend, "")

	opts, err := parseCommitCommandOptions([]string{
		"-backend", "api",
		"-model", "openai/gpt-5.6-luna-pro",
	})
	if err != nil {
		t.Fatalf("parseCommitCommandOptions() error = %v", err)
	}
	if opts.backend != oneshotBackendAPI || opts.model != "openai/gpt-5.6-luna-pro" {
		t.Fatalf("backend/model = %q/%q, want api/openai/gpt-5.6-luna-pro", opts.backend, opts.model)
	}
	if !opts.push {
		t.Fatal("push = false, want normal default push semantics")
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

func TestFrameworkCommitRunner_APIValidationDoesNotRetry(t *testing.T) {
	invoker := &invalidCommitInvoker{}
	runner := &frameworkCommitRunner{
		framework:  oneshot.NewFramework(invoker, nil),
		def:        commands.CommitDefinition{},
		maxRetries: 1,
	}
	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result == nil || result.Error == nil {
		t.Fatalf("result = %#v, want validation failure", result)
	}
	if invoker.calls != 1 {
		t.Fatalf("invocations = %d, want exactly one", invoker.calls)
	}
}
