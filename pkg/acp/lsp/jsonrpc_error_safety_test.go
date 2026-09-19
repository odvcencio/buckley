package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"m31labs.dev/buckley/pkg/acp/partialresult"
	pb "m31labs.dev/buckley/pkg/acp/proto"
)

func TestHandleMessage_TextQueryOrdinaryErrorUsesSafeMessage(t *testing.T) {
	bridge := newInitializedJSONRPCSafetyBridge(t)
	const secret = "provider-secret-ordinary-error"
	bridge.grpcClient = &mockAgentCommunicationClient{
		sendMessageFunc: func(context.Context, *pb.SendMessageRequest, ...grpc.CallOption) (*pb.SendMessageResponse, error) {
			return nil, errors.New("coordinator exploded with " + secret)
		},
	}

	response := bridge.HandleMessage(context.Background(), textQuerySafetyMessage(t, "tell me"))
	require.NotNil(t, response)
	require.NotNil(t, response.Error)
	if response.Error.Code != InternalError {
		t.Fatalf("error code = %d, want %d", response.Error.Code, InternalError)
	}
	if response.Error.Message != "request failed: coordinator error" {
		t.Fatalf("error message = %q, want stable safe message", response.Error.Message)
	}
	if response.Error.Data != nil {
		t.Fatalf("error data = %s, want none for ordinary error", response.Error.Data)
	}
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "coordinator exploded") {
		t.Fatalf("ordinary error leaked raw text: %s", encoded)
	}
}

func TestHandleMessage_TextQueryTypedPartialStillCarriesData(t *testing.T) {
	bridge := newInitializedJSONRPCSafetyBridge(t)
	bridge.grpcClient = &mockAgentCommunicationClient{
		sendMessageFunc: func(context.Context, *pb.SendMessageRequest, ...grpc.CallOption) (*pb.SendMessageResponse, error) {
			return nil, partialresult.StatusError(codes.Internal, "ACP response incomplete", &pb.PartialResult{
				ReasonCode: "token_budget",
				SafeError:  "safe incomplete",
				PartialResponse: &pb.Message{
					Role:    "assistant",
					Content: "public bounded draft",
				},
			})
		},
	}

	response := bridge.HandleMessage(context.Background(), textQuerySafetyMessage(t, "tell me"))
	require.NotNil(t, response)
	require.NotNil(t, response.Error)
	if response.Error.Message != "ACP response incomplete" {
		t.Fatalf("typed partial message = %q, want original safe status message", response.Error.Message)
	}
	var data map[string]any
	require.NoError(t, json.Unmarshal(response.Error.Data, &data))
	if data["schema_version"] != partialresult.SchemaVersion || data["incomplete"] != true || data["partial_response"] != "public bounded draft" {
		t.Fatalf("partial data = %+v, want bounded incomplete data", data)
	}
}

func TestHandleMessage_TextQuerySuccessUnchanged(t *testing.T) {
	bridge := newInitializedJSONRPCSafetyBridge(t)
	bridge.grpcClient = &mockAgentCommunicationClient{
		sendMessageFunc: func(context.Context, *pb.SendMessageRequest, ...grpc.CallOption) (*pb.SendMessageResponse, error) {
			return &pb.SendMessageResponse{Response: &pb.Message{Role: "assistant", Content: "ordinary success"}}, nil
		},
	}

	response := bridge.HandleMessage(context.Background(), textQuerySafetyMessage(t, "tell me"))
	require.NotNil(t, response)
	if response.Error != nil {
		t.Fatalf("unexpected error: %+v", response.Error)
	}
	var result TextQueryResult
	require.NoError(t, json.Unmarshal(response.Result, &result))
	if result.Response != "ordinary success" || result.AgentID != "test-agent" {
		t.Fatalf("result = %+v, want normal textQuery success", result)
	}
}

func newInitializedJSONRPCSafetyBridge(t *testing.T) *Bridge {
	t.Helper()
	bridge, err := NewBridge(&BridgeConfig{CoordinatorAddr: "localhost:50051", AgentID: "test-agent"})
	require.NoError(t, err)
	_, err = bridge.Initialize(context.Background(), InitializeParams{})
	require.NoError(t, err)
	require.NoError(t, bridge.Initialized(context.Background()))
	return bridge
}

func textQuerySafetyMessage(t *testing.T, query string) *JSONRPCMessage {
	t.Helper()
	paramsJSON, err := json.Marshal(TextQueryParams{Query: query})
	require.NoError(t, err)
	id := json.RawMessage(`1`)
	return &JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  "buckley/textQuery",
		Params:  paramsJSON,
	}
}
