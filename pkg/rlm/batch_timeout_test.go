package rlm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/coordination/reliability"
	"m31labs.dev/buckley/pkg/rules"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/transparency"
)

func TestBatchDispatcherTaskTimeoutRetainsPartialAndDoesNotRetry(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch call {
		case 1:
			_, _ = io.WriteString(w, timeoutToolCallResponse("chatcmpl-timeout-tool", "call_timeout_read", 5, 1))
		case 2:
			select {
			case <-r.Context().Done():
			case <-release:
			}
		default:
			t.Errorf("unexpected model replay call %d", call)
			http.Error(w, "unexpected replay", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(func() {
		close(release)
		server.Close()
	})
	rt := newTimeoutRuntime(t, server, tool.NewEmptyRegistry(), func(cfg *Config) {
		cfg.SubAgent.Timeout = 150 * time.Millisecond
	})
	rt.dispatcher.registry.Register(fakeReadTool{name: "read_file", body: "tool evidence"})

	results, err := rt.ExecuteTasks(context.Background(), BatchRequest{Tasks: []SubTask{{
		ID: "timeout-task", Prompt: "read then timeout", AllowedTools: []string{"read_file"},
	}}})
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, errSubAgentTaskTimeout) {
		t.Fatalf("ExecuteTasks error = %v, want sub-agent task timeout deadline", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("model calls = %d, want first tool response plus one timed-out synthesis", calls.Load())
	}
	got := singleTimeoutResult(t, results)
	if !strings.Contains(got.Error, "sub-agent task timeout") {
		t.Fatalf("result error = %q, want task timeout label", got.Error)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].Name != "read_file" {
		t.Fatalf("tool calls = %+v, want retained completed tool", got.ToolCalls)
	}
	wantUsage := transparency.TokenUsage{Input: 5, Output: 1, ReportedTotal: 6, UsageEvidencePresent: true}
	if !reflect.DeepEqual(got.Usage, wantUsage) || got.TokensUsed != 6 || got.InputTokens != 5 || got.OutputTokens != 1 {
		t.Fatalf("usage = %+v legacy=%d/%d/%d, want retained first response usage", got.Usage, got.TokensUsed, got.InputTokens, got.OutputTokens)
	}
	if len(got.ModelExecutions) != 1 || got.ModelExecutions[0].ResponseID != "chatcmpl-timeout-tool" {
		t.Fatalf("model executions = %+v, want first response identity retained", got.ModelExecutions)
	}
}

func TestBatchDispatcherParentDeadlineKeepsParentProvenance(t *testing.T) {
	parentCause := errors.New("parent orchestration deadline")
	var calls atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(func() {
		close(release)
		server.Close()
	})
	rt := newTimeoutRuntime(t, server, tool.NewEmptyRegistry(), func(cfg *Config) {
		cfg.SubAgent.Timeout = time.Second
	})
	ctx, cancel := context.WithDeadlineCause(context.Background(), time.Now().Add(150*time.Millisecond), parentCause)
	defer cancel()

	results, err := rt.ExecuteTasks(ctx, BatchRequest{Tasks: []SubTask{{ID: "parent-deadline", Prompt: "block"}}})
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, parentCause) {
		t.Fatalf("ExecuteTasks error = %v, want parent deadline and custom cause", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("model calls = %d, want one parent-canceled call", calls.Load())
	}
	got := singleTimeoutResult(t, results)
	if strings.Contains(got.Error, "sub-agent task timeout") {
		t.Fatalf("result error = %q, should preserve parent deadline provenance", got.Error)
	}
	if !strings.Contains(got.Error, parentCause.Error()) {
		t.Fatalf("result error = %q, want parent cause", got.Error)
	}
}

func TestBatchDispatcherQueueWaitDoesNotConsumeTaskTimeout(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, timeoutFinalResponse("chatcmpl-queued", "queued done", 2, 1))
	}))
	defer server.Close()
	rt := newTimeoutRuntime(t, server, tool.NewEmptyRegistry(), func(cfg *Config) {
		cfg.SubAgent.Timeout = 100 * time.Millisecond
		cfg.SubAgent.MaxConcurrent = 1
	})
	rt.dispatcher.semaphore <- struct{}{}
	done := make(chan struct {
		results []BatchResult
		err     error
	}, 1)
	go func() {
		results, err := rt.dispatcher.executeParallel(context.Background(), []SubTask{{ID: "queued-task", Prompt: "fast"}})
		done <- struct {
			results []BatchResult
			err     error
		}{results: results, err: err}
	}()

	time.Sleep(200 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatalf("model calls before slot release = %d, want task not started while queued", calls.Load())
	}
	<-rt.dispatcher.semaphore
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("executeParallel error = %v, want queued task to keep fresh timeout", got.err)
		}
		result := singleTimeoutResult(t, got.results)
		if result.TaskID != "queued-task" {
			t.Fatalf("TaskID = %q, want exact queued task ID", result.TaskID)
		}
		if result.Summary != "queued done" {
			t.Fatalf("result summary = %q, want queued done", result.Summary)
		}
	case <-time.After(time.Second):
		t.Fatal("executeParallel did not finish after releasing queue slot")
	}
}

func TestBatchDispatcherQueueWaitDoesNotConsumeTaskTimeoutAcrossModes(t *testing.T) {
	modes := []struct {
		name      string
		parallel  bool
		taskCount int
	}{
		{name: "single", taskCount: 1},
		{name: "sequential", taskCount: 2},
		{name: "parallel", parallel: true, taskCount: 2},
	}

	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, timeoutFinalResponse("mode-queued", "queued done", 2, 1))
			}))
			t.Cleanup(server.Close)

			rt := newTimeoutRuntime(t, server, tool.NewEmptyRegistry(), func(cfg *Config) {
				cfg.SubAgent.Timeout = 100 * time.Millisecond
				cfg.SubAgent.MaxConcurrent = 1
			})
			rt.dispatcher.semaphore <- struct{}{}
			t.Cleanup(func() {
				select {
				case <-rt.dispatcher.semaphore:
				default:
				}
			})

			ctx := &observedDoneContext{Context: context.Background(), observed: make(chan struct{})}
			tasks := make([]SubTask, mode.taskCount)
			for i := range tasks {
				tasks[i] = SubTask{ID: fmt.Sprintf("fresh-timeout-%d", i), Prompt: "fast after admission"}
			}
			done := make(chan struct {
				results []BatchResult
				err     error
			}, 1)
			go func() {
				results, err := rt.dispatcher.Execute(ctx, BatchRequest{Tasks: tasks, Parallel: mode.parallel})
				done <- struct {
					results []BatchResult
					err     error
				}{results: results, err: err}
			}()

			select {
			case <-ctx.observed:
			case <-time.After(time.Second):
				t.Fatal("batch did not reach the admission queue")
			}
			time.Sleep(200 * time.Millisecond)
			if calls.Load() != 0 {
				t.Fatalf("model calls before admission = %d, want queued task not started", calls.Load())
			}
			<-rt.dispatcher.semaphore

			select {
			case got := <-done:
				if got.err != nil {
					t.Fatalf("execute error = %v, want queue wait excluded from task timeout", got.err)
				}
				if len(got.results) != len(tasks) {
					t.Fatalf("results = %d, want %d", len(got.results), len(tasks))
				}
				for i, result := range got.results {
					if result.TaskID != tasks[i].ID || result.Summary != "queued done" || result.Error != "" {
						t.Errorf("result %d = %+v, want successful fresh execution", i, result)
					}
				}
			case <-time.After(time.Second):
				t.Fatal("batch did not finish after releasing queue slot")
			}
			if len(rt.dispatcher.semaphore) != 0 {
				t.Fatalf("held slots = %d, want no released permit leak", len(rt.dispatcher.semaphore))
			}
		})
	}
}

func TestBatchDispatcherQueueParentCancelReturnsExactTaskIDWithoutStarting(t *testing.T) {
	parentCause := fmt.Errorf("parent stopped while queued: %w", context.Canceled)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, timeoutFinalResponse("chatcmpl-queued-cancel", "should not run", 2, 1))
	}))
	defer server.Close()
	rt := newTimeoutRuntime(t, server, tool.NewEmptyRegistry(), func(cfg *Config) {
		cfg.SubAgent.Timeout = 100 * time.Millisecond
		cfg.SubAgent.MaxConcurrent = 1
	})
	rt.dispatcher.semaphore <- struct{}{}
	ctx, cancel := context.WithCancelCause(context.Background())
	done := make(chan struct {
		results []BatchResult
		err     error
	}, 1)
	go func() {
		results, err := rt.dispatcher.executeParallel(ctx, []SubTask{{ID: "queued-exact-id", Prompt: "must not start"}})
		done <- struct {
			results []BatchResult
			err     error
		}{results: results, err: err}
	}()
	cancel(parentCause)
	t.Cleanup(func() {
		select {
		case <-rt.dispatcher.semaphore:
		default:
		}
	})

	select {
	case got := <-done:
		if !errors.Is(got.err, context.Canceled) || !errors.Is(got.err, parentCause) {
			t.Fatalf("executeParallel error = %v, want parent cancellation cause", got.err)
		}
		result := singleTimeoutResult(t, got.results)
		if result.TaskID != "queued-exact-id" {
			t.Fatalf("TaskID = %q, want exact queued task ID", result.TaskID)
		}
		if !strings.Contains(result.Error, parentCause.Error()) {
			t.Fatalf("result error = %q, want parent cancellation cause", result.Error)
		}
	case <-time.After(time.Second):
		t.Fatal("executeParallel did not return after parent cancellation while queued")
	}
	if calls.Load() != 0 {
		t.Fatalf("model calls = %d, want queued task not started", calls.Load())
	}
}

func TestBatchDispatcherHostTimeoutDoesNotOpenBreakerButProviderTimeoutDoes(t *testing.T) {
	t.Run("host task timeouts are breaker neutral", func(t *testing.T) {
		var calls atomic.Int32
		release := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			select {
			case <-r.Context().Done():
			case <-release:
			}
		}))
		t.Cleanup(func() {
			close(release)
			server.Close()
		})
		rt := newTimeoutRuntime(t, server, tool.NewEmptyRegistry(), func(cfg *Config) {
			cfg.SubAgent.Timeout = 150 * time.Millisecond
		})
		rt.dispatcher.breaker = reliability.NewCircuitBreaker(reliability.CircuitBreakerConfig{MaxFailures: 1})
		for i := 0; i < 2; i++ {
			_, err := rt.ExecuteTasks(context.Background(), BatchRequest{Tasks: []SubTask{{ID: fmt.Sprintf("host-timeout-%d", i), Prompt: "block"}}})
			if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, errSubAgentTaskTimeout) {
				t.Fatalf("run %d error = %v, want host task timeout", i, err)
			}
		}
		if calls.Load() != 2 {
			t.Fatalf("model calls = %d, want breaker to allow repeated host timeouts", calls.Load())
		}
		metrics := rt.dispatcher.breaker.Metrics()
		if metrics.FailureCount != 0 || metrics.SuccessCount != 0 || rt.dispatcher.breaker.ConsecutiveFailures() != 0 {
			t.Fatalf("breaker metrics = %+v consecutive=%d, want host timeouts unrecorded", metrics, rt.dispatcher.breaker.ConsecutiveFailures())
		}
	})

	t.Run("provider timeout opens breaker", func(t *testing.T) {
		var calls atomic.Int32
		release := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			select {
			case <-r.Context().Done():
			case <-release:
			}
		}))
		t.Cleanup(func() {
			close(release)
			server.Close()
		})
		rt := newTimeoutRuntime(t, server, tool.NewEmptyRegistry(), func(cfg *Config) {
			cfg.SubAgent.Timeout = 10 * time.Second
		})
		rt.models.SetRequestTimeout(5 * time.Millisecond)
		rt.dispatcher.breaker = reliability.NewCircuitBreaker(reliability.CircuitBreakerConfig{MaxFailures: 1})
		_, firstErr := rt.ExecuteTasks(context.Background(), BatchRequest{Tasks: []SubTask{{ID: "provider-timeout-1", Prompt: "block"}}})
		if !errors.Is(firstErr, context.DeadlineExceeded) || errors.Is(firstErr, errSubAgentTaskTimeout) {
			t.Fatalf("first error = %v, want provider/client deadline, not host task timeout", firstErr)
		}
		callsAfterFirstDispatch := calls.Load()
		if callsAfterFirstDispatch < 1 {
			t.Fatalf("model calls after first dispatch = %d, want provider/client timeout to reach HTTP server", callsAfterFirstDispatch)
		}
		_, secondErr := rt.ExecuteTasks(context.Background(), BatchRequest{Tasks: []SubTask{{ID: "provider-timeout-2", Prompt: "block"}}})
		if !errors.Is(secondErr, reliability.ErrCircuitOpen) {
			t.Fatalf("second error = %v, want opened circuit", secondErr)
		}
		if calls.Load() != callsAfterFirstDispatch {
			t.Fatalf("model calls changed from %d to %d after circuit opened; retry envelope count before opening is not a 5ms race-stable contract", callsAfterFirstDispatch, calls.Load())
		}
	})
}

func TestBatchDispatcherRetryUsesSingleTaskTimeout(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch call {
		case 1:
			time.Sleep(600 * time.Millisecond)
			http.Error(w, "non-retriable dispatch failure", http.StatusBadRequest)
		case 2:
			select {
			case <-time.After(600 * time.Millisecond):
				_, _ = io.WriteString(w, timeoutFinalResponse("chatcmpl-renewed-timeout-would-pass", "late renewed success", 2, 1))
			case <-r.Context().Done():
			case <-release:
			}
		default:
			t.Errorf("unexpected retry after task timeout: call %d", call)
			http.Error(w, "unexpected replay", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(func() {
		close(release)
		server.Close()
	})
	engine, err := rules.NewEngine()
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	rt := newTimeoutRuntime(t, server, tool.NewEmptyRegistry(), func(cfg *Config) {
		cfg.SubAgent.Timeout = time.Second
	})
	rt.engine = engine
	rt.dispatcher.engine = engine
	start := time.Now()

	results, err := rt.ExecuteTasks(context.Background(), BatchRequest{Tasks: []SubTask{{ID: "retry-timeout", Prompt: "retry once"}}})
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, errSubAgentTaskTimeout) {
		t.Fatalf("ExecuteTasks error = %v, want shared task timeout across retry", err)
	}
	if !strings.Contains(err.Error(), "HTTP 400") {
		t.Fatalf("ExecuteTasks error = %v, want earlier failed attempt retained", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("model calls = %d, want one failed call and one timed-out retry", calls.Load())
	}
	got := singleTimeoutResult(t, results)
	if got.Summary == "late renewed success" || !strings.Contains(got.Error, "HTTP 400") {
		t.Fatalf("result = %+v, want timeout failure with earlier attempt retained, not renewed success", got)
	}
	if elapsed := time.Since(start); elapsed > 1300*time.Millisecond {
		t.Fatalf("elapsed = %s, want single timeout budget across retry attempts", elapsed)
	}
}

func TestSubAgentTaskTerminalErrorRejectsLateSuccessAfterDeadline(t *testing.T) {
	taskErr := errors.New("late host task deadline")
	taskCtx, cancel := context.WithDeadlineCause(context.Background(), time.Now().Add(-time.Nanosecond), taskErr)
	defer cancel()

	err := subAgentTaskTerminalError(nil, taskCtx)
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, taskErr) {
		t.Fatalf("terminal error = %v, want late success rejected with task context cause", err)
	}
}

func newTimeoutRuntime(t *testing.T, server *httptest.Server, registry *tool.Registry, configure func(*Config)) *Runtime {
	t.Helper()
	return newTaskResultRuntime(t, server, registry, configure)
}

func timeoutToolCallResponse(id, callID string, input, output int) string {
	return fmt.Sprintf(`{
		"id":%q,"model":"gpt-4o",
		"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":%q,"type":"function","function":{"name":"read_file","arguments":"{\"path\":\"fixture.txt\"}"}}]},"finish_reason":"tool_calls"}],
		"usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d}
	}`, id, callID, input, output, input+output)
}

func timeoutFinalResponse(id, content string, input, output int) string {
	return fmt.Sprintf(`{
		"id":%q,"model":"gpt-4o",
		"choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d}
	}`, id, content, input, output, input+output)
}

func singleTimeoutResult(t *testing.T, results []BatchResult) BatchResult {
	t.Helper()
	if len(results) != 1 {
		t.Fatalf("results = %+v, want one result", results)
	}
	return results[0]
}
