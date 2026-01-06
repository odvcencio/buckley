package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	pb "github.com/odvcencio/buckley/pkg/acp/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// mockStreamTaskClient implements the streaming client interface
type mockStreamTaskClient struct {
	grpc.ClientStream
	events   []*pb.TaskEvent
	idx      int
	mu       sync.Mutex
	err      error
	recvFunc func() (*pb.TaskEvent, error)
}

func (m *mockStreamTaskClient) Recv() (*pb.TaskEvent, error) {
	// Use custom recv function if provided
	if m.recvFunc != nil {
		return m.recvFunc()
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.err != nil {
		return nil, m.err
	}

	if m.idx >= len(m.events) {
		return nil, io.EOF
	}

	event := m.events[m.idx]
	m.idx++
	return event, nil
}

// mockAgentCommunicationClientWithStreaming extends the mock client with streaming support
type mockAgentCommunicationClientWithStreaming struct {
	mockAgentCommunicationClient
	streamTaskFunc func(ctx context.Context, req *pb.TaskStreamRequest, opts ...grpc.CallOption) (pb.AgentCommunication_StreamTaskClient, error)
}

func (m *mockAgentCommunicationClientWithStreaming) StreamTask(ctx context.Context, req *pb.TaskStreamRequest, opts ...grpc.CallOption) (pb.AgentCommunication_StreamTaskClient, error) {
	if m.streamTaskFunc != nil {
		return m.streamTaskFunc(ctx, req, opts...)
	}
	return nil, errors.New("streamTaskFunc not set")
}

func TestBridge_HandleStreamQuery(t *testing.T) {
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

	// Set up mock gRPC client with streaming
	mockStream := &mockStreamTaskClient{
		events: []*pb.TaskEvent{
			{
				TaskId:    "task-1",
				Status:    "running",
				Message:   "Processing query...",
				Timestamp: timestamppb.Now(),
			},
			{
				TaskId:    "task-1",
				Status:    "completed",
				Message:   "Query complete!",
				Timestamp: timestamppb.Now(),
			},
		},
	}

	mockClient := &mockAgentCommunicationClientWithStreaming{
		streamTaskFunc: func(ctx context.Context, req *pb.TaskStreamRequest, opts ...grpc.CallOption) (pb.AgentCommunication_StreamTaskClient, error) {
			assert.Equal(t, "test-agent", req.AgentId)
			assert.Equal(t, "What is the weather?", req.Query)
			return mockStream, nil
		},
	}
	bridge.grpcClient = mockClient

	// Collect chunks
	var chunks []string
	var doneFlags []bool

	callback := func(chunk string, done bool) error {
		chunks = append(chunks, chunk)
		doneFlags = append(doneFlags, done)
		return nil
	}

	// Test successful streaming query
	streamID, err := bridge.HandleStreamQuery(ctx, "What is the weather?", callback)
	require.NoError(t, err)
	assert.NotEmpty(t, streamID)

	// Verify we received all chunks plus done signal
	require.Len(t, chunks, 3) // 2 events + 1 done
	assert.Equal(t, "Processing query...", chunks[0])
	assert.False(t, doneFlags[0])
	assert.Equal(t, "Query complete!", chunks[1])
	assert.False(t, doneFlags[1])
	assert.Equal(t, "", chunks[2])
	assert.True(t, doneFlags[2])
}

func TestBridge_HandleStreamQuery_MultipleChunks(t *testing.T) {
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

	// Create mock stream with multiple chunks
	mockStream := &mockStreamTaskClient{
		events: []*pb.TaskEvent{
			{TaskId: "task-1", Status: "running", Message: "Chunk 1"},
			{TaskId: "task-1", Status: "running", Message: "Chunk 2"},
			{TaskId: "task-1", Status: "running", Message: "Chunk 3"},
			{TaskId: "task-1", Status: "running", Message: "Chunk 4"},
			{TaskId: "task-1", Status: "completed", Message: "Done"},
		},
	}

	mockClient := &mockAgentCommunicationClientWithStreaming{
		streamTaskFunc: func(ctx context.Context, req *pb.TaskStreamRequest, opts ...grpc.CallOption) (pb.AgentCommunication_StreamTaskClient, error) {
			return mockStream, nil
		},
	}
	bridge.grpcClient = mockClient

	var chunks []string
	callback := func(chunk string, done bool) error {
		chunks = append(chunks, chunk)
		return nil
	}

	streamID, err := bridge.HandleStreamQuery(ctx, "test query", callback)
	require.NoError(t, err)
	assert.NotEmpty(t, streamID)

	// Verify all chunks received (5 events + 1 done signal)
	require.Len(t, chunks, 6)
	assert.Equal(t, "Chunk 1", chunks[0])
	assert.Equal(t, "Chunk 2", chunks[1])
	assert.Equal(t, "Chunk 3", chunks[2])
	assert.Equal(t, "Chunk 4", chunks[3])
	assert.Equal(t, "Done", chunks[4])
	assert.Equal(t, "", chunks[5]) // Done signal
}

func TestBridge_HandleStreamQuery_StreamError(t *testing.T) {
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

	// Create mock stream that sends events then errors
	var recvCount int
	mockStream := &mockStreamTaskClient{
		events: []*pb.TaskEvent{
			{TaskId: "task-1", Status: "running", Message: "Chunk 1"},
			{TaskId: "task-1", Status: "running", Message: "Chunk 2"},
		},
	}

	mockStream.recvFunc = func() (*pb.TaskEvent, error) {
		mockStream.mu.Lock()
		defer mockStream.mu.Unlock()

		if recvCount < len(mockStream.events) {
			event := mockStream.events[recvCount]
			recvCount++
			return event, nil
		}
		// Return error after all events
		return nil, status.Error(codes.Internal, "stream processing error")
	}

	mockClient := &mockAgentCommunicationClientWithStreaming{
		streamTaskFunc: func(ctx context.Context, req *pb.TaskStreamRequest, opts ...grpc.CallOption) (pb.AgentCommunication_StreamTaskClient, error) {
			return mockStream, nil
		},
	}
	bridge.grpcClient = mockClient

	var chunks []string
	callback := func(chunk string, done bool) error {
		chunks = append(chunks, chunk)
		return nil
	}

	streamID, err := bridge.HandleStreamQuery(ctx, "test query", callback)

	// Should return stream ID but also an error
	assert.NotEmpty(t, streamID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stream error")

	// Verify we received chunks before the error
	require.Len(t, chunks, 2)
	assert.Equal(t, "Chunk 1", chunks[0])
	assert.Equal(t, "Chunk 2", chunks[1])
}

func TestBridge_HandleStreamQuery_EmptyQuery(t *testing.T) {
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

	// Set up mock client (shouldn't be called)
	bridge.grpcClient = &mockAgentCommunicationClientWithStreaming{}

	callback := func(chunk string, done bool) error {
		return nil
	}

	// Test with empty query
	_, err = bridge.HandleStreamQuery(ctx, "", callback)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query cannot be empty")

	// Test with whitespace-only query
	_, err = bridge.HandleStreamQuery(ctx, "   \t\n  ", callback)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query cannot be empty")
}

func TestBridge_HandleStreamQuery_NotConnected(t *testing.T) {
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

	callback := func(chunk string, done bool) error {
		return nil
	}

	// Test without connection
	_, err = bridge.HandleStreamQuery(ctx, "test query", callback)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not connected to coordinator")
}

func TestBridge_HandleStreamQuery_NotInitialized(t *testing.T) {
	config := &BridgeConfig{
		CoordinatorAddr: "localhost:50051",
		AgentID:         "test-agent",
	}
	bridge, err := NewBridge(config)
	require.NoError(t, err)

	// Do NOT initialize the bridge

	ctx := context.Background()
	callback := func(chunk string, done bool) error {
		return nil
	}

	_, err = bridge.HandleStreamQuery(ctx, "test query", callback)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bridge not initialized")
}

func TestBridge_HandleStreamQuery_Cancellation(t *testing.T) {
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

	// Create a cancellable context
	streamCtx, cancel := context.WithCancel(ctx)

	// Create mock stream that blocks
	mockStream := &mockStreamTaskClient{
		events: []*pb.TaskEvent{
			{TaskId: "task-1", Status: "running", Message: "Chunk 1"},
		},
	}

	// Set up custom Recv that returns one event then waits for cancellation
	var recvCount int
	mockStream.recvFunc = func() (*pb.TaskEvent, error) {
		recvCount++
		if recvCount == 1 {
			return mockStream.events[0], nil
		}
		// Check context cancellation
		select {
		case <-streamCtx.Done():
			return nil, streamCtx.Err()
		case <-time.After(100 * time.Millisecond):
			return nil, streamCtx.Err()
		}
	}

	mockClient := &mockAgentCommunicationClientWithStreaming{
		streamTaskFunc: func(ctx context.Context, req *pb.TaskStreamRequest, opts ...grpc.CallOption) (pb.AgentCommunication_StreamTaskClient, error) {
			return mockStream, nil
		},
	}
	bridge.grpcClient = mockClient

	var chunks []string
	var mu sync.Mutex
	callback := func(chunk string, done bool) error {
		mu.Lock()
		defer mu.Unlock()
		chunks = append(chunks, chunk)
		return nil
	}

	// Start streaming in goroutine
	var streamErr error
	done := make(chan struct{})
	go func() {
		_, streamErr = bridge.HandleStreamQuery(streamCtx, "test query", callback)
		close(done)
	}()

	// Cancel after a short delay
	time.Sleep(50 * time.Millisecond)
	cancel()

	// Wait for completion
	<-done

	// Should have an error due to cancellation
	require.Error(t, streamErr)
	assert.Contains(t, streamErr.Error(), "stream error")
}

func TestBridge_CancelStream(t *testing.T) {
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

	// Channel to capture the stream context
	streamCtxChan := make(chan context.Context, 1)

	// Create a long-running mock stream
	mockStream := &mockStreamTaskClient{
		events: []*pb.TaskEvent{
			{TaskId: "task-1", Status: "running", Message: "Chunk 1"},
		},
	}

	// Make Recv block after first event, but respond to context cancellation
	var recvCount int
	mockStream.recvFunc = func() (*pb.TaskEvent, error) {
		recvCount++
		if recvCount == 1 {
			return mockStream.events[0], nil
		}
		// Get the stream context that was passed to StreamTask
		var streamCtx context.Context
		select {
		case streamCtx = <-streamCtxChan:
		default:
			return nil, io.EOF
		}
		// Block but check for cancellation
		select {
		case <-streamCtx.Done():
			return nil, streamCtx.Err()
		case <-time.After(10 * time.Second):
			return nil, io.EOF
		}
	}

	mockClient := &mockAgentCommunicationClientWithStreaming{
		streamTaskFunc: func(ctx context.Context, req *pb.TaskStreamRequest, opts ...grpc.CallOption) (pb.AgentCommunication_StreamTaskClient, error) {
			// Capture the context for later cancellation checking
			streamCtxChan <- ctx
			return mockStream, nil
		},
	}
	bridge.grpcClient = mockClient

	var chunks []string
	var streamErr error

	callback := func(chunk string, done bool) error {
		chunks = append(chunks, chunk)
		return nil
	}

	// Start streaming in goroutine
	done := make(chan struct{})
	go func() {
		_, streamErr = bridge.HandleStreamQuery(ctx, "test query", callback)
		close(done)
	}()

	// Wait a bit for stream to start and get the stream ID
	time.Sleep(100 * time.Millisecond)

	// Get the stream ID from the bridge's active streams
	bridge.streamsMu.RLock()
	var activeStreamID string
	for id := range bridge.activeStreams {
		activeStreamID = id
		break
	}
	bridge.streamsMu.RUnlock()

	// Cancel the stream
	err = bridge.CancelStream(activeStreamID)
	require.NoError(t, err)

	// Wait for completion (with timeout)
	select {
	case <-done:
		// Stream should have been cancelled
		require.Error(t, streamErr)
	case <-time.After(2 * time.Second):
		t.Fatal("stream cancellation did not complete in time")
	}

	// Verify chunk was received before cancellation
	assert.GreaterOrEqual(t, len(chunks), 1)
}

func TestBridge_CancelStream_NotFound(t *testing.T) {
	config := &BridgeConfig{
		CoordinatorAddr: "localhost:50051",
		AgentID:         "test-agent",
	}
	bridge, err := NewBridge(config)
	require.NoError(t, err)

	// Try to cancel a stream that doesn't exist
	err = bridge.CancelStream("nonexistent-stream-id")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stream not found")
}

func TestBridge_CancelStream_AlreadyCancelled(t *testing.T) {
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

	// Create a quick stream
	mockStream := &mockStreamTaskClient{
		events: []*pb.TaskEvent{
			{TaskId: "task-1", Status: "completed", Message: "Done"},
		},
	}

	mockClient := &mockAgentCommunicationClientWithStreaming{
		streamTaskFunc: func(ctx context.Context, req *pb.TaskStreamRequest, opts ...grpc.CallOption) (pb.AgentCommunication_StreamTaskClient, error) {
			return mockStream, nil
		},
	}
	bridge.grpcClient = mockClient

	callback := func(chunk string, done bool) error {
		return nil
	}

	// Start and complete stream
	streamID, err := bridge.HandleStreamQuery(ctx, "test query", callback)
	require.NoError(t, err)

	// Try to cancel completed stream
	err = bridge.CancelStream(streamID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stream not found")
}

func TestHandleMessage_StreamQuery(t *testing.T) {
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

	// Create a buckley/streamQuery request
	params := StreamQueryParams{
		Query: "test streaming query",
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	id := json.RawMessage(`1`)
	msg := &JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  "buckley/streamQuery",
		Params:  paramsJSON,
	}

	// Handle the message (should indicate async handling needed)
	response := bridge.HandleMessage(ctx, msg)
	require.NotNil(t, response)
	assert.Equal(t, "2.0", response.JSONRPC)
	assert.Equal(t, &id, response.ID)

	// Current implementation should return error indicating async needed
	require.NotNil(t, response.Error)
	assert.Equal(t, InternalError, response.Error.Code)
	assert.Contains(t, response.Error.Message, "streamQuery requires async handling")
}

func TestHandleMessage_CancelRequest(t *testing.T) {
	config := &BridgeConfig{
		CoordinatorAddr: "localhost:50051",
		AgentID:         "test-agent",
	}
	bridge, err := NewBridge(config)
	require.NoError(t, err)

	// Create a $/cancelRequest notification
	params := CancelParams{
		ID: json.RawMessage(`1`),
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	msg := &JSONRPCMessage{
		JSONRPC: "2.0",
		Method:  "$/cancelRequest",
		Params:  paramsJSON,
	}

	// Handle the notification (should return nil for notifications)
	response := bridge.HandleMessage(context.Background(), msg)
	assert.Nil(t, response)
}

func TestStreamCallback_CallbackError(t *testing.T) {
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

	// Create mock stream
	mockStream := &mockStreamTaskClient{
		events: []*pb.TaskEvent{
			{TaskId: "task-1", Status: "running", Message: "Chunk 1"},
			{TaskId: "task-1", Status: "running", Message: "Chunk 2"},
		},
	}

	mockClient := &mockAgentCommunicationClientWithStreaming{
		streamTaskFunc: func(ctx context.Context, req *pb.TaskStreamRequest, opts ...grpc.CallOption) (pb.AgentCommunication_StreamTaskClient, error) {
			return mockStream, nil
		},
	}
	bridge.grpcClient = mockClient

	// Callback that errors on second chunk
	var callCount int
	callback := func(chunk string, done bool) error {
		callCount++
		if callCount > 1 {
			return errors.New("callback processing error")
		}
		return nil
	}

	// Stream should fail with callback error
	streamID, err := bridge.HandleStreamQuery(ctx, "test query", callback)
	assert.NotEmpty(t, streamID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "callback processing error")

	// Verify callback was called twice
	assert.Equal(t, 2, callCount)
}

func TestBridge_StreamStartError(t *testing.T) {
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

	// Mock client that errors on StreamTask call
	mockClient := &mockAgentCommunicationClientWithStreaming{
		streamTaskFunc: func(ctx context.Context, req *pb.TaskStreamRequest, opts ...grpc.CallOption) (pb.AgentCommunication_StreamTaskClient, error) {
			return nil, status.Error(codes.Unavailable, "coordinator unavailable")
		},
	}
	bridge.grpcClient = mockClient

	callback := func(chunk string, done bool) error {
		return nil
	}

	// Should fail to start stream
	_, err = bridge.HandleStreamQuery(ctx, "test query", callback)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to start stream")
}
