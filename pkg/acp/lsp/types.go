package lsp

import "encoding/json"

// InitializeParams represents LSP initialize request params
type InitializeParams struct {
	ProcessID    *int               `json:"processId"`
	ClientInfo   *ClientInfo        `json:"clientInfo,omitempty"`
	RootURI      string             `json:"rootUri"`
	Capabilities ClientCapabilities `json:"capabilities"`
}

// ClientInfo holds client information
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// ClientCapabilities represents client capabilities
type ClientCapabilities struct {
	TextDocument *TextDocumentClientCapabilities `json:"textDocument,omitempty"`
}

// TextDocumentClientCapabilities represents text document capabilities
type TextDocumentClientCapabilities struct {
	Synchronization *TextDocumentSyncClientCapabilities `json:"synchronization,omitempty"`
}

// TextDocumentSyncClientCapabilities represents sync capabilities
type TextDocumentSyncClientCapabilities struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
}

// InitializeResult represents LSP initialize response
type InitializeResult struct {
	Capabilities ServerCapabilities `json:"capabilities"`
	ServerInfo   *ServerInfo        `json:"serverInfo,omitempty"`
}

// ServerInfo holds server information
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// ServerCapabilities represents server capabilities
type ServerCapabilities struct {
	TextDocumentSync *TextDocumentSyncOptions `json:"textDocumentSync,omitempty"`
}

// TextDocumentSyncOptions represents text document sync options
type TextDocumentSyncOptions struct {
	OpenClose bool                 `json:"openClose,omitempty"`
	Change    TextDocumentSyncKind `json:"change,omitempty"`
}

// TextDocumentSyncKind defines text document sync modes
type TextDocumentSyncKind int

const (
	TextDocumentSyncKindNone        TextDocumentSyncKind = 0
	TextDocumentSyncKindFull        TextDocumentSyncKind = 1
	TextDocumentSyncKindIncremental TextDocumentSyncKind = 2
)

// TextQueryParams represents parameters for buckley/textQuery method
type TextQueryParams struct {
	Query string `json:"query"`
}

// TextQueryResult represents result for buckley/textQuery method
type TextQueryResult struct {
	Response string `json:"response"`
	AgentID  string `json:"agentId"`
}

// StreamQueryParams represents parameters for buckley/streamQuery method
type StreamQueryParams struct {
	Query string `json:"query"`
}

// StreamQueryResult represents initial result for buckley/streamQuery method
type StreamQueryResult struct {
	StreamID string `json:"streamId"`
	AgentID  string `json:"agentId"`
}

// StreamChunkNotification represents a streaming chunk notification
type StreamChunkNotification struct {
	StreamID string `json:"streamId"`
	Chunk    string `json:"chunk"`
	Done     bool   `json:"done"`
}

// CancelParams represents parameters for $/cancelRequest
type CancelParams struct {
	ID json.RawMessage `json:"id"`
}
