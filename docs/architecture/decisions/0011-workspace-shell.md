# ADR 0011: Three-panel workspace shell

- Status: accepted
- Date: 2026-07-30

## Context

Buckley's terminal UI has historically treated the transcript as the destination for every kind of output. That is useful for final answers, but it becomes noisy when file reads, edits, shell streams, tool payloads, and subagent output are rendered inline. The existing sidebar already has task and telemetry summaries, but it is fixed-width, transient, and not an inspection surface.

Buckley also already parses assistant output through Markdown++ before lowering it to FluffyUI markdown. The missing piece is a workspace model that gives different information different homes.

## Decision

Buckley adopts a responsive three-panel shell:

| Region | Purpose | Default behavior |
|---|---|---|
| Left navigator | Current task, plan, active tools and agents, recent files, project/session navigation | Collapsed until requested |
| Center transcript | User turns, assistant turns, compact progress summaries | Always present |
| Right inspector | Persistent tool/subagent activity, file and edit detail, command output, deep reads | Opens on first inspectable activity |

Both side panels are independently collapsible. Their widths are adjustable in fixed terminal-column steps, and the right panel preserves the user's explicit hide/show choice after it has been touched.

The transcript contains concise progress records only. Detailed arguments, results, file payloads, edits, and subagent output flow through telemetry into the inspector. Telemetry details are bounded and recursively redact common secret-bearing fields.

The center scrollbar is interactive. It supports click-to-jump and drag-to-seek, and it carries semantic marks for user and assistant turns.

Markdown++ remains the canonical parser for user-visible model output. The terminal lowering adds dense table rendering and recognizable `☐` / `☑` task-list glyphs.

## Key bindings

| Action | Binding |
|---|---|
| Toggle navigator | `Ctrl+B` |
| Toggle activity inspector | `Ctrl+Shift+B` |
| Resize navigator | `Alt+[` / `Alt+]` |
| Resize inspector | `Alt+{` / `Alt+}` |

## Consequences

The transcript becomes substantially easier to read during tool-heavy sessions, while full operational detail remains one click away. Subagents become visible work units instead of disappearing when they finish. Responsive collapse preserves a usable chat width on smaller terminals.

Telemetry payloads become richer, so bounding and redaction are part of the contract rather than optional presentation behavior.

## Follow-up checklist

- [ ] Add project/session/agent trees to the left navigator.
- [ ] Add mouse-drag resizing for panel separators.
- [ ] Persist the redacted telemetry stream as a replayable JSONL journal.
- [ ] Add a built-in query tool so later agents can consume prior activity records.
- [ ] Let the inspector pin files, rendered Markdown++ documents, diffs, and research notes as tabs.
- [ ] Add interactive checklist toggling backed by source ranges.
- [ ] Add scrollbar hover labels and keyboard navigation between turn bookmarks.
- [ ] Share the workspace state model with the web UI and ACP clients.
