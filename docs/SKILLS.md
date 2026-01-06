# Skills

Skills are workflow guidance documents that help Buckley approach tasks consistently.

---

## What Skills Do

A skill is a markdown file that:
- Describes how to approach a type of work
- Can restrict which tools are available
- Can enforce TODO tracking
- Activates automatically based on workflow phase

Think of skills as reusable playbooks.

---

## Bundled Skills

Buckley ships with 8 skills:

| Skill | When It Activates | What It Does |
|-------|-------------------|--------------|
| `test-driven-development` | Execution phase | Write tests before implementation |
| `systematic-debugging` | When fixing bugs | Structured approach: reproduce, isolate, fix, verify |
| `refactoring` | Code changes | Safe refactoring patterns |
| `planning` | Planning phase | Break work into tasks |
| `code-review` | Review phase | Review checklist |
| `api-design` | API work | REST/gRPC design principles |
| `git-workflow` | Git operations | Branching, commits, PRs |
| `creative-writing` | Documentation | Writing style guide |

---

## How Skills Activate

Skills activate based on workflow phase:

```
Planning phase  → planning skill
Execution phase → test-driven-development, refactoring
Review phase    → code-review
```

You can also activate skills manually:

```
/skill test-driven-development
```

---

## Skill File Format

Skills live in `~/.buckley/skills/` or `./.buckley/skills/`:

```markdown
---
name: my-skill
description: What this skill does
phases: [execution]  # When to auto-activate
tools:
  allowed: [read_file, edit_file, run_tests]  # Optional: restrict tools
  blocked: [run_shell]  # Optional: block tools
requires_todos: true  # Optional: enforce task tracking
---

# My Skill

Guidance content here. Buckley reads this and follows it.

## Steps

1. First do this
2. Then do that
3. Verify with tests
```

---

## Creating Custom Skills

1. Create a file in `./.buckley/skills/my-skill.md`
2. Add frontmatter with name and description
3. Write the guidance content
4. Buckley will discover it on next run

Example custom skill for your codebase:

```markdown
---
name: our-api-conventions
description: Our team's API design rules
phases: [execution]
---

# Our API Conventions

## Endpoints

- Use plural nouns: `/users` not `/user`
- Version in URL: `/v1/users`
- Use kebab-case: `/user-profiles`

## Responses

- Always include `id` field
- Use ISO 8601 for dates
- Wrap lists in `{ "data": [...] }`
```

---

## Tool Restrictions

Skills can limit what tools are available:

```yaml
tools:
  allowed: [read_file, search_text, run_tests]
```

This prevents the AI from using tools that don't fit the workflow. A research skill might only allow read operations.

---

## Phase-Based Activation

| Phase | Default Skills |
|-------|----------------|
| `planning` | planning |
| `execution` | test-driven-development |
| `review` | code-review |

Override in config:

```yaml
skills:
  planning: [planning, our-planning-rules]
  execution: [test-driven-development, our-conventions]
  review: [code-review, security-review]
```

---

## Skill Locations

Buckley looks for skills in:

1. `./.buckley/skills/` (project-specific)
2. `~/.buckley/skills/` (user-wide)
3. Bundled skills (built-in)

Project skills override user skills override bundled.

---

## Related

- [Configuration](./CONFIGURATION.md) - Skill settings
- [Tools](./TOOLS.md) - Available tools to allow/block
- [Orchestration](./ORCHESTRATION.md) - Workflow phases
