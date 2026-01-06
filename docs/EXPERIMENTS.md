# Buckley Experiments

Run the same task across multiple AI models and compare results.

## Quick Start

```bash
# Compare GPT-4 and Claude on a refactoring task
buckley experiment run refactor-auth \
  -m gpt-4o \
  -m claude-3-5-sonnet-20241022 \
  -p "Refactor the auth module to use JWT tokens"

# Add success criteria
buckley experiment run add-tests \
  -m gpt-4o \
  -m claude-3-5-sonnet-20241022 \
  -p "Add unit tests for the user service" \
  --criteria "test_pass:go test ./..." \
  --criteria "file_exists:pkg/user/user_test.go"
```

## Commands

### `experiment run`

Run an experiment with multiple models.

```
buckley experiment run <name> [flags]
```

**Flags:**
- `-m, --model <model>` - Model to compare (repeatable)
- `-p, --prompt <text>` - Task prompt
- `--criteria <type:target>` - Success criteria (repeatable)
- `--timeout <duration>` - Timeout per variant (default: 30m)
- `--max-concurrent <n>` - Max parallel variants

**Example:**
```bash
buckley experiment run optimize-queries \
  -m gpt-4o \
  -m claude-3-5-sonnet-20241022 \
  -m codellama:34b \
  -p "Optimize the database queries in pkg/storage/queries.go" \
  --criteria "test_pass:go test ./pkg/storage/..." \
  --timeout 15m
```

### `experiment list`

List experiments with optional filtering.

```
buckley experiment list [flags]
```

**Flags:**
- `--status <status>` - Filter by status (pending|running|completed|failed|cancelled)
- `--limit <n>` - Maximum experiments to list (default: 20)

**Example:**
```bash
buckley experiment list --status completed --limit 10
```

### `experiment show`

Show detailed results for an experiment.

```
buckley experiment show <id|name>
```

**Example:**
```bash
buckley experiment show optimize-queries
```

### `experiment replay`

Replay a previous session with a different model.

```
buckley experiment replay <session-id> [flags]
```

**Flags:**
- `-m, --model <model>` - Model to use for replay (required)
- `--provider <id>` - Provider override
- `--system-prompt <text>` - System prompt override
- `--temperature <float>` - Temperature override
- `--deterministic-tools` - Replay tool calls deterministically

## Success Criteria

Define how success is measured for each variant.

| Type | Description | Example |
|------|-------------|---------|
| `test_pass` | Command must exit with code 0 | `test_pass:go test ./...` |
| `file_exists` | File must exist after completion | `file_exists:pkg/new_feature.go` |
| `contains` | Output must contain text | `contains:All tests passed` |
| `command` | Custom command must succeed | `command:./scripts/validate.sh` |
| `manual` | Requires manual verification | `manual:Review code quality` |

**Multiple Criteria:**
```bash
buckley experiment run full-check \
  -m gpt-4o \
  -p "Implement user authentication" \
  --criteria "test_pass:go test ./..." \
  --criteria "file_exists:pkg/auth/jwt.go" \
  --criteria "command:go build ./..." \
  --criteria "contains:authentication successful"
```

Criteria scores are weighted. By default all criteria have weight 1.0.

## Configuration

Add to `.buckley/config.yaml`:

```yaml
experiment:
  enabled: true
  max_concurrent: 4
  default_timeout: 30m
  worktree_root: .buckley/experiments
  cleanup_on_done: true
  max_cost_per_run: 1.00
  max_tokens_per_run: 100000
```

**Settings:**
- `enabled` - Enable experiment commands
- `max_concurrent` - Maximum variants running in parallel
- `default_timeout` - Default timeout per variant
- `worktree_root` - Directory for experiment worktrees
- `cleanup_on_done` - Remove worktrees after completion
- `max_cost_per_run` - Maximum cost per variant run
- `max_tokens_per_run` - Maximum tokens per variant run

## Notifications

Get notified when experiments complete via Telegram or Slack.

### Telegram

```yaml
notify:
  enabled: true
  telegram:
    enabled: true
    bot_token: "your-bot-token"
    chat_id: "your-chat-id"
```

### Slack

```yaml
notify:
  enabled: true
  slack:
    enabled: true
    webhook_url: "https://hooks.slack.com/services/..."
    channel: "#experiments"
```

Notifications include:
- Experiment started (with variant count)
- Variant completed (cost, duration, success)
- Variant failed (error message)
- Experiment completed (summary, rankings)

## Understanding Reports

After an experiment completes, `buckley experiment show` displays:

```
Experiment: optimize-queries (completed)

Model               | Score | Cost    | Duration | Tokens
claude-3-5-sonnet   | 100%  | $0.0234 | 3m 42s   | 4,231
gpt-4o              | 100%  | $0.0156 | 2m 18s   | 3,102
codellama:34b       |  60%  | $0.00   | 5m 12s   | 8,445

Cost Comparison:
claude-3-5  ████████████████████████ $0.0234
gpt-4o      ████████████████ $0.0156
codellama   ▏ $0.00

Winner: gpt-4o (100% score, lowest cost)
```

**Ranking Factors (in order):**
1. **Score** - Percentage of criteria passed (weighted)
2. **Cost** - Lower cost wins when scores tie
3. **Duration** - Faster wins when cost ties

## How It Works

1. **Worktree Creation** - Each variant runs in an isolated git worktree
2. **Parallel Execution** - Variants run concurrently (up to `max_concurrent`)
3. **Criteria Evaluation** - After completion, success criteria are checked
4. **Comparison** - Variants are ranked by score, cost, then duration
5. **Cleanup** - Worktrees are removed if `cleanup_on_done` is true

## Tips

**Compare local vs cloud models:**
```bash
buckley experiment run local-vs-cloud \
  -m gpt-4o \
  -m ollama/llama3:70b \
  -p "Implement the feature described in SPEC.md"
```

**Test prompt variations:**
Create separate experiments with different prompts to find the most effective wording.

**Use cost limits:**
Set `max_cost_per_run` to prevent runaway experiments with verbose models.

**Check progress:**
```bash
buckley experiment list --status running
```
