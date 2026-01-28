# Buckley Configuration Reference

Complete reference for all configuration options.

## Configuration Hierarchy

Buckley loads configuration in this order (later sources override earlier):

```
1. Built-in defaults
   ↓
2. User config: ~/.buckley/config.yaml
   ↓
3. Project config: ./.buckley/config.yaml
   ↓
4. Environment variables (highest priority)
```

## Configuration Files

| Path | Purpose |
|------|---------|
| `~/.buckley/config.yaml` | User-wide settings |
| `./.buckley/config.yaml` | Project-specific overrides |
| `~/.buckley/config.env` | Environment variables (sourced on load) |
| `~/.buckley/buckley.db` | SQLite database (sessions, history). Override with `BUCKLEY_DB_PATH` or `BUCKLEY_DATA_DIR`. |
| `~/.buckley/buckley-acp-events.db` | ACP event store (when `acp.event_store=sqlite`). Override with `BUCKLEY_ACP_EVENTS_DB_PATH` or `BUCKLEY_DATA_DIR`. |
| `~/.buckley/remote-auth.json` | CLI remote login session cookie jar. Override with `BUCKLEY_REMOTE_AUTH_PATH` (or `BUCKLEY_DATA_DIR`). |
| `~/.buckley/checkpoints/` | JSON checkpoints. Override with `BUCKLEY_CHECKPOINTS_DIR` (or `BUCKLEY_DATA_DIR`). |
| `./.buckley/logs/` | Default log directory. Override with `BUCKLEY_LOG_DIR`. |

## Quick Start Examples

### Minimal Configuration

```yaml
# ~/.buckley/config.yaml
models:
  planning: anthropic/claude-sonnet-4-5
  execution: anthropic/claude-sonnet-4-5
  review: anthropic/claude-sonnet-4-5
```

### Project-Specific Configuration

```yaml
# ./.buckley/config.yaml
orchestrator:
  trust_level: balanced
  max_self_heal_attempts: 5

approval:
  mode: auto
  trusted_paths:
    - ./src
    - ./tests
```

## Configuration Sections

### models

Model selection for different workflow phases.

```yaml
models:
  # Primary models for each phase
  planning: anthropic/claude-sonnet-4-5
  execution: anthropic/claude-sonnet-4-5
  review: anthropic/claude-sonnet-4-5

  # Default provider when model prefix doesn't specify
  default_provider: openrouter  # openrouter | openai | anthropic | google

  # Reasoning effort level
  reasoning: medium  # off | low | medium | high | "" (auto-detect)

  # Vision model fallback chain (tried in order)
  vision_fallback:
    - openai/gpt-5-nano
    - google/gemini-3-flash

  # Model fallback chains for resilience
  fallback_chains:
    anthropic/claude-sonnet-4-5:
      - anthropic/claude-3-haiku
      - openai/gpt-5.2-codex-xhigh-mini

  # Utility models for lightweight tasks
  utility:
    commit: openai/gpt-5.2-codex-xhigh-mini
    pr: openai/gpt-5.2-codex-xhigh-mini
    compaction: openai/gpt-5.2-codex-xhigh-mini
    todo_plan: openai/gpt-5.2-codex-xhigh-mini
```

**Defaults:**

| Field | Default |
|-------|---------|
| `planning` | `moonshotai/kimi-k2-thinking` |
| `execution` | `moonshotai/kimi-k2-thinking` |
| `review` | `moonshotai/kimi-k2-thinking` |
| `default_provider` | `openrouter` |
| `reasoning` | `""` (auto-detect) |
| `utility.commit` | `openai/gpt-5.2-codex-xhigh-mini` |
| `utility.pr` | `openai/gpt-5.2-codex-xhigh-mini` |
| `utility.compaction` | `openai/gpt-5.2-codex-xhigh-mini` |
| `utility.todo_plan` | `openai/gpt-5.2-codex-xhigh-mini` |

### providers

API provider configuration.

```yaml
providers:
  openrouter:
    enabled: true
    api_key: ""  # Use env var instead: OPENROUTER_API_KEY
    base_url: https://openrouter.ai/api/v1

  openai:
    enabled: false
    api_key: ""  # OPENAI_API_KEY
    base_url: https://api.openai.com/v1

  anthropic:
    enabled: false
    api_key: ""  # ANTHROPIC_API_KEY
    base_url: https://api.anthropic.com/v1

  google:
    enabled: false
    api_key: ""  # GOOGLE_API_KEY
    base_url: https://generativelanguage.googleapis.com/v1beta

  # Route models by prefix to specific providers
  model_routing:
    openai/: openai
    anthropic/: anthropic
    google/: google
    gpt-: openai
    claude-: anthropic
    gemini-: google
```

**Security Note:** Never commit API keys in config files. Use environment variables or `~/.buckley/config.env`.

### prompt_cache

Provider prompt caching controls.

```yaml
prompt_cache:
  enabled: true
  providers: [openrouter, litellm, openai]
  system_messages: 2
  tail_messages: 8
  key: "project-cache-key"
  retention: "24h" # in-memory | 24h
```

**Selection & behavior:**
- If `providers` is empty, caching applies to any provider that supports it.
- `openrouter` and `litellm` trim messages to the last `system_messages` + `tail_messages` for OpenAI-compatible caching.
- `openai` uses `prompt_cache_key` and `prompt_cache_retention` (no message trimming).
- Per-request `prompt_cache` settings override config defaults.

### diagnostics

Debugging and logging controls.

```yaml
diagnostics:
  # Log LLM provider HTTP requests/responses to network.jsonl under BUCKLEY_LOG_DIR (default: .buckley/logs/network.jsonl).
  # WARNING: This can capture prompts and code; keep disabled unless debugging.
  network_logs_enabled: false
```

**Environment overrides:**
- `BUCKLEY_NETWORK_LOGS_ENABLED=true` - Enable network request/response logging
- `BUCKLEY_DISABLE_NETWORK_LOGS=true` - Force-disable network request/response logging

### encoding

Serialization preferences.

```yaml
encoding:
  use_toon: true  # Use TOON format (saves ~30% tokens)
```

TOON is a compact serialization format. Set `false` for standard JSON when integrating with external tools.

**Environment overrides:**
- `BUCKLEY_USE_TOON=false` - Disable TOON
- `BUCKLEY_DISABLE_TOON=true` - Disable TOON

### orchestrator

Feature workflow orchestration settings.

```yaml
orchestrator:
  # Trust level for autonomous operations
  trust_level: balanced  # conservative | balanced | autonomous

  # Auto-retry failed operations
  max_self_heal_attempts: 3

  # Maximum review iterations before requiring approval
  max_review_cycles: 3

  # Automatically start workflow on feature request
  auto_workflow: false

  # Planning mode settings
  planning:
    enabled: true
    complexity_threshold: 0.6  # Score above triggers planning
    planning_model: ""         # Use execution model if empty

    # Long-run autonomous mode
    long_run_enabled: false
    long_run_max_minutes: 30
    long_run_log_decisions: true
    long_run_pause_on_risk: true
```

**Trust Levels:**

| Level | Behavior |
|-------|----------|
| `conservative` | Pause for approval on most operations |
| `balanced` | Auto-approve safe operations, pause on risky |
| `autonomous` | Minimal interruptions, high trust |

### approval

Permission and safety settings.

```yaml
approval:
  # Approval mode
  mode: safe  # ask | safe | auto | yolo

  # Additional paths with write access (beyond workspace)
  trusted_paths: []

  # Paths never writable (even in yolo mode)
  denied_paths:
    - ~/.ssh
    - ~/.gnupg
    - ~/.aws
    - /etc
    - /var

  # Allow network access without prompting (in auto mode)
  allow_network: false

  # Tools that can run without approval (in ask mode)
  allowed_tools:
    - read_file
    - list_files
    - search_files
    - semantic_search

  # Tools that always require approval (even in yolo mode)
  denied_tools: []

  # Shell command patterns that auto-approve
  auto_approve_patterns:
    - go test
    - go build
    - npm test
    - make test
    - pytest
```

**Approval Modes:**

| Mode | Description |
|------|-------------|
| `ask` | Explicit approval for all writes and commands |
| `safe` | Read anything, write workspace only, shell needs approval |
| `auto` | Full workspace access, approval for external operations |
| `yolo` | Full autonomy (dangerous, use with caution) |

### memory

Conversation memory and compaction.

```yaml
memory:
  # Trigger compaction at this context usage percentage
  auto_compact_threshold: 0.9

  # Maximum compactions per session (0 = unlimited)
  max_compactions: 0

  # Enable semantic retrieval from past conversations
  retrieval_enabled: true
  retrieval_limit: 5
  retrieval_max_tokens: 1200
```

### cost_management

Budget tracking and limits.

```yaml
cost_management:
  session_budget: 10.00   # Per-session limit in USD
  daily_budget: 20.00     # Daily limit
  monthly_budget: 200.00  # Monthly limit
  auto_stop_at: 50.00     # Pause when remaining budget hits this
```

When a budget is exceeded, Buckley pauses and asks for confirmation before continuing.

### git_clone

Controls which git clone URLs are allowed when Buckley needs to clone a repository (headless sessions, batch workers).

```yaml
git_clone:
  # Empty list means "allow all".
  allowed_schemes: [https, ssh]

  # Optional host allow/deny lists (supports exact hosts, suffixes like ".example.com", and wildcards like "*.example.com")
  allowed_hosts: []
  denied_hosts: []

  # When true and allowed_hosts is empty, reject loopback/private/link-local IPs (and hostnames that resolve to them).
  deny_private_networks: false
  resolve_dns: true
  dns_resolve_timeout_seconds: 2

  # scp-style git URLs like git@github.com:org/repo.git
  deny_scp_syntax: false
```

**Environment overrides:**
- `BUCKLEY_GIT_ALLOWED_SCHEMES`
- `BUCKLEY_GIT_ALLOWED_HOSTS`
- `BUCKLEY_GIT_DENIED_HOSTS`
- `BUCKLEY_GIT_DENY_PRIVATE_NETWORKS`
- `BUCKLEY_GIT_RESOLVE_DNS`
- `BUCKLEY_GIT_DENY_SCP_SYNTAX`
- `BUCKLEY_GIT_DNS_TIMEOUT_SECONDS`

### ipc

HTTP/WebSocket server for desktop UI and remote access.

```yaml
ipc:
  enabled: false
  bind: 127.0.0.1:4488
  enable_browser: false

  # CORS allowed origins
  allowed_origins:
    - http://localhost
    - http://127.0.0.1

  # Observability
  public_metrics: false # Expose /metrics without auth (healthz is always public)

  # Authentication
  require_token: false
  basic_auth_enabled: false
  basic_auth_username: ""
  basic_auth_password: ""

  # Web push (for notifications)
  push_subject: ""  # mailto: or https: URL
```

**Security:** When binding to non-localhost addresses, authentication is required.

### mcp

Model Context Protocol (MCP) server integration.

```yaml
mcp:
  enabled: true
  servers:
    - name: filesystem
      command: /path/to/mcp-server
      args: ["--stdio"]
      env:
        MCP_LOG_LEVEL: info
      timeout: 30s
      disabled: false
```

Each server command is launched as a subprocess and must speak MCP JSON-RPC over stdio.

### acp

Zed Agent Communication Protocol (ACP) gRPC server.

```yaml
acp:
  event_store: sqlite  # sqlite | nats
  listen: ""           # Empty = disabled, e.g., "127.0.0.1:50051"
  allow_insecure_local: false

  # TLS configuration (required for non-localhost)
  tls_cert_file: ""
  tls_key_file: ""
  tls_client_ca_file: ""

  # NATS configuration (when event_store: nats)
  nats:
    url: nats://127.0.0.1:4222
    username: ""
    password: ""
    token: ""
    tls: false
    stream_prefix: acp
    snapshot_bucket: acp_snapshots
    connect_timeout: 5s
    request_timeout: 5s
```

### worktrees

Git worktree management.

```yaml
worktrees:
  use_containers: false
  root_path: "" # optional; blank keeps operations scoped to the current repo
  container_service: dev
```

### batch

Kubernetes batch execution for CI/CD.

```yaml
batch:
  enabled: false
  namespace: ""
  kubeconfig: ""
  wait_for_completion: true
  follow_logs: true

  job_template:
    image: ""
    image_pull_policy: IfNotPresent
    service_account: ""
    command: [buckley]
    args:
      - execute-task
      - --plan
      - "{{PLAN_ID}}"
      - --task
      - "{{TASK_ID}}"
      - --remote-branch
      - "{{REMOTE_BRANCH}}"
      - --remote-name
      - "{{REMOTE_NAME}}"
    env:
      BUCKLEY_PLAIN_MODE: "1"
    env_from_secrets: []
    env_from_configmaps: []
    config_map: ""
    config_map_mount_path: "/home/buckley/.buckley/config.yaml"

    # Workspace volume selection
    workspace_claim: ""
    workspace_mount_path: /workspace
    workspace_volume_template:
      storage_class: ""
      access_modes: [ReadWriteOnce]
      size: 20Gi

    # Shared config volume (recommended for hosted mode)
    shared_config_claim: ""
    shared_config_mount_path: /buckley/shared

    image_pull_secrets: []
    resources: {}
    node_selector: {}
    tolerations: []
    affinity: {}

    ttl_seconds_after_finished: 600
    backoff_limit: 1

  remote_branch:
    enabled: true
    prefix: automation/
    remote_name: origin
```

`buckley execute-task` expects to run inside a git repository. In Kubernetes jobs, set `job_template.env.BUCKLEY_TASK_WORKDIR` (default `/workspace`) and either mount a repo at that path or set `BUCKLEY_REPO_URL` so the worker can clone into the workspace volume before executing.

### retry_policy

Retry behavior for transient errors.

```yaml
retry_policy:
  max_retries: 3
  initial_backoff: 1s
  max_backoff: 30s
  multiplier: 2.0
```

### artifacts

File locations for plans, execution logs, and archives.

```yaml
artifacts:
  planning_dir: docs/plans
  execution_dir: docs/execution
  review_dir: docs/reviews
  archive_dir: docs/archive
  archive_by_month: true
  auto_archive_on_pr_merge: true
```

### workflow

Workflow behavior settings.

```yaml
workflow:
  planning_questions_min: 5
  planning_questions_max: 10
  incremental_approval: true
  pause_on_business_ambiguity: true
  pause_on_architectural_conflict: true
  pause_on_complexity_explosion: true
  pause_on_environment_mismatch: true
  review_iterations_max: 5
  allow_nits_in_approval: true
  generate_opportunistic_improvements: true

  task_phase_loop: [builder, verify, review]
  task_phases:
    - stage: builder
      name: Builder
      description: Generate and apply code changes
      targets: [Translate plan into code, Run tools/commands]
    - stage: verify
      name: Verifier
      description: Validate results locally
      targets: [Run tests/linters, Check edge cases]
    - stage: review
      name: Reviewer
      description: Review and enforce quality
      targets: [Catch regressions, Ensure conventions]
```

### compaction

Conversation compaction settings.

```yaml
compaction:
  context_threshold: 0.80
  task_interval: 20
  token_threshold: 15000
  target_reduction: 0.70
  preserve_commands: true
  models:
    - deepseek/deepseek-chat
    - openai/gpt-5.2-codex-xhigh-mini
```

### ui

User interface preferences.

```yaml
ui:
  activity_panel_default: collapsed  # collapsed | expanded
  diff_viewer_default: collapsed
  tool_grouping_window_seconds: 30
  show_tool_costs: true
  show_intent_statements: true

  # Accessibility
  high_contrast: false
  use_text_labels: false
  reduce_animation: false
```

### personality

AI personality settings.

```yaml
personality:
  enabled: true
  quirk_probability: 0.15
  tone: friendly  # professional | friendly | quirky
  default_persona: ""
  phase_overrides: {}
```

### commenting

Code commenting requirements.

```yaml
commenting:
  require_function_docs: true
  require_block_comments_over_lines: 10
  comment_non_obvious_only: true
```

### git_events

Git webhook and automation settings.

```yaml
git_events:
  enabled: false
  secret: ""  # Webhook secret for validation
  auto_regression_plan: false
  webhook_bind: ":8085"
  regression_command: "./scripts/test.sh"
  release_command: "./deploy/push-image.sh"
  failure_command: "./notify/regression_failed.sh"
```

### input

Multimodal input processing.

```yaml
input:
  transcription:
    provider: api  # api | system | hybrid
    whisper_model: whisper-1
    api_endpoint: ""
    timeout: 60

  video:
    enabled: false
    max_frames: 5
    extract_audio: true
    ffmpeg_path: ""
```

## Environment Variables Reference

### API Keys

| Variable | Description |
|----------|-------------|
| `OPENROUTER_API_KEY` | OpenRouter API key |
| `OPENAI_API_KEY` | OpenAI API key |
| `ANTHROPIC_API_KEY` | Anthropic API key |
| `GOOGLE_API_KEY` | Google AI API key |

### Model Overrides

| Variable | Description |
|----------|-------------|
| `BUCKLEY_MODEL_PLANNING` | Override planning model |
| `BUCKLEY_MODEL_EXECUTION` | Override execution model |
| `BUCKLEY_MODEL_REVIEW` | Override review model |
| `BUCKLEY_MODEL_COMMIT` | Override commit generation model |
| `BUCKLEY_MODEL_PR` | Override PR generation model |

### Behavior

| Variable | Description |
|----------|-------------|
| `BUCKLEY_TRUST_LEVEL` | Trust level override |
| `BUCKLEY_APPROVAL_MODE` | Approval mode override |
| `BUCKLEY_USE_TOON` | Enable TOON encoding |
| `BUCKLEY_DISABLE_TOON` | Disable TOON encoding |
| `BUCKLEY_QUIET` | Suppress non-essential output |
| `BUCKLEY_SANDBOX` | Container mode (container/host/off) |

### Paths

| Variable | Description |
|----------|-------------|
| `BUCKLEY_DB_PATH` | Override primary SQLite DB path |
| `BUCKLEY_DATA_DIR` | Directory containing Buckley DB files |
| `BUCKLEY_LOG_DIR` | Override log directory (default: `.buckley/logs`) |
| `BUCKLEY_ACP_EVENTS_DB_PATH` | Override ACP SQLite event store path |
| `BUCKLEY_REMOTE_AUTH_PATH` | Override `remote-auth.json` path |
| `BUCKLEY_CHECKPOINTS_DIR` | Override checkpoints directory |

### IPC/Authentication

| Variable | Description |
|----------|-------------|
| `BUCKLEY_IPC_TOKEN` | IPC authentication token |
| `BUCKLEY_IPC_TOKEN_FILE` | Load IPC token from a file (used by `buckley serve`) |
| `BUCKLEY_GENERATE_IPC_TOKEN` | Generate + persist IPC token when missing (used by `buckley serve`) |
| `BUCKLEY_PRINT_GENERATED_IPC_TOKEN` | Print generated IPC token to stderr (used by `buckley serve`) |
| `BUCKLEY_BASIC_AUTH_ENABLED` | Enable basic auth |
| `BUCKLEY_BASIC_AUTH_USER` | Basic auth username |
| `BUCKLEY_BASIC_AUTH_PASSWORD` | Basic auth password |
| `BUCKLEY_PUBLIC_METRICS` | Expose /metrics without authentication |
| `BUCKLEY_PUSH_SUBJECT` | Web push subject |

### Prompts

| Variable | Description |
|----------|-------------|
| `BUCKLEY_PROMPT_COMMIT` | Custom commit prompt template |
| `BUCKLEY_PROMPT_PR` | Custom PR prompt template |
| `BUCKLEY_PR_BASE` | PR base branch override |

### Git

| Variable | Description |
|----------|-------------|
| `BUCKLEY_REMOTE_BRANCH` | Default remote branch |
| `BUCKLEY_REMOTE_NAME` | Default remote name |

### Display

| Variable | Description |
|----------|-------------|
| `NO_COLOR` | Disable colored output |

## Validation

Run `buckley config check` to validate your configuration:

```bash
$ buckley config check
Checking Buckley configuration...

Configuration files:
  ✓ User config:    /home/user/.buckley/config.yaml
  - Project config: .buckley/config.yaml (not found)

API keys:
  ✓ OpenRouter: configured (sk-or-v1...)
  - OpenAI: not set
  - Anthropic: not set
  - Google: not set

Dependencies:
  ✓ git: installed

✓ Configuration is valid
```

## See Also

- [CLI Reference](CLI.md)
- [Error Codes](ERRORS.md)
- [Release Process](RELEASE.md)
