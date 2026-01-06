# Buckley

**The agent harness that doesn't waste your time.**

Buckley is an AI development assistant built for people who ship. Not another chatbot wrapper. Not another "just add an API key" toy. A real engineering tool with parallel execution, self-healing workflows, and the architecture to prove it.

```bash
# Compare models on the same task
buckley experiment run "refactor-auth" \
    -m moonshotai/kimi-k2-thinking \
    -m anthropic/claude-sonnet-4-5 \
    -m openai/gpt-5.2-codex-xhigh \
    -p "Refactor authentication to use JWT"
```

---

## Why Buckley?

**Most AI coding tools are thin wrappers.** They send your prompt to an API, stream the response, and call it a day. When something goes wrong, you start over.

**Buckley is infrastructure.** 216K lines of Go. 67 packages. A real orchestrator with state machines, retry logic, and loop detection. When a task fails, Buckley figures out why and tries again. When it's truly stuck, it asks you—then remembers the answer.

| What You Get | How It Works |
|--------------|--------------|
| **Parallel model comparison** | Run the same task across Kimi, Claude, GPT, and local LLMs simultaneously |
| **Self-healing execution** | Retry with loop detection—won't spam the same failing approach |
| **Human-in-the-loop approvals** | File changes require your sign-off, with unified diffs |
| **Dual interface** | Terminal TUI or web UI—your choice |
| **Provider agnostic** | OpenRouter gives you 100+ models. Ollama runs locally. |
| **Real persistence** | SQLite-backed sessions that survive crashes and resume cleanly |

---

## Quick Start

```bash
# Install
go install github.com/odvcencio/buckley/cmd/buckley@latest

# Or build from source
git clone https://github.com/odvcencio/buckley && cd buckley
go build -o buckley ./cmd/buckley

# Set your API key (OpenRouter recommended—100+ models)
export OPENROUTER_API_KEY="your-key-here"

# Run
./buckley
```

That's it. No Docker required. No database setup. Just Go and an API key.

---

## Three Ways to Use Buckley

### 1. Interactive TUI

```bash
buckley
```

A terminal interface with real-time streaming, tool approvals, and sidebar status. Works over SSH.

### 2. Web UI (Mission Control)

```bash
buckley serve --browser
# Open http://127.0.0.1:4488
```

Full browser UI with conversation history, approval modals, command palette, and live operation visibility.

### 3. Headless API

```bash
buckley api --bind 0.0.0.0:8080
```

REST endpoints for CI/CD. Generate commit messages, PR descriptions, or run custom workflows programmatically.

---

## Core Commands

| Command | What It Does |
|---------|--------------|
| `/plan <name> <desc>` | Create a feature plan with tasks |
| `/execute` | Run the current plan with self-healing |
| `/hunt` | Scan codebase for improvements |
| `/dream` | Get architectural suggestions |
| `/search <query>` | Semantic code search |

### One-Shot Commands

```bash
buckley commit    # AI-generated commit message from staged changes
buckley pr        # AI-generated PR description from branch diff
```

---

## Architecture (Why It Won't Break)

Buckley is built on Domain-Driven Design with Clean Architecture. This isn't marketing—it's why you can trust it with real work.

```
pkg/
├── model/         # LLM routing (planning, execution, review models)
├── orchestrator/  # State machine: Pending → InProgress → Completed/Failed
├── parallel/      # Worktree-based parallel execution
├── conversation/  # Session management with compaction
├── tool/          # 46+ built-in tools + plugin system
├── telemetry/     # Event hub with 20+ event types
├── storage/       # SQLite persistence (WAL mode, 20+ tables)
└── ui/            # TUI (custom widget runtime) + Web (React)
```

**What this means for you:**
- Swap SQLite for Postgres? Change one file.
- Add a new LLM provider? Implement one interface.
- Replace the TUI with a VS Code extension? Domain logic stays the same.

Test coverage: 65% average, 80%+ on core packages. The coordination layer alone (21K lines) has 74-96% coverage across subpackages.

---

## Configuration

Buckley uses hierarchical config with clear precedence:

```
Built-in defaults → ~/.buckley/config.yaml → ./.buckley/config.yaml → Environment variables
```

Minimal config:
```yaml
# ~/.buckley/config.yaml
models:
  planning: moonshotai/kimi-k2-thinking
  execution: moonshotai/kimi-k2-thinking
  review: moonshotai/kimi-k2-thinking

cost_management:
  session_budget: 5.00
```

That's enough to start. See [docs/CONFIGURATION.md](docs/CONFIGURATION.md) for the full reference.

---

## Experiments

The killer feature: run the same task across multiple models and compare results.

```bash
buckley experiment run "add-dark-mode" \
    -m moonshotai/kimi-k2-thinking \
    -m anthropic/claude-sonnet-4-5 \
    -m qwen/qwen3-coder \
    -p "Add dark mode toggle to settings"
```

Each model runs in an isolated git worktree. Results are compared automatically:

```
| Model                          | Success | Duration | Tokens | Files |
|--------------------------------|---------|----------|--------|-------|
| moonshotai/kimi-k2-thinking    | ✓       | 2m 58s   | 3,842  | 6     |
| anthropic/claude-sonnet-4-5    | ✓       | 3m 12s   | 4,231  | 6     |
| qwen/qwen3-coder               | ✓       | 4m 15s   | 6,892  | 5     |
```

Local LLMs via Ollama work identically to cloud models—same tool interface, same approvals.

### How Experiments Work

```
┌─────────────────────────────────────────────────────────────────┐
│                    buckley experiment run                       │
│                                                                 │
│  "add-dark-mode" -m kimi -m claude -m qwen -p "task"           │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                     Parallel Orchestrator                       │
│                                                                 │
│  Creates isolated git worktrees for each variant                │
└─────────────────────────────────────────────────────────────────┘
                              │
         ┌────────────────────┼────────────────────┐
         ▼                    ▼                    ▼
┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐
│   Worktree A    │  │   Worktree B    │  │   Worktree C    │
│                 │  │                 │  │                 │
│  Model: Kimi    │  │  Model: Claude  │  │  Model: Qwen    │
│                 │  │                 │  │                 │
│  ┌───────────┐  │  │  ┌───────────┐  │  │  ┌───────────┐  │
│  │ Tool Loop │  │  │  │ Tool Loop │  │  │  │ Tool Loop │  │
│  │ read_file │  │  │  │ read_file │  │  │  │ read_file │  │
│  │ run_shell │  │  │  │ run_shell │  │  │  │ run_shell │  │
│  │ edit_file │  │  │  │ edit_file │  │  │  │ edit_file │  │
│  └───────────┘  │  │  └───────────┘  │  │  └───────────┘  │
└─────────────────┘  └─────────────────┘  └─────────────────┘
         │                    │                    │
         └────────────────────┼────────────────────┘
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                    Criteria Evaluation                          │
│                                                                 │
│  --criteria test_pass:go test ./...                            │
│  --criteria file_exists:pkg/theme/dark.go                      │
│  --criteria command:grep -q "dark-mode" settings.go            │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                    Comparison Report                            │
│                                                                 │
│  buckley experiment show "add-dark-mode" --format terminal      │
│  buckley experiment diff "add-dark-mode" --output              │
└─────────────────────────────────────────────────────────────────┘
```

**Commands:**
- `experiment run` - Execute task with multiple models in parallel
- `experiment list` - List recent experiments
- `experiment show <id>` - View results (supports `--format terminal|markdown|compact`)
- `experiment diff <id>` - Compare variant outputs side-by-side
- `experiment replay <session-id>` - Re-run a session with a different model

**Success Criteria:**
```bash
# Run tests and check for pass
--criteria test_pass:"go test ./..."

# Check if a file was created
--criteria file_exists:pkg/feature/new_file.go

# Check if output contains specific text
--criteria contains:"dark mode enabled"

# Run any command (exit 0 = pass)
--criteria command:"grep -q 'toggle' settings.go"
```

---

## Plugin System

Add tools without recompiling:

```yaml
# ~/.buckley/plugins/my-tool/tool.yaml
name: my_tool
description: Does something useful
parameters:
  type: object
  properties:
    input:
      type: string
executable: ./my_tool.sh
```

Your executable reads JSON from stdin, writes JSON to stdout. Any language works.

---

## Notifications

Get pinged when Buckley needs you:

```yaml
# config.yaml
notify:
  telegram:
    bot_token: ${TELEGRAM_BOT_TOKEN}
    chat_id: ${TELEGRAM_CHAT_ID}
```

Events: `stuck`, `question`, `progress`, `complete`, `error`.

Also supports Slack and NATS.

---

## Safety Defaults

- IPC server **disabled by default**. When enabled, binds to `127.0.0.1`.
- Token auth is opt-in (`BUCKLEY_IPC_TOKEN`).
- Telemetry is local-only (`.buckley/logs/`). No remote metrics.
- Plugins load only from local paths. Nothing fetched from network.
- File changes require approval with diff preview.

---

## Requirements

- Go 1.25.1+
- An API key (OpenRouter recommended, or Anthropic/OpenAI directly)
- Git (for worktree features)
- Optional: Docker (for containerized isolation)
- Optional: Ollama (for local LLMs)

---

## Project Status

| Component | Status | Coverage |
|-----------|--------|----------|
| Core orchestrator | Production | 80%+ |
| TUI | Production | Growing |
| Web UI | Production | 8.6K lines |
| Parallel execution | Complete | 96%+ |
| Ollama provider | Production | — |
| Experiment CLI | Production | — |

See the [docs/](docs/) directory for detailed documentation.

---

## Development

```bash
# Run tests
./scripts/test.sh

# With race detector
./scripts/test.sh -race

# Specific package
go test ./pkg/orchestrator

# Build web UI (requires bun)
./scripts/build-ui.sh
```

We follow XP: TDD, pair programming, small releases, continuous refactoring.

---

## Getting Help

- **Bugs/features**: [GitHub Issues](https://github.com/odvcencio/buckley/issues)
- **Security**: See `SECURITY.md` (don't file public issues)
- **Docs**: `docs/` directory or run `buckley --help`

---

## Contributing

We value:
- **Clean boundaries**: Domain logic doesn't import infrastructure
- **Explicit dependencies**: No hidden coupling
- **Testability**: If it's hard to test, the design is wrong
- **Small commits**: Focused changes that tell a story

Before submitting:
1. Tests pass (`./scripts/test.sh`)
2. Go conventions followed (`gofmt`, `golint`)
3. Docs updated if behavior changes

---

## Inspirations

Buckley stands on the shoulders of giants. Special thanks to:

- **[Kimi K2](https://github.com/MoonshotAI/Kimi-K2)** by MoonshotAI — The model that inspired Buckley. Exceptional context longevity, precise tool use, and open-source ethos.
- **[Claude Code](https://github.com/anthropics/claude-code)** — Anthropic's official CLI, demonstrating what's possible with careful UX
- **[Cline](https://github.com/cline/cline)** — VS Code extension pushing autonomous coding forward
- **[Roo Code](https://github.com/RooVetGit/Roo-Code)** — Community-driven fork exploring new directions
- **[Zed](https://zed.dev/)** — Modern editor with first-class AI integration
- **[OpenCode](https://github.com/OpenCodeHQ/opencode)** — Open-source AI coding assistant

---

## License

MIT License. See [LICENSE](LICENSE).

---

## Why "Buckley"?

Named after a very good dog who would have loved chasing bugs.

---

*216K lines of Go. 67 packages. One goal: make AI development tools that don't waste your time.*
