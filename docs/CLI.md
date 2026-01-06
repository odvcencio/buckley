# Buckley CLI Reference

Complete command-line interface documentation for Buckley.

## Synopsis

```bash
buckley [FLAGS] [COMMAND] [ARGS...]
```

## Global Flags

| Flag | Short | Description |
|------|-------|-------------|
| `--help` | `-h` | Show help message and exit |
| `--version` | `-v` | Show version, commit, and build information |
| `--config <path>` | `-c` | Use a custom configuration file |
| `--quiet` | `-q` | Suppress non-essential output (banners, tips) |
| `--no-color` | | Disable colored output (also respects `NO_COLOR` env) |
| `--tui` | | Force rich TUI interface (default when interactive) |
| `--plain` | | Force plain scrollback mode (default when piped) |
| `--encoding <format>` | | Set serialization format: `json` or `toon` |
| `--json` | | Shortcut for `--encoding json` |
| `-p <prompt>` | | Run a single prompt and exit (one-shot mode) |

## Exit Codes

| Code | Meaning |
|------|---------|
| `0` | Success |
| `1` | General error (runtime failure, invalid input) |
| `2` | Configuration error (missing API key, invalid config) |

## Commands

### Interactive Session

```bash
buckley                    # Start interactive TUI
buckley --plain            # Start in plain scrollback mode
buckley -p "your prompt"   # One-shot mode: run prompt and exit
```

**One-shot mode** (`-p`) executes a single prompt, prints the response, and exits. Useful for scripting:

```bash
# Generate a commit message
buckley -p "Generate a commit message for the staged changes" --quiet

# Pipe input
echo "Explain this error" | buckley --plain
```

### plan

Generate a feature implementation plan.

```bash
buckley plan <feature-name> <description>
```

**Example:**
```bash
buckley plan user-auth "Add JWT-based authentication with refresh tokens"
```

**Output:** Creates a plan with tasks and implementation strategy, stored in the database.

### execute

Execute a previously created plan.

```bash
buckley execute <plan-id>
```

**Example:**
```bash
buckley execute 2024-01-15-user-auth
```

### execute-task

Execute a single task from a plan. Designed for CI/batch environments.

```bash
buckley execute-task --plan <plan-id> --task <task-id> [OPTIONS]
```

**Options:**
| Flag | Default | Description |
|------|---------|-------------|
| `--plan` | (required) | Plan identifier |
| `--task` | (required) | Task identifier |
| `--remote-branch` | `$BUCKLEY_REMOTE_BRANCH` | Branch to push after completion |
| `--remote-name` | `origin` | Git remote name |
| `--push` | `true` | Push to remote after completion |

**Example:**
```bash
buckley execute-task --plan user-auth --task implement-jwt --remote-branch feature/auth
```

### commit

Generate and create a conventional commit from staged changes.

```bash
buckley commit [OPTIONS]
```

**Options:**
| Flag | Description |
|------|-------------|
| `--dry-run` | Show generated commit message without committing |
| `--no-verify` | Skip pre-commit hooks |

**Environment Variables:**
- `BUCKLEY_MODEL_COMMIT` - Override model for commit generation
- `BUCKLEY_PROMPT_COMMIT` - Override prompt template

**Example:**
```bash
git add -p                    # Stage changes
buckley commit                # Generate and create commit
buckley commit --dry-run      # Preview commit message
```

### pr

Generate and create a GitHub pull request.

```bash
buckley pr [OPTIONS]
```

**Options:**
| Flag | Description |
|------|-------------|
| `--dry-run` | Show generated PR without creating |
| `--base` | Base branch (default: from `BUCKLEY_PR_BASE` or repo default) |

**Environment Variables:**
- `BUCKLEY_MODEL_PR` - Override model for PR generation
- `BUCKLEY_PROMPT_PR` - Override prompt template
- `BUCKLEY_PR_BASE` - Override base branch

**Example:**
```bash
buckley pr                    # Create PR for current branch
buckley pr --dry-run          # Preview PR title and body
buckley pr --base develop     # Target specific base branch
```

### serve

Start the local HTTP/WebSocket IPC server (and optional embedded Mission Control UI).

```bash
buckley serve [OPTIONS]
```

**Options:**
| Flag | Default | Description |
|------|---------|-------------|
| `--bind` | `127.0.0.1:4488` | Address to bind |
| `--browser` | `false` | Enable browser UI |
| `--assets` | | Path to static assets for browser |
| `--allow-origin` | | Additional allowed CORS origins (repeatable) |
| `--require-token` | `false` | Require authentication token |
| `--auth-token` | | Set authentication token |

**Example:**
```bash
# Serve the embedded Mission Control UI
buckley serve --browser

# Production (with auth)
buckley serve --bind 0.0.0.0:4488 --require-token --browser
```

### remote

Manage remote Buckley sessions.

#### remote attach

Attach to a running remote session.

```bash
buckley remote attach --url <host> --session <id> [OPTIONS]
```

**Options:**
| Flag | Description |
|------|-------------|
| `--url` | Remote Buckley instance URL |
| `--session` | Session ID to attach to |
| `--token` | Authentication token |
| `--basic-auth-user` | Basic auth username |
| `--basic-auth-pass` | Basic auth password |

**Example:**
```bash
buckley remote attach \
  --url https://buckley.example.com \
  --session abc123 \
  --token "$BUCKLEY_IPC_TOKEN"
```

#### remote sessions

List active sessions on a remote instance.

```bash
buckley remote sessions --url <host> [--token <token>]
```

#### remote tokens

Manage API tokens on a remote instance.

```bash
buckley remote tokens list --url <host> --token <admin-token>
buckley remote tokens create --url <host> --name <name> --scope <scope>
buckley remote tokens revoke --url <host> --id <token-id>
```

#### remote login

Authenticate the CLI via a browser-approved ticket flow (recommended for hosted deployments with browser login).

```bash
buckley remote login --url <host> [--label <label>] [--no-browser]
```

#### remote console

Open an interactive shell on a remote session.

```bash
buckley remote console --url <host> --session <id>
```

### config

Manage Buckley configuration.

#### config check

Validate configuration and show diagnostic information.

```bash
buckley config check
```

**Output includes:**
- Configuration file locations and status
- API key validation (masked)
- Dependency checks (git, etc.)
- Validation errors with suggestions

#### config show

Display current effective configuration.

```bash
buckley config show
```

#### config path

Show configuration file paths.

```bash
buckley config path
```

### completion

Generate shell completion scripts.

```bash
buckley completion [bash|zsh|fish]
```

**Installation:**

```bash
# Bash (add to ~/.bashrc)
eval "$(buckley completion bash)"

# Zsh (add to ~/.zshrc)
eval "$(buckley completion zsh)"

# Fish (add to ~/.config/fish/config.fish)
buckley completion fish | source
```

### worktree

Manage git worktrees with optional container support.

#### worktree create

Create a new git worktree.

```bash
buckley worktree create [--container] <branch-name>
```

**Options:**
| Flag | Description |
|------|-------------|
| `--container` | Create with container environment |

### migrate

Apply database migrations.

```bash
buckley migrate
```

Run this after upgrading Buckley to ensure database schema is current.

### resume

Resume a previous session.

```bash
buckley resume <session-id>
```

**Example:**
```bash
# List recent sessions
buckley config show  # Shows session info

# Resume specific session
buckley resume abc123def456
```

### batch

Batch processing commands for CI/CD environments.

#### batch prune-workspaces

Clean up stale batch workspaces.

```bash
buckley batch prune-workspaces [--older-than <duration>]
```

### git-webhook

Start the git webhook listener for regression gates.

```bash
# Local (default bind is loopback)
buckley git-webhook --bind 127.0.0.1:8085

# Remote webhook receiver (requires a shared secret)
buckley git-webhook --bind 0.0.0.0:8085 --secret <webhook-secret>
```

See [Regression Gate documentation](../README.md#regression-gate--release-automation) for configuration.

### agent-server

Start a small HTTP proxy that bridges editor workflows to the ACP gRPC server.

```bash
buckley agent-server --bind 127.0.0.1:5555 --acp-target 127.0.0.1:50051
```

Connect to a remote ACP server with mTLS:

```bash
buckley agent-server \
  --bind 127.0.0.1:5555 \
  --acp-target acp.example.com:50051 \
  --acp-ca-file ./acp-ca.pem \
  --acp-client-cert ./acp-client.pem \
  --acp-client-key ./acp-client-key.pem
```

## Interactive Commands

When running in interactive mode, use `/` prefix for commands:

| Command | Description |
|---------|-------------|
| `/help` | List all commands |
| `/quit`, `/exit` | Exit Buckley |
| `/clear` | Clear conversation |
| `/new` | Start new session |
| `/plan <name> <desc>` | Create feature plan |
| `/execute [task-id]` | Execute plan or task |
| `/status` | Show current status |
| `/plans` | List available plans |
| `/resume <plan-id>` | Resume a plan |
| `/pr` | Generate pull request |
| `/hunt` | Scan for code improvements |
| `/dream` | Get architectural ideas |
| `/search <query>` | Semantic code search |
| `/tools` | List available tools |
| `/models [filter]` | List available models |
| `/model <id>` | Switch to a different model |
| `/usage` | Show token/cost statistics |
| `/history [count]` | Show conversation history |
| `/export [file]` | Export conversation |
| `/config` | Show configuration |
| `/agents init` | Create AGENTS.md template |
| `/agents show` | Display project rules |
| `/agents reload` | Reload AGENTS.md |
| `/services status` | Show container service status |
| `/services up` | Start all services |
| `/services down [-v]` | Stop services |
| `/deps status` | Check dependency updates |
| `/sessions complete [id]` | Mark session completed |
| `/workflow status` | Show workflow state |
| `/workflow pause <note>` | Pause automation |
| `/workflow resume <note>` | Resume automation |

## Environment Variables

### Required

| Variable | Description |
|----------|-------------|
| `OPENROUTER_API_KEY` | OpenRouter API key (recommended) |

**Alternative providers** (set one or more):
- `OPENAI_API_KEY` - OpenAI API key
- `ANTHROPIC_API_KEY` - Anthropic API key
- `GOOGLE_API_KEY` - Google AI API key

### Optional

| Variable | Description |
|----------|-------------|
| `BUCKLEY_MODEL_PLANNING` | Override planning model |
| `BUCKLEY_MODEL_EXECUTION` | Override execution model |
| `BUCKLEY_MODEL_REVIEW` | Override review model |
| `BUCKLEY_MODEL_COMMIT` | Override model for `buckley commit` |
| `BUCKLEY_MODEL_PR` | Override model for `buckley pr` |
| `BUCKLEY_PROMPT_COMMIT` | Custom commit prompt template |
| `BUCKLEY_PROMPT_PR` | Custom PR prompt template |
| `BUCKLEY_PR_BASE` | Override PR base branch |
| `BUCKLEY_TRUST_LEVEL` | Trust level: conservative, balanced, autonomous |
| `BUCKLEY_APPROVAL_MODE` | Approval mode: ask, safe, auto, yolo |
| `BUCKLEY_USE_TOON` | Enable TOON encoding (true/false) |
| `BUCKLEY_DISABLE_TOON` | Disable TOON encoding (true/false) |
| `BUCKLEY_QUIET` | Suppress non-essential output |
| `BUCKLEY_IPC_TOKEN` | IPC authentication token |
| `BUCKLEY_BASIC_AUTH_ENABLED` | Enable basic auth (true/false) |
| `BUCKLEY_BASIC_AUTH_USER` | Basic auth username |
| `BUCKLEY_BASIC_AUTH_PASSWORD` | Basic auth password |
| `BUCKLEY_SANDBOX` | Container mode: container, host, off |
| `BUCKLEY_REMOTE_BRANCH` | Default remote branch for pushes |
| `BUCKLEY_REMOTE_NAME` | Default remote name (default: origin) |
| `NO_COLOR` | Disable colored output |

## Examples

### Basic Usage

```bash
# Start interactive session
buckley

# Quick question (one-shot)
buckley -p "How do I implement pagination in this API?"

# Generate commit for staged changes
git add -p && buckley commit

# Create PR for current branch
buckley pr
```

### CI/CD Integration

```bash
# Execute a specific task in CI
buckley execute-task \
  --plan feature-auth \
  --task implement-jwt \
  --remote-branch automation/feature-auth

# Run without interaction
buckley --quiet -p "Run tests and fix any failures"
```

### Remote Development

```bash
# Start server for team access
buckley serve \
  --bind 0.0.0.0:4488 \
  --require-token \
  --browser

# Connect from another machine
buckley remote attach \
  --url https://buckley.example.com \
  --session $SESSION_ID \
  --token $BUCKLEY_IPC_TOKEN
```

### Scripting

```bash
#!/bin/bash
# Automated code review script

# Check for uncommitted changes
if ! git diff --quiet; then
  echo "Reviewing changes..."
  buckley --quiet -p "Review the following diff for issues: $(git diff)"
fi
```

## See Also

- [Configuration Reference](CONFIGURATION.md)
- [Error Codes](ERRORS.md)
- [Release Process](RELEASE.md)
