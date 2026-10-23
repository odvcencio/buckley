package rlm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/tool"
)

func TestSubAgent_ReservesExplorationUnderShortDeadlines(t *testing.T) {
	for _, tt := range []struct {
		name                 string
		window, lead, wanted time.Duration
	}{
		{"reserve exceeds phase", 30 * time.Second, 90 * time.Second, 15 * time.Second},
		{"critic shortened phase", 45 * time.Second, 85 * time.Second, 22500 * time.Millisecond},
		{"reserve equals phase", time.Minute, time.Minute, 30 * time.Second},
		{"explicit reserve fits", 255 * time.Second, 165 * time.Second, 165 * time.Second},
		{"normal reserve", 6 * time.Minute, 90 * time.Second, 90 * time.Second},
	} {
		t.Run(tt.name, func(t *testing.T) {
			start := time.Now()
			deadline := start.Add(tt.window)
			ctx, cancel := context.WithDeadline(context.Background(), deadline)
			t.Cleanup(cancel)
			a := &SubAgent{adaptive: true, synthesisLead: tt.lead}
			if got := a.effectiveSynthesisLead(deadline, start); got != tt.wanted {
				t.Fatalf("reserve=%s, want %s", got, tt.wanted)
			}
			if a.shouldSynthesize(ctx, 0, 4, start) {
				t.Fatal("first request lost inspection tools")
			}
			toolCtx, stop := a.explorationContext(ctx, start)
			t.Cleanup(stop)
			toolDeadline, ok := toolCtx.Deadline()
			if !ok || !toolDeadline.Equal(deadline.Add(-tt.wanted)) || toolCtx.Err() != nil {
				t.Fatalf("tool deadline=%s, error=%v", toolDeadline, toolCtx.Err())
			}
			if !a.shouldSynthesize(ctx, 3, 4, start) {
				t.Fatal("last turn no longer reserves final synthesis")
			}
			cancel()
			if !errors.Is(toolCtx.Err(), context.Canceled) {
				t.Fatal("command cancellation did not reach tool phase")
			}
		})
	}
}

func TestSubAgent_ShortDeadlineSynthesisCutoffStaysFixed(t *testing.T) {
	now := time.Now()
	start, deadline := now.Add(-20*time.Second), now.Add(10*time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	t.Cleanup(cancel)
	a := &SubAgent{adaptive: true, synthesisLead: 90 * time.Second}
	if !a.shouldSynthesize(ctx, 1, 4, start) {
		t.Fatal("phase past its exploration window did not enter synthesis")
	}
	toolCtx, stop := a.explorationContext(ctx, start)
	t.Cleanup(stop)
	got, _ := toolCtx.Deadline()
	if !got.Equal(start.Add(15*time.Second)) || !errors.Is(toolCtx.Err(), context.DeadlineExceeded) {
		t.Fatalf("cutoff moved forward: deadline=%s error=%v", got, toolCtx.Err())
	}
}

func TestSubAgentExecute_ShortDeadlineInspectsBeforeAnswering(t *testing.T) {
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
		if requests == 1 && len(request.Tools) > 0 {
			fmt.Fprint(w, `{"id":"inspect","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"read-proof","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"proof.txt\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
			return
		}
		if requests == 2 && !strings.Contains(string(request.Messages), "captured evidence") {
			t.Error("final answer request lost the inspection result")
		}
		fmt.Fprint(w, `{"id":"answer","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"answered with captured evidence"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25}}`)
	}))
	t.Cleanup(server.Close)
	registry := tool.NewEmptyRegistry()
	reader := &countingReadTool{fakeReadTool: fakeReadTool{name: "read_file", body: "captured evidence"}}
	registry.Register(reader)
	t.Cleanup(func() { registry.Close() })
	agent, err := NewSubAgent(SubAgentConfig{ID: "short-review", Model: "gpt-4o", MaxIterations: 4, Adaptive: true, SynthesisLead: 90 * time.Second, AllowedTools: []string{"read_file"}}, SubAgentDeps{Models: newSubAgentTestManager(t, server), Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	result, err := agent.Execute(ctx, "inspect proof.txt before answering")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || reader.calls != 1 || len(result.ToolCalls) != 1 || !result.ToolCalls[0].Success || result.Summary != "answered with captured evidence" {
		t.Fatalf("inspection loop missing: requests=%d reads=%d result=%+v", requests, reader.calls, result)
	}
}
