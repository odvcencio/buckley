package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/odvcencio/buckley/pkg/config"
)

func printHelp() {
	fmt.Println("Buckley - AI Development Assistant")
	fmt.Println()
	fmt.Println("USAGE:")
	fmt.Println("  buckley [FLAGS] [COMMAND]")
	fmt.Println()
	fmt.Println("MODES:")
	fmt.Println("  buckley                          Start interactive session (rich TUI by default)")
	fmt.Println("  buckley --plain                  Start with plain scrollback mode")
	fmt.Println("  buckley --tui                    Force rich TUI interface")
	fmt.Println("  buckley -p \"prompt\"              One-shot mode: run prompt and exit")
	fmt.Println()
	fmt.Println("COMMANDS:")
	fmt.Println("  plan <name> <desc>               Generate feature plan")
	fmt.Println("  execute <plan-id>                Execute a plan")
	fmt.Println("  execute-task --plan <id> --task <id>")
	fmt.Println("                                   Execute single task (CI/batch friendly)")
	fmt.Println("  commit [--dry-run]               Generate structured commit via tool-use (transparent)")
	fmt.Println("  pr [--dry-run]                   Generate structured PR via tool-use (transparent)")
	fmt.Println("  experiment run <name> -m <model> -p <prompt>")
	fmt.Println("                                   Run a parallel model comparison experiment")
	fmt.Println("  experiment list [--status <s>]   List recent experiments")
	fmt.Println("  experiment show <id|name>        Show experiment results (--format terminal|markdown)")
	fmt.Println("  experiment diff <id|name>        Compare variant outputs side-by-side")
	fmt.Println("  experiment replay <session-id>   Replay a session with a new model")
	fmt.Println("  serve [--bind host:port]         Start local HTTP/WebSocket server")
	fmt.Println("  remote <subcommand>              Remote session operations (attach, sessions, tokens, login, console)")
	fmt.Println("  batch prune-workspaces           Garbage-collect stale batch workspaces (k8s/CI)")
	fmt.Println("  git-webhook                      Listen for merge webhooks and run regression/release commands")
	fmt.Println("  agent-server                     HTTP proxy for ACP editor workflows (inline propose/apply)")
	fmt.Println("  lsp [--coordinator addr]         Start LSP server on stdio (editor integration)")
	fmt.Println("  acp [--workdir dir] [--log file] Start ACP agent on stdio (Zed/JetBrains/Neovim)")
	fmt.Println("  hunt [--dir path]                Scan codebase for improvement suggestions")
	fmt.Println("  dream [--dir path] [--plan]      Analyze architecture and identify gaps")
	fmt.Println("  ralph --prompt <p> [--timeout t] Autonomous task runner with iteration control")
	fmt.Println("  config [check|show|path]         Manage configuration")
	fmt.Println("  doctor                           Quick system health check (alias for config check)")
	fmt.Println("  completion [bash|zsh|fish]       Generate shell completions")
	fmt.Println("  worktree create [--container]    Create git worktree")
	fmt.Println("  migrate                          Apply database migrations")
	fmt.Println("  db backup --out <path>           Create a consistent SQLite backup (VACUUM INTO)")
	fmt.Println("  db restore --in <path> --force   Restore SQLite backup (stop Buckley first)")
	fmt.Println("  resume <session-id>              Resume a previous session")
	fmt.Println()
	fmt.Println("FLAGS:")
	fmt.Println("  -p <prompt>                      Run prompt in one-shot mode")
	fmt.Println("  -c, --config <path>              Use custom config file")
	fmt.Println("  -q, --quiet                      Suppress non-essential output")
	fmt.Println("  --no-color                       Disable colored output")
	fmt.Println("  --tui                            Use rich TUI interface")
	fmt.Println("  --plain                          Use plain scrollback mode")
	fmt.Println("  --rlm                            Use RLM execution mode (experimental)")
	fmt.Println("  --agent-socket <addr>            Start agent API server (unix:/path or tcp:host:port)")
	fmt.Println("  --verbose                        Stream model reasoning to stderr (one-shot)")
	fmt.Println("  --encoding json|toon             Set serialization format")
	fmt.Println("  --json                           Shortcut for --encoding json")
	fmt.Println("  -v, --version                    Show version information")
	fmt.Println("  -h, --help                       Show this help")
	fmt.Println()
	fmt.Println("ENVIRONMENT:")
	fmt.Println("  OPENROUTER_API_KEY               Provider API key (at least one provider key is required)")
	fmt.Println("  OPENAI_API_KEY                   Provider API key (optional alternative)")
	fmt.Println("  ANTHROPIC_API_KEY                Provider API key (optional alternative)")
	fmt.Println("  GOOGLE_API_KEY                   Provider API key (optional alternative)")
	fmt.Println("  BUCKLEY_MODEL_PLANNING           Override planning model")
	fmt.Println("  BUCKLEY_MODEL_EXECUTION          Override execution model")
	fmt.Println("  BUCKLEY_MODEL_REVIEW             Override review model")
	fmt.Println("  BUCKLEY_MODEL_COMMIT             Override model for `buckley commit`")
	fmt.Println("  BUCKLEY_MODEL_PR                 Override model for `buckley pr`")
	fmt.Println("  BUCKLEY_PROMPT_COMMIT            Override prompt template for `buckley commit`")
	fmt.Println("  BUCKLEY_PROMPT_PR                Override prompt template for `buckley pr`")
	fmt.Println("  BUCKLEY_PR_BASE                  Override PR base branch (e.g., main)")
	fmt.Println("  BUCKLEY_REMOTE_NAME              Remote name for pushes (default: origin)")
	fmt.Println("  BUCKLEY_IPC_TOKEN                IPC auth token (required for remote binds when enabled)")
	fmt.Println("  BUCKLEY_GENERATE_IPC_TOKEN       Auto-generate an IPC token when missing (serve mode)")
	fmt.Println("  BUCKLEY_IPC_TOKEN_FILE           Read/write the IPC token from this path (serve mode)")
	fmt.Println("  BUCKLEY_PRINT_GENERATED_IPC_TOKEN Print generated IPC token to stderr (serve mode; use cautiously)")
	fmt.Println("  BUCKLEY_BASIC_AUTH_USER          IPC basic auth username (optional)")
	fmt.Println("  BUCKLEY_BASIC_AUTH_PASSWORD      IPC basic auth password (optional)")
	fmt.Println("  BUCKLEY_DB_PATH                  Override primary SQLite DB path")
	fmt.Println("  BUCKLEY_DATA_DIR                 Directory containing Buckley DB files (db, remote-auth, checkpoints, etc)")
	fmt.Println("  BUCKLEY_LOG_DIR                  Override telemetry log directory")
	fmt.Println("  BUCKLEY_QUIET                    Suppress non-essential output")
	fmt.Println("  NO_COLOR                         Disable colored output")
	fmt.Println()
	fmt.Println("CONFIGURATION:")
	fmt.Println("  User config:    ~/.buckley/config.yaml")
	fmt.Println("  Project config: ./.buckley/config.yaml")
	fmt.Println("  Run 'buckley config check' to validate your setup")
	fmt.Println()
	fmt.Println("GETTING STARTED:")
	fmt.Println("  1. Get an API key: https://openrouter.ai/keys")
	fmt.Println(`  2. Run: export OPENROUTER_API_KEY="<YOUR_OPENROUTER_API_KEY>"`)
	fmt.Println("  3. Start: buckley")
	fmt.Println("  4. Type /help for available commands")
	fmt.Println()
	fmt.Println("DOCUMENTATION:")
	fmt.Println("  https://github.com/odvcencio/buckley")
}

func printVersion() {
	fmt.Printf("Buckley %s\n", version)
	if commit != "unknown" {
		fmt.Printf("  Commit:     %s\n", commit)
	}
	if buildDate != "unknown" {
		fmt.Printf("  Built:      %s\n", buildDate)
	}
	fmt.Printf("  Go version: %s\n", runtime.Version())
}

func runConfigCommand(args []string) error {
	subCmd := "show"
	if len(args) > 0 {
		subCmd = args[0]
	}

	switch subCmd {
	case "check":
		return runConfigCheck()
	case "show":
		return runConfigShow()
	case "path":
		return runConfigPath()
	default:
		return fmt.Errorf("unknown config command: %s (use check, show, or path)", subCmd)
	}
}

func runConfigCheck() error {
	fmt.Println("Checking Buckley configuration...")
	fmt.Println()

	// Check config files
	home, _ := os.UserHomeDir()
	userConfig := filepath.Join(home, ".buckley", "config.yaml")
	projectConfig := ".buckley/config.yaml"

	fmt.Println("Configuration files:")
	if _, err := os.Stat(userConfig); err == nil {
		fmt.Printf("  ✓ User config:    %s\n", userConfig)
	} else {
		fmt.Printf("  - User config:    %s (not found)\n", userConfig)
	}
	if _, err := os.Stat(projectConfig); err == nil {
		fmt.Printf("  ✓ Project config: %s\n", projectConfig)
	} else {
		fmt.Printf("  - Project config: %s (not found)\n", projectConfig)
	}
	fmt.Println()

	// Check API keys
	fmt.Println("API keys:")
	providers := []struct {
		name   string
		envVar string
	}{
		{"OpenRouter", "OPENROUTER_API_KEY"},
		{"OpenAI", "OPENAI_API_KEY"},
		{"Anthropic", "ANTHROPIC_API_KEY"},
		{"Google", "GOOGLE_API_KEY"},
	}

	hasProvider := false
	for _, p := range providers {
		if key := os.Getenv(p.envVar); key != "" {
			fmt.Printf("  ✓ %s: configured\n", p.name)
			hasProvider = true
		} else {
			fmt.Printf("  - %s: not set\n", p.name)
		}
	}

	// Check config.env fallback
	if !hasProvider {
		if key := checkConfigEnvFile(); key != "" {
			fmt.Printf("  ✓ OpenRouter: found in ~/.buckley/config.env\n")
			hasProvider = true
		}
	}
	fmt.Println()

	// Check dependencies
	fmt.Println("Dependencies:")
	if _, err := exec.LookPath("git"); err == nil {
		fmt.Println("  ✓ git: installed")
	} else {
		fmt.Println("  ✗ git: not found (required)")
	}
	fmt.Println()

	// Load and validate config
	cfg, err := config.Load()
	if err != nil {
		return withExitCode(err, 2)
	}

	// Show validation warnings
	warnings := cfg.ValidationWarnings()
	if len(warnings) > 0 {
		fmt.Println("Warnings:")
		for _, w := range warnings {
			fmt.Printf("  ⚠ %s\n", w)
		}
		fmt.Println()
	}

	if cfg.Providers.HasReadyProvider() {
		fmt.Println("✓ Configuration is valid")
	} else {
		fmt.Println("✗ No provider configured")
		fmt.Println()
		fmt.Println(`To fix: export OPENROUTER_API_KEY="<YOUR_OPENROUTER_API_KEY>"`)
		fmt.Println("Or enable a local provider (BUCKLEY_OLLAMA_ENABLED=1 or BUCKLEY_LITELLM_ENABLED=1).")
		fmt.Println("Get a key at: https://openrouter.ai/keys")
		return withExitCode(fmt.Errorf("no providers configured"), 2)
	}

	return nil
}

func runConfigShow() error {
	cfg, err := config.Load()
	if err != nil {
		return withExitCode(fmt.Errorf("failed to load config: %w", err), 2)
	}

	fmt.Println("Current configuration:")
	fmt.Println()
	fmt.Printf("Models:\n")
	fmt.Printf("  Planning:  %s\n", cfg.Models.Planning)
	fmt.Printf("  Execution: %s\n", cfg.Models.Execution)
	fmt.Printf("  Review:    %s\n", cfg.Models.Review)
	fmt.Println()
	fmt.Printf("Orchestrator:\n")
	fmt.Printf("  Trust level: %s\n", cfg.Orchestrator.TrustLevel)
	fmt.Printf("  Auto workflow: %v\n", cfg.Orchestrator.AutoWorkflow)
	fmt.Println()
	fmt.Printf("Providers:\n")
	for _, p := range cfg.Providers.ReadyProviders() {
		fmt.Printf("  ✓ %s\n", p)
	}
	return nil
}

func runConfigPath() error {
	home, _ := os.UserHomeDir()
	fmt.Println("Configuration file locations:")
	fmt.Printf("  User:    %s\n", filepath.Join(home, ".buckley", "config.yaml"))
	fmt.Printf("  Project: %s\n", ".buckley/config.yaml")
	fmt.Printf("  Env:     %s\n", filepath.Join(home, ".buckley", "config.env"))
	dbPath, err := resolveDBPath()
	if err != nil {
		dbPath = fmt.Sprintf("error: %v", err)
	}
	fmt.Printf("  DB:      %s\n", dbPath)
	return nil
}

func checkConfigEnvFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	envPath := filepath.Join(home, ".buckley", "config.env")
	data, err := os.ReadFile(envPath)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		if strings.HasPrefix(line, "OPENROUTER_API_KEY=") {
			key := strings.TrimPrefix(line, "OPENROUTER_API_KEY=")
			return strings.Trim(key, "\"'")
		}
	}
	return ""
}

func runCompletionCommand(args []string) error {
	if len(args) == 0 {
		fmt.Println("Generate shell completions for Buckley")
		fmt.Println()
		fmt.Println("Usage: buckley completion [bash|zsh|fish]")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  # Bash (add to ~/.bashrc)")
		fmt.Println("  eval \"$(buckley completion bash)\"")
		fmt.Println()
		fmt.Println("  # Zsh (add to ~/.zshrc)")
		fmt.Println("  eval \"$(buckley completion zsh)\"")
		fmt.Println()
		fmt.Println("  # Fish (add to ~/.config/fish/config.fish)")
		fmt.Println("  buckley completion fish | source")
		return nil
	}

	shell := args[0]
	switch shell {
	case "bash":
		printBashCompletion()
	case "zsh":
		printZshCompletion()
	case "fish":
		printFishCompletion()
	default:
		return fmt.Errorf("unsupported shell: %s (use bash, zsh, or fish)", shell)
	}
	return nil
}

func printBashCompletion() {
	fmt.Print(`_buckley_completions() {
    local cur prev commands
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD-1]}"

    commands="plan execute execute-task commit pr experiment serve remote batch git-webhook agent-server lsp acp config doctor completion worktree migrate db resume help version"

    case "${prev}" in
        buckley)
            COMPREPLY=( $(compgen -W "${commands} --help --version --tui --plain --quiet --no-color --config" -- "${cur}") )
            return 0
            ;;
        batch)
            COMPREPLY=( $(compgen -W "prune-workspaces" -- "${cur}") )
            return 0
            ;;
        config)
            COMPREPLY=( $(compgen -W "check show path" -- "${cur}") )
            return 0
            ;;
        experiment)
            COMPREPLY=( $(compgen -W "run" -- "${cur}") )
            return 0
            ;;
        completion)
            COMPREPLY=( $(compgen -W "bash zsh fish" -- "${cur}") )
            return 0
            ;;
        db)
            COMPREPLY=( $(compgen -W "backup restore" -- "${cur}") )
            return 0
            ;;
        --config|-c)
            COMPREPLY=( $(compgen -f -- "${cur}") )
            return 0
            ;;
    esac

    COMPREPLY=( $(compgen -W "${commands}" -- "${cur}") )
}
complete -F _buckley_completions buckley
`)
}

func printZshCompletion() {
	fmt.Print(`#compdef buckley

_buckley() {
    local -a commands
    commands=(
        'plan:Generate feature plan'
        'execute:Execute a plan'
        'execute-task:Execute single task'
        'commit:Create action-style commit'
        'pr:Create pull request'
        'experiment:Run model comparison experiments'
        'serve:Start local server'
        'remote:Remote session management'
        'batch:Batch helpers (k8s/CI)'
        'git-webhook:Run regression/release webhooks daemon'
        'agent-server:Run ACP HTTP proxy for editor workflows'
        'lsp:Start LSP server on stdio for editor integration'
        'acp:Start ACP agent on stdio for Zed/JetBrains/Neovim'
        'config:Manage configuration'
        'completion:Generate shell completions'
        'worktree:Git worktree management'
        'migrate:Apply database migrations'
        'db:Backup/restore SQLite DB'
        'resume:Resume a previous session'
        'doctor:Quick system health check'
        'help:Show help information'
        'version:Show version information'
    )

    _arguments -C \
        '-p[Run prompt in one-shot mode]:prompt:' \
        '-c[Use custom config file]:config file:_files' \
        '--config[Use custom config file]:config file:_files' \
        '-q[Suppress non-essential output]' \
        '--quiet[Suppress non-essential output]' \
        '--no-color[Disable colored output]' \
        '--verbose[Stream model reasoning to stderr]' \
        '--tui[Use rich TUI interface]' \
        '--plain[Use plain scrollback mode]' \
        '-v[Show version]' \
        '--version[Show version]' \
        '-h[Show help]' \
        '--help[Show help]' \
        '1: :->command' \
        '*::arg:->args'

    case $state in
        command)
            _describe -t commands 'buckley commands' commands
            ;;
        args)
            case $words[1] in
                batch)
                    _values 'batch command' prune-workspaces
                    ;;
                experiment)
                    _values 'experiment command' run
                    ;;
                config)
                    _values 'config command' check show path
                    ;;
                completion)
                    _values 'shell' bash zsh fish
                    ;;
                db)
                    _values 'db command' backup restore
                    ;;
            esac
            ;;
    esac
}

_buckley "$@"
`)
}

func printFishCompletion() {
	fmt.Print(`# Fish completion for buckley

complete -c buckley -f

# Commands
complete -c buckley -n __fish_use_subcommand -a plan -d 'Generate feature plan'
complete -c buckley -n __fish_use_subcommand -a execute -d 'Execute a plan'
complete -c buckley -n __fish_use_subcommand -a execute-task -d 'Execute single task'
complete -c buckley -n __fish_use_subcommand -a commit -d 'Create action-style commit'
complete -c buckley -n __fish_use_subcommand -a pr -d 'Create pull request'
complete -c buckley -n __fish_use_subcommand -a experiment -d 'Run model comparison experiments'
complete -c buckley -n __fish_use_subcommand -a serve -d 'Start local server'
complete -c buckley -n __fish_use_subcommand -a remote -d 'Remote session management'
complete -c buckley -n __fish_use_subcommand -a batch -d 'Batch helpers (k8s/CI)'
complete -c buckley -n __fish_use_subcommand -a git-webhook -d 'Run regression/release webhooks daemon'
complete -c buckley -n __fish_use_subcommand -a agent-server -d 'Run ACP HTTP proxy for editor workflows'
complete -c buckley -n __fish_use_subcommand -a lsp -d 'Start LSP server on stdio'
complete -c buckley -n __fish_use_subcommand -a acp -d 'Start ACP agent on stdio (Zed/JetBrains/Neovim)'
complete -c buckley -n __fish_use_subcommand -a config -d 'Manage configuration'
complete -c buckley -n __fish_use_subcommand -a completion -d 'Generate shell completions'
complete -c buckley -n __fish_use_subcommand -a worktree -d 'Git worktree management'
complete -c buckley -n __fish_use_subcommand -a migrate -d 'Apply database migrations'
complete -c buckley -n __fish_use_subcommand -a db -d 'Backup/restore SQLite DB'
complete -c buckley -n __fish_use_subcommand -a resume -d 'Resume a previous session'
complete -c buckley -n __fish_use_subcommand -a doctor -d 'Quick system health check'
complete -c buckley -n __fish_use_subcommand -a help -d 'Show help information'
complete -c buckley -n __fish_use_subcommand -a version -d 'Show version information'

# Global flags
complete -c buckley -s p -d 'Run prompt in one-shot mode'
complete -c buckley -s c -l config -d 'Use custom config file' -r
complete -c buckley -s q -l quiet -d 'Suppress non-essential output'
complete -c buckley -l no-color -d 'Disable colored output'
complete -c buckley -l tui -d 'Use rich TUI interface'
complete -c buckley -l plain -d 'Use plain scrollback mode'
complete -c buckley -s v -l version -d 'Show version'
complete -c buckley -s h -l help -d 'Show help'

# Config subcommands
complete -c buckley -n '__fish_seen_subcommand_from config' -a check -d 'Validate configuration'
complete -c buckley -n '__fish_seen_subcommand_from config' -a show -d 'Show current configuration'
complete -c buckley -n '__fish_seen_subcommand_from config' -a path -d 'Show config file paths'

# Experiment subcommands
complete -c buckley -n '__fish_seen_subcommand_from experiment' -a run -d 'Run an experiment'

# Completion subcommands
complete -c buckley -n '__fish_seen_subcommand_from completion' -a bash -d 'Generate bash completion'
complete -c buckley -n '__fish_seen_subcommand_from completion' -a zsh -d 'Generate zsh completion'
complete -c buckley -n '__fish_seen_subcommand_from completion' -a fish -d 'Generate fish completion'

# DB subcommands
complete -c buckley -n '__fish_seen_subcommand_from db' -a backup -d 'Create a consistent SQLite backup'
complete -c buckley -n '__fish_seen_subcommand_from db' -a restore -d 'Restore an SQLite backup'

# Batch subcommands
complete -c buckley -n '__fish_seen_subcommand_from batch' -a prune-workspaces -d 'Garbage-collect stale batch workspaces'
`)
}
