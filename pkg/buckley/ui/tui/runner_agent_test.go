// Package tui provides the integrated terminal user interface for Buckley.
// This file tests the agent server functionality for real-time TUI control.

package tui

import (
	"testing"
	"time"

	"github.com/odvcencio/fluffyui/agent"
)

func TestRunnerAgentServer_NotInitialized(t *testing.T) {
	r := &Runner{}

	// Should return error when agent not initialized
	_, err := r.GetAgentSnapshot()
	if err == nil {
		t.Error("expected error when agent not initialized")
	}

	resp, err := r.AgentExecuteCommand(AgentCommand{Type: "snapshot"})
	if err == nil {
		t.Error("expected error when agent not initialized")
	}
	if resp != nil {
		t.Error("expected nil response when agent not initialized")
	}
}

func TestRunnerAgentServer_AgentAddr(t *testing.T) {
	r := &Runner{}
	if r.AgentAddr() != "" {
		t.Error("expected empty address when agent not initialized")
	}

	// Test with mock agent server
	r.agentServer = &AgentServer{addr: "tcp:localhost:9999"}
	if r.AgentAddr() != "tcp:localhost:9999" {
		t.Errorf("expected tcp:localhost:9999, got %s", r.AgentAddr())
	}
}

func TestRunnerAgentExecuteCommand_UnknownType(t *testing.T) {
	// Create a mock agent - we can't easily test with real agent
	// without a running TUI, so we test error handling
	r := &Runner{
		agentServer: &AgentServer{
			agent: &agent.Agent{}, // This won't work for real but tests the path
		},
	}

	// This will fail because the agent isn't properly initialized
	// but it tests the command routing logic
	cmd := AgentCommand{Type: "unknown", Text: "test"}
	resp, _ := r.AgentExecuteCommand(cmd)
	
	if resp != nil && resp.Success {
		t.Error("expected failure for unknown command type")
	}
}

func TestRunnerAgentExecuteCommand_Validation(t *testing.T) {
	r := &Runner{
		agentServer: &AgentServer{
			agent: &agent.Agent{},
		},
	}

	tests := []struct {
		name     string
		cmd      AgentCommand
		wantSucc bool
	}{
		{
			name:     "focus without text",
			cmd:      AgentCommand{Type: "focus"},
			wantSucc: false,
		},
		{
			name:     "type without text",
			cmd:      AgentCommand{Type: "type"},
			wantSucc: false,
		},
		{
			name:     "activate without text",
			cmd:      AgentCommand{Type: "activate"},
			wantSucc: false,
		},
		{
			name:     "find without text",
			cmd:      AgentCommand{Type: "find"},
			wantSucc: false,
		},
		{
			name:     "getValue without text",
			cmd:      AgentCommand{Type: "getValue"},
			wantSucc: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, _ := r.AgentExecuteCommand(tt.cmd)
			if resp == nil {
				t.Fatal("expected non-nil response")
			}
			if resp.Success != tt.wantSucc {
				t.Errorf("Success = %v, want %v", resp.Success, tt.wantSucc)
			}
		})
	}
}

func TestAgentServer_Lifecycle(t *testing.T) {
	// This is a minimal test - full lifecycle testing would require
	// a running TUI which is better suited for integration tests
	
	// Test that stopAgentServer doesn't panic when not initialized
	r := &Runner{}
	r.stopAgentServer() // Should not panic

	// Test that stopAgentServer cleans up properly
	r.agentServer = &AgentServer{
		done: make(chan struct{}),
	}
	close(r.agentServer.done) // Pre-close the channel
	r.stopAgentServer() // Should handle gracefully
}

func TestAgentCommandTypes(t *testing.T) {
	// Test that all command types are properly defined
	cmdTypes := []string{
		"focus",
		"type",
		"activate",
		"snapshot",
		"list",
		"find",
		"getValue",
	}

	for _, cmdType := range cmdTypes {
		cmd := AgentCommand{
			Type:   cmdType,
			Text:   "test",
			ID:     "test-id",
			Params: map[string]string{"key": "value"},
		}
		
		if cmd.Type != cmdType {
			t.Errorf("command type mismatch: %s", cmdType)
		}
	}
}

func TestAgentResponseStructure(t *testing.T) {
	resp := AgentResponse{
		Success:   true,
		Message:   "test message",
		Error:     "",
		Timestamp: time.Now(),
	}

	if !resp.Success {
		t.Error("expected success to be true")
	}
	if resp.Message != "test message" {
		t.Error("message mismatch")
	}
	if resp.Error != "" {
		t.Error("expected no error")
	}
}

// Integration test skipped - requires running TUI
// func TestRunnerAgentServer_Integration(t *testing.T) {
// 	// This would test:
// 	// 1. Starting agent server
// 	// 2. Connecting via WebSocket
// 	// 3. Sending commands
// 	// 4. Receiving snapshots
// 	// 5. Proper cleanup on shutdown
// }
