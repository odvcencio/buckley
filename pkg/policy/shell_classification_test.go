package policy

import "testing"

func TestClassifyShellCommand_AgentHarnessInvocations(t *testing.T) {
	for _, command := range []string{
		"buckley -p inspect",
		"/home/user/.local/bin/codex exec task",
		"env MODEL=x opencode run",
		"go test ./... && claude -p fix",
		"(aider --yes)",
	} {
		if got := ClassifyShellCommand(command); got != CommandClassAgentHarness {
			t.Errorf("ClassifyShellCommand(%q) = %q", command, got)
		}
	}
}

func TestClassifyShellCommand_ReferencesAreNotInvocations(t *testing.T) {
	for _, command := range []string{
		"go test ./cmd/buckley",
		"rg buckley pkg",
		"printf 'use codex for this task'",
		"git diff -- pkg/tool/builtin/delegate.go",
	} {
		if got := ClassifyShellCommand(command); got != "" {
			t.Errorf("ClassifyShellCommand(%q) = %q", command, got)
		}
	}
}
