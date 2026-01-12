# Context Management Overhaul

## Problem

Conversations degrade after ~5 turns due to:
1. RLM strategy hardcodes 5-message window with 500-char truncation
2. Headless mode defaults to 8192 token context (too small)
3. Token budget trimming drops messages without summarization
4. Compaction exists but is never wired up
5. Tool output formatting inconsistent between modes

## Solution

Unified context management with async compaction-as-a-tool.

## Changes

### 1. Remove RLM Hardcoded Limits

**File:** `pkg/execution/rlm_strategy.go`

Delete lines 150-161:
```go
// REMOVE THIS:
start := len(messages) - 5
if start < 0 {
    start = 0
}
for _, msg := range messages[start:] {
    content := contentToString(msg.Content)
    sb.WriteString(fmt.Sprintf("\n**%s**: %s", msg.Role, truncate(content, 500)))
}
```

Replace with shared context builder that respects token budget.

### 2. Use Full Model Context Window

**File:** `pkg/headless/context_budget.go`

Change:
```go
// FROM:
headlessDefaultContextWindow = 8192

// TO:
headlessDefaultContextWindow = 0  // 0 = use model's actual context window
```

Update `headlessPromptBudget()` to query model info first, fall back to 128K if unknown.

### 3. Unify Tool Output to TOON

**File:** `pkg/toolrunner/runner.go` (lines 479-489)

Change:
```go
// FROM:
if data, err := json.Marshal(toolResult.Data); err == nil {
    return ToolExecutionResult{Result: string(data), ...}
}

// TO:
result, err := tool.ToJSON(toolResult)  // Uses TOON codec
if err == nil {
    return ToolExecutionResult{Result: result, ...}
}
```

This makes Classic mode consistent with RLM mode.

### 4. Compaction as Tool

**New file:** `pkg/tool/builtin/compact.go`

```go
type CompactContextTool struct {
    compactor *conversation.CompactionManager
}

func (t *CompactContextTool) Name() string { return "compact_context" }

func (t *CompactContextTool) Description() string {
    return "Summarize older conversation context to free up token budget. " +
           "Use when conversation is long or before complex multi-step work."
}

func (t *CompactContextTool) Execute(params map[string]any) (*builtin.Result, error) {
    // Trigger async compaction
    go t.compactor.CompactAsync(ctx)
    return &builtin.Result{
        Success: true,
        Data: map[string]any{"status": "compaction_started"},
    }, nil
}
```

### 5. Wire Up Async Compaction

**File:** `pkg/conversation/compaction.go`

Add async method:
```go
func (cm *CompactionManager) CompactAsync(ctx context.Context) {
    go func() {
        result, err := cm.Compact(ctx)
        if err != nil {
            // Try fallback models
            for _, model := range cm.fallbackModels {
                result, err = cm.compactWith(ctx, model)
                if err == nil {
                    break
                }
            }
        }
        if result != nil {
            cm.onComplete(result)
        }
    }()
}
```

**Fallback chain:**
1. `moonshotai/kimi-k2-thinking` (primary - cheap, good)
2. Configured fallbacks from `config.Compaction.Models`
3. Aggressive truncation with `[Earlier context summarized]` marker

### 6. Auto-Compaction Triggers

**File:** `pkg/conversation/compaction.go`

```go
type CompactionConfig struct {
    ClassicAutoTrigger float64  // 0.75 = 75% context usage
    RLMAutoTrigger     float64  // 0.85 = 85% (scratchpad helps)
    CompactionRatio    float64  // 0.45 = compact oldest 45%
}

func (cm *CompactionManager) ShouldAutoCompact(mode string, usageRatio float64) bool {
    threshold := cm.cfg.ClassicAutoTrigger
    if mode == "rlm" {
        threshold = cm.cfg.RLMAutoTrigger
    }
    return usageRatio >= threshold
}
```

### 7. Runtime Mode Toggle

**File:** `pkg/ui/tui/controller.go`

Add command handler:
```go
func (c *Controller) handleModeCommand(mode string) error {
    strategy, err := c.factory.Create(mode)
    if err != nil {
        return err
    }
    c.strategy = strategy
    c.emitStatus(fmt.Sprintf("Switched to %s mode", mode))
    return nil
}
```

**File:** `pkg/headless/runner.go`

Same pattern - add `SetMode(mode string)` method.

### 8. Shared Context Builder

**New file:** `pkg/conversation/context_builder.go`

Extract context building to shared location:
```go
type ContextBuilder struct {
    tokenCounter TokenCounter
    compactor    *CompactionManager
}

func (cb *ContextBuilder) BuildMessages(
    conv *Conversation,
    budget int,
    mode string,
) []Message {
    // Check if compaction needed
    usage := cb.estimateUsage(conv)
    if cb.compactor.ShouldAutoCompact(mode, usage) {
        cb.compactor.CompactAsync(context.Background())
    }

    // Trim to budget (keeping most recent)
    return cb.trimToBudget(conv.Messages, budget)
}
```

Both ClassicStrategy and RLMStrategy use this.

## Config Defaults

```yaml
memory:
  auto_compact_threshold: 0.75  # Classic mode trigger

compaction:
  rlm_auto_trigger: 0.85        # RLM mode trigger
  compaction_ratio: 0.45        # Compact oldest 45%
  models:
    - moonshotai/kimi-k2-thinking
    - qwen/qwen3-coder
    - openai/gpt-5.2-mini
```

## File Summary

| File | Change |
|------|--------|
| `pkg/execution/rlm_strategy.go` | Remove 5-msg limit, use shared context builder |
| `pkg/headless/context_budget.go` | Use model's actual context window |
| `pkg/toolrunner/runner.go` | Use TOON for tool output |
| `pkg/tool/builtin/compact.go` | NEW - compact_context tool |
| `pkg/conversation/compaction.go` | Add async, fallback chain, auto-trigger |
| `pkg/conversation/context_builder.go` | NEW - shared context building |
| `pkg/ui/tui/controller.go` | Add /mode command |
| `pkg/headless/runner.go` | Add SetMode method |
| `pkg/config/config.go` | Add compaction config fields |

## Testing

1. Unit tests for ContextBuilder
2. Unit tests for CompactionManager async flow
3. Integration test: 20+ turn conversation maintains coherence
4. Integration test: Mode toggle mid-conversation
5. Integration test: Compaction fallback chain

## Migration

No breaking changes. Existing configs continue working with better defaults.
