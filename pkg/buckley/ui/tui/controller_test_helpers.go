// Package tui provides the integrated terminal user interface for Buckley.
// This file contains test helper methods for the Controller.

package tui

import (
	"github.com/odvcencio/buckley/pkg/tool"
)

// App returns the TUI app instance for testing.
func (c *Controller) App() App {
	if c == nil {
		return nil
	}
	return c.app
}

// IsStreaming returns true if the current session is streaming a response.
func (c *Controller) IsStreaming() bool {
	if c == nil || len(c.sessions) == 0 {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.currentSession < 0 || c.currentSession >= len(c.sessions) {
		return false
	}
	return c.sessions[c.currentSession].Streaming
}

// RegisterTool registers a tool for testing.
func (c *Controller) RegisterTool(t tool.Tool) {
	if c == nil || c.registry == nil {
		return
	}
	c.registry.Register(t)
	
	// Also register on current session's tool registry if different
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.currentSession >= 0 && c.currentSession < len(c.sessions) {
		sess := c.sessions[c.currentSession]
		if sess.ToolRegistry != nil && sess.ToolRegistry != c.registry {
			sess.ToolRegistry.Register(t)
		}
	}
}
