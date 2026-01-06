package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

// Agent implements the official ACP protocol over stdio.
// It acts as a bridge between ACP-compatible editors and Buckley's internal systems.
type Agent struct {
	transport *Transport

	// Session management
	sessions   map[string]*AgentSession
	sessionsMu sync.RWMutex

	// Callbacks for Buckley integration
	handlers AgentHandlers

	// Cancellation for active prompts
	activePrompts   map[string]context.CancelFunc
	activePromptsMu sync.Mutex

	// Server info
	name    string
	version string
}

// AgentSession represents an active conversation session.
type AgentSession struct {
	ID               string
	CreatedAt        time.Time
	WorkingDirectory string
	Environment      map[string]string
	Mode             string
	Modes            *SessionModeState
}

// AgentHandlers are callbacks that connect ACP to Buckley's internals.
type AgentHandlers struct {
	// OnPrompt is called when the user sends a prompt.
	// It should stream responses back using the StreamFunc.
	OnPrompt func(ctx context.Context, session *AgentSession, content []ContentBlock, stream StreamFunc) (*PromptResult, error)

	// OnReadFile reads a file from the workspace.
	OnReadFile func(ctx context.Context, path string, startLine, endLine int) (string, error)

	// OnWriteFile writes a file to the workspace.
	OnWriteFile func(ctx context.Context, path string, content string) error

	// OnCreateTerminal creates a terminal.
	OnCreateTerminal func(ctx context.Context, command string, args []string, cwd string) (string, error)

	// OnTerminalOutput gets terminal output.
	OnTerminalOutput func(ctx context.Context, terminalID string) (string, *int, error)

	// OnKillTerminal kills a terminal.
	OnKillTerminal func(ctx context.Context, terminalID string) error

	// OnRequestPermission asks the user for permission.
	OnRequestPermission func(ctx context.Context, tool, description string, args json.RawMessage, risk string) (bool, bool, error)

	// OnSessionModes provides the session mode list for the client.
	OnSessionModes func(ctx context.Context, session *AgentSession) (*SessionModeState, error)
}

// StreamFunc is used to stream updates back to the client.
type StreamFunc func(update SessionUpdate) error

// NewAgent creates a new ACP agent.
func NewAgent(name, version string, handlers AgentHandlers) *Agent {
	return &Agent{
		sessions:      make(map[string]*AgentSession),
		activePrompts: make(map[string]context.CancelFunc),
		handlers:      handlers,
		name:          name,
		version:       version,
	}
}

// Serve starts the agent on the given reader/writer (typically stdin/stdout).
// It blocks until the context is cancelled or an error occurs.
func (a *Agent) Serve(ctx context.Context, reader io.Reader, writer io.Writer) error {
	a.transport = NewTransport(reader, writer)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		msg, err := a.transport.ReadMessage()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read message: %w", err)
		}

		req, err := ParseRequest(msg)
		if err != nil {
			_ = a.transport.SendError(nil, ErrCodeParse, "Parse error", err.Error())
			continue
		}

		// Handle in goroutine to allow concurrent requests
		go a.handleRequest(ctx, req)
	}
}

// handleRequest dispatches a request to the appropriate handler.
func (a *Agent) handleRequest(ctx context.Context, req *Request) {
	switch req.Method {
	case "initialize":
		a.handleInitialize(ctx, req)
	case "session/new":
		a.handleSessionNew(ctx, req)
	case "session/load":
		a.handleSessionLoad(ctx, req)
	case "session/prompt":
		a.handleSessionPrompt(ctx, req)
	case "session/set_mode":
		a.handleSessionSetMode(ctx, req)
	case "session/cancel":
		a.handleSessionCancel(ctx, req)
	case "shutdown":
		a.handleShutdown(ctx, req)
	default:
		if !IsNotification(req) {
			_ = a.transport.SendError(req.ID, ErrCodeMethodNotFound, "Method not found", req.Method)
		}
	}
}

// handleInitialize handles the initialize request.
func (a *Agent) handleInitialize(ctx context.Context, req *Request) {
	params, err := ParseParams[InitializeParams](req)
	if err != nil {
		_ = a.transport.SendError(req.ID, ErrCodeInvalidParams, "Invalid params", err.Error())
		return
	}

	_ = params // We could use client info for logging

	title := a.name
	result := InitializeResult{
		ProtocolVersion: ProtocolVersion,
		AgentInfo: &Implementation{
			Name:    a.name,
			Version: a.version,
			Title:   &title,
		},
		AgentCapabilities: AgentCapabilities{
			LoadSession: false,
			McpCapabilities: McpCapabilities{
				HTTP: false,
				SSE:  false,
			},
			PromptCapabilities: PromptCapabilities{
				Audio:           false,
				EmbeddedContext: false,
				Image:           false,
			},
			SessionCapabilities: SessionCapabilities{},
		},
	}

	_ = a.transport.SendResponse(req.ID, result)
}

// handleSessionNew creates a new session.
func (a *Agent) handleSessionNew(ctx context.Context, req *Request) {
	params, err := ParseParams[NewSessionParams](req)
	if err != nil {
		_ = a.transport.SendError(req.ID, ErrCodeInvalidParams, "Invalid params", err.Error())
		return
	}

	session := &AgentSession{
		ID:               ulid.Make().String(),
		CreatedAt:        time.Now(),
		WorkingDirectory: params.Cwd,
		Mode:             "normal",
	}

	a.sessionsMu.Lock()
	a.sessions[session.ID] = session
	a.sessionsMu.Unlock()

	var modes *SessionModeState
	if a.handlers.OnSessionModes != nil {
		if state, err := a.handlers.OnSessionModes(ctx, session); err == nil {
			modes = state
		}
	}
	if modes != nil {
		session.Modes = modes
		if modes.CurrentModeID != "" {
			session.Mode = modes.CurrentModeID
		}
	}

	_ = a.transport.SendResponse(req.ID, NewSessionResult{SessionID: session.ID, Modes: modes})
}

// handleSessionLoad loads an existing session.
func (a *Agent) handleSessionLoad(ctx context.Context, req *Request) {
	params, err := ParseParams[LoadSessionParams](req)
	if err != nil {
		_ = a.transport.SendError(req.ID, ErrCodeInvalidParams, "Invalid params", err.Error())
		return
	}

	a.sessionsMu.RLock()
	session, exists := a.sessions[params.SessionID]
	a.sessionsMu.RUnlock()

	if !exists {
		_ = a.transport.SendError(req.ID, ErrCodeSessionNotFound, "Session not found", params.SessionID)
		return
	}

	_ = a.transport.SendResponse(req.ID, LoadSessionResult{Modes: session.Modes})
}

// handleSessionPrompt handles a user prompt.
func (a *Agent) handleSessionPrompt(ctx context.Context, req *Request) {
	params, err := ParseParams[PromptParams](req)
	if err != nil {
		_ = a.transport.SendError(req.ID, ErrCodeInvalidParams, "Invalid params", err.Error())
		return
	}

	a.sessionsMu.RLock()
	session, exists := a.sessions[params.SessionID]
	a.sessionsMu.RUnlock()

	if !exists {
		_ = a.transport.SendError(req.ID, ErrCodeSessionNotFound, "Session not found", params.SessionID)
		return
	}

	// Create cancellable context for this prompt
	promptCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	a.activePromptsMu.Lock()
	a.activePrompts[params.SessionID] = cancel
	a.activePromptsMu.Unlock()

	defer func() {
		a.activePromptsMu.Lock()
		delete(a.activePrompts, params.SessionID)
		a.activePromptsMu.Unlock()
	}()

	// Stream function to send updates
	streamFunc := func(update SessionUpdate) error {
		return a.transport.SendNotification("session/update", SessionUpdateNotification{
			SessionID: params.SessionID,
			Update:    update,
		})
	}

	// Call the handler
	if a.handlers.OnPrompt == nil {
		a.transport.SendError(req.ID, ErrCodeInternal, "No prompt handler configured", nil)
		return
	}

	result, err := a.handlers.OnPrompt(promptCtx, session, params.Prompt, streamFunc)
	if err != nil {
		if promptCtx.Err() == context.Canceled {
			a.transport.SendResponse(req.ID, PromptResult{StopReason: "cancelled"})
			return
		}
		a.transport.SendError(req.ID, ErrCodeInternal, "Prompt failed", err.Error())
		return
	}

	a.transport.SendResponse(req.ID, result)
}

// handleSessionSetMode changes the session mode.
func (a *Agent) handleSessionSetMode(ctx context.Context, req *Request) {
	params, err := ParseParams[SetModeParams](req)
	if err != nil {
		a.transport.SendError(req.ID, ErrCodeInvalidParams, "Invalid params", err.Error())
		return
	}

	a.sessionsMu.Lock()
	session, exists := a.sessions[params.SessionID]
	if exists {
		if session.Modes != nil && len(session.Modes.AvailableModes) > 0 {
			valid := false
			for _, mode := range session.Modes.AvailableModes {
				if mode.ID == params.ModeID {
					valid = true
					break
				}
			}
			if !valid {
				a.sessionsMu.Unlock()
				a.transport.SendError(req.ID, ErrCodeInvalidParams, "Unknown mode", params.ModeID)
				return
			}
		}
		session.Mode = params.ModeID
		if session.Modes != nil {
			session.Modes.CurrentModeID = params.ModeID
		}
	}
	a.sessionsMu.Unlock()

	if !exists {
		a.transport.SendError(req.ID, ErrCodeSessionNotFound, "Session not found", params.SessionID)
		return
	}

	a.transport.SendResponse(req.ID, SetModeResult{})
	if exists {
		_ = a.transport.SendNotification("session/update", SessionUpdateNotification{
			SessionID: params.SessionID,
			Update:    NewCurrentModeUpdate(params.ModeID),
		})
	}
}

// handleSessionCancel cancels an active prompt.
func (a *Agent) handleSessionCancel(ctx context.Context, req *Request) {
	params, err := ParseParams[CancelParams](req)
	if err != nil {
		// Cancel is a notification, no response needed
		return
	}

	a.activePromptsMu.Lock()
	cancel, exists := a.activePrompts[params.SessionID]
	a.activePromptsMu.Unlock()

	if exists {
		cancel()
	}
}

// handleShutdown handles a shutdown request.
func (a *Agent) handleShutdown(ctx context.Context, req *Request) {
	// Cancel all active prompts
	a.activePromptsMu.Lock()
	for _, cancel := range a.activePrompts {
		cancel()
	}
	a.activePromptsMu.Unlock()

	a.transport.SendResponse(req.ID, nil)
}

// Client method helpers (for calling back to the editor)

// ReadFile reads a file from the editor's workspace.
func (a *Agent) ReadFile(ctx context.Context, path string, startLine, endLine int) (string, error) {
	if a.handlers.OnReadFile != nil {
		return a.handlers.OnReadFile(ctx, path, startLine, endLine)
	}
	return "", fmt.Errorf("read file not supported")
}

// WriteFile writes a file to the editor's workspace.
func (a *Agent) WriteFile(ctx context.Context, path string, content string) error {
	if a.handlers.OnWriteFile != nil {
		return a.handlers.OnWriteFile(ctx, path, content)
	}
	return fmt.Errorf("write file not supported")
}

// RequestPermission asks the user for permission to perform an action.
func (a *Agent) RequestPermission(ctx context.Context, tool, description string, args json.RawMessage, risk string) (granted bool, remember bool, err error) {
	if a.handlers.OnRequestPermission != nil {
		return a.handlers.OnRequestPermission(ctx, tool, description, args, risk)
	}
	return false, false, fmt.Errorf("permission request not supported")
}
