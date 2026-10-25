package main

import (
	"fmt"
	"slices"
	"strings"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/tool"
)

// oneShotShellRequest is everything that decides whether a one-shot run gets
// run_shell.
type oneShotShellRequest struct {
	Flag        bool     // --allow-shell
	ConfigAllow bool     // oneshot.allow_shell
	Tools       []string // --tools
	Intent      agentloop.TaskIntent
	Trust       projectTrustStatus
	DeniedTools []string // approval.denied_tools
	HostTools   bool     // an unrestricted yolo lane already runs host tools
}

type oneShotShellDecision struct {
	Allow  bool
	Source string // what asked for it, for the trace header
	// DropReason explains why a request was set aside without failing the run.
	DropReason string
}

// decideOneShotShell grants run_shell to a one-shot run only when the caller
// asked for it, the intent is mutation, and the project is trusted. A one-shot
// run has no approver, and the default policy hides destructive tools from it,
// so shell is opt-in. Granting it only lifts that default: the sandbox, the
// approval middleware and approval.denied_tools still apply to every call.
func decideOneShotShell(req oneShotShellRequest) oneShotShellDecision {
	if req.HostTools {
		return oneShotShellDecision{Allow: true, Source: "unrestricted host lane"}
	}
	source := ""
	switch {
	case slices.Contains(req.Tools, "run_shell"):
		source = "--tools"
	case req.Flag:
		source = "--allow-shell"
	case req.ConfigAllow:
		source = "oneshot.allow_shell"
	}
	if source == "" {
		return oneShotShellDecision{}
	}
	// A refused request does not fail the run: it proceeds without shell, and
	// the trace header names the dropped tool and how to enable it.
	if req.Trust != projectTrustTrusted {
		return oneShotShellDecision{Source: source, DropReason: fmt.Sprintf("needs shell_exec; requested by %s but this project is not trusted (trust: %s): run `buckley trust allow` in this project", source, req.Trust)}
	}
	if slices.Contains(req.DeniedTools, "run_shell") {
		return oneShotShellDecision{Source: source, DropReason: fmt.Sprintf("needs shell_exec; requested by %s but approval.denied_tools denies run_shell: remove it from that list", source)}
	}
	if req.Intent != agentloop.MutationIntent {
		return oneShotShellDecision{Source: source, DropReason: "needs shell_exec; run_shell is only offered with --task-intent mutation"}
	}
	return oneShotShellDecision{Allow: true, Source: source}
}

// droppedRequestedTools lists the tools named in --tools that the model will not
// see, each with the reason, so a silent drop shows in the trace header.
func droppedRequestedTools(registry *tool.Registry, requested []string, shell oneShotShellDecision, hostTools bool) []string {
	var dropped []string
	for _, name := range cleanToolNames(requested) {
		if hostTools || registry == nil {
			continue
		}
		registered, ok := registry.Get(name)
		if !ok {
			continue
		}
		if name == "run_shell" {
			if !shell.Allow {
				reason := shell.DropReason
				if reason == "" {
					reason = "needs shell_exec; one-shot runs have no approver"
				}
				dropped = append(dropped, fmt.Sprintf("%s (%s)", name, reason))
			}
			continue
		}
		if risk := acpRiskLabel(tool.GetMetadata(registered).Impact); !acpFallbackPermissionDecision(risk) {
			dropped = append(dropped, fmt.Sprintf("%s (denied by the default risk policy: %s risk and no approver)", name, risk))
		}
	}
	return dropped
}

func formatDroppedTools(dropped []string) string {
	if len(dropped) == 0 {
		return ""
	}
	return "dropped from --tools: " + strings.Join(dropped, "; ")
}
