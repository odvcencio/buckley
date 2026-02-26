# ADR 0005: Context Compaction Strategy

## Status

Accepted

## Context

LLM context windows are finite. Long conversations exceed limits, causing:
- Failed API calls when context is exhausted
- Degraded quality as important context is truncated
- High costs from unnecessarily large prompts

Requirements:
- Automatically manage context before hitting limits
- Preserve important information (system prompts, recent messages)
- Graceful degradation when summarization fails
- Configurable thresholds per use case

Options considered:
1. **Hard truncation** - Drop old messages. Simple but loses critical context.
2. **Sliding window** - Keep last N messages. Loses early context that may be relevant.
3. **Summarization with thresholds** - Compress old messages into summaries at configurable points.

## Decision

Implement automatic compaction with summarization:

```go
// Trigger compaction at 75% of context limit
func (cm *CompactionManager) ShouldCompact(conv *Conversation, maxTokens int) bool {
    threshold := float64(maxTokens) * cm.cfg.Memory.AutoCompactThreshold
    return float64(conv.TokenCount) >= threshold
}

// Compact with retry and graceful degradation
for attempt := 0; attempt < maxRetries; attempt++ {
    summary, err = cm.generateSummary(toSummarize)
    if err == nil {
        break
    }
    time.Sleep(time.Duration(1<<uint(attempt)) * time.Second)
}

// Fallback if summarization fails
if lastErr != nil && summary == "" {
    summary = fmt.Sprintf("[%d messages truncated - summary unavailable]", len(toSummarize))
}
```

Segment selection:
- Always preserve system messages and persona content
- Summarize oldest non-system messages
- Keep recent messages intact for continuity

## Consequences

### Positive
- Conversations can run indefinitely without manual intervention
- Important context preserved through summarization
- Graceful degradation when LLM summarization fails
- Configurable threshold (default 75%) balances efficiency and safety

### Negative
- Summarization adds latency and cost
- Some detail loss is inevitable
- Summary quality depends on model capability

### Configuration
```yaml
memory:
  auto_compact_threshold: 0.75
  max_compactions: 0          # 0 = unlimited
  summary_timeout_secs: 30
```
