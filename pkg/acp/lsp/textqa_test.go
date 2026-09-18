package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"m31labs.dev/buckley/pkg/acp/partialresult"
	pb "m31labs.dev/buckley/pkg/acp/proto"
)

// mockAgentCommunicationClient is a mock implementation of the gRPC client
type mockAgentCommunicationClient struct {
	pb.AgentCommunicationClient
	sendMessageFunc func(ctx context.Context, req *pb.SendMessageRequest, opts ...grpc.CallOption) (*pb.SendMessageResponse, error)
}

func (m *mockAgentCommunicationClient) SendMessage(ctx context.Context, req *pb.SendMessageRequest, opts ...grpc.CallOption) (*pb.SendMessageResponse, error) {
	if m.sendMessageFunc != nil {
		return m.sendMessageFunc(ctx, req, opts...)
	}
	return nil, errors.New("sendMessageFunc not set")
}

func TestBridge_HandleTextQuery(t *testing.T) {
	// Create bridge
	config := &BridgeConfig{
		CoordinatorAddr: "localhost:50051",
		AgentID:         "test-agent",
	}
	bridge, err := NewBridge(config)
	require.NoError(t, err)

	// Initialize the bridge
	ctx := context.Background()
	_, err = bridge.Initialize(ctx, InitializeParams{})
	require.NoError(t, err)
	err = bridge.Initialized(ctx)
	require.NoError(t, err)

	// Set up mock gRPC client
	mockClient := &mockAgentCommunicationClient{
		sendMessageFunc: func(ctx context.Context, req *pb.SendMessageRequest, opts ...grpc.CallOption) (*pb.SendMessageResponse, error) {
			assert.Equal(t, "test-agent", req.AgentId)
			assert.Equal(t, "What is the weather?", req.Message.Content)
			assert.Equal(t, "user", req.Message.Role)

			return &pb.SendMessageResponse{
				Response: &pb.Message{
					Role:    "assistant",
					Content: "The weather is sunny and 72 degrees.",
				},
			}, nil
		},
	}
	bridge.grpcClient = mockClient

	// Test successful query
	response, err := bridge.HandleTextQuery(ctx, "What is the weather?")
	require.NoError(t, err)
	assert.Equal(t, "The weather is sunny and 72 degrees.", response)
}

func TestBridge_HandleTextQuery_EmptyQuery(t *testing.T) {
	config := &BridgeConfig{
		CoordinatorAddr: "localhost:50051",
		AgentID:         "test-agent",
	}
	bridge, err := NewBridge(config)
	require.NoError(t, err)

	// Initialize the bridge
	ctx := context.Background()
	_, err = bridge.Initialize(ctx, InitializeParams{})
	require.NoError(t, err)
	err = bridge.Initialized(ctx)
	require.NoError(t, err)

	// Set up mock client
	bridge.grpcClient = &mockAgentCommunicationClient{}

	// Test with empty query
	_, err = bridge.HandleTextQuery(ctx, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query cannot be empty")

	// Test with whitespace-only query
	_, err = bridge.HandleTextQuery(ctx, "   \t\n  ")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query cannot be empty")
}

func TestBridge_HandleTextQuery_CoordinatorError(t *testing.T) {
	config := &BridgeConfig{
		CoordinatorAddr: "localhost:50051",
		AgentID:         "test-agent",
	}
	bridge, err := NewBridge(config)
	require.NoError(t, err)

	// Initialize the bridge
	ctx := context.Background()
	_, err = bridge.Initialize(ctx, InitializeParams{})
	require.NoError(t, err)
	err = bridge.Initialized(ctx)
	require.NoError(t, err)

	// Set up mock client that returns an error
	mockClient := &mockAgentCommunicationClient{
		sendMessageFunc: func(ctx context.Context, req *pb.SendMessageRequest, opts ...grpc.CallOption) (*pb.SendMessageResponse, error) {
			return nil, status.Error(codes.Unavailable, "coordinator unavailable")
		},
	}
	bridge.grpcClient = mockClient

	// Test coordinator error
	_, err = bridge.HandleTextQuery(ctx, "test query")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "coordinator error")
}

func TestBridge_HandleTextQuery_PreservesTypedPartialResultOnError(t *testing.T) {
	config := &BridgeConfig{
		CoordinatorAddr: "localhost:50051",
		AgentID:         "test-agent",
	}
	bridge, err := NewBridge(config)
	require.NoError(t, err)

	ctx := context.Background()
	_, err = bridge.Initialize(ctx, InitializeParams{})
	require.NoError(t, err)
	require.NoError(t, bridge.Initialized(ctx))

	const private = "private reasoning sentinel"
	partialErr := partialresult.StatusError(codes.Internal, "ACP response incomplete", &pb.PartialResult{
		ReasonCode: "provider_error",
		SafeError:  "raw provider secret " + private,
		PartialResponse: &pb.Message{
			Role:    "assistant",
			Content: "public partial draft",
		},
		TaskResults: []*pb.PartialTaskResult{{
			TaskId:       "task-1",
			Status:       "incomplete",
			Summary:      "public task summary",
			Error:        "safe task error",
			FinishReason: "length",
			Usage: &pb.PartialUsage{
				InputTokens:               5,
				OutputTokens:              3,
				ReportedTotalTokens:       8,
				UsageEvidencePresent:      true,
				UsageEvidenceMissing:      true,
				ReportedReasoningTokens:   ptrInt64(1),
				ReportedCachedInputTokens: ptrInt64(2),
				Estimated:                 true,
			},
			ToolCallCount: 1,
			CommandCount:  2,
		}},
		ModelIdentities: []*pb.PartialModelIdentity{{
			RequestedModel: "alias",
			SelectedModel:  "provider/model",
			ProviderId:     "openai",
			ResponseModel:  "gpt-4o",
			ResponseId:     "chatcmpl-partial",
		}},
	})
	mockClient := &mockAgentCommunicationClient{
		sendMessageFunc: func(ctx context.Context, req *pb.SendMessageRequest, opts ...grpc.CallOption) (*pb.SendMessageResponse, error) {
			return nil, partialErr
		},
	}
	bridge.grpcClient = mockClient

	response, err := bridge.HandleTextQuery(ctx, "test query")
	if response != "" {
		t.Fatalf("response = %q, want no accepted text", response)
	}
	var incomplete *partialresult.IncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("error = %T %v, want partialresult.IncompleteError", err, err)
	}
	got := incomplete.Result
	if got.GetPartialResponse().GetContent() != "public partial draft" {
		t.Fatalf("partial draft = %q", got.GetPartialResponse().GetContent())
	}
	if got.GetSafeError() != "ACP response incomplete" || strings.Contains(got.String(), private) {
		t.Fatalf("partial result unsafe: %+v", got)
	}
	if len(got.GetTaskResults()) != 1 || len(got.GetModelIdentities()) != 1 {
		t.Fatalf("partial evidence rows = tasks %d identities %d", len(got.GetTaskResults()), len(got.GetModelIdentities()))
	}
}

func TestBridge_HandleTextQuery_EmptyResponse(t *testing.T) {
	config := &BridgeConfig{
		CoordinatorAddr: "localhost:50051",
		AgentID:         "test-agent",
	}
	bridge, err := NewBridge(config)
	require.NoError(t, err)

	// Initialize the bridge
	ctx := context.Background()
	_, err = bridge.Initialize(ctx, InitializeParams{})
	require.NoError(t, err)
	err = bridge.Initialized(ctx)
	require.NoError(t, err)

	// Set up mock client that returns nil response
	mockClient := &mockAgentCommunicationClient{
		sendMessageFunc: func(ctx context.Context, req *pb.SendMessageRequest, opts ...grpc.CallOption) (*pb.SendMessageResponse, error) {
			return &pb.SendMessageResponse{
				Response: nil,
			}, nil
		},
	}
	bridge.grpcClient = mockClient

	// Test empty response
	_, err = bridge.HandleTextQuery(ctx, "test query")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty response from coordinator")
}

func TestBridge_HandleTextQuery_Timeout(t *testing.T) {
	config := &BridgeConfig{
		CoordinatorAddr: "localhost:50051",
		AgentID:         "test-agent",
	}
	bridge, err := NewBridge(config)
	require.NoError(t, err)

	// Initialize the bridge
	ctx := context.Background()
	_, err = bridge.Initialize(ctx, InitializeParams{})
	require.NoError(t, err)
	err = bridge.Initialized(ctx)
	require.NoError(t, err)

	// Set up mock client that simulates a timeout
	mockClient := &mockAgentCommunicationClient{
		sendMessageFunc: func(ctx context.Context, req *pb.SendMessageRequest, opts ...grpc.CallOption) (*pb.SendMessageResponse, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	bridge.grpcClient = mockClient

	// Create a context with timeout
	ctxWithTimeout, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()

	// Test timeout
	_, err = bridge.HandleTextQuery(ctxWithTimeout, "test query")
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled))
}

func TestBridge_HandleTextQuery_NotInitialized(t *testing.T) {
	config := &BridgeConfig{
		CoordinatorAddr: "localhost:50051",
		AgentID:         "test-agent",
	}
	bridge, err := NewBridge(config)
	require.NoError(t, err)

	// Do NOT initialize the bridge

	ctx := context.Background()
	_, err = bridge.HandleTextQuery(ctx, "test query")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bridge not initialized")
}

func TestBridge_HandleTextQuery_NotConnected(t *testing.T) {
	config := &BridgeConfig{
		CoordinatorAddr: "localhost:50051",
		AgentID:         "test-agent",
	}
	bridge, err := NewBridge(config)
	require.NoError(t, err)

	// Initialize the bridge but don't connect
	ctx := context.Background()
	_, err = bridge.Initialize(ctx, InitializeParams{})
	require.NoError(t, err)
	err = bridge.Initialized(ctx)
	require.NoError(t, err)

	// Test without connection
	_, err = bridge.HandleTextQuery(ctx, "test query")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not connected to coordinator")
}

func TestBridge_ConnectToCoordinator(t *testing.T) {
	// This test is a placeholder for actual gRPC connection testing
	// In a real scenario, you'd use a mock gRPC server
	config := &BridgeConfig{
		CoordinatorAddr: "localhost:50051",
		AgentID:         "test-agent",
	}
	bridge, err := NewBridge(config)
	require.NoError(t, err)

	// Note: This will fail without a real server, which is expected in unit tests
	// Integration tests should handle actual connections
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err = bridge.ConnectToCoordinator(ctx)
	// We expect an error since there's no server
	assert.Error(t, err)
}

func TestBridge_ConnectToCoordinator_AlreadyConnected(t *testing.T) {
	config := &BridgeConfig{
		CoordinatorAddr: "localhost:50051",
		AgentID:         "test-agent",
	}
	bridge, err := NewBridge(config)
	require.NoError(t, err)

	// Simulate already connected by setting grpcConn to a non-nil value
	bridge.grpcConn = &grpc.ClientConn{}

	ctx := context.Background()
	err = bridge.ConnectToCoordinator(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already connected")
}

func TestBridge_DisconnectFromCoordinator(t *testing.T) {
	config := &BridgeConfig{
		CoordinatorAddr: "localhost:50051",
		AgentID:         "test-agent",
	}
	bridge, err := NewBridge(config)
	require.NoError(t, err)

	// Test disconnect when not connected (should not error)
	err = bridge.DisconnectFromCoordinator()
	require.NoError(t, err)

	// Note: Testing actual disconnection requires a real connection
	// which is better suited for integration tests
}

func TestHandleMessage_TextQuery(t *testing.T) {
	config := &BridgeConfig{
		CoordinatorAddr: "localhost:50051",
		AgentID:         "test-agent",
	}
	bridge, err := NewBridge(config)
	require.NoError(t, err)

	// Initialize the bridge
	ctx := context.Background()
	_, err = bridge.Initialize(ctx, InitializeParams{})
	require.NoError(t, err)
	err = bridge.Initialized(ctx)
	require.NoError(t, err)

	// Set up mock gRPC client
	mockClient := &mockAgentCommunicationClient{
		sendMessageFunc: func(ctx context.Context, req *pb.SendMessageRequest, opts ...grpc.CallOption) (*pb.SendMessageResponse, error) {
			return &pb.SendMessageResponse{
				Response: &pb.Message{
					Role:    "assistant",
					Content: "Test response",
				},
			}, nil
		},
	}
	bridge.grpcClient = mockClient

	// Create a buckley/textQuery request
	params := TextQueryParams{
		Query: "test query",
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	id := json.RawMessage(`1`)
	msg := &JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  "buckley/textQuery",
		Params:  paramsJSON,
	}

	// Handle the message
	response := bridge.HandleMessage(ctx, msg)
	require.NotNil(t, response)
	assert.Equal(t, "2.0", response.JSONRPC)
	assert.Equal(t, &id, response.ID)
	assert.Nil(t, response.Error)

	// Parse result
	var result TextQueryResult
	err = json.Unmarshal(response.Result, &result)
	require.NoError(t, err)
	assert.Equal(t, "Test response", result.Response)
	assert.Equal(t, "test-agent", result.AgentID)
}

func TestHandleMessage_TextQuery_InvalidParams(t *testing.T) {
	config := &BridgeConfig{
		CoordinatorAddr: "localhost:50051",
		AgentID:         "test-agent",
	}
	bridge, err := NewBridge(config)
	require.NoError(t, err)

	// Initialize the bridge
	ctx := context.Background()
	_, err = bridge.Initialize(ctx, InitializeParams{})
	require.NoError(t, err)
	err = bridge.Initialized(ctx)
	require.NoError(t, err)

	// Create a request with malformed JSON params
	id := json.RawMessage(`1`)
	msg := &JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  "buckley/textQuery",
		Params:  json.RawMessage(`{invalid json}`),
	}

	// Handle the message
	response := bridge.HandleMessage(ctx, msg)
	require.NotNil(t, response)
	assert.Equal(t, "2.0", response.JSONRPC)
	assert.Equal(t, &id, response.ID)
	require.NotNil(t, response.Error)
	assert.Equal(t, InvalidParams, response.Error.Code)
}

func TestHandleMessage_TextQuery_HandlerError(t *testing.T) {
	config := &BridgeConfig{
		CoordinatorAddr: "localhost:50051",
		AgentID:         "test-agent",
	}
	bridge, err := NewBridge(config)
	require.NoError(t, err)

	// Initialize the bridge
	ctx := context.Background()
	_, err = bridge.Initialize(ctx, InitializeParams{})
	require.NoError(t, err)
	err = bridge.Initialized(ctx)
	require.NoError(t, err)

	// Set up mock client that returns an error
	mockClient := &mockAgentCommunicationClient{
		sendMessageFunc: func(ctx context.Context, req *pb.SendMessageRequest, opts ...grpc.CallOption) (*pb.SendMessageResponse, error) {
			return nil, errors.New("test error")
		},
	}
	bridge.grpcClient = mockClient

	// Create a buckley/textQuery request
	params := TextQueryParams{
		Query: "test query",
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	id := json.RawMessage(`1`)
	msg := &JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  "buckley/textQuery",
		Params:  paramsJSON,
	}

	// Handle the message
	response := bridge.HandleMessage(ctx, msg)
	require.NotNil(t, response)
	assert.Equal(t, "2.0", response.JSONRPC)
	assert.Equal(t, &id, response.ID)
	require.NotNil(t, response.Error)
	assert.Equal(t, InternalError, response.Error.Code)
	assert.Contains(t, response.Error.Message, "coordinator error")
}

func TestHandleMessage_TextQuery_PartialErrorData(t *testing.T) {
	config := &BridgeConfig{
		CoordinatorAddr: "localhost:50051",
		AgentID:         "test-agent",
	}
	bridge, err := NewBridge(config)
	require.NoError(t, err)

	ctx := context.Background()
	_, err = bridge.Initialize(ctx, InitializeParams{})
	require.NoError(t, err)
	require.NoError(t, bridge.Initialized(ctx))

	bridge.grpcClient = &mockAgentCommunicationClient{
		sendMessageFunc: func(ctx context.Context, req *pb.SendMessageRequest, opts ...grpc.CallOption) (*pb.SendMessageResponse, error) {
			return nil, partialresult.StatusError(codes.Internal, "ACP response incomplete", &pb.PartialResult{
				ReasonCode: "token_budget",
				PartialResponse: &pb.Message{
					Role:    "assistant",
					Content: "public draft",
				},
				TaskResults: []*pb.PartialTaskResult{{TaskId: "task-1", Status: "incomplete"}},
			})
		},
	}

	paramsJSON, err := json.Marshal(TextQueryParams{Query: "test query"})
	require.NoError(t, err)
	id := json.RawMessage(`1`)
	response := bridge.HandleMessage(ctx, &JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  "buckley/textQuery",
		Params:  paramsJSON,
	})
	require.NotNil(t, response)
	if response.Result != nil {
		t.Fatalf("JSON-RPC result = %s, want nil on incomplete error", response.Result)
	}
	require.NotNil(t, response.Error)
	if response.Error.Message != "ACP response incomplete" {
		t.Fatalf("error message = %q", response.Error.Message)
	}
	var data map[string]any
	require.NoError(t, json.Unmarshal(response.Error.Data, &data))
	if data["schema_version"] != partialresult.SchemaVersion || data["incomplete"] != true || data["partial_response"] != "public draft" {
		t.Fatalf("error data = %+v", data)
	}
	if data["task_results"] != float64(1) {
		t.Fatalf("task_results = %#v, want 1", data["task_results"])
	}
}

func ptrInt64(v int64) *int64 { return &v }
