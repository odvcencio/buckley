package main

import (
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/agentloop"
)

func TestDecideOneShotShell(t *testing.T) {
	trusted := projectTrustTrusted
	tests := []struct {
		name      string
		req       oneShotShellRequest
		wantAllow bool
		wantDrop2 []string // substrings of the drop reason
		wantDrop  string
	}{
		{"not requested gives no shell", oneShotShellRequest{Intent: agentloop.MutationIntent, Trust: trusted}, false, nil, ""},
		{"trusted mutation named in --tools", oneShotShellRequest{Tools: []string{"read_file", "run_shell"}, Intent: agentloop.MutationIntent, Trust: trusted}, true, nil, ""},
		{"trusted mutation with --allow-shell", oneShotShellRequest{Flag: true, Intent: agentloop.MutationIntent, Trust: trusted}, true, nil, ""},
		{"trusted mutation with oneshot.allow_shell", oneShotShellRequest{ConfigAllow: true, Intent: agentloop.MutationIntent, Trust: trusted}, true, nil, ""},
		{"restricted project refuses and says how to enable", oneShotShellRequest{Tools: []string{"run_shell"}, Intent: agentloop.MutationIntent, Trust: projectTrustRestricted},
			false, []string{"not trusted", "buckley trust allow"}, ""},
		{"unknown trust refuses", oneShotShellRequest{Flag: true, Intent: agentloop.MutationIntent, Trust: projectTrustUnknown},
			false, []string{"buckley trust allow"}, ""},
		{"read-only intent drops it", oneShotShellRequest{Flag: true, Intent: agentloop.ReadOnlyIntent, Trust: trusted}, false, nil, "--task-intent mutation"},
		{"approval deny list wins", oneShotShellRequest{Flag: true, Intent: agentloop.MutationIntent, Trust: trusted, DeniedTools: []string{"run_shell"}},
			false, []string{"approval.denied_tools"}, ""},
		{"unrestricted host lane already has shell", oneShotShellRequest{HostTools: true, Intent: agentloop.MutationIntent, Trust: projectTrustRestricted}, true, nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := decideOneShotShell(tc.req)
			if got.Allow != tc.wantAllow {
				t.Fatalf("Allow = %v, want %v (%+v)", got.Allow, tc.wantAllow, got)
			}
			for _, want := range tc.wantDrop2 {
				if !strings.Contains(got.DropReason, want) {
					t.Errorf("drop reason %q lacks %q", got.DropReason, want)
				}
			}
			if tc.wantDrop != "" && !strings.Contains(got.DropReason, tc.wantDrop) {
				t.Fatalf("drop reason %q lacks %q", got.DropReason, tc.wantDrop)
			}
			if !tc.wantAllow && got.Source != "" && got.DropReason == "" {
				t.Fatalf("a refused request gave no reason: %+v", got)
			}
		})
	}
}

// With shell granted, the model is offered run_shell; without, the default
// no-approver policy still hides it.
func TestToolTurnOffersShellOnlyWhenGranted(t *testing.T) {
	registry := hostToolTestRegistry()
	without := buildACPToolTurn(registry, nil, nil, true, false, false, agentloop.MutationIntent, false, false)
	if containsString(without.AllowedTools, "run_shell") {
		t.Fatalf("shell offered without a grant: %v", without.AllowedTools)
	}
	with := buildACPToolTurn(registry, nil, nil, true, false, false, agentloop.MutationIntent, false, true)
	if !containsString(with.AllowedTools, "run_shell") || !containsString(with.AllowedTools, "write_file") {
		t.Fatalf("granted shell missing from %v", with.AllowedTools)
	}
	// The action window keeps it as well.
	action := buildACPToolTurn(registry, nil, nil, true, false, true, agentloop.MutationIntent, false, true)
	if !containsString(action.AllowedTools, "run_shell") {
		t.Fatalf("action window dropped granted shell: %v", action.AllowedTools)
	}
}

func TestDroppedRequestedTools(t *testing.T) {
	registry := hostToolTestRegistry()
	got := droppedRequestedTools(registry, []string{"read_file", "run_shell", "write_file"}, oneShotShellDecision{DropReason: "needs shell_exec; project is trusted but shell was not requested"}, false)
	if len(got) != 1 || !strings.HasPrefix(got[0], "run_shell (") || !strings.Contains(got[0], "shell_exec") {
		t.Fatalf("dropped = %v", got)
	}
	if got := droppedRequestedTools(registry, []string{"read_file", "run_shell"}, oneShotShellDecision{Allow: true}, false); len(got) != 0 {
		t.Fatalf("a granted shell was reported dropped: %v", got)
	}
	// An unrestricted host lane keeps every tool.
	if got := droppedRequestedTools(registry, []string{"run_shell"}, oneShotShellDecision{}, true); len(got) != 0 {
		t.Fatalf("host lane dropped %v", got)
	}
}

func TestParseStartupOptions_AllowShellIsOptIn(t *testing.T) {
	off, err := parseStartupOptions([]string{"-p", "hi"})
	if err != nil || off.allowShell {
		t.Fatalf("shell on by default: %+v err=%v", off, err)
	}
	on, err := parseStartupOptions([]string{"--allow-shell", "--task-intent", "mutation", "-p", "hi"})
	if err != nil || !on.allowShell {
		t.Fatalf("--allow-shell not parsed: %+v err=%v", on, err)
	}
}
