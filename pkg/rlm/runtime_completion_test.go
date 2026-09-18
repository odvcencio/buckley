package rlm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/telemetry"
)

func TestRuntimeExecute_TokenBudgetStopsWithIncompletePartialAnswer(t *testing.T) {
	var bodies []string
	hub := telemetry.NewHub()
	events, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, coordinatorSetAnswerResponseWithDetails(
			"chatcmpl-budget",
			"budget partial",
			false,
			0.8,
			10,
			5,
			[]string{"scratchpad:alpha"},
			[]string{"retry with more budget"},
		))
	}))
	defer server.Close()

	rt := newCompletionTestRuntime(t, server, func(cfg *Config) {
		cfg.Coordinator.MaxTokensBudget = 15
		cfg.Coordinator.ConfidenceThreshold = 0.95
	})
	rt.telemetry = hub
	rt.sessionID = "budget-session"

	answer, err := rt.Execute(context.Background(), "retain partial on budget stop")
	var incomplete *agentloop.IncompleteTurnError
	if err == nil || !errors.As(err, &incomplete) || incomplete.Code != "token_budget" || !strings.Contains(err.Error(), "token budget") {
		t.Fatalf("Execute error = %v, want token budget incomplete error", err)
	}
	if answer == nil {
		t.Fatal("answer = nil")
	}
	if answer.Content != "budget partial" || answer.Ready || answer.Confidence != 0.8 || answer.TokensUsed != 15 {
		t.Fatalf("answer = %+v, want retained non-ready partial with tokens/confidence", answer)
	}
	if len(answer.Artifacts) != 1 || answer.Artifacts[0] != "scratchpad:alpha" ||
		len(answer.NextSteps) != 1 || answer.NextSteps[0] != "retry with more budget" {
		t.Fatalf("answer artifacts/next steps = %+v/%+v, want retained details", answer.Artifacts, answer.NextSteps)
	}
	if len(bodies) != 1 {
		t.Fatalf("model requests = %d, want no extra request after budget stop", len(bodies))
	}
	warnings := drainTelemetryEvents(events, telemetry.EventRLMBudgetWarning)
	if len(warnings) != 1 {
		t.Fatalf("budget warnings = %d, want exactly one; events=%+v", len(warnings), warnings)
	}
	warning := warnings[0]
	if warning.SessionID != "budget-session" {
		t.Fatalf("budget warning session = %q, want budget-session", warning.SessionID)
	}
	wantData := map[string]any{
		"tokens_used": 15,
		"max_tokens":  15,
		"reason":      "token_budget_exhausted",
		"action":      "return_incomplete",
	}
	for key, want := range wantData {
		if got := warning.Data[key]; got != want {
			t.Fatalf("budget warning data[%s] = %#v, want %#v; data=%+v", key, got, want, warning.Data)
		}
	}
	for _, forbidden := range []string{"budget partial", "scratchpad:alpha", "retry with more budget", "token budget"} {
		if strings.Contains(fmt.Sprint(warning.Data), forbidden) {
			t.Fatalf("budget warning leaked %q in data: %+v", forbidden, warning.Data)
		}
	}
}

func TestRuntimeExecute_StreamPartialsEnabledPublishesCoordinatorProgress(t *testing.T) {
	var calls atomic.Int32
	hub := telemetry.NewHub()
	events, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		call := calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if call == 1 {
			_, _ = io.WriteString(w, coordinatorSetAnswerResponse("chatcmpl-progress", "progress draft", false, 0.2, 1, 1))
			return
		}
		_, _ = io.WriteString(w, coordinatorPlainResponse("chatcmpl-final", "completed answer", "stop", 1, 1, ""))
	}))
	defer server.Close()

	rt := newCompletionTestRuntime(t, server, func(cfg *Config) {
		cfg.Coordinator.StreamPartials = true
	})
	rt.telemetry = hub
	var hookCalls int
	rt.OnIteration(func(IterationEvent) {
		hookCalls++
	})

	answer, err := rt.Execute(context.Background(), "publish coordinator progress")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if answer == nil || answer.Content != "completed answer" || !answer.Ready {
		t.Fatalf("answer = %+v, want completed answer", answer)
	}
	iterationEvents := countTelemetryEvents(drainAllTelemetryEvents(events), telemetry.EventRLMIteration)
	if hookCalls == 0 || iterationEvents == 0 || hookCalls != iterationEvents {
		t.Fatalf("iteration hooks/events = %d/%d, want matching non-zero coordinator progress publication", hookCalls, iterationEvents)
	}
}

func TestRuntimeExecute_StreamPartialsDisabledSuppressesProgressButPreservesIncompleteResult(t *testing.T) {
	hub := telemetry.NewHub()
	events, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, coordinatorSetAnswerResponse("chatcmpl-no-progress", "retained draft", false, 0.2, 10, 5))
	}))
	defer server.Close()

	rt := newCompletionTestRuntime(t, server, func(cfg *Config) {
		cfg.Coordinator.StreamPartials = false
		cfg.Coordinator.MaxTokensBudget = 15
	})
	rt.telemetry = hub
	rt.sessionID = "no-progress-session"
	var hookCalls int
	rt.OnIteration(func(IterationEvent) {
		hookCalls++
	})

	answer, err := rt.Execute(context.Background(), "suppress coordinator progress")
	var incomplete *agentloop.IncompleteTurnError
	if err == nil || !errors.As(err, &incomplete) || incomplete.Code != "token_budget" {
		t.Fatalf("Execute error = %v, want token budget incomplete error", err)
	}
	if answer == nil || answer.Content != "retained draft" || answer.Ready || answer.TokensUsed != 15 {
		t.Fatalf("answer = %+v, want retained incomplete draft", answer)
	}
	if got := hookCalls; got != 0 {
		t.Fatalf("iteration hook calls = %d, want 0", got)
	}

	allEvents := drainAllTelemetryEvents(events)
	if got := countTelemetryEvents(allEvents, telemetry.EventRLMIteration); got != 0 {
		t.Fatalf("rlm iteration telemetry events = %d, want 0", got)
	}
	if got := countTelemetryEvents(allEvents, telemetry.EventRLMBudgetWarning); got != 1 {
		t.Fatalf("rlm budget warning telemetry events = %d, want 1", got)
	}
	if !containsRuntimeTerminationTelemetry(allEvents) {
		t.Fatalf("telemetry events = %+v, want coordinator termination telemetry", allEvents)
	}
}

func TestRuntimeExecute_HighConfidenceExplicitPartialDoesNotAutoComplete(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "application/json")
		switch len(bodies) {
		case 1:
			_, _ = io.WriteString(w, coordinatorSetAnswerResponse("chatcmpl-partial", "high confidence partial", false, 0.99, 10, 5))
		default:
			_, _ = io.WriteString(w, coordinatorPlainResponse("chatcmpl-final", "final synthesized answer", "stop", 6, 4, ""))
		}
	}))
	defer server.Close()

	rt := newCompletionTestRuntime(t, server, func(cfg *Config) {
		cfg.Coordinator.ConfidenceThreshold = 0.95
	})

	answer, err := rt.Execute(context.Background(), "confidence is not readiness")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if answer == nil || answer.Content != "final synthesized answer" || !answer.Ready {
		t.Fatalf("answer = %+v, want final conclusive answer", answer)
	}
	if len(bodies) != 2 {
		t.Fatalf("model requests = %d, want second request after explicit ready=false", len(bodies))
	}
}

func TestRuntimeExecute_ExplicitReadyCompletesOnBudgetLimitRound(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, coordinatorSetAnswerResponse("chatcmpl-ready", "complete answer", true, 0.99, 10, 5))
	}))
	defer server.Close()

	rt := newCompletionTestRuntime(t, server, func(cfg *Config) {
		cfg.Coordinator.MaxTokensBudget = 15
		cfg.Coordinator.ConfidenceThreshold = 0.95
	})

	answer, err := rt.Execute(context.Background(), "ready beats budget on same round")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if answer == nil || answer.Content != "complete answer" || !answer.Ready || answer.Confidence != 0.99 || answer.TokensUsed != 15 {
		t.Fatalf("answer = %+v, want explicit ready success", answer)
	}
	if len(bodies) != 1 {
		t.Fatalf("model requests = %d, want immediate success", len(bodies))
	}
}

func TestRuntimeExecute_InternalDeadlineRetainsPartialWithoutReady(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		request := requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if request == 1 {
			_, _ = io.WriteString(w, coordinatorSetAnswerResponse("chatcmpl-deadline", "deadline partial", false, 0.4, 1, 1))
			return
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	rt := newCompletionTestRuntime(t, server, func(cfg *Config) {
		cfg.Coordinator.MaxWallTime = time.Second
		cfg.Coordinator.MaxTokensBudget = 1000
	})

	answer, err := rt.Execute(context.Background(), "deadline should retain partial")
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Execute error = %v, want context deadline cause", err)
	}
	var incomplete *agentloop.IncompleteTurnError
	if !errors.As(err, &incomplete) || incomplete.Code != "runtime_deadline" {
		t.Fatalf("Execute error = %v, want runtime_deadline incomplete error", err)
	}
	if answer == nil || answer.Content != "deadline partial" || answer.Ready || answer.TokensUsed != 2 {
		t.Fatalf("answer = %+v, want retained non-ready deadline partial", answer)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("model requests = %d, want second request to hit runtime deadline", got)
	}
}

func TestRuntimeExecute_TruncatedCoordinatorOutputRetainsPublicDraftOnly(t *testing.T) {
	const privateReasoning = "PRIVATE_COORDINATOR_REASONING_SENTINEL"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, coordinatorPlainResponse("chatcmpl-truncated", "public truncated draft", "length", 7, 3, privateReasoning))
	}))
	defer server.Close()

	rt := newCompletionTestRuntime(t, server, func(cfg *Config) {})

	answer, err := rt.Execute(context.Background(), "retain truncated public draft")
	if err == nil {
		t.Fatal("Execute error = nil, want truncated/incomplete error")
	}
	if answer == nil || answer.Content != "public truncated draft" || answer.Ready || answer.TokensUsed != 10 {
		t.Fatalf("answer = %+v, want retained public truncated draft only", answer)
	}
	if strings.Contains(answer.Content, privateReasoning) {
		t.Fatalf("private reasoning leaked into answer content: %q", answer.Content)
	}
}

func newCompletionTestRuntime(t *testing.T, server *httptest.Server, configure func(*Config)) *Runtime {
	t.Helper()
	mgr := newCoordinatorTestManager(t, server)
	cfg := DefaultConfig()
	if configure != nil {
		configure(&cfg)
	}
	rt, err := NewRuntime(cfg, RuntimeDeps{Models: mgr})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	return rt
}

func coordinatorSetAnswerResponse(id, content string, ready bool, confidence float64, input, output int) string {
	return coordinatorSetAnswerResponseWithDetails(id, content, ready, confidence, input, output, nil, nil)
}

func coordinatorSetAnswerResponseWithDetails(id, content string, ready bool, confidence float64, input, output int, artifacts, nextSteps []string) string {
	args := map[string]any{
		"content":    content,
		"ready":      ready,
		"confidence": confidence,
	}
	if len(artifacts) > 0 {
		args["artifacts"] = artifacts
	}
	if len(nextSteps) > 0 {
		args["next_steps"] = nextSteps
	}
	argBytes, _ := json.Marshal(args)
	argumentString, _ := json.Marshal(string(argBytes))
	return fmt.Sprintf(`{
		"id":"`+id+`","model":"gpt-4o",
		"choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_set_answer","type":"function","function":{"name":"set_answer","arguments":%s}}]},"finish_reason":"tool_calls"}],
		"usage":{"prompt_tokens":`+intLiteral(input)+`,"completion_tokens":`+intLiteral(output)+`,"total_tokens":`+intLiteral(input+output)+`}
	}`, string(argumentString))
}

func coordinatorPlainResponse(id, content, finish string, input, output int, reasoning string) string {
	reasoningField := ""
	if reasoning != "" {
		reasoningField = `,"reasoning":"` + reasoning + `"`
	}
	return `{
		"id":"` + id + `","model":"gpt-4o",
		"choices":[{"index":0,"message":{"role":"assistant","content":"` + content + `"` + reasoningField + `},"finish_reason":"` + finish + `"}],
		"usage":{"prompt_tokens":` + intLiteral(input) + `,"completion_tokens":` + intLiteral(output) + `,"total_tokens":` + intLiteral(input+output) + `}
	}`
}

func boolLiteral(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func formatConfidence(v float64) string {
	switch v {
	case 0.4:
		return "0.4"
	case 0.8:
		return "0.8"
	case 0.99:
		return "0.99"
	default:
		return "0"
	}
}

func intLiteral(v int) string {
	return fmt.Sprintf("%d", v)
}

func drainTelemetryEvents(events <-chan telemetry.Event, typ telemetry.EventType) []telemetry.Event {
	var out []telemetry.Event
	for {
		select {
		case event := <-events:
			if event.Type == typ {
				out = append(out, event)
			}
		default:
			return out
		}
	}
}

func drainAllTelemetryEvents(events <-chan telemetry.Event) []telemetry.Event {
	var out []telemetry.Event
	for {
		select {
		case event := <-events:
			out = append(out, event)
		default:
			return out
		}
	}
}

func countTelemetryEvents(events []telemetry.Event, typ telemetry.EventType) int {
	count := 0
	for _, event := range events {
		if event.Type == typ {
			count++
		}
	}
	return count
}

func containsRuntimeTerminationTelemetry(events []telemetry.Event) bool {
	for _, event := range events {
		if event.Type == telemetry.EventDebug && event.Data["source"] == "rlm.controller" {
			return true
		}
	}
	return false
}
