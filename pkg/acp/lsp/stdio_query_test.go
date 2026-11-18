package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	pb "m31labs.dev/buckley/pkg/acp/proto"
)

func serveEditorFixture(t *testing.T, client pb.AgentCommunicationClient) (func(string), func() *JSONRPCMessage, <-chan error) {
	t.Helper()
	bridge, err := NewBridge(&BridgeConfig{AgentID: "editor"})
	require.NoError(t, err)
	bridge.grpcClient = client

	editor, server := net.Pipe()
	t.Cleanup(func() { _ = editor.Close(); _ = server.Close() })
	done := make(chan error, 1)
	go func() { done <- bridge.ServeStdio(t.Context(), server, server) }()
	reader := bufio.NewReader(editor)
	write := func(body string) {
		t.Helper()
		require.NoError(t, editor.SetWriteDeadline(time.Now().Add(time.Second)))
		var msg JSONRPCMessage
		require.NoError(t, json.Unmarshal([]byte(body), &msg))
		require.NoError(t, WriteMessage(editor, &msg))
	}
	read := func() *JSONRPCMessage {
		t.Helper()
		require.NoError(t, editor.SetReadDeadline(time.Now().Add(time.Second)))
		msg, err := ReadMessage(reader)
		require.NoError(t, err)
		return msg
	}
	write(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{}}`)
	require.Nil(t, read().Error)
	write(`{"jsonrpc":"2.0","method":"initialized"}`)
	return write, read, done
}

func TestServeStdio_TextQueryCancellationReachesCoordinator(t *testing.T) {
	started := make(chan struct{})
	client := &mockAgentCommunicationClient{
		sendMessageFunc: func(ctx context.Context, req *pb.SendMessageRequest, _ ...grpc.CallOption) (*pb.SendMessageResponse, error) {
			if req.GetMessage().GetContent() == "slow" {
				close(started)
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return &pb.SendMessageResponse{Response: &pb.Message{Content: "ready"}}, nil
		},
	}
	write, read, done := serveEditorFixture(t, client)
	write(`{"jsonrpc":"2.0","id":"first","method":"buckley/textQuery","params":{"query":"slow"}}`)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("coordinator did not start the query")
	}
	write(`{"jsonrpc":"2.0","method":"$/cancelRequest","params":{"id":"first"}}`)
	response := read()
	require.Equal(t, `"first"`, string(*response.ID))
	require.NotNil(t, response.Error)
	require.Equal(t, -32800, response.Error.Code)

	write(`{"jsonrpc":"2.0","id":2,"method":"buckley/textQuery","params":{"query":"next"}}`)
	response = read()
	require.Nil(t, response.Error)
	require.JSONEq(t, `{"response":"ready","agentId":"editor"}`, string(response.Result))
	write(`{"jsonrpc":"2.0","id":3,"method":"shutdown"}`)
	require.Nil(t, read().Error)
	write(`{"jsonrpc":"2.0","method":"exit"}`)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("exit did not close the stdio server")
	}
}

func TestServeStdio_StreamQueryEmitsChunkBeforeRequestCompletes(t *testing.T) {
	finish := make(chan struct{})
	client := &mockAgentCommunicationClientWithStreaming{
		streamTaskFunc: func(ctx context.Context, req *pb.TaskStreamRequest, _ ...grpc.CallOption) (pb.AgentCommunication_StreamTaskClient, error) {
			first := true
			return &mockStreamTaskClient{recvFunc: func() (*pb.TaskEvent, error) {
				if first {
					first = false
					return &pb.TaskEvent{TaskId: req.GetTaskId(), Message: "first useful chunk"}, nil
				}
				select {
				case <-finish:
					return nil, io.EOF
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}}, nil
		},
	}
	write, read, _ := serveEditorFixture(t, client)
	write(`{"jsonrpc":"2.0","id":1,"method":"buckley/streamQuery","params":{"query":"inspect"}}`)
	msg := read()
	require.Equal(t, "buckley/streamChunk", msg.Method)
	var chunk StreamChunkNotification
	require.NoError(t, json.Unmarshal(msg.Params, &chunk))
	require.NotEmpty(t, chunk.StreamID)
	require.NotNil(t, chunk.RequestID)
	require.Equal(t, "1", string(*chunk.RequestID))
	require.Equal(t, "first useful chunk", chunk.Chunk)
	require.False(t, chunk.Done)
	close(finish)
	msg = read()
	require.Equal(t, "buckley/streamChunk", msg.Method)
	require.NoError(t, json.Unmarshal(msg.Params, &chunk))
	require.True(t, chunk.Done)

	response := read()
	require.NotNil(t, response.ID)
	require.Equal(t, "1", string(*response.ID))
	require.Nil(t, response.Error)
	var result StreamQueryResult
	require.NoError(t, json.Unmarshal(response.Result, &result))
	require.Equal(t, chunk.StreamID, result.StreamID)
	require.Equal(t, "editor", result.AgentID)
}
