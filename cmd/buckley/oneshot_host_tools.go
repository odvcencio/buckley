package main

import (
	"regexp"

	"m31labs.dev/buckley/pkg/agentspec"
	"m31labs.dev/buckley/pkg/tool"
)

// hostMutationCoreTools are the file, shell, and git tools an unattended
// mutation lane needs to finish work in its worktree. Protocol tool caps and
// action-window filters must never hide them.
var hostMutationCoreTools = []string{
	"read_file", "write_file", "edit_file", "apply_patch", "search_text",
	"find_files", "list_directory", "run_shell", "run_tests", "run_verification",
	"git_status", "git_diff", "git_log",
}

// hostActionTools stay available after discovery is parked: the lane still
// has to run checks, inspect state, and commit through the shell.
var hostActionTools = []string{"run_shell", "git_status", "git_diff", "git_log"}

// ensureHostMutationTools adds the core tools that the registry has to a
// restricted tool filter. A nil filter already exposes every tool.
func ensureHostMutationTools(filter []string, registry *tool.Registry) []string {
	if filter == nil || registry == nil {
		return filter
	}
	out := append([]string(nil), filter...)
	for _, name := range hostMutationCoreTools {
		if _, ok := registry.Get(name); ok {
			out = append(out, name)
		}
	}
	return cleanToolNames(out)
}

// removeImplicitSubagentDelegation drops spawn_subagent from an unattended
// mutation lane that has no agent profile and no explicit allowlist entry.
// Without project subagent profiles, generic children time out and the lane
// reports BLOCKED without touching the worktree. The lane works in place.
func removeImplicitSubagentDelegation(registry *tool.Registry, profile *agentspec.RuntimeProfile, explicitAllowed []string) {
	if registry == nil || profile != nil {
		return
	}
	for _, name := range cleanToolNames(explicitAllowed) {
		if name == "spawn_subagent" {
			return
		}
	}
	registry.Remove("spawn_subagent")
}

// Only explicit whole-run statements count. Rules such as "never edit the
// owner's checkout" or "do not commit specs" must not match, because they
// constrain where to write, not whether the task writes at all.
var noChangeBriefPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(do not|don't|dont|must not)\s+(make|write|create|apply|produce)\s+(any\s+)?(file\s+|code\s+|workspace\s+|repo\s+|repository\s+)?(changes|edits|modifications)\b`),
	regexp.MustCompile(`(?i)\b(do not|don't|dont|must not)\s+(modify|edit|change)\s+(any|the)\s+(files?|code|worktree|workspace|repo|repository)\b`),
	regexp.MustCompile(`(?i)\bno\s+(file|code|workspace|repo|repository)\s+(changes|edits|modifications)\b`),
	regexp.MustCompile(`(?i)\b(report[- ]only|read[- ]only task|verification[- ]only|analysis[- ]only|review[- ]only|audit[- ]only)\b`),
	regexp.MustCompile(`(?i)\bwithout\s+(making\s+)?(any\s+)?(file\s+|code\s+)?(changes|edits|modifications)\b`),
}

// briefForbidsChanges reports whether the brief says the run must not change
// files. Such a run finishes with a report, so the completion contract must
// not demand an observable workspace change.
func briefForbidsChanges(prompt string) bool {
	for _, pattern := range noChangeBriefPatterns {
		if pattern.MatchString(prompt) {
			return true
		}
	}
	return false
}

func isHostActionTool(name string) bool {
	for _, candidate := range hostActionTools {
		if candidate == name {
			return true
		}
	}
	return false
}

// defaultMaxNoChangeContinuations bounds repeated "no workspace change"
// rejections so a lane that keeps answering without editing ends with a clear
// status instead of running to the general continuation limit.
const defaultMaxNoChangeContinuations = 6

func maxNoChangeContinuations(limits acpLoopLimits) int {
	if limits.allowHostTools && limits.MaxContinuations > 0 {
		return defaultMaxNoChangeContinuations
	}
	return 0
}
