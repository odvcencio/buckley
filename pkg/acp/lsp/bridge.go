package lsp

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	pb "github.com/odvcencio/buckley/pkg/acp/proto"
	"github.com/oklog/ulid/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Bridge implements LSP server that bridges to ACP coordinator
type Bridge struct {
	config *BridgeConfig

	mu           sync.RWMutex
	initializing bool
	initialized  bool
	shutdown     bool

	// gRPC connection to coordinator
	grpcConn   *grpc.ClientConn
	grpcClient pb.AgentCommunicationClient

	// Active stream cancellation
	streamsMu     sync.RWMutex
	activeStreams map[string]context.CancelFunc
}

// InlineCompletionCallback receives streamed inline completion text.
type InlineCompletionCallback func(text string, done bool) error

// ApplyTextEdits applies LSP text edits to the provided content and returns the updated text.
func ApplyTextEdits(content string, edits []*pb.TextEdit) (string, error) {
	base := []rune(content)
	type resolvedEdit struct {
		start   int
		end     int
		newText string
	}

	var resolved []resolvedEdit
	for _, edit := range edits {
		if edit == nil {
			return "", fmt.Errorf("nil edit")
		}
		if edit.Range == nil {
			resolved = append(resolved, resolvedEdit{start: 0, end: len(base), newText: edit.NewText})
			continue
		}
		start, err := offsetFromPosition(base, edit.Range.Start)
		if err != nil {
			return "", err
		}
		end, err := offsetFromPosition(base, edit.Range.End)
		if err != nil {
			return "", err
		}
		if end < start {
			return "", fmt.Errorf("end before start")
		}
		resolved = append(resolved, resolvedEdit{start: start, end: end, newText: edit.NewText})
	}

	sort.Slice(resolved, func(i, j int) bool {
		if resolved[i].start == resolved[j].start {
			return resolved[i].end > resolved[j].end
		}
		return resolved[i].start > resolved[j].start
	})

	out := base
	for _, e := range resolved {
		if e.start > len(out) || e.end > len(out) {
			return "", fmt.Errorf("range out of bounds")
		}
		head := append([]rune{}, out[:e.start]...)
		head = append(head, []rune(e.newText)...)
		out = append(head, out[e.end:]...)
	}
	return string(out), nil
}

func offsetFromPosition(content []rune, pos *pb.Position) (int, error) {
	if pos == nil {
		return 0, fmt.Errorf("position required")
	}
	if pos.Line < 0 || pos.Character < 0 {
		return 0, fmt.Errorf("negative position")
	}
	line := 0
	char := 0
	for idx, r := range content {
		if line == int(pos.Line) && char == int(pos.Character) {
			return idx, nil
		}
		if r == '\n' {
			line++
			char = 0
		} else {
			char++
		}
	}
	if line == int(pos.Line) && char == int(pos.Character) {
		return len(content), nil
	}
	return 0, fmt.Errorf("position out of range")
}

// BuildEditorContext constructs a minimal EditorContext for a single document.
func BuildEditorContext(uri, language, content string, selection *pb.Range) *pb.EditorContext {
	return &pb.EditorContext{
		Document: &pb.DocumentSnapshot{
			Uri:           uri,
			LanguageId:    language,
			Content:       content,
			Selection:     selection,
			VisibleRanges: nil,
			Version:       0,
		},
	}
}

// BridgeConfig holds LSP bridge configuration
type BridgeConfig struct {
	// CoordinatorAddr is the endpoint for gRPC connection
	CoordinatorAddr string

	// AgentID for this bridge instance
	AgentID string

	// Capabilities to advertise
	Capabilities []string
}

// NewBridge creates a new LSP bridge instance
func NewBridge(config *BridgeConfig) (*Bridge, error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}

	return &Bridge{
		config:        config,
		activeStreams: make(map[string]context.CancelFunc),
	}, nil
}

// Initialize handles LSP initialize request
func (b *Bridge) Initialize(ctx context.Context, params InitializeParams) (*InitializeResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.initializing || b.initialized {
		return nil, fmt.Errorf("already initialized")
	}

	// Build server capabilities based on config
	capabilities := b.buildServerCapabilities()

	result := &InitializeResult{
		Capabilities: capabilities,
		ServerInfo: &ServerInfo{
			Name:    "buckley-acp-bridge",
			Version: "1.0.0",
		},
	}

	// Mark as initializing after successful initialize request
	// Will be marked fully initialized on Initialized notification
	b.initializing = true

	return result, nil
}

// Initialized handles LSP initialized notification
func (b *Bridge) Initialized(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.initializing {
		return fmt.Errorf("not initializing")
	}

	b.initializing = false
	b.initialized = true
	return nil
}

// Shutdown handles LSP shutdown request
func (b *Bridge) Shutdown(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.initialized {
		return fmt.Errorf("not initialized")
	}

	b.shutdown = true
	return nil
}

// Exit handles LSP exit notification
func (b *Bridge) Exit(ctx context.Context) error {
	// Clean exit - no error
	return nil
}

// ServeStdio starts the LSP server on stdio
func (b *Bridge) ServeStdio(ctx context.Context, reader io.Reader, writer io.Writer) error {
	bufReader, ok := reader.(*bufio.Reader)
	if !ok {
		bufReader = bufio.NewReader(reader)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Read message
		msg, err := ReadMessage(bufReader)
		if err != nil {
			if err == io.EOF {
				return nil // Clean shutdown
			}
			return fmt.Errorf("failed to read message: %w", err)
		}

		// Handle message
		response := b.HandleMessage(ctx, msg)

		// Write response (if any)
		if response != nil {
			if err := WriteMessage(writer, response); err != nil {
				return fmt.Errorf("failed to write response: %w", err)
			}
		}
	}
}

// buildServerCapabilities constructs LSP server capabilities
func (b *Bridge) buildServerCapabilities() ServerCapabilities {
	return ServerCapabilities{
		TextDocumentSync: &TextDocumentSyncOptions{
			OpenClose: true,
			Change:    TextDocumentSyncKindFull,
		},
	}
}

// ConnectToCoordinator establishes gRPC connection to the ACP coordinator
func (b *Bridge) ConnectToCoordinator(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.grpcConn != nil {
		return fmt.Errorf("already connected")
	}

	// Establish gRPC connection
	conn, err := grpc.DialContext(
		ctx,
		b.config.CoordinatorAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return fmt.Errorf("failed to connect to coordinator: %w", err)
	}

	b.grpcConn = conn
	b.grpcClient = pb.NewAgentCommunicationClient(conn)

	return nil
}

// DisconnectFromCoordinator closes the gRPC connection
func (b *Bridge) DisconnectFromCoordinator() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.grpcConn == nil {
		return nil
	}

	err := b.grpcConn.Close()
	b.grpcConn = nil
	b.grpcClient = nil

	return err
}

// HandleTextQuery processes a text query and returns a response
func (b *Bridge) HandleTextQuery(ctx context.Context, query string) (string, error) {
	b.mu.RLock()
	if !b.initialized {
		b.mu.RUnlock()
		return "", fmt.Errorf("bridge not initialized")
	}

	if b.grpcClient == nil {
		b.mu.RUnlock()
		return "", fmt.Errorf("not connected to coordinator")
	}

	client := b.grpcClient
	b.mu.RUnlock()

	// Validate query
	if strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("query cannot be empty")
	}

	// Send query to coordinator via SendMessage RPC
	request := &pb.SendMessageRequest{
		AgentId: b.config.AgentID,
		Message: &pb.Message{
			Role:    "user",
			Content: query,
		},
	}

	response, err := client.SendMessage(ctx, request)
	if err != nil {
		return "", fmt.Errorf("coordinator error: %w", err)
	}

	if response.Response == nil {
		return "", fmt.Errorf("empty response from coordinator")
	}

	return response.Response.Content, nil
}

// StreamCallback is called for each chunk in the stream
type StreamCallback func(chunk string, done bool) error

// HandleStreamQuery processes a streaming query
func (b *Bridge) HandleStreamQuery(ctx context.Context, query string, callback StreamCallback) (string, error) {
	b.mu.RLock()
	if !b.initialized {
		b.mu.RUnlock()
		return "", fmt.Errorf("bridge not initialized")
	}

	if b.grpcClient == nil {
		b.mu.RUnlock()
		return "", fmt.Errorf("not connected to coordinator")
	}

	client := b.grpcClient
	b.mu.RUnlock()

	// Validate query
	if strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("query cannot be empty")
	}

	// Generate stream ID
	streamID := ulid.Make().String()

	// Create cancellable context
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Register stream for cancellation
	b.streamsMu.Lock()
	b.activeStreams[streamID] = cancel
	b.streamsMu.Unlock()

	defer func() {
		b.streamsMu.Lock()
		delete(b.activeStreams, streamID)
		b.streamsMu.Unlock()
	}()

	// Start streaming from coordinator
	request := &pb.TaskStreamRequest{
		AgentId: b.config.AgentID,
		TaskId:  streamID,
		Query:   query,
	}

	stream, err := client.StreamTask(streamCtx, request)
	if err != nil {
		return "", fmt.Errorf("failed to start stream: %w", err)
	}

	// Process stream events
	for {
		event, err := stream.Recv()
		if err == io.EOF {
			// Stream completed successfully
			callback("", true)
			break
		}
		if err != nil {
			return streamID, fmt.Errorf("stream error: %w", err)
		}

		// Send chunk to callback (use Message field from TaskEvent)
		if err := callback(event.Message, false); err != nil {
			return streamID, err
		}
	}

	return streamID, nil
}

// CancelStream cancels an active stream by ID
func (b *Bridge) CancelStream(streamID string) error {
	b.streamsMu.Lock()
	defer b.streamsMu.Unlock()

	cancel, exists := b.activeStreams[streamID]
	if !exists {
		return fmt.Errorf("stream not found: %s", streamID)
	}

	cancel()
	delete(b.activeStreams, streamID)

	return nil
}

// StreamInlineCompletions streams inline completions to the callback.
func (b *Bridge) StreamInlineCompletions(ctx context.Context, req *pb.InlineCompletionRequest, cb InlineCompletionCallback) (string, error) {
	if req == nil {
		return "", fmt.Errorf("request cannot be nil")
	}

	b.mu.RLock()
	if !b.initialized {
		b.mu.RUnlock()
		return "", fmt.Errorf("bridge not initialized")
	}
	client := b.grpcClient
	b.mu.RUnlock()

	if client == nil {
		return "", fmt.Errorf("not connected to coordinator")
	}

	streamID := req.GetSessionId()
	if strings.TrimSpace(streamID) == "" {
		streamID = ulid.Make().String()
	}
	req.SessionId = streamID

	stream, err := client.StreamInlineCompletions(ctx, req)
	if err != nil {
		return streamID, fmt.Errorf("start inline completions: %w", err)
	}

	for {
		ev, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return streamID, fmt.Errorf("inline stream error: %w", err)
		}
		if cb != nil {
			if err := cb(ev.Text, ev.GetIsFinal()); err != nil {
				return streamID, err
			}
		}
		if ev.GetIsFinal() {
			break
		}
	}
	return streamID, nil
}

// ProposeEdits calls the ACP propose edits RPC.
func (b *Bridge) ProposeEdits(ctx context.Context, req *pb.ProposeEditsRequest) (*pb.ProposeEditsResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("request cannot be nil")
	}

	b.mu.RLock()
	client := b.grpcClient
	initialized := b.initialized
	b.mu.RUnlock()

	if !initialized {
		return nil, fmt.Errorf("bridge not initialized")
	}
	if client == nil {
		return nil, fmt.Errorf("not connected to coordinator")
	}

	return client.ProposeEdits(ctx, req)
}

// ApplyEdits forwards apply edits to ACP.
func (b *Bridge) ApplyEdits(ctx context.Context, req *pb.ApplyEditsRequest) (*pb.ApplyEditsResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("request cannot be nil")
	}

	b.mu.RLock()
	client := b.grpcClient
	initialized := b.initialized
	b.mu.RUnlock()

	if !initialized {
		return nil, fmt.Errorf("bridge not initialized")
	}
	if client == nil {
		return nil, fmt.Errorf("not connected to coordinator")
	}

	return client.ApplyEdits(ctx, req)
}

// UpdateEditorState retrieves editor state snapshot (plan/TODO/approvals).
func (b *Bridge) UpdateEditorState(ctx context.Context, req *pb.UpdateEditorStateRequest) (*pb.UpdateEditorStateResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("request cannot be nil")
	}

	b.mu.RLock()
	client := b.grpcClient
	initialized := b.initialized
	b.mu.RUnlock()

	if !initialized {
		return nil, fmt.Errorf("bridge not initialized")
	}
	if client == nil {
		return nil, fmt.Errorf("not connected to coordinator")
	}

	return client.UpdateEditorState(ctx, req)
}
