---
name: test-driven-development
description: Implements features using TDD discipline - write failing test first, minimal implementation, then refactor. Use when implementing new functionality or fixing bugs.
allowed_tools: [read_file, write_file, run_tests, apply_patch, search_text]
phase: execute
requires_todo: false
priority: 10
---

# Test-Driven Development

## Process

When implementing features or fixing bugs, follow this cycle:

1. **Write the test first** - Create a failing test that defines the desired behavior
   - Think about the interface before the implementation
   - Focus on one specific behavior or requirement
   - Make the test as simple as possible

2. **Run the test** - Confirm it fails for the right reason
   - The failure message should clearly show what's missing
   - If it passes unexpectedly, the test isn't testing what you think
   - If it fails for the wrong reason, fix the test

3. **Minimal implementation** - Write just enough code to make the test pass
   - Don't add features that aren't tested
   - Don't optimize prematurely
   - Keep it simple and direct

4. **Run tests again** - Verify the test passes
   - All existing tests should still pass
   - If anything breaks, fix it before moving on

5. **Refactor** - Clean up code while keeping tests green
   - Improve names, structure, and clarity
   - Remove duplication
   - Run tests after each refactoring step

## TODO Integration

For multi-step implementations, consider creating TODOs following this pattern:

```
- Write test for [feature X]
- Implement [feature X] to pass tests
- Refactor [feature X] for clarity
- Write test for [feature Y]
- Implement [feature Y] to pass tests
- Refactor [feature Y] for clarity
```

Each cycle should be small enough to complete in minutes, not hours.

## Key Principles

- **Never write implementation code before the test exists** - This ensures you're actually testing something
- **Tests should fail initially** - This validates they actually test the behavior
- **Keep each cycle small** - Aim for 5-15 minute cycles
- **Refactor with confidence** - Green tests give you permission to improve code
- **One behavior per test** - Tests should be focused and clear

## Anti-Patterns to Avoid

- Writing multiple features before running tests
- Making tests pass by changing the test instead of the implementation
- Skipping the refactor step "to save time"
- Writing tests after implementation (that's not TDD)
- Making tests too complex or testing multiple things at once
