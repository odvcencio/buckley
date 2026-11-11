## What does this PR do?

<!-- Say what changes for a user in one or two sentences. -->

## Why this approach?

<!-- Give the reason, and the main alternative you set aside. -->

## Correctness

- [ ] The changed behavior has a test that failed before the change
- [ ] The existing tests of the touched packages pass
- [ ] Edge cases (empty input, very long input) were tried by hand
- [ ] The change handles every error path it adds
- [ ] Output formats did not change, or the change is described above

<!-- Name the exact commands you ran. -->

## Performance

- [ ] Timing was compared before and after on the same machine
- [ ] Memory use of the touched path was measured
- [ ] The change adds no work to the common path
- [ ] A slower path is called out in the text above

<!-- Counters or timings go here if they matter. -->

## Maintainability

- [ ] The change stays inside the scope of this PR
- [ ] Names follow the style of the surrounding code
- [ ] No new dependency was added

## Self-review

Read the whole diff once before you ask for review. Note the spots that need a second pair of eyes.

- [ ] Read the full diff in the review tool
- [ ] Left a comment on each non-obvious choice
- [ ] Removed debugging leftovers
- [ ] Checked the commit messages for accuracy

## Due diligence

- [ ] Read the code around each change before editing it
- [ ] Looked for the same pattern elsewhere and kept it consistent
- [ ] Listed the known risks and limits of this change
- [ ] Linked the issue or discussion, if there is one
