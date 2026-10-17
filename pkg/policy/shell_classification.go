package policy

import (
	"regexp"
	"strings"

	"github.com/anmitsu/go-shlex"
)

const CommandClassAgentHarness = "agent_harness"

var shellAssignmentPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// ClassifyShellCommand returns a stable policy fact for command forms that
// launch a known agent harness directly or after common execution wrappers.
// References in arguments, file paths, and prose are intentionally ignored.
func ClassifyShellCommand(command string) string {
	var quote byte
	escaped := false
	start := 0
	for i := 0; i < len(command); i++ {
		char := command[i]
		if escaped {
			escaped = false
			continue
		}
		if char == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if char == quote {
				quote = 0
			}
			continue
		}
		if char == '\'' || char == '"' {
			quote = char
			continue
		}
		if char == '#' && (i == start || strings.ContainsRune(" \t", rune(command[i-1]))) {
			if shellSegmentLaunchesAgent(command[start:i]) {
				return CommandClassAgentHarness
			}
			for i < len(command) && command[i] != '\n' {
				i++
			}
			start = i + 1
			continue
		}
		if strings.ContainsRune("\n\r;&|()", rune(char)) {
			if shellSegmentLaunchesAgent(command[start:i]) {
				return CommandClassAgentHarness
			}
			start = i + 1
		}
	}
	if start < len(command) && shellSegmentLaunchesAgent(command[start:]) {
		return CommandClassAgentHarness
	}
	return ""
}

func shellSegmentLaunchesAgent(command string) bool {
	words, err := shlex.Split(command, true)
	if err != nil {
		return false
	}
	for i := 0; i < len(words); i++ {
		if shellAssignmentPattern.MatchString(words[i]) {
			continue
		}
		name := strings.ReplaceAll(words[i], `\`, "/")
		if slash := strings.LastIndexByte(name, '/'); slash >= 0 {
			name = name[slash+1:]
		}
		name = strings.TrimSuffix(strings.ToLower(name), ".exe")
		switch name {
		case "if", "then", "elif", "else", "do", "!":
			continue
		case "buckley", "codex", "claude", "opencode", "aider", "cursor-agent", "gemini", "goose", "amp", "copilot":
			return true
		case "command", "exec", "nohup", "sudo", "env", "nice", "timeout":
			for i+1 < len(words) && strings.HasPrefix(words[i+1], "-") {
				i++
				option := words[i]
				if option == "--" {
					break
				}
				if option == "--help" || option == "--version" ||
					(name == "command" && (option == "-v" || option == "-V")) ||
					(name == "sudo" && (option == "-v" || option == "-l")) {
					return false
				}
				if shellWrapperOptionTakesValue(name, option) {
					i++
				}
			}
			if name == "timeout" {
				i++ // duration precedes the executable
			}
		default:
			return false
		}
	}
	return false
}

func shellWrapperOptionTakesValue(name, option string) bool {
	switch name {
	case "exec":
		return option == "-a"
	case "env":
		return option == "-u" || option == "--unset" || option == "-C" || option == "--chdir"
	case "sudo":
		return option == "-u" || option == "--user" || option == "-g" || option == "--group" ||
			option == "-h" || option == "--host" || option == "-p" || option == "--prompt" ||
			option == "-D" || option == "--chdir"
	case "nice":
		return option == "-n" || option == "--adjustment"
	case "timeout":
		return option == "-s" || option == "--signal" || option == "-k" || option == "--kill-after"
	}
	return false
}
