# Built-in Tools

Buckley has 40+ built-in tools. Here are the most important ones.

---

## File Operations

| Tool | What It Does |
|------|--------------|
| `read_file` | Read file contents (supports offset/limit for large files) |
| `create_file` | Create a new file |
| `edit_file` | Replace text in existing file (exact match required) |
| `delete_file` | Delete a file |
| `list_directory` | List files in a directory |

**Example: edit_file**
```json
{
  "path": "src/main.go",
  "old_text": "func main() {",
  "new_text": "func main() {\n\tlog.Println(\"starting\")"
}
```

---

## Search

| Tool | What It Does |
|------|--------------|
| `search_text` | Regex search across files |
| `semantic_search` | Natural language search using embeddings |
| `find_files` | Find files by glob pattern |

**Example: search_text**
```json
{
  "pattern": "func.*Handler",
  "path": "pkg/",
  "file_pattern": "*.go"
}
```

---

## Shell

| Tool | What It Does |
|------|--------------|
| `run_shell` | Execute shell command |

**Limits:**
- 120 second timeout (configurable)
- 100KB output limit
- Working directory defaults to project root

**Example:**
```json
{
  "command": "go test ./...",
  "timeout": 60
}
```

---

## Git

| Tool | What It Does |
|------|--------------|
| `git_status` | Show working tree status |
| `git_diff` | Show changes |
| `git_log` | Show commit history |
| `git_branch` | List or create branches |
| `git_checkout` | Switch branches |
| `git_commit` | Create commit |
| `git_merge` | Merge branches |
| `git_stash` | Stash changes |

---

## Code Quality

| Tool | What It Does |
|------|--------------|
| `run_tests` | Run test suite |
| `lint` | Run linter |
| `browser` | Open URL in browser (for previewing) |

---

## Task Tracking

| Tool | What It Does |
|------|--------------|
| `todo` | Create/update/complete tasks |

**Example:**
```json
{
  "action": "create",
  "task": "Add input validation",
  "priority": "high"
}
```

Tasks persist to SQLite. Visible in TUI sidebar.

---

## Navigation

| Tool | What It Does |
|------|--------------|
| `get_workdir` | Get current working directory |
| `change_workdir` | Change working directory |

---

## Embeddings

| Tool | What It Does |
|------|--------------|
| `semantic_search` | Query codebase with natural language |
| `manage_embeddings_index` | Rebuild or clear the embeddings index |

The embeddings index auto-builds on first semantic search. Uses `text-embedding-3-small` via OpenRouter.

---

## Tool Approval

By default, Buckley asks before:
- Creating files
- Editing files
- Running shell commands
- Git operations that modify state

Configure trust level in `.buckley/config.yaml`:

```yaml
approval:
  mode: balanced  # auto | balanced | conservative
  trusted_paths:
    - "tests/"    # Auto-approve changes here
```

---

## External Plugins

Add custom tools via YAML manifests:

```yaml
# .buckley/plugins/my-tool/tool.yaml
name: my_tool
description: What it does
parameters:
  type: object
  properties:
    input:
      type: string
      description: The input
  required: [input]
executable: ./my_tool.sh
timeout_ms: 30000
```

Plugin receives JSON on stdin, writes JSON to stdout.

---

## Related

- [Skills](./SKILLS.md) - Restrict tools per workflow
- [Configuration](./CONFIGURATION.md) - Tool settings
- [CLI Reference](./CLI.md) - `/tools` command
