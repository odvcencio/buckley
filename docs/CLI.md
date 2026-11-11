# Buckley CLI Reference

Complete command-line interface documentation for Buckley.

## Synopsis

```bash
buckley [FLAGS] [COMMAND] [ARGS...]
```

## Global Flags

| Flag | Short | Description |
|------|-------|-------------|
| `--help` | `-h` | Show help message and exit |
| `--version` | `-v` | Show version, commit, and build information |
| `--config <path>` | `-c` | Use a custom configuration file |
| `--quiet` | `-q` | Suppress non-essential output (banners, tips) |
| `--no-color` | | Disable colored output (also respects `NO_COLOR` env) |
| `--tui` | | Force rich TUI interface (default when interactive) |
| `--plain` | | Force plain scrollback mode (default when piped) |
| `--encoding <format>` | | Set serialization format: `json` or `toon` |
| `--json` | | Shortcut for `--encoding json` |
| `--code-mode` | | Offer audited, isolated `exec_program` for batched repository analysis |
| `-p <prompt>` | | Run a single prompt and exit (one-shot mode) |

## Exit Codes

| Code | Meaning |
|------|---------|
| `0` | Success |
| `1` | General error (runtime failure, invalid input) |
| `2` | Configuration error (missing API key, invalid config) |

## Commands

### Interactive Session

```bash
buckley                    # Start interactive TUI
buckley --plain            # Start in plain scrollback mode
buckley -p "your prompt"   # One-shot mode: run prompt and exit
```

**One-shot mode** (`-p`) executes a single prompt, prints the response, and exits. Useful for scripting:

```bash
# Generate a commit message
buckley -p "Generate a commit message for the staged changes" --quiet

# Pipe input
echo "Explain this error" | buckley --plain
```

### Focused one-shot tasks

Choose the task contract explicitly and provide a small source list. For a spec or
plan returned on stdout, use `--task-intent read_only` and restrict tools to file
inspection. Give the required sections, target length, acceptance criteria, and
exact files in the prompt. Approved external sources need `--read-root` (repeatable)
or `tools.read_roots`; requesting a path in prose does not grant access.

```bash
buckley --task-intent read_only \
  --tools read_file,search_text,find_files \
  --read-root ~/specs/architecture.md \
  -p "Read AGENTS.md and ~/specs/architecture.md. Return a 700-word implementation plan with acceptance criteria. Distinguish existing behavior from proposals. Do not edit files." \
  > plan.md
```

One-shot runs have no tool-call, elapsed-time, output-token or cost cap unless you
ask for one. Read-only intent keeps its no-edit contract and instruction but adds no
limit, so a model that reads widely and thinks for a long time can finish. Set caps
for one run with `--max-tool-calls N`, `--max-elapsed DURATION` (for example `90m`),
`--max-output-tokens N` and `--budget USD`, or set them for every run with
`oneshot.max_tool_calls`, `oneshot.max_elapsed`, `oneshot.max_output_tokens` and
`oneshot.budget_usd`. Zero means no cap, and a flag overrides the config key. A run
that reaches a cap says which one and how to raise it. Repeated identical tool calls
and action cycles are still stopped: that detector guards against a loop that makes no
progress and is not a budget. `buckley agent run` also accepts `--max-tool-calls`,
`--max-elapsed-seconds`, `--max-output-tokens`, `--max-model-requests` and
`--budget` with an agent spec. Use `--dry-run --json` to inspect the invocation.
Output tokens include any provider reasoning allowance.

One-shot runs have no approver, so by default they are not offered `run_shell` or other
destructive tools. A trusted project can opt in to shell for a mutation run by naming
`run_shell` in `--tools`, passing `--allow-shell`, or setting `oneshot.allow_shell: true`.
Shell is granted only with `--task-intent mutation` in a project marked trusted
(`buckley trust allow`); the sandbox, the approval rules and `approval.denied_tools`
still apply to every command. A request that cannot be granted does not stop the run: the
trace header prints `dropped from --tools: run_shell (...)` with the reason and, for an
untrusted project, how to enable it.

For `buckley agent run --allow-shell`, include `run_shell` in the agent's tool
allowlist. The same trust, intent, sandbox, and approval checks apply.

For a code change, use `--task-intent mutation`, name the files or behavior to
change and the relevant checks, and request a final account of changes and test
results. Keep repository verification requirements active. For committing already
prepared code, use the dedicated staged-diff helper instead of asking a general
agent to rediscover the repository:

```bash
git diff --cached --check
git diff --cached
buckley commit --yes --min --push=false
```

Stage only intended changes first. The commit helper binds its message to the
staged diff and checks that the diff has not changed; it does not substitute for
the project's tests. Push separately when authorized. Scripts should check the
exit code as well as the output: an incomplete answer is a failed run, even if a
partial draft was printed. Keep stderr as the diagnostic trace and stdout as the
deliverable. Successful exit does not establish the factual quality of a spec.

### TUI chat shortcuts

| Shortcut | Action |
| --- | --- |
| `Enter` | Send the current prompt |
| `Shift+Enter` | Insert a newline without sending |
| `Alt+C` | Copy the latest code block |

Fenced code blocks use Tree-sitter syntax highlighting when Buckley has a
grammar for the declared language. Other code blocks remain readable with the
standard code style.

### plan

Generate a feature implementation plan.

```bash
buckley plan <feature-name> <description>
```

**Example:**
```bash
buckley plan user-auth "Add JWT-based authentication with refresh tokens"
```

**Output:** Creates a plan with tasks and implementation strategy, stored in the database.

### execute

Execute a previously created plan.

```bash
buckley execute <plan-id>
```

**Example:**
```bash
buckley execute 2024-01-15-user-auth
```

### execute-task

Execute a single task from a plan. Designed for CI/batch environments.

```bash
buckley execute-task --plan <plan-id> --task <task-id> [OPTIONS]
```

**Options:**
| Flag | Default | Description |
|------|---------|-------------|
| `--plan` | (required) | Plan identifier |
| `--task` | (required) | Task identifier |
| `--remote-branch` | `$BUCKLEY_REMOTE_BRANCH` | Branch to push after completion |
| `--remote-name` | `origin` | Git remote name |
| `--push` | `true` | Push to remote after completion |

**Example:**
```bash
buckley execute-task --plan user-auth --task implement-jwt --remote-branch feature/auth
```

### experiment

Run and inspect retained model comparison experiments.

```bash
buckley experiment run <name> -m <model> -p <prompt>
buckley experiment show <id|name> --format json > experiment.snapshot.json
buckley experiment compare --snapshot experiment.snapshot.json
```

`experiment show --format json` exports a versioned snapshot for one stored
experiment. The snapshot contains the experiment row, full stored run rows,
input manifests, observed model execution identity rows when available, public
run output, scalar metrics/status/errors, optional rich token-usage evidence,
cost-known/unknown markers, and retained criterion evaluation rows.
It does not embed worktree files or transcripts; `Run.Files` is only the stored
list of touched paths. Version 1 preserves the existing stored Go row shape for
nested experiment/run/evaluation fields, including capitalized field names such
as `ExperimentID` and `InputManifest`.

`experiment compare --snapshot <file>` reads that JSON file, validates the
bounded 16 MiB payload and checksum, and recomputes the comparison report
offline from retained rows only. It does not initialize providers, open the
Buckley database, dispatch tools, or re-run shell criteria. Its JSON output is
an envelope containing the source snapshot version, source snapshot digest, a
fixed local provenance disclaimer, and the recomputed comparison report.
Comparison cost ranking only uses runs with comparable authoritative cost
evidence. Legacy positive scalar costs remain usable, but legacy zero-cost rows
or retained usage marked missing, estimated, inconsistent, or otherwise
unpriceable are shown as unknown/known-subtotal evidence rather than as free
winners.

Privacy note: the snapshot intentionally preserves full public run output and
criteria details so future Buckley releases can re-evaluate stored comparison
logic. Treat it as sensitive if prompts, user code, command output, or error
text are sensitive. The snapshot checksum proves JSON self-consistency only; it
is not an execution attestation, the export is not an atomic database snapshot,
and the exporter version is not the historical harness identity that ran the
experiment.

### ralph

Run Ralph autonomous sessions for long-running tasks.

```bash
buckley ralph --prompt "Audit the repo for flaky tests" --timeout 45m
buckley ralph --prompt-file ./prompt.txt --dir /path/to/worktree
```

**Session management:**
```bash
buckley ralph list
buckley ralph control --status
buckley ralph control --set backends.buckley.enabled=false
```

### commit

Generate and create a conventional commit from staged changes.

```bash
buckley commit [OPTIONS]
```

**Options:**
| Flag | Description |
|------|-------------|
| `--dry-run` | Show generated commit message without committing |
| `--yes` | Skip confirmation and create the commit |
| `--push=false` | Create the commit without pushing the current branch |
| `--paths <path>` | Commit only matching staged paths; repeatable |
| `--exclusive` | With `--paths`, reject staged files outside the selected paths |
| `-C <dir>` / `--dir <dir>` | Run as if started in `<dir>`, like `git -C`. The commit takes everything staged in that repository, so pair `-C` with `--paths` (and `--exclusive`) when another process may be staging there. Uppercase `-C`: lowercase `-c` selects the config file |
| `--context-trailer=false` | Omit the privacy-preserving change digest and aggregate stats trailers |
| `--allow-mixed` | Do not warn when generated files are staged next to source files |
| `--breaking <auto\|keep\|never>` | `BREAKING CHANGE` trailer policy (default `auto`). See [Breaking changes](#breaking-changes) |
| `--minimal-output` | Minimize terminal output |
| `--trace` | Show context audit and model reasoning after generation |
| `--model <id>` | Override the commit-message model |
| `--backend <api\|codex\|claude>` | Select the one-shot backend |
| `--effort <level>` | Override reasoning effort. No effect for a model that cannot reason and answer a required tool call in one request (`qwen/qwen3.8-flash` on OpenRouter); stderr then says so |
| `--timeout <duration>` | Time limit for generating the message, retries included. Default: `BUCKLEY_TIMEOUT_COMMIT`, then `BUCKLEY_ONESHOT_TIMEOUT`, then `5m` |
| `--squash <base>` | Squash every commit since the merge-base with `<base>` into one commit |
| `--force` | With `--squash`, allow squashing a protected branch (`main`/`master`) |
| `--force-with-lease` | With `--squash`, push the rewritten branch (`git push --force-with-lease`) |

Name files to stage after the flags: `buckley commit --yes -- main.go`. The flag
parser stops reading flags at the first file, so a flag written after one (for
example `buckley commit main.go -C /other`) is an error, not a dropped flag. A
`--` ends the flags, and a file name that starts with `-` goes after it.

**Environment Variables:**
- `BUCKLEY_MODEL_COMMIT` - Override model for commit generation
- `BUCKLEY_COMMIT_BACKEND` - Override backend; falls back to `BUCKLEY_ONESHOT_BACKEND`, then `api`
- `BUCKLEY_EFFORT_COMMIT` - Override effort; falls back to `BUCKLEY_ONESHOT_EFFORT`, then reasoning config
- `BUCKLEY_TIMEOUT_COMMIT` - Time limit for generation (a duration such as `90s` or `5m`); falls back to `BUCKLEY_ONESHOT_TIMEOUT`, then `5m`
- `BUCKLEY_PROMPT_COMMIT` - Override prompt template

**Example:**
```bash
git add -p                    # Stage changes
buckley commit                # Generate, confirm, create, and push
buckley commit --dry-run      # Preview commit message
buckley commit --yes --push=false  # Create without confirmation or push
```

Generated commits include `Buckley-Change-Hash` and `Buckley-Change-Stats`
trailers by default. The hash binds the message to the exact staged diff; the
stats record only aggregate file and line counts. No paths, filenames, or diff
content are stored in these trailers. Buckley rechecks the staged diff before
committing and aborts if it changed after message generation. Disable the
trailers with `--context-trailer=false`; the staged-diff recheck still runs.

#### Generated files

A commit of generated files only (build output, minified or vendored files,
files that `.gitattributes` marks `linguist-generated`) gets a fixed
`update(<scope>): regenerate ...` message without a model call. When generated
files are staged next to source files, `buckley commit` warns and names them,
because the message can then describe only the source change. Test data is not
counted: files under `testdata/` or `__snapshots__/` and `*.golden` or `*.snap`
files change with the code they test. `--allow-mixed` turns the warning off for
one commit.

#### Headers and bullets over the limit

A header of more than 72 characters goes back to the model once, with the limit
and the current length, so that it can write a shorter one. If the next answer
is still too long, `buckley commit` shortens it in this order: it removes
filler words and a trailing qualifier, then cuts words from the end of the
subject so that the subject ends on a content word (never on `from`, `to`,
`with` and similar words). It keeps the scope unless the subject would be left
with fewer than 4 words, and only then drops the scope. Each change is printed
on stderr.

A bullet of more than 20 words goes back to the model once, with the bullet
number and the limit, so that it can shorten its own words. If the next answer
is still too long, `buckley commit` cuts the bullet at a clause break or at the
word limit, and prints `buckley: changed the generated message: cut bullet N
from X to Y words` on stderr. Check that a cut bullet still says what changed.

#### Breaking changes

The model marks a commit as breaking, and must then say what users or importers
have to change; the message gets a `BREAKING CHANGE:` trailer with that reason.
`buckley commit` keeps that claim for any change to production code, also under
`internal/`, because such a change can still alter what a command does for the
people who run it. With `--breaking=auto` (the default) it drops the claim only
when every changed file is a test, a fixture (`testdata/`, golden files,
snapshots) or documentation, and prints a note on stderr. `--breaking=keep`
always keeps the claim the model makes; `--breaking=never` always leaves it out.
`buckley pr` takes the same flag and applies the same rule to its Breaking
Changes section.

#### Merge, cherry-pick, and revert commits

When `MERGE_HEAD`, `CHERRY_PICK_HEAD`, or `REVERT_HEAD` is present (a
conflict stopped the operation mid-way), `buckley commit` completes it
instead of creating a normal commit:

- **Merge**: refuses if any path is still unmerged, then generates a
  message with subject `merge(<scope>): Merge <source> into <target>` and a
  body that summarizes the incoming commits and states how each conflicted
  file was resolved (kept one side, or combined both). Buckley writes the
  header; the model supplies the scope and the body, and only those are
  checked. `buckley commit-check` accepts this header as it is and applies
  the bullet limits to the body.
- **Cherry-pick**: completes with the original commit's message plus a
  `(cherry picked from commit <sha>)` trailer. No model call is made — the
  message is already known.
- **Revert**: completes with git's default `Revert "<subject>"` / `This
  reverts commit <sha>.` message. No model call is made.
- **Rebase**: `buckley commit` refuses and prints guidance to run `git
  rebase --continue` after resolving conflicts. Buckley does not drive
  interactive rebases.

A prepared `git merge --squash` message (`SQUASH_MSG`, no `MERGE_HEAD`) is
used as extra context for a normal generated commit message.

#### Squashing commits

```bash
buckley commit --squash main            # squash topic's commits onto main's merge-base
buckley commit --squash main --push=false
buckley commit --squash main --force-with-lease   # push the rewritten branch
```

`--squash <base>` soft-resets the current branch to its merge-base with
`<base>`, then creates one commit with a generated message summarizing the
whole range (subject plus a body listing the key changes). Safety:

- refuses if the working tree has uncommitted tracked changes;
- refuses to squash a protected branch (`main`/`master`) unless `--force`;
- prints the pre-squash `HEAD`, recoverable from the reflog (`git reset
  --hard <printed sha>`);
- never force-pushes: squashing rewrites history, so pushing the result
  needs the explicit `--force-with-lease` flag. Without it, the commit is
  created locally and left unpushed.

### pr

Generate and create a GitHub pull request.

```bash
buckley pr [OPTIONS]
```

**Options:**
| Flag | Description |
|------|-------------|
| `--dry-run` | Show generated PR without creating |
| `--output <file>` | Also write the title and body to `<file>`, in the form `--body-file` reads. With `--dry-run` this saves the text for review |
| `--body-file <file>` | Post `<file>` as it is, without a model call. A first line `Title: ...` is the title; the rest is the body |
| `--title <text>` | Use this title instead of the generated one or the one in `--body-file` |
| `--breaking <auto\|keep\|never>` | Breaking Changes section policy (default `auto`), the same rule as for `buckley commit`. See [Breaking changes](#breaking-changes) |
| `--yes` | Skip the confirmation prompt |
| `--head <branch>` | Branch the PR comes from (default: the checked-out branch). `owner:branch` names a fork's branch; `owner` must own the repository of the push remote (`BUCKLEY_REMOTE_NAME`, default `origin`) |
| `--base` | Base branch. Default: `BUCKLEY_PR_BASE`; else the remote's default branch (`refs/remotes/<remote>/HEAD`, which `git remote set-head <remote> --auto` refreshes); else `main`, `master` or `develop` on the remote, then the same names locally. An error when none exists |
| `-C <dir>` / `--dir <dir>` | Run as if started in `<dir>`, like `git -C`. Relative `--context-file`, `--config` and `--agent` (`BUCKLEY_AGENT`) paths resolve against the directory you launched in, so a file in the target repository cannot stand in for them. Uppercase `-C`: lowercase `-c` selects the config file |
| `--push=false` | Do not push the head branch before creating the PR |
| `--draft` | Create the PR as a draft |
| `--no-update` | Fail instead of replacing the title and body of an open PR for the head |
| `--diff-budget <bytes>` | Byte budget for the diff in the prompt (default 300000) |
| `--require-verification` / `--require-receipt` / `--no-require-verification` | Set the Buckbot verification label on the PR |
| `--verbose` | Show the context audit and the model's reasoning |
| `--cost=false` | Do not print the token and cost lines |
| `--context-file <path>` | Author notes for the PR; repeatable, oldest first. See [Author notes](#author-notes) |
| `--template=false` | Keep Buckley's own sections when the repository has a pull request template. See [Pull request template](#pull-request-template) |
| `--strict` | Fail, and create nothing, when the generated text states a decision or a number that has no source (a command that has no source only warns). Default: warn. See [Checks against the sources](#checks-against-the-sources) |
| `--model <id>` | Override the PR model |
| `--backend <api\|codex\|claude>` | Select the one-shot backend |
| `--effort <level>` | Override reasoning effort. No effect for a model that cannot reason and answer a required tool call in one request (`qwen/qwen3.8-flash` on OpenRouter); stderr then says so |
| `--timeout <duration>` | Time limit for generating the text, retries included. Default: `BUCKLEY_TIMEOUT_PR`, then `BUCKLEY_ONESHOT_TIMEOUT`, then `5m` |

**Environment Variables:**
- `BUCKLEY_MODEL_PR` - Override model for PR generation
- `BUCKLEY_PR_BACKEND` - Override backend; falls back to `BUCKLEY_ONESHOT_BACKEND`, then `api`
- `BUCKLEY_EFFORT_PR` - Override effort; falls back to `BUCKLEY_ONESHOT_EFFORT`, then reasoning config
- `BUCKLEY_TIMEOUT_PR` - Time limit for generation; falls back to `BUCKLEY_ONESHOT_TIMEOUT`, then `5m`. `pr merge` uses it too
- `BUCKLEY_PROMPT_PR` - Override prompt template
- `BUCKLEY_PR_BASE` - Base branch when `--base` is not given
- `BUCKLEY_REMOTE_NAME` - Remote for fetches and pushes (default `origin`)

**Example:**
```bash
buckley pr                    # Create PR for current branch
buckley pr --dry-run          # Preview PR title and body
buckley pr --base develop     # Target specific base branch
buckley pr --head feature -C ~/work/repo --yes   # PR for a branch another process has checked out
```

#### Review the text, then post that text

Each run asks the model again, so the text of a dry run is not the text a later
run would post. To post what you read:

```bash
buckley pr --dry-run --output pr.md --context-file pr-notes.md   # 1. write the text
$EDITOR pr.md                                                     # 2. read it, change it
buckley pr --yes --body-file pr.md                                # 3. post that file
```

`--output` writes `Title: <title>`, a blank line and the body. `--body-file`
reads the same form and makes no model call, so it needs no provider key; the
head and base checks, the push and the open-PR lookup run as usual. `--title`
replaces the title in either mode. `--body-file` cannot be combined with
`--context-file`. Under `-C`, both paths resolve against the directory you
launched in.

#### Output for scripts

When standard output is a terminal, `buckley pr` shows a progress spinner, a
header, a preview box and the text. When it is a file or a pipe, standard output
holds the text once, as `--output` writes it, and then, after a real run, the
PR's URL on a line of its own:

```text
Title: add(api): retry budget

## Summary
...
https://github.com/owner/repo/pull/12
```

Progress, the model line, warnings and the cost go to stderr, each as one plain
line: no spinner frames, no cursor escapes, no box. So `buckley pr --dry-run >
pr.md` writes a file that `--body-file pr.md` posts.

#### Pull request template

When the repository has a pull request template, the body follows it. Buckley
reads `.github/PULL_REQUEST_TEMPLATE.md`, `PULL_REQUEST_TEMPLATE.md` or
`docs/PULL_REQUEST_TEMPLATE.md` (also in lower case) from the base branch, where
GitHub reads it, and from the head branch when the base has none. An uncommitted
template is not read. `--template=false` keeps Buckley's own sections.

The body keeps the template's shape:

1. **Headings.** The headings at the template's level are exactly the
   template's, in its order, whether the template has sections that ask for
   text or only fixed text and checkboxes. Buckley adds nothing after the
   template as a new top-level section. A Testing, Counters, Evidence, Not
   verified or Known gaps section of the notes that no template section takes,
   the Not verified statement, the breaking-change statement, the related
   issues, and (for a template with no section that asks for text) Buckley's own
   Summary, Changes, Testing and Review Focus are written as subsections one
   level below the template's deepest heading, at the end of one template
   section. A heading inside such a section moves down with it, so that none
   reaches the template's level; headings in code fences stay. At heading level
   6 the label is bold text. The section is the first whose heading, guide
   comments or checkbox text has a word that starts with a keyword of the kind
   (case-insensitive):

   | Kind | Keywords |
   |------|----------|
   | Testing, Evidence | `test`, `validat`, `verif`, `correct` |
   | Counters | `counter`, `perf`, `benchmark` |
   | Not verified, Known gaps | `unvalidated`, `not verified`, `unverified`, `unsure`, `risk`, `limit`, `gap` |
   | Breaking changes | `break`, `compat` |
   | Related issues | `issue`, `relat`, `link` |

   When no section has a keyword (or the kind has none), the block goes to the
   end of the last template section that is not a pure checklist. Only when
   every section is a pure checklist does the block follow Buckley's own
   sections as a top-level section (the template stays last). Then
   `buckley pr` warns on stderr and names the notes section, and `--strict`
   makes that an error. The generated Not verified statement also warns in that
   case, but `--strict` does not fail on it, because only the author can fix an
   author section that has no place.
2. **Checkboxes.** An item is a checkbox line of the template outside code
   fences and HTML comments, in a section or before the first heading. Its
   identity is its text without inline HTML comments, white space collapsed;
   case matters. Every item is written word for word, in the template's order.
   A box is ticked only when the notes contain a ticked line with the same item
   text in a section the template section takes, or in a notes section headed
   `Checklist`. A checkbox-shaped line in a fence or a comment is fixed text.
   A checkbox line in a section the template section takes that matches no item
   of that section is not written; stderr names it, and `--strict` makes that an
   error. The author's other lines in the section follow the checklist.
   A notes section headed `Checklist` counts only when a template is used (with
   `--template=false` or no template it is an ordinary section). It only ticks;
   its other text goes after the checklist of a template section with the same
   heading, else it is placed like a section of no known kind, or, when the
   template is only checklists, named in a warning (an error with `--strict`).
   In a template section without checkboxes, and in a placed section, the
   author's checkbox lines are the author's own list and are written as the
   author wrote them. The model's checkbox lines are dropped everywhere, with one
   warning that counts them: the model never ticks, adds or removes a checkbox.
3. **Fixed text.** The template's fixed text is written word for word, except
   that a blank line may be added or removed at the edge of a section and line
   endings are normalized. The guide comments of a section that received text
   are left out; a section that received nothing stays as the template has it.
4. **Open sections.** A section that has only comments under its heading asks
   for text. A notes section whose heading equals the template heading fills it
   word for word (see [Author notes](#author-notes)); so does a section of the
   kind that the heading names. Otherwise the model writes one text for it,
   guided by the comments, and that text replaces them. A section the model
   leaves empty stays as the template has it. Text the model writes for a
   section that a notes section already fills is ignored.

A template in which every section is a pure checklist (or that has no headings)
follows Buckley's own sections; the author's ticks still apply to it.
`--template=false` ignores the template.

#### Author notes

`--context-file` gives the model notes that the diff cannot show: why the change
exists, what was run, what was measured. The model reads them as data, not as
instructions, and reads at most 16,000 bytes of them; `buckley pr` warns when it
cuts them.

A model rewrites what it reads, so the parts that must not change are copied by
code. A section of a notes file reaches the PR body word for word, under the
author's heading, when the heading names one of these:

| Section | Headings that select it |
|---------|-------------------------|
| Testing | starts with `Testing`, `Tests`, `Test plan`, `Verification`, `Validation`, `How to verify` or `How to test` |
| Counters | contains the word `counters` |
| Evidence | contains the word `evidence` |
| Not verified | contains `not verified`, `unverified`, `not verify`, `not tested`, `untested`, `not measured`, `not validated` or `not checked` |
| Known gaps | contains `known gaps`, `known limitations` or `known issues`, or is `Gaps` or `Limitations` |

A section starts at a Markdown heading (`## Evidence`) and ends at the next
heading of the same or a higher level. A short label on a line of its own
(`Evidence` above a list, or `Not verified:`) also starts one; it ends at the
next heading or the next such label. An author Testing section replaces the
testing steps the model writes. When no notes file has a Not verified section,
the body always says that the author stated nothing, also when the model wrote
text of its own for a Not verified slot of the template (that text follows the
statement).

HTML comments (`<!-- ... -->`) in a notes file are never sent to the model and
never published. Buckley removes every comment when it reads the file, wherever
it is: in prose, in inline code and in a fenced code block. The model, the
carried sections and the sources that the text is checked against all see the
note without them. A `<!--` that has no closing `-->` is an error that names the
file and the line: close the comment, or write the opener another way (for
example `&lt;!--`) when it is meant as text. The same error applies when removing
a comment would join the characters around it into a new `<!--` (for example
`<!<!-- x -->--`): the text after it cannot be trusted, so the file is refused
instead of being cut. Comments are removed once, when the file is read. Other
text in a carried section is copied word for word.

When two files have a section of the same kind, the body carries both, in the
order of the files. A later file never replaces an earlier one, so remove a
stale section from its file. The prompt still tells the model that a later note
is the current one for the text it writes.

```bash
buckley pr --dry-run --context-file handoff.md --context-file pr-notes.md
```

#### Checks against the sources

The sources of a PR text are the author's notes, the commit messages, the diff,
the file list and `AGENTS.md`. After the model answers, `buckley pr` compares
what it wrote with them:

- **The command check is a warning.** For a testing step that names a command
  (a backtick span that starts with a program such as `go`, `make` or a script
  path, or a step that starts with such a program in plain text, with its
  arguments in code format when it has any: ``go test ./pkg -run `TestFoo` ``),
  `buckley pr` looks for the command in the sources, with inline code marks
  ignored on both sides. When it does not find it, stderr names the step and the
  command text it could not find. The step stays in the body as written, and
  `--strict` does not fail on this warning. The check reads prose with a
  heuristic: it takes the text up to a lone `-` or `--`, a dash, a colon before
  a space, a semicolon, a parenthesis, or a plain word that is not a flag, a
  path or a flag value (such as `1` in `-count 1`; a trailing sentence period
  does not make a flag value). A flag in a code span after prose is checked on
  its own. It can miss an unsourced command and can warn about a sourced one.
  An unformatted plain-word flag value such as `-tags integration` ends the
  command; later flags before an explicit prose separator may be checked
  separately. To get exact testing steps, put the
  commands you ran in a Testing section of a notes file: such a section is
  copied by code, word for word.
- A word that states a decision (`rejected`, `approved`, `decided`, `ruled out`,
  `agreed`, `abandoned`, `dismissed`, `declined`) gets a warning when neither the
  notes nor the commit messages use that word.
- A number of two or more digits gets a warning when it appears nowhere in the
  sources.

Code cannot rewrite a sentence, so these are warnings on stderr and the text
stays as the model wrote it. A command in the running text of a template
section is a command warning too. With `--strict` the decision and number
warnings are errors: nothing is pushed or created, and a dry run exits
non-zero. Command warnings never are.

The evidence that buckley gathers for the PR text comes from commits. The diff,
commit log, file list, `AGENTS.md` and `.gitattributes` are read from the head
branch's git objects, not from the working tree, so uncommitted edits and the
checked-out branch do not reach it. That covers what buckley collects. With
`--backend codex` or `--backend claude`, the agent CLI runs in the directory
where buckley runs (the `-C` target, when given) and has read tools of its own,
so it can still read the checked-out branch and uncommitted files. Reading
`.gitattributes` from the head commit needs git 2.40 or newer; an older git makes
buckley warn and read the working tree's `.gitattributes` instead.
`$GIT_DIR/info/attributes` and `core.attributesFile` apply in both cases.

The checks that can fail without a model run before the model call:
confirmation (`--yes` or `--dry-run` when stdin is not a terminal), `gh`, the
head and base branches, at least one commit ahead, and conflicts with an open
PR.

`buckley pr` takes flags only. Name the branch with `--head`. The flag parser
stops reading flags at the first plain argument, so `buckley pr --yes feature -C /other`
is an error, not a PR for the directory you launched in.

How the head is read and pushed, with the worktree untouched in every case.
Fetching the remote branch skips `FETCH_HEAD` (git 2.29 or newer), so a `git pull`
that another process runs in the same checkout cannot pick up the fetched
branch:

| Head state | Evidence | Push |
|------------|----------|------|
| Local only | Local branch | Pushed (see `-u` below); an error before the model call with `--push=false` |
| Remote only | `<remote>/<branch>`, after fetching that one branch | None |
| Local equals remote | Local branch | Nothing to send, so no push and the branch config stays as it is. The one exception is the checked-out branch (no `--head`) when it does not track `<remote>/<branch>`: a push that sends nothing records the upstream |
| Local ahead | Local branch | Fast-forward (see `-u` below). With `--push=false`: `<remote>/<branch>`, with a warning, because the PR holds only what the remote has |
| Local behind | `<remote>/<branch>`, with a warning | None |
| Diverged | Refused | Never forced |

`-u` records an upstream. Without `--head`, `gh` finds the head through the
branch's upstream, so the push passes `-u` unless the branch already tracks
`<remote>/<branch>`. With `--head`, `gh` is told the head and another process may
have the branch checked out, so `-u` goes only to a branch that has no upstream.
An upstream that exists, even one that points elsewhere, is left alone.

Without `--head`, a detached `HEAD` is an error. The open-PR lookup is keyed on
the head branch and on the owner of the head repository (the push remote's
owner), because `gh` lists PRs by branch name only. Neither the PR for whichever
branch the worktree has checked out nor a PR from a same-named branch of
another repository is edited, and in a fork checkout your own PR is the one
found. An explicit `--base` that differs from the open PR's base is refused, not
retargeted; `--no-update` refuses any open PR for the head. Before an update
replaces the title and body of an open PR, `buckley pr` tries to save the text
it replaces under the git directory as `buckley/pr-<number>-<time>-<random>.md`
(the random part keeps two saves in one second apart) and stderr names the
file; `buckley pr --yes --body-file <that file>` puts it back. When the copy
cannot be written, a warning says so and the update goes on without a copy. If `gh pr create`
prints no PR URL, `buckley pr` fails instead of labeling a PR it cannot name.

### pr merge

```bash
buckley pr merge <number> [--squash|--merge|--rebase] [--admin] [--delete-branch]
```

Generates a squash or merge commit title and body from the PR's commits,
diff, and description (plain language, conventional `action(scope): subject`
grammar), then merges it with `gh pr merge --subject/--body`. A `--rebase`
merge preserves each original commit message, so no message is generated
and `--subject`/`--body` are not passed.

**Options:**
| Flag | Description |
|------|-------------|
| `--squash` / `--merge` / `--rebase` | Merge method (default: the repository's preferred allowed method — squash, then merge, then rebase) |
| `--admin` | Use administrator privileges to bypass merge requirements. Never implied by any other flag. |
| `--delete-branch` | Delete the local and remote branch after merge |
| `--dry-run` | Print the generated title and body without merging |
| `--yes` | Skip confirmation and merge |
| `--model <id>` / `--backend <api\|codex\|claude>` / `--effort <level>` / `--timeout <duration>` | Same as `buckley pr` |

Before merging, buckley runs `gh pr checks <number>` and refuses, listing
every check that is not passing, when any required check is failing or
still pending. This check is unconditional: `--admin` bypasses GitHub's own
merge requirements, not buckley's own check-passing gate.

**Example:**
```bash
buckley pr merge 42 --squash --delete-branch
buckley pr merge 42 --dry-run   # preview the generated squash message
```

For `commit`, `pr`, and `pr merge`, reasoning effort follows this precedence:
`--effort` > `BUCKLEY_EFFORT_COMMIT` or `BUCKLEY_EFFORT_PR` >
`BUCKLEY_ONESHOT_EFFORT` > `models.reasoning`. Accepted flag and environment
values are `low`, `medium`, `high`, `xhigh`, and `max`; other values fail with
an error. Existing config values such as `auto` and `off` retain their meaning.

The `codex` backend passes effort as `-c model_reasoning_effort="<value>"` to
`codex exec`. Without `--model`, `BUCKLEY_MODEL_COMMIT`, or `BUCKLEY_MODEL_PR`,
it omits the model argument so the Codex CLI selects its configured default.
Utility model config applies to the `api` backend. The `claude` backend ignores
effort and logs a debug note. API requests use the existing reasoning capability
checks and compatibility rules; choose an effort supported by the selected model.

```bash
buckley commit --backend codex --effort xhigh
buckley pr --backend codex --model gpt-6.1-sol --effort high
```

Both `commit` and `pr` use the `api` one-shot backend by default (OpenRouter, or
another configured provider). By default (`oneshot.data_policy: none`, or
`BUCKLEY_ONESHOT_DATA_POLICY=none`), these requests carry no
`provider.zdr`/`provider.data_collection` field and are not subject to the
durable-goal model-data-policy contract — only the OpenRouter account's own
privacy/guardrail settings apply, the same as any other request. Set
`oneshot.data_policy: zdr` to force zero data retention (relaxing only for a
recognized OSS-licensed workspace) or `oneshot.data_policy: deny` to force
non-ZDR retention with `data_collection: deny`; both opt-ins require the
workspace to carry recognized OSS license evidence and fail closed otherwise.
This setting is independent of Buckbot's `openrouter_privacy_fallback`.

### buckbot

Buckbot is Buckley's general-purpose review agent. It uses the same governed
review runtime as `review` and `review-pr`, while making the review intent
explicit in scripts, CI, and team workflows.

Unless overridden with `--model` or `BUCKLEY_MODEL_REVIEW`, Buckbot uses
`deepseek/deepseek-v4-pro-0813` through OpenRouter. The account's privacy and
guardrail settings must allow a matching endpoint.

If the configured Buckbot policy is
`openrouter_privacy_fallback: zdr_then_data_collection_deny`, Buckley tries a
ZDR route first and retries one policy-filtered 404 with
`data_collection: deny`. This makes DeepSeek usable when it has no ZDR route,
but it is not equivalent to ZDR; leave the setting disabled for strict
zero-retention reviews.

```bash
buckley buckbot                         # review current local scope
buckley buckbot --scope branch          # review the current branch
buckley buckbot repo                    # advisory repository-wide assessment
buckley buckbot repo --depth balanced   # evidence map plus targeted falsification
buckley buckbot repo --depth in-depth   # exhaustive coverage and verification
buckley buckbot pr 123                  # review a GitHub PR without posting
buckley buckbot pr 123 --post           # explicitly post that PR review
buckley buckbot --max-tool-calls 12      # opt into an experimental tool cap
```

`buckley buckbot repo` is equivalent to `buckley review --project`. It is
repository-wide, advisory-only, and cannot issue an approval verdict. Posting
to GitHub is never implicit: it requires `--post` on a PR review.

Repository reviews start with a complete Git-visible tracked-file inventory,
plus a filtered immutable capture of safe non-ignored untracked text files in
project/worktree reviews. The map is a navigable table of contents:
it includes repository metrics, complexity hotspots, major call sites, and
direct caller/callee edges for important flows, along with call-graph and parse
health. The model must expand that map into a coverage ledger for every
tracked source, test, configuration, and documentation area. Long files are
read in explicit pages, so a large file is not silently discarded. A report is
emitted only when it declares `COMPLETE` coverage; a timed-out or incomplete
pass is discarded rather than presented as a caveated review. Project reviews default to a 20-minute wall-clock window; use
`--timeout 45m` (or another explicit duration) for a larger repository. The
snapshot boundary excludes ignored paths, agent instructions, secret-like
paths/content, symlinks, binary/control content, and oversized files; excluded
paths are listed in the review rather than silently treated as inspected.

`review`, `review-pr`, and every `buckbot` alias accept
`--effort <level>`. Accepted levels are `low`, `medium`, `high`, `xhigh`, and
`max`. Precedence is `--effort` >
`BUCKLEY_EFFORT_REVIEW` > `BUCKLEY_ONESHOT_EFFORT` > `buckbot.reasoning`, with
`models.reasoning` and adaptive policy supplying existing defaults when
`buckbot.reasoning` is `auto`. Flag and environment overrides stay fixed across
review-size planning and adaptive model selection. Reviews select their backend
through the model's configured provider; use the `codex/` model prefix for Codex.

```bash
buckley review --model codex/gpt-6.1-sol --effort high
buckley review-pr 123 --model codex/gpt-6.1-sol --effort xhigh
buckley buckbot repo --model codex/gpt-6.1-sol --effort max
```

Every review surface supports three depth modes. `--depth spot` is the fast
health scan and is the default for compatibility. `--depth balanced` performs
a map-then-falsify pass and requires a model-directed verification attempt when
a repository gate exists. `--depth in-depth` removes generated turn/tool
ceilings (explicit operator caps still win), expands important call and state
paths, runs an adversarial pass, and requires a coverage plus verification
ledger. `--in-depth` is a shorthand for `--depth in-depth`; `standard`,
`detailed`, and `exhaustive` are accepted aliases for the corresponding modes.
No-test gates are typed as not applicable; required unavailable evidence fails
the pass instead of being silently counted as passing evidence.

Screenshots: `buckley review-pr 123 --image before.png --image after.png`
attaches up to five local PNG, JPEG or GIF files (10 MiB each, 20 MiB in total)
to the review as pixels. Buckley reads each file once, when the command starts,
and sends the same bytes with every model call of that review. A symlink, a
directory, a URL or a file that is not an image is refused. Text inside an
image and the captions are treated as data, not as instructions.
`--image` works only when the primary model and the critic model run on a provider
that carries the pixels. The Codex, native Anthropic, Google and Ollama providers
turn each message into text today, so Buckley refuses `--image` for them and
names the provider, instead of running a review that never saw the image. An
inline image is counted as a fixed 1,600 input tokens, an estimate, not by the
length of its file.

Verification: `buckley verify run|record|list` manage receipts, and
`buckley buckbot ... --require-verification=none|receipt|receipt-or-ci` sets
the evidence a review needs before it starts (exit 16 when missing, 17 when
failing, no tokens spent). See `docs/CONFIGURATION.md`, "Verification evidence".

A review that ends early prints `Stop reason: <kind>` and `Cause: <detail>` in its
"Incomplete review" banner and exits with a status for that kind: 10 budget
(the cause shows spent and limit), 11 timeout, 12 turn limit, 13 tool-call
limit, 14 provider error, 15 validation failure, 1 unclassified. The salvaged
text is not a merge verdict. With `--run-verification`, `--evidence-timeout <duration>` caps the host
verification that runs before the model (default: half of the time left; the
model keeps at least a quarter). See `docs/CONFIGURATION.md` for
`buckbot.budget_per_kloc_usd`, `buckbot.max_review_budget_usd` and
`buckbot.generated_globs`.

Buckbot is uncapped by default: a model is allowed to finish its review rather
than being truncated by an automatic dollar ceiling. Use `--budget <USD>` only
when a run needs an explicit spending limit. If an older project configuration
sets `per_review_budget_usd`, use `--no-budget` for a completion-first review.
Reviews also have no default per-review tool-call cap. Use
`--max-tool-calls <N>` when experimenting with an expensive model and you want
an explicit inspection/verification ceiling. The monetary and tool-call caps
are independent. Project reviews also have no hard model-turn or exploration
ceiling by default; the ordinary outer timeout, synthesis reserve, verification,
and governor safety controls still apply.

Child runs use an explicit execution contract. `step_cap`, `max_tool_calls`,
`max_model_requests`, `max_elapsed_seconds`, `max_cost_usd`, and
`timeout_seconds` can narrow an individual `spawn_subagent` request. Zero or an
omitted value means no contract-specific ceiling; repetition/cycle protection,
provider limits, cancellation, and operator emergency fuses still apply. When
action work stops after gathering evidence, Buckley reserves a final
tools-disabled synthesis request when the declared model-request and cost
ceilings still allow it. Model-request, tool, and elapsed ceilings are hard
harness limits. A cost ceiling is a conservative client-side admission limit,
not provider-side payment authorization: Buckley prices an explicit input and
output envelope before dispatch and rejects a response and all of its tool
calls if provider-reported usage crosses the remainder. Provider billing or
token accounting can still differ after a request has been accepted upstream.
The child is explicitly incomplete whenever Buckley cannot safely admit or use
the request.
Child stdout and stderr stream to a private 32 MiB spool; snapshots keep only a
256 KiB head/tail preview, while the retained transcript is pinned to evidence
before the spool is removed. A child that reaches the spool ceiling fails
explicitly with observed and retained byte counts.

For API models, Buckbot automatically exposes audited read-only code mode on
the immutable review snapshot when bubblewrap is working. This lets a model
compose repository-wide inventories and cross-file checks in one program
without network access or a mount of the live checkout. If the sandbox is
unavailable, Buckbot keeps the ordinary snapshot-rooted inspection tools. An
exact `--model` value remains exact in both cases.

### review

Review local changes with repository context.

```bash
buckley review                         # the worktree against the base branch
buckley review --scope branch          # commits only
buckley review --output review.md      # write the review to a file
buckley review > review.md             # the same through a redirect
```

| Flag | Description |
|------|-------------|
| `--scope <worktree\|branch\|changes>` | What to review (default `worktree`) |
| `--base <branch>` | Base branch to compare against |
| `--model <id>` / `--effort <level>` | Review model and reasoning effort (`BUCKLEY_MODEL_REVIEW`, `BUCKLEY_EFFORT_REVIEW`) |
| `--depth <spot\|balanced\|in-depth>` | Review depth (default `spot`) |
| `--output <file>` | Write the review to a file instead of standard output |
| `--no-interactive` | Never show the fix menu |
| `--timeout <duration>` | Total time limit |

After the review, a menu offers to let the model fix findings. The menu reads
answers from standard input, so it appears only when standard input and
standard output are both terminals. A script or an agent gets the review and
no menu, without `--no-interactive`. When standard output is a file or a pipe,
it holds the review text alone; the model line, the attempt counts and the cost
go to stderr.

### review-pr

Review a GitHub pull request. The command uses dry-run output by default.

```bash
buckley review-pr 123
buckley review-pr 123 -post
```

Use `-post` only when you want Buckbot to write to GitHub. A posted review adds
an eyes reaction, an intake comment, and the final review.

Buckbot binds review evidence to one PR head and its CI state. Re-run a review
when the head or CI checks change during a long-running pass.

### Prose style (plain language)

Buckley follows [decision 0012](hypha://m31labs/hyphae/decisions/0012-plain-language-writing-standard)
and the [writing-plainly skill](hypha://m31labs/hyphae/skills/writing-plainly).
Commit messages, PR titles and bodies, reviews, merge notes, and decline comments
use guidance for their reader's task; prose findings require reader harm and a
suggested rewrite, while commit headers, the 72-character limit, header repair,
body bullet rules, and PR JSON output stay unchanged.

### serve

Start the local HTTP/WebSocket IPC server (and optional embedded Mission Control UI).

```bash
buckley serve [OPTIONS]
```

**Options:**
| Flag | Default | Description |
|------|---------|-------------|
| `--bind` | `127.0.0.1:4488` | Address to bind |
| `--browser` | `false` | Enable the embedded GoSX Mission Control UI |
| `--assets` | | Optional external static UI override |
| `--allow-origin` | | Additional allowed CORS origins (repeatable) |
| `--require-token` | `false` | Require authentication token |
| `--auth-token` | | Set authentication token |

**Example:**
```bash
# Serve the embedded GoSX Mission Control UI
buckley serve --browser

# Production (with auth)
buckley serve --bind 0.0.0.0:4488 --require-token --browser
```

### remote

Manage remote Buckley sessions.

#### remote attach

Attach to a running remote session.

```bash
buckley remote attach --url <host> --session <id> [OPTIONS]
```

**Options:**
| Flag | Description |
|------|-------------|
| `--url` | Remote Buckley instance URL |
| `--session` | Session ID to attach to |
| `--token` | Authentication token |
| `--basic-auth-user` | Basic auth username |
| `--basic-auth-pass` | Basic auth password |

**Example:**
```bash
buckley remote attach \
  --url https://buckley.example.com \
  --session abc123 \
  --token "$BUCKLEY_IPC_TOKEN"
```

#### remote sessions

List active sessions on a remote instance.

```bash
buckley remote sessions --url <host> [--token <token>]
```

#### remote tokens

Manage API tokens on a remote instance.

```bash
buckley remote tokens list --url <host> --token <admin-token>
buckley remote tokens create --url <host> --name <name> --scope <scope>
buckley remote tokens revoke --url <host> --id <token-id>
```

#### remote login

Authenticate the CLI via a browser-approved ticket flow (recommended for hosted deployments with browser login).

```bash
buckley remote login --url <host> [--label <label>] [--no-browser]
```

#### remote console

Open an interactive shell on a remote session.

```bash
buckley remote console --url <host> --session <id>
```

### config

Manage Buckley configuration.

#### config check

Validate configuration and show diagnostic information.

```bash
buckley config check
```

**Output includes:**
- Configuration file locations and status
- API key validation (masked)
- Dependency checks (git, etc.)
- Validation errors with suggestions

#### config show

Display current effective configuration.

```bash
buckley config show
```

#### config path

Show configuration file paths.

```bash
buckley config path
```

### completion

Generate shell completion scripts.

```bash
buckley completion [bash|zsh|fish]
```

**Installation:**

```bash
# Bash (add to ~/.bashrc)
eval "$(buckley completion bash)"

# Zsh (add to ~/.zshrc)
eval "$(buckley completion zsh)"

# Fish (add to ~/.config/fish/config.fish)
buckley completion fish | source
```

### worktree

Manage git worktrees with optional container support.

#### worktree create

Create a new git worktree.

```bash
buckley worktree create [--container] <branch-name>
```

**Options:**
| Flag | Description |
|------|-------------|
| `--container` | Create with container environment |

### migrate

Apply database migrations.

```bash
buckley migrate
```

Run this after upgrading Buckley to ensure database schema is current.

### resume

Resume a previous session.

```bash
buckley resume <session-id>
```

**Example:**
```bash
# List recent sessions
buckley config show  # Shows session info

# Resume specific session
buckley resume abc123def456
```

### batch

Batch processing commands for CI/CD environments.

#### batch prune-workspaces

Clean up stale batch workspaces.

```bash
buckley batch prune-workspaces [--older-than <duration>]
```

### git-webhook

Start the git webhook listener for regression gates.

```bash
# Local (default bind is loopback)
buckley git-webhook --bind 127.0.0.1:8085

# Remote webhook receiver (requires a shared secret)
buckley git-webhook --bind 0.0.0.0:8085 --secret <webhook-secret>
```

See the [`git_events` configuration](./CONFIGURATION.md#git_events) for the
listener and release-command settings.

### agent-server

Start a small HTTP proxy that bridges editor workflows to the ACP gRPC server.

```bash
buckley agent-server --bind 127.0.0.1:5555 --acp-target 127.0.0.1:50051
```

Connect to a remote ACP server with mTLS:

```bash
buckley agent-server \
  --bind 127.0.0.1:5555 \
  --acp-target acp.example.com:50051 \
  --acp-ca-file ./acp-ca.pem \
  --acp-client-cert ./acp-client.pem \
  --acp-client-key ./acp-client-key.pem
```

## Interactive Commands

When running in interactive mode, use `/` prefix for commands:

| Command | Description |
|---------|-------------|
| `/help` | List all commands |
| `/quit`, `/exit` | Exit Buckley |
| `/clear` | Clear conversation |
| `/new` | Start new session |
| `/plan <name> <desc>` | Create feature plan |
| `/execute [task-id]` | Execute plan or task |
| `/status` | Show current status |
| `/plans` | List available plans |
| `/resume <plan-id>` | Resume a plan |
| `/pr` | Generate pull request |
| `/hunt` | Scan for code improvements |
| `/dream` | Get architectural ideas |
| `/search <query>` | Semantic code search |
| `/tools` | List available tools |
| `/models [filter]` | List available models |
| `/model <id>` | Switch to a different model |
| `/usage` | Show token/cost statistics |
| `/history [count]` | Show conversation history |
| `/export [file]` | Export conversation |
| `/config` | Show configuration |
| `/agents init` | Create AGENTS.md template |
| `/agents show` | Display project rules |
| `/agents reload` | Reload AGENTS.md |
| `/services status` | Show container service status |
| `/services up` | Start all services |
| `/services down [-v]` | Stop services |
| `/deps status` | Check dependency updates |
| `/sessions complete [id]` | Mark session completed |
| `/workflow status` | Show workflow state |
| `/workflow pause <note>` | Pause automation |
| `/workflow resume <note>` | Resume automation |

## Environment Variables

### Required

| Variable | Description |
|----------|-------------|
| `OPENROUTER_API_KEY` | OpenRouter API key (recommended) |

**Alternative providers** (set one or more):
- `OPENAI_API_KEY` - OpenAI API key
- `ANTHROPIC_API_KEY` - Anthropic API key
- `GOOGLE_API_KEY` - Google AI API key

### Optional

| Variable | Description |
|----------|-------------|
| `BUCKLEY_MODEL_PLANNING` | Override planning model |
| `BUCKLEY_MODEL_EXECUTION` | Override execution model |
| `BUCKLEY_MODEL_REVIEW` | Override review model |
| `BUCKLEY_MODEL_COMMIT` | Override model for `buckley commit` |
| `BUCKLEY_MODEL_PR` | Override model for `buckley pr` |
| `BUCKLEY_ONESHOT_BACKEND` | Default one-shot backend: `api`, `codex`, or `claude` |
| `BUCKLEY_COMMIT_BACKEND` | Override backend for `commit` |
| `BUCKLEY_PR_BACKEND` | Override backend for `pr` |
| `BUCKLEY_EFFORT_COMMIT` | Reasoning effort for `commit` |
| `BUCKLEY_EFFORT_PR` | Reasoning effort for `pr` and `pr merge` |
| `BUCKLEY_EFFORT_REVIEW` | Reasoning effort for `review`, `review-pr`, and `buckbot` |
| `BUCKLEY_ONESHOT_EFFORT` | Fallback effort: `low`, `medium`, `high`, `xhigh`, or `max` |
| `BUCKLEY_PROMPT_COMMIT` | Custom commit prompt template |
| `BUCKLEY_PROMPT_PR` | Custom PR prompt template |
| `BUCKLEY_ONESHOT_DATA_POLICY` | `commit`/`pr` OpenRouter privacy mode: `none` (default), `zdr`, or `deny` |
| `BUCKLEY_PR_BASE` | Override PR base branch |
| `BUCKLEY_TRUST_LEVEL` | Trust level: conservative, balanced, autonomous |
| `BUCKLEY_APPROVAL_MODE` | Approval mode: ask, safe, auto, yolo |
| `BUCKLEY_CODE_MODE` | Enable audited code mode for interactive or one-shot use |
| `BUCKLEY_ADAPTIVE_PROTOCOL_MODE` | Override adaptive protocol mode: legacy, shadow, or dynamic |
| `BUCKLEY_USE_TOON` | Enable TOON encoding (true/false) |
| `BUCKLEY_DISABLE_TOON` | Disable TOON encoding (true/false) |
| `BUCKLEY_QUIET` | Suppress non-essential output |
| `BUCKLEY_IPC_TOKEN` | IPC authentication token |
| `BUCKLEY_BASIC_AUTH_ENABLED` | Enable basic auth (true/false) |
| `BUCKLEY_BASIC_AUTH_USER` | Basic auth username |
| `BUCKLEY_BASIC_AUTH_PASSWORD` | Basic auth password |
| `BUCKLEY_SANDBOX` | Container mode: container, host, off |
| `BUCKLEY_REMOTE_BRANCH` | Default remote branch for pushes |
| `BUCKLEY_REMOTE_NAME` | Default remote name (default: origin) |
| `NO_COLOR` | Disable colored output |

## Examples

### Basic Usage

```bash
# Start interactive session
buckley

# Quick question (one-shot)
buckley -p "How do I implement pagination in this API?"

# Generate commit for staged changes
git add -p && buckley commit

# Create PR for current branch
buckley pr
```

### CI/CD Integration

```bash
# Execute a specific task in CI
buckley execute-task \
  --plan feature-auth \
  --task implement-jwt \
  --remote-branch automation/feature-auth

# Run without interaction
buckley --quiet -p "Run tests and fix any failures"
```

### Remote Development

```bash
# Start server for team access
buckley serve \
  --bind 0.0.0.0:4488 \
  --require-token \
  --browser

# Connect from another machine
buckley remote attach \
  --url https://buckley.example.com \
  --session $SESSION_ID \
  --token $BUCKLEY_IPC_TOKEN
```

### Scripting

```bash
#!/bin/bash
# Automated code review script

# Check for uncommitted changes
if ! git diff --quiet; then
  echo "Reviewing changes..."
  buckley --quiet -p "Review the following diff for issues: $(git diff)"
fi
```

## See Also

- [Configuration Reference](CONFIGURATION.md)
- [Troubleshooting](troubleshooting.md)
- [Running Durable Goals](goals.md)
- [Mission Control](MISSION_CONTROL.md)
