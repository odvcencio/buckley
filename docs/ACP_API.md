# ACP API Documentation

**Version**: 1.0.0
**Last Updated**: 2025-11-19
**Status**: Production Ready (with recommended hardening)

## Overview

The Agent Communication Protocol (ACP) provides a secure, observable, and scalable framework for inter-agent communication in Buckley. This document covers all public APIs across the ACP components.

## Table of Contents

1. [Authentication API](#authentication-api)
2. [Authorization API](#authorization-api)
3. [Event System API](#event-system-api)
4. [P2P Communication API](#p2p-communication-api)
5. [LSP Bridge API](#lsp-bridge-api)
6. [Observability API](#observability-api)
7. [Error Codes](#error-codes)
8. [gRPC Service Definitions](#grpc-service-definitions)

---

## Authentication API

**Package**: `github.com/odvcencio/buckley/pkg/acp/security`

### TokenManager

Manages JWT token lifecycle for agent authentication.

#### Constructor

```go
func NewTokenManager(secretKey string) *TokenManager
```

Creates a new token manager with the given HMAC secret key.

**Parameters**:
- `secretKey`: Secret key for HMAC-SHA256 signing (min 32 bytes recommended)

**Returns**: `*TokenManager`

#### GenerateToken

```go
func (tm *TokenManager) GenerateToken(
    agentID string,
    capabilities []string,
    duration time.Duration,
) (string, error)
```

Generates a new JWT token for an agent.

**Parameters**:
- `agentID`: Unique identifier for the agent
- `capabilities`: List of capability names (e.g., "code_execution", "file_access")
- `duration`: Token lifetime (recommended: 1 hour for production)

**Returns**: JWT token string

**Errors**:
- `fmt.Errorf("failed to generate token ID")`: Cryptographic RNG failure
- `fmt.Errorf("failed to sign token")`: JWT signing failure

**Example**:
```go
tm := NewTokenManager("my-secret-key")
token, err := tm.GenerateToken("agent-1", []string{"code_analysis"}, 1*time.Hour)
if err != nil {
    log.Fatalf("Failed to generate token: %v", err)
}
fmt.Printf("Token: %s\n", token)
```

#### ValidateToken

```go
func (tm *TokenManager) ValidateToken(tokenString string) (*Claims, error)
```

Validates a JWT token and returns the embedded claims.

**Parameters**:
- `tokenString`: JWT token to validate

**Returns**: `*Claims` containing agent ID and capabilities

**Errors**:
- `ErrInvalidToken`: Token is malformed or signature invalid
- `ErrExpiredToken`: Token has expired
- `ErrRevokedToken`: Token has been revoked

**Example**:
```go
claims, err := tm.ValidateToken(token)
if err != nil {
    if errors.Is(err, ErrExpiredToken) {
        log.Println("Token expired, please refresh")
    } else {
        log.Printf("Invalid token: %v", err)
    }
    return
}
fmt.Printf("Agent: %s, Capabilities: %v\n", claims.AgentID, claims.Capabilities)
```

#### RevokeToken

```go
func (tm *TokenManager) RevokeToken(tokenString string) error
```

Revokes a token by adding it to the revocation list.

**Parameters**:
- `tokenString`: JWT token to revoke

**Returns**: Error if token cannot be parsed

**Example**:
```go
err := tm.RevokeToken(oldToken)
if err != nil {
    log.Printf("Failed to revoke token: %v", err)
}
```

#### RefreshToken

```go
func (tm *TokenManager) RefreshToken(
    oldTokenString string,
    duration time.Duration,
) (string, error)
```

Generates a new token based on an existing valid token and revokes the old one.

**Parameters**:
- `oldTokenString`: Current valid token
- `duration`: Lifetime for the new token

**Returns**: New JWT token string

**Errors**:
- `ErrInvalidToken`: Old token is invalid
- `ErrExpiredToken`: Old token has expired
- `fmt.Errorf("failed to revoke old token")`: Revocation failed

**Example**:
```go
newToken, err := tm.RefreshToken(oldToken, 2*time.Hour)
if err != nil {
    log.Printf("Failed to refresh token: %v", err)
    return
}
fmt.Printf("New token: %s\n", newToken)
```

#### CleanupRevokedTokens

```go
func (tm *TokenManager) CleanupRevokedTokens()
```

Removes tokens revoked more than 24 hours ago from the revocation list.

**Usage**: Call periodically (e.g., via cron) to prevent memory growth.

**Example**:
```go
ticker := time.NewTicker(1 * time.Hour)
go func() {
    for range ticker.C {
        tm.CleanupRevokedTokens()
    }
}()
```

### Claims

JWT payload structure.

```go
type Claims struct {
    AgentID      string   `json:"agent_id"`
    Capabilities []string `json:"capabilities"`
    jwt.RegisteredClaims
}
```

**Fields**:
- `AgentID`: Unique agent identifier
- `Capabilities`: List of granted capabilities
- `RegisteredClaims`: Standard JWT claims (ID, Subject, IssuedAt, ExpiresAt, NotBefore)

### Context Helpers

#### ContextWithClaims

```go
func ContextWithClaims(ctx context.Context, claims *Claims) context.Context
```

Adds authentication claims to a context.

#### ClaimsFromContext

```go
func ClaimsFromContext(ctx context.Context) (*Claims, bool)
```

Extracts authentication claims from a context.

**Returns**: Claims and boolean indicating if claims were found

**Example**:
```go
claims, ok := ClaimsFromContext(ctx)
if !ok {
    return ErrInsufficientAuth
}
fmt.Printf("Authenticated as: %s\n", claims.AgentID)
```

#### RequireCapability

```go
func RequireCapability(ctx context.Context, capability string) error
```

Checks if the authenticated agent has a required capability.

**Parameters**:
- `ctx`: Context with embedded claims
- `capability`: Required capability name

**Returns**: `ErrNoCapability` if capability missing

**Example**:
```go
if err := RequireCapability(ctx, "code_execution"); err != nil {
    return status.Error(codes.PermissionDenied, "code execution not allowed")
}
```

### gRPC Interceptors

#### AuthInterceptor

```go
func NewAuthInterceptor(tokenManager *TokenManager) *AuthInterceptor
```

Creates a gRPC interceptor for authentication.

#### UnaryInterceptor

```go
func (ai *AuthInterceptor) UnaryInterceptor() grpc.UnaryServerInterceptor
```

Returns a gRPC unary server interceptor that validates tokens from metadata.

**Usage**:
```go
interceptor := NewAuthInterceptor(tokenManager)
server := grpc.NewServer(
    grpc.UnaryInterceptor(interceptor.UnaryInterceptor()),
    grpc.StreamInterceptor(interceptor.StreamInterceptor()),
)
```

#### StreamInterceptor

```go
func (ai *AuthInterceptor) StreamInterceptor() grpc.StreamServerInterceptor
```

Returns a gRPC stream server interceptor for authentication.

---

## Authorization API

**Package**: `github.com/odvcencio/buckley/pkg/acp/security`

### ToolPolicy

Maps capabilities to allowed tools.

#### Constructor

```go
func NewToolPolicy() *ToolPolicy
```

Creates an empty tool policy.

#### DefaultToolPolicy

```go
func DefaultToolPolicy() *ToolPolicy
```

Returns a pre-configured policy matching Buckley's builtin tools.

**Capabilities**:
- `admin`: All tools (*)
- `shell_execution`: shell
- `file_access`: file, terminal_editor
- `git_access`: git, merge
- `code_analysis`: search, semantic_search, code_index, navigation, quality
- `code_modification`: refactoring, terminal_editor, file
- `testing`: testing
- `documentation`: documentation
- `web_access`: browser
- `task_management`: todo
- `agent_orchestration`: delegate, skill_activation
- `data_operations`: excel
- `read_only`: search, semantic_search, code_index, navigation, quality, browser

**Example**:
```go
policy := DefaultToolPolicy()
```

#### AddRule

```go
func (tp *ToolPolicy) AddRule(capability string, tools []string)
```

Adds a policy rule allowing certain tools for a capability.

**Parameters**:
- `capability`: Capability name
- `tools`: List of tool names (use "*" for all tools)

**Example**:
```go
policy := NewToolPolicy()
policy.AddRule("admin", []string{"*"})
policy.AddRule("file_access", []string{"file", "terminal_editor"})
```

#### RemoveRule

```go
func (tp *ToolPolicy) RemoveRule(capability string)
```

Removes all tools for a capability.

#### IsToolAllowed

```go
func (tp *ToolPolicy) IsToolAllowed(capability string, tool string) bool
```

Checks if a single capability allows a tool.

**Returns**: `true` if allowed

**Example**:
```go
if policy.IsToolAllowed("code_analysis", "search") {
    fmt.Println("search is allowed for code_analysis")
}
```

#### IsToolAllowedForCapabilities

```go
func (tp *ToolPolicy) IsToolAllowedForCapabilities(
    capabilities []string,
    tool string,
) bool
```

Checks if any of the given capabilities allow a tool.

#### GetAllowedTools

```go
func (tp *ToolPolicy) GetAllowedTools(capability string) []string
```

Returns all tools allowed for a capability.

### ToolApprover

Enforces tool access policies for agents.

#### Constructor

```go
func NewToolApprover(policy *ToolPolicy) *ToolApprover
```

Creates a new tool approver with the given policy.

#### CheckToolAccess

```go
func (ta *ToolApprover) CheckToolAccess(ctx context.Context, tool string) error
```

Verifies that an agent can use a specific tool.

**Parameters**:
- `ctx`: Context with embedded authentication claims
- `tool`: Tool name to check

**Returns**: `ErrToolNotAllowed` if denied

**Side Effects**: Logs access attempt to audit log

**Example**:
```go
err := approver.CheckToolAccess(ctx, "shell")
if err != nil {
    return status.Error(codes.PermissionDenied, "shell access denied")
}
// Proceed with shell execution
```

#### GetAllowedToolsForAgent

```go
func (ta *ToolApprover) GetAllowedToolsForAgent(ctx context.Context) []string
```

Returns all tools an agent can use based on their capabilities.

**Returns**: List of tool names (or `["*"]` for admin)

**Example**:
```go
tools := approver.GetAllowedToolsForAgent(ctx)
fmt.Printf("Agent can use: %v\n", tools)
```

#### GetAuditLog

```go
func (ta *ToolApprover) GetAuditLog(agentID string, limit int) []AuditEntry
```

Returns recent audit entries for an agent.

**Parameters**:
- `agentID`: Agent identifier
- `limit`: Maximum number of entries to return

**Returns**: Audit entries (oldest to newest)

**Example**:
```go
entries := approver.GetAuditLog("agent-1", 100)
for _, entry := range entries {
    fmt.Printf("[%s] %s attempted %s: %v (%s)\n",
        entry.Timestamp, entry.AgentID, entry.ToolName, entry.Allowed, entry.Reason)
}
```

### AuditEntry

Records a tool access attempt.

```go
type AuditEntry struct {
    Timestamp time.Time
    AgentID   string
    ToolName  string
    Allowed   bool
    Reason    string
}
```

**Fields**:
- `Timestamp`: When the access was attempted
- `AgentID`: Agent making the attempt
- `ToolName`: Tool being accessed
- `Allowed`: Whether access was granted
- `Reason`: Why access was allowed or denied

---

## Event System API

**Package**: `github.com/odvcencio/buckley/pkg/acp/events`

### Event

Represents an event in the event stream.

```go
type Event struct {
    StreamID  string
    Type      string
    Data      json.RawMessage
    Metadata  map[string]string
    Timestamp time.Time
    Version   int
}
```

### EventStore

Interface for event persistence.

```go
type EventStore interface {
    Append(ctx context.Context, streamID string, events []Event) error
    Read(ctx context.Context, streamID string, fromVersion int) ([]Event, error)
    Subscribe(ctx context.Context, streamID string, fromVersion int) (<-chan Event, error)
    SaveSnapshot(ctx context.Context, streamID string, version int, state interface{}) error
    LoadSnapshot(ctx context.Context, streamID string) (int, interface{}, error)
}
```

#### SQLiteEventStore

```go
func NewSQLiteEventStore(dbPath string) (*SQLiteEventStore, error)
```

Creates an event store backed by SQLite.

**Example**:
```go
store, err := NewSQLiteEventStore("./events.db")
if err != nil {
    log.Fatalf("Failed to create event store: %v", err)
}
defer store.Close()
```

#### NATSEventStore

```go
func NewNATSEventStore(natsURL string) (*NATSEventStore, error)
```

Creates an event store backed by NATS JetStream.

**Example**:
```go
store, err := NewNATSEventStore("nats://localhost:4222")
if err != nil {
    log.Fatalf("Failed to connect to NATS: %v", err)
}
defer store.Close()
```

---

## P2P Communication API

**Package**: `github.com/odvcencio/buckley/pkg/acp/p2p`

### P2PClient

Client for peer-to-peer agent communication.

```go
type P2PClient struct {
    // fields omitted
}
```

#### Constructor

```go
func NewP2PClient(
    peerAddress string,
    token string,
    opts ...Option,
) (*P2PClient, error)
```

Creates a new P2P client.

**Parameters**:
- `peerAddress`: Target peer address (e.g., "localhost:50051")
- `token`: Authentication token
- `opts`: Optional configuration

**Returns**: `*P2PClient`

**Example**:
```go
client, err := NewP2PClient(
    "peer-agent:50051",
    authToken,
    WithCircuitBreaker(5, 30*time.Second),
)
if err != nil {
    log.Fatalf("Failed to create P2P client: %v", err)
}
defer client.Close()
```

#### Connect

```go
func (c *P2PClient) Connect(ctx context.Context) error
```

Establishes connection to the peer with token validation.

#### SendMessage

```go
func (c *P2PClient) SendMessage(
    ctx context.Context,
    message *Message,
) (*Response, error)
```

Sends a message to the peer through the circuit breaker.

**Returns**: `ErrCircuitOpen` if circuit breaker is open

#### Close

```go
func (c *P2PClient) Close() error
```

Closes the connection to the peer.

### Options

#### WithCircuitBreaker

```go
func WithCircuitBreaker(maxFailures int, timeout time.Duration) Option
```

Configures circuit breaker parameters.

**Default**: 5 failures, 30 second timeout

---

## LSP Bridge API

**Package**: `github.com/odvcencio/buckley/pkg/acp/lsp`

### Bridge

Bridges JSON-RPC 2.0 communication between agents and LSP servers.

```go
type Bridge struct {
    // fields omitted
}
```

#### Constructor

```go
func NewBridge(reader io.Reader, writer io.Writer) *Bridge
```

Creates a new LSP bridge.

**Parameters**:
- `reader`: Input stream (e.g., os.Stdin)
- `writer`: Output stream (e.g., os.Stdout)

**Example**:
```go
bridge := NewBridge(os.Stdin, os.Stdout)
```

#### HandleRequest

```go
func (b *Bridge) HandleRequest(
    ctx context.Context,
    method string,
    params json.RawMessage,
) (json.RawMessage, error)
```

Handles a JSON-RPC request.

**Parameters**:
- `ctx`: Request context
- `method`: JSON-RPC method name
- `params`: Method parameters

**Returns**: Response data or error

#### HandleStream

```go
func (b *Bridge) HandleStream(
    ctx context.Context,
    streamID string,
    method string,
    params json.RawMessage,
) (<-chan json.RawMessage, error)
```

Handles a streaming JSON-RPC request.

**Returns**: Channel of response messages

**Example**:
```go
responseChan, err := bridge.HandleStream(ctx, "stream-1", "textDocument/didChange", params)
if err != nil {
    log.Printf("Failed to start stream: %v", err)
    return
}

for response := range responseChan {
    // Process streaming response
    fmt.Printf("Response: %s\n", response)
}
```

#### CancelStream

```go
func (b *Bridge) CancelStream(streamID string) error
```

Cancels an active stream.

---

## Observability API

**Package**: `github.com/odvcencio/buckley/pkg/acp/observability`

### Tracing

#### InitTracer

```go
func InitTracer(serviceName string) error
```

Initializes OpenTelemetry tracing with stdout exporter.

**Example**:
```go
if err := InitTracer("buckley-agent"); err != nil {
    log.Fatalf("Failed to initialize tracer: %v", err)
}
defer ShutdownTracer()
```

#### ShutdownTracer

```go
func ShutdownTracer() error
```

Shuts down the tracer and flushes pending spans.

### Metrics

#### InitMetrics

```go
func InitMetrics() error
```

Initializes Prometheus metrics registry.

**Example**:
```go
if err := InitMetrics(); err != nil {
    log.Fatalf("Failed to initialize metrics: %v", err)
}

// Expose metrics endpoint
http.Handle("/metrics", promhttp.Handler())
http.ListenAndServe(":9090", nil)
```

#### Available Metrics

**Authentication**:
- `acp_auth_token_generation_total`: Total tokens generated
- `acp_auth_token_validation_total`: Total validations (labels: status={success|expired|revoked|invalid})
- `acp_auth_token_revocation_total`: Total revocations
- `acp_auth_active_sessions`: Current active authenticated sessions

**Authorization**:
- `acp_authz_tool_access_total`: Total tool access checks (labels: allowed={true|false})
- `acp_authz_policy_evaluations_total`: Total policy evaluations
- `acp_authz_denied_access_total`: Total denied access attempts (labels: tool)

**Events**:
- `acp_events_appended_total`: Total events appended (labels: stream_id)
- `acp_events_read_total`: Total events read
- `acp_events_snapshot_saved_total`: Total snapshots saved
- `acp_events_subscription_active`: Active subscriptions

**P2P**:
- `acp_p2p_connections_total`: Total P2P connections (labels: status={success|failure})
- `acp_p2p_messages_sent_total`: Total messages sent (labels: status={success|failure})
- `acp_p2p_circuit_breaker_state`: Circuit breaker state (labels: state={closed|open|half_open})
- `acp_p2p_message_duration_seconds`: Message send duration histogram

**LSP**:
- `acp_lsp_requests_total`: Total LSP requests (labels: method, status={success|error})
- `acp_lsp_streams_active`: Active LSP streams
- `acp_lsp_request_duration_seconds`: Request duration histogram

### Logging

#### GetLogger

```go
func GetLogger(component string) *slog.Logger
```

Returns a structured logger for a component.

**Example**:
```go
logger := GetLogger("auth")
logger.Info("Token generated",
    "agent_id", "agent-1",
    "capabilities", []string{"testing"},
    "duration", "1h",
)
```

### Event Streaming

#### EventStream

WebSocket event streaming for real-time monitoring.

```go
func NewEventStream() *EventStream
```

Creates a new event stream.

#### Publish

```go
func (s *EventStream) Publish(event Event) error
```

Publishes an event to all subscribers.

**Example**:
```go
stream := NewEventStream()
stream.Publish(Event{
    Type: "tool.access.denied",
    Data: map[string]interface{}{
        "agent_id": "agent-1",
        "tool": "shell",
        "reason": "insufficient capability",
    },
})
```

#### HandleWebSocket

```go
func (s *EventStream) HandleWebSocket(w http.ResponseWriter, r *http.Request)
```

HTTP handler for WebSocket connections.

**Usage**:
```go
stream := NewEventStream()
http.HandleFunc("/events", stream.HandleWebSocket)
http.ListenAndServe(":8080", nil)
```

**Client Example**:
```javascript
const ws = new WebSocket('ws://localhost:8080/events');
ws.onmessage = (event) => {
    const data = JSON.parse(event.data);
    console.log('Event:', data.type, data.data);
};
```

---

## Error Codes

### Authentication Errors

```go
var (
    ErrNoToken          = errors.New("no authentication token provided")
    ErrInvalidToken     = errors.New("invalid authentication token")
    ErrExpiredToken     = errors.New("token has expired")
    ErrRevokedToken     = errors.New("token has been revoked")
    ErrInsufficientAuth = errors.New("insufficient authentication")
    ErrNoCapability     = errors.New("missing required capability")
)
```

### Authorization Errors

```go
var (
    ErrToolNotAllowed = fmt.Errorf("tool not allowed for agent capabilities")
)
```

### gRPC Status Codes

- `codes.Unauthenticated`: Missing or invalid authentication
- `codes.PermissionDenied`: Insufficient permissions for operation

---

## gRPC Service Definitions

### AgentService

```protobuf
service AgentService {
    rpc SendMessage(Message) returns (Response);
    rpc StreamMessages(stream Message) returns (stream Response);
}

message Message {
    string id = 1;
    string type = 2;
    bytes payload = 3;
    map<string, string> metadata = 4;
}

message Response {
    string message_id = 1;
    int32 status_code = 2;
    bytes data = 3;
    string error = 4;
}
```

### Authentication

All gRPC requests must include authentication via metadata:

```go
md := metadata.Pairs("authorization", "Bearer " + token)
ctx := metadata.NewOutgoingContext(context.Background(), md)
response, err := client.SendMessage(ctx, message)
```

---

## Best Practices

### Token Management

1. **Secret Key Security**: Use at least 32 bytes for HMAC secret, store securely (environment variable, secret manager)
2. **Token Lifetime**: Recommend 1 hour for production, use refresh tokens for longer sessions
3. **Cleanup**: Run `CleanupRevokedTokens()` periodically (hourly recommended)
4. **Rotation**: Implement key rotation for production (future enhancement)

### Authorization

1. **Least Privilege**: Grant minimum capabilities needed for agent role
2. **Audit Regularly**: Review audit logs for unauthorized access attempts
3. **Policy Updates**: Update policies atomically, test before deploying

### Error Handling

```go
if err := approver.CheckToolAccess(ctx, "shell"); err != nil {
    if errors.Is(err, ErrToolNotAllowed) {
        // Log and return appropriate error to client
        logger.Warn("Tool access denied",
            "agent_id", claims.AgentID,
            "tool", "shell",
            "capabilities", claims.Capabilities,
        )
        return status.Error(codes.PermissionDenied, "shell access not allowed")
    }
    // Unexpected error
    return status.Error(codes.Internal, "authorization check failed")
}
```

### Observability

1. **Tracing**: Enable tracing for all RPC calls
2. **Metrics**: Monitor authentication failures, denied access attempts
3. **Logging**: Use structured logging with consistent field names
4. **Event Streaming**: Use WebSocket streams for real-time dashboards

---

## Migration Guide

### From Basic Auth to ACP

**Before**:
```go
// No authentication
conn, _ := grpc.Dial(address, grpc.WithInsecure())
```

**After**:
```go
// Generate token
tm := NewTokenManager(secretKey)
token, _ := tm.GenerateToken("agent-1", []string{"testing"}, 1*time.Hour)

// Add to metadata
md := metadata.Pairs("authorization", "Bearer " + token)
ctx := metadata.NewOutgoingContext(context.Background(), md)

// Secure connection
conn, _ := grpc.Dial(address, grpc.WithTransportCredentials(...))
client := pb.NewAgentServiceClient(conn)
response, _ := client.SendMessage(ctx, message)
```

### Server Setup

**Before**:
```go
server := grpc.NewServer()
```

**After**:
```go
tm := NewTokenManager(secretKey)
policy := DefaultToolPolicy()
approver := NewToolApprover(policy)
interceptor := NewAuthInterceptor(tm)

server := grpc.NewServer(
    grpc.UnaryInterceptor(interceptor.UnaryInterceptor()),
    grpc.StreamInterceptor(interceptor.StreamInterceptor()),
)

// Register services
pb.RegisterAgentServiceServer(server, &yourService{
    approver: approver,
})
```

---

## See Also

- [ACP Security Audit](./ACP_SECURITY_AUDIT.md) - Security assessment and recommendations
- [ACP Tool Policy](./ACP_TOOL_POLICY.md) - Detailed tool policy documentation
- [ACP Architecture](./ACP_ARCHITECTURE.md) - System design and architecture
