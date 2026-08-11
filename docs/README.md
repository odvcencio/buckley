# Buckley documentation

This directory holds **operator documentation** — what you need to install,
configure, run, and troubleshoot Buckley.

| Guide | Covers |
|---|---|
| [Introduction](index.md) | What Buckley is |
| [CLI Reference](CLI.md) | Commands and flags |
| [Configuration](CONFIGURATION.md) | `config.yaml`, environment, models |
| [Running Goals](goals.md) | Durable long-horizon work, budgets, reports |
| [Code Mode](code-mode.md) | The sandboxed code-execution surface |
| [Troubleshooting](troubleshooting.md) | Symptoms and fixes |
| [Mission Control](MISSION_CONTROL.md) | GoSX browser interface |
| [Editor Integration](ACP.md) | ACP clients (Zed, JetBrains, Neovim) |

## Architecture, concepts, and decisions

Internal knowledge — architecture overviews, concept references, ADRs,
and analyses — is canonical in the Hyphae space `hypha://m31labs/buckley`,
not in this repository. It is versioned, searchable, and shared across
the whole stack:

```bash
hypha recall "context compaction"
hypha show concept.architecture-overview
hypha show decision.adr-0013-code-execution-surface
```

Keeping one copy avoids the drift that comes from maintaining two.
