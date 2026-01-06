# ADR 0001: SQLite with WAL Mode for Persistence

## Status

Accepted

## Context

Buckley needs persistent storage for:
- Sessions and conversation history
- TODO items and checkpoints
- Embeddings for semantic search
- Skill activation state
- Telemetry and metrics

Key requirements:
- Single-binary deployment (no external database dependencies)
- Concurrent read access during streaming
- Cross-platform compatibility (Linux, macOS, Windows)
- Low latency for local operations
- Reliable crash recovery

Options considered:
1. **PostgreSQL/MySQL** - Too heavy, requires external setup
2. **In-memory with JSON files** - No transactional guarantees, complex recovery
3. **SQLite (default mode)** - Single-writer limitation causes blocking
4. **SQLite with WAL** - Write-ahead logging enables concurrent readers

## Decision

Use SQLite with Write-Ahead Logging (WAL) mode and the following configuration:

```go
// Enable WAL mode and busy timeout
db.Exec("PRAGMA journal_mode=WAL")
db.Exec("PRAGMA busy_timeout=5000")
db.SetMaxOpenConns(1)  // SQLite only supports single writer
```

## Consequences

### Positive
- Zero external dependencies - ships as single binary
- WAL mode allows concurrent reads during writes (important for streaming)
- 5-second busy timeout handles contention gracefully
- Automatic crash recovery via WAL replay
- Cross-platform with mattn/go-sqlite3 driver
- Excellent performance for local operations

### Negative
- Single writer limitation (mitigated by connection pooling)
- WAL files can grow during long transactions
- Not suitable for multi-machine distributed setups (not needed for Buckley)

### Risks
- Database locking under heavy load - mitigated by busy timeout and single writer pattern
- WAL checkpoint delays - acceptable for our use case
