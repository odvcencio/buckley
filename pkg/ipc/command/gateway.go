package command

import "fmt"

// SessionCommand represents a high-level user instruction targeting a session.
type SessionCommand struct {
	SessionID string `json:"sessionId"`
	Type      string `json:"type"`
	Content   string `json:"content"`
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
