package main

import (
	"strings"
	"testing"
)

func TestGitCommandEnvOverridesNonInteractive(t *testing.T) {
	oldTerminal := stdinIsTerminalFn
	stdinIsTerminalFn = func() bool { return false }
	t.Cleanup(func() { stdinIsTerminalFn = oldTerminal })

	env := gitCommandEnv([]string{
		"PATH=/bin",
		"GIT_TERMINAL_PROMPT=1",
	})

	got := envSliceToMap(env)
	if got["GIT_TERMINAL_PROMPT"] != "0" {
		t.Fatalf("GIT_TERMINAL_PROMPT=%q want %q", got["GIT_TERMINAL_PROMPT"], "0")
	}
	if got["GCM_INTERACTIVE"] != "never" {
		t.Fatalf("GCM_INTERACTIVE=%q want %q", got["GCM_INTERACTIVE"], "never")
	}
	if got["GIT_SSH_COMMAND"] != "ssh -o BatchMode=yes" {
		t.Fatalf("GIT_SSH_COMMAND=%q want %q", got["GIT_SSH_COMMAND"], "ssh -o BatchMode=yes")
	}
	for _, pair := range env {
		if strings.HasPrefix(pair, "GIT_TERMINAL_PROMPT=") && pair != "GIT_TERMINAL_PROMPT=0" {
			t.Fatalf("unexpected env pair %q", pair)
		}
	}
}

func TestGitCommandEnvRespectsExistingGitSSHCommand(t *testing.T) {
	oldTerminal := stdinIsTerminalFn
	stdinIsTerminalFn = func() bool { return false }
	t.Cleanup(func() { stdinIsTerminalFn = oldTerminal })

	env := gitCommandEnv([]string{
		"PATH=/bin",
		"GIT_SSH_COMMAND=custom ssh",
	})

	got := envSliceToMap(env)
	if got["GIT_SSH_COMMAND"] != "custom ssh" {
		t.Fatalf("GIT_SSH_COMMAND=%q want %q", got["GIT_SSH_COMMAND"], "custom ssh")
	}
}

func envSliceToMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, pair := range env {
		key, val, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		out[key] = val
	}
	return out
}
