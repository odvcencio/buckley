# Codex completion guard

`buckley completion-check --hook` implements a bounded Codex Stop hook.
It calls `typesafe/jev-1.13` through OpenRouter's **Decisions API**.
The embedded Arbiter strategy decides whether to request one continuation.
It does not run a second coding agent, execute tools, or inspect repository files.

## Behavior

- `UserPromptSubmit` records the current Astra request in private local state.
- `Stop` compares that request with the final assistant message.
- Jev answers four independent probability questions in one request.
- Arbiter requires action, unfinished work, and authorization probabilities of
  at least 0.85, with a needs-user probability at or below 0.20.
- A positive decision returns Codex's `decision: "block"` continuation response.
- One attempt is allowed per human prompt. Hook continuations and duplicates
  cannot replenish that budget. Provider failures consume the attempt too.
- `Interrupt` cancels a pending recommendation. `SessionEnd` removes the saved request.
- Other models, plan mode, missing/stale context, and oversized inputs skip inference.

Only `gpt-6-astra` and its dated variants are selected. Changing Codex's default
model is unnecessary. The guard requests zero data retention and denies data
collection. It never relaxes those preferences or falls back to another model.

## Install

Build Buckley and use its absolute executable path in
`examples/codex-completion-hooks.json`. Merge the events into your user-level
`$CODEX_HOME/hooks.json`; preserve existing hooks and `notify` settings.

For a Windows Codex host using a WSL Buckley installation, use:

```text
wsl.exe -d Ubuntu --exec /absolute/path/to/buckley completion-check --hook
```

Review and trust the four hook definitions through Codex's `/hooks` interface.
Untrusted definitions are skipped. Do not forge trust hashes or bypass hook trust.
Start a new user turn after activation; existing turns have no captured request.

Credentials come from Buckley's user configuration and environment, including
`~/.buckley/config.env`. Project configuration cannot redirect this request.
The endpoint and model are fixed. No transcript, source files, or tool output
are sent: only the user request and final assistant response, plus judge questions.
Requests over 8 KiB or replies over 12 KiB are skipped rather than truncated.

## Inspect or disable

State defaults to `~/.buckley/completion-guard` with private file permissions.
`decisions.jsonl` records probabilities, policy outcomes, model identity, token
usage, and reported cost. It omits the saved request and response text.

Create an empty `disabled` file in that directory, or set
`BUCKLEY_COMPLETION_GUARD=off`, to disable the guard. Remove the file to resume.
`--state-dir` selects a separate directory for tests or isolated installations.

If a process is forcibly killed, its session lock can remain. Stop the affected
hook process before removing that session's `.lock` directory. A stale lock
causes the guard to skip; it does not restart work.

For a direct, explicitly requested paid decision without lifecycle behavior:

```sh
printf '%s' '{"request":"Implement the fix and run its test.","response":"I can implement it next."}' | buckley completion-check
```

## Limits

This is an early-stop detector, not a completion certificate. It can notice
admitted unfinished work. It cannot independently disprove a false completion
claim, recover omitted conversation context, or certify tests it did not observe.
A stop outcome means no sufficiently clear continuation was established.
The thresholds are initial operating choices, not calibrated accuracy claims.
Review the decision log against real outcomes before expanding the loop budget.

The local request is retained until SessionEnd or replacement by a new request.
Requests older than 12 hours are never evaluated. The log is local and can be
removed when no hook is running.

Sources: [Codex hooks](https://developers.openai.com/es-419/docs/hooks),
[OpenRouter Decisions integration](https://github.com/OpenRouterTeam/ai-sdk-provider#evaluation-jev-with-ai-sdk-through-openrouter),
[TypeSafe primitives](https://docs.typesafe.ai/primitives).
