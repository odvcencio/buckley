// Package acp implements the Zed Agent Communication Protocol (ACP) for editor integration.
// ACP is a JSON-RPC 2.0 protocol over stdio that standardizes communication between
// editors (clients) and AI coding agents (servers like Buckley).
//
// See: https://agentcommunicationprotocol.com
package acp

import "encoding/json"

// ProtocolVersion is the ACP protocol version we implement.
const ProtocolVersion uint16 = 1

// JSON-RPC 2.0 Message Types

type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Notification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *Error      `json:"error,omitempty"`
}

type Error struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
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
	Cwd        string            `json:"cwd"`
	McpServers []json.RawMessage `json:"mcpServers"`
}

type NewSessionResult struct {
	SessionID string            `json:"sessionId"`
	Modes     *SessionModeState `json:"modes,omitempty"`
}

type LoadSessionParams struct {
	SessionID  string            `json:"sessionId"`
	Cwd        string            `json:"cwd"`
	McpServers []json.RawMessage `json:"mcpServers"`
}

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
	ModeID        string `json:"modeId,omitempty"`
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
	return SessionUpdate{SessionUpdate: SessionUpdateCurrentModeUpdate, ModeID: modeID}
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
