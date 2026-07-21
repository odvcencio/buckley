---
layout: home

hero:
  name: Buckley
  text: AI dev assistant that remembers what you're doing
  tagline: Sessions survive crashes. Four trust levels. Loop detection. Multi-model experiments. Built by someone who uses it daily.
  image:
    src: /logo.jpg
    alt: Buckley - your AI development companion
  actions:
    - theme: brand
      text: Get Started
      link: /CLI
    - theme: alt
      text: View on GitHub
      link: https://github.com/odvcencio/buckley

features:
  - icon: 💾
    title: Sessions That Survive
    details: Crash? Power outage? Your work is still there. SQLite persistence means you pick up where you left off.
  - icon: 🎚️
    title: Tiered Autonomy
    details: Four trust levels from "ask everything" to "full auto". Smart command classification knows what's safe. Trusted paths per project.
  - icon: 📱
    title: Walk Away, Get Pinged
    details: Telegram and Slack notifications when Buckley needs you. Respond from your phone. Come back when it matters.
  - icon: 🔄
    title: Loop Detection
    details: AI gets stuck retrying the same thing? Buckley detects it and stops. Tokens aren't free.
  - icon: 🎯
    title: Multi-Model Experiments
    details: Run the same task across different models. See who's actually good, not who has the best marketing.
  - icon: 🔓
    title: No Vendor Lock-in
    details: OpenRouter, OpenAI, Anthropic, Google, or local Ollama. Your choice, always.
---

## Quick Start

```bash
go install m31labs.dev/buckley/cmd/buckley@latest
export OPENROUTER_API_KEY="your-key"
buckley
```

## Core Concepts

### Three Ways to Use It

| Mode | Command | Use Case |
|------|---------|----------|
| **TUI** | `buckley` | Interactive terminal with streaming and approvals |
| **Web** | `buckley serve --browser` | Browser-based Mission Control |
| **API** | `buckley api` | Headless for CI/CD integration |

### The Workflow

1. **Plan** - `/plan "add user auth"` breaks work into tasks
2. **Execute** - `/execute` runs tasks with self-healing retries
3. **Review** - AI reviews changes before you merge

### One-Shot Commands

```bash
buckley commit    # AI-generated commit message from staged changes
buckley pr        # AI-generated PR description
buckley review    # Code review current changes
buckley review-pr 123 # Review a GitHub PR, CI, and unresolved feedback
buckley hunt      # Scan codebase for improvements
buckley dream     # Architectural analysis
```

Local reviews exclude untracked files by default. Repeat `buckley review --scope worktree --include-untracked path/to/new.go` to explicitly allowlist filtered untracked text for model input; inspect each path for secrets first. PR reviews fail closed unless the local checkout is at the captured PR head. Primary, retry, and approval-critic passes reuse the same immutable verification snapshot. Codex receives a self-contained captured-commit workspace, and approval requires trusted JSONL command events proving classifiable build and test runs over the changed source paths. API-backed tools are confined to an independently materialized copy and can execute only deterministic build/test/check plans with source read-only, private temporary output, a scrubbed environment, and network disabled; API approval requires successful same-toolchain build and test calls covering every changed source package. Project-wide review is explicitly advisory and cannot issue an approval verdict.

## Configuration

Buckley looks for config in order:
1. `~/.buckley/config.yaml` (user defaults)
2. `./.buckley/config.yaml` (project overrides)
3. Environment variables (highest priority)

Minimal config - just set your API key:

```yaml
# ~/.buckley/config.yaml
providers:
  openrouter:
    api_key: ${OPENROUTER_API_KEY}
```

## Documentation

| Page | What's There |
|------|--------------|
| [CLI Reference](./CLI.md) | Commands, flags, shortcuts |
| [Configuration](./CONFIGURATION.md) | All config options |
| [Skills](./SKILLS.md) | Workflow guidance system |
| [Tools](./TOOLS.md) | Built-in tools reference |
| [Orchestration](./ORCHESTRATION.md) | Multi-agent coordination |
| [Editor Integration](./ACP.md) | Zed ACP (optional LSP bridge) |
| [Architecture Decisions](./architecture/decisions/) | Design rationale |

## Open Source

MIT licensed. [GitHub](https://github.com/odvcencio/buckley).
