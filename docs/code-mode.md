# Code Mode

Code mode lets the model answer a question by **writing one program**
instead of making a dozen tool calls. It writes ordinary Go against a
small typed capability client, Buckley runs it in a sandbox, and only the
printed result comes back.

It is off by default. Turn it on per run:

```bash
buckley goal run --exec-program <run-id>
```

## Why it exists

A read-heavy investigation — list a tree, read six files, count matches —
costs one model round-trip per step, and every intermediate result lands
in context. A program does the whole thing in one call and prints the
answer.

Measured on one benchmark (count Go files and matching lines in a
directory tree): **7 model turns and $0.62 before, 1 turn and $0.01
after** the capability API was taught properly, with identical answers.
Your mileage varies with task shape; the structural win is that
composition happens in Go rather than in model turns.

## What a program looks like

```go
package main

import (
    "fmt"
    "strings"

    "execprogram/caps"
)

func main() {
    files, err := caps.WalkDir(".")
    if err != nil {
        panic(err)
    }
    todos := 0
    for _, f := range files {
        if !strings.HasSuffix(f, ".go") {
            continue
        }
        body, _, err := caps.ReadFile(f)
        if err != nil {
            continue
        }
        todos += strings.Count(body, "TODO")
    }
    fmt.Printf("todos=%d\n", todos)
}
```

The capability surface:

| Function | Returns |
|---|---|
| `caps.ReadFile(path)` | file contents (capped), truncation flag |
| `caps.ListDir(dir)` | one level; directories end in `/` |
| `caps.WalkDir(dir)` | the whole tree, workspace-relative |
| `caps.SearchText(pattern)` | literal matches with file and line |
| `caps.SearchTextGlob(pattern, glob)` | the same, restricted to `*.go` and friends |

Paths are workspace-relative. Standard library only — there is no module
fetching inside the sandbox.

## What the sandbox actually is

Two independent layers, both enforced below the model:

**The capability broker.** A per-run unix socket serving only the
functions above, jailed to the workspace with symlink escapes resolved
and refused, authenticated with a per-run token that expires. Every call
is recorded **before** it answers; a call that cannot be audited fails.
A run can be granted a narrower set than the default (`--exec-caps
minimal` drops whole-tree search).

**The process sandbox.** The program runs under bubblewrap with no
network, no host PID or IPC namespace, a read-only system view, and — the
important part — **no workspace mount at all**. The caps socket is the
program's only window into your repository. Writes are confined to a
scratch directory that is deleted afterward. The environment is scrubbed,
so nothing from Buckley's own process, including credentials, is visible.

If the sandbox is unavailable, the tool **refuses to start** rather than
running unsandboxed. Install `bubblewrap` to enable code mode:

```bash
sudo apt-get install bubblewrap    # Debian/Ubuntu
```

Some hosts restrict unprivileged user namespaces, which breaks the
sandbox even when the binary exists. Buckley probes for a working sandbox
rather than trusting the binary's presence, so you get a clear refusal
instead of a mid-run failure.

## Programs are durable

Every program is stored as evidence before it runs, and its output after.
That means a program that worked can be re-run for **zero model tokens**:

```
exec_program(reuse: "ev_TWKNOERODJAXX2PYALFKWOP76F")
```

Pay the model once to write a workflow; after that it is just compute.
The IDs appear in `buckley goal report` and `buckley goal audit`.

## What code mode is not

- **Not a write surface.** Programs read, transform, and compose. Edits,
  commits, pushes, and anything destructive stay with the ordinary
  approval-gated tools.
- **Not a network client.** There is no route out of the sandbox.
- **Not a replacement for the shell.** `run_shell` remains for actions a
  read-only program cannot take.

## Ferrous Wheel

Set `language: "fw"` on the tool call to write Ferrous Wheel instead of
Go. It is transpiled on the host with `ferrous-wheel emit` and the
resulting Go compiles in the same sandbox. Go is the default because
models write it natively.

## Auditing a code-mode run

```bash
buckley goal audit <run-id> | grep caps
```

```
21:44:59  caps files.list   ok     {"dir":"execmode-src","recursive":true}
21:44:59  caps files.read   ok     {"path":"execmode-src/broker.go"}
```

Every capability the program touched, with its outcome — including
denials and errors. If it is not in that list, the program did not do it.
