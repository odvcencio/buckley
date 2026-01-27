// Package tui provides the integrated terminal user interface for Buckley.
package tui

import (
	"context"
	"log"
	"strings"

	"github.com/odvcencio/fluffy-ui/agent"
	"github.com/odvcencio/fluffy-ui/runtime"
	"github.com/odvcencio/fluffy-ui/terminal"
)

// ============================================================================
// FILE: app_agent.go
// PURPOSE: Agent server initialization for external debugging/testing
// FUNCTIONS:
//   - initAgentServer
//   - stopAgentServer
// ============================================================================

// initAgentServer starts the agent API server for external debugging/testing.
// The agent exposes the UI's accessibility tree via a JSONL protocol.
func (a *WidgetApp) initAgentServer(addr string) {
	if a == nil || addr == "" {
		return
	}

	// Validate address format
	addr = strings.TrimSpace(addr)
	if !(strings.HasPrefix(addr, "unix:") || strings.HasPrefix(addr, "tcp:")) {
		log.Printf("agent server: invalid address format %q (must be unix:/path or tcp:host:port)", addr)
		return
	}

	// Create agent with manual screen attachment and custom key posting
	a.agent = agent.New(agent.Config{
		DisableAutoAttach: true,
		IncludeText:       true,
		PostKey: func(msg runtime.KeyMsg) error {
			// Forward key events to buckley's backend
			return a.backend.PostEvent(terminal.KeyEvent{
				Key:   msg.Key,
				Rune:  msg.Rune,
				Alt:   msg.Alt,
				Ctrl:  msg.Ctrl,
				Shift: msg.Shift,
			})
		},
	})
	a.agent.SetScreen(a.screen)

	// Create server
	var err error
	a.agentServer, err = agent.NewServer(agent.ServerOptions{
		Addr:      addr,
		Agent:     a.agent,
		AllowText: true,
	})
	if err != nil {
		log.Printf("agent server init failed: %v", err)
		return
	}

	// Start server in background
	a.agentCtx, a.agentCancel = context.WithCancel(context.Background())
	go func() {
		if err := a.agentServer.Serve(a.agentCtx); err != nil && a.agentCtx.Err() == nil {
			log.Printf("agent server error: %v", err)
		}
	}()

	log.Printf("agent server listening on %s", addr)
}

// stopAgentServer shuts down the agent API server.
func (a *WidgetApp) stopAgentServer() {
	if a == nil {
		return
	}
	if a.agentCancel != nil {
		a.agentCancel()
	}
	if a.agentServer != nil {
		_ = a.agentServer.Close()
	}
}
