package main

import (
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/oneshot/commands"
	"m31labs.dev/buckley/pkg/transparency"
)

func TestParsePRCommandOptions(t *testing.T) {
	opts, err := parsePRCommandOptions([]string{
		"-dry-run",
		"-yes",
		"-push=false",
		"-verbose",
		"-cost=false",
		"-base=develop",
		"-model=openai/test-pr",
		"-backend=codex",
		"-timeout=15s",
	})
	if err != nil {
		t.Fatalf("parsePRCommandOptions: %v", err)
	}

	if !opts.dryRun {
		t.Fatal("dryRun = false, want true")
	}
	if !opts.yes {
		t.Fatal("yes = false, want true")
	}
	if opts.push {
		t.Fatal("push = true, want false")
	}
	if !opts.verbose {
		t.Fatal("verbose = false, want true")
	}
	if opts.showCost {
		t.Fatal("showCost = true, want false")
	}
	if opts.base != "develop" {
		t.Fatalf("base = %q, want develop", opts.base)
	}
	if opts.model != "openai/test-pr" {
		t.Fatalf("model = %q, want openai/test-pr", opts.model)
	}
	if opts.backend != oneshot.CLIBackendCodex {
		t.Fatalf("backend = %q, want codex", opts.backend)
	}
	if opts.timeout != 15*time.Second {
		t.Fatalf("timeout = %s, want 15s", opts.timeout)
	}
}

func TestParsePRCommandOptionsHonorsEnvironment(t *testing.T) {
	t.Setenv(envPRBackend, "claude")

	opts, err := parsePRCommandOptions(nil)
	if err != nil {
		t.Fatalf("parsePRCommandOptions: %v", err)
	}

	if opts.backend != oneshot.CLIBackendClaude {
		t.Fatalf("backend = %q, want claude", opts.backend)
	}
	if opts.push != true {
		t.Fatal("push = false, want true")
	}
	if opts.showCost != true {
		t.Fatal("showCost = false, want true")
	}
	if opts.timeout != 2*time.Minute {
		t.Fatalf("timeout = %s, want 2m", opts.timeout)
	}
}

func TestPRRunResultFromFramework(t *testing.T) {
	pr := &commands.PRResult{
		Title:   "tighten pr command",
		Summary: "Split command wiring into smaller helpers.",
		Changes: []string{
			"Parse flags separately",
		},
		Testing: []string{
			"go test ./cmd/buckley",
		},
	}
	trace := &transparency.Trace{Reasoning: "reasoning"}
	audit := transparency.NewContextAudit()

	result := prRunResultFromFramework(&oneshot.RunResult{
		Value:        pr,
		Trace:        trace,
		ContextAudit: audit,
	})

	if result.PR != pr {
		t.Fatal("PR result was not preserved")
	}
	if result.Trace != trace {
		t.Fatal("trace was not preserved")
	}
	if result.ContextAudit != audit {
		t.Fatal("context audit was not preserved")
	}
	if result.Error != nil {
		t.Fatalf("Error = %v, want nil", result.Error)
	}
}

func TestPRRunResultFromFrameworkRejectsUnexpectedValue(t *testing.T) {
	result := prRunResultFromFramework(&oneshot.RunResult{Value: "not a pr"})

	if result.PR != nil {
		t.Fatal("PR result should be nil for unexpected value")
	}
	if result.Error == nil {
		t.Fatal("expected unexpected result type error")
	}
	if !strings.Contains(result.Error.Error(), "unexpected result type") {
		t.Fatalf("error = %q, want unexpected result type", result.Error)
	}
}
