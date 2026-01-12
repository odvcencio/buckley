# Context Graph Service Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Build a service that captures decision traces from Buckley commits, indexes them for semantic search, and provides precedent-aware context to inform future work.

**Architecture:** Separate service (`contextd`) communicates with Buckley via NATS for trace ingestion and HTTP/WebSocket for queries. PostgreSQL stores structured data, Qdrant handles vector search. Client library in Buckley publishes traces on commit and queries for precedents.

**Tech Stack:** Go, PostgreSQL, Qdrant, NATS JetStream, OpenAI embeddings (configurable)

---

## Phase 1: Core Types and Client Library

### Task 1.1: Define Core Types

**Files:**
- Create: `pkg/contextgraph/types.go`
- Test: `pkg/contextgraph/types_test.go`

**Step 1: Write the failing test**

```go
// pkg/contextgraph/types_test.go
package contextgraph

import (
    "encoding/json"
    "testing"
    "time"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestCommitTrace_JSON(t *testing.T) {
    trace := CommitTrace{
        ID:        "test-id",
        SHA:       "abc123",
        Repo:      "org/repo",
        Branch:    "main",
        Timestamp: time.Date(2026, 1, 7, 12, 0, 0, 0, time.UTC),
        Areas:     []string{"pkg/auth"},
        Decisions: []Decision{
            {
                ID:        "dec-1",
                Context:   "How to handle auth",
                Options:   []Option{{Description: "JWT"}, {Description: "Session"}},
                Selected:  0,
                Reasoning: "JWT is stateless",
            },
        },
        Status: StatusPending,
    }

    data, err := json.Marshal(trace)
    require.NoError(t, err)

    var decoded CommitTrace
    err = json.Unmarshal(data, &decoded)
    require.NoError(t, err)

    assert.Equal(t, trace.SHA, decoded.SHA)
    assert.Equal(t, trace.Repo, decoded.Repo)
    assert.Len(t, decoded.Decisions, 1)
    assert.Equal(t, "JWT is stateless", decoded.Decisions[0].Reasoning)
}

func TestTraceStatus_Valid(t *testing.T) {
    tests := []struct {
        status TraceStatus
        valid  bool
    }{
        {StatusPending, true},
        {StatusLanded, true},
        {StatusReverted, true},
        {TraceStatus("invalid"), false},
    }

    for _, tt := range tests {
        t.Run(string(tt.status), func(t *testing.T) {
            assert.Equal(t, tt.valid, tt.status.Valid())
        })
    }
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./pkg/contextgraph/... -v -run TestCommitTrace`
Expected: FAIL - package does not exist

**Step 3: Write minimal implementation**

```go
// pkg/contextgraph/types.go
package contextgraph

import "time"

// TraceStatus represents the lifecycle state of a commit trace.
type TraceStatus string

const (
    StatusPending  TraceStatus = "pending"
    StatusLanded   TraceStatus = "landed"
    StatusReverted TraceStatus = "reverted"
)

// Valid returns true if the status is a known value.
func (s TraceStatus) Valid() bool {
    switch s {
    case StatusPending, StatusLanded, StatusReverted:
        return true
    default:
        return false
    }
}

// ExecutionMode indicates which Buckley mode produced the trace.
type ExecutionMode string

const (
    ModeClassic ExecutionMode = "classic"
    ModeRLM     ExecutionMode = "rlm"
)

// CommitTrace captures all context from a Buckley session that produced a commit.
type CommitTrace struct {
    // Identity
    ID        string    `json:"id"`
    SHA       string    `json:"sha"`
    Repo      string    `json:"repo"`
    Branch    string    `json:"branch"`
    Author    string    `json:"author,omitempty"`
    Timestamp time.Time `json:"timestamp"`

    // What changed
    Files []FileChange `json:"files,omitempty"`
    Areas []string     `json:"areas,omitempty"`
    Stats DiffStats    `json:"stats,omitempty"`

    // Why - the valuable knowledge
    Decisions   []Decision    `json:"decisions,omitempty"`
    ToolCalls   []ToolCall    `json:"tool_calls,omitempty"`
    Errors      []ErrorRecord `json:"errors,omitempty"`
    ModelCalls  []ModelCall   `json:"model_calls,omitempty"`

    // Execution metadata
    Mode        ExecutionMode `json:"mode,omitempty"`
    Iterations  int           `json:"iterations,omitempty"`
    Escalations []Escalation  `json:"escalations,omitempty"`
    Duration    time.Duration `json:"duration,omitempty"`
    TokensUsed  int           `json:"tokens_used,omitempty"`

    // For search
    Summary   string    `json:"summary,omitempty"`
    Embedding []float32 `json:"-"`

    // Lifecycle
    Status     TraceStatus `json:"status"`
    LandedAt   *time.Time  `json:"landed_at,omitempty"`
    MergedVia  *string     `json:"merged_via,omitempty"`
    RevertedBy *string     `json:"reverted_by,omitempty"`

    // Metadata
    CreatedAt time.Time `json:"created_at,omitempty"`
    UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// FileChange represents a single file modification.
type FileChange struct {
    Path    string `json:"path"`
    OldPath string `json:"old_path,omitempty"`
    Status  string `json:"status"` // A, M, D, R
}

// DiffStats contains aggregate change statistics.
type DiffStats struct {
    Files      int `json:"files"`
    Insertions int `json:"insertions"`
    Deletions  int `json:"deletions"`
}

// Decision captures a single decision point with alternatives and reasoning.
type Decision struct {
    ID        string    `json:"id"`
    TraceID   string    `json:"trace_id,omitempty"`
    Timestamp time.Time `json:"timestamp,omitempty"`

    Context   string   `json:"context"`
    Options   []Option `json:"options,omitempty"`
    Selected  int      `json:"selected"`
    Reasoning string   `json:"reasoning,omitempty"`

    Category   DecisionCategory `json:"category,omitempty"`
    Risk       RiskLevel        `json:"risk,omitempty"`
    Reversible bool             `json:"reversible,omitempty"`

    Auto       bool    `json:"auto,omitempty"`
    Confidence float64 `json:"confidence,omitempty"`
    Area       string  `json:"area,omitempty"`
}

// Option represents one alternative in a decision.
type Option struct {
    Description string   `json:"description"`
    Pros        []string `json:"pros,omitempty"`
    Cons        []string `json:"cons,omitempty"`
}

// DecisionCategory classifies the type of decision.
type DecisionCategory string

const (
    CategoryArchitecture   DecisionCategory = "architecture"
    CategoryImplementation DecisionCategory = "implementation"
    CategoryTooling        DecisionCategory = "tooling"
    CategoryRecovery       DecisionCategory = "recovery"
    CategoryEscalation     DecisionCategory = "escalation"
)

// RiskLevel indicates the risk associated with a decision.
type RiskLevel string

const (
    RiskLow    RiskLevel = "low"
    RiskMedium RiskLevel = "medium"
    RiskHigh   RiskLevel = "high"
)

// ToolCall records a single tool invocation.
type ToolCall struct {
    ID        string         `json:"id"`
    Tool      string         `json:"tool"`
    Args      map[string]any `json:"args,omitempty"`
    Result    string         `json:"result,omitempty"`
    Success   bool           `json:"success"`
    Duration  time.Duration  `json:"duration,omitempty"`
    Timestamp time.Time      `json:"timestamp,omitempty"`
}

// ErrorRecord captures an error and how it was resolved.
type ErrorRecord struct {
    ID         string    `json:"id"`
    Message    string    `json:"message"`
    Tool       string    `json:"tool,omitempty"`
    Recovery   string    `json:"recovery,omitempty"`
    Iterations int       `json:"iterations,omitempty"`
    Timestamp  time.Time `json:"timestamp,omitempty"`
}

// ModelCall records a single LLM invocation.
type ModelCall struct {
    ID        string        `json:"id"`
    Model     string        `json:"model"`
    Provider  string        `json:"provider,omitempty"`
    Tokens    TokenUsage    `json:"tokens"`
    Duration  time.Duration `json:"duration,omitempty"`
    Reasoning string        `json:"reasoning,omitempty"`
    Timestamp time.Time     `json:"timestamp,omitempty"`
}

// TokenUsage tracks token consumption.
type TokenUsage struct {
    Input  int `json:"input"`
    Output int `json:"output"`
    Cached int `json:"cached,omitempty"`
}

// Escalation records a tier change in RLM mode.
type Escalation struct {
    FromTier  int       `json:"from_tier"`
    ToTier    int       `json:"to_tier"`
    Reason    string    `json:"reason,omitempty"`
    Iteration int       `json:"iteration,omitempty"`
    Timestamp time.Time `json:"timestamp,omitempty"`
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./pkg/contextgraph/... -v`
Expected: PASS

**Step 5: Commit**

```bash
git add pkg/contextgraph/
git commit -m "add(contextgraph): core types for commit traces and decisions"
```

---

### Task 1.2: Define API Types

**Files:**
- Create: `pkg/contextgraph/api.go`
- Test: `pkg/contextgraph/api_test.go`

**Step 1: Write the failing test**

```go
// pkg/contextgraph/api_test.go
package contextgraph

import (
    "encoding/json"
    "testing"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestSearchRequest_JSON(t *testing.T) {
    req := SearchRequest{
        Query: "rate limiting auth",
        Filters: SearchFilters{
            Repo:   "org/repo",
            Areas:  []string{"pkg/auth"},
            Status: StatusLanded,
        },
        Limit: 10,
    }

    data, err := json.Marshal(req)
    require.NoError(t, err)

    var decoded SearchRequest
    err = json.Unmarshal(data, &decoded)
    require.NoError(t, err)

    assert.Equal(t, req.Query, decoded.Query)
    assert.Equal(t, req.Filters.Repo, decoded.Filters.Repo)
}

func TestSearchResult_Score(t *testing.T) {
    result := SearchResult{
        Trace: CommitTrace{SHA: "abc123"},
        Score: 0.87,
    }
    assert.Equal(t, 0.87, result.Score)
}

func TestPrefetchRequest_Defaults(t *testing.T) {
    req := PrefetchRequest{
        Repo:   "org/repo",
        Branch: "main",
    }
    req.ApplyDefaults()
    assert.Equal(t, 20, req.Limit)
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./pkg/contextgraph/... -v -run TestSearch`
Expected: FAIL - types not defined

**Step 3: Write minimal implementation**

```go
// pkg/contextgraph/api.go
package contextgraph

import "time"

// NATS subjects
const (
    SubjectCommitTrace = "buckley.contextgraph.trace"
)

// SearchRequest is the request body for POST /v1/search.
type SearchRequest struct {
    Query            string        `json:"query"`
    Filters          SearchFilters `json:"filters,omitempty"`
    Limit            int           `json:"limit,omitempty"`
    IncludeDecisions bool          `json:"include_decisions,omitempty"`
    MinScore         float64       `json:"min_score,omitempty"`
}

// SearchFilters constrains search results.
type SearchFilters struct {
    Repo   string      `json:"repo,omitempty"`
    Areas  []string    `json:"areas,omitempty"`
    Status TraceStatus `json:"status,omitempty"`
    Mode   string      `json:"mode,omitempty"`
    Since  *time.Time  `json:"since,omitempty"`
    Author string      `json:"author,omitempty"`
}

// SearchResponse is the response for POST /v1/search.
type SearchResponse struct {
    Results     []SearchResult `json:"results"`
    Total       int            `json:"total"`
    QueryTimeMs int64          `json:"query_time_ms"`
}

// SearchResult is a single search hit.
type SearchResult struct {
    Trace            CommitTrace       `json:"trace"`
    Score            float64           `json:"score"`
    MatchedDecisions []MatchedDecision `json:"matched_decisions,omitempty"`
}

// MatchedDecision links a decision to its relevance score.
type MatchedDecision struct {
    Decision  Decision `json:"decision"`
    Relevance float64  `json:"relevance"`
}

// PrefetchRequest is the request body for POST /v1/prefetch.
type PrefetchRequest struct {
    Repo            string   `json:"repo"`
    Branch          string   `json:"branch"`
    Areas           []string `json:"areas,omitempty"`
    RecentFiles     []string `json:"recent_files,omitempty"`
    TaskDescription string   `json:"task_description,omitempty"`
    Limit           int      `json:"limit,omitempty"`
}

// ApplyDefaults fills in default values.
func (r *PrefetchRequest) ApplyDefaults() {
    if r.Limit == 0 {
        r.Limit = 20
    }
}

// PrefetchResponse is the response for POST /v1/prefetch.
type PrefetchResponse struct {
    Precedents  []Precedent `json:"precedents"`
    Summary     string      `json:"summary,omitempty"`
    Suggestions []string    `json:"suggestions,omitempty"`
}

// Precedent is a trace with relevance context.
type Precedent struct {
    Trace       CommitTrace `json:"trace"`
    Relevance   float64     `json:"relevance"`
    MatchReason string      `json:"match_reason,omitempty"`
}

// EscalationRequest is the request body for POST /v1/escalation.
type EscalationRequest struct {
    Repo        string   `json:"repo"`
    Area        string   `json:"area,omitempty"`
    CurrentTier int      `json:"current_tier"`
    Iterations  int      `json:"iterations"`
    Errors      []string `json:"errors,omitempty"`
    TaskType    string   `json:"task_type,omitempty"`
}

// EscalationResponse is the response for POST /v1/escalation.
type EscalationResponse struct {
    Recommendation EscalationRecommendation `json:"recommendation"`
    Reasoning      string                   `json:"reasoning"`
    SimilarCases   []EscalationCase         `json:"similar_cases,omitempty"`
    Alternative    *EscalationRecommendation `json:"alternative,omitempty"`
}

// EscalationRecommendation suggests an action.
type EscalationRecommendation struct {
    Action     string  `json:"action"` // escalate, continue, ask_user
    TargetTier int     `json:"target_tier,omitempty"`
    Confidence float64 `json:"confidence"`
}

// EscalationCase is a historical escalation example.
type EscalationCase struct {
    TraceID          string `json:"trace_id"`
    SHA              string `json:"sha"`
    Outcome          string `json:"outcome"`
    IterationsBefore int    `json:"iterations_before"`
    IterationsAfter  int    `json:"iterations_after"`
}

// WebSocketMessage is sent over the ambient subscription.
type WebSocketMessage struct {
    Type      string       `json:"type"` // context_update, precedent
    Trace     *CommitTrace `json:"trace,omitempty"`
    Relevance float64      `json:"relevance,omitempty"`
    Trigger   string       `json:"trigger,omitempty"`
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./pkg/contextgraph/... -v`
Expected: PASS

**Step 5: Commit**

```bash
git add pkg/contextgraph/api.go pkg/contextgraph/api_test.go
git commit -m "add(contextgraph): API request/response types"
```

---

### Task 1.3: Create Client Interface and Config

**Files:**
- Create: `pkg/contextgraph/client.go`
- Test: `pkg/contextgraph/client_test.go`

**Step 1: Write the failing test**

```go
// pkg/contextgraph/client_test.go
package contextgraph

import (
    "testing"

    "github.com/stretchr/testify/assert"
)

func TestConfig_Defaults(t *testing.T) {
    cfg := DefaultConfig()

    assert.False(t, cfg.Enabled)
    assert.Equal(t, "http://localhost:8080", cfg.ServiceEndpoint)
    assert.Equal(t, "nats://localhost:4222", cfg.NatsURL)
}

func TestNewClient_Disabled(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Enabled = false

    client, err := New(cfg)
    assert.NoError(t, err)
    assert.NotNil(t, client)
    assert.False(t, client.Enabled())
}

func TestClient_NilSafe(t *testing.T) {
    // Nil client should not panic
    var c *Client
    assert.False(t, c.Enabled())
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./pkg/contextgraph/... -v -run TestConfig`
Expected: FAIL - New function not defined

**Step 3: Write minimal implementation**

```go
// pkg/contextgraph/client.go
package contextgraph

import (
    "context"
    "net/http"
    "time"
)

// Config configures the context graph client.
type Config struct {
    Enabled         bool          `yaml:"enabled"`
    ServiceEndpoint string        `yaml:"service_endpoint"`
    NatsURL         string        `yaml:"nats_url"`
    Timeout         time.Duration `yaml:"timeout"`
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
    return Config{
        Enabled:         false,
        ServiceEndpoint: "http://localhost:8080",
        NatsURL:         "nats://localhost:4222",
        Timeout:         10 * time.Second,
    }
}

// Client provides access to the Context Graph Service.
type Client struct {
    http     *http.Client
    endpoint string
    natsURL  string
    enabled  bool
}

// New creates a new context graph client.
func New(cfg Config) (*Client, error) {
    if !cfg.Enabled {
        return &Client{enabled: false}, nil
    }

    return &Client{
        http:     &http.Client{Timeout: cfg.Timeout},
        endpoint: cfg.ServiceEndpoint,
        natsURL:  cfg.NatsURL,
        enabled:  true,
    }, nil
}

// Enabled returns whether the client is active.
func (c *Client) Enabled() bool {
    if c == nil {
        return false
    }
    return c.enabled
}

// Close releases resources.
func (c *Client) Close() error {
    if c == nil || !c.enabled {
        return nil
    }
    // NATS connection cleanup will be added later
    return nil
}

// PublishTrace publishes a commit trace to NATS.
// Placeholder - NATS integration added in later task.
func (c *Client) PublishTrace(ctx context.Context, trace *CommitTrace) error {
    if !c.Enabled() {
        return nil
    }
    // TODO: Implement NATS publish
    return nil
}

// Search finds traces similar to the query.
// Placeholder - HTTP implementation added in later task.
func (c *Client) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
    if !c.Enabled() {
        return &SearchResponse{}, nil
    }
    // TODO: Implement HTTP POST
    return &SearchResponse{}, nil
}

// Prefetch retrieves relevant traces for a session.
// Placeholder - HTTP implementation added in later task.
func (c *Client) Prefetch(ctx context.Context, req PrefetchRequest) (*PrefetchResponse, error) {
    if !c.Enabled() {
        return &PrefetchResponse{}, nil
    }
    // TODO: Implement HTTP POST
    return &PrefetchResponse{}, nil
}

// EscalationHint gets escalation recommendation.
// Placeholder - HTTP implementation added in later task.
func (c *Client) EscalationHint(ctx context.Context, req EscalationRequest) (*EscalationResponse, error) {
    if !c.Enabled() {
        return nil, nil
    }
    // TODO: Implement HTTP POST
    return nil, nil
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./pkg/contextgraph/... -v`
Expected: PASS

**Step 5: Commit**

```bash
git add pkg/contextgraph/client.go pkg/contextgraph/client_test.go
git commit -m "add(contextgraph): client interface and configuration"
```

---

## Phase 2: Context Service Foundation

### Task 2.1: Service Entry Point

**Files:**
- Create: `cmd/contextd/main.go`
- Create: `internal/contextservice/config/config.go`

**Step 1: Create config structure**

```go
// internal/contextservice/config/config.go
package config

import (
    "os"
    "time"

    "gopkg.in/yaml.v3"
)

// Config is the service configuration.
type Config struct {
    Server    ServerConfig    `yaml:"server"`
    Postgres  PostgresConfig  `yaml:"postgres"`
    Qdrant    QdrantConfig    `yaml:"qdrant"`
    NATS      NATSConfig      `yaml:"nats"`
    Embedder  EmbedderConfig  `yaml:"embedder"`
    Log       LogConfig       `yaml:"log"`
}

// ServerConfig configures the HTTP server.
type ServerConfig struct {
    Addr         string        `yaml:"addr"`
    ReadTimeout  time.Duration `yaml:"read_timeout"`
    WriteTimeout time.Duration `yaml:"write_timeout"`
}

// PostgresConfig configures PostgreSQL.
type PostgresConfig struct {
    URL             string `yaml:"url"`
    MaxOpenConns    int    `yaml:"max_open_conns"`
    MaxIdleConns    int    `yaml:"max_idle_conns"`
    ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime"`
}

// QdrantConfig configures Qdrant.
type QdrantConfig struct {
    Host       string `yaml:"host"`
    Port       int    `yaml:"port"`
    APIKey     string `yaml:"api_key"`
    Collection string `yaml:"collection"`
}

// NATSConfig configures NATS JetStream.
type NATSConfig struct {
    URL      string `yaml:"url"`
    Stream   string `yaml:"stream"`
    Consumer string `yaml:"consumer"`
}

// EmbedderConfig configures embedding generation.
type EmbedderConfig struct {
    Provider  string `yaml:"provider"` // openai, local
    Model     string `yaml:"model"`
    APIKey    string `yaml:"api_key"`
    BatchSize int    `yaml:"batch_size"`
}

// LogConfig configures logging.
type LogConfig struct {
    Level  string `yaml:"level"`
    Format string `yaml:"format"` // json, text
}

// Default returns a config with sensible defaults.
func Default() *Config {
    return &Config{
        Server: ServerConfig{
            Addr:         ":8080",
            ReadTimeout:  30 * time.Second,
            WriteTimeout: 30 * time.Second,
        },
        Postgres: PostgresConfig{
            URL:             "postgres://context:context@localhost:5432/context?sslmode=disable",
            MaxOpenConns:    25,
            MaxIdleConns:    5,
            ConnMaxLifetime: 5 * time.Minute,
        },
        Qdrant: QdrantConfig{
            Host:       "localhost",
            Port:       6333,
            Collection: "commit_traces",
        },
        NATS: NATSConfig{
            URL:      "nats://localhost:4222",
            Stream:   "BUCKLEY",
            Consumer: "contextd",
        },
        Embedder: EmbedderConfig{
            Provider:  "openai",
            Model:     "text-embedding-ada-002",
            BatchSize: 100,
        },
        Log: LogConfig{
            Level:  "info",
            Format: "json",
        },
    }
}

// Load reads config from a YAML file.
func Load(path string) (*Config, error) {
    cfg := Default()

    data, err := os.ReadFile(path)
    if err != nil {
        if os.IsNotExist(err) {
            return cfg, nil
        }
        return nil, err
    }

    if err := yaml.Unmarshal(data, cfg); err != nil {
        return nil, err
    }

    return cfg, nil
}
```

**Step 2: Create main entry point**

```go
// cmd/contextd/main.go
package main

import (
    "context"
    "flag"
    "fmt"
    "log/slog"
    "os"
    "os/signal"
    "syscall"

    "github.com/odvcencio/buckley/internal/contextservice/config"
)

func main() {
    configPath := flag.String("config", "", "path to config file")
    flag.Parse()

    cfg, err := config.Load(*configPath)
    if err != nil {
        fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
        os.Exit(1)
    }

    // Setup logger
    var handler slog.Handler
    if cfg.Log.Format == "json" {
        handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
            Level: parseLogLevel(cfg.Log.Level),
        })
    } else {
        handler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
            Level: parseLogLevel(cfg.Log.Level),
        })
    }
    logger := slog.New(handler)
    slog.SetDefault(logger)

    ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
    defer cancel()

    logger.Info("starting contextd", "addr", cfg.Server.Addr)

    // TODO: Initialize and start server
    // For now, just wait for shutdown signal
    <-ctx.Done()
    logger.Info("shutting down")
}

func parseLogLevel(level string) slog.Level {
    switch level {
    case "debug":
        return slog.LevelDebug
    case "info":
        return slog.LevelInfo
    case "warn":
        return slog.LevelWarn
    case "error":
        return slog.LevelError
    default:
        return slog.LevelInfo
    }
}
```

**Step 3: Verify it compiles**

Run: `go build ./cmd/contextd/`
Expected: Binary builds successfully

**Step 4: Commit**

```bash
git add cmd/contextd/ internal/contextservice/
git commit -m "add(contextd): service entry point and config"
```

---

### Task 2.2: PostgreSQL Store Interface

**Files:**
- Create: `internal/contextservice/store/store.go`
- Create: `internal/contextservice/store/postgres.go`
- Test: `internal/contextservice/store/postgres_test.go`

**Step 1: Write the failing test**

```go
// internal/contextservice/store/postgres_test.go
package store

import (
    "context"
    "testing"
    "time"

    "github.com/odvcencio/buckley/pkg/contextgraph"
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestPostgresStore_SaveAndGet(t *testing.T) {
    // Skip if no test database
    dsn := testDSN()
    if dsn == "" {
        t.Skip("TEST_POSTGRES_URL not set")
    }

    ctx := context.Background()
    store, err := NewPostgres(dsn)
    require.NoError(t, err)
    defer store.Close()

    trace := &contextgraph.CommitTrace{
        ID:        "test-trace-1",
        SHA:       "abc123def456",
        Repo:      "test/repo",
        Branch:    "main",
        Author:    "test@example.com",
        Timestamp: time.Now().UTC().Truncate(time.Second),
        Areas:     []string{"pkg/auth", "pkg/api"},
        Status:    contextgraph.StatusPending,
        Decisions: []contextgraph.Decision{
            {
                ID:        "dec-1",
                Context:   "How to structure auth",
                Reasoning: "Keep it simple",
            },
        },
    }

    err = store.SaveTrace(ctx, trace)
    require.NoError(t, err)

    got, err := store.GetTrace(ctx, trace.Repo, trace.SHA)
    require.NoError(t, err)
    require.NotNil(t, got)

    assert.Equal(t, trace.SHA, got.SHA)
    assert.Equal(t, trace.Repo, got.Repo)
    assert.Equal(t, trace.Areas, got.Areas)
    assert.Len(t, got.Decisions, 1)
}

func testDSN() string {
    return os.Getenv("TEST_POSTGRES_URL")
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/contextservice/store/... -v`
Expected: FAIL - Store interface not defined

**Step 3: Write store interface**

```go
// internal/contextservice/store/store.go
package store

import (
    "context"

    "github.com/odvcencio/buckley/pkg/contextgraph"
)

// Store provides persistence operations for context traces.
type Store interface {
    // Write operations
    SaveTrace(ctx context.Context, trace *contextgraph.CommitTrace) error
    UpdateStatus(ctx context.Context, repo, sha string, status contextgraph.TraceStatus, meta StatusMeta) error

    // Read operations
    GetTrace(ctx context.Context, repo, sha string) (*contextgraph.CommitTrace, error)
    ListTraces(ctx context.Context, filter TraceFilter) (*TraceList, error)

    // Lifecycle
    Close() error
}

// StatusMeta provides additional metadata when updating trace status.
type StatusMeta struct {
    LandedAt   *time.Time
    MergedVia  *string
    RevertedBy *string
}

// TraceFilter specifies criteria for listing traces.
type TraceFilter struct {
    Repo   string
    Status contextgraph.TraceStatus
    Areas  []string
    Since  *time.Time
    Limit  int
    Offset int
}

// TraceList is a paginated list of traces.
type TraceList struct {
    Traces []contextgraph.CommitTrace
    Total  int
}
```

**Step 4: Write PostgreSQL implementation**

```go
// internal/contextservice/store/postgres.go
package store

import (
    "context"
    "database/sql"
    "encoding/json"
    "fmt"
    "time"

    "github.com/lib/pq"
    "github.com/odvcencio/buckley/pkg/contextgraph"
)

// Postgres implements Store using PostgreSQL.
type Postgres struct {
    db *sql.DB
}

// NewPostgres creates a new PostgreSQL store.
func NewPostgres(dsn string) (*Postgres, error) {
    db, err := sql.Open("postgres", dsn)
    if err != nil {
        return nil, fmt.Errorf("open database: %w", err)
    }

    if err := db.Ping(); err != nil {
        db.Close()
        return nil, fmt.Errorf("ping database: %w", err)
    }

    return &Postgres{db: db}, nil
}

// Close closes the database connection.
func (p *Postgres) Close() error {
    return p.db.Close()
}

// SaveTrace saves a commit trace.
func (p *Postgres) SaveTrace(ctx context.Context, trace *contextgraph.CommitTrace) error {
    traceData, err := json.Marshal(trace)
    if err != nil {
        return fmt.Errorf("marshal trace: %w", err)
    }

    files, err := json.Marshal(trace.Files)
    if err != nil {
        return fmt.Errorf("marshal files: %w", err)
    }

    stats, err := json.Marshal(trace.Stats)
    if err != nil {
        return fmt.Errorf("marshal stats: %w", err)
    }

    query := `
        INSERT INTO commit_traces (
            id, sha, repo, branch, author, timestamp,
            areas, files, stats, mode, iterations,
            duration_ms, tokens_used, trace_data, summary, status,
            created_at, updated_at
        ) VALUES (
            $1, $2, $3, $4, $5, $6,
            $7, $8, $9, $10, $11,
            $12, $13, $14, $15, $16,
            $17, $17
        )
        ON CONFLICT (sha, repo) DO UPDATE SET
            trace_data = EXCLUDED.trace_data,
            summary = EXCLUDED.summary,
            updated_at = NOW()
    `

    _, err = p.db.ExecContext(ctx, query,
        trace.ID, trace.SHA, trace.Repo, trace.Branch, trace.Author, trace.Timestamp,
        pq.Array(trace.Areas), files, stats, trace.Mode, trace.Iterations,
        trace.Duration.Milliseconds(), trace.TokensUsed, traceData, trace.Summary, trace.Status,
        time.Now().UTC(),
    )
    if err != nil {
        return fmt.Errorf("insert trace: %w", err)
    }

    return nil
}

// GetTrace retrieves a trace by repo and SHA.
func (p *Postgres) GetTrace(ctx context.Context, repo, sha string) (*contextgraph.CommitTrace, error) {
    query := `
        SELECT trace_data FROM commit_traces
        WHERE repo = $1 AND sha = $2
    `

    var data []byte
    err := p.db.QueryRowContext(ctx, query, repo, sha).Scan(&data)
    if err == sql.ErrNoRows {
        return nil, nil
    }
    if err != nil {
        return nil, fmt.Errorf("query trace: %w", err)
    }

    var trace contextgraph.CommitTrace
    if err := json.Unmarshal(data, &trace); err != nil {
        return nil, fmt.Errorf("unmarshal trace: %w", err)
    }

    return &trace, nil
}

// UpdateStatus updates a trace's status.
func (p *Postgres) UpdateStatus(ctx context.Context, repo, sha string, status contextgraph.TraceStatus, meta StatusMeta) error {
    query := `
        UPDATE commit_traces SET
            status = $3,
            landed_at = $4,
            merged_via = $5,
            reverted_by = $6,
            updated_at = NOW()
        WHERE repo = $1 AND sha = $2
    `

    _, err := p.db.ExecContext(ctx, query, repo, sha, status, meta.LandedAt, meta.MergedVia, meta.RevertedBy)
    if err != nil {
        return fmt.Errorf("update status: %w", err)
    }

    return nil
}

// ListTraces returns traces matching the filter.
func (p *Postgres) ListTraces(ctx context.Context, filter TraceFilter) (*TraceList, error) {
    // Implementation for listing with filters
    // Simplified for initial implementation
    query := `
        SELECT trace_data FROM commit_traces
        WHERE ($1 = '' OR repo = $1)
        AND ($2 = '' OR status = $2)
        ORDER BY timestamp DESC
        LIMIT $3 OFFSET $4
    `

    limit := filter.Limit
    if limit == 0 {
        limit = 100
    }

    rows, err := p.db.QueryContext(ctx, query, filter.Repo, filter.Status, limit, filter.Offset)
    if err != nil {
        return nil, fmt.Errorf("query traces: %w", err)
    }
    defer rows.Close()

    var traces []contextgraph.CommitTrace
    for rows.Next() {
        var data []byte
        if err := rows.Scan(&data); err != nil {
            return nil, fmt.Errorf("scan row: %w", err)
        }
        var trace contextgraph.CommitTrace
        if err := json.Unmarshal(data, &trace); err != nil {
            return nil, fmt.Errorf("unmarshal: %w", err)
        }
        traces = append(traces, trace)
    }

    return &TraceList{Traces: traces, Total: len(traces)}, nil
}
```

**Step 5: Add postgres driver import**

Add to go.mod: `github.com/lib/pq`

Run: `go mod tidy`

**Step 6: Commit**

```bash
git add internal/contextservice/store/ go.mod go.sum
git commit -m "add(contextd): PostgreSQL store implementation"
```

---

### Task 2.3: Database Migrations

**Files:**
- Create: `internal/contextservice/migrations/001_initial.up.sql`
- Create: `internal/contextservice/migrations/001_initial.down.sql`
- Create: `internal/contextservice/migrations/migrate.go`

**Step 1: Create up migration**

```sql
-- internal/contextservice/migrations/001_initial.up.sql

-- Extensions
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pg_trgm";

-- Core trace storage
CREATE TABLE IF NOT EXISTS commit_traces (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    sha         VARCHAR(40) NOT NULL,
    repo        VARCHAR(255) NOT NULL,
    branch      VARCHAR(255) NOT NULL,
    author      VARCHAR(255),
    timestamp   TIMESTAMPTZ NOT NULL,

    areas       TEXT[] NOT NULL DEFAULT '{}',
    files       JSONB NOT NULL DEFAULT '[]',
    stats       JSONB NOT NULL DEFAULT '{}',

    mode        VARCHAR(20) NOT NULL DEFAULT 'classic',
    iterations  INT NOT NULL DEFAULT 0,
    duration_ms BIGINT,
    tokens_used INT,

    trace_data  JSONB NOT NULL,
    summary     TEXT,

    status      VARCHAR(20) NOT NULL DEFAULT 'pending',
    landed_at   TIMESTAMPTZ,
    merged_via  VARCHAR(100),
    reverted_by VARCHAR(40),

    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_trace_sha_repo UNIQUE(sha, repo)
);

-- Indexes
CREATE INDEX IF NOT EXISTS idx_traces_repo ON commit_traces(repo);
CREATE INDEX IF NOT EXISTS idx_traces_status ON commit_traces(status);
CREATE INDEX IF NOT EXISTS idx_traces_mode ON commit_traces(mode);
CREATE INDEX IF NOT EXISTS idx_traces_timestamp ON commit_traces(timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_traces_author ON commit_traces(author);
CREATE INDEX IF NOT EXISTS idx_traces_areas ON commit_traces USING GIN(areas);
CREATE INDEX IF NOT EXISTS idx_traces_landed ON commit_traces(landed_at DESC) WHERE status = 'landed';
CREATE INDEX IF NOT EXISTS idx_traces_summary_trgm ON commit_traces USING GIN(summary gin_trgm_ops);

-- Decisions table
CREATE TABLE IF NOT EXISTS decisions (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    trace_id    UUID NOT NULL REFERENCES commit_traces(id) ON DELETE CASCADE,
    timestamp   TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    context     TEXT NOT NULL,
    options     JSONB NOT NULL DEFAULT '[]',
    selected    INT NOT NULL,
    reasoning   TEXT,

    category    VARCHAR(50),
    risk        VARCHAR(20),
    reversible  BOOLEAN DEFAULT true,

    auto        BOOLEAN DEFAULT false,
    confidence  REAL,
    area        VARCHAR(255),

    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_decisions_trace ON decisions(trace_id);
CREATE INDEX IF NOT EXISTS idx_decisions_category ON decisions(category);
CREATE INDEX IF NOT EXISTS idx_decisions_area ON decisions(area);

-- Escalations table
CREATE TABLE IF NOT EXISTS escalations (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    trace_id        UUID NOT NULL REFERENCES commit_traces(id) ON DELETE CASCADE,
    from_tier       INT NOT NULL,
    to_tier         INT NOT NULL,
    reason          TEXT,
    iteration       INT NOT NULL,
    timestamp       TIMESTAMPTZ NOT NULL,
    resolved_at_tier INT,
    iterations_after INT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_escalations_trace ON escalations(trace_id);
CREATE INDEX IF NOT EXISTS idx_escalations_tiers ON escalations(from_tier, to_tier);

-- Update timestamp trigger
CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS traces_updated_at ON commit_traces;
CREATE TRIGGER traces_updated_at
    BEFORE UPDATE ON commit_traces
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at();
```

**Step 2: Create down migration**

```sql
-- internal/contextservice/migrations/001_initial.down.sql

DROP TRIGGER IF EXISTS traces_updated_at ON commit_traces;
DROP FUNCTION IF EXISTS update_updated_at();
DROP TABLE IF EXISTS escalations;
DROP TABLE IF EXISTS decisions;
DROP TABLE IF EXISTS commit_traces;
```

**Step 3: Create migration runner**

```go
// internal/contextservice/migrations/migrate.go
package migrations

import (
    "database/sql"
    "embed"
    "fmt"
    "io/fs"
    "sort"
    "strings"
)

//go:embed *.sql
var migrationFS embed.FS

// Migrate runs all pending migrations.
func Migrate(db *sql.DB) error {
    // Create migrations table
    _, err := db.Exec(`
        CREATE TABLE IF NOT EXISTS schema_migrations (
            version VARCHAR(255) PRIMARY KEY,
            applied_at TIMESTAMPTZ DEFAULT NOW()
        )
    `)
    if err != nil {
        return fmt.Errorf("create migrations table: %w", err)
    }

    // Get applied migrations
    applied := make(map[string]bool)
    rows, err := db.Query("SELECT version FROM schema_migrations")
    if err != nil {
        return fmt.Errorf("query migrations: %w", err)
    }
    defer rows.Close()

    for rows.Next() {
        var version string
        if err := rows.Scan(&version); err != nil {
            return err
        }
        applied[version] = true
    }

    // Find and run pending migrations
    entries, err := fs.ReadDir(migrationFS, ".")
    if err != nil {
        return fmt.Errorf("read migrations: %w", err)
    }

    var upMigrations []string
    for _, e := range entries {
        if strings.HasSuffix(e.Name(), ".up.sql") {
            upMigrations = append(upMigrations, e.Name())
        }
    }
    sort.Strings(upMigrations)

    for _, name := range upMigrations {
        version := strings.TrimSuffix(name, ".up.sql")
        if applied[version] {
            continue
        }

        content, err := fs.ReadFile(migrationFS, name)
        if err != nil {
            return fmt.Errorf("read %s: %w", name, err)
        }

        tx, err := db.Begin()
        if err != nil {
            return fmt.Errorf("begin transaction: %w", err)
        }

        if _, err := tx.Exec(string(content)); err != nil {
            tx.Rollback()
            return fmt.Errorf("execute %s: %w", name, err)
        }

        if _, err := tx.Exec("INSERT INTO schema_migrations (version) VALUES ($1)", version); err != nil {
            tx.Rollback()
            return fmt.Errorf("record %s: %w", name, err)
        }

        if err := tx.Commit(); err != nil {
            return fmt.Errorf("commit %s: %w", name, err)
        }
    }

    return nil
}
```

**Step 4: Commit**

```bash
git add internal/contextservice/migrations/
git commit -m "add(contextd): database migrations"
```

---

## Phase 3: Vector Search with Qdrant

### Task 3.1: Qdrant Client

**Files:**
- Create: `internal/contextservice/store/qdrant.go`
- Test: `internal/contextservice/store/qdrant_test.go`

**Step 1: Add Qdrant dependency**

Run: `go get github.com/qdrant/go-client`

**Step 2: Write failing test**

```go
// internal/contextservice/store/qdrant_test.go
package store

import (
    "context"
    "os"
    "testing"

    "github.com/stretchr/testify/require"
)

func TestQdrant_Upsert(t *testing.T) {
    host := os.Getenv("TEST_QDRANT_HOST")
    if host == "" {
        t.Skip("TEST_QDRANT_HOST not set")
    }

    ctx := context.Background()
    q, err := NewQdrant(QdrantConfig{
        Host:       host,
        Port:       6333,
        Collection: "test_traces",
    })
    require.NoError(t, err)
    defer q.Close()

    // Ensure collection exists
    err = q.EnsureCollection(ctx, 1536)
    require.NoError(t, err)

    // Upsert a vector
    err = q.Upsert(ctx, VectorPoint{
        ID:        "test-1",
        Vector:    make([]float32, 1536),
        Payload: map[string]any{
            "sha":  "abc123",
            "repo": "test/repo",
        },
    })
    require.NoError(t, err)
}
```

**Step 3: Write Qdrant implementation**

```go
// internal/contextservice/store/qdrant.go
package store

import (
    "context"
    "fmt"

    "github.com/qdrant/go-client/qdrant"
)

// QdrantConfig configures the Qdrant client.
type QdrantConfig struct {
    Host       string
    Port       int
    APIKey     string
    Collection string
}

// Qdrant provides vector storage operations.
type Qdrant struct {
    client     *qdrant.Client
    collection string
}

// NewQdrant creates a new Qdrant client.
func NewQdrant(cfg QdrantConfig) (*Qdrant, error) {
    client, err := qdrant.NewClient(&qdrant.Config{
        Host:   cfg.Host,
        Port:   cfg.Port,
        APIKey: cfg.APIKey,
    })
    if err != nil {
        return nil, fmt.Errorf("create client: %w", err)
    }

    return &Qdrant{
        client:     client,
        collection: cfg.Collection,
    }, nil
}

// Close closes the Qdrant connection.
func (q *Qdrant) Close() error {
    return q.client.Close()
}

// EnsureCollection creates the collection if it doesn't exist.
func (q *Qdrant) EnsureCollection(ctx context.Context, vectorSize int) error {
    exists, err := q.client.CollectionExists(ctx, q.collection)
    if err != nil {
        return fmt.Errorf("check collection: %w", err)
    }

    if !exists {
        err = q.client.CreateCollection(ctx, &qdrant.CreateCollection{
            CollectionName: q.collection,
            VectorsConfig: qdrant.NewVectorsConfig(&qdrant.VectorParams{
                Size:     uint64(vectorSize),
                Distance: qdrant.Distance_Cosine,
            }),
        })
        if err != nil {
            return fmt.Errorf("create collection: %w", err)
        }
    }

    return nil
}

// VectorPoint represents a point to upsert.
type VectorPoint struct {
    ID      string
    Vector  []float32
    Payload map[string]any
}

// Upsert adds or updates a vector point.
func (q *Qdrant) Upsert(ctx context.Context, point VectorPoint) error {
    _, err := q.client.Upsert(ctx, &qdrant.UpsertPoints{
        CollectionName: q.collection,
        Points: []*qdrant.PointStruct{
            {
                Id:      qdrant.NewIDStr(point.ID),
                Vectors: qdrant.NewVectors(point.Vector...),
                Payload: toQdrantPayload(point.Payload),
            },
        },
    })
    if err != nil {
        return fmt.Errorf("upsert: %w", err)
    }

    return nil
}

// SearchResult from vector search.
type VectorSearchResult struct {
    ID      string
    Score   float32
    Payload map[string]any
}

// Search finds similar vectors.
func (q *Qdrant) Search(ctx context.Context, vector []float32, limit int, filter map[string]any) ([]VectorSearchResult, error) {
    var qdrantFilter *qdrant.Filter
    if len(filter) > 0 {
        qdrantFilter = buildFilter(filter)
    }

    results, err := q.client.Query(ctx, &qdrant.QueryPoints{
        CollectionName: q.collection,
        Query:          qdrant.NewQuery(vector...),
        Limit:          qdrant.PtrOf(uint64(limit)),
        WithPayload:    qdrant.NewWithPayload(true),
        Filter:         qdrantFilter,
    })
    if err != nil {
        return nil, fmt.Errorf("search: %w", err)
    }

    var out []VectorSearchResult
    for _, r := range results {
        out = append(out, VectorSearchResult{
            ID:      r.Id.GetUuid(),
            Score:   r.Score,
            Payload: fromQdrantPayload(r.Payload),
        })
    }

    return out, nil
}

func toQdrantPayload(m map[string]any) map[string]*qdrant.Value {
    out := make(map[string]*qdrant.Value)
    for k, v := range m {
        switch val := v.(type) {
        case string:
            out[k] = qdrant.NewValueString(val)
        case int:
            out[k] = qdrant.NewValueInt(int64(val))
        case float64:
            out[k] = qdrant.NewValueDouble(val)
        case bool:
            out[k] = qdrant.NewValueBool(val)
        case []string:
            out[k] = qdrant.NewValueList(stringsToValues(val)...)
        }
    }
    return out
}

func fromQdrantPayload(m map[string]*qdrant.Value) map[string]any {
    out := make(map[string]any)
    for k, v := range m {
        switch v.Kind.(type) {
        case *qdrant.Value_StringValue:
            out[k] = v.GetStringValue()
        case *qdrant.Value_IntegerValue:
            out[k] = v.GetIntegerValue()
        case *qdrant.Value_DoubleValue:
            out[k] = v.GetDoubleValue()
        case *qdrant.Value_BoolValue:
            out[k] = v.GetBoolValue()
        }
    }
    return out
}

func stringsToValues(ss []string) []*qdrant.Value {
    out := make([]*qdrant.Value, len(ss))
    for i, s := range ss {
        out[i] = qdrant.NewValueString(s)
    }
    return out
}

func buildFilter(m map[string]any) *qdrant.Filter {
    var conditions []*qdrant.Condition
    for k, v := range m {
        switch val := v.(type) {
        case string:
            conditions = append(conditions, &qdrant.Condition{
                ConditionOneOf: &qdrant.Condition_Field{
                    Field: &qdrant.FieldCondition{
                        Key:   k,
                        Match: &qdrant.Match{MatchValue: &qdrant.Match_Keyword{Keyword: val}},
                    },
                },
            })
        }
    }
    return &qdrant.Filter{Must: conditions}
}
```

**Step 4: Commit**

```bash
git add internal/contextservice/store/qdrant.go internal/contextservice/store/qdrant_test.go go.mod go.sum
git commit -m "add(contextd): Qdrant vector store integration"
```

---

## Phase 4: NATS Consumer

### Task 4.1: NATS JetStream Consumer

**Files:**
- Create: `internal/contextservice/ingest/consumer.go`
- Test: `internal/contextservice/ingest/consumer_test.go`

**Step 1: Write failing test**

```go
// internal/contextservice/ingest/consumer_test.go
package ingest

import (
    "context"
    "encoding/json"
    "testing"
    "time"

    "github.com/nats-io/nats.go"
    "github.com/odvcencio/buckley/pkg/contextgraph"
    "github.com/stretchr/testify/require"
)

func TestConsumer_ProcessTrace(t *testing.T) {
    // This test requires a running NATS server
    url := os.Getenv("TEST_NATS_URL")
    if url == "" {
        t.Skip("TEST_NATS_URL not set")
    }

    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    nc, err := nats.Connect(url)
    require.NoError(t, err)
    defer nc.Close()

    processed := make(chan *contextgraph.CommitTrace, 1)
    handler := func(ctx context.Context, trace *contextgraph.CommitTrace) error {
        processed <- trace
        return nil
    }

    consumer, err := NewConsumer(ConsumerConfig{
        NatsURL:  url,
        Stream:   "TEST_BUCKLEY",
        Consumer: "test-consumer",
        Subject:  contextgraph.SubjectCommitTrace,
    }, handler)
    require.NoError(t, err)

    go consumer.Start(ctx)

    // Publish a test trace
    trace := &contextgraph.CommitTrace{
        ID:   "test-1",
        SHA:  "abc123",
        Repo: "test/repo",
    }
    data, _ := json.Marshal(trace)

    js, _ := nc.JetStream()
    _, err = js.Publish(contextgraph.SubjectCommitTrace, data)
    require.NoError(t, err)

    select {
    case got := <-processed:
        require.Equal(t, trace.SHA, got.SHA)
    case <-ctx.Done():
        t.Fatal("timeout waiting for trace")
    }
}
```

**Step 2: Write consumer implementation**

```go
// internal/contextservice/ingest/consumer.go
package ingest

import (
    "context"
    "encoding/json"
    "fmt"
    "log/slog"
    "time"

    "github.com/nats-io/nats.go"
    "github.com/odvcencio/buckley/pkg/contextgraph"
)

// TraceHandler processes incoming traces.
type TraceHandler func(ctx context.Context, trace *contextgraph.CommitTrace) error

// ConsumerConfig configures the NATS consumer.
type ConsumerConfig struct {
    NatsURL  string
    Stream   string
    Consumer string
    Subject  string
}

// Consumer subscribes to NATS and processes traces.
type Consumer struct {
    nc      *nats.Conn
    js      nats.JetStreamContext
    sub     *nats.Subscription
    handler TraceHandler
    cfg     ConsumerConfig
    logger  *slog.Logger
}

// NewConsumer creates a new NATS consumer.
func NewConsumer(cfg ConsumerConfig, handler TraceHandler) (*Consumer, error) {
    nc, err := nats.Connect(cfg.NatsURL)
    if err != nil {
        return nil, fmt.Errorf("connect: %w", err)
    }

    js, err := nc.JetStream()
    if err != nil {
        nc.Close()
        return nil, fmt.Errorf("jetstream: %w", err)
    }

    return &Consumer{
        nc:      nc,
        js:      js,
        handler: handler,
        cfg:     cfg,
        logger:  slog.Default().With("component", "consumer"),
    }, nil
}

// Start begins consuming messages.
func (c *Consumer) Start(ctx context.Context) error {
    // Ensure stream exists
    _, err := c.js.StreamInfo(c.cfg.Stream)
    if err != nil {
        _, err = c.js.AddStream(&nats.StreamConfig{
            Name:     c.cfg.Stream,
            Subjects: []string{c.cfg.Subject},
            Storage:  nats.FileStorage,
        })
        if err != nil {
            return fmt.Errorf("create stream: %w", err)
        }
    }

    // Create durable consumer
    sub, err := c.js.PullSubscribe(
        c.cfg.Subject,
        c.cfg.Consumer,
        nats.Durable(c.cfg.Consumer),
        nats.AckExplicit(),
    )
    if err != nil {
        return fmt.Errorf("subscribe: %w", err)
    }
    c.sub = sub

    c.logger.Info("started consuming", "stream", c.cfg.Stream, "subject", c.cfg.Subject)

    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        default:
        }

        msgs, err := sub.Fetch(10, nats.MaxWait(5*time.Second))
        if err != nil {
            if err == nats.ErrTimeout {
                continue
            }
            c.logger.Error("fetch error", "error", err)
            continue
        }

        for _, msg := range msgs {
            if err := c.processMessage(ctx, msg); err != nil {
                c.logger.Error("process error", "error", err)
                msg.Nak()
            } else {
                msg.Ack()
            }
        }
    }
}

func (c *Consumer) processMessage(ctx context.Context, msg *nats.Msg) error {
    var trace contextgraph.CommitTrace
    if err := json.Unmarshal(msg.Data, &trace); err != nil {
        return fmt.Errorf("unmarshal: %w", err)
    }

    c.logger.Debug("processing trace", "sha", trace.SHA, "repo", trace.Repo)

    if err := c.handler(ctx, &trace); err != nil {
        return fmt.Errorf("handler: %w", err)
    }

    return nil
}

// Stop stops the consumer.
func (c *Consumer) Stop(ctx context.Context) error {
    if c.sub != nil {
        if err := c.sub.Unsubscribe(); err != nil {
            return err
        }
    }
    c.nc.Close()
    return nil
}
```

**Step 3: Commit**

```bash
git add internal/contextservice/ingest/
git commit -m "add(contextd): NATS JetStream consumer"
```

---

## Phase 5: HTTP API

### Task 5.1: HTTP Server and Routes

**Files:**
- Create: `internal/contextservice/server/server.go`
- Create: `internal/contextservice/server/routes.go`
- Create: `internal/contextservice/server/handlers.go`

**Step 1: Create server**

```go
// internal/contextservice/server/server.go
package server

import (
    "context"
    "log/slog"
    "net/http"
    "time"

    "github.com/go-chi/chi/v5"
    "github.com/go-chi/chi/v5/middleware"
)

// Server is the HTTP server.
type Server struct {
    router  *chi.Mux
    server  *http.Server
    logger  *slog.Logger
    search  SearchService
    store   Store
}

// SearchService handles search operations.
type SearchService interface {
    Search(ctx context.Context, req SearchRequest) (*SearchResponse, error)
    Prefetch(ctx context.Context, req PrefetchRequest) (*PrefetchResponse, error)
    EscalationHint(ctx context.Context, req EscalationRequest) (*EscalationResponse, error)
}

// Store provides data access.
type Store interface {
    GetTrace(ctx context.Context, repo, sha string) (*CommitTrace, error)
    UpdateStatus(ctx context.Context, repo, sha string, status TraceStatus, meta StatusMeta) error
}

// Config configures the server.
type Config struct {
    Addr         string
    ReadTimeout  time.Duration
    WriteTimeout time.Duration
}

// New creates a new server.
func New(cfg Config, search SearchService, store Store) *Server {
    s := &Server{
        router: chi.NewRouter(),
        logger: slog.Default().With("component", "server"),
        search: search,
        store:  store,
    }

    s.server = &http.Server{
        Addr:         cfg.Addr,
        Handler:      s.router,
        ReadTimeout:  cfg.ReadTimeout,
        WriteTimeout: cfg.WriteTimeout,
    }

    s.setupRoutes()
    return s
}

func (s *Server) setupRoutes() {
    s.router.Use(middleware.RequestID)
    s.router.Use(middleware.RealIP)
    s.router.Use(middleware.Logger)
    s.router.Use(middleware.Recoverer)

    // Health checks
    s.router.Get("/health/live", s.handleLive)
    s.router.Get("/health/ready", s.handleReady)

    // API v1
    s.router.Route("/v1", func(r chi.Router) {
        r.Post("/search", s.handleSearch)
        r.Get("/trace/{repo}/{sha}", s.handleGetTrace)
        r.Post("/prefetch", s.handlePrefetch)
        r.Post("/escalation", s.handleEscalation)
    })

    // Webhooks
    s.router.Post("/webhook/github", s.handleGitHubWebhook)
}

// Start starts the server.
func (s *Server) Start() error {
    s.logger.Info("starting server", "addr", s.server.Addr)
    return s.server.ListenAndServe()
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
    return s.server.Shutdown(ctx)
}
```

**Step 2: Create handlers**

```go
// internal/contextservice/server/handlers.go
package server

import (
    "encoding/json"
    "net/http"

    "github.com/go-chi/chi/v5"
)

func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
    json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
    // TODO: Check dependencies
    json.NewEncoder(w).Encode(map[string]string{"status": "ready"})
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
    var req SearchRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }

    resp, err := s.search.Search(r.Context(), req)
    if err != nil {
        s.logger.Error("search error", "error", err)
        http.Error(w, "internal error", http.StatusInternalServerError)
        return
    }

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleGetTrace(w http.ResponseWriter, r *http.Request) {
    repo := chi.URLParam(r, "repo")
    sha := chi.URLParam(r, "sha")

    trace, err := s.store.GetTrace(r.Context(), repo, sha)
    if err != nil {
        s.logger.Error("get trace error", "error", err)
        http.Error(w, "internal error", http.StatusInternalServerError)
        return
    }

    if trace == nil {
        http.Error(w, "not found", http.StatusNotFound)
        return
    }

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(trace)
}

func (s *Server) handlePrefetch(w http.ResponseWriter, r *http.Request) {
    var req PrefetchRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }

    resp, err := s.search.Prefetch(r.Context(), req)
    if err != nil {
        s.logger.Error("prefetch error", "error", err)
        http.Error(w, "internal error", http.StatusInternalServerError)
        return
    }

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleEscalation(w http.ResponseWriter, r *http.Request) {
    var req EscalationRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }

    resp, err := s.search.EscalationHint(r.Context(), req)
    if err != nil {
        s.logger.Error("escalation error", "error", err)
        http.Error(w, "internal error", http.StatusInternalServerError)
        return
    }

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
    // TODO: Validate signature, parse payload, update trace status
    w.WriteHeader(http.StatusOK)
}
```

**Step 3: Commit**

```bash
git add internal/contextservice/server/
git commit -m "add(contextd): HTTP server and API handlers"
```

---

## Phase 6: Buckley Integration

### Task 6.1: Wire Client into Config

**Files:**
- Modify: `pkg/config/config.go`

**Step 1: Add context graph config**

Find the Config struct and add:

```go
ContextGraph contextgraph.Config `yaml:"context_graph"`
```

**Step 2: Commit**

```bash
git add pkg/config/config.go
git commit -m "update(config): add context graph client configuration"
```

---

### Task 6.2: Integrate with Commit Flow

**Files:**
- Modify: `pkg/oneshot/commit/run.go`

**Step 1: Add trace building and publishing**

Add context graph client to Runner struct and publish trace after successful commit.

**Step 2: Commit**

```bash
git add pkg/oneshot/commit/run.go
git commit -m "update(commit): publish decision traces to context graph"
```

---

### Task 6.3: Classic Mode Enrichment

**Files:**
- Modify: `pkg/agent/executor.go`

**Step 1: Add prefetch on session start**

Load relevant precedents and enrich system prompt.

**Step 2: Commit**

```bash
git add pkg/agent/executor.go
git commit -m "update(agent): enrich classic mode with context precedents"
```

---

### Task 6.4: RLM Mode Integration

**Files:**
- Modify: `pkg/rlm/coordinator.go`

**Step 1: Add prefetch to scratchpad and escalation hints**

Load precedents into scratchpad, query for escalation recommendations.

**Step 2: Commit**

```bash
git add pkg/rlm/coordinator.go
git commit -m "update(rlm): integrate context graph for precedent and escalation hints"
```

---

## Phase 7: Deployment

### Task 7.1: Dockerfile

**Files:**
- Create: `cmd/contextd/Dockerfile`

```dockerfile
FROM golang:1.25-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o contextd ./cmd/contextd

FROM alpine:3.19
RUN apk add --no-cache ca-certificates
COPY --from=builder /app/contextd /usr/local/bin/
ENTRYPOINT ["contextd"]
```

**Step 1: Commit**

```bash
git add cmd/contextd/Dockerfile
git commit -m "add(contextd): Dockerfile"
```

---

### Task 7.2: Helm Chart

**Files:**
- Create: `deploy/charts/context-service/Chart.yaml`
- Create: `deploy/charts/context-service/values.yaml`
- Create: `deploy/charts/context-service/templates/deployment.yaml`
- Create: `deploy/charts/context-service/templates/service.yaml`
- Create: `deploy/charts/context-service/templates/configmap.yaml`

See design document for full Helm chart structure.

**Step 1: Commit**

```bash
git add deploy/charts/context-service/
git commit -m "add(contextd): Helm chart for Kubernetes deployment"
```

---

### Task 7.3: Docker Compose for Development

**Files:**
- Create: `docker-compose.context.yml`

```yaml
version: "3.8"

services:
  contextd:
    build:
      context: .
      dockerfile: cmd/contextd/Dockerfile
    ports:
      - "8080:8080"
    environment:
      - POSTGRES_URL=postgres://context:context@postgres:5432/context?sslmode=disable
      - QDRANT_URL=http://qdrant:6333
      - NATS_URL=nats://nats:4222
    depends_on:
      - postgres
      - qdrant
      - nats

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

volumes:
  postgres_data:
  qdrant_data:
  nats_data:
```

**Step 1: Commit**

```bash
git add docker-compose.context.yml
git commit -m "add(contextd): Docker Compose for local development"
```

---

## Summary

This plan covers:

1. **Phase 1:** Core types and client library (`pkg/contextgraph/`)
2. **Phase 2:** Service foundation (`cmd/contextd/`, config, PostgreSQL store)
3. **Phase 3:** Vector search with Qdrant
4. **Phase 4:** NATS JetStream consumer for trace ingestion
5. **Phase 5:** HTTP API for queries and webhooks
6. **Phase 6:** Buckley integration (commit flow, Classic mode, RLM mode)
7. **Phase 7:** Deployment artifacts (Docker, Helm)

Each task is designed for TDD: write failing test, implement, verify, commit.
