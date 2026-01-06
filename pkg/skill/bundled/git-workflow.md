---
name: git-workflow
description: Git best practices for commits, branches, and PRs - maintain clean history and enable effective collaboration. Use when working with version control.
requires_todo: false
priority: 5
---

# Git Workflow

## Purpose

Good git practices make collaboration easier, code review more effective, and debugging faster through clear history.

## Commit Guidelines

### Atomic Commits

Each commit should represent one logical change:

- **One concept per commit** - Fix one bug, add one feature, refactor one component
- **Independently revertable** - Any commit can be reverted without breaking others
- **All tests pass** - Every commit should leave the codebase in a working state

**Example good commit sequence:**
```
1. Add user authentication interface
2. Implement JWT token generation
3. Add authentication middleware
4. Wire up auth to user endpoints
```

**Example bad commit:**
```
1. Add auth, fix bugs, update README, refactor utils
```

### Commit Messages

Follow conventional commits format:

```
<type>(<scope>): <subject>

<body>

<footer>
```

**Types:**
- `feat` - New feature
- `fix` - Bug fix
- `docs` - Documentation only
- `refactor` - Code change that neither fixes a bug nor adds a feature
- `test` - Adding or updating tests
- `chore` - Maintenance tasks (dependencies, tooling)

**Example:**
```
feat(auth): add JWT token generation

Implement JWT token creation and validation using the
standard crypto libraries. Tokens expire after 24 hours
and include user ID and role claims.

Closes #123
```

**Subject line rules:**
- Use imperative mood ("add" not "added" or "adds")
- Don't capitalize first letter
- No period at the end
- Max 50 characters
- Be specific ("fix auth bug" → "fix token expiration check")

**Body rules:**
- Wrap at 72 characters
- Explain *what* and *why*, not *how* (code shows how)
- Separate from subject with blank line
- Include context for future maintainers

### What to Commit

**Do commit:**
- Source code
- Configuration (without secrets)
- Documentation
- Tests
- Build scripts
- Dependencies manifest (go.mod, package.json)

**Don't commit:**
- Build artifacts (binaries, bundles)
- Dependencies themselves (node_modules, vendor)
- IDE/editor files (.vscode, .idea) unless team-wide
- OS files (.DS_Store, Thumbs.db)
- Secrets (API keys, passwords, certificates)
- Large binary files (unless using Git LFS)

Use `.gitignore` to exclude these automatically.

## Branch Strategy

### Branch Naming

Use descriptive, kebab-case names:

```
feature/user-authentication
fix/token-expiration-bug
refactor/database-layer
docs/api-endpoints
```

**Pattern:** `<type>/<short-description>`

### Branch Lifecycle

1. **Create from main** - Always branch from up-to-date main
2. **Work in isolation** - Keep branch focused on one feature/fix
3. **Sync regularly** - Rebase or merge main to stay current
4. **Review before merge** - Use pull requests for code review
5. **Delete after merge** - Clean up merged branches

### Working with Branches

**Create and switch:**
```bash
git checkout -b feature/new-thing
```

**Keep updated with main:**
```bash
# Option 1: Rebase (cleaner history)
git fetch origin
git rebase origin/main

# Option 2: Merge (preserves branch history)
git merge origin/main
```

**Push to remote:**
```bash
git push -u origin feature/new-thing
```

## Pull Request Guidelines

### PR Description

A good PR description includes:

1. **Summary** - What does this change do?
2. **Motivation** - Why is this change needed?
3. **Approach** - How does it work at a high level?
4. **Testing** - How was this tested?
5. **Screenshots** - For UI changes
6. **Checklist** - Tests pass, docs updated, etc.

### PR Size

Keep PRs reviewable:

- **Target: 200-400 lines changed**
- Maximum: 800 lines (beyond this, split into multiple PRs)
- If unavoidable: Add extra context and guide reviewers

### Before Creating PR

Checklist:

- [ ] All tests pass locally
- [ ] Code follows project style
- [ ] New tests added for new functionality
- [ ] Documentation updated if needed
- [ ] No debug code or commented-out code
- [ ] Commits are logical and well-messaged
- [ ] Branch is up to date with main

## Common Workflows

### Fix a Bug

```bash
git checkout main
git pull
git checkout -b fix/bug-description
# Make changes, write test
git add .
git commit -m "fix: description of bug fix"
git push -u origin fix/bug-description
# Create PR
```

### Add a Feature

```bash
git checkout main
git pull
git checkout -b feature/feature-name
# Implement in small commits
git add file1.go
git commit -m "feat: add interface"
git add file2.go file3.go
git commit -m "feat: implement core logic"
git add file4_test.go
git commit -m "test: add coverage for new feature"
git push -u origin feature/feature-name
# Create PR
```

### Update Branch with Main

```bash
git checkout feature/my-feature
git fetch origin
git rebase origin/main
# Resolve conflicts if any
git push --force-with-lease
```

## Key Principles

- **Commit often** - Small commits are easier to review and revert
- **Write for future developers** - Including future you
- **Keep history clean** - Rebase or squash before merging if needed
- **Review your own changes** - Look at the diff before pushing
- **Test before committing** - Broken commits make bisecting harder

## Anti-Patterns

- **Committing untested code** - Every commit should pass tests
- **"WIP" or "fix" commit messages** - Use meaningful messages
- **Massive commits** - 1000+ lines changed in one commit
- **Mixing concerns** - Refactor + feature + bug fix in one commit
- **Force pushing shared branches** - Use `--force-with-lease` instead
- **Committing secrets** - Use environment variables and `.gitignore`
- **Not syncing with main** - Long-lived branches accumulate conflicts
