# ADR 0007: TOON Encoding for Tool Outputs

## Status

Accepted

## Context

Tool outputs are included in LLM context. JSON encoding is verbose:
- Repeated keys waste tokens
- Quotes around strings add overhead
- Deeply nested structures balloon quickly

With per-token pricing, encoding efficiency directly impacts cost.

Requirements:
- Reduce token count for tool outputs
- Maintain semantic fidelity
- Fall back gracefully when needed
- Zero impact on tool functionality

Options considered:
1. **Standard JSON** - Universal but verbose
2. **MessagePack/CBOR** - Binary, not human-readable, models may struggle
3. **TOON (Text Object-Oriented Notation)** - Text-based, compact, LLM-friendly

## Decision

Use TOON encoding for tool outputs with JSON fallback:

```go
type Codec struct {
    useToon bool
}

func (c *Codec) Marshal(v any) ([]byte, error) {
    if !c.useToon || v == nil {
        return json.Marshal(v)
    }
    encoded, err := gotoon.Encode(v)
    if err != nil {
        return nil, fmt.Errorf("toon encode: %w", err)
    }
    return []byte(encoded), nil
}

// Always unmarshal as JSON (TOON is one-way to models)
func (c *Codec) Unmarshal(data []byte, v any) error {
    return json.Unmarshal(data, v)
}
```

TOON example:
```
// JSON (87 chars)
{"files": ["main.go", "config.go"], "count": 2, "success": true}

// TOON (~60 chars)
files[main.go,config.go]count:2,success:true
```

## Consequences

### Positive
- 20-40% token reduction for typical tool outputs
- Text-based format remains model-interpretable
- Configurable per deployment
- Graceful JSON fallback

### Negative
- Non-standard format may confuse some models
- One-way encoding (model responses still JSON)
- Slight encoding overhead

### Configuration
```yaml
encoding:
  use_toon: true  # Default enabled
```

Or via environment:
```bash
BUCKLEY_USE_TOON=false  # Disable if causing issues
```
