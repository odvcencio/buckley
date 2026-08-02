// Package acp implements the Agent Client Protocol (ACP) for editor integration.
// ACP is a JSON-RPC 2.0 protocol over stdio that standardizes communication between
// editors (clients) and AI coding agents (servers like Buckley).
//
// See: https://agentclientprotocol.com
package acp

import "encoding/json"

// ProtocolVersion is the ACP protocol version we implement.
const ProtocolVersion uint16 = 1

// JSON-RPC 2.0 Message Types

type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Notification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Result  any    `json:"result,omitempty"`
	Error   *Error `json:"error,omitempty"`
}

type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Standard JSON-RPC error codes.
const (
	ErrCodeParse          = -32700
	ErrCodeInvalidRequest = -32600
	ErrCodeMethodNotFound = -32601
	ErrCodeInvalidParams  = -32602
	ErrCodeInternal       = -32603

	// ACP-specific error codes
	ErrCodeSessionNotFound = -32000
	ErrCodeToolDenied      = -32001
	ErrCodeAuthRequired    = -32002
	ErrCodeCancelled       = -32003
)

// Initialization Types

type InitializeParams struct {
	ProtocolVersion    uint16              `json:"protocolVersion"`
	ClientInfo         *Implementation     `json:"clientInfo,omitempty"`
	ClientCapabilities *ClientCapabilities `json:"clientCapabilities,omitempty"`
}

type InitializeResult struct {
	ProtocolVersion   uint16            `json:"protocolVersion"`
	AgentInfo         *Implementation   `json:"agentInfo,omitempty"`
	AgentCapabilities AgentCapabilities `json:"agentCapabilities,omitempty"`
	AuthMethods       []AuthMethod      `json:"authMethods,omitempty"`
}

// Implementation describes a client or agent implementation.
type Implementation struct {
	Name    string  `json:"name"`
	Version string  `json:"version"`
	Title   *string `json:"title,omitempty"`
}

// ClientCapabilities describes what the client supports.
type ClientCapabilities struct {
	FS       FileSystemCapability `json:"fs,omitempty"`
	Terminal bool                 `json:"terminal,omitempty"`
}

// FileSystemCapability describes filesystem support.
type FileSystemCapability struct {
	ReadTextFile  bool `json:"readTextFile,omitempty"`
	WriteTextFile bool `json:"writeTextFile,omitempty"`
}

// AgentCapabilities describes what the agent supports.
type AgentCapabilities struct {
	LoadSession         bool                `json:"loadSession,omitempty"`
	McpCapabilities     McpCapabilities     `json:"mcpCapabilities,omitempty"`
	PromptCapabilities  PromptCapabilities  `json:"promptCapabilities,omitempty"`
	SessionCapabilities SessionCapabilities `json:"sessionCapabilities,omitempty"`
}

type McpCapabilities struct {
	HTTP bool `json:"http,omitempty"`
	SSE  bool `json:"sse,omitempty"`
}

type PromptCapabilities struct {
	Audio           bool `json:"audio,omitempty"`
	EmbeddedContext bool `json:"embeddedContext,omitempty"`
	Image           bool `json:"image,omitempty"`
}

// SessionCapabilities is currently empty in the ACP schema.
type SessionCapabilities struct{}

// AuthMethod is a placeholder for ACP auth method definitions.
// The current Buckley ACP integration does not advertise auth methods.
type AuthMethod json.RawMessage

// Session Types

type NewSessionParams struct {
	Cwd        string      `json:"cwd"`
	McpServers []McpServer `json:"mcpServers"`
}

type NewSessionResult struct {
	SessionID string            `json:"sessionId"`
	Modes     *SessionModeState `json:"modes,omitempty"`
}

type LoadSessionParams struct {
	SessionID  string      `json:"sessionId"`
	Cwd        string      `json:"cwd"`
	McpServers []McpServer `json:"mcpServers"`
}

// McpServer describes one entry in session/new's (or session/load's)
// mcpServers array (S5). Per the ACP schema, stdio is the untagged default
// variant -- a stdio declaration carries no "type" field on the wire, only
// name/command/args/env. The http and sse variants carry an explicit
// "type": "http" | "sse" discriminator plus a url and headers. A single
// flat struct decodes all three shapes since their field sets do not
// collide; McpServerKind reports which one a decoded value represents.
type McpServer struct {
	Type    string        `json:"type,omitempty"`
	Name    string        `json:"name"`
	Command string        `json:"command,omitempty"`
	Args    []string      `json:"args,omitempty"`
	Env     []EnvVariable `json:"env,omitempty"`
	URL     string        `json:"url,omitempty"`
}

// EnvVariable is one KEY/VALUE pair to set when launching a stdio MCP
// server (McpServer.Env).
type EnvVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// McpServer transport kinds, matched against McpServer.Type. Stdio is the
// zero-value "" case since it is untagged on the wire.
const (
	McpServerKindStdio = ""
	McpServerKindHTTP  = "http"
	McpServerKindSSE   = "sse"
)

type LoadSessionResult struct {
	Modes *SessionModeState `json:"modes,omitempty"`
}

// SessionModeState describes the current mode and available modes.
type SessionModeState struct {
	CurrentModeID  string        `json:"currentModeId"`
	AvailableModes []SessionMode `json:"availableModes"`
}

// SessionMode describes a selectable mode.
type SessionMode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Prompt Types

type PromptParams struct {
	SessionID string         `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt"`
}

type PromptResult struct {
	StopReason string `json:"stopReason"`
}

// Session Update Types (notifications from agent to client)

type SessionUpdateNotification struct {
	SessionID string        `json:"sessionId"`
	Update    SessionUpdate `json:"update"`
}

// SessionUpdate is the update payload for a session/update notification.
type SessionUpdate struct {
	SessionUpdate string `json:"sessionUpdate"`
	Content       any    `json:"content,omitempty"`
	// CurrentModeID is populated for current_mode_update notifications. The
	// wire field is "currentModeId" per the ACP schema -- not "modeId" (that
	// name is reserved for the session/set_mode request parameter).
	CurrentModeID string `json:"currentModeId,omitempty"`
	// AvailableCommands is populated for available_commands_update notifications.
	AvailableCommands []AvailableCommand `json:"availableCommands,omitempty"`

	ToolCallID string             `json:"toolCallId,omitempty"`
	Title      string             `json:"title,omitempty"`
	Kind       string             `json:"kind,omitempty"`
	Status     string             `json:"status,omitempty"`
	RawInput   any                `json:"rawInput,omitempty"`
	RawOutput  any                `json:"rawOutput,omitempty"`
	Locations  []ToolCallLocation `json:"locations,omitempty"`
}

// ContentBlock represents a piece of content (text, image, resource, etc).
type ContentBlock struct {
	Type        string          `json:"type"`
	Text        string          `json:"text,omitempty"`
	Data        string          `json:"data,omitempty"`
	MimeType    string          `json:"mimeType,omitempty"`
	URI         string          `json:"uri,omitempty"`
	Name        string          `json:"name,omitempty"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	Size        *int64          `json:"size,omitempty"`
	Resource    json.RawMessage `json:"resource,omitempty"`
}

// Session mode / cancellation

type SetModeParams struct {
	SessionID string `json:"sessionId"`
	ModeID    string `json:"modeId"`
}

type SetModeResult struct{}

type CancelParams struct {
	SessionID string `json:"sessionId"`
}

// ShutdownResult is the response body for the "_shutdown" extension method.
// It is an empty object rather than a bare null result, per JSON-RPC 2.0:
// a response must carry either "result" or "error".
type ShutdownResult struct{}

// Client-bound requests (agent -> client)
//
// These are outbound JSON-RPC requests Buckley sends to the client over
// Transport.SendRequest, answered by the client rather than by Buckley.

// RequestPermissionParams is the "session/request_permission" request
// Buckley sends before running a tool call that needs user authorization.
// See: https://agentclientprotocol.com/protocol/v1/tool-calls#requesting-permission
type RequestPermissionParams struct {
	SessionID string             `json:"sessionId"`
	ToolCall  ToolCallUpdate     `json:"toolCall"`
	Options   []PermissionOption `json:"options"`
}

// ToolCallUpdate carries the tool-call fields the client needs to render a
// permission prompt: the same shape session/update tool_call notifications
// use, without the sessionUpdate discriminator (which only belongs on
// session/update).
type ToolCallUpdate struct {
	ToolCallID string             `json:"toolCallId"`
	Title      string             `json:"title,omitempty"`
	Kind       string             `json:"kind,omitempty"`
	Status     string             `json:"status,omitempty"`
	RawInput   any                `json:"rawInput,omitempty"`
	Locations  []ToolCallLocation `json:"locations,omitempty"`
}

// PermissionOption is a single choice offered to the user in a
// session/request_permission request.
type PermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// Permission option kinds, per the ACP schema's PermissionOptionKind union.
const (
	PermissionOptionKindAllowOnce    = "allow_once"
	PermissionOptionKindAllowAlways  = "allow_always"
	PermissionOptionKindRejectOnce   = "reject_once"
	PermissionOptionKindRejectAlways = "reject_always"
)

// RequestPermissionResult is the response body for
// "session/request_permission".
type RequestPermissionResult struct {
	Outcome RequestPermissionOutcome `json:"outcome"`
}

// RequestPermissionOutcome is the user's decision: either they selected one
// of the offered options, or the client cancelled the prompt turn (via
// session/cancel) before they responded.
type RequestPermissionOutcome struct {
	Outcome  string `json:"outcome"` // "selected" | "cancelled"
	OptionID string `json:"optionId,omitempty"`
}

// RequestPermissionOutcome discriminator values.
const (
	RequestPermissionOutcomeSelected  = "selected"
	RequestPermissionOutcomeCancelled = "cancelled"
)

// Constructors

const (
	SessionUpdateUserMessageChunk  = "user_message_chunk"
	SessionUpdateAgentMessageChunk = "agent_message_chunk"
	SessionUpdateAgentThoughtChunk = "agent_thought_chunk"
	SessionUpdateCurrentModeUpdate = "current_mode_update"
	SessionUpdateAvailableCommands = "available_commands_update"
	SessionUpdateToolCall          = "tool_call"
	SessionUpdateToolCallUpdate    = "tool_call_update"
)

// AvailableCommand describes a slash command advertised by the agent.
type AvailableCommand struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Input       *AvailableCommandInput `json:"input,omitempty"`
}

// AvailableCommandInput describes command input requirements.
type AvailableCommandInput struct {
	Hint string `json:"hint"`
}

// NewTextContent creates a text content block.
func NewTextContent(text string) ContentBlock {
	return ContentBlock{Type: "text", Text: text}
}

// NewAgentMessageChunk creates a session update chunk for agent output.
func NewAgentMessageChunk(text string) SessionUpdate {
	return SessionUpdate{SessionUpdate: SessionUpdateAgentMessageChunk, Content: NewTextContent(text)}
}

// NewAgentThoughtChunk creates a session update chunk for agent reasoning.
func NewAgentThoughtChunk(text string) SessionUpdate {
	return SessionUpdate{SessionUpdate: SessionUpdateAgentThoughtChunk, Content: NewTextContent(text)}
}

// NewCurrentModeUpdate notifies the client that the current mode changed.
func NewCurrentModeUpdate(modeID string) SessionUpdate {
	return SessionUpdate{SessionUpdate: SessionUpdateCurrentModeUpdate, CurrentModeID: modeID}
}

// ToolCallContent describes output emitted by a tool call.
type ToolCallContent struct {
	Type       string        `json:"type"`
	Content    *ContentBlock `json:"content,omitempty"`
	Path       string        `json:"path,omitempty"`
	OldText    *string       `json:"oldText,omitempty"`
	NewText    *string       `json:"newText,omitempty"`
	TerminalID string        `json:"terminalId,omitempty"`
}

// ToolCallLocation points to a file location touched by a tool call.
type ToolCallLocation struct {
	Path string `json:"path"`
	Line *int   `json:"line,omitempty"`
}

const (
	ToolCallStatusPending    = "pending"
	ToolCallStatusInProgress = "in_progress"
	ToolCallStatusCompleted  = "completed"
	ToolCallStatusFailed     = "failed"
)

const (
	ToolKindRead    = "read"
	ToolKindEdit    = "edit"
	ToolKindDelete  = "delete"
	ToolKindMove    = "move"
	ToolKindSearch  = "search"
	ToolKindExecute = "execute"
	ToolKindThink   = "think"
	ToolKindFetch   = "fetch"
	ToolKindOther   = "other"
)

// --- Buckley Machine Extensions ---
// These extend the ACP protocol with machine-specific operations for
// coordinating parallel agents and modality switching. Per the ACP
// extensibility convention, every custom method is prefixed with an
// underscore so it cannot collide with a future spec method, and none of
// these events ride the standard "sessionUpdate" union -- see
// MachineNotifyParams and the "_machine/notify" notification below.

// SpawnAgentParams is the request body for "_machine/spawn_agent".
type SpawnAgentParams struct {
	SessionID string `json:"sessionId"`
	Task      string `json:"task"`
	Modality  string `json:"modality,omitempty"` // "classic", "rlm", "ralph"
	Model     string `json:"model,omitempty"`
	Spec      string `json:"spec,omitempty"` // ralph spec text
}

// SpawnAgentResult is the response for "_machine/spawn_agent".
type SpawnAgentResult struct {
	AgentID string `json:"agentId"`
}

// SteerAgentParams is the request body for "_machine/steer_agent".
type SteerAgentParams struct {
	SessionID string `json:"sessionId"`
	AgentID   string `json:"agentId"`
	Content   string `json:"content"`
}

// SteerAgentResult is the response for "_machine/steer_agent".
type SteerAgentResult struct{}

// ListAgentsParams is the request body for "_machine/list_agents".
type ListAgentsParams struct {
	SessionID string `json:"sessionId"`
}

// AgentInfo describes a running agent for list_agents responses.
type AgentInfo struct {
	AgentID  string `json:"agentId"`
	State    string `json:"state"`
	Modality string `json:"modality"`
	ParentID string `json:"parentId,omitempty"`
}

// ListAgentsResult is the response for "_machine/list_agents".
type ListAgentsResult struct {
	Agents []AgentInfo `json:"agents"`
}

// EscalateModeParams is the request body for "_machine/escalate_mode".
type EscalateModeParams struct {
	SessionID string `json:"sessionId"`
	AgentID   string `json:"agentId"`
	Modality  string `json:"modality"` // target modality
}

// EscalateModeResult is the response for "_machine/escalate_mode".
type EscalateModeResult struct {
	PreviousModality string `json:"previousModality"`
	NewModality      string `json:"newModality"`
}

// Machine event kinds delivered via "_machine/notify" notifications. These
// used to ride the standard "sessionUpdate" union as custom tags
// (machine_state/machine_lock/machine_agent), which broke serde-strict
// clients that reject an unrecognized sessionUpdate discriminator. They are
// now a distinct, non-spec notification method instead.
const (
	MachineEventState = "machine_state"
	MachineEventLock  = "machine_lock"
	MachineEventAgent = "machine_agent"
)

// MachineNotifyParams is the payload for a "_machine/notify" notification:
// Buckley's out-of-band channel for agent-swarm telemetry (spawn/state/lock
// events) that has no equivalent in the ACP session/update union.
type MachineNotifyParams struct {
	SessionID string         `json:"sessionId"`
	Kind      string         `json:"kind"` // one of the MachineEvent* constants
	Payload   map[string]any `json:"payload,omitempty"`
}
