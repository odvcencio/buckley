package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const callbackSecret = "CALLBACK_SECRET_SENTINEL"

func TestAgentCallbackErrorsDoNotExposeRawData(t *testing.T) {
	tests := []struct {
		name    string
		method  string
		code    int
		message string
		setup   func(*Agent)
		params  any
	}{
		{
			name:    "prompt",
			method:  "session/prompt",
			code:    ErrCodeInternal,
			message: "Prompt failed",
			setup: func(agent *Agent) {
				agent.handlers.OnPrompt = func(context.Context, *AgentSession, []ContentBlock, StreamFunc) (*PromptResult, error) {
					return nil, errors.New("raw prompt failure " + callbackSecret)
				}
				agent.sessions["sess-1"] = &AgentSession{ID: "sess-1"}
			},
			params: PromptParams{SessionID: "sess-1", Prompt: []ContentBlock{{Type: "text", Text: "hello"}}},
		},
		{
			name:    "set config option",
			method:  "session/set_config_option",
			code:    ErrCodeInvalidParams,
			message: "Set config option failed",
			setup: func(agent *Agent) {
				agent.handlers.OnSetConfigOption = func(context.Context, *AgentSession, string, ConfigOptionValue) ([]SessionConfigOption, error) {
					return nil, errors.New("raw config failure " + callbackSecret)
				}
				agent.sessions["sess-1"] = &AgentSession{ID: "sess-1"}
			},
			params: SetConfigOptionParams{SessionID: "sess-1", ConfigID: "model", Value: json.RawMessage(`"openai/gpt-4o"`)},
		},
		{
			name:    "machine spawn",
			method:  "_machine/spawn_agent",
			code:    ErrCodeInternal,
			message: "spawn_agent failed",
			setup: func(agent *Agent) {
				agent.SetMachineHandlers(MachineHandlers{
					OnSpawnAgent: func(context.Context, *SpawnAgentParams) (*SpawnAgentResult, error) {
						return nil, errors.New("raw spawn failure " + callbackSecret)
					},
				})
			},
			params: SpawnAgentParams{SessionID: "sess-1", Task: "work"},
		},
		{
			name:    "machine steer",
			method:  "_machine/steer_agent",
			code:    ErrCodeInternal,
			message: "steer_agent failed",
			setup: func(agent *Agent) {
				agent.SetMachineHandlers(MachineHandlers{
					OnSteerAgent: func(context.Context, *SteerAgentParams) error {
						return errors.New("raw steer failure " + callbackSecret)
					},
				})
			},
			params: SteerAgentParams{SessionID: "sess-1", AgentID: "agent-1", Content: "go"},
		},
		{
			name:    "machine list",
			method:  "_machine/list_agents",
			code:    ErrCodeInternal,
			message: "list_agents failed",
			setup: func(agent *Agent) {
				agent.SetMachineHandlers(MachineHandlers{
					OnListAgents: func(context.Context, *ListAgentsParams) (*ListAgentsResult, error) {
						return nil, errors.New("raw list failure " + callbackSecret)
					},
				})
			},
			params: ListAgentsParams{SessionID: "sess-1"},
		},
		{
			name:    "machine escalate",
			method:  "_machine/escalate_mode",
			code:    ErrCodeInternal,
			message: "escalate_mode failed",
			setup: func(agent *Agent) {
				agent.SetMachineHandlers(MachineHandlers{
					OnEscalateMode: func(context.Context, *EscalateModeParams) (*EscalateModeResult, error) {
						return nil, errors.New("raw escalate failure " + callbackSecret)
					},
				})
			},
			params: EscalateModeParams{SessionID: "sess-1", AgentID: "agent-1", Modality: "rlm"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, raw := handleAgentRequest(t, tt.method, tt.params, tt.setup)
			if resp.Error == nil {
				t.Fatalf("error = nil, raw response %s", raw)
			}
			if resp.Error.Code != tt.code || resp.Error.Message != tt.message {
				t.Fatalf("error = %+v, want code %d message %q", resp.Error, tt.code, tt.message)
			}
			if resp.Error.Data != nil {
				t.Fatalf("error data = %#v, want nil", resp.Error.Data)
			}
			if strings.Contains(string(raw), callbackSecret) {
				t.Fatalf("serialized response leaked callback secret: %s", raw)
			}
		})
	}
}

func TestAgentPromptCancellationStillReturnsPromptResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	resp, raw := handleAgentRequestWithContext(t, ctx, "session/prompt", PromptParams{
		SessionID: "sess-1",
		Prompt:    []ContentBlock{{Type: "text", Text: "hello"}},
	}, func(agent *Agent) {
		agent.handlers.OnPrompt = func(ctx context.Context, session *AgentSession, content []ContentBlock, stream StreamFunc) (*PromptResult, error) {
			return nil, context.Canceled
		}
		agent.sessions["sess-1"] = &AgentSession{ID: "sess-1"}
		agent.activePrompts["sess-1"] = func() {}
	})
	if resp.Error != nil {
		t.Fatalf("error = %+v, raw response %s", resp.Error, raw)
	}
	data, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var result PromptResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal PromptResult: %v", err)
	}
	if result.StopReason != "cancelled" {
		t.Fatalf("StopReason = %q, want cancelled", result.StopReason)
	}
}

func TestAgentMachineSpawnSuccessUnaffected(t *testing.T) {
	resp, raw := handleAgentRequest(t, "_machine/spawn_agent", SpawnAgentParams{SessionID: "sess-1", Task: "work"}, func(agent *Agent) {
		agent.SetMachineHandlers(MachineHandlers{
			OnSpawnAgent: func(context.Context, *SpawnAgentParams) (*SpawnAgentResult, error) {
				return &SpawnAgentResult{AgentID: "agent-1"}, nil
			},
		})
	})
	if resp.Error != nil {
		t.Fatalf("error = %+v, raw response %s", resp.Error, raw)
	}
	data, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var result SpawnAgentResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal SpawnAgentResult: %v", err)
	}
	if result.AgentID != "agent-1" {
		t.Fatalf("AgentID = %q, want agent-1", result.AgentID)
	}
}

func handleAgentRequest(t *testing.T, method string, params any, setup func(*Agent)) (Response, []byte) {
	t.Helper()
	return handleAgentRequestWithContext(t, context.Background(), method, params, setup)
}

func handleAgentRequestWithContext(t *testing.T, ctx context.Context, method string, params any, setup func(*Agent)) (Response, []byte) {
	t.Helper()
	var out bytes.Buffer
	agent := NewAgent("test", "0.1", AgentHandlers{})
	agent.transport = NewTransport(strings.NewReader(""), &out)
	if setup != nil {
		setup(agent)
	}
	req := &Request{JSONRPC: "2.0", ID: "1", Method: method, Params: mustMarshal(t, params)}
	agent.handleRequest(ctx, req)

	raw := bytes.TrimSpace(out.Bytes())
	var resp Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal response %q: %v", raw, err)
	}
	return resp, raw
}
