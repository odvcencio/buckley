# Ox Alpha: classify malformed patch responses after durable capture

Repository: `m31labs.dev/buckley`, MIT licensed at the repository root.
Return only a complete valid unified diff. Do not use Markdown fences or prose.

## Observed harness defect

`cmd/buckley-ox-once` currently treats any non-empty model text as a successful
patch. Three real bounded Ox runs were therefore reported as saved successes
even though `git apply --check` rejected them:

1. Markdown fences, prose, and bare `@@` hunk headers;
2. an invented source shape plus a truncated hunk; and
3. the correct one-line semantic change with incorrect hunk counts and no final
   newline.

Raw responses must still be preserved exactly for review and remote evidence.
The defect is the success classification, not the durable capture.

## Bounded change

Modify only:

- `cmd/buckley-ox-once/main.go`
- `cmd/buckley-ox-once/main_test.go`

After `writeExclusiveOutput` has successfully persisted the exact raw bytes,
validate the response deterministically against the already-pinned clean
repository root. A malformed or non-applicable response must return a nonzero
error that says the raw output was preserved and names its output path. A valid
response retains the current success message and zero exit behavior.

Add one small private validator with these rules:

- content begins exactly with `diff --git `;
- content ends with a newline;
- `git -C <canonical-root> apply --check --whitespace=nowarn -` accepts the
  content from stdin without modifying the worktree;
- use the existing restricted Git environment shape (`GIT_CONFIG_NOSYSTEM=1`
  and `GIT_TERMINAL_PROMPT=0`);
- include concise bounded Git stderr in the returned error;
- do not use `--recount`, fuzz, normalization, auto-repair, or retries.

The validator must run only after the exclusive output file closes
successfully, so every non-empty model response remains durable even when the
command exits nonzero. Do not delete, rewrite, trim, or normalize that output.

## Current call site

```go
	content, err := oxAlphaPatchText(response)
	if err != nil {
		return err
	}
	if err := writeExclusiveOutput(outputPath, []byte(content)); err != nil {
		return err
	}
	fmt.Printf("saved one Ox Alpha response for %s at %s\n", exactHead, outputPath)
	return nil
```

`main.go` already imports `bytes`, `context`, `errors`, `flag`, `fmt`, `os`,
`os/exec`, `path/filepath`, `strings`, and `time`. Reuse those packages.

## Tests

Add focused table-driven coverage showing:

- a real applicable unified diff is accepted;
- a fenced/prose-prefixed response is rejected;
- a response without a final newline is rejected;
- a truncated or wrong-count hunk is rejected; and
- a syntactically valid diff for nonexistent context is rejected.

Use `t.TempDir()` and the existing local Git fixture helpers. Do not call a
model or the network. Keep the patch under 150 net new Go lines.

The patch must be designed to pass:

```bash
go test ./cmd/buckley-ox-once -count=20
./scripts/test.sh
```
