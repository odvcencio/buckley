package policy

import "testing"

func TestClassifyShellCommand_AgentHarnessInvocations(t *testing.T) {
	for _, command := range []string{
		"buckley -p inspect",
		"/home/user/.local/bin/codex exec task",
		"env MODEL=x opencode run",
		"go test ./... && claude -p fix",
		"(aider --yes)",
		`"/opt/agent tools/codex" exec task`,
		"MODEL=x codex exec task",
		"env -i MODEL=x codex exec task",
		"sudo -u worker codex exec task",
		"timeout -s TERM 30s codex exec task",
		"nice -n 5 codex exec task",
		"command -- codex exec task",
		"exec -a worker codex exec task",
		"printf 'finished';codex exec task",
		"go test ./...\nclaude -p fix",
		"if true; then codex exec task; fi",
		"# explanatory comment\ncodex exec task",
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
		`printf 'first; codex task'`,
		`rg '&& codex exec' README.md`,
		`printf '%s' '(claude --help)'`,
		"command -v codex",
		"env --help codex",
		"sudo -v codex",
		"# example: go test; codex exec task",
		"printf done # example; codex exec task",
	} {
		if got := ClassifyShellCommand(command); got != "" {
			t.Errorf("ClassifyShellCommand(%q) = %q", command, got)
		}
	}
}
