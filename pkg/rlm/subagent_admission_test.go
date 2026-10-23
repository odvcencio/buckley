package rlm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestSubAgent_InspectionAdmissionUsesCompletedCycle(t *testing.T) {
	for _, tt := range []struct {
		name              string
		agent             SubAgent
		elapsed, left     time.Duration
		previous          time.Duration
		deadline, wantEnd bool
	}{
		{"profiled review", SubAgent{adaptive: true, synthesisLead: 85 * time.Second}, 15400 * time.Millisecond, 44600 * time.Millisecond, 15400 * time.Millisecond, true, true},
		{"ample time", SubAgent{adaptive: true, synthesisLead: 30 * time.Second}, 0, 120 * time.Second, 15 * time.Second, true, false},
		{"no prior cycle", SubAgent{adaptive: true, synthesisLead: 30 * time.Second}, 0, 40 * time.Second, 0, true, false},
		{"explicit exploration cap", SubAgent{adaptive: true, synthesisLead: 30 * time.Second, explorationTimeout: 20 * time.Second}, 0, 120 * time.Second, 25 * time.Second, true, true},
		{"not adaptive", SubAgent{synthesisLead: 30 * time.Second}, 0, 40 * time.Second, 15 * time.Second, true, false},
		{"no synthesis reserve", SubAgent{adaptive: true}, 0, 40 * time.Second, 50 * time.Second, true, false},
		{"no command deadline", SubAgent{adaptive: true, synthesisLead: 30 * time.Second}, 0, 0, time.Hour, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Now()
			ctx := context.Background()
			if tt.deadline {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, now.Add(tt.left))
				t.Cleanup(cancel)
			}
			if got := tt.agent.shouldSynthesizeForLatency(ctx, now.Add(-tt.elapsed), tt.previous); got != tt.wantEnd {
				t.Fatalf("finalize=%t, want %t", got, tt.wantEnd)
			}
		})
	}
}

type delayedInspectionTool struct{ fakeReadTool }

func (f delayedInspectionTool) Execute(params map[string]any) (*builtin.Result, error) {
	time.Sleep(250 * time.Millisecond)
	return f.fakeReadTool.Execute(params)
}

func TestSubAgentExecute_FinalizesBeforeAnotherInspectionWillNotFit(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Tools    []json.RawMessage `json:"tools"`
			Messages json.RawMessage   `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requests++
		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			time.Sleep(350 * time.Millisecond)
			fmt.Fprint(w, `{"id":"inspect","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"read-proof","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"proof.txt\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
			return
		}
		if len(request.Tools) != 0 || !strings.Contains(string(request.Messages), "FINAL RESPONSE MODE") || !strings.Contains(string(request.Messages), "observed evidence") {
			t.Errorf("second request did not enter evidence-preserving final synthesis: tools=%d messages=%s", len(request.Tools), request.Messages)
		}
		fmt.Fprint(w, `{"id":"answer","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"answer with observed evidence"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25}}`)
	}))
	t.Cleanup(server.Close)
	registry := tool.NewEmptyRegistry()
	registry.Register(delayedInspectionTool{fakeReadTool{name: "read_file", body: "observed evidence"}})
	t.Cleanup(func() { registry.Close() })
	agent, err := NewSubAgent(SubAgentConfig{ID: "admission-review", Model: "gpt-4o", MaxIterations: 6, Adaptive: true, SynthesisLead: 4 * time.Second, AllowedTools: []string{"read_file"}}, SubAgentDeps{Models: newSubAgentTestManager(t, server), Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	result, err := agent.Execute(ctx, "inspect proof.txt then answer")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || len(result.ToolCalls) != 1 || !result.ToolCalls[0].Success || result.Summary != "answer with observed evidence" {
		t.Fatalf("unexpected rounds or evidence: requests=%d result=%+v", requests, result)
	}
}
