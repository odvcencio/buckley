# AGENTS.md

This document is the single source of truth for **any** agent (human or automated) contributing to Buckley. Follow it regardless of model vendor. When in doubt, prioritize clarity, safety, and the XP mindset: plan deliberately, ship iteratively, and keep the repo in a working state.

**OSS note:** Treat this as a public project. Keep secrets out of commits, prefer localhost binds unless explicitly required, and favor small, reviewable changes that keep the tree working at all times.

## Collaboration Principles

- Treat every task as pair-programming with maintainers: explain reasoning, cite files, and leave the tree clean.
- Read `git status` before and after work; never discard user changes and avoid touching unrelated files.
- Prefer incremental plans (plan → implement → test → summarize). Deviation is fine when justified in the transcript.
- Default to running `./scripts/test.sh` for meaningful changes. Run `go test ./...` only when you specifically need coverage outside the core packages.
- Keep edits reproducible: mention required commands, environment variables, or migrations.
- Document non-obvious behavior with concise comments; avoid restating what the code clearly expresses.
- Avoid creating documentation files (*.md, docs/) unless explicitly requested. Reduce doc bloat and sprawl.
- If a user overrides or restates a convention (naming, formatting, workflow), treat that override as the new local convention for the rest of the session.

## Execution Playbook

1. **Discover**: inspect context (`README.md`, `AGENTS.md`, relevant package code).
2. **Plan**: outline steps when work spans multiple files or contains risk. Skip only for trivial edits.
3. **Implement**:
   - Use `go fmt`/language-idiomatic formatters automatically where possible.
   - Favor `rg`/`fd` for search; avoid destructive git commands.
4. **Validate**: run targeted tests first (`./scripts/test.sh`, `go test ./pkg/<pkg>`), then broader suites when necessary. Add new tests when fixing bugs or adding behavior.
5. **Report**: summarize changes, reference touched files with line numbers, and highlight manual follow-ups.

### Build & Run

```bash
# Build the binary
go build -o buckley ./cmd/buckley

# Build and run
go build -o buckley ./cmd/buckley && ./buckley

# Run directly without producing an artifact
go run ./cmd/buckley
```

### Testing

```bash
# Fast suite (pkg + CLI)
./scripts/test.sh

# With custom flags (e.g., race detector)
./scripts/test.sh -race

# Focused suites
go test ./pkg/config
go test ./pkg/conversation -run TestDefaultConfig
```

### Environment & Secrets

- Go 1.21+
- OpenRouter API key (`export OPENROUTER_API_KEY=your-key-here`)
- Git (worktree features) and optionally Docker for containerized workflows
- Keep secrets out of commits; prefer env vars over files.

## Architecture Overview

Buckley is a terminal-based AI development assistant built on four pillars:

1. **Multi-model orchestration** via OpenRouter
2. **Plan-first, resumable execution** for long-running features
3. **Persistent memory** with automatic compaction
4. **One-shot commands** for commit, PR, and custom workflows

```
cmd/buckley/           # CLI entry point
pkg/
  ├── config/          # Hierarchical config: defaults → user → project → env
  ├── model/           # OpenRouter catalog + client
  ├── conversation/    # Sessions, compaction, token logic
  ├── storage/         # SQLite persistence
  ├── tool/            # Built-in + external plugin registry
  ├── orchestrator/    # Planner/executor/review workflow
  ├── ui/              # TUI implementation
  ├── session/         # Git-based session discovery
  ├── cost/            # Budget tracking
  ├── worktree/        # Git worktree helpers
  ├── github/          # gh CLI integration
  ├── oneshot/         # One-shot command infrastructure
  ├── api/             # REST API server for headless mode
  └── notify/          # Async notifications (NATS, Telegram, Slack)
```

## Key Subsystems

**Tool Usage**
Tools are provided programmatically with full JSON schemas via OpenAI function calling format. Always use the exact tool schemas provided in the API request—this file describes architecture guidance, tool schemas are authoritative.

**Model Routing** (`pkg/model/`)
- Distinct planning/execution/review models validated against the OpenRouter catalog.
- Streaming-first HTTP client for responsive TUI updates.

**Tool System** (`pkg/tool/`)
- Built-in capabilities plus manifest-driven external plugins (`tool.yaml`).
- Plugins spawn as short-lived subprocesses, exchanging JSON via stdin/stdout.
- Discovery paths: `~/.buckley/plugins/`, `./.buckley/plugins/`, `./plugins/`.

**Memory & Compaction** (`pkg/conversation/`)
- Token estimates via tiktoken-go with fallback heuristics.
- Automatic compaction trims older messages at 90% context usage.

**Orchestrator** (`pkg/orchestrator/`)
```
/plan    → generate task list
/execute → state machine (Pending → InProgress → Completed/Failed) with self-healing retries
/review  → targeted feedback loops
/pr      → generate conventional commits + PR text
```

**Configuration** (`pkg/config/`)
```
Built-in defaults
→ ~/.buckley/config.yaml (user)
→ ./.buckley/config.yaml (project)
→ environment variables (highest priority)
```

## Plugin Development

1. Create `~/.buckley/plugins/<name>/tool.yaml`:
   ```yaml
   name: my_tool
   description: One-line summary
   parameters:
     type: object
     properties:
       param1:
         type: string
         description: Description
     required: [param1]
   executable: ./my_tool.sh
   timeout_ms: 60000
   ```

2. Implement the executable (any language) to read JSON from stdin and emit JSON to stdout:
   ```bash
   #!/bin/bash
   INPUT=$(cat)
   PARAM1=$(echo "$INPUT" | jq -r '.param1')
   # ...do work...
   cat <<'JSON'
   {"success": true, "data": {"output": "result"}}
   JSON
   ```

3. `chmod +x` the executable. Buckley discovers plugins at startup.

## Slash Commands

- `/help` – list commands
- `/usage` – token/cost stats
- `/quit` – exit TUI
- `/models [filter]`, `/model <id>` – inspect or switch models
- `/clear`, `/new`, `/history [count]`, `/export [file]` – conversation management
- `/plan <name> <desc>`, `/execute [task-id]`, `/status`, `/plans`, `/resume <plan-id>`, `/pr` – orchestrator lifecycle
- `/tools`, `/config` – tooling and config introspection

## Contributing

- Keep this file canonical; other agent-specific guides should reference `@AGENTS.md`.
- Update `pkg/context/loader.go` if the schema or format changes.
