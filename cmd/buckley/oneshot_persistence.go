package main

import (
	"fmt"
	"io"
	"time"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func applyOneShotPersistence(cfg *config.Config, limits acpLoopLimits, stderr io.Writer) acpLoopLimits {
	if limits.Persist && (limits.TaskIntent == agentloop.UnknownIntent || limits.TaskIntent == "") {
		limits.TaskIntent = agentloop.MutationIntent
	}
	if limits.ChildContract || limits.SourceScope != nil || limits.TaskIntent != agentloop.MutationIntent {
		return limits
	}
	limits.bestEffortObservation = true
	limits.longMutation = oneShotHostToolsAllowed(cfg) || limits.Persist
	if !limits.longMutation {
		return limits
	}
	limits.MaxContinuations = config.DefaultUnattendedMaxContinuations
	if cfg != nil && cfg.Oneshot.MaxContinuations != nil {
		limits.MaxContinuations = *cfg.Oneshot.MaxContinuations
	}
	previous := limits.OnContinuation
	limits.OnContinuation = func(count int, reason string) {
		fmt.Fprintf(stderr, "One-shot continuation: count=%d limit=%d reason=%q\n", count, limits.MaxContinuations, reason)
		if previous != nil {
			previous(count, reason)
		}
	}
	if cfg != nil {
		limits.MaxModelRequests = minPositiveLimit(limits.MaxModelRequests, cfg.AgentController.EmergencyFuse.ModelRequests)
	}
	if cfg != nil && cfg.AgentController.EmergencyFuse.WallTime > 0 {
		seconds := max(1, int(cfg.AgentController.EmergencyFuse.WallTime/time.Second))
		limits.MaxElapsedSeconds = minPositiveLimit(limits.MaxElapsedSeconds, seconds)
	}
	return limits
}

func registerOneShotVerification(registry *tool.Registry) {
	if shell, ok := registry.Get("run_shell"); ok {
		if shell, ok := shell.(*builtin.ShellCommandTool); ok {
			registry.Register(builtin.NewWorkspaceVerificationTool(shell))
		}
	}
}
