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
| `--code-mode` | | Offer audited, isolated `exec_program` for batched repository analysis |
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

### TUI chat shortcuts

| Shortcut | Action |
| --- | --- |
| `Enter` | Send the current prompt |
| `Shift+Enter` | Insert a newline without sending |
| `Alt+C` | Copy the latest code block |

Fenced code blocks use Tree-sitter syntax highlighting when Buckley has a
grammar for the declared language. Other code blocks remain readable with the
standard code style.

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

### ralph

Run Ralph autonomous sessions for long-running tasks.

```bash
buckley ralph --prompt "Audit the repo for flaky tests" --timeout 45m
buckley ralph --prompt-file ./prompt.txt --dir /path/to/worktree
```

**Session management:**
```bash
buckley ralph list
buckley ralph control --status
buckley ralph control --set backends.buckley.enabled=false
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
| `--yes` | Skip confirmation and create the commit |
| `--push=false` | Create the commit without pushing the current branch |
| `--paths <path>` | Commit only matching staged paths; repeatable |
| `--exclusive` | With `--paths`, reject staged files outside the selected paths |
| `--context-trailer=false` | Omit the privacy-preserving change digest and aggregate stats trailers |
| `--minimal-output` | Minimize terminal output |
| `--trace` | Show context audit and model reasoning after generation |
| `--model <id>` | Override the commit-message model |
| `--backend <api|codex|claude>` | Select the one-shot backend |
| `--timeout <duration>` | Bound message generation (default `2m`) |

**Environment Variables:**
- `BUCKLEY_MODEL_COMMIT` - Override model for commit generation
- `BUCKLEY_PROMPT_COMMIT` - Override prompt template

**Example:**
```bash
git add -p                    # Stage changes
buckley commit                # Generate, confirm, create, and push
buckley commit --dry-run      # Preview commit message
buckley commit --yes --push=false  # Create without confirmation or push
```

Generated commits include `Buckley-Change-Hash` and `Buckley-Change-Stats`
trailers by default. The hash binds the message to the exact staged diff; the
stats record only aggregate file and line counts. No paths, filenames, or diff
content are stored in these trailers. Buckley rechecks the staged diff before
committing and aborts if it changed after message generation. Disable the
trailers with `--context-trailer=false`; the staged-diff recheck still runs.

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

### buckbot

Buckbot is Buckley's general-purpose review agent. It uses the same governed
review runtime as `review` and `review-pr`, while making the review intent
explicit in scripts, CI, and team workflows.

Unless overridden with `--model` or `BUCKLEY_MODEL_REVIEW`, Buckbot uses
`deepseek/deepseek-v4-pro-0813` through OpenRouter. The account's privacy and
guardrail settings must allow a matching endpoint.

If the configured Buckbot policy is
`openrouter_privacy_fallback: zdr_then_data_collection_deny`, Buckley tries a
ZDR route first and retries one policy-filtered 404 with
`data_collection: deny`. This makes DeepSeek usable when it has no ZDR route,
but it is not equivalent to ZDR; leave the setting disabled for strict
zero-retention reviews.

```bash
buckley buckbot                         # review current local scope
buckley buckbot --scope branch          # review the current branch
buckley buckbot repo                    # advisory repository-wide assessment
buckley buckbot repo --depth balanced   # evidence map plus targeted falsification
buckley buckbot repo --depth in-depth   # exhaustive coverage and verification
buckley buckbot pr 123                  # review a GitHub PR without posting
buckley buckbot pr 123 --post           # explicitly post that PR review
buckley buckbot --max-tool-calls 12      # opt into an experimental tool cap
```

`buckley buckbot repo` is equivalent to `buckley review --project`. It is
repository-wide, advisory-only, and cannot issue an approval verdict. Posting
to GitHub is never implicit: it requires `--post` on a PR review.

Repository reviews start with a complete Git-visible tracked-file inventory,
plus a filtered immutable capture of safe non-ignored untracked text files in
project/worktree reviews. The map is a navigable table of contents:
it includes repository metrics, complexity hotspots, major call sites, and
direct caller/callee edges for important flows, along with call-graph and parse
health. The model must expand that map into a coverage ledger for every
tracked source, test, configuration, and documentation area. Long files are
read in explicit pages, so a large file is not silently discarded. A report is
emitted only when it declares `COMPLETE` coverage; a timed-out or incomplete
pass is discarded rather than presented as a caveated review. Project reviews default to a 20-minute wall-clock window; use
`--timeout 45m` (or another explicit duration) for a larger repository. The
snapshot boundary excludes ignored paths, agent instructions, secret-like
paths/content, symlinks, binary/control content, and oversized files; excluded
paths are listed in the review rather than silently treated as inspected.

Every review surface supports three depth modes. `--depth spot` is the fast
health scan and is the default for compatibility. `--depth balanced` performs
a map-then-falsify pass and requires a model-directed verification attempt when
a repository gate exists. `--depth in-depth` removes generated turn/tool
ceilings (explicit operator caps still win), expands important call and state
paths, runs an adversarial pass, and requires a coverage plus verification
ledger. `--in-depth` is a shorthand for `--depth in-depth`; `standard`,
`detailed`, and `exhaustive` are accepted aliases for the corresponding modes.
No-test gates are typed as not applicable; required unavailable evidence fails
the pass instead of being silently counted as passing evidence.

Buckbot is uncapped by default: a model is allowed to finish its review rather
than being truncated by an automatic dollar ceiling. Use `--budget <USD>` only
when a run needs an explicit spending limit. If an older project configuration
sets `per_review_budget_usd`, use `--no-budget` for a completion-first review.
Reviews also have no default per-review tool-call cap. Use
`--max-tool-calls <N>` when experimenting with an expensive model and you want
an explicit inspection/verification ceiling. The monetary and tool-call caps
are independent. Project reviews also have no hard model-turn or exploration
ceiling by default; the ordinary outer timeout, synthesis reserve, verification,
and governor safety controls still apply.

Child runs use an explicit execution contract. `step_cap`, `max_tool_calls`,
`max_model_requests`, `max_elapsed_seconds`, `max_cost_usd`, and
`timeout_seconds` can narrow an individual `spawn_subagent` request. Zero or an
omitted value means no contract-specific ceiling; repetition/cycle protection,
provider limits, cancellation, and operator emergency fuses still apply. When
action work stops after gathering evidence, Buckley reserves a final
tools-disabled synthesis request when the declared model-request and cost
ceilings still allow it. Model-request, tool, and elapsed ceilings are hard
harness limits. A cost ceiling is a conservative client-side admission limit,
not provider-side payment authorization: Buckley prices an explicit input and
output envelope before dispatch and rejects a response and all of its tool
calls if provider-reported usage crosses the remainder. Provider billing or
token accounting can still differ after a request has been accepted upstream.
The child is explicitly incomplete whenever Buckley cannot safely admit or use
the request.
Child stdout and stderr stream to a private 32 MiB spool; snapshots keep only a
256 KiB head/tail preview, while the retained transcript is pinned to evidence
before the spool is removed. A child that reaches the spool ceiling fails
explicitly with observed and retained byte counts.

For API models, Buckbot automatically exposes audited read-only code mode on
the immutable review snapshot when bubblewrap is working. This lets a model
compose repository-wide inventories and cross-file checks in one program
without network access or a mount of the live checkout. If the sandbox is
unavailable, Buckbot keeps the ordinary snapshot-rooted inspection tools. An
exact `--model` value remains exact in both cases.

### review-pr

Review a GitHub pull request. The command uses dry-run output by default.

```bash
buckley review-pr 123
buckley review-pr 123 -post
```

Use `-post` only when you want Buckbot to write to GitHub. A posted review adds
an eyes reaction, an intake comment, and the final review.

Buckbot binds review evidence to one PR head and its CI state. Re-run a review
when the head or CI checks change during a long-running pass.

### Prose style (ASD-STE100)

Buckley writes commit messages, PR titles, and PR bodies in ASD-STE100
(Simplified Technical English). ASD-STE100 is a controlled-language
writing standard.

Follow these rules for generated and hand-written prose:
- Use active voice. Use the imperative mood for instructions.
- Keep procedural sentences at or below 20 words. Keep descriptive sentences at or below 25 words.
- Give each word one meaning. Use it the same way every time.
- Do not write noun clusters of more than three nouns.
- Define an abbreviation at first use.

`buckley review` checks this rule too. It flags ASD-STE100 violations in
commit messages, PR descriptions, and added doc or comment text. Each
flag includes a suggested rewrite.

These rules govern prose only. The commit header format, the
72-character limit, and the JSON output contract for PRs stay unchanged.

### serve

Start the local HTTP/WebSocket IPC server (and optional embedded Mission Control UI).

```bash
buckley serve [OPTIONS]
```

**Options:**
| Flag | Default | Description |
|------|---------|-------------|
| `--bind` | `127.0.0.1:4488` | Address to bind |
| `--browser` | `false` | Enable the embedded GoSX Mission Control UI |
| `--assets` | | Optional external static UI override |
| `--allow-origin` | | Additional allowed CORS origins (repeatable) |
| `--require-token` | `false` | Require authentication token |
| `--auth-token` | | Set authentication token |

**Example:**
```bash
# Serve the embedded GoSX Mission Control UI
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

See the [`git_events` configuration](./CONFIGURATION.md#git_events) for the
listener and release-command settings.

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
| `BUCKLEY_CODE_MODE` | Enable audited code mode for interactive or one-shot use |
| `BUCKLEY_ADAPTIVE_PROTOCOL_MODE` | Override adaptive protocol mode: legacy, shadow, or dynamic |
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
- [Troubleshooting](troubleshooting.md)
- [Running Durable Goals](goals.md)
- [Mission Control](MISSION_CONTROL.md)
