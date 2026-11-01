package policy

import (
	"regexp"
	"slices"
	"strings"
)

const CommandClassAgentHarness = "agent_harness"

var shellAssignmentPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// buckleyTrustWriteVerbs are the "buckley trust" subcommands that change the
// project trust store. TestTrustWriteVerbs_MatchRunTrustCommand compares them
// with runTrustCommand in cmd/buckley.
var buckleyTrustWriteVerbs = map[string]bool{
	"allow": true, "trust": true,
	"deny": true, "restrict": true,
	"reset": true, "forget": true, "rm": true,
}

// buckleyStartupValueFlags are the startup flags that read the next word as
// their value, wherever the flag sits before the subcommand.
// TestStartupValueFlags_MatchConsumeStartupFlag compares them with
// consumeStartupFlag in cmd/buckley.
var buckleyStartupValueFlags = map[string]bool{
	"-p": true, "--encoding": true,
	"-c": true, "--config": true,
	"-m": true, "--model": true,
	"--agent": true, "--tools": true,
	"--max-tool-calls": true, "--max-elapsed": true,
	"--max-output-tokens": true, "--budget": true,
	"--read-root": true, "--task-intent": true,
}

// buckleyStateCommands are the buckley commands, written as the words that
// select them, that resolve an approval or change stored credentials. Like the
// "buckley trust" writes, they stay denied when the operator lets a model launch
// agents: the switch does not let it approve its own work or mint itself a
// token. TestStateCommands_MatchDispatch checks each word against the dispatch
// in cmd/buckley.
var buckleyStateCommands = [][]string{
	// Resolves a parked durable approval, or denies it with --deny.
	{"goal", "approve"},
	// Stores remote credentials after a browser approval.
	{"remote", "login"},
	// Mint and revoke API tokens.
	{"remote", "tokens", "create"},
	{"remote", "tokens", "revoke"},
}

// ClassifyShellCommand returns a stable policy fact for command forms that
// launch a known agent harness directly or after common execution wrappers.
// References in arguments, file paths, and prose are intentionally ignored.
func ClassifyShellCommand(command string) string {
	if anyShellSegment(command, shellSegmentLaunchesAgent) {
		return CommandClassAgentHarness
	}
	return ""
}

// writesBuckleyState reports whether any command in the line runs buckley so
// that it changes buckley's own trust, approval, or credential state: a project
// trust decision, a durable approval, an API token, or a remote login. It reads
// the command text only, like ClassifyShellCommand, and never runs the shell. It
// cannot see through `sh -c`, `eval`, scripts, a command substitution inside
// double quotes, or a variable that stands in for the program name. A subcommand
// or verb written as an expansion counts as a write, because the text cannot show
// what it becomes.
func writesBuckleyState(command string) bool {
	return anyShellSegment(command, shellSegmentWritesBuckleyState)
}

func shellSegmentLaunchesAgent(words []string) bool {
	_, _, ok := shellSegmentAgent(words)
	return ok
}

// shellSegmentWritesBuckleyState reports whether the command runs buckley so
// that it changes buckley's own trust, approval, or credential state. It matches
// in four cases:
//
//   - The subcommand word is an expansion. Startup flags and their values come
//     first and the next word is the subcommand, so an expansion there may be
//     any command.
//   - The words name one of buckleyStateCommands. Startup flags and the value
//     of -c/--config may sit between them, and an expansion stands in for any
//     word.
//   - A write verb follows the word "trust". It can sit anywhere after it, so
//     startup flags such as --quiet between the two do not hide it.
//   - The word that should be the verb is an expansion. buckley takes
//     -c/--config and its value anywhere on the line, so that value is skipped
//     first.
//
// A stray word such as `buckley pr --title trust --body rm` also matches; the
// caller denies it, which is the safe error.
func shellSegmentWritesBuckleyState(words []string) bool {
	name, args, ok := shellSegmentAgent(words)
	if !ok || name != "buckley" {
		return false
	}
	if i, unsure := buckleyNextWord(args, 0); unsure || (i < len(args) && shellWordIsExpanded(args[i])) {
		return true
	}
	if buckleyNamesStateCommand(args) {
		return true
	}
	afterTrust, verbSeen, configValue := false, false, false
	for _, arg := range args {
		if configValue {
			configValue = false
			continue
		}
		word := strings.ToLower(strings.TrimSpace(arg))
		if afterTrust {
			if buckleyTrustWriteVerbs[word] {
				return true
			}
			if word == "-c" || word == "--config" {
				configValue = true
			} else if !verbSeen && strings.HasPrefix(word, "-") && shellWordIsExpanded(arg) {
				return true // it may become -c and take the verb as its value
			} else if !verbSeen && !strings.HasPrefix(word, "-") {
				verbSeen = true
				if shellWordIsExpanded(arg) {
					return true
				}
			}
		}
		if word == "trust" {
			afterTrust, verbSeen = true, false
		}
	}
	return false
}

// buckleyNamesStateCommand reports whether the words after the program name
// begin one of the buckleyStateCommands. It ignores case, and an expansion
// counts as the word it stands in for.
func buckleyNamesStateCommand(args []string) bool {
	return slices.ContainsFunc(buckleyStateCommands, func(command []string) bool {
		at := 0
		for _, want := range command {
			var unsure bool
			if at, unsure = buckleyNextWord(args, at); unsure {
				return true
			}
			if at == len(args) {
				return false
			}
			if word := args[at]; !shellWordIsExpanded(word) && !strings.EqualFold(strings.TrimSpace(word), want) {
				return false
			}
			at++
		}
		return true
	})
}

// buckleyNextWord returns the index of the first word at or after from that is
// neither a startup flag nor the value of one, or len(args) when there is none.
// The startup parser takes these flags before the subcommand, and -c/--config
// with its value from anywhere, so they can sit before the subcommand and
// between the words after it. After the subcommand the parser leaves the other
// flags in place and the command fails on the first one, so skipping them here
// as well costs nothing.
//
// unsure reports that a skipped flag word holds an expansion. The text cannot
// show whether it becomes a flag that takes the next word (`-c$(:)` runs as
// -c), so the returned index may not be the word buckley reads; the caller
// denies it, which is the safe error.
func buckleyNextWord(args []string, from int) (next int, unsure bool) {
	i := from
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		if shellWordIsExpanded(args[i]) {
			unsure = true
		}
		if buckleyStartupValueFlags[args[i]] {
			i++
		}
		i++
	}
	return min(i, len(args)), unsure
}

// shellWordIsExpanded reports whether the shell may rewrite the word: a
// variable, substitution, brace list, or glob. The reader has already removed
// quotes, so a quoted "$V" counts too; the caller denies it, which is the safe
// error.
func shellWordIsExpanded(word string) bool {
	return strings.ContainsAny(word, "$`*?[{")
}

// shellSegmentAgent returns the agent CLI that a simple command runs and the
// words after it. It skips variable assignments, shell keywords, and execution
// wrappers on the way to the executable.
func shellSegmentAgent(words []string) (string, []string, bool) {
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
		case "buckley", "codex", "claude", "opencode", "aider", "cursor-agent", "gemini", "amp", "copilot":
			return name, words[i+1:], true
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
					return "", nil, false
				}
				if shellWrapperOptionTakesValue(name, option) {
					i++
				}
			}
			if name == "timeout" {
				i++ // duration precedes the executable
			}
		default:
			return "", nil, false
		}
	}
	return "", nil, false
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
