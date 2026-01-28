// Package tui provides the integrated terminal user interface for Buckley.
// This file implements agent server support for real-time control of the TUI.

package tui

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/odvcencio/fluffyui/agent"
)

const (
	runnerAgentShutdownTimeout = 2 * time.Second
	defaultAgentPort           = "localhost:0" // Let OS assign port
)

// AgentServer provides real-time control of the Buckley TUI.
type AgentServer struct {
	server   *agent.WebSocketServer
	agent    *agent.Agent
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	addr     string
	runner   *Runner
}

// AgentCommand represents a command that can be sent to the TUI.
type AgentCommand struct {
	Type    string            `json:"type"`
	Action  string            `json:"action"`
	Params  map[string]string `json:"params,omitempty"`
	ID      string            `json:"id,omitempty"`
	Text    string            `json:"text,omitempty"`
}

// AgentResponse represents a response from the TUI.
type AgentResponse struct {
	Success   bool              `json:"success"`
	Message   string            `json:"message,omitempty"`
	Error     string            `json:"error,omitempty"`
	Snapshot  *agent.Snapshot   `json:"snapshot,omitempty"`
	Widgets   []agent.WidgetInfo `json:"widgets,omitempty"`
	Timestamp time.Time         `json:"timestamp"`
}

// initAgentServer initializes the agent server for real-time TUI control.
func (r *Runner) initAgentServer(addr string) error {
	if r == nil || addr == "" {
		return nil
	}

	// Validate address format
	addr = strings.TrimSpace(addr)
	if !(strings.HasPrefix(addr, "unix:") || strings.HasPrefix(addr, "tcp:")) {
		return fmt.Errorf("invalid address format %q (must be unix:/path or tcp:host:port)", addr)
	}

	// Remove prefix for WebSocket server
	listenAddr := addr
	if strings.HasPrefix(addr, "unix:") {
		listenAddr = addr[5:] // Remove "unix:" prefix
	} else if strings.HasPrefix(addr, "tcp:") {
		listenAddr = addr[4:] // Remove "tcp:" prefix
	}

	// Create agent that wraps the runtime.App
	// This provides semantic access to the widget tree
	cfg := agent.Config{
		App:         r.app,
		IncludeText: true,
		TickRate:    50 * time.Millisecond,
	}
	
	ag := agent.New(cfg)

	// Create WebSocket server for real-time control
	wsServer, err := agent.NewWebSocketServer(agent.ServerOptions{
		Addr:            listenAddr,
		Agent:           ag,
		AllowText:       true,
		SnapshotTimeout: 5 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("create agent server: %w", err)
	}

	// Store reference
	r.agentServer = &AgentServer{
		server: wsServer,
		agent:  ag,
		runner: r,
	}

	// Start server in background
	r.agentServer.ctx, r.agentServer.cancel = context.WithCancel(context.Background())
	r.agentServer.done = make(chan struct{})
	
	go func() {
		defer close(r.agentServer.done)
		
		log.Printf("agent server starting on %s", addr)
		
		// The WebSocket server handles HTTP requests
		httpServer := &http.Server{
			Addr:    listenAddr,
			Handler: nil, // Uses default mux with WebSocket handler
		}
		
		// Start in goroutine
		go func() {
			if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("agent server error: %v", err)
			}
		}()
		
		// Wait for shutdown signal
		<-r.agentServer.ctx.Done()
		
		// Graceful shutdown
		shutdownCtx, cancel := context.WithTimeout(context.Background(), runnerAgentShutdownTimeout)
		defer cancel()
		
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			log.Printf("agent server shutdown error: %v", err)
		}
	}()

	// Store the actual address (may differ if port 0 was used)
	r.agentServer.addr = addr
	
	log.Printf("agent server listening on %s", addr)
	return nil
}

// stopAgentServer shuts down the agent server.
func (r *Runner) stopAgentServer() {
	if r == nil || r.agentServer == nil {
		return
	}

	if r.agentServer.cancel != nil {
		r.agentServer.cancel()
	}

	select {
	case <-r.agentServer.done:
		log.Printf("agent server stopped gracefully")
	case <-time.After(runnerAgentShutdownTimeout):
		log.Printf("agent server shutdown timed out after %s", runnerAgentShutdownTimeout)
	}

	r.agentServer = nil
}

// GetAgentSnapshot returns the current UI state for external control.
func (r *Runner) GetAgentSnapshot() (agent.Snapshot, error) {
	if r == nil || r.agentServer == nil || r.agentServer.agent == nil {
		return agent.Snapshot{}, fmt.Errorf("agent server not initialized")
	}

	return r.agentServer.agent.Snapshot(), nil
}

// AgentExecuteCommand executes a command via the agent.
func (r *Runner) AgentExecuteCommand(cmd AgentCommand) (*AgentResponse, error) {
	if r == nil || r.agentServer == nil || r.agentServer.agent == nil {
		return nil, fmt.Errorf("agent server not initialized")
	}

	ag := r.agentServer.agent
	resp := &AgentResponse{
		Success:   true,
		Timestamp: time.Now(),
	}

	switch cmd.Type {
	case "focus":
		if cmd.Text == "" {
			resp.Success = false
			resp.Error = "focus requires text parameter (label)"
			return resp, nil
		}
		if err := ag.Focus(cmd.Text); err != nil {
			resp.Success = false
			resp.Error = err.Error()
		} else {
			resp.Message = fmt.Sprintf("focused on %s", cmd.Text)
		}

	case "type":
		if cmd.Text == "" {
			resp.Success = false
			resp.Error = "type requires text parameter"
			return resp, nil
		}
		if err := ag.SendKeyString(cmd.Text); err != nil {
			resp.Success = false
			resp.Error = err.Error()
		} else {
			resp.Message = fmt.Sprintf("typed %d characters", len(cmd.Text))
		}

	case "activate":
		if cmd.Text == "" {
			resp.Success = false
			resp.Error = "activate requires text parameter (label)"
			return resp, nil
		}
		if err := ag.Activate(cmd.Text); err != nil {
			resp.Success = false
			resp.Error = err.Error()
		} else {
			resp.Message = fmt.Sprintf("activated %s", cmd.Text)
		}

	case "snapshot":
		snap := ag.Snapshot()
		resp.Snapshot = &snap
		resp.Message = "snapshot captured"

	case "list":
		widgets := ag.ListWidgets("")
		resp.Widgets = widgets
		resp.Message = fmt.Sprintf("found %d widgets", len(widgets))

	case "find":
		if cmd.Text == "" {
			resp.Success = false
			resp.Error = "find requires text parameter"
			return resp, nil
		}
		if info := ag.FindByLabel(cmd.Text); info != nil {
			resp.Widgets = []agent.WidgetInfo{*info}
			resp.Message = fmt.Sprintf("found widget: %s", info.Label)
		} else {
			resp.Success = false
			resp.Error = fmt.Sprintf("widget not found: %s", cmd.Text)
		}

	case "getValue":
		if cmd.Text == "" {
			resp.Success = false
			resp.Error = "getValue requires text parameter (label)"
			return resp, nil
		}
		value, err := ag.GetValue(cmd.Text)
		if err != nil {
			resp.Success = false
			resp.Error = err.Error()
		} else {
			resp.Message = value
		}

	default:
		resp.Success = false
		resp.Error = fmt.Sprintf("unknown command type: %s", cmd.Type)
	}

	return resp, nil
}

// AgentAddr returns the agent server address.
func (r *Runner) AgentAddr() string {
	if r == nil || r.agentServer == nil {
		return ""
	}
	return r.agentServer.addr
}
