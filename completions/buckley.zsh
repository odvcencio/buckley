#compdef buckley

# Zsh completion for buckley
# Install by adding to ~/.zshrc:
#   fpath=(/path/to/completions $fpath)
#   autoload -Uz compinit && compinit
# Or copy to a directory in your fpath:
#   cp buckley.zsh ~/.zsh/completions/_buckley

_buckley() {
    local -a commands
    local -a global_flags

    commands=(
        'plan:Create a new feature plan'
        'execute:Execute a plan or resume execution'
        'resume:Resume a saved plan'
        'remote:Connect to a remote Buckley session'
        'batch:Run in batch processing mode'
        'git-webhook:Handle git webhook events'
        'execute-task:Execute a single task (for batch workers)'
        'serve:Start local IPC/WebSocket server'
        'migrate:Run database migrations'
        'worktree:Manage git worktrees'
        'agent-server:Start the agent server'
    )

    global_flags=(
        '--version[Show version]::'
        '-v[Show version]::'
        '--help[Show help]::'
        '-h[Show help]::'
        '--plain[Use plain scrollback mode]::'
        '--no-tui[Use plain scrollback mode]::'
        '--tui[Use rich Bubble Tea TUI interface]::'
        '--encoding[Override serialization format]:format:(json toon)'
        '--json[Shortcut for --encoding json]::'
    )

    _arguments -C \
        '1: :->command' \
        '*:: :->args' \
        && return 0

    case $state in
        command)
            _describe -t commands 'buckley commands' commands
            _describe -t flags 'global flags' global_flags
            ;;
        args)
            case $words[1] in
                serve)
                    _arguments \
                        '--bind[Bind address]:address:' \
                        '--basic-auth-user[Basic auth username]:username:' \
                        '--basic-auth-pass[Basic auth password]:password:'
                    ;;
                remote)
                    local -a remote_cmds
                    remote_cmds=(
                        'attach:Attach to a remote session'
                    )
                    _describe -t remote-commands 'remote commands' remote_cmds

                    if [[ $words[2] == "attach" ]]; then
                        _arguments \
                            '--url[Remote host URL]:url:' \
                            '--session[Session ID]:session:'
                    fi
                    ;;
                execute-task)
                    _arguments \
                        '--plan[Plan ID]:plan:' \
                        '--task[Task ID]:task:' \
                        '--remote-branch[Remote branch]:branch:'
                    ;;
                worktree)
                    local -a worktree_cmds
                    worktree_cmds=(
                        'create:Create a new worktree'
                        'list:List worktrees'
                        'delete:Delete a worktree'
                    )
                    _describe -t worktree-commands 'worktree commands' worktree_cmds

                    if [[ $words[2] == "create" ]]; then
                        _arguments \
                            '--container[Enable container support]::'
                    fi
                    ;;
                *)
                    _arguments $global_flags
                    ;;
            esac
            ;;
    esac
}

_buckley "$@"
