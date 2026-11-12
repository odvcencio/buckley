## What does this PR do?

Splits the token cache so that readers no longer block on writers.

## Correctness

- [x] The changed behavior has a test that failed before the change
- [ ] The existing tests of the touched packages pass
- [x] Edge cases (empty input, very long input) were tried by hand
- [x] The change handles every error path it adds
- [x] Added a changelog entry

The new test is `TestSplitCacheReaders`.

## Performance

- [x]   Timing was compared before and after on the same machine
- [x] Memory use of the touched path was measured

## Maintainability

- [x] No new dependency was added

## Self-review

- [x] Read the full diff in the review tool

## Checklist

- [x] Removed debugging leftovers
- [ ] Linked the issue or discussion, if there is one

## Testing

- `go test ./cache -run TestSplitCacheReaders -count=1` passed

## Counters

| step | before | after |
|---|---|---|
| lock waits | 412 | 0 |

## Not verified

- Windows was not tried.
