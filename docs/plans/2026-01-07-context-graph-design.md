# Context Graph Service Design

## Overview

A service that captures decision traces from Buckley sessions, indexes them for semantic search, and provides precedent-aware context to inform future work. The core insight: reasoning connecting data to action has never been treated as data. This service fixes that.

## Problem Statement

Buckley makes decisions constantly: which files to edit, how to structure code, when to escalate, what approach to take. These decisions reflect accumulated knowledge—patterns that worked, mistakes to avoid, tradeoffs considered. Today, this knowledge evaporates when a session ends.

Symptoms:
- Same mistakes repeated across sessions
- No learning from successful patterns
- Onboarding requires re-discovering tribal knowledge
- RLM escalation decisions based on heuristics, not history
- No audit trail connecting decisions to outcomes

## Goals

1. **Capture** decision traces at commit granularity
2. **Index** traces for semantic similarity search
3. **Surface** relevant precedents at decision time
4. **Track** decision outcomes (landed, reverted, iterated)
5. **Learn** from historical escalation patterns (RLM)
6. **Enrich** context for both Classic and RLM modes

## Non-Goals

- Real-time collaboration features
- Code review automation
- Replacing existing telemetry (complements it)
- User-facing dashboards (API-first, UIs can come later)

---

## Architecture

### System Overview

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         Buckley Instances                               │
│              (Classic mode, RLM mode, oneshot commands)                 │
└─────────────────────────┬───────────────────────────────────────────────┘
                          │ publish CommitTrace
                          ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                         NATS JetStream                                  │
│   Subjects:                                                             │
│     buckley.trace.commit    - commit traces from sessions               │
│     buckley.trace.decision  - individual decisions (optional fanout)    │
│     buckley.event.landed    - internal: merge webhook results           │
└─────────────────────────┬───────────────────────────────────────────────┘
                          │ subscribe
                          ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                       Context Service                                   │
│                                                                         │
│   Ingest:                                                               │
│     NATS Consumer         ← CommitTraces from Buckley instances         │
│     POST /webhook/github  ← Merge events (updates trace status)         │
│                                                                         │
│   Process:                                                              │
│     Summarization         → LLM-generated trace summaries               │
│     Embedding             → Vector representations for search           │
│     Extraction            → Denormalized decision records               │
│                                                                         │
│   Query:                                                                │
│     POST /v1/search       → Semantic search across traces               │
│     GET  /v1/trace/:sha   → Full trace for specific commit              │
│     POST /v1/prefetch     → Batch relevant traces for session start     │
│     POST /v1/escalation   → RLM escalation recommendations              │
│     GET  /v1/subscribe    → WebSocket for ambient precedents            │
│                                                                         │
└────────┬─────────────────────────────────────┬──────────────────────────┘
         │                                     │
         ▼                                     ▼
┌─────────────────────────┐         ┌─────────────────────────────────────┐
│      PostgreSQL         │         │              Qdrant                 │
│                         │         │                                     │
│  - Source of truth      │         │  - Vector similarity search         │
│  - Relational queries   │         │  - Payload filtering                │
│  - JSONB trace storage  │         │  - Fast approximate nearest neighbor│
│  - Transaction support  │         │  - Scales horizontally              │
│  - Decision analytics   │         │                                     │
└─────────────────────────┘         └─────────────────────────────────────┘
```

### Data Flow

**Ingest flow (commit time):**

```
1. Developer commits via Buckley
2. Buckley assembles CommitTrace from session state:
   - Decisions made (with alternatives, reasoning)
   - Tool calls executed
   - Errors encountered and recoveries
   - Model interactions (tokens, reasoning, escalations)
3. Buckley publishes trace to NATS (fire-and-forget)
4. Context Service consumes from NATS:
   a. Generate summary via LLM if not present
   b. Generate embedding vector from summary
   c. Write to PostgreSQL (source of truth)
   d. Write to Qdrant (vector index)
5. Trace stored with status: "pending"
```

**Webhook flow (merge time):**

```
1. PR merges to main branch
2. GitHub sends webhook to Context Service
3. Service extracts commit SHAs from merge
4. Updates trace status: "pending" → "landed"
5. Records merge metadata (PR number, timestamp)
```

**Query flow (decision time):**

```
1. Buckley needs to make a decision (or starts a session)
2. Buckley queries Context Service with current context
3. Service searches Qdrant for similar traces
4. Service enriches results from PostgreSQL
5. Returns ranked precedents with relevance scores
6. Buckley incorporates precedents into decision-making
```

---

## Data Models

### CommitTrace

The primary unit of captured knowledge. One trace per commit.

```go
type CommitTrace struct {
    // Identity
    ID        string    `json:"id"`         // UUID, primary key
    SHA       string    `json:"sha"`        // Git commit SHA
    Repo      string    `json:"repo"`       // org/repo format
    Branch    string    `json:"branch"`     // Source branch
    Author    string    `json:"author"`     // Git author
    Timestamp time.Time `json:"timestamp"`  // Commit timestamp

    // What changed
    Files []FileChange `json:"files"`
    Areas []string     `json:"areas"`       // Affected areas: pkg/auth, cmd/server
    Stats DiffStats    `json:"stats"`       // Lines added/removed

    // Why - the valuable knowledge
    Decisions   []Decision    `json:"decisions"`
    ToolCalls   []ToolCall    `json:"tool_calls"`
    Errors      []ErrorRecord `json:"errors"`
    ModelCalls  []ModelCall   `json:"model_calls"`

    // Execution metadata
    Mode        ExecutionMode `json:"mode"`        // classic, rlm
    Iterations  int           `json:"iterations"`  // Tool loops executed
    Escalations []Escalation  `json:"escalations"` // RLM tier changes
    Duration    time.Duration `json:"duration"`    // Total session time
    TokensUsed  int           `json:"tokens_used"` // Total tokens consumed

    // For semantic search
    Summary   string    `json:"summary"`   // LLM-generated summary
    Embedding []float32 `json:"-"`         // Vector (not exposed to clients)

    // Lifecycle
    Status    TraceStatus `json:"status"`              // pending, landed, reverted
    LandedAt  *time.Time  `json:"landed_at,omitempty"`
    MergedVia *string     `json:"merged_via,omitempty"` // PR number or merge commit
    RevertedBy *string    `json:"reverted_by,omitempty"` // If reverted, which commit

    // Metadata
    CreatedAt time.Time `json:"created_at"`
    UpdatedAt time.Time `json:"updated_at"`
}

type ExecutionMode string

const (
    ModeClassic ExecutionMode = "classic"
    ModeRLM     ExecutionMode = "rlm"
)

type TraceStatus string

const (
    StatusPending  TraceStatus = "pending"  // Committed but not merged
    StatusLanded   TraceStatus = "landed"   // Merged to main
    StatusReverted TraceStatus = "reverted" // Landed then reverted
)

type FileChange struct {
    Path    string `json:"path"`
    OldPath string `json:"old_path,omitempty"` // For renames
    Status  string `json:"status"`              // A, M, D, R
}

type DiffStats struct {
    Files      int `json:"files"`
    Insertions int `json:"insertions"`
    Deletions  int `json:"deletions"`
}
```

### Decision

The atomic unit of reasoning. Multiple decisions per trace.

```go
type Decision struct {
    ID          string    `json:"id"`          // UUID
    TraceID     string    `json:"trace_id"`    // Parent trace
    Timestamp   time.Time `json:"timestamp"`   // When decision was made

    // The decision itself
    Context     string   `json:"context"`     // What was this about
    Options     []Option `json:"options"`     // Alternatives considered
    Selected    int      `json:"selected"`    // Index of chosen option
    Reasoning   string   `json:"reasoning"`   // Why this choice

    // Classification
    Category    DecisionCategory `json:"category"`    // architecture, implementation, tooling
    Risk        RiskLevel        `json:"risk"`        // low, medium, high
    Reversible  bool             `json:"reversible"`  // Can this be easily undone

    // Provenance
    Auto        bool   `json:"auto"`         // Auto-decided vs user-confirmed
    Confidence  float64 `json:"confidence"`  // Model confidence (0-1)
    Area        string `json:"area"`         // Codebase area affected

    // For search
    Embedding   []float32 `json:"-"`         // Optional per-decision embedding
}

type Option struct {
    Description string   `json:"description"`
    Pros        []string `json:"pros,omitempty"`
    Cons        []string `json:"cons,omitempty"`
}

type DecisionCategory string

const (
    CategoryArchitecture   DecisionCategory = "architecture"   // Structural choices
    CategoryImplementation DecisionCategory = "implementation" // How to code something
    CategoryTooling        DecisionCategory = "tooling"        // Which tool to use
    CategoryRecovery       DecisionCategory = "recovery"       // Error handling approach
    CategoryEscalation     DecisionCategory = "escalation"     // When to escalate (RLM)
)

type RiskLevel string

const (
    RiskLow    RiskLevel = "low"
    RiskMedium RiskLevel = "medium"
    RiskHigh   RiskLevel = "high"
)
```

### Supporting Types

```go
type ToolCall struct {
    ID        string         `json:"id"`
    Tool      string         `json:"tool"`
    Args      map[string]any `json:"args"`
    Result    string         `json:"result"`     // Truncated if large
    Success   bool           `json:"success"`
    Duration  time.Duration  `json:"duration"`
    Timestamp time.Time      `json:"timestamp"`
}

type ErrorRecord struct {
    ID         string    `json:"id"`
    Message    string    `json:"message"`
    Tool       string    `json:"tool,omitempty"`      // Which tool failed
    Recovery   string    `json:"recovery,omitempty"`  // What fixed it
    Iterations int       `json:"iterations"`          // How many attempts
    Timestamp  time.Time `json:"timestamp"`
}

type ModelCall struct {
    ID        string        `json:"id"`
    Model     string        `json:"model"`
    Provider  string        `json:"provider"`
    Tokens    TokenUsage    `json:"tokens"`
    Duration  time.Duration `json:"duration"`
    Reasoning string        `json:"reasoning,omitempty"` // Extended thinking
    Timestamp time.Time     `json:"timestamp"`
}

type TokenUsage struct {
    Input   int `json:"input"`
    Output  int `json:"output"`
    Cached  int `json:"cached,omitempty"`
}

type Escalation struct {
    FromTier  int       `json:"from_tier"`
    ToTier    int       `json:"to_tier"`
    Reason    string    `json:"reason"`
    Iteration int       `json:"iteration"` // Which iteration triggered it
    Timestamp time.Time `json:"timestamp"`
}
```

---

## API Specification

### Ingest Endpoints

**GitHub Webhook**

```
POST /webhook/github
Content-Type: application/json
X-Hub-Signature-256: sha256=...
X-GitHub-Event: pull_request | push

Request: GitHub webhook payload

Response: 200 OK
{
    "processed": true,
    "traces_updated": 3
}
```

Handles:
- `pull_request` with action `closed` and `merged: true`
- `push` to default branch (for direct pushes)

### Query Endpoints

**Semantic Search**

```
POST /v1/search
Content-Type: application/json

Request:
{
    "query": "how to handle rate limiting in auth middleware",
    "filters": {
        "repo": "org/repo",           // optional
        "areas": ["pkg/auth"],        // optional
        "status": "landed",           // optional: pending, landed, reverted
        "mode": "classic",            // optional: classic, rlm
        "since": "2025-01-01T00:00:00Z", // optional
        "author": "developer@example.com" // optional
    },
    "limit": 10,                      // default 10, max 100
    "include_decisions": true,        // expand matched decisions
    "min_score": 0.5                  // minimum relevance threshold
}

Response: 200 OK
{
    "results": [
        {
            "trace": { /* CommitTrace */ },
            "score": 0.87,
            "matched_decisions": [
                {
                    "decision": { /* Decision */ },
                    "relevance": 0.92
                }
            ]
        }
    ],
    "total": 42,
    "query_time_ms": 23
}
```

**Get Trace by SHA**

```
GET /v1/trace/:repo/:sha

Response: 200 OK
{
    "trace": { /* CommitTrace */ }
}

Response: 404 Not Found
{
    "error": "trace not found"
}
```

**Prefetch for Session**

Retrieves relevant traces for a starting session context.

```
POST /v1/prefetch
Content-Type: application/json

Request:
{
    "repo": "org/repo",
    "branch": "feature/auth-refactor",
    "areas": ["pkg/auth", "pkg/middleware"],
    "recent_files": ["pkg/auth/handler.go", "pkg/auth/middleware.go"],
    "task_description": "Add rate limiting to auth endpoints", // optional
    "limit": 20
}

Response: 200 OK
{
    "precedents": [
        {
            "trace": { /* CommitTrace */ },
            "relevance": 0.85,
            "match_reason": "Similar work in pkg/auth with rate limiting"
        }
    ],
    "summary": "Found 20 relevant traces. Common patterns: middleware-based rate limiting, Redis for state, exponential backoff on 429s.",
    "suggestions": [
        "Consider pkg/ratelimit which was added in commit abc123",
        "Previous auth work used token bucket algorithm"
    ]
}
```

**Escalation Hint (RLM)**

Provides escalation recommendations based on historical patterns.

```
POST /v1/escalation
Content-Type: application/json

Request:
{
    "repo": "org/repo",
    "area": "pkg/auth",
    "current_tier": 1,
    "iterations": 3,
    "errors": ["context deadline exceeded", "test failure"],
    "task_type": "bug_fix"  // optional: bug_fix, feature, refactor
}

Response: 200 OK
{
    "recommendation": {
        "action": "escalate",          // escalate, continue, ask_user
        "target_tier": 2,
        "confidence": 0.78
    },
    "reasoning": "3 similar cases in pkg/auth escalated from tier 1→2 after avg 2.3 iterations when hitting test failures. Tier 2 resolved 87% of these.",
    "similar_cases": [
        {
            "trace_id": "...",
            "sha": "abc123",
            "outcome": "resolved_after_escalation",
            "iterations_before": 3,
            "iterations_after": 1
        }
    ],
    "alternative": {
        "action": "continue",
        "reasoning": "23% of similar cases resolved without escalation by iteration 4-5"
    }
}
```

**WebSocket Subscription (Ambient)**

```
GET /v1/subscribe?session_id=xxx
Upgrade: websocket

Client sends:
{
    "type": "context_update",
    "repo": "org/repo",
    "areas": ["pkg/auth"],
    "current_task": "implementing rate limiter",
    "recent_decisions": ["using token bucket", "storing in Redis"]
}

Server pushes:
{
    "type": "precedent",
    "trace": { /* CommitTrace */ },
    "relevance": 0.82,
    "trigger": "Your Redis decision matches a pattern from 2 months ago",
    "suggestion": "That implementation used connection pooling to handle load"
}
```

Push conditions:
- Relevance score > 0.75
- Not already surfaced in this session
- Debounced (max 1 push per 30 seconds)

### Admin Endpoints

```
GET /v1/stats
Response:
{
    "traces": {
        "total": 12453,
        "pending": 234,
        "landed": 12000,
        "reverted": 219
    },
    "decisions": {
        "total": 45621,
        "by_category": { ... }
    },
    "repos": 12,
    "storage": {
        "postgres_size_mb": 1024,
        "qdrant_vectors": 58074
    }
}

POST /v1/reindex
Request:
{
    "repo": "org/repo",  // optional, reindex all if omitted
    "regenerate_embeddings": true
}
Response:
{
    "job_id": "...",
    "traces_queued": 500
}
```

---

## Storage Schema

### PostgreSQL

```sql
-- Extensions
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pg_trgm";  -- For text search

-- Core trace storage
CREATE TABLE commit_traces (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    sha         VARCHAR(40) NOT NULL,
    repo        VARCHAR(255) NOT NULL,
    branch      VARCHAR(255) NOT NULL,
    author      VARCHAR(255),
    timestamp   TIMESTAMPTZ NOT NULL,

    -- Denormalized for filtering
    areas       TEXT[] NOT NULL DEFAULT '{}',
    files       JSONB NOT NULL DEFAULT '[]',
    stats       JSONB NOT NULL DEFAULT '{}',

    -- Execution metadata
    mode        VARCHAR(20) NOT NULL DEFAULT 'classic',
    iterations  INT NOT NULL DEFAULT 0,
    duration_ms BIGINT,
    tokens_used INT,

    -- Full trace data
    trace_data  JSONB NOT NULL,

    -- Summary and search
    summary     TEXT,

    -- Lifecycle
    status      VARCHAR(20) NOT NULL DEFAULT 'pending',
    landed_at   TIMESTAMPTZ,
    merged_via  VARCHAR(100),
    reverted_by VARCHAR(40),

    -- Metadata
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- Constraints
    CONSTRAINT uq_trace_sha_repo UNIQUE(sha, repo)
);

-- Indexes for common queries
CREATE INDEX idx_traces_repo ON commit_traces(repo);
CREATE INDEX idx_traces_status ON commit_traces(status);
CREATE INDEX idx_traces_mode ON commit_traces(mode);
CREATE INDEX idx_traces_timestamp ON commit_traces(timestamp DESC);
CREATE INDEX idx_traces_author ON commit_traces(author);
CREATE INDEX idx_traces_areas ON commit_traces USING GIN(areas);
CREATE INDEX idx_traces_landed ON commit_traces(landed_at DESC) WHERE status = 'landed';

-- Trigram index for text search on summary
CREATE INDEX idx_traces_summary_trgm ON commit_traces USING GIN(summary gin_trgm_ops);

-- Extracted decisions for analytics
CREATE TABLE decisions (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    trace_id    UUID NOT NULL REFERENCES commit_traces(id) ON DELETE CASCADE,
    timestamp   TIMESTAMPTZ NOT NULL,

    -- Decision content
    context     TEXT NOT NULL,
    options     JSONB NOT NULL DEFAULT '[]',
    selected    INT NOT NULL,
    reasoning   TEXT,

    -- Classification
    category    VARCHAR(50),
    risk        VARCHAR(20),
    reversible  BOOLEAN DEFAULT true,

    -- Provenance
    auto        BOOLEAN DEFAULT false,
    confidence  REAL,
    area        VARCHAR(255),

    -- Metadata
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_decisions_trace ON decisions(trace_id);
CREATE INDEX idx_decisions_category ON decisions(category);
CREATE INDEX idx_decisions_area ON decisions(area);
CREATE INDEX idx_decisions_risk ON decisions(risk);

-- Escalation records for RLM analysis
CREATE TABLE escalations (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    trace_id    UUID NOT NULL REFERENCES commit_traces(id) ON DELETE CASCADE,
    from_tier   INT NOT NULL,
    to_tier     INT NOT NULL,
    reason      TEXT,
    iteration   INT NOT NULL,
    timestamp   TIMESTAMPTZ NOT NULL,

    -- Outcome tracking
    resolved_at_tier INT,
    iterations_after INT,

    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_escalations_trace ON escalations(trace_id);
CREATE INDEX idx_escalations_tiers ON escalations(from_tier, to_tier);

-- Error patterns for learning
CREATE TABLE error_patterns (
    id            UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    trace_id      UUID NOT NULL REFERENCES commit_traces(id) ON DELETE CASCADE,
    message       TEXT NOT NULL,
    message_hash  VARCHAR(64) NOT NULL,  -- For deduplication
    tool          VARCHAR(100),
    recovery      TEXT,
    iterations    INT NOT NULL DEFAULT 1,
    timestamp     TIMESTAMPTZ NOT NULL,

    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_errors_trace ON error_patterns(trace_id);
CREATE INDEX idx_errors_hash ON error_patterns(message_hash);
CREATE INDEX idx_errors_tool ON error_patterns(tool);

-- Update timestamp trigger
CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER traces_updated_at
    BEFORE UPDATE ON commit_traces
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at();
```

### Qdrant Collections

**commit_traces collection:**

```json
{
    "collection_name": "commit_traces",
    "vectors": {
        "size": 1536,
        "distance": "Cosine"
    },
    "optimizers_config": {
        "indexing_threshold": 10000
    },
    "on_disk_payload": true
}
```

Payload schema:
```json
{
    "id": "uuid",
    "sha": "string (indexed)",
    "repo": "string (indexed)",
    "areas": "string[] (indexed)",
    "status": "string (indexed)",
    "mode": "string (indexed)",
    "timestamp": "datetime (indexed)",
    "author": "string (indexed)",
    "summary": "string"
}
```

**decisions collection (optional, for fine-grained search):**

```json
{
    "collection_name": "decisions",
    "vectors": {
        "size": 1536,
        "distance": "Cosine"
    }
}
```

Payload schema:
```json
{
    "id": "uuid",
    "trace_id": "uuid (indexed)",
    "repo": "string (indexed)",
    "area": "string (indexed)",
    "category": "string (indexed)",
    "risk": "string (indexed)",
    "context": "string"
}
```

---

## Service Implementation

### Package Structure

```
pkg/contextservice/
├── cmd/
│   └── contextd/
│       └── main.go                 # Entry point, signal handling
│
├── internal/
│   ├── server/
│   │   ├── server.go               # HTTP server setup, middleware
│   │   ├── routes.go               # Route registration
│   │   ├── webhook.go              # GitHub webhook handler
│   │   ├── search.go               # Search endpoint handler
│   │   ├── prefetch.go             # Prefetch endpoint handler
│   │   ├── escalation.go           # Escalation hint handler
│   │   └── websocket.go            # WebSocket subscription handler
│   │
│   ├── ingest/
│   │   ├── consumer.go             # NATS JetStream consumer
│   │   ├── processor.go            # Trace processing pipeline
│   │   ├── validator.go            # Trace validation
│   │   └── dedup.go                # Deduplication logic
│   │
│   ├── embedding/
│   │   ├── embedder.go             # Embedder interface
│   │   ├── openai.go               # OpenAI implementation
│   │   ├── local.go                # Local model implementation
│   │   └── cache.go                # Embedding cache
│   │
│   ├── summarize/
│   │   ├── summarizer.go           # Summarizer interface
│   │   └── llm.go                  # LLM-based summarization
│   │
│   ├── store/
│   │   ├── store.go                # Store interface
│   │   ├── postgres.go             # PostgreSQL implementation
│   │   ├── qdrant.go               # Qdrant implementation
│   │   └── composite.go            # Unified store (writes to both)
│   │
│   ├── search/
│   │   ├── engine.go               # Search orchestration
│   │   ├── ranking.go              # Result ranking/reranking
│   │   └── filters.go              # Filter parsing and application
│   │
│   ├── escalation/
│   │   ├── analyzer.go             # Historical pattern analysis
│   │   ├── recommender.go          # Escalation recommendations
│   │   └── outcomes.go             # Outcome tracking
│   │
│   └── realtime/
│       ├── hub.go                  # WebSocket connection hub
│       ├── session.go              # Session state tracking
│       └── matcher.go              # Relevance matching for pushes
│
├── api/
│   └── v1/
│       ├── types.go                # Request/response types
│       ├── errors.go               # Error types
│       └── validation.go           # Request validation
│
├── config/
│   ├── config.go                   # Configuration struct
│   └── defaults.go                 # Default values
│
└── migrations/
    ├── 001_initial.up.sql
    ├── 001_initial.down.sql
    └── migrate.go                  # Migration runner
```

### Core Interfaces

```go
// Store abstracts persistence
type Store interface {
    // Write operations
    SaveTrace(ctx context.Context, trace *CommitTrace) error
    UpdateStatus(ctx context.Context, repo, sha string, status TraceStatus, meta StatusMeta) error

    // Read operations
    GetTrace(ctx context.Context, repo, sha string) (*CommitTrace, error)
    ListTraces(ctx context.Context, filter TraceFilter) (*TraceList, error)

    // Search operations
    Search(ctx context.Context, query SearchQuery) (*SearchResults, error)

    // Analytics
    GetEscalationPatterns(ctx context.Context, filter EscalationFilter) ([]EscalationPattern, error)
    GetErrorPatterns(ctx context.Context, filter ErrorFilter) ([]ErrorPattern, error)
}

// Embedder generates vector embeddings
type Embedder interface {
    Embed(ctx context.Context, text string) ([]float32, error)
    EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
    Dimensions() int
}

// Summarizer generates trace summaries
type Summarizer interface {
    Summarize(ctx context.Context, trace *CommitTrace) (string, error)
}

// Consumer handles incoming traces from NATS
type Consumer interface {
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
    Health() error
}

// RealtimeHub manages WebSocket connections
type RealtimeHub interface {
    Register(conn *Connection, sessionID string) error
    Unregister(sessionID string)
    UpdateContext(sessionID string, ctx SessionContext) error
    Broadcast(sessionID string, msg PrecedentPush) error
}
```

### Processing Pipeline

```go
type Processor struct {
    store      Store
    embedder   Embedder
    summarizer Summarizer
    validator  *Validator
    metrics    *Metrics
}

func (p *Processor) Process(ctx context.Context, trace *CommitTrace) error {
    span, ctx := tracer.StartSpan(ctx, "processor.process")
    defer span.End()

    // 1. Validate trace
    if err := p.validator.Validate(trace); err != nil {
        p.metrics.InvalidTraces.Inc()
        return fmt.Errorf("validation: %w", err)
    }

    // 2. Generate summary if not present
    if trace.Summary == "" {
        summary, err := p.summarizer.Summarize(ctx, trace)
        if err != nil {
            // Non-fatal: proceed without summary
            span.RecordError(err)
            trace.Summary = p.fallbackSummary(trace)
        } else {
            trace.Summary = summary
        }
    }

    // 3. Generate embedding
    embedding, err := p.embedder.Embed(ctx, trace.Summary)
    if err != nil {
        return fmt.Errorf("embedding: %w", err)
    }
    trace.Embedding = embedding

    // 4. Extract decisions for denormalized storage
    decisions := extractDecisions(trace)

    // 5. Write to store (handles both Postgres and Qdrant)
    if err := p.store.SaveTrace(ctx, trace); err != nil {
        return fmt.Errorf("save trace: %w", err)
    }

    p.metrics.TracesProcessed.Inc()
    p.metrics.DecisionsExtracted.Add(float64(len(decisions)))

    return nil
}

func (p *Processor) fallbackSummary(trace *CommitTrace) string {
    // Generate a basic summary from structured data
    var parts []string
    if len(trace.Areas) > 0 {
        parts = append(parts, fmt.Sprintf("Changes to %s", strings.Join(trace.Areas, ", ")))
    }
    if len(trace.Decisions) > 0 {
        parts = append(parts, fmt.Sprintf("%d decisions made", len(trace.Decisions)))
    }
    if trace.Stats.Insertions > 0 || trace.Stats.Deletions > 0 {
        parts = append(parts, fmt.Sprintf("+%d/-%d lines", trace.Stats.Insertions, trace.Stats.Deletions))
    }
    return strings.Join(parts, ". ")
}
```

---

## Buckley Integration

### Client Package

New package in Buckley: `pkg/context/`

```go
// pkg/context/client.go

package context

import (
    "context"
    "encoding/json"
    "net/http"
    "time"

    "github.com/nats-io/nats.go"
)

// Client provides access to the Context Service
type Client struct {
    nats       *nats.Conn
    js         nats.JetStreamContext
    http       *http.Client
    endpoint   string
    enabled    bool
}

// Config configures the context client
type Config struct {
    Enabled         bool          `yaml:"enabled"`
    ServiceEndpoint string        `yaml:"service_endpoint"`
    NatsURL         string        `yaml:"nats_url"`
    Timeout         time.Duration `yaml:"timeout"`
}

func DefaultConfig() Config {
    return Config{
        Enabled:         false,
        ServiceEndpoint: "http://localhost:8080",
        NatsURL:         "nats://localhost:4222",
        Timeout:         10 * time.Second,
    }
}

// New creates a new context client
func New(cfg Config) (*Client, error) {
    if !cfg.Enabled {
        return &Client{enabled: false}, nil
    }

    nc, err := nats.Connect(cfg.NatsURL)
    if err != nil {
        return nil, fmt.Errorf("nats connect: %w", err)
    }

    js, err := nc.JetStream()
    if err != nil {
        nc.Close()
        return nil, fmt.Errorf("jetstream: %w", err)
    }

    return &Client{
        nats:     nc,
        js:       js,
        http:     &http.Client{Timeout: cfg.Timeout},
        endpoint: cfg.ServiceEndpoint,
        enabled:  true,
    }, nil
}

// PublishTrace publishes a commit trace to NATS
func (c *Client) PublishTrace(ctx context.Context, trace *CommitTrace) error {
    if !c.enabled {
        return nil
    }

    data, err := json.Marshal(trace)
    if err != nil {
        return fmt.Errorf("marshal: %w", err)
    }

    _, err = c.js.Publish(SubjectCommitTrace, data)
    if err != nil {
        return fmt.Errorf("publish: %w", err)
    }

    return nil
}

// Search finds traces similar to the query
func (c *Client) Search(ctx context.Context, query string, opts SearchOptions) (*SearchResults, error) {
    if !c.enabled {
        return &SearchResults{}, nil
    }
    // POST to /v1/search
    // ...
}

// Prefetch retrieves relevant traces for a session
func (c *Client) Prefetch(ctx context.Context, req PrefetchRequest) (*PrefetchResults, error) {
    if !c.enabled {
        return &PrefetchResults{}, nil
    }
    // POST to /v1/prefetch
    // ...
}

// EscalationHint gets escalation recommendation
func (c *Client) EscalationHint(ctx context.Context, req EscalationRequest) (*EscalationHint, error) {
    if !c.enabled {
        return nil, nil
    }
    // POST to /v1/escalation
    // ...
}

// Subscribe opens ambient precedent subscription
func (c *Client) Subscribe(ctx context.Context, sessionID string) (<-chan Precedent, error) {
    if !c.enabled {
        ch := make(chan Precedent)
        close(ch)
        return ch, nil
    }
    // WebSocket to /v1/subscribe
    // ...
}

// Close closes the client connections
func (c *Client) Close() error {
    if c.nats != nil {
        c.nats.Close()
    }
    return nil
}
```

### Classic Mode Integration

Classic mode benefits from context graph through enriched decision-making.

```go
// pkg/agent/executor.go

type Executor struct {
    // ... existing fields ...
    contextClient *context.Client
    precedents    []context.Precedent  // Loaded at session start
}

// Start initializes the executor with context
func (e *Executor) Start(ctx context.Context) error {
    // Prefetch relevant precedents
    if e.contextClient != nil {
        result, err := e.contextClient.Prefetch(ctx, context.PrefetchRequest{
            Repo:   e.repo,
            Branch: e.branch,
            Areas:  e.detectAreas(),
        })
        if err != nil {
            // Log but don't fail - context is enhancement, not requirement
            e.logger.Warn("failed to prefetch context", "error", err)
        } else {
            e.precedents = result.Precedents
            e.logger.Info("loaded context precedents", "count", len(e.precedents))
        }
    }

    return nil
}

// EnrichSystemPrompt adds precedent context to system messages
func (e *Executor) EnrichSystemPrompt(base string) string {
    if len(e.precedents) == 0 {
        return base
    }

    var relevant []string
    for _, p := range e.precedents[:min(3, len(e.precedents))] {
        relevant = append(relevant, fmt.Sprintf("- %s (relevance: %.0f%%)",
            p.Summary, p.Relevance*100))
    }

    return fmt.Sprintf(`%s

## Relevant Precedents

Previous work in this area:
%s

Consider these patterns when making decisions.`, base, strings.Join(relevant, "\n"))
}

// BeforeDecision queries for specific precedent
func (e *Executor) BeforeDecision(ctx context.Context, decision DecisionContext) []context.Precedent {
    if e.contextClient == nil {
        return nil
    }

    results, err := e.contextClient.Search(ctx, decision.Description, context.SearchOptions{
        Repo:   e.repo,
        Areas:  []string{decision.Area},
        Status: "landed",
        Limit:  5,
    })
    if err != nil {
        e.logger.Warn("decision precedent search failed", "error", err)
        return nil
    }

    return results.Precedents
}
```

### RLM Mode Integration

RLM gets deeper integration with escalation hints and scratchpad population.

```go
// pkg/rlm/coordinator.go

type Coordinator struct {
    // ... existing fields ...
    contextClient *context.Client
    precedentChan <-chan context.Precedent  // Ambient subscription
}

// Start initializes RLM with context awareness
func (c *Coordinator) Start(ctx context.Context) error {
    if c.contextClient != nil {
        // 1. Prefetch into scratchpad
        result, err := c.contextClient.Prefetch(ctx, context.PrefetchRequest{
            Repo:            c.repo,
            Branch:          c.branch,
            Areas:           c.areas,
            TaskDescription: c.taskDescription,
        })
        if err == nil {
            for i, p := range result.Precedents {
                c.scratchpad.Set(fmt.Sprintf("precedent:%d", i), rlm.Entry{
                    Type:      rlm.EntryTypeDecision,
                    Summary:   p.Summary,
                    Raw:       mustMarshal(p),
                    CreatedAt: time.Now(),
                })
            }
            c.logger.Info("loaded precedents to scratchpad", "count", len(result.Precedents))
        }

        // 2. Start ambient subscription
        precedents, err := c.contextClient.Subscribe(ctx, c.sessionID)
        if err == nil {
            c.precedentChan = precedents
            go c.handleAmbientPrecedents(ctx)
        }
    }

    return nil
}

// handleAmbientPrecedents processes pushed precedents
func (c *Coordinator) handleAmbientPrecedents(ctx context.Context) {
    for {
        select {
        case <-ctx.Done():
            return
        case p, ok := <-c.precedentChan:
            if !ok {
                return
            }
            // Add high-relevance precedents to scratchpad
            if p.Relevance > 0.8 {
                c.scratchpad.Set(fmt.Sprintf("ambient:%s", p.SHA[:8]), rlm.Entry{
                    Type:      rlm.EntryTypeDecision,
                    Summary:   fmt.Sprintf("[Ambient] %s", p.Summary),
                    Raw:       mustMarshal(p),
                    CreatedAt: time.Now(),
                })
                c.logger.Info("ambient precedent added", "sha", p.SHA[:8], "relevance", p.Relevance)
            }
        }
    }
}

// ConsiderEscalation uses historical patterns
func (c *Coordinator) ConsiderEscalation(ctx context.Context) (bool, int) {
    if c.contextClient != nil {
        hint, err := c.contextClient.EscalationHint(ctx, context.EscalationRequest{
            Repo:        c.repo,
            Area:        c.currentArea,
            CurrentTier: c.tier,
            Iterations:  c.iterations,
            Errors:      c.recentErrors(),
        })
        if err == nil && hint != nil && hint.Confidence > 0.7 {
            c.logger.Info("context-informed escalation",
                "from", c.tier,
                "to", hint.TargetTier,
                "confidence", hint.Confidence,
                "reasoning", hint.Reasoning,
            )
            return true, hint.TargetTier
        }
    }

    // Fall back to existing heuristics
    return c.heuristicEscalation()
}
```

### Commit Integration

Capture traces when commits are made.

```go
// pkg/oneshot/commit/run.go

func (r *Runner) Run(ctx context.Context, opts ContextOptions) (*RunResult, error) {
    // ... existing commit logic ...

    // After successful commit, publish trace
    if r.contextClient != nil && result.Commit != nil && r.sessionState != nil {
        trace := r.buildCommitTrace(result, r.sessionState)

        // Fire and forget - don't block commit on context service
        go func() {
            if err := r.contextClient.PublishTrace(context.Background(), trace); err != nil {
                r.logger.Warn("failed to publish commit trace", "error", err)
            }
        }()
    }

    return result, nil
}

func (r *Runner) buildCommitTrace(result *RunResult, state *SessionState) *context.CommitTrace {
    return &context.CommitTrace{
        SHA:       result.SHA,
        Repo:      r.repo,
        Branch:    r.branch,
        Author:    r.author,
        Timestamp: time.Now(),

        Files: result.Files,
        Areas: result.Areas,
        Stats: context.DiffStats{
            Files:      result.Stats.Files,
            Insertions: result.Stats.Insertions,
            Deletions:  result.Stats.Deletions,
        },

        Decisions:   state.Decisions,
        ToolCalls:   state.ToolCalls,
        Errors:      state.Errors,
        ModelCalls:  state.ModelCalls,
        Iterations:  state.Iterations,
        Escalations: state.Escalations,

        Mode:       state.Mode,
        Duration:   state.Duration,
        TokensUsed: state.TokensUsed,

        Status: context.StatusPending,
    }
}
```

---

## Deployment

### Helm Chart

```yaml
# deploy/charts/context-service/Chart.yaml
apiVersion: v2
name: context-service
description: Buckley Context Graph Service
version: 0.1.0
appVersion: "0.1.0"

dependencies:
  - name: postgresql
    version: "12.x.x"
    repository: https://charts.bitnami.com/bitnami
    condition: postgresql.enabled
  - name: qdrant
    version: "0.7.x"
    repository: https://qdrant.github.io/qdrant-helm
    condition: qdrant.enabled
  - name: nats
    version: "1.1.x"
    repository: https://nats-io.github.io/k8s/helm/charts
    condition: nats.enabled
```

```yaml
# deploy/charts/context-service/values.yaml

replicaCount: 2

image:
  repository: ghcr.io/buckley/context-service
  tag: ""  # Defaults to chart appVersion
  pullPolicy: IfNotPresent

service:
  type: ClusterIP
  port: 8080

ingress:
  enabled: true
  className: nginx
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt-prod
  hosts:
    - host: context.buckley.example.com
      paths:
        - path: /
          pathType: Prefix
  tls:
    - secretName: context-tls
      hosts:
        - context.buckley.example.com

resources:
  requests:
    cpu: 100m
    memory: 256Mi
  limits:
    cpu: 1000m
    memory: 1Gi

config:
  logLevel: info
  logFormat: json

  embedder:
    provider: openai
    model: text-embedding-ada-002
    batchSize: 100
    # For local embeddings:
    # provider: local
    # model: all-MiniLM-L6-v2
    # endpoint: http://embeddings:8000

  summarizer:
    provider: openai
    model: gpt-4o-mini
    maxTokens: 500

  webhook:
    secret: ""  # Set via secret

  realtime:
    enabled: true
    relevanceThreshold: 0.75
    debounceMs: 30000

# PostgreSQL subchart config
postgresql:
  enabled: true
  auth:
    database: context
    username: context
    password: ""  # Set via secret
  primary:
    persistence:
      size: 20Gi

# Qdrant subchart config
qdrant:
  enabled: true
  replicaCount: 1
  persistence:
    size: 10Gi
  config:
    storage:
      on_disk_payload: true

# NATS subchart config
nats:
  enabled: true
  nats:
    jetstream:
      enabled: true
      memStorage:
        enabled: true
        size: 1Gi
      fileStorage:
        enabled: true
        size: 5Gi
        storageClassName: ""

# External services (when subcharts disabled)
external:
  postgresql:
    host: ""
    port: 5432
    database: context
    sslmode: require
  qdrant:
    host: ""
    port: 6333
    apiKey: ""
  nats:
    url: ""
```

### Docker Compose (Development)

```yaml
# docker-compose.yml

version: "3.8"

services:
  context-service:
    build: .
    ports:
      - "8080:8080"
    environment:
      - CONFIG_PATH=/etc/context/config.yaml
      - POSTGRES_URL=postgres://context:context@postgres:5432/context?sslmode=disable
      - QDRANT_URL=http://qdrant:6333
      - NATS_URL=nats://nats:4222
      - OPENAI_API_KEY=${OPENAI_API_KEY}
    depends_on:
      - postgres
      - qdrant
      - nats
    volumes:
      - ./config.yaml:/etc/context/config.yaml:ro

  postgres:
    image: postgres:15
    environment:
      POSTGRES_USER: context
      POSTGRES_PASSWORD: context
      POSTGRES_DB: context
    volumes:
      - postgres_data:/var/lib/postgresql/data
    ports:
      - "5432:5432"

  qdrant:
    image: qdrant/qdrant:v1.7.0
    volumes:
      - qdrant_data:/qdrant/storage
    ports:
      - "6333:6333"

  nats:
    image: nats:2.10-alpine
    command: ["--jetstream", "--store_dir=/data"]
    volumes:
      - nats_data:/data
    ports:
      - "4222:4222"
      - "8222:8222"  # Monitoring

volumes:
  postgres_data:
  qdrant_data:
  nats_data:
```

---

## Observability

### Metrics

```go
// Prometheus metrics
var (
    tracesIngested = promauto.NewCounterVec(prometheus.CounterOpts{
        Name: "context_traces_ingested_total",
        Help: "Total traces ingested",
    }, []string{"repo", "mode", "status"})

    decisionsExtracted = promauto.NewCounter(prometheus.CounterOpts{
        Name: "context_decisions_extracted_total",
        Help: "Total decisions extracted from traces",
    })

    searchLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
        Name:    "context_search_duration_seconds",
        Help:    "Search request latency",
        Buckets: []float64{.01, .025, .05, .1, .25, .5, 1},
    }, []string{"endpoint"})

    embeddingLatency = promauto.NewHistogram(prometheus.HistogramOpts{
        Name:    "context_embedding_duration_seconds",
        Help:    "Embedding generation latency",
        Buckets: []float64{.1, .25, .5, 1, 2.5, 5},
    })

    activeWebsockets = promauto.NewGauge(prometheus.GaugeOpts{
        Name: "context_websocket_connections_active",
        Help: "Active WebSocket connections",
    })
)
```

### Tracing

OpenTelemetry integration for distributed tracing across Buckley → NATS → Context Service → Storage.

### Health Checks

```go
// GET /health/live - Kubernetes liveness
{
    "status": "ok"
}

// GET /health/ready - Kubernetes readiness
{
    "status": "ready",
    "checks": {
        "postgres": "ok",
        "qdrant": "ok",
        "nats": "ok"
    }
}
```

---

## Security Considerations

### Authentication

- GitHub webhook: Validate `X-Hub-Signature-256` using shared secret
- API endpoints: API key in `Authorization: Bearer <key>` header
- NATS: TLS + credentials file for production

### Data Privacy

- Traces may contain code snippets and reasoning - treat as sensitive
- Support per-repo access controls (future)
- Embedding model sees summaries, not full code
- Consider on-prem embedding model for sensitive codebases

### Rate Limiting

- Webhook endpoint: 100 req/min per repo
- Search endpoints: 60 req/min per API key
- WebSocket: Max 10 concurrent connections per session

---

## Future Considerations

### Phase 2

- **Revert detection**: Track when landed commits are reverted, update trace status, learn from failures
- **Team namespacing**: Multi-tenant support with team-level access controls
- **Hybrid search**: Combine vector similarity with keyword/BM25 for better recall
- **Decision linking**: Connect decisions across commits (decision chains)

### Phase 3

- **What-if simulation**: "What would have happened with different decisions?"
- **Pattern extraction**: Automatically surface common decision patterns
- **Recommendation engine**: Proactive suggestions based on context
- **IDE integration**: Surface precedents directly in editor

---

## Success Metrics

| Metric | Target | Measurement |
|--------|--------|-------------|
| Trace capture rate | >95% of Buckley commits | Commits with published traces / total commits |
| Search relevance | >70% useful results | User feedback sampling |
| Escalation accuracy | >80% correct tier | Escalation suggestions vs actual outcomes |
| Query latency | P95 < 200ms | Search endpoint metrics |
| Adoption | >50% sessions use prefetch | Prefetch calls / session starts |

---

## References

- [Context Graphs for AI Agents](https://foundationcapital.com/context-graphs-for-ai-agents/) - Foundation Capital
- [Building Context Graphs](https://animesh.blog/building-context-graphs/) - Animesh Koratana
- [Qdrant Documentation](https://qdrant.tech/documentation/)
- [NATS JetStream](https://docs.nats.io/nats-concepts/jetstream)
- ADR-0009: RLM Runtime Architecture
