# ADR 0003: Multi-Model Routing Strategy

## Status

Accepted

## Context

Different AI tasks benefit from different model capabilities:
- Planning requires strong reasoning and structure
- Execution needs reliable tool calling and code generation
- Review benefits from different perspective/critic capabilities
- Summaries for compaction can use faster, cheaper models

Requirements:
- Route requests to appropriate models by task type
- Support multiple AI providers via single API
- Handle model availability and fallbacks
- Cost optimization for high-volume operations

Options considered:
1. **Single model for all tasks** - Simple but suboptimal
2. **Direct multi-provider integration** - Complex, inconsistent APIs
3. **OpenRouter as unified gateway** - Single API, model marketplace, automatic fallbacks

## Decision

Use OpenRouter as the model gateway with distinct models for different workflow phases:

```yaml
models:
  planning: moonshotai/kimi-k2-thinking   # Default planning/execution/review model
  execution: moonshotai/kimi-k2-thinking
  review: moonshotai/kimi-k2-thinking
```

Buckley’s compaction summaries currently use `openai/gpt-5.2-codex-xhigh-mini` internally for cost control.

Implementation:
- Validate model availability against OpenRouter catalog at startup
- Stream responses for responsive TUI updates
- Rate limiting with token bucket algorithm
- Retry with exponential backoff for transient failures

## Consequences

### Positive
- Optimal model selection per task type
- Single API integration point (OpenRouter)
- Automatic fallbacks when models unavailable
- Cost optimization through model tiering
- Easy to swap models via configuration

### Negative
- OpenRouter dependency (single point of failure)
- Additional latency hop through proxy
- Usage-based pricing complexity

### Rate Limiting Strategy
```go
// Proactive rate limiting
limiter := rate.NewLimiter(rate.Limit(10), 50)  // 10 req/s, 50 burst
limiter.Wait(ctx)  // Block until token available
```

### Fallback Chain
1. Try primary model
2. On 429/503, wait with exponential backoff
3. After max retries, report error to user
