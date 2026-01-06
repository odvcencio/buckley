---
name: code-review
description: Structured code review checklist covering correctness, testing, performance, security, and maintainability. Use when reviewing implementation before merge or PR creation.
phase: review
requires_todo: false
priority: 10
---

# Code Review

## Purpose

Code review catches bugs, ensures quality, shares knowledge, and maintains consistency. Good reviews are thorough but constructive.

## Review Checklist

### 1. Correctness

Does the code do what it's supposed to do?

- [ ] **Requirements met** - All acceptance criteria satisfied
- [ ] **Edge cases handled** - Null/empty/invalid inputs considered
- [ ] **Error handling** - Failures are caught and handled gracefully
- [ ] **Logic is sound** - No off-by-one errors, incorrect conditions, or race conditions
- [ ] **Type safety** - Correct types used throughout

### 2. Testing

Is the code adequately tested?

- [ ] **Tests exist** - New code has corresponding tests
- [ ] **Tests pass** - All tests run successfully
- [ ] **Coverage is adequate** - Critical paths and edge cases covered
- [ ] **Tests are clear** - Test names and assertions are understandable
- [ ] **Tests are isolated** - No dependencies between tests

### 3. Performance

Will this code perform well at scale?

- [ ] **No obvious bottlenecks** - O(n²) where O(n) would work
- [ ] **Resources are released** - Files closed, connections released, goroutines cleaned up
- [ ] **Caching where appropriate** - Expensive operations aren't repeated unnecessarily
- [ ] **Database queries optimized** - No N+1 queries, proper indexing
- [ ] **Memory usage reasonable** - No memory leaks or excessive allocation

### 4. Security

Does this code introduce vulnerabilities?

- [ ] **Input validation** - User input is sanitized and validated
- [ ] **SQL injection prevented** - Parameterized queries or ORM used
- [ ] **XSS prevented** - Output is properly escaped
- [ ] **Authentication checked** - Protected endpoints require auth
- [ ] **Authorization enforced** - Users can only access their own resources
- [ ] **Secrets not committed** - No API keys, passwords, or tokens in code
- [ ] **HTTPS enforced** - Sensitive data transmitted securely

### 5. Maintainability

Will this code be easy to work with in the future?

- [ ] **Clear naming** - Variables, functions, and types have descriptive names
- [ ] **Reasonable complexity** - Functions are focused and not too long
- [ ] **Documentation exists** - Non-obvious behavior is explained
- [ ] **No duplication** - Similar code is factored into reusable functions
- [ ] **Consistent style** - Follows project conventions and formatting
- [ ] **Dependencies justified** - New dependencies add clear value

### 6. Architecture

Does this code fit well with the existing system?

- [ ] **Separation of concerns** - Business logic, data access, and presentation are separate
- [ ] **Appropriate abstractions** - Interfaces and types are at the right level
- [ ] **Follows patterns** - Consistent with existing architectural patterns
- [ ] **Minimal coupling** - Changes here won't ripple through the codebase
- [ ] **Clear boundaries** - Module responsibilities are well-defined

## Review Process

1. **Read the description** - Understand what problem is being solved
2. **Check tests first** - Tests document intended behavior
3. **Review small chunks** - Don't try to review too much at once (max 400 lines)
4. **Ask questions** - "Why" questions lead to better understanding
5. **Suggest, don't demand** - Phrase feedback as suggestions when appropriate
6. **Acknowledge good work** - Call out elegant solutions and improvements

## Giving Feedback

**Good feedback is:**
- **Specific** - Point to exact lines and explain the issue
- **Actionable** - Suggest concrete improvements
- **Kind** - Critique code, not people
- **Balanced** - Mention both strengths and areas for improvement

**Example:**
- ❌ "This is wrong"
- ✅ "This loop has an off-by-one error on line 47. Should be `i < len(items)` instead of `i <= len(items)`"

## Severity Levels

Use these to prioritize feedback:

- **🔴 Blocking** - Must fix before merge (bugs, security issues, breaking changes)
- **🟡 Important** - Should fix soon (performance issues, missing tests, poor naming)
- **🟢 Nit** - Nice to have (style preferences, minor refactors)

## When to Approve

Approve when:
- All blocking issues are resolved
- Tests pass and provide adequate coverage
- Code meets quality standards
- You understand what the code does and why

## Red Flags

Stop and dig deeper if you see:
- Code that's hard to understand
- Missing tests for complex logic
- Copy-pasted code with minor variations
- Functions over 50 lines
- Deeply nested conditionals (3+ levels)
- Comments that apologize or express uncertainty
