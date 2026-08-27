package policy

import (
	"regexp"
	"strings"
)

const CommandClassAgentHarness = "agent_harness"

var agentHarnessInvocationPattern = regexp.MustCompile(`(?i)(^|[\n\r;&|()])[ \t]*(([^ \t;&|()]+/)?(command|exec|nohup|sudo|env|nice|timeout)[ \t]+|[A-Za-z_][A-Za-z0-9_]*=[^ \t;&|()]+[ \t]+)*([^ \t;&|()]+/)?(buckley|codex|claude|opencode|aider|cursor-agent|gemini|goose|amp|copilot)([ \t\r\n;&|)]|$)`)

// ClassifyShellCommand returns a stable policy fact for command forms that
// launch a known agent harness directly or after common execution wrappers.
// References in arguments, file paths, and prose are intentionally ignored.
func ClassifyShellCommand(command string) string {
	if agentHarnessInvocationPattern.MatchString(strings.TrimSpace(command)) {
		return CommandClassAgentHarness
	}
	return ""
}
