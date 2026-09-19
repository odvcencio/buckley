package lsp

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"m31labs.dev/buckley/pkg/acp/partialresult"
	pb "m31labs.dev/buckley/pkg/acp/proto"
	buckleyversion "m31labs.dev/buckley/pkg/version"
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
	if result.ServerInfo.Version != buckleyversion.Release {
		t.Errorf("ServerInfo.Version = %q, want %q", result.ServerInfo.Version, buckleyversion.Release)
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

func TestBridge_HandleTextQuery_OrdinaryErrorAndNilResponseAreSafe(t *testing.T) {
	bridge := newInitializedBridgeForBoundaryTest(t)
	const secret = "hostile text-query secret"
	bridge.grpcClient = &mockAgentCommunicationClient{
		sendMessageFunc: func(context.Context, *pb.SendMessageRequest, ...grpc.CallOption) (*pb.SendMessageResponse, error) {
			return nil, status.Error(codes.Internal, "raw upstream "+secret)
		},
	}

	response, err := bridge.HandleTextQuery(context.Background(), "hello")
	if response != "" {
		t.Fatalf("response = %q, want no accepted text", response)
	}
	if err == nil || err.Error() != "coordinator error" {
		t.Fatalf("error = %v, want stable coordinator error", err)
	}
	assertErrorChainOmits(t, err, secret)
	if errors.Unwrap(err) != nil {
		t.Fatalf("ordinary error unwrap = %v, want nil", errors.Unwrap(err))
	}

	bridge.grpcClient = &mockAgentCommunicationClient{
		sendMessageFunc: func(context.Context, *pb.SendMessageRequest, ...grpc.CallOption) (*pb.SendMessageResponse, error) {
			return nil, nil
		},
	}
	response, err = bridge.HandleTextQuery(context.Background(), "hello")
	if response != "" {
		t.Fatalf("nil response text = %q, want none", response)
	}
	if err == nil || err.Error() != "empty response from coordinator" {
		t.Fatalf("nil response error = %v, want stable empty response error", err)
	}
}

func TestBridge_HandleTextQuery_TypedPartialSanitizesCause(t *testing.T) {
	bridge := newInitializedBridgeForBoundaryTest(t)
	const secret = "hostile text partial cause"
	bridge.grpcClient = &mockAgentCommunicationClient{
		sendMessageFunc: func(context.Context, *pb.SendMessageRequest, ...grpc.CallOption) (*pb.SendMessageResponse, error) {
			return nil, typedPartialStatusError(t, codes.ResourceExhausted, "raw text "+secret, "public text draft")
		},
	}

	response, err := bridge.HandleTextQuery(context.Background(), "hello")
	if response != "" {
		t.Fatalf("response = %q, want no accepted text", response)
	}
	var incomplete *partialresult.IncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("error = %T %v, want partialresult.IncompleteError", err, err)
	}
	assertErrorChainOmits(t, err, secret)
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("typed partial code = %v, want ResourceExhausted", status.Code(err))
	}
	if incomplete.Result.GetPartialResponse().GetContent() != "public text draft" {
		t.Fatalf("partial response = %+v", incomplete.Result.GetPartialResponse())
	}
}

func TestBridge_EditorUnaryErrorsAreSafeAndFailClosed(t *testing.T) {
	bridge := newInitializedBridgeForBoundaryTest(t)
	const secret = "hostile editor secret"
	client := &mockEditorBridgeClient{
		proposeFunc: func(context.Context, *pb.ProposeEditsRequest, ...grpc.CallOption) (*pb.ProposeEditsResponse, error) {
			return &pb.ProposeEditsResponse{Summary: "must not be accepted"}, status.Error(codes.PermissionDenied, "raw propose "+secret)
		},
		applyFunc: func(context.Context, *pb.ApplyEditsRequest, ...grpc.CallOption) (*pb.ApplyEditsResponse, error) {
			return &pb.ApplyEditsResponse{Applied: true, Message: "must not be accepted"}, status.Error(codes.Internal, "raw apply "+secret)
		},
		updateFunc: func(context.Context, *pb.UpdateEditorStateRequest, ...grpc.CallOption) (*pb.UpdateEditorStateResponse, error) {
			return &pb.UpdateEditorStateResponse{PlanState: "must not be accepted"}, errors.New("raw update " + secret)
		},
	}
	bridge.grpcClient = client

	proposed, err := bridge.ProposeEdits(context.Background(), &pb.ProposeEditsRequest{Instruction: "x"})
	if proposed != nil || err == nil || err.Error() != "propose edits failed" {
		t.Fatalf("ProposeEdits response=%+v error=%v, want nil safe failure", proposed, err)
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("ProposeEdits code = %v, want PermissionDenied", status.Code(err))
	}
	assertErrorChainOmits(t, err, secret)

	applied, err := bridge.ApplyEdits(context.Background(), &pb.ApplyEditsRequest{Edits: []*pb.TextEdit{{Uri: "file:///tmp/x", NewText: "x"}}})
	if applied != nil || err == nil || err.Error() != "apply edits failed" {
		t.Fatalf("ApplyEdits response=%+v error=%v, want nil safe failure", applied, err)
	}
	if status.Code(err) != codes.Internal {
		t.Fatalf("ApplyEdits code = %v, want Internal", status.Code(err))
	}
	assertErrorChainOmits(t, err, secret)

	state, err := bridge.UpdateEditorState(context.Background(), &pb.UpdateEditorStateRequest{})
	if state != nil || err == nil || err.Error() != "update editor state failed" {
		t.Fatalf("UpdateEditorState response=%+v error=%v, want nil safe failure", state, err)
	}
	if errors.Unwrap(err) != nil {
		t.Fatalf("UpdateEditorState unwrap = %v, want nil", errors.Unwrap(err))
	}
	assertErrorChainOmits(t, err, secret)
}

func TestBridge_EditorUnaryNilSuccessResponsesAreSafe(t *testing.T) {
	bridge := newInitializedBridgeForBoundaryTest(t)
	bridge.grpcClient = &mockEditorBridgeClient{
		proposeFunc: func(context.Context, *pb.ProposeEditsRequest, ...grpc.CallOption) (*pb.ProposeEditsResponse, error) {
			return nil, nil
		},
		applyFunc: func(context.Context, *pb.ApplyEditsRequest, ...grpc.CallOption) (*pb.ApplyEditsResponse, error) {
			return nil, nil
		},
		updateFunc: func(context.Context, *pb.UpdateEditorStateRequest, ...grpc.CallOption) (*pb.UpdateEditorStateResponse, error) {
			return nil, nil
		},
	}

	if response, err := bridge.ProposeEdits(context.Background(), &pb.ProposeEditsRequest{Instruction: "x"}); response != nil || err == nil || err.Error() != "propose edits unavailable" {
		t.Fatalf("ProposeEdits nil success response=%+v error=%v", response, err)
	}
	if response, err := bridge.ApplyEdits(context.Background(), &pb.ApplyEditsRequest{}); response != nil || err == nil || err.Error() != "apply edits unavailable" {
		t.Fatalf("ApplyEdits nil success response=%+v error=%v", response, err)
	}
	if response, err := bridge.UpdateEditorState(context.Background(), &pb.UpdateEditorStateRequest{}); response != nil || err == nil || err.Error() != "update editor state unavailable" {
		t.Fatalf("UpdateEditorState nil success response=%+v error=%v", response, err)
	}
}

func newInitializedBridgeForBoundaryTest(t *testing.T) *Bridge {
	t.Helper()
	bridge, err := NewBridge(&BridgeConfig{CoordinatorAddr: "localhost:50051", AgentID: "test-agent"})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	if _, err := bridge.Initialize(context.Background(), InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := bridge.Initialized(context.Background()); err != nil {
		t.Fatalf("Initialized: %v", err)
	}
	return bridge
}

type mockEditorBridgeClient struct {
	pb.AgentCommunicationClient
	proposeFunc func(context.Context, *pb.ProposeEditsRequest, ...grpc.CallOption) (*pb.ProposeEditsResponse, error)
	applyFunc   func(context.Context, *pb.ApplyEditsRequest, ...grpc.CallOption) (*pb.ApplyEditsResponse, error)
	updateFunc  func(context.Context, *pb.UpdateEditorStateRequest, ...grpc.CallOption) (*pb.UpdateEditorStateResponse, error)
}

func (m *mockEditorBridgeClient) ProposeEdits(ctx context.Context, req *pb.ProposeEditsRequest, opts ...grpc.CallOption) (*pb.ProposeEditsResponse, error) {
	if m.proposeFunc != nil {
		return m.proposeFunc(ctx, req, opts...)
	}
	return nil, errors.New("proposeFunc not set")
}

func (m *mockEditorBridgeClient) ApplyEdits(ctx context.Context, req *pb.ApplyEditsRequest, opts ...grpc.CallOption) (*pb.ApplyEditsResponse, error) {
	if m.applyFunc != nil {
		return m.applyFunc(ctx, req, opts...)
	}
	return nil, errors.New("applyFunc not set")
}

func (m *mockEditorBridgeClient) UpdateEditorState(ctx context.Context, req *pb.UpdateEditorStateRequest, opts ...grpc.CallOption) (*pb.UpdateEditorStateResponse, error) {
	if m.updateFunc != nil {
		return m.updateFunc(ctx, req, opts...)
	}
	return nil, errors.New("updateFunc not set")
}

func assertErrorChainOmits(t *testing.T, err error, secret string) {
	t.Helper()
	for current := err; current != nil; current = errors.Unwrap(current) {
		if strings.Contains(current.Error(), secret) {
			t.Fatalf("error chain leaked secret in %T: %q", current, current.Error())
		}
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
