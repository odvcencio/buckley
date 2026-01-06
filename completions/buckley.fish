# Fish completion for buckley
# Install by copying to ~/.config/fish/completions/buckley.fish

# Disable file completions for all commands
complete -c buckley -f

# Global flags
complete -c buckley -l version -s v -d 'Show version'
complete -c buckley -l help -s h -d 'Show help'
complete -c buckley -l plain -d 'Use plain scrollback mode'
complete -c buckley -l no-tui -d 'Use plain scrollback mode'
complete -c buckley -l tui -d 'Use rich Bubble Tea TUI interface'
complete -c buckley -l encoding -d 'Override serialization format' -xa 'json toon'
complete -c buckley -l json -d 'Shortcut for --encoding json'

# Subcommands
complete -c buckley -n '__fish_use_subcommand' -a plan -d 'Create a new feature plan'
complete -c buckley -n '__fish_use_subcommand' -a execute -d 'Execute a plan or resume execution'
complete -c buckley -n '__fish_use_subcommand' -a resume -d 'Resume a saved plan'
complete -c buckley -n '__fish_use_subcommand' -a remote -d 'Connect to a remote Buckley session'
complete -c buckley -n '__fish_use_subcommand' -a batch -d 'Run in batch processing mode'
complete -c buckley -n '__fish_use_subcommand' -a git-webhook -d 'Handle git webhook events'
complete -c buckley -n '__fish_use_subcommand' -a execute-task -d 'Execute a single task (for batch workers)'
complete -c buckley -n '__fish_use_subcommand' -a serve -d 'Start local IPC/WebSocket server'
complete -c buckley -n '__fish_use_subcommand' -a migrate -d 'Run database migrations'
complete -c buckley -n '__fish_use_subcommand' -a worktree -d 'Manage git worktrees'
complete -c buckley -n '__fish_use_subcommand' -a agent-server -d 'Start the agent server'

# serve subcommand
complete -c buckley -n '__fish_seen_subcommand_from serve' -l bind -d 'Bind address (host:port)'
complete -c buckley -n '__fish_seen_subcommand_from serve' -l basic-auth-user -d 'Basic auth username'
complete -c buckley -n '__fish_seen_subcommand_from serve' -l basic-auth-pass -d 'Basic auth password'

# remote subcommand
complete -c buckley -n '__fish_seen_subcommand_from remote' -a attach -d 'Attach to a remote session'
complete -c buckley -n '__fish_seen_subcommand_from remote; and __fish_seen_subcommand_from attach' -l url -d 'Remote host URL'
complete -c buckley -n '__fish_seen_subcommand_from remote; and __fish_seen_subcommand_from attach' -l session -d 'Session ID'

# execute-task subcommand
complete -c buckley -n '__fish_seen_subcommand_from execute-task' -l plan -d 'Plan ID'
complete -c buckley -n '__fish_seen_subcommand_from execute-task' -l task -d 'Task ID'
complete -c buckley -n '__fish_seen_subcommand_from execute-task' -l remote-branch -d 'Remote branch'

# worktree subcommand
complete -c buckley -n '__fish_seen_subcommand_from worktree' -a create -d 'Create a new worktree'
complete -c buckley -n '__fish_seen_subcommand_from worktree' -a list -d 'List worktrees'
complete -c buckley -n '__fish_seen_subcommand_from worktree' -a delete -d 'Delete a worktree'
complete -c buckley -n '__fish_seen_subcommand_from worktree; and __fish_seen_subcommand_from create' -l container -d 'Enable container support'
