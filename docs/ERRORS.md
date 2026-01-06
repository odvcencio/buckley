# Buckley Error Reference

This guide documents how Buckley surfaces errors (CLI exit codes, plus IPC/API JSON errors) and the most common remediation steps.

## CLI Exit Codes

| Code | Meaning |
|------|---------|
| `0` | Success |
| `1` | General error (runtime failure, invalid input, tool execution failure) |
| `2` | Configuration error (invalid YAML, missing provider key, validation failure) |

## IPC / API Error Responses

When running `buckley serve` (or using the desktop/mobile UIs), errors are returned as JSON:

```json
{
  "error": "human-friendly message",
  "status": 400,
  "code": "CONFIG_INVALID",
  "message": "human-friendly message",
  "details": "[CONFIG_INVALID] invalid config: ...",
  "remediation": ["actionable tip 1", "actionable tip 2"],
  "retryable": false,
  "timestamp": "2025-01-01T00:00:00Z"
}
```

- `status` is the HTTP status code.
- `code` is optional and is present when the backend wraps the error using `pkg/errors`.
- `remediation` is best-effort guidance; `details` is often the most precise signal for debugging.

## Structured Error Codes

These codes are defined in `pkg/errors/types.go`. They can appear:

- In IPC error responses as `code`
- In some CLI errors as a `[CODE] ...` prefix

### Configuration

#### `CONFIG_LOAD` — config file could not be read

Common causes:
- Config file path does not exist
- Permissions prevent reading the file
- The filesystem is unavailable or transiently failing

Fix:
- Verify your config path (`--config`) and permissions.
- Run `buckley config check` to validate loading/merging.

#### `CONFIG_PARSE` — config file could not be parsed (YAML)

Common causes:
- YAML syntax error or invalid types (e.g., `enabled: yes` instead of `true`)

Fix:
- Run `buckley config check`.
- Validate YAML syntax with a linter or `yq`.

#### `CONFIG_INVALID` — config is semantically invalid

Common scenarios:
- No provider keys configured (e.g., missing `OPENROUTER_API_KEY`)
- Invalid enum values (trust level, approval mode)
- Unsafe network bind (e.g., IPC bound to non-loopback without auth)
- Container/worktree settings invalid for your environment

Fix:
- Run `buckley config check` for a precise validation error.
- Configure at least one provider key:
  ```bash
  export OPENROUTER_API_KEY="<YOUR_OPENROUTER_API_KEY>"
  ```
- If binding IPC to a non-loopback interface, enable auth (`BUCKLEY_IPC_TOKEN` or basic auth) or bind to `127.0.0.1`.

### Model

- `MODEL_NOT_FOUND`: a configured model ID is not present in the provider catalog
- `MODEL_INVALID`: the model entry exists but is not usable for the requested operation
- `MODEL_API_ERROR`: upstream API returned an error (often 4xx/5xx)
- `MODEL_TIMEOUT`: request timed out
- `MODEL_RATE_LIMIT`: upstream rate limit (429)

Fix:
- Confirm your provider key is valid and has quota.
- Use `/models` in interactive mode to browse model IDs.
- Configure fallbacks if you’re using non-default models.

### Storage

- `STORAGE_READ`: failed to read from SQLite or the local filesystem
- `STORAGE_WRITE`: failed to write to SQLite or the local filesystem
- `STORAGE_CORRUPT`: database appears corrupted or inconsistent

Fix:
- Check `~/.buckley/` disk space and permissions.
- If SQLite is locked, restart Buckley and retry.
- If the DB is corrupted, back it up then recreate:
  ```bash
  mv ~/.buckley/buckley.db ~/.buckley/buckley.db.bak
  buckley migrate
  ```

### Tool

- `TOOL_NOT_FOUND`: a tool was requested but not registered
- `TOOL_EXECUTION`: a tool ran but returned an error (non-zero exit, parse failure)
- `TOOL_TIMEOUT`: a tool exceeded its timeout

Fix:
- Use `/tools` to list available tools and confirm plugin discovery paths.
- Inspect stderr/stdout for the failing tool (Terminal pane / activity log).

### Orchestrator

- `PLAN_INVALID`: plan structure or schema is invalid
- `TASK_FAILED`: task execution failed (often due to tool/model failure)
- `SELF_HEAL_FAILED`: automated recovery/repair attempts failed

Fix:
- Use `/workflow status` to find the failing task and context.
- Fix the underlying issue (tool error, config, dependency) and re-run `/execute`.

### Budget

- `BUDGET_EXCEEDED`: run exceeded configured budget limits
- `COST_TRACKING`: failed to record cost/budget state

Fix:
- Increase budget limits in config or reduce requested work.

### Generic

- `INVALID_INPUT`: invalid user input
- `NOT_IMPLEMENTED`: feature exists in the design but is not yet implemented
- `INTERNAL`: unexpected error; please report with reproduction steps

## Authentication / Authorization (IPC)

Authentication failures are usually surfaced via HTTP status codes:

- `401 Unauthorized`: missing/invalid token (set `BUCKLEY_IPC_TOKEN` or use the Config panel to create a token)
- `403 Forbidden`: token lacks required scope (`viewer|member|operator`)

## Troubleshooting Checklist

- Confirm build/version: `buckley --version`
- Validate config: `buckley config check`
- Inspect merged config: `buckley config show`
- Inspect logs: `ls -la ~/.buckley/logs/` and `tail -f ~/.buckley/logs/**/*.jsonl`

## Getting Help

- Search issues: https://github.com/odvcencio/buckley/issues
- Ask in discussions: https://github.com/odvcencio/buckley/discussions
- Security issues: follow `SECURITY.md` (do not open a public issue)

## See Also

- `docs/CLI.md`
- `docs/CONFIGURATION.md`
