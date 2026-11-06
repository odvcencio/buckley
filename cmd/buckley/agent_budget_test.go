package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestParseAgentRunArgs_CostAndRequestCaps(t *testing.T) {
	opts, err := parseAgentRunArgs([]string{"--budget", "0.05", "--max-model-requests", "6", "agent.yaml", "worker", "fix the function"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.maxCostUSD != 0.05 || opts.maxModelRequests != 6 {
		t.Fatalf("caps = $%v / %v requests", opts.maxCostUSD, opts.maxModelRequests)
	}
	preview := buildAgentRunPreviewSnapshot(opts, nil)
	if preview.MaxCostUSD != 0.05 || preview.MaxModelRequests != 6 {
		t.Fatalf("dry-run omitted caps: %#v", preview)
	}
	for _, flag := range []string{"--budget=-1", "--budget=NaN", "--budget=Inf", "--max-model-requests=-1"} {
		if _, err := parseAgentRunArgs([]string{flag, "agent.yaml", "worker", "fix"}); err == nil {
			t.Errorf("accepted invalid cap %s", flag)
		}
	}
}

func TestAgentRun_ExplicitShellOptInIsNarrow(t *testing.T) {
	opts, err := parseAgentRunArgs([]string{"--allow-shell", "--budget", "0.05", "agent.yaml", "worker", "verify"})
	if err != nil || !opts.allowShell || !buildAgentRunPreviewSnapshot(opts, nil).AllowShell {
		t.Fatalf("lost shell opt-in: %#v, %v", opts, err)
	}
	registry := tool.NewEmptyRegistry()
	registry.Register(&builtin.ShellCommandTool{})
	registry.Register(&builtin.DynamicCodeTool{})
	if slices.Contains(acpFallbackAllowedToolNames(registry, nil), "run_shell") {
		t.Fatal("offered shell without explicit opt-in")
	}
	names := acpFallbackAllowedToolNames(registry, nil, true)
	if !slices.Contains(names, "run_shell") || slices.Contains(names, "run_code") {
		t.Fatalf("opt-in broadened unrelated tools: %v", names)
	}
	if len(acpFallbackAllowedToolNames(registry, []string{}, true)) != 0 {
		t.Fatal("opt-in escaped an explicit empty tool filter")
	}
}

func TestAgentRun_ShellFlagDoesNotWidenProfile(t *testing.T) {
	t.Setenv("BUCKLEY_CHILD_CONTRACT", "")
	spec := filepath.Join(t.TempDir(), "agent.yaml")
	const content = `version: buckley.agent/v1
name: scoped-test
tools:
  tier: full
  allow: [read_file]
subagents:
  - name: worker
`
	if err := os.WriteFile(spec, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runAgentRun([]string{"--dry-run", "--allow-shell", spec, "worker", "inspect"})
	if err == nil || !strings.Contains(err.Error(), "requires run_shell in the agent's allowed tools") {
		t.Fatalf("shell flag did not respect the profile: %v", err)
	}
}

func TestApplyAgentRunExplicitLimits_CannotRaiseChildSpend(t *testing.T) {
	base := acpLoopLimits{MaxCostUSD: 0.02, MaxModelRequests: 4}
	got := applyAgentRunExplicitLimits(base, agentRunOptions{maxCostUSD: 0.05, maxModelRequests: 10})
	if got.MaxCostUSD != 0.02 || got.MaxModelRequests != 4 {
		t.Fatalf("loosened child caps: %#v", got)
	}
	got = applyAgentRunExplicitLimits(base, agentRunOptions{maxCostUSD: 0.01, maxModelRequests: 2})
	if got.MaxCostUSD != 0.01 || got.MaxModelRequests != 2 {
		t.Fatalf("did not tighten caps: %#v", got)
	}
	got = applyAgentRunExplicitLimits(acpLoopLimits{}, agentRunOptions{maxCostUSD: 0.05, maxModelRequests: 6})
	if got.MaxCostUSD != 0.05 || got.MaxModelRequests != 6 {
		t.Fatalf("did not install caps: %#v", got)
	}
}
