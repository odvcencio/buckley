package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"golang.org/x/term"

	"github.com/odvcencio/buckley/pkg/config"
)

func dispatchSubcommand(args []string) (bool, int) {
	if len(args) == 0 {
		return false, 0
	}
	switch args[0] {
	case "--version", "-v", "version":
		printVersion()
		return true, 0
	case "--help", "-h", "help":
		printHelp()
		return true, 0
	case "plan":
		return true, runCommand(runPlanCommand, args[1:])
	case "execute":
		return true, runCommand(runExecuteCommand, args[1:])
	case "remote":
		return true, runCommand(runRemoteCommand, args[1:])
	case "batch":
		return true, runCommand(runBatchCommand, args[1:])
	case "git-webhook":
		return true, runCommand(runGitWebhookCommand, args[1:])
	case "execute-task":
		return true, runCommand(runExecuteTaskCommand, args[1:])
	case "commit":
		return true, runCommand(runCommitCommand, args[1:])
	case "pr":
		return true, runCommand(runPRCommand, args[1:])
	case "review":
		return true, runCommand(runReviewCommand, args[1:])
	case "review-pr":
		return true, runCommand(runReviewPRCommand, args[1:])
	case "experiment":
		return true, runCommand(runExperimentCommand, args[1:])
	case "serve":
		return true, runCommand(runServeCommand, args[1:])
	case "migrate":
		if err := runMigrateCommand(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return true, exitCodeForError(err)
		}
		return true, 0
	case "db":
		return true, runCommand(runDBCommand, args[1:])
	case "worktree":
		return true, runCommand(runWorktreeCommand, args[1:])
	case "resume":
		return false, 0
	case "agent-server":
		return true, runCommand(runAgentServerCommand, args[1:])
	case "lsp":
		return true, runCommand(runLSPCommand, args[1:])
	case "acp":
		return true, runCommand(runACPCommand, args[1:])
	case "hunt":
		return true, runCommand(runHuntCommand, args[1:])
	case "dream":
		return true, runCommand(runDreamCommand, args[1:])
	case "ralph":
		return true, runCommand(runRalphCommand, args[1:])
	case "config":
		return true, runCommand(runConfigCommand, args[1:])
	case "doctor":
		// Alias for config check - quick system health check
		return true, runCommand(runConfigCommand, []string{"check"})
	case "completion":
		return true, runCommand(runCompletionCommand, args[1:])
	default:
		if strings.HasPrefix(args[0], "-") {
			fmt.Fprintf(os.Stderr, "Error: unknown flag: %s\n", args[0])
		} else {
			fmt.Fprintf(os.Stderr, "Error: unknown command: %s\n", args[0])
		}
		fmt.Fprintln(os.Stderr, "Run 'buckley --help' for usage.")
		return true, 1
	}
}

func runCommand(handler func([]string) error, args []string) int {
	if err := handler(args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return exitCodeForError(err)
	}
	return 0
}

func parseStartupOptions(raw []string) (*startupOptions, error) {
	opts := &startupOptions{}
	if val, ok := parseBoolEnv("BUCKLEY_QUIET"); ok {
		opts.quiet = val
	}
	if val, ok := parseBoolEnv("NO_COLOR"); ok {
		opts.noColor = val
	}

	filtered := make([]string, 0, len(raw))
	var nextPrompt bool
	var nextEncoding bool
	var nextConfig bool
	var nextAgentSocket bool

	for _, arg := range raw {
		if nextPrompt {
			opts.prompt = arg
			nextPrompt = false
			continue
		}
		if nextEncoding {
			opts.encodingOverride = strings.ToLower(arg)
			nextEncoding = false
			continue
		}
		if nextConfig {
			opts.configPath = arg
			nextConfig = false
			continue
		}
		if nextAgentSocket {
			opts.agentSocket = arg
			nextAgentSocket = false
			continue
		}

		switch arg {
		case "--plain", "--no-tui":
			opts.plainModeSet = true
			opts.plainMode = true
		case "--tui":
			opts.plainModeSet = true
			opts.plainMode = false
		case "-p":
			nextPrompt = true
		case "--encoding":
			nextEncoding = true
		case "--encoding=toon":
			opts.encodingOverride = "toon"
		case "--encoding=json", "--json":
			opts.encodingOverride = "json"
		case "--verbose":
			opts.verbose = true
		case "--quiet", "-q":
			opts.quiet = true
		case "--no-color":
			opts.noColor = true
		case "--rlm":
			opts.rlmMode = true
		case "--config", "-c":
			nextConfig = true
		case "--agent-socket":
			nextAgentSocket = true
		default:
			if strings.HasPrefix(arg, "--config=") {
				opts.configPath = strings.TrimPrefix(arg, "--config=")
			} else if strings.HasPrefix(arg, "--agent-socket=") {
				opts.agentSocket = strings.TrimPrefix(arg, "--agent-socket=")
			} else {
				filtered = append(filtered, arg)
			}
		}
	}

	if nextPrompt {
		return nil, fmt.Errorf("-p requires a prompt argument")
	}
	if nextEncoding {
		return nil, fmt.Errorf("--encoding requires a value")
	}
	if nextAgentSocket {
		return nil, fmt.Errorf("--agent-socket requires an address")
	}
	if nextConfig {
		return nil, fmt.Errorf("--config requires a path argument")
	}

	opts.args = filtered
	return opts, nil
}

func (o *startupOptions) consumeResumeCommand() error {
	if len(o.args) == 0 || o.args[0] != "resume" {
		return nil
	}
	if len(o.args) < 2 {
		return fmt.Errorf("usage: buckley resume <session-id>")
	}
	o.resumeSessionID = o.args[1]
	o.args = o.args[:0]
	return nil
}

func parseBoolEnv(key string) (bool, bool) {
	val := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if val == "" {
		return false, false
	}
	switch val {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	default:
		return false, false
	}
}

func applySandboxOverride(cfg *config.Config) {
	if cfg == nil {
		return
	}
	mode := strings.TrimSpace(strings.ToLower(os.Getenv("BUCKLEY_SANDBOX")))
	if mode == "" {
		mode = strings.TrimSpace(strings.ToLower(os.Getenv("BUCKLEY_SANDBOX_MODE")))
	}
	switch mode {
	case "container", "containers", "devcontainer", "sandbox", "on", "true", "yes":
		cfg.Worktrees.UseContainers = true
	case "host", "off", "disable", "disabled", "false", "no":
		cfg.Worktrees.UseContainers = false
	}
}

func applyRLMOverride(cfg *config.Config) {
	if cfg == nil {
		return
	}
	if cliFlags.rlmMode {
		cfg.Execution.Mode = config.ExecutionModeRLM
		cfg.Oneshot.Mode = config.ExecutionModeRLM
	}
}

func isInteractiveTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) &&
		term.IsTerminal(int(os.Stdout.Fd()))
}

// ansiEscapePattern matches ANSI escape sequences including:
// - CSI sequences like \x1b[...X (cursor reports, colors, etc.)
// - OSC sequences like \x1b]...ST (window titles, etc.)
// - Simple escapes like \x1b[?1h
var ansiEscapePattern = regexp.MustCompile(`\x1b(?:\[[0-9;?]*[a-zA-Z]|\][^\x07]*\x07|\[[^\x1b]*[a-zA-Z])`)

// sanitizeTerminalInput removes ANSI escape sequences from input
// This filters out cursor position reports, color codes, and other
// terminal responses that can leak into pasted text
func sanitizeTerminalInput(input string) string {
	return ansiEscapePattern.ReplaceAllString(input, "")
}
