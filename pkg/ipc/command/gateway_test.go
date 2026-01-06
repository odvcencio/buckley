package command

import (
	"errors"
	"testing"

	"go.uber.org/mock/gomock"
)

func TestNewGateway(t *testing.T) {
	gateway := NewGateway()

	if gateway == nil {
		t.Fatal("NewGateway() returned nil")
	}
	if gateway.handler != nil {
		t.Error("NewGateway() should initialize with nil handler")
	}
}

func TestGateway_Register(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	gateway := NewGateway()
	mockHandler := NewMockHandler(ctrl)

	gateway.Register(mockHandler)

	if gateway.handler == nil {
		t.Error("Register() should set handler")
	}
}

func TestGateway_Dispatch_NoHandler(t *testing.T) {
	gateway := NewGateway()

	cmd := SessionCommand{
		SessionID: "test-session",
		Type:      "execute",
		Content:   "test command",
	}

	err := gateway.Dispatch(cmd)

	if err == nil {
		t.Error("Dispatch() should return error when no handler is registered")
	}
	if err.Error() != "no command handler registered" {
		t.Errorf("Dispatch() error = %v, want 'no command handler registered'", err)
	}
}

func TestGateway_Dispatch_NilGateway(t *testing.T) {
	var gateway *Gateway

	cmd := SessionCommand{
		SessionID: "test-session",
		Type:      "execute",
		Content:   "test command",
	}

	err := gateway.Dispatch(cmd)

	if err == nil {
		t.Error("Dispatch() should return error for nil gateway")
	}
}

func TestGateway_Dispatch_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	gateway := NewGateway()
	mockHandler := NewMockHandler(ctrl)

	cmd := SessionCommand{
		SessionID: "test-session-123",
		Type:      "execute",
		Content:   "run tests",
	}

	// Expect handler to be called with the command
	mockHandler.EXPECT().HandleSessionCommand(cmd).Return(nil)

	gateway.Register(mockHandler)
	err := gateway.Dispatch(cmd)

	if err != nil {
		t.Errorf("Dispatch() error = %v, want nil", err)
	}
}

func TestGateway_Dispatch_HandlerError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	gateway := NewGateway()
	mockHandler := NewMockHandler(ctrl)

	cmd := SessionCommand{
		SessionID: "test-session-456",
		Type:      "invalid",
		Content:   "bad command",
	}

	expectedError := errors.New("invalid command type")
	mockHandler.EXPECT().HandleSessionCommand(cmd).Return(expectedError)

	gateway.Register(mockHandler)
	err := gateway.Dispatch(cmd)

	if err == nil {
		t.Error("Dispatch() should return error from handler")
	}
	if err != expectedError {
		t.Errorf("Dispatch() error = %v, want %v", err, expectedError)
	}
}

func TestGateway_Dispatch_MultipleCommands(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	gateway := NewGateway()
	mockHandler := NewMockHandler(ctrl)

	cmd1 := SessionCommand{SessionID: "s1", Type: "start", Content: "cmd1"}
	cmd2 := SessionCommand{SessionID: "s2", Type: "stop", Content: "cmd2"}
	cmd3 := SessionCommand{SessionID: "s3", Type: "pause", Content: "cmd3"}

	// Expect handler to be called for each command
	mockHandler.EXPECT().HandleSessionCommand(cmd1).Return(nil)
	mockHandler.EXPECT().HandleSessionCommand(cmd2).Return(nil)
	mockHandler.EXPECT().HandleSessionCommand(cmd3).Return(nil)

	gateway.Register(mockHandler)

	if err := gateway.Dispatch(cmd1); err != nil {
		t.Errorf("Dispatch(cmd1) error = %v", err)
	}
	if err := gateway.Dispatch(cmd2); err != nil {
		t.Errorf("Dispatch(cmd2) error = %v", err)
	}
	if err := gateway.Dispatch(cmd3); err != nil {
		t.Errorf("Dispatch(cmd3) error = %v", err)
	}
}

func TestGateway_Register_ReplaceHandler(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	gateway := NewGateway()
	mockHandler1 := NewMockHandler(ctrl)
	mockHandler2 := NewMockHandler(ctrl)

	cmd := SessionCommand{SessionID: "test", Type: "test", Content: "test"}

	// Register first handler
	gateway.Register(mockHandler1)

	// Replace with second handler
	gateway.Register(mockHandler2)

	// Second handler should receive the command
	mockHandler2.EXPECT().HandleSessionCommand(cmd).Return(nil)

	err := gateway.Dispatch(cmd)
	if err != nil {
		t.Errorf("Dispatch() error = %v", err)
	}
}

func TestHandlerFunc(t *testing.T) {
	called := false
	var receivedCmd SessionCommand

	handler := HandlerFunc(func(cmd SessionCommand) error {
		called = true
		receivedCmd = cmd
		return nil
	})

	cmd := SessionCommand{
		SessionID: "func-test",
		Type:      "test",
		Content:   "test content",
	}

	err := handler.HandleSessionCommand(cmd)

	if err != nil {
		t.Errorf("HandlerFunc error = %v", err)
	}
	if !called {
		t.Error("HandlerFunc was not called")
	}
	if receivedCmd.SessionID != cmd.SessionID {
		t.Errorf("receivedCmd.SessionID = %s, want %s", receivedCmd.SessionID, cmd.SessionID)
	}
}

func TestHandlerFunc_ReturnsError(t *testing.T) {
	expectedError := errors.New("handler function error")

	handler := HandlerFunc(func(cmd SessionCommand) error {
		return expectedError
	})

	cmd := SessionCommand{SessionID: "error-test"}

	err := handler.HandleSessionCommand(cmd)

	if err == nil {
		t.Error("HandlerFunc should return error")
	}
	if err != expectedError {
		t.Errorf("HandlerFunc error = %v, want %v", err, expectedError)
	}
}

func TestSessionCommand_Fields(t *testing.T) {
	cmd := SessionCommand{
		SessionID: "test-session-789",
		Type:      "execute-task",
		Content:   "implement feature X",
	}

	if cmd.SessionID != "test-session-789" {
		t.Errorf("SessionID = %s, want test-session-789", cmd.SessionID)
	}
	if cmd.Type != "execute-task" {
		t.Errorf("Type = %s, want execute-task", cmd.Type)
	}
	if cmd.Content != "implement feature X" {
		t.Errorf("Content = %s, want 'implement feature X'", cmd.Content)
	}
}
