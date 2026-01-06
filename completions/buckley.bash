# Bash completion for buckley
# Source this file or add to ~/.bashrc:
#   source /path/to/buckley.bash
# Or install to system:
#   cp buckley.bash /etc/bash_completion.d/buckley

_buckley_completions() {
    local cur prev words cword
    _init_completion || return

    local commands="plan execute resume remote batch git-webhook execute-task serve migrate worktree agent-server"
    local global_flags="--version -v --help -h --plain --no-tui --tui --encoding --json"

    # Subcommand-specific flags
    local serve_flags="--bind --basic-auth-user --basic-auth-pass"
    local remote_flags="attach --url --session"
    local execute_task_flags="--plan --task --remote-branch"
    local worktree_flags="create list delete --container"

    case "${prev}" in
        buckley)
            COMPREPLY=($(compgen -W "${commands} ${global_flags}" -- "${cur}"))
            return 0
            ;;
        --encoding)
            COMPREPLY=($(compgen -W "json toon" -- "${cur}"))
            return 0
            ;;
        serve)
            COMPREPLY=($(compgen -W "${serve_flags}" -- "${cur}"))
            return 0
            ;;
        remote)
            COMPREPLY=($(compgen -W "attach" -- "${cur}"))
            return 0
            ;;
        attach)
            COMPREPLY=($(compgen -W "--url --session" -- "${cur}"))
            return 0
            ;;
        execute-task)
            COMPREPLY=($(compgen -W "${execute_task_flags}" -- "${cur}"))
            return 0
            ;;
        worktree)
            COMPREPLY=($(compgen -W "${worktree_flags}" -- "${cur}"))
            return 0
            ;;
        --bind|--url|--session|--plan|--task|--remote-branch|--basic-auth-user|--basic-auth-pass)
            # These flags expect a value, don't complete
            return 0
            ;;
    esac

    # Default to global flags if nothing else matches
    if [[ "${cur}" == -* ]]; then
        COMPREPLY=($(compgen -W "${global_flags}" -- "${cur}"))
    else
        COMPREPLY=($(compgen -W "${commands}" -- "${cur}"))
    fi
}

complete -F _buckley_completions buckley
