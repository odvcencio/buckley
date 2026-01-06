# Agent Communication Protocol (ACP) - Research & Design

**Version**: 1.0
**Date**: 2025-11-18
**Status**: Design Complete - Ready for Implementation

---

## Executive Summary

The Agent Communication Protocol (ACP) is a comprehensive communication framework designed to enable:

1. **Zed Editor Integration** - LSP-based AI assistance directly in the editor (primary goal)
2. **Agent Collaboration** - Multi-agent orchestration for complex tasks (secondary)
3. **External IDE Embedding** - Programmatic control for VS Code, IntelliJ, etc. (secondary)
4. **Tool Ecosystem Expansion** - Bidirectional plugin communication (secondary)

ACP builds on Buckley's existing gRPC and IPC infrastructure, adding new capabilities for:
- Feature-based capability negotiation (start lightweight, evolve over time)
- Orchestrated agent swarms with peer-to-peer mesh communication
- Event-sourced state management with perfect recovery
- Context-aware security with trust-based tool approval
- Full observability stack (logs, metrics, tracing, real-time dashboards)

**Implementation Strategy**: Big bang - all components built together to ensure architectural consistency.

---

## 1. Background & Motivation

### 1.1 Current State

Buckley currently provides:
- **gRPC SDK** (`pkg/sdk/grpc`) - JSON-over-gRPC with Plan, ExecutePlan, GetPlan, ListPlans RPCs
- **IPC Server** (`pkg/ipc`) - HTTP/WebSocket on 127.0.0.1:4488 with REST APIs and WebSocket events
- **Plugin System** (`pkg/tool`) - External plugins via stdin/stdout JSON exchange
- **SQLite Storage** (`pkg/storage`) - Sessions, messages, API calls, embeddings persistence

### 1.2 Limitations

Current infrastructure doesn't support:
- **Editor integration** - No LSP server for Zed/VS Code/IntelliJ
- **Agent-to-agent communication** - Agents can't discover or collaborate with each other
- **Real-time streaming** - Limited to WebSocket broadcasts, no targeted streaming
- **Capability negotiation** - Clients can't discover what features are available
- **P2P communication** - All traffic routes through central server (bottleneck)
- **Event sourcing** - State is ephemeral, crashes lose context
- **Distributed tracing** - Can't debug complex multi-agent interactions

### 1.3 Goals

ACP addresses these limitations by providing:

**Primary Goal**: Enable Zed editor to act as a rich AI assistant client
- Start with lightweight text Q&A (LSP custom commands)
- Evolve to deep integration (task execution, progress streaming, tool approvals)
- Architecture supports evolution without breaking changes

**Secondary Goals**:
- **Agent swarms** - Coordinate multiple agents on complex plans
- **IDE ecosystem** - Support VS Code, IntelliJ, and future editors
- **Plugin bidirectionality** - Plugins can receive events and make requests

### 1.4 Design Principles

1. **Evolution over versioning** - Feature flags enable progressive capability adoption
2. **Hybrid topologies** - Central orchestration + P2P mesh for scale
3. **Environment-aware** - Different backends for local dev (SQLite) vs production (K8s)
4. **Security by default** - Token auth + capability grants for fine-grained control
5. **Observable by design** - Multi-layered telemetry built in from day one
6. **Leverage existing infrastructure** - Extend gRPC, reuse WebSocket, build on SQLite

---

## 2. Architecture Overview

### 2.1 System Topology

```
┌─────────────────────────────────────────────────────────────────┐
│                      ACP Coordinator                            │
│                                                                 │
│  ┌────────────────┐  ┌────────────────┐  ┌─────────────────┐  │
│  │  gRPC Server   │  │  LSP Bridge    │  │ Service         │  │
│  │  (streaming)   │  │  Plugin        │  │ Discovery       │  │
│  │                │  │                │  │ Registry        │  │
│  └────────────────┘  └────────────────┘  └─────────────────┘  │
│                                                                 │
│  ┌────────────────┐  ┌────────────────┐  ┌─────────────────┐  │
│  │  Event Store   │  │  Pub/Sub       │  │ Circuit Breaker │  │
│  │  - SQLite      │  │  - NATS/Redis  │  │ Manager         │  │
│  │  - NATS JS     │  │  - In-memory   │  │                 │  │
│  │  - Kafka       │  │                │  │                 │  │
│  └────────────────┘  └────────────────┘  └─────────────────┘  │
│                                                                 │
│  ┌────────────────┐  ┌────────────────┐  ┌─────────────────┐  │
│  │  Capability    │  │  Context       │  │ Observability   │  │
│  │  Manager       │  │  Store         │  │ - OTEL Tracing  │  │
│  │                │  │  (Sessions +   │  │ - Prometheus    │  │
│  │                │  │   Handles)     │  │ - Event Stream  │  │
│  └────────────────┘  └────────────────┘  └─────────────────┘  │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
              │                      │                     │
              │                      │                     │
      ┌───────┴────────┐     ┌──────┴───────┐     ┌──────┴───────┐
      │                │     │              │     │              │
      │  Zed Editor    │     │ Agent Pool   │     │ External     │
      │  (LSP Client)  │     │ (P2P Mesh)   │     │ IDEs         │
      │                │     │              │     │              │
      └────────────────┘     └──────────────┘     └──────────────┘
           LSP/JSON              gRPC Streams         gRPC/LSP
```

### 2.2 Communication Patterns

**Three modes of communication**:

1. **Client ↔ Coordinator** (gRPC bidirectional streams)
   - Zed editor via LSP bridge plugin
   - External agents registering/discovering
   - Session establishment and context management

2. **Agent ↔ Agent** (Direct P2P gRPC)
   - After coordinator introduction
   - Large context payloads (code, embeddings)
   - Collaborative editing sessions

3. **Broadcast** (Pub/Sub)
   - Task progress updates (1 → N observers)
   - Telemetry events
   - Agent lifecycle notifications

### 2.3 Message Routing Strategy

**Hybrid: Metadata through coordinator, Payloads direct P2P**

```
Control messages (route via coordinator):
- Task assignments
- Agent registration/discovery
- Capability grants
- Status updates
- Audit events

Data payloads (route P2P):
- File contents
- Embedding vectors
- Code context
- Conversation history
- Tool outputs
```

**Benefits**:
- Coordinator maintains full visibility into orchestration
- No coordinator bottleneck for large transfers
- Circuit breakers protect both paths independently

---

## 3. Core Components

### 3.1 gRPC Protocol Extensions

**New RPCs** (extends existing `pkg/sdk/grpc`):

```protobuf
service AgentCommunication {
  // Agent lifecycle
  rpc RegisterAgent(RegisterAgentRequest) returns (RegisterAgentResponse);
  rpc UnregisterAgent(UnregisterAgentRequest) returns (Empty);
  rpc DiscoverAgents(DiscoverAgentsRequest) returns (DiscoverAgentsResponse);
  rpc GetAgentInfo(GetAgentInfoRequest) returns (AgentInfo);

  // Capability negotiation
  rpc GetServerCapabilities(Empty) returns (ServerCapabilities);
  rpc RequestCapabilities(CapabilityRequest) returns (CapabilityGrant);
  rpc RevokeCapabilities(CapabilityRevocation) returns (Empty);

  // Context management
  rpc CreateSession(CreateSessionRequest) returns (Session);
  rpc UpdateSessionContext(ContextDelta) returns (Empty);
  rpc CreateContextHandle(ContextHandleRequest) returns (ContextHandle);
  rpc ResolveContextHandle(ContextHandle) returns (ContextData);

  // Task streaming
  rpc StreamTask(TaskStreamRequest) returns (stream TaskEvent);
  rpc SubscribeTaskEvents(TaskSubscription) returns (stream TaskEvent);

  // P2P introduction
  rpc GetP2PEndpoint(P2PEndpointRequest) returns (P2PEndpoint);
  rpc EstablishP2PConnection(P2PHandshake) returns (P2PConnectionInfo);

  // Tool execution with approval
  rpc RequestToolExecution(ToolExecutionRequest) returns (stream ToolExecutionEvent);
  rpc ApproveToolExecution(ToolApproval) returns (Empty);
  rpc RejectToolExecution(ToolRejection) returns (Empty);
}
```

**Feature Flags** (ServerCapabilities):

```go
type ServerCapabilities struct {
    ProtocolVersion string
    Features        []string
    MaxAgents       int
    SupportedAuth   []AuthMethod
}

// Feature flag examples:
const (
    FeatureStreamingTasks    = "streaming_tasks"
    FeatureToolApproval      = "tool_approval"
    FeatureContextSharing    = "context_sharing"
    FeatureP2PMesh           = "p2p_mesh"
    FeatureEventSourcing     = "event_sourcing"
    FeaturePubSubBroadcast   = "pubsub_broadcast"
    FeatureDistributedTracing = "distributed_tracing"
)
```

### 3.2 LSP Bridge Plugin

**Architecture**: Plugin loaded by coordinator (`pkg/tool/plugin_loader.go`)

**Responsibilities**:
1. **LSP Server** - Listen on stdio, implement LSP lifecycle
2. **Protocol Translation** - LSP JSON-RPC ↔ gRPC
3. **Feature Negotiation** - Map LSP client capabilities to ACP features
4. **Streaming Adapter** - Convert gRPC streams to LSP notifications

**LSP Extensions** (custom methods):

```typescript
// Text-based Q&A (Phase 1 - lightweight)
interface AskRequest {
  question: string;
  context?: {
    activeFile?: string;
    selection?: Range;
    openFiles?: string[];
  };
}

interface AskResponse {
  answer: string;
  references?: CodeReference[];
}

// Task execution with streaming (Phase 2)
interface ExecuteTaskRequest {
  task: string;
  planId?: string;
  context?: SessionContext;
}

interface TaskProgressNotification {
  taskId: string;
  status: "pending" | "in_progress" | "completed" | "failed";
  progress?: number;
  message?: string;
  toolExecutions?: ToolExecution[];
}

// Tool approval prompts (Phase 3)
interface ToolApprovalRequest {
  tool: string;
  parameters: Record<string, any>;
  risk: "low" | "medium" | "high" | "destructive";
  agent: AgentInfo;
}

interface ToolApprovalResponse {
  approved: boolean;
  remember?: boolean; // Trust this tool+agent combo
}
```

**Implementation Phases**:

| Phase | Capabilities | LSP Features |
|-------|-------------|--------------|
| 1 - Lightweight | Text Q&A | Custom `buckley/ask` command |
| 2 - Streaming | Task progress | `$/buckley/taskProgress` notifications |
| 3 - Interactive | Tool approvals | `buckley/approveTool` requests |
| 4 - Deep Integration | Workflow control, multi-session | Full ACP feature set |

### 3.3 Event Sourcing Infrastructure

**Event Store Abstraction**:

```go
type EventStore interface {
    Append(ctx context.Context, streamID string, events []Event) error
    Read(ctx context.Context, streamID string, fromVersion int64) ([]Event, error)
    Subscribe(ctx context.Context, streamID string, handler EventHandler) error
    Snapshot(ctx context.Context, streamID string, state interface{}) error
    LoadSnapshot(ctx context.Context, streamID string) (interface{}, int64, error)
}

// Implementations
type SQLiteEventStore struct { /* uses pkg/storage */ }
type NATSEventStore struct { /* NATS JetStream */ }
type KafkaEventStore struct { /* Kafka topics */ }
```

**Event Types**:

```go
// Agent lifecycle events
type AgentRegisteredEvent struct {
    AgentID      string
    Capabilities []string
    Endpoint     string
    Timestamp    time.Time
}

type AgentUnregisteredEvent struct {
    AgentID   string
    Reason    string
    Timestamp time.Time
}

// Task events
type TaskCreatedEvent struct {
    TaskID    string
    PlanID    string
    AgentID   string
    Timestamp time.Time
}

type TaskProgressEvent struct {
    TaskID   string
    Progress int
    Message  string
    Timestamp time.Time
}

type TaskCompletedEvent struct {
    TaskID    string
    Result    interface{}
    Timestamp time.Time
}

// Context events
type ContextHandleCreatedEvent struct {
    HandleID string
    Type     ContextType
    Size     int64
    Timestamp time.Time
}

type SessionContextUpdatedEvent struct {
    SessionID string
    Delta     ContextDelta
    Timestamp time.Time
}

// Capability events
type CapabilityGrantedEvent struct {
    GrantID     string
    AgentID     string
    Capabilities []string
    ExpiresAt   time.Time
    Timestamp   time.Time
}
```

**Replay Engine**:

```go
type ReplayEngine struct {
    store EventStore
    handlers map[string]EventHandler
}

func (e *ReplayEngine) RebuildState(ctx context.Context) error {
    // Read all events from store
    events, err := e.store.Read(ctx, "coordinator", 0)
    if err != nil {
        return err
    }

    // Replay events to rebuild state
    for _, event := range events {
        handler := e.handlers[event.Type]
        if err := handler(ctx, event); err != nil {
            return fmt.Errorf("replay failed at event %d: %w", event.Version, err)
        }
    }

    return nil
}
```

### 3.4 Pub/Sub Layer

**Topic-Based Broadcasting**:

```go
type PubSub interface {
    Publish(ctx context.Context, topic string, msg interface{}) error
    Subscribe(ctx context.Context, topic string, handler MessageHandler) (Subscription, error)
    Unsubscribe(ctx context.Context, sub Subscription) error
}

// Topic patterns
const (
    TopicTaskProgress     = "task.progress.{planID}.{taskID}"
    TopicAgentLifecycle   = "agent.{event}.{agentID}"
    TopicTelemetry        = "telemetry.{category}"
    TopicToolExecution    = "tool.{event}.{agentID}"
)

// Implementations
type InMemoryPubSub struct { /* local dev */ }
type NATSPubSub struct { /* K8s with NATS */ }
type RedisPubSub struct { /* K8s with Redis */ }
```

**Integration with gRPC Streams**:

```go
// When client subscribes to task events
func (s *Server) SubscribeTaskEvents(req *TaskSubscription, stream grpc.ServerStreamingServer) error {
    topic := fmt.Sprintf("task.progress.%s.*", req.PlanID)

    sub, err := s.pubsub.Subscribe(stream.Context(), topic, func(msg interface{}) {
        event := msg.(*TaskEvent)
        stream.Send(event)
    })
    defer s.pubsub.Unsubscribe(stream.Context(), sub)

    <-stream.Context().Done()
    return nil
}
```

### 3.5 P2P Mesh Communication

**Introduction Protocol**:

```
1. Agent A requests P2P endpoint for Agent B
   A → Coordinator: GetP2PEndpoint(agentID=B)

2. Coordinator validates A has capability to contact B
   Coordinator checks: A.capabilities includes "p2p_mesh"

3. Coordinator returns B's endpoint + temporary token
   Coordinator → A: P2PEndpoint{addr="agent-b:50051", token="..."}

4. A establishes direct connection to B
   A → B: EstablishP2PConnection(token="...")

5. B validates token with Coordinator
   B → Coordinator: ValidateP2PToken(token="...")

6. Coordinator confirms, returns A's metadata
   Coordinator → B: TokenValid{agentID=A, capabilities=[...]}

7. A and B communicate directly
   A ↔ B: Direct gRPC calls (no coordinator involvement)
```

**Circuit Breaker Integration**:

```go
type P2PClient struct {
    conn    *grpc.ClientConn
    breaker *CircuitBreaker
}

func (c *P2PClient) SendContext(ctx context.Context, data []byte) error {
    return c.breaker.Execute(func() error {
        _, err := c.conn.SendContext(ctx, &ContextPayload{Data: data})
        return err
    })
}

// Circuit breaker config per P2P connection
type CircuitBreakerConfig struct {
    MaxFailures   int           // Open after N failures
    Timeout       time.Duration // How long to wait before retry
    SuccessThreshold int        // Successes needed to close
}
```

### 3.6 Capability System

**Capability Definitions**:

```go
type Capability string

const (
    CapReadFiles        Capability = "read_files"
    CapWriteFiles       Capability = "write_files"
    CapExecuteTools     Capability = "execute_tools"
    CapExecuteShell     Capability = "execute_shell"
    CapSpawnAgents      Capability = "spawn_agents"
    CapP2PMesh          Capability = "p2p_mesh"
    CapContextSharing   Capability = "context_sharing"
    CapStreamingTasks   Capability = "streaming_tasks"
)

type CapabilityGrant struct {
    GrantID      string
    AgentID      string
    Capabilities []Capability
    IssuedAt     time.Time
    ExpiresAt    time.Time
    Context      GrantContext
}

type GrantContext struct {
    // Restrictions
    AllowedPaths  []string // For file operations
    AllowedTools  []string // For tool execution
    MaxAgents     int      // For spawn_agents

    // Approval mode
    ApprovalMode  ApprovalMode
}

type ApprovalMode string

const (
    ApprovalAutomatic  ApprovalMode = "automatic"  // Sandbox mode
    ApprovalTrustBased ApprovalMode = "trust"      // Remember trusted combos
    ApprovalAlways     ApprovalMode = "always"     // Always prompt
)
```

**Context-Aware Tool Approval**:

```go
type ToolApprovalStrategy interface {
    ShouldApprove(ctx context.Context, req *ToolExecutionRequest) (ApprovalDecision, error)
}

type ApprovalDecision struct {
    Approved  bool
    Reason    string
    Prompt    *UserPrompt // If user input needed
}

// Trust-based strategy
type TrustBasedApproval struct {
    trustStore TrustStore
}

func (s *TrustBasedApproval) ShouldApprove(ctx context.Context, req *ToolExecutionRequest) (ApprovalDecision, error) {
    // Check if this tool+agent combo is trusted
    key := fmt.Sprintf("%s:%s", req.AgentID, req.Tool)
    if s.trustStore.IsTrusted(key) {
        return ApprovalDecision{Approved: true, Reason: "trusted"}, nil
    }

    // Check risk level
    risk := assessRisk(req.Tool, req.Parameters)
    if risk == RiskLow {
        return ApprovalDecision{Approved: true, Reason: "low-risk"}, nil
    }

    // Prompt user
    return ApprovalDecision{
        Approved: false,
        Prompt: &UserPrompt{
            Message: fmt.Sprintf("Agent %s wants to execute %s", req.AgentID, req.Tool),
            Options: []string{"Approve", "Approve & Trust", "Deny"},
        },
    }, nil
}

// Sandbox strategy
type SandboxApproval struct {}

func (s *SandboxApproval) ShouldApprove(ctx context.Context, req *ToolExecutionRequest) (ApprovalDecision, error) {
    // In sandbox, everything is approved
    return ApprovalDecision{Approved: true, Reason: "sandbox"}, nil
}
```

### 3.7 Service Discovery

**Discovery Protocol**:

```go
type DiscoveryService interface {
    Register(ctx context.Context, service ServiceInfo) error
    Unregister(ctx context.Context, serviceID string) error
    Discover(ctx context.Context, query DiscoveryQuery) ([]ServiceInfo, error)
    Watch(ctx context.Context, query DiscoveryQuery) (<-chan ServiceEvent, error)
}

type ServiceInfo struct {
    ID           string
    Type         ServiceType // "coordinator", "agent", "lsp_bridge"
    Endpoint     string
    Capabilities []string
    Metadata     map[string]string
    Health       HealthStatus
}

type DiscoveryQuery struct {
    Type         ServiceType
    Capabilities []string // Must have ALL these capabilities
    Tags         map[string]string
}

// Implementations
type DNSDiscovery struct { /* DNS SRV records */ }
type ConsulDiscovery struct { /* HashiCorp Consul */ }
type EtcdDiscovery struct { /* etcd v3 */ }
type K8sDiscovery struct { /* Kubernetes service discovery */ }
```

**Health Checking**:

```go
type HealthChecker struct {
    discovery DiscoveryService
    interval  time.Duration
}

func (h *HealthChecker) Start(ctx context.Context) {
    ticker := time.NewTicker(h.interval)
    defer ticker.Stop()

    for {
        select {
        case <-ticker.C:
            services, _ := h.discovery.Discover(ctx, DiscoveryQuery{})
            for _, svc := range services {
                if !h.checkHealth(svc) {
                    h.discovery.Unregister(ctx, svc.ID)
                }
            }
        case <-ctx.Done():
            return
        }
    }
}
```

### 3.8 Observability Stack

**OpenTelemetry Integration**:

```go
type Tracer struct {
    provider trace.TracerProvider
    tracer   trace.Tracer
}

// Trace agent-to-agent communication
func (c *Coordinator) ExecuteTask(ctx context.Context, req *ExecuteTaskRequest) error {
    ctx, span := c.tracer.Start(ctx, "coordinator.execute_task",
        trace.WithAttributes(
            attribute.String("task.id", req.TaskID),
            attribute.String("agent.id", req.AgentID),
        ),
    )
    defer span.End()

    // Find agent
    agent, err := c.findAgent(ctx, req.AgentID)
    if err != nil {
        span.RecordError(err)
        return err
    }

    // Delegate to agent (trace propagates via gRPC metadata)
    result, err := agent.Execute(ctx, req)
    if err != nil {
        span.RecordError(err)
        return err
    }

    span.SetAttributes(attribute.String("result.status", result.Status))
    return nil
}
```

**Prometheus Metrics**:

```go
var (
    activeAgents = promauto.NewGauge(prometheus.GaugeOpts{
        Name: "buckley_acp_active_agents",
        Help: "Number of currently registered agents",
    })

    taskQueueDepth = promauto.NewGaugeVec(prometheus.GaugeOpts{
        Name: "buckley_acp_task_queue_depth",
        Help: "Number of pending tasks per agent",
    }, []string{"agent_id"})

    messageRate = promauto.NewCounterVec(prometheus.CounterOpts{
        Name: "buckley_acp_messages_total",
        Help: "Total messages sent",
    }, []string{"type", "source", "destination"})

    p2pConnectionsActive = promauto.NewGauge(prometheus.GaugeOpts{
        Name: "buckley_acp_p2p_connections_active",
        Help: "Number of active P2P connections",
    })

    circuitBreakerState = promauto.NewGaugeVec(prometheus.GaugeOpts{
        Name: "buckley_acp_circuit_breaker_state",
        Help: "Circuit breaker state (0=closed, 1=open, 2=half-open)",
    }, []string{"agent_id", "target"})
)
```

**Event Stream UI** (WebSocket):

```go
// Extends existing pkg/ipc/server.go
func (s *Server) handleEventStream(w http.ResponseWriter, r *http.Request) {
    conn, err := s.upgrader.Upgrade(w, r, nil)
    if err != nil {
        return
    }
    defer conn.Close()

    // Subscribe to all telemetry topics
    sub, err := s.pubsub.Subscribe(r.Context(), "telemetry.*", func(msg interface{}) {
        conn.WriteJSON(msg)
    })
    defer s.pubsub.Unsubscribe(r.Context(), sub)

    <-r.Context().Done()
}

// Event types for UI
type AgentActivityEvent struct {
    Timestamp time.Time
    AgentID   string
    Activity  string // "registered", "task_started", "tool_executed", etc.
    Metadata  map[string]interface{}
}

type TaskFlowEvent struct {
    Timestamp time.Time
    PlanID    string
    TaskID    string
    Flow      string // "created → assigned → in_progress → completed"
}

type MessageFlowEvent struct {
    Timestamp   time.Time
    MessageID   string
    Source      string
    Destination string
    Route       string // "coordinator" or "p2p"
    SizeBytes   int
}
```

**Structured Logging**:

```go
type Logger struct {
    logger *zap.Logger
}

func (l *Logger) LogAgentRegistration(agentID string, capabilities []string) {
    l.logger.Info("agent registered",
        zap.String("agent_id", agentID),
        zap.Strings("capabilities", capabilities),
        zap.String("component", "coordinator"),
        zap.String("event_type", "agent_lifecycle"),
    )
}

func (l *Logger) LogP2PConnection(source, destination string, success bool) {
    l.logger.Info("p2p connection established",
        zap.String("source", source),
        zap.String("destination", destination),
        zap.Bool("success", success),
        zap.String("component", "p2p_mesh"),
        zap.String("event_type", "connection"),
    )
}
```

---

## 4. Error Handling & Reliability

### 4.1 Multi-Layered Approach

**Layer 1: Automatic Retry with Backoff**

```go
type RetryStrategy struct {
    MaxRetries int
    BaseDelay  time.Duration
    MaxDelay   time.Duration
    Multiplier float64
}

func (s *RetryStrategy) Execute(ctx context.Context, fn func() error) error {
    var lastErr error
    delay := s.BaseDelay

    for attempt := 0; attempt <= s.MaxRetries; attempt++ {
        if attempt > 0 {
            select {
            case <-time.After(delay):
            case <-ctx.Done():
                return ctx.Err()
            }
            delay = time.Duration(float64(delay) * s.Multiplier)
            if delay > s.MaxDelay {
                delay = s.MaxDelay
            }
        }

        if err := fn(); err == nil {
            return nil
        } else if !isRetriable(err) {
            return err
        } else {
            lastErr = err
        }
    }

    return fmt.Errorf("max retries exceeded: %w", lastErr)
}

func isRetriable(err error) bool {
    // Network errors, timeouts, rate limits are retriable
    // Business logic errors, auth failures are not
    if errors.Is(err, context.DeadlineExceeded) {
        return true
    }
    if errors.Is(err, context.Canceled) {
        return false
    }
    // Check for gRPC status codes
    st, ok := status.FromError(err)
    if !ok {
        return false
    }
    switch st.Code() {
    case codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted:
        return true
    default:
        return false
    }
}
```

**Layer 2: Circuit Breaker**

```go
type CircuitBreaker struct {
    name         string
    maxFailures  int
    timeout      time.Duration
    successThreshold int

    mu           sync.RWMutex
    state        CircuitState
    failures     int
    successes    int
    lastFailTime time.Time
}

type CircuitState int

const (
    CircuitClosed CircuitState = iota
    CircuitOpen
    CircuitHalfOpen
)

func (cb *CircuitBreaker) Execute(fn func() error) error {
    if !cb.canExecute() {
        return ErrCircuitOpen
    }

    err := fn()
    cb.recordResult(err)
    return err
}

func (cb *CircuitBreaker) canExecute() bool {
    cb.mu.RLock()
    defer cb.mu.RUnlock()

    switch cb.state {
    case CircuitClosed:
        return true
    case CircuitOpen:
        // Check if timeout has elapsed
        if time.Since(cb.lastFailTime) > cb.timeout {
            cb.mu.RUnlock()
            cb.mu.Lock()
            cb.state = CircuitHalfOpen
            cb.successes = 0
            cb.mu.Unlock()
            cb.mu.RLock()
            return true
        }
        return false
    case CircuitHalfOpen:
        return true
    }
    return false
}

func (cb *CircuitBreaker) recordResult(err error) {
    cb.mu.Lock()
    defer cb.mu.Unlock()

    if err != nil {
        cb.failures++
        cb.lastFailTime = time.Now()
        if cb.failures >= cb.maxFailures {
            cb.state = CircuitOpen
            // Emit metric
            circuitBreakerState.WithLabelValues(cb.name, "target").Set(1)
        }
    } else {
        cb.successes++
        if cb.state == CircuitHalfOpen && cb.successes >= cb.successThreshold {
            cb.state = CircuitClosed
            cb.failures = 0
            // Emit metric
            circuitBreakerState.WithLabelValues(cb.name, "target").Set(0)
        }
    }
}
```

**Layer 3: Graceful Degradation**

```go
type DegradationManager struct {
    services map[string]ServiceStatus
    mu       sync.RWMutex
}

type ServiceStatus struct {
    Available    bool
    Capabilities []string
    DegradedMode bool
}

func (dm *DegradationManager) ExecutePlan(ctx context.Context, plan *Plan) error {
    // Check which services are available
    reviewAvailable := dm.isServiceAvailable("code_review")
    testAvailable := dm.isServiceAvailable("test_runner")

    for _, task := range plan.Tasks {
        // Execute core task
        if err := dm.executeTask(ctx, task); err != nil {
            return err
        }

        // Optional review step
        if task.RequiresReview && reviewAvailable {
            if err := dm.reviewTask(ctx, task); err != nil {
                log.Warn("review failed, continuing without review", zap.Error(err))
                // Don't fail the plan, just skip review
            }
        } else if task.RequiresReview {
            log.Warn("code review service unavailable, skipping review")
        }

        // Optional test step
        if task.RequiresTesting && testAvailable {
            if err := dm.testTask(ctx, task); err != nil {
                log.Warn("testing failed, continuing without tests", zap.Error(err))
            }
        } else if task.RequiresTesting {
            log.Warn("test runner unavailable, skipping tests")
        }
    }

    return nil
}

func (dm *DegradationManager) isServiceAvailable(service string) bool {
    dm.mu.RLock()
    defer dm.mu.RUnlock()
    status, exists := dm.services[service]
    return exists && status.Available
}
```

### 4.2 Error Propagation

**Error Context Chain**:

```go
type ACPError struct {
    Code      ErrorCode
    Message   string
    Source    string
    Timestamp time.Time
    Cause     error
    Metadata  map[string]interface{}
}

type ErrorCode string

const (
    ErrAgentNotFound       ErrorCode = "AGENT_NOT_FOUND"
    ErrCapabilityDenied    ErrorCode = "CAPABILITY_DENIED"
    ErrContextInvalid      ErrorCode = "CONTEXT_INVALID"
    ErrToolExecutionFailed ErrorCode = "TOOL_EXECUTION_FAILED"
    ErrCircuitOpen         ErrorCode = "CIRCUIT_OPEN"
    ErrP2PConnectionFailed ErrorCode = "P2P_CONNECTION_FAILED"
)

func (e *ACPError) Error() string {
    return fmt.Sprintf("[%s] %s: %s (source: %s)", e.Code, e.Message, e.Cause, e.Source)
}

func (e *ACPError) Unwrap() error {
    return e.Cause
}

// Wrap errors with context as they propagate
func wrapError(err error, code ErrorCode, source string, msg string) error {
    if err == nil {
        return nil
    }
    return &ACPError{
        Code:      code,
        Message:   msg,
        Source:    source,
        Timestamp: time.Now(),
        Cause:     err,
        Metadata:  make(map[string]interface{}),
    }
}
```

---

## 5. Security Model

### 5.1 Authentication Flow

**Token-Based Authentication** (extends `pkg/ipc/server.go` auth):

```
1. Client connects to coordinator
   Client → Coordinator: Connect()

2. Coordinator challenges client
   Coordinator → Client: AuthChallenge{methods: ["bearer", "mtls"]}

3. Client presents credentials
   Client → Coordinator: AuthResponse{method: "bearer", token: "..."}

4. Coordinator validates token
   - Check signature
   - Check expiration
   - Check revocation list

5. Coordinator issues session token
   Coordinator → Client: SessionToken{token: "...", expiresAt: "..."}

6. Client uses session token for subsequent requests
   Client → Coordinator: Request{sessionToken: "..."}
```

**Token Structure** (JWT):

```json
{
  "iss": "buckley-coordinator",
  "sub": "agent-12345",
  "aud": ["acp"],
  "exp": 1700000000,
  "iat": 1699999000,
  "capabilities": ["read_files", "execute_tools"],
  "context": {
    "approval_mode": "trust",
    "allowed_paths": ["/workspace/*"],
    "max_agents": 5
  }
}
```

### 5.2 Capability Grant Lifecycle

```go
type CapabilityManager struct {
    grants map[string]*CapabilityGrant
    mu     sync.RWMutex
}

func (cm *CapabilityManager) RequestCapabilities(ctx context.Context, req *CapabilityRequest) (*CapabilityGrant, error) {
    // Validate requester
    agentID := getAgentID(ctx)
    if agentID == "" {
        return nil, errors.New("unauthenticated")
    }

    // Check if requester is allowed to request these capabilities
    if !cm.canRequest(agentID, req.Capabilities) {
        return nil, wrapError(nil, ErrCapabilityDenied, "capability_manager",
            "agent not authorized to request these capabilities")
    }

    // For high-privilege capabilities, require user approval
    if requiresUserApproval(req.Capabilities) {
        approval, err := cm.requestUserApproval(ctx, agentID, req.Capabilities)
        if err != nil || !approval {
            return nil, wrapError(err, ErrCapabilityDenied, "capability_manager",
                "user denied capability request")
        }
    }

    // Issue time-limited grant
    grant := &CapabilityGrant{
        GrantID:      generateID(),
        AgentID:      agentID,
        Capabilities: req.Capabilities,
        IssuedAt:     time.Now(),
        ExpiresAt:    time.Now().Add(req.Duration),
        Context:      req.Context,
    }

    cm.mu.Lock()
    cm.grants[grant.GrantID] = grant
    cm.mu.Unlock()

    // Emit event
    cm.eventStore.Append(ctx, "coordinator", []Event{
        &CapabilityGrantedEvent{
            GrantID:      grant.GrantID,
            AgentID:      agentID,
            Capabilities: req.Capabilities,
            ExpiresAt:    grant.ExpiresAt,
            Timestamp:    time.Now(),
        },
    })

    return grant, nil
}

func (cm *CapabilityManager) ValidateCapability(ctx context.Context, agentID string, capability Capability) error {
    cm.mu.RLock()
    defer cm.mu.RUnlock()

    for _, grant := range cm.grants {
        if grant.AgentID == agentID && time.Now().Before(grant.ExpiresAt) {
            for _, cap := range grant.Capabilities {
                if cap == capability {
                    return nil
                }
            }
        }
    }

    return wrapError(nil, ErrCapabilityDenied, "capability_manager",
        fmt.Sprintf("agent %s does not have capability %s", agentID, capability))
}
```

### 5.3 P2P Security

**Token-Based P2P Introduction**:

```go
func (c *Coordinator) GetP2PEndpoint(ctx context.Context, req *P2PEndpointRequest) (*P2PEndpoint, error) {
    // Validate requester has p2p_mesh capability
    if err := c.capabilityMgr.ValidateCapability(ctx, req.RequesterID, CapP2PMesh); err != nil {
        return nil, err
    }

    // Find target agent
    target, err := c.findAgent(ctx, req.TargetAgentID)
    if err != nil {
        return nil, wrapError(err, ErrAgentNotFound, "coordinator",
            "target agent not found")
    }

    // Issue temporary P2P token
    token := &P2PToken{
        TokenID:     generateID(),
        RequesterID: req.RequesterID,
        TargetID:    req.TargetAgentID,
        IssuedAt:    time.Now(),
        ExpiresAt:   time.Now().Add(5 * time.Minute), // Short-lived
    }

    c.p2pTokens.Store(token.TokenID, token)

    return &P2PEndpoint{
        Address: target.Endpoint,
        Token:   token.TokenID,
    }, nil
}

func (a *Agent) EstablishP2PConnection(ctx context.Context, req *P2PHandshake) (*P2PConnectionInfo, error) {
    // Validate token with coordinator
    validation, err := a.coordinator.ValidateP2PToken(ctx, req.Token)
    if err != nil {
        return nil, err
    }

    // Accept connection from requester
    conn := &P2PConnection{
        RemoteAgentID: validation.RequesterID,
        Capabilities:  validation.RequesterCapabilities,
        EstablishedAt: time.Now(),
    }

    a.p2pConnections.Store(validation.RequesterID, conn)

    return &P2PConnectionInfo{
        ConnectionID: conn.ID,
        Capabilities: a.capabilities,
    }, nil
}
```

---

## 6. Deployment Scenarios

### 6.1 Local Development

**Configuration**:
```yaml
# .buckley/config.yaml
acp:
  mode: local
  coordinator:
    address: 127.0.0.1:50052
  event_store:
    type: sqlite
    path: .buckley/acp_events.db
  pubsub:
    type: in_memory
  discovery:
    type: static
    services:
      - id: coordinator
        endpoint: 127.0.0.1:50052
  observability:
    tracing:
      enabled: false
    metrics:
      enabled: true
      port: 9090
```

**Architecture**:
```
┌──────────────────────────────────────┐
│  Buckley Process                     │
│  ┌────────────┐  ┌────────────┐     │
│  │Coordinator │  │LSP Bridge  │     │
│  │            │  │Plugin      │     │
│  └────────────┘  └────────────┘     │
│  ┌────────────┐  ┌────────────┐     │
│  │SQLite Event│  │In-Memory   │     │
│  │Store       │  │PubSub      │     │
│  └────────────┘  └────────────┘     │
└──────────────────────────────────────┘
           ↕ LSP stdio
    ┌──────────────┐
    │ Zed Editor   │
    └──────────────┘
```

### 6.2 Kubernetes Production

**Configuration**:
```yaml
# kubernetes/configmap.yaml
acp:
  mode: production
  coordinator:
    replicas: 3
    address: buckley-coordinator.default.svc.cluster.local:50052
  event_store:
    type: nats
    servers:
      - nats://nats.default.svc.cluster.local:4222
    stream: buckley_events
  pubsub:
    type: nats
    servers:
      - nats://nats.default.svc.cluster.local:4222
  discovery:
    type: kubernetes
    namespace: default
    label_selector: app=buckley-agent
  observability:
    tracing:
      enabled: true
      endpoint: jaeger-collector.observability.svc.cluster.local:4317
    metrics:
      enabled: true
      port: 9090
```

**Architecture**:
```
┌────────────────────────────────────────────────────────┐
│  Kubernetes Cluster                                    │
│                                                        │
│  ┌──────────────────────┐  ┌──────────────────────┐  │
│  │ Coordinator Pod (x3) │  │ NATS Cluster         │  │
│  │ - gRPC Server        │  │ - Event Store        │  │
│  │ - LSP Bridge         │  │ - Pub/Sub            │  │
│  │ - Circuit Breakers   │  │                      │  │
│  └──────────────────────┘  └──────────────────────┘  │
│           ↕                          ↕                │
│  ┌──────────────────────┐  ┌──────────────────────┐  │
│  │ Agent Pool (scaling) │  │ Observability        │  │
│  │ - Builder Agents     │  │ - Jaeger (tracing)   │  │
│  │ - Review Agents      │  │ - Prometheus         │  │
│  │ - Research Agents    │  │ - Grafana            │  │
│  └──────────────────────┘  └──────────────────────┘  │
│           ↕                                           │
│  ┌──────────────────────┐                            │
│  │ Ingress              │                            │
│  │ - gRPC (50052)       │                            │
│  │ - LSP (stdio/TCP)    │                            │
│  └──────────────────────┘                            │
└────────────────────────────────────────────────────────┘
           ↕ gRPC/LSP
    ┌──────────────┐
    │ External     │
    │ Clients      │
    │ (Zed, etc)   │
    └──────────────┘
```

**Deployment**:
```yaml
# kubernetes/deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: buckley-coordinator
spec:
  replicas: 3
  selector:
    matchLabels:
      app: buckley-coordinator
  template:
    metadata:
      labels:
        app: buckley-coordinator
    spec:
      containers:
      - name: coordinator
        image: buckley:latest
        command: ["buckley", "coordinator", "--config", "/etc/buckley/config.yaml"]
        ports:
        - containerPort: 50052
          name: grpc
        - containerPort: 9090
          name: metrics
        env:
        - name: OTEL_EXPORTER_OTLP_ENDPOINT
          value: "jaeger-collector.observability.svc.cluster.local:4317"
        - name: NATS_URL
          value: "nats://nats.default.svc.cluster.local:4222"
        volumeMounts:
        - name: config
          mountPath: /etc/buckley
      volumes:
      - name: config
        configMap:
          name: buckley-config
---
apiVersion: v1
kind: Service
metadata:
  name: buckley-coordinator
spec:
  selector:
    app: buckley-coordinator
  ports:
  - port: 50052
    name: grpc
  - port: 9090
    name: metrics
```

### 6.3 Hybrid (Local Dev + Cloud Agents)

**Configuration**:
```yaml
# .buckley/config.yaml
acp:
  mode: hybrid
  coordinator:
    # Local coordinator
    address: 127.0.0.1:50052
  agents:
    # Can discover cloud agents
    discovery:
      type: multi
      backends:
        - type: static  # Local agents
          services:
            - id: local-agent-1
              endpoint: 127.0.0.1:50053
        - type: kubernetes  # Cloud agents
          endpoint: https://k8s.example.com
          namespace: buckley-agents
  event_store:
    type: sqlite  # Local events
    path: .buckley/acp_events.db
  pubsub:
    type: hybrid
    local: in_memory
    remote:
      type: nats
      servers:
        - nats://nats.example.com:4222
```

---

## 7. Implementation Roadmap

### 7.1 Component Build Order (Big Bang)

All components built in parallel, integrated continuously:

**Week 1-2: Foundation**
- [ ] gRPC protocol extensions (RPCs, messages)
- [ ] Feature flag system (ServerCapabilities)
- [ ] Event store abstraction (interface + SQLite impl)
- [ ] Basic coordinator structure

**Week 3-4: Core Services**
- [ ] Agent registration/discovery
- [ ] Capability system (grants, validation)
- [ ] Context management (sessions + handles)
- [ ] Pub/Sub layer (in-memory impl)

**Week 5-6: Communication**
- [ ] gRPC bidirectional streaming
- [ ] P2P mesh (introduction protocol)
- [ ] Circuit breakers
- [ ] Retry/backoff strategies

**Week 7-8: LSP Integration**
- [ ] LSP bridge plugin
- [ ] LSP ↔ gRPC translation
- [ ] Phase 1: Text Q&A (buckley/ask)
- [ ] Phase 2: Streaming (task progress notifications)

**Week 9-10: Production Features**
- [ ] Service discovery (DNS/Consul/K8s)
- [ ] NATS event store implementation
- [ ] NATS pub/sub implementation
- [ ] Health checking

**Week 11-12: Observability**
- [ ] OpenTelemetry instrumentation
- [ ] Prometheus metrics
- [ ] Event stream WebSocket endpoint
- [ ] Structured logging

**Week 13-14: Security Hardening**
- [ ] Token-based auth
- [ ] P2P token validation
- [ ] Tool approval strategies
- [ ] Security audit

**Week 15-16: Testing & Documentation**
- [ ] Integration tests
- [ ] Load testing (agent swarm scenarios)
- [ ] API documentation
- [ ] Deployment guides

### 7.2 Testing Strategy

**Unit Tests**:
- Each component tested in isolation
- Mock dependencies (event store, pub/sub, discovery)
- Target: 80%+ coverage

**Integration Tests**:
```go
func TestEndToEndFlow(t *testing.T) {
    // 1. Start coordinator
    coord := startTestCoordinator(t)
    defer coord.Stop()

    // 2. Register agent
    agent := registerTestAgent(t, coord, []Capability{CapExecuteTools})

    // 3. Zed client connects via LSP bridge
    lspClient := connectLSPClient(t, coord)

    // 4. Ask question
    resp, err := lspClient.Ask("What files are in the project?")
    require.NoError(t, err)

    // 5. Verify agent executed tool
    assert.Contains(t, resp.Answer, "README.md")

    // 6. Check telemetry
    events := coord.GetTelemetryEvents()
    assert.Contains(t, events, "tool.executed")
}

func TestAgentSwarm(t *testing.T) {
    coord := startTestCoordinator(t)
    defer coord.Stop()

    // Register 10 agents
    agents := make([]*TestAgent, 10)
    for i := 0; i < 10; i++ {
        agents[i] = registerTestAgent(t, coord, []Capability{CapP2PMesh})
    }

    // Agent 0 requests P2P with all others
    for i := 1; i < 10; i++ {
        endpoint, err := agents[0].GetP2PEndpoint(agents[i].ID)
        require.NoError(t, err)

        conn, err := agents[0].ConnectP2P(endpoint)
        require.NoError(t, err)

        // Send large context payload
        err = conn.SendContext(makeLargeContext(10 * 1024 * 1024))
        require.NoError(t, err)
    }

    // Verify all connections active
    assert.Equal(t, 9, agents[0].ActiveP2PConnections())
}

func TestCircuitBreaker(t *testing.T) {
    coord := startTestCoordinator(t)
    defer coord.Stop()

    agent1 := registerTestAgent(t, coord, []Capability{CapP2PMesh})
    agent2 := registerFlakyAgent(t, coord, 0.8) // 80% failure rate

    endpoint, _ := agent1.GetP2PEndpoint(agent2.ID)

    // Make requests until circuit opens
    var circuitOpened bool
    for i := 0; i < 20; i++ {
        err := agent1.SendP2PMessage(endpoint, "test")
        if errors.Is(err, ErrCircuitOpen) {
            circuitOpened = true
            break
        }
    }

    assert.True(t, circuitOpened, "circuit should have opened after failures")

    // Verify metric
    metric := getMetric(t, "buckley_acp_circuit_breaker_state")
    assert.Equal(t, 1.0, metric) // 1 = open
}
```

**Load Tests**:
```go
func BenchmarkAgentSwarm(b *testing.B) {
    coord := startBenchCoordinator(b)
    defer coord.Stop()

    // Register 100 agents
    agents := make([]*TestAgent, 100)
    for i := 0; i < 100; i++ {
        agents[i] = registerTestAgent(b, coord, []Capability{CapP2PMesh, CapExecuteTools})
    }

    b.ResetTimer()

    // Each agent executes 100 tasks in parallel
    var wg sync.WaitGroup
    for _, agent := range agents {
        wg.Add(1)
        go func(a *TestAgent) {
            defer wg.Done()
            for j := 0; j < 100; j++ {
                a.ExecuteTask("task-" + strconv.Itoa(j))
            }
        }(agent)
    }
    wg.Wait()
}
```

---

## 8. Open Questions & Risks

### 8.1 Open Questions

**Protocol Evolution**:
- Q: How do we handle feature flag deprecation?
- A: Coordinator advertises `deprecated_features` list with sunset dates. Clients can plan migrations.

**Context Size Limits**:
- Q: What's the max context size for P2P transfers?
- A: Start with 100MB limit. Use chunking for larger contexts (streaming file transfers).

**Agent Lifecycle**:
- Q: What happens if an agent crashes mid-task?
- A: Event sourcing allows coordinator to detect failure (missed heartbeat), reassign task to another agent. Original agent can resume from last checkpoint if it restarts.

**Multi-Tenancy**:
- Q: Can one coordinator support multiple teams/projects?
- A: Yes, via namespace isolation. Each team gets a namespace, agents can only discover/communicate within their namespace.

**LSP Compatibility**:
- Q: Which LSP version should we target?
- A: LSP 3.17 (latest stable). Custom extensions use `$/buckley/*` namespace.

### 8.2 Risks

**Risk: Event store scalability**
- **Impact**: High event volume could overwhelm SQLite in production
- **Mitigation**: Use NATS/Kafka in production, implement event compaction/archival

**Risk: P2P mesh connectivity**
- **Impact**: Agents behind NAT/firewalls can't establish P2P connections
- **Mitigation**: Fall back to coordinator-routed messages if P2P fails. Add TURN server support for NAT traversal.

**Risk: Circuit breaker tuning**
- **Impact**: Overly aggressive breakers cause false positives, too lenient allows cascades
- **Mitigation**: Make breaker configs tunable per agent type. Monitor breaker state metrics, adjust thresholds based on production data.

**Risk: Tool approval UX**
- **Impact**: Too many approval prompts annoy users, too few create security risks
- **Mitigation**: Smart defaults (low-risk auto-approve), trust learning (remember approved combos), batch approvals (approve all tools for this task).

**Risk: Big bang complexity**
- **Impact**: Building all components together delays value delivery, increases integration risk
- **Mitigation**: Continuous integration (daily builds), comprehensive test suite, feature flags to toggle incomplete features.

---

## 9. Success Criteria

### 9.1 Phase 1: Zed Text Q&A (Lightweight)

**Criteria**:
- [ ] Zed extension connects to Buckley via LSP bridge
- [ ] User can ask questions in editor (e.g., "explain this function")
- [ ] Buckley responds with text answers
- [ ] Context includes active file, selection, open files
- [ ] Latency < 2s for simple questions
- [ ] No crashes or hangs during normal usage

### 9.2 Phase 2: Streaming Task Progress

**Criteria**:
- [ ] Zed can trigger Buckley workflows (e.g., "fix this bug")
- [ ] Task progress streams to editor in real-time
- [ ] UI shows active task, completed steps, pending steps
- [ ] Tool executions visible in progress stream
- [ ] User can cancel running tasks from editor
- [ ] Latency < 500ms for progress updates

### 9.3 Phase 3: Agent Swarm

**Criteria**:
- [ ] Coordinator can orchestrate 10+ agents
- [ ] Agents can discover each other via service discovery
- [ ] P2P connections establish successfully (>95% success rate)
- [ ] Circuit breakers prevent cascading failures
- [ ] Event sourcing enables crash recovery
- [ ] Pub/sub fan-out supports 20+ observers per task

### 9.4 Phase 4: Production Readiness

**Criteria**:
- [ ] Kubernetes deployment succeeds (3 coordinator replicas)
- [ ] NATS event store handles 1000+ events/sec
- [ ] OpenTelemetry traces show end-to-end request flows
- [ ] Prometheus dashboards visualize agent activity
- [ ] Security audit passes (no critical vulnerabilities)
- [ ] Load test: 100 agents × 100 tasks completes without errors

---

## 10. Conclusion

The Agent Communication Protocol provides a comprehensive foundation for:
1. Enabling rich Zed editor integration (primary goal)
2. Scaling to multi-agent orchestration (secondary goal)
3. Supporting future IDE integrations (extensibility goal)

**Key Strengths**:
- Leverages existing infrastructure (gRPC, SQLite, WebSocket)
- Feature flags enable progressive evolution
- Multi-layered reliability (retries, circuit breakers, degradation)
- Environment-aware (local dev vs production)
- Observable by design (logs, metrics, tracing, event stream)

**Next Steps**:
1. Review this design with stakeholders
2. Create detailed implementation plan (/plan)
3. Begin big bang implementation (Week 1: Foundation)
4. Continuous integration and testing throughout

---

## Appendix A: References

- **Existing Code**:
  - `pkg/sdk/grpc/server.go` - Current gRPC implementation
  - `pkg/ipc/server.go` - HTTP/WebSocket IPC server
  - `pkg/tool/` - Plugin system
  - `pkg/storage/` - SQLite persistence

- **External Protocols**:
  - LSP Specification: https://microsoft.github.io/language-server-protocol/
  - gRPC: https://grpc.io/docs/
  - JSON-RPC 2.0: https://www.jsonrpc.org/specification

- **Dependencies**:
  - OpenTelemetry Go: https://opentelemetry.io/docs/instrumentation/go/
  - Prometheus Client: https://prometheus.io/docs/guides/go-application/
  - NATS: https://docs.nats.io/
  - Circuit Breaker: https://github.com/sony/gobreaker

---

**End of Document**
