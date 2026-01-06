---
name: refactoring
description: Safe refactoring patterns and practices - improve code structure while maintaining behavior. Use when cleaning up code, reducing complexity, or eliminating duplication.
phase: execute
requires_todo: false
priority: 10
allowed_tools: [read_file, write_file, apply_patch, run_tests, search_text, search_replace, rename_symbol]
---

# Refactoring

## Purpose

Refactoring improves code structure without changing behavior. Good refactoring makes code easier to understand, modify, and extend.

## Core Principle

**Green → Refactor → Green**

Never refactor without tests. The tests are your safety net that ensures behavior doesn't change.

## The Refactoring Process

1. **Ensure tests are green** - All tests must pass before refactoring
2. **Make one small change** - Rename a variable, extract a function, etc.
3. **Run tests** - Verify they still pass
4. **Commit** - Small, safe commits let you rollback easily
5. **Repeat** - Continue with small, verified steps

## Common Refactoring Patterns

### Extract Function

**When:** A code block does something specific that could be named.

**Before:**
```go
// Calculate total with tax
subtotal := 0.0
for _, item := range items {
    subtotal += item.Price * item.Quantity
}
total := subtotal * 1.08
```

**After:**
```go
total := calculateTotalWithTax(items)

func calculateTotalWithTax(items []Item) float64 {
    subtotal := 0.0
    for _, item := range items {
        subtotal += item.Price * item.Quantity
    }
    return subtotal * 1.08
}
```

### Rename

**When:** Names don't clearly express intent.

**Before:**
```go
func proc(d []byte) ([]byte, error) {
    // ...
}
```

**After:**
```go
func processUserData(rawData []byte) ([]byte, error) {
    // ...
}
```

### Extract Variable

**When:** An expression is complex or used multiple times.

**Before:**
```go
if user.Age >= 18 && user.Age <= 65 && user.Country == "US" {
    // ...
}
```

**After:**
```go
isWorkingAgeUSResident := user.Age >= 18 && user.Age <= 65 && user.Country == "US"
if isWorkingAgeUSResident {
    // ...
}
```

### Inline

**When:** A function or variable adds no value.

**Before:**
```go
func isAdult(age int) bool {
    return age >= 18
}

if isAdult(user.Age) { ... }
```

**After:**
```go
if user.Age >= 18 { ... }
```

### Replace Magic Numbers

**When:** Numbers appear without explanation.

**Before:**
```go
if response.StatusCode == 429 {
    time.Sleep(60 * time.Second)
}
```

**After:**
```go
const (
    statusTooManyRequests = 429
    rateLimitBackoff = 60 * time.Second
)

if response.StatusCode == statusTooManyRequests {
    time.Sleep(rateLimitBackoff)
}
```

### Consolidate Conditional

**When:** Multiple conditions lead to the same action.

**Before:**
```go
if status == "pending" {
    return false
}
if status == "processing" {
    return false
}
if status == "queued" {
    return false
}
return true
```

**After:**
```go
incompleteStatuses := []string{"pending", "processing", "queued"}
return !contains(incompleteStatuses, status)
```

## When to Refactor

**Good times:**
- When adding a feature and the structure fights you
- After getting tests to pass (TDD refactor step)
- When you find yourself copying code
- When reviewing code and spotting complexity

**Bad times:**
- When tests are failing
- Right before a deadline
- Without understanding the code first
- When behavior needs to change (that's not refactoring)

## Red Flags to Refactor

Watch for these code smells:

- **Long functions** - Over 50 lines
- **Deep nesting** - More than 3 levels of indentation
- **Duplicated code** - Same logic in multiple places
- **Large structs/classes** - Too many responsibilities
- **Long parameter lists** - More than 3-4 parameters
- **Mysterious names** - `data`, `tmp`, `x`, `doIt`
- **Comments explaining complex code** - The code should explain itself

## Key Principles

- **One change at a time** - Don't mix refactoring with feature work
- **Keep tests green** - If a test fails, revert immediately
- **Small steps** - 5-10 minute refactorings, not hour-long rewrites
- **Commit frequently** - Easy rollback if something goes wrong
- **Improve, don't perfect** - Leave code better than you found it

## Anti-Patterns

- **Big bang refactoring** - Rewriting large chunks without incremental verification
- **Refactoring without tests** - How do you know behavior didn't change?
- **Premature optimization** - Refactoring for performance before measuring
- **Over-abstraction** - Adding layers of indirection that obscure intent
- **Bikeshedding** - Arguing about style details instead of meaningful improvements
