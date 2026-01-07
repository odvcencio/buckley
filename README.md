# Buckley

AI dev assistant that remembers what you're doing.

Sessions survive crashes. Four trust levels. Loop detection. Multi-model support.

---

## Quick Start

```bash
go install github.com/odvcencio/buckley/cmd/buckley@latest
export OPENROUTER_API_KEY="your-key"
buckley
```

No Docker. No database setup. Just Go and an API key.

---

## Three Ways to Use It

| Mode | Command | Use Case |
|------|---------|----------|
| **TUI** | `buckley` | Interactive terminal with streaming and approvals |
| **Web** | `buckley serve --browser` | Browser-based Mission Control |
| **API** | `buckley api` | Headless for CI/CD integration |

---

## The Workflow

1. **Plan** — `/plan "add user auth"` breaks work into tasks
2. **Execute** — `/execute` runs tasks with self-healing retries
3. **Review** — AI reviews changes before you merge

The orchestrator coordinates builder, review, and research agents. Tasks persist to SQLite. Crash? Resume where you left off.

---

## One-Shot Commands

```bash
buckley commit    # AI-generated commit message from staged changes
buckley pr        # AI-generated PR description
buckley review    # Code review current changes
buckley hunt      # Scan codebase for improvements
buckley dream     # Architectural analysis
```

---

## Skills

Skills are workflow playbooks. Buckley ships with 8:

- **test-driven-development** — Write tests before code
- **systematic-debugging** — Reproduce, isolate, fix, verify
- **code-review** — Review checklist
- **planning** — Break work into tasks

Skills activate by phase. Create your own in `.buckley/skills/`.

---

## Tools

46+ built-in tools:

| Category | Tools |
|----------|-------|
| Files | `read_file`, `edit_file`, `create_file` |
| Search | `search_text`, `semantic_search`, `find_files` |
| Shell | `run_shell` (with timeout and output limits) |
| Git | `git_status`, `git_diff`, `git_commit`, etc. |
| Quality | `run_tests`, `lint` |

Add custom tools via YAML plugin manifests.

---

## Trust Levels

Four modes from manual to full auto:

| Level | Behavior |
|-------|----------|
| **Conservative** | Approve everything |
| **Balanced** | Approve file changes and shell |
| **Standard** | Auto-approve safe operations |
| **Autonomous** | Full auto (careful) |

Configure per-project in `.buckley/config.yaml`.

---

## Multi-Model

Different models for different jobs:

```yaml
models:
  planning: moonshotai/kimi-k2-thinking
  execution: moonshotai/kimi-k2-thinking
  review: moonshotai/kimi-k2-thinking
```

Supports OpenRouter (100+ models), Anthropic, OpenAI, Google, Ollama.

---

## Experiments

Compare models on the same task:

```bash
buckley experiment run "add-dark-mode" \
    -m moonshotai/kimi-k2-thinking \
    -m anthropic/claude-sonnet-4-5 \
    -p "Add dark mode toggle"
```

Each model runs in an isolated git worktree. Results compared automatically.

---

## Configuration

Hierarchical config:

```
~/.buckley/config.yaml    (user defaults)
./.buckley/config.yaml    (project overrides)
Environment variables     (highest priority)
```

Minimal setup:

```yaml
providers:
  openrouter:
    api_key: ${OPENROUTER_API_KEY}
```

---

## Notifications

Get pinged when Buckley needs you:

```yaml
notify:
  telegram:
    bot_token: ${TELEGRAM_BOT_TOKEN}
    chat_id: ${TELEGRAM_CHAT_ID}
```

Supports Telegram, Slack, NATS.

---

## Documentation

| Page | Description |
|------|-------------|
| [CLI Reference](https://buckley.draco.quest/CLI) | Commands and flags |
| [Configuration](https://buckley.draco.quest/CONFIGURATION) | All config options |
| [Skills](https://buckley.draco.quest/SKILLS) | Workflow guidance system |
| [Tools](https://buckley.draco.quest/TOOLS) | Built-in tools reference |
| [Orchestration](https://buckley.draco.quest/ORCHESTRATION) | Multi-agent coordination |

Full docs at [buckley.draco.quest](https://buckley.draco.quest)

---

## Requirements

- Go 1.25.1+
- API key (OpenRouter recommended)
- Git (for worktree features)
- Optional: Docker, Ollama

---

## Development

```bash
./scripts/test.sh          # Run tests
./scripts/test.sh -race    # With race detector
go test ./pkg/orchestrator # Specific package
```

---

## License

MIT. See [LICENSE](LICENSE).

---

*Named after a very good dog.*
