# Chat Loop Performance Analysis

## Executive Summary

This document analyzes the chat loop implementations in Buckley for performance faults and runaway execution risks. The analysis covers three primary loop implementations: `toolrunner`, `headless`, and `ralph` executors.

**Risk Level: MEDIUM**
- Multiple safety mechanisms are in place
- No evidence of infinite loop vulnerabilities
- Some inconsistencies in limit configurations that should be standardized

## Loop Implementations

### 1. ToolRunner Loop (`pkg/toolrunner/runner.go`)

**Location:** Lines 456-643 (main execution loop)

**Iteration Limits:**
```go
const defaultMaxIterations = 25
const defaultMaxToolsPhase1 = 15
```

**Key Safety Mechanisms:**
1. **Hard iteration cap** at 25 iterations (line 20)
2. **Context cancellation checking** at each iteration (lines 459-462, 496-499)
3. **Tool result deduplication** via `toolResultDeduper` (lines 454, 631)
4. **Streaming error propagation** (lines 500-506)

**Potential Issues:**
- ❌ No cost-based termination - could spend significant money in fewer than 25 iterations with expensive models
- ❌ Tool deduplication only catches exact duplicates, not semantic duplicates
- ⚠️ `maxToolsPhase1` limit of 15 tools may be too high for some use cases

**Code Quality:**
- ✅ Clean separation of concerns
- ✅ Proper context handling
- ✅ Streaming support with proper cleanup

---

### 2. Headless Runner Loop (`pkg/headless/runner.go`)

**Location:** Lines 497-565 (conversation loop)

**Iteration Limits:**
```go
maxIterations := 50 // Hardcoded at line 512
```

**Key Safety Mechanisms:**
1. **Hard iteration cap** at 50 iterations
2. **Context cancellation** via `cancelFunc` (lines 498-502)
3. **State watcher** for pause/stop (lines 527-529, 578-598)
4. **Idle timeout** and **max runtime** timers (lines 443-459)
5. **Tool execution timeout** clamping (lines 1127-1140)

**Potential Issues:**
- ❌ Hardcoded 50-iteration limit differs from toolrunner's 25
- ❌ No cost tracking integration in the loop itself
- ⚠️ State watcher polls every 200ms (line 582) - acceptable but could be event-driven

**Code Quality:**
- ✅ Comprehensive event emission
- ✅ Approval flow integration
- ✅ Policy engine integration

---

### 3. Ralph Executor Loop (`pkg/ralph/executor.go`)

**Location:** Lines 179-202 (main loop), 204-349 (iteration execution)

**Iteration Limits:**
```go
// Configurable via session.MaxIterations
if e.session.MaxIterations > 0 && e.session.Iteration() >= e.session.MaxIterations {
    return nil
}
```

**Key Safety Mechanisms:**
1. **Configurable iteration limits** via session settings
2. **Context timeout** support (lines 141-145)
3. **Rate limit backoff** with parking detection (lines 272-290)
4. **Session state tracking** with proper cleanup (defer block, lines 156-177)

**Potential Issues:**
- ⚠️ No default iteration limit if `MaxIterations` is 0
- ⚠️ Backend rate limit recovery could theoretically loop indefinitely (though unlikely)
- ❌ No cost tracking in the main loop

**Code Quality:**
- ✅ Most configurable of the three implementations
- ✅ Proper session state machine
- ✅ Comprehensive logging

---

## Safety Mechanism Comparison

| Mechanism | ToolRunner | Headless | Ralph |
|-----------|-----------|----------|-------|
| Iteration Limit | 25 (hardcoded) | 50 (hardcoded) | Configurable |
| Context Cancellation | ✅ | ✅ | ✅ |
| Cost Tracking | ❌ | ❌ | ❌ |
| Timeout Support | ❌ | ✅ (idle+max) | ✅ (session) |
| Rate Limiting | ❌ | ❌ | ✅ |
| Deduplication | ✅ | ❌ | ❌ |

## Performance Faults Identified

### 1. Inconsistent Iteration Limits (MEDIUM PRIORITY)

**Issue:** Different components use different hardcoded limits:
- ToolRunner: 25 iterations
- Headless: 50 iterations
- Ralph: Configurable (no default)

**Risk:** Confusing behavior when switching between modes. A task that succeeds in one mode might fail in another.

**Recommendation:** Standardize on a single default (25 recommended) and make all limits configurable.

### 2. No Cost-Based Termination (HIGH PRIORITY)

**Issue:** None of the loops terminate based on actual spend. A single expensive model call could cost $1+ without triggering any limit.

**Risk:** Runaway costs with expensive models (Claude Opus, GPT-4, etc.)

**Recommendation:** Add cost accumulator to each loop with configurable max cost per session/request.

### 3. Missing Deduplication in Headless/Ralph (MEDIUM PRIORITY)

**Issue:** Only ToolRunner has tool result deduplication. Headless and Ralph could execute identical tool calls repeatedly.

**Risk:** Wasted API calls and tokens on redundant operations.

**Recommendation:** Port the `toolResultDeduper` to all loop implementations.

### 4. No Request-Level Timeouts in ToolRunner (LOW PRIORITY)

**Issue:** ToolRunner relies on context cancellation but doesn't enforce a maximum execution time per request.

**Risk:** Slow model responses could cause unexpected delays.

**Recommendation:** Add optional timeout parameter to `toolrunner.Request`.

## Test Coverage

Created comprehensive chaos tests in `tests/integration/chat_loop_chaos_test.go`:

1. **TestChatLoop_IterationLimit** - Verifies hard iteration caps work
2. **TestChatLoop_ContextTimeout** - Tests context cancellation
3. **TestChatLoop_CostTrackingAccuracy** - Validates cost tracking
4. **TestChatLoop_HeadlessSessionLoop** - Tests headless runner limits
5. **TestChatLoop_ToolResultDeduplication** - Checks duplicate detection
6. **TestChatLoop_MemoryBudgetTest** - Verifies memory safety
7. **TestChatLoop_RapidCancellation** - Tests rapid start/stop cycles
8. **TestChatLoop_ConversationCompaction** - Tests context window management

## Recommendations

### Immediate (High Priority)
1. Add cost-based termination to all loops
2. Standardize iteration limit defaults
3. Add comprehensive telemetry for loop metrics

### Short-term (Medium Priority)
4. Port deduplication to all implementations
5. Add request-level timeouts
6. Implement loop health metrics

### Long-term (Low Priority)
7. Consider unifying loop implementations
8. Add adaptive limits based on model pricing
9. Implement loop visualization/debugging tools

## Appendix: Loop Detection Checklist

When reviewing chat loop code, verify:

- [ ] Iteration limit is enforced at loop start
- [ ] Context cancellation is checked each iteration
- [ ] Cost tracking is integrated
- [ ] Timeout is configurable
- [ ] Tool call deduplication is active
- [ ] Progress/events are emitted
- [ ] Errors don't cause infinite retries
- [ ] Resources are cleaned up on exit

## References

- `pkg/toolrunner/runner.go` - Tool execution loop
- `pkg/headless/runner.go` - Headless session loop
- `pkg/ralph/executor.go` - Ralph iteration loop
- `tests/integration/chat_loop_chaos_test.go` - Chaos engineering tests
