package main

import (
	"reflect"
	"testing"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/agentspec"
	"m31labs.dev/buckley/pkg/tool"
)

func hostToolTestRegistry() *tool.Registry {
	registry := tool.NewEmptyRegistry()
	for _, name := range []string{"read_file", "write_file", "edit_file", "run_shell", "git_status", "spawn_subagent"} {
		impact := tool.ImpactModifying
		if name == "run_shell" {
			impact = tool.ImpactDestructive
		} else if name == "read_file" || name == "git_status" {
			impact = tool.ImpactReadOnly
		}
		registry.Register(&acpStateTestTool{name: name, metadata: tool.ToolMetadata{Impact: impact}})
	}
	return registry
}

func TestEnsureHostMutationToolsRestoresShellFileAndGit(t *testing.T) {
	registry := hostToolTestRegistry()
	got := ensureHostMutationTools([]string{"read_file", "search_text"}, registry)
	for _, want := range []string{"run_shell", "git_status", "write_file", "edit_file"} {
		found := false
		for _, name := range got {
			found = found || name == want
		}
		if !found {
			t.Fatalf("filter %v is missing %s", got, want)
		}
	}
	if ensureHostMutationTools(nil, registry) != nil {
		t.Fatal("nil filter must stay unrestricted")
	}
}

func TestActionWindowKeepsShellForHostLanes(t *testing.T) {
	registry := hostToolTestRegistry()
	without := buildACPToolTurn(registry, nil, nil, true, true, true, agentloop.MutationIntent)
	if reflect.DeepEqual(without.AllowedTools, nil) || containsString(without.AllowedTools, "run_shell") {
		t.Fatalf("non-host action window = %v", without.AllowedTools)
	}
	with := buildACPToolTurn(registry, nil, nil, true, true, true, agentloop.MutationIntent, true)
	for _, want := range []string{"run_shell", "git_status", "write_file"} {
		if !containsString(with.AllowedTools, want) {
			t.Fatalf("host action window %v is missing %s", with.AllowedTools, want)
		}
	}
}

func TestRemoveImplicitSubagentDelegation(t *testing.T) {
	registry := hostToolTestRegistry()
	removeImplicitSubagentDelegation(registry, &agentspec.RuntimeProfile{}, nil)
	if _, ok := registry.Get("spawn_subagent"); !ok {
		t.Fatal("a lane with an agent profile keeps delegation")
	}
	removeImplicitSubagentDelegation(registry, nil, []string{"spawn_subagent"})
	if _, ok := registry.Get("spawn_subagent"); !ok {
		t.Fatal("an explicit allowlist keeps delegation")
	}
	removeImplicitSubagentDelegation(registry, nil, nil)
	if _, ok := registry.Get("spawn_subagent"); ok {
		t.Fatal("a profile-less lane must not delegate")
	}
}

func TestBriefForbidsChanges(t *testing.T) {
	yes := []string{
		"Run go vet and write the findings. Do not make any file changes.",
		"This is a report-only task.",
		"Verify the build with no file changes.",
		"Answer without making changes.",
		"You must not modify any files.",
	}
	no := []string{
		"Never edit the owner's checkout. Fix the parser bug and add tests.",
		"Do not commit specs or plans to the repo. Implement feature X.",
		"Commit only with buckley commit. NEVER use git stash.",
	}
	for _, text := range yes {
		if !briefForbidsChanges(text) {
			t.Errorf("should forbid changes: %q", text)
		}
	}
	for _, text := range no {
		if briefForbidsChanges(text) {
			t.Errorf("should not forbid changes: %q", text)
		}
	}
}

func TestReportOnlyBriefDropsObservableChangeRequirement(t *testing.T) {
	limits := acpLoopLimits{TaskIntent: agentloop.MutationIntent, MaxContinuations: 200, noChangeExpected: true}
	if contract := acpCompletionContract(limits); contract == nil || contract.RequireObservableChange {
		t.Fatalf("contract = %+v", contract)
	}
	limits.noChangeExpected = false
	if contract := acpCompletionContract(limits); contract == nil || !contract.RequireObservableChange {
		t.Fatalf("contract = %+v", contract)
	}
}
