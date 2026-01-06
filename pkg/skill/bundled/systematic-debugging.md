---
name: systematic-debugging
description: Root cause analysis framework for bugs - systematic investigation before proposing fixes. Use when encountering unexpected behavior, test failures, or errors.
phase: execute
requires_todo: true
priority: 15
todo_template: |
  - Reproduce the bug with minimal test case
  - Identify the failing component or function
  - Trace root cause through call stack
  - Design fix with test coverage
  - Implement fix and verify resolution
---

# Systematic Debugging

## Core Principle

**Understand before fixing.** Jumping to solutions without understanding root cause leads to symptom fixes, not real fixes.

## The Four-Phase Framework

### Phase 1: Reproduce Reliably

Before anything else, create a minimal reproduction:

1. **Isolate the failure** - Strip away everything unrelated to the bug
2. **Create a test case** - Write a test that consistently demonstrates the failure
3. **Verify the reproduction** - Run it multiple times to ensure consistency
4. **Document the symptoms** - What's the expected vs. actual behavior?

**Why this matters:** If you can't reproduce it reliably, you can't verify your fix.

### Phase 2: Investigate Root Cause

Now dig into *why* it's happening:

1. **Add instrumentation** - Log relevant values, state, and flow
2. **Trace execution** - Follow the code path that leads to the failure
3. **Identify the divergence point** - Where does behavior deviate from expectations?
4. **Find the source** - What code or data causes the divergence?

**Key question:** "Where is the invalid state or incorrect logic introduced?"

### Phase 3: Analyze Patterns

Look for deeper issues:

1. **Is this a symptom of a larger problem?** - Could other code paths have the same issue?
2. **Why did this slip through?** - What validation or testing could have caught it?
3. **Are there similar bugs elsewhere?** - Search for the same pattern in other code

**Why this matters:** Fixing one instance while leaving others creates technical debt.

### Phase 4: Design & Implement Fix

Only now should you fix it:

1. **Design the fix** - What's the minimal change that addresses the root cause?
2. **Add test coverage** - Ensure the reproduction test passes after the fix
3. **Verify comprehensively** - Run full test suite to catch regressions
4. **Document if needed** - Add comments explaining non-obvious fixes

## TODO Integration

This skill **requires TODO tracking**. Create TODOs for each phase:

```
- Reproduce bug: [brief description]
- Investigate root cause in [component/function]
- Trace execution path from [entry point] to [failure point]
- Design fix for [root cause]
- Implement fix with test coverage
- Verify fix resolves issue without regressions
```

## Red Flags

Stop and re-investigate if you find yourself:

- Trying multiple random fixes to see what works
- Fixing symptoms without understanding the cause
- Adding defensive checks without knowing why they're needed
- Skipping test coverage "just this once"
- Saying "this should work" more than once

## Anti-Patterns

- **Guess-and-check debugging** - Trying fixes without understanding the problem
- **Symptom fixing** - Addressing the visible issue without finding root cause
- **Incomplete reproduction** - "It fails sometimes" is not a reproduction
- **Skipping instrumentation** - Trying to fix based on assumptions instead of data
