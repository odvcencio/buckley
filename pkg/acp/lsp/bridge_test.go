package lsp

import (
	"bytes"
	"context"
	"testing"

	pb "github.com/odvcencio/buckley/pkg/acp/proto"
)

func TestNewLSPBridge(t *testing.T) {
	config := &BridgeConfig{
		CoordinatorAddr: "localhost:50051",
		AgentID:         "test-agent",
		Capabilities:    []string{"textDocument/completion", "textDocument/hover"},
	}

	bridge, err := NewBridge(config)
	if err != nil {
		t.Fatalf("NewBridge() error = %v, want nil", err)
	}

	if bridge == nil {
		t.Fatal("NewBridge() returned nil bridge")
	}

	if bridge.config != config {
		t.Error("NewBridge() did not store config correctly")
	}

	// Verify initial state
	if bridge.initializing {
		t.Error("NewBridge() initializing should be false")
	}

	if bridge.initialized {
		t.Error("NewBridge() initialized should be false")
	}

	if bridge.shutdown {
		t.Error("NewBridge() shutdown should be false")
	}
}

func TestNewLSPBridge_InvalidConfig(t *testing.T) {
	tests := []struct {
		name    string
		config  *BridgeConfig
		wantErr bool
	}{
		{
			name:    "nil config",
			config:  nil,
			wantErr: true,
		},
		{
			name: "valid config",
			config: &BridgeConfig{
				CoordinatorAddr: "localhost:50051",
				AgentID:         "test-agent",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bridge, err := NewBridge(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewBridge() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && bridge == nil {
				t.Error("NewBridge() returned nil bridge for valid config")
			}
		})
	}
}

func TestLSPBridge_Initialize(t *testing.T) {
	config := &BridgeConfig{
		CoordinatorAddr: "localhost:50051",
		AgentID:         "test-agent",
		Capabilities:    []string{"textDocument/completion"},
	}

	bridge, err := NewBridge(config)
	if err != nil {
		t.Fatalf("NewBridge() error = %v", err)
	}

	ctx := context.Background()
	processID := 1234
	params := InitializeParams{
		ProcessID: &processID,
		ClientInfo: &ClientInfo{
			Name:    "Zed",
			Version: "1.0.0",
		},
		RootURI: "file:///workspace",
		Capabilities: ClientCapabilities{
			TextDocument: &TextDocumentClientCapabilities{
				Synchronization: &TextDocumentSyncClientCapabilities{
					DynamicRegistration: true,
				},
			},
		},
	}

	result, err := bridge.Initialize(ctx, params)
	if err != nil {
		t.Fatalf("Initialize() error = %v, want nil", err)
	}

	if result == nil {
		t.Fatal("Initialize() returned nil result")
	}

	// Verify server info
	if result.ServerInfo == nil {
		t.Fatal("Initialize() ServerInfo is nil")
	}
	if result.ServerInfo.Name != "buckley-acp-bridge" {
		t.Errorf("ServerInfo.Name = %q, want %q", result.ServerInfo.Name, "buckley-acp-bridge")
	}
	if result.ServerInfo.Version != "1.0.0" {
		t.Errorf("ServerInfo.Version = %q, want %q", result.ServerInfo.Version, "1.0.0")
	}

	// Verify capabilities
	if result.Capabilities.TextDocumentSync == nil {
		t.Fatal("Initialize() TextDocumentSync is nil")
	}
	if !result.Capabilities.TextDocumentSync.OpenClose {
		t.Error("Initialize() OpenClose should be true")
	}
	if result.Capabilities.TextDocumentSync.Change != TextDocumentSyncKindFull {
		t.Errorf("Initialize() Change = %v, want %v", result.Capabilities.TextDocumentSync.Change, TextDocumentSyncKindFull)
	}
}

func TestLSPBridge_Initialize_AlreadyInitialized(t *testing.T) {
	config := &BridgeConfig{
		CoordinatorAddr: "localhost:50051",
		AgentID:         "test-agent",
	}

	bridge, err := NewBridge(config)
	if err != nil {
		t.Fatalf("NewBridge() error = %v", err)
	}

	ctx := context.Background()
	params := InitializeParams{
		RootURI: "file:///workspace",
	}

	// First initialize should succeed
	_, err = bridge.Initialize(ctx, params)
	if err != nil {
		t.Fatalf("First Initialize() error = %v, want nil", err)
	}

	// Second initialize should fail
	_, err = bridge.Initialize(ctx, params)
	if err == nil {
		t.Error("Second Initialize() error = nil, want error")
	}
}

func TestLSPBridge_Initialized(t *testing.T) {
	config := &BridgeConfig{
		CoordinatorAddr: "localhost:50051",
		AgentID:         "test-agent",
	}

	bridge, err := NewBridge(config)
	if err != nil {
		t.Fatalf("NewBridge() error = %v", err)
	}

	ctx := context.Background()

	// Initialized should fail if not initializing
	err = bridge.Initialized(ctx)
	if err == nil {
		t.Error("Initialized() without Initialize() should return error")
	}

	// Initialize first
	params := InitializeParams{
		RootURI: "file:///workspace",
	}
	_, err = bridge.Initialize(ctx, params)
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	// Now Initialized should succeed
	err = bridge.Initialized(ctx)
	if err != nil {
		t.Fatalf("Initialized() error = %v, want nil", err)
	}

	// Verify initialized flag is set and initializing is cleared
	bridge.mu.RLock()
	initialized := bridge.initialized
	initializing := bridge.initializing
	bridge.mu.RUnlock()

	if !initialized {
		t.Error("Initialized() did not set initialized flag")
	}
	if initializing {
		t.Error("Initialized() did not clear initializing flag")
	}
}

func TestLSPBridge_Shutdown(t *testing.T) {
	config := &BridgeConfig{
		CoordinatorAddr: "localhost:50051",
		AgentID:         "test-agent",
	}

	bridge, err := NewBridge(config)
	if err != nil {
		t.Fatalf("NewBridge() error = %v", err)
	}

	ctx := context.Background()

	// Shutdown without initialize should fail
	err = bridge.Shutdown(ctx)
	if err == nil {
		t.Error("Shutdown() before Initialize() should return error")
	}

	// Initialize and Initialized notifications
	params := InitializeParams{
		RootURI: "file:///workspace",
	}
	_, err = bridge.Initialize(ctx, params)
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	err = bridge.Initialized(ctx)
	if err != nil {
		t.Fatalf("Initialized() error = %v", err)
	}

	// Now shutdown should succeed
	err = bridge.Shutdown(ctx)
	if err != nil {
		t.Fatalf("Shutdown() error = %v, want nil", err)
	}

	// Verify shutdown flag is set
	bridge.mu.RLock()
	shutdown := bridge.shutdown
	bridge.mu.RUnlock()

	if !shutdown {
		t.Error("Shutdown() did not set shutdown flag")
	}
}

func TestLSPBridge_Exit(t *testing.T) {
	config := &BridgeConfig{
		CoordinatorAddr: "localhost:50051",
		AgentID:         "test-agent",
	}

	bridge, err := NewBridge(config)
	if err != nil {
		t.Fatalf("NewBridge() error = %v", err)
	}

	ctx := context.Background()

	// Exit should always succeed
	err = bridge.Exit(ctx)
	if err != nil {
		t.Errorf("Exit() error = %v, want nil", err)
	}
}

func TestLSPBridge_ServeStdio(t *testing.T) {
	config := &BridgeConfig{
		CoordinatorAddr: "localhost:50051",
		AgentID:         "test-agent",
	}

	bridge, err := NewBridge(config)
	if err != nil {
		t.Fatalf("NewBridge() error = %v", err)
	}

	ctx := context.Background()
	reader := &bytes.Buffer{}
	writer := &bytes.Buffer{}

	// Basic test - just verify ServeStdio can be called
	// Full JSON-RPC protocol testing will come in later tasks
	err = bridge.ServeStdio(ctx, reader, writer)
	if err != nil {
		t.Errorf("ServeStdio() error = %v, want nil", err)
	}
}

func TestLSPBridge_InlineCompletion_NotConnected(t *testing.T) {
	config := &BridgeConfig{
		CoordinatorAddr: "localhost:50051",
		AgentID:         "test-agent",
	}

	bridge, err := NewBridge(config)
	if err != nil {
		t.Fatalf("NewBridge() error = %v", err)
	}

	ctx := context.Background()
	_, err = bridge.StreamInlineCompletions(ctx, &pb.InlineCompletionRequest{
		AgentId: "test-agent",
		Context: &pb.EditorContext{Document: &pb.DocumentSnapshot{Uri: "file:///tmp/x", Content: "x"}},
	}, nil)
	if err == nil {
		t.Fatal("StreamInlineCompletions expected error when not initialized")
	}
}

func TestLSPBridge_ProposeApply_NotConnected(t *testing.T) {
	config := &BridgeConfig{
		CoordinatorAddr: "localhost:50051",
		AgentID:         "test-agent",
	}

	bridge, err := NewBridge(config)
	if err != nil {
		t.Fatalf("NewBridge() error = %v", err)
	}

	ctx := context.Background()
	if _, err := bridge.ProposeEdits(ctx, &pb.ProposeEditsRequest{
		AgentId:     "a",
		SessionId:   "s",
		Instruction: "x",
		Context:     &pb.EditorContext{Document: &pb.DocumentSnapshot{Uri: "file:///tmp/x", Content: "x"}},
	}); err == nil {
		t.Fatal("ProposeEdits expected error when not initialized")
	}

	if _, err := bridge.ApplyEdits(ctx, &pb.ApplyEditsRequest{
		AgentId:   "a",
		SessionId: "s",
		Edits:     []*pb.TextEdit{{Uri: "file:///tmp/x", NewText: "y"}},
	}); err == nil {
		t.Fatal("ApplyEdits expected error when not initialized")
	}

	if _, err := bridge.UpdateEditorState(ctx, &pb.UpdateEditorStateRequest{
		AgentId:   "a",
		SessionId: "s",
		Context:   &pb.EditorContext{Document: &pb.DocumentSnapshot{Uri: "file:///tmp/x", Content: "x"}},
	}); err == nil {
		t.Fatal("UpdateEditorState expected error when not initialized")
	}
}

func TestApplyTextEdits(t *testing.T) {
	content := "hello world\n"
	edits := []*pb.TextEdit{
		{
			Range: &pb.Range{
				Start: &pb.Position{Line: 0, Character: 6},
				End:   &pb.Position{Line: 0, Character: 11},
			},
			NewText: "zed",
		},
	}

	updated, err := ApplyTextEdits(content, edits)
	if err != nil {
		t.Fatalf("ApplyTextEdits error: %v", err)
	}
	if updated != "hello zed\n" {
		t.Fatalf("unexpected content: %q", updated)
	}
}

func TestLSPBridge_CapabilityNegotiation(t *testing.T) {
	tests := []struct {
		name         string
		capabilities []string
		wantSync     bool
	}{
		{
			name:         "basic capabilities",
			capabilities: []string{"textDocument/completion"},
			wantSync:     true,
		},
		{
			name:         "multiple capabilities",
			capabilities: []string{"textDocument/completion", "textDocument/hover", "textDocument/definition"},
			wantSync:     true,
		},
		{
			name:         "no capabilities",
			capabilities: []string{},
			wantSync:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &BridgeConfig{
				CoordinatorAddr: "localhost:50051",
				AgentID:         "test-agent",
				Capabilities:    tt.capabilities,
			}

			bridge, err := NewBridge(config)
			if err != nil {
				t.Fatalf("NewBridge() error = %v", err)
			}

			ctx := context.Background()
			params := InitializeParams{
				RootURI: "file:///workspace",
			}

			result, err := bridge.Initialize(ctx, params)
			if err != nil {
				t.Fatalf("Initialize() error = %v", err)
			}

			// Verify text document sync is always advertised
			if tt.wantSync {
				if result.Capabilities.TextDocumentSync == nil {
					t.Error("Initialize() should advertise TextDocumentSync capability")
				}
			}
		})
	}
}
