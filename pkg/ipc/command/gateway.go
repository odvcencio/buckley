package command

import (
	"fmt"
	"strings"

	"github.com/oklog/ulid/v2"
)

// SessionCommand represents a high-level user instruction targeting a session.
type SessionCommand struct {
	SessionID string `json:"sessionId"`
	ID        string `json:"commandId,omitempty"`
	Type      string `json:"type"`
	Content   string `json:"content"`
}

// EnsureID assigns the stable identity returned to clients and carried by
// command lifecycle events.
func (c *SessionCommand) EnsureID() string {
	if c == nil {
		return ""
	}
	c.ID = strings.TrimSpace(c.ID)
	if c.ID == "" {
		c.ID = strings.ToLower(ulid.Make().String())
	}
	return c.ID
}

// RequiresContent reports whether a command carries user-authored input.
func RequiresContent(commandType string) bool {
	switch strings.TrimSpace(commandType) {
	case "input", "slash", "steer", "queue", "model":
		return true
	default:
		return false
	}
}

// Handler executes a session command.
//
//go:generate mockgen -package=command -destination=mock_handler_test.go github.com/odvcencio/buckley/pkg/ipc/command Handler
type Handler interface {
	HandleSessionCommand(SessionCommand) error
}

// HandlerFunc adapts a function into a Handler.
type HandlerFunc func(SessionCommand) error

func (f HandlerFunc) HandleSessionCommand(cmd SessionCommand) error {
	return f(cmd)
}

// Gateway routes commands from IPC clients to the active agent.
type Gateway struct {
	handler Handler
}

// NewGateway constructs a gateway without a handler.
func NewGateway() *Gateway {
	return &Gateway{}
}

// Register attaches a handler to receive future commands.
func (g *Gateway) Register(handler Handler) {
	g.handler = handler
}

// Dispatch forwards the command to the registered handler.
func (g *Gateway) Dispatch(cmd SessionCommand) error {
	if g == nil || g.handler == nil {
		return fmt.Errorf("no command handler registered")
	}
	return g.handler.HandleSessionCommand(cmd)
}
