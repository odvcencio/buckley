package rlm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/tool"
)

func TestBatchDispatcher_PreCanceledDoesNotRouteTasks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("canceled batch reached the provider")
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	rt := newTimeoutRuntime(t, server, tool.NewEmptyRegistry(), nil)
	ctx, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("batch stopped by caller")
	cancel(cause)
	tasks := []SubTask{{ID: "first", Prompt: "never start"}, {ID: "second", Prompt: "never start"}}
	for _, parallel := range []bool{false, true} {
		t.Run(fmt.Sprintf("parallel=%t", parallel), func(t *testing.T) {
			results, err := rt.dispatcher.Execute(ctx, BatchRequest{Tasks: tasks, Parallel: parallel})
			if !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
				t.Fatalf("error = %v, want cancellation and its cause", err)
			}
			if len(results) != len(tasks) {
				t.Fatalf("results = %d, want %d", len(results), len(tasks))
			}
			for i, result := range results {
				if result.TaskID != tasks[i].ID || result.ModelUsed != "" || result.Error == "" {
					t.Errorf("canceled task was routed or lost its identity: %+v", result)
				}
			}
		})
	}
}

func TestBatchDispatcher_ConcurrentBatchesShareAdmissionLimit(t *testing.T) {
	var active, peak atomic.Int32
	started := make(chan struct{}, 6)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		current := active.Add(1)
		defer active.Add(-1)
		for previous := peak.Load(); current > previous; previous = peak.Load() {
			if peak.CompareAndSwap(previous, current) {
				break
			}
		}
		started <- struct{}{}
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, timeoutFinalResponse("parallel", "done", 2, 1))
	}))
	t.Cleanup(func() { unblock(); server.Close() })
	rt := newTimeoutRuntime(t, server, tool.NewEmptyRegistry(), func(cfg *Config) {
		cfg.SubAgent.MaxConcurrent = 2
		cfg.SubAgent.Timeout = time.Minute
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 2)
	for batch := 0; batch < 2; batch++ {
		go func(batch int) {
			tasks := make([]SubTask, 3)
			for i := range tasks {
				tasks[i] = SubTask{ID: fmt.Sprintf("batch-%d-task-%d", batch, i), Prompt: "finish"}
			}
			results, err := rt.dispatcher.Execute(ctx, BatchRequest{Tasks: tasks, Parallel: true})
			if err == nil && len(results) != len(tasks) {
				err = fmt.Errorf("results = %d, want %d", len(results), len(tasks))
			}
			if err == nil {
				for i, result := range results {
					if result.TaskID != tasks[i].ID || result.Summary != "done" {
						err = fmt.Errorf("result %d lost its identity or answer: %+v", i, result)
					}
				}
			}
			done <- err
		}(batch)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("parallel tasks did not start")
		}
	}
	unblock()
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("batch did not finish")
		}
	}
	if peak.Load() != 2 || len(rt.dispatcher.semaphore) != 0 {
		t.Fatalf("peak=%d held slots=%d, want 2 concurrent calls and no leaked slots", peak.Load(), len(rt.dispatcher.semaphore))
	}
}

func TestBatchDispatcher_CancelPreservesCompletedResultsAndQueuedIDs(t *testing.T) {
	var calls atomic.Int32
	secondStarted := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		if call == 1 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, timeoutFinalResponse("completed", "retained result", 2, 1))
			return
		}
		if call == 2 {
			close(secondStarted)
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(func() { close(release); server.Close() })
	rt := newTimeoutRuntime(t, server, tool.NewEmptyRegistry(), func(cfg *Config) {
		cfg.SubAgent.MaxConcurrent = 1
		cfg.SubAgent.Timeout = time.Minute
	})
	tasks := make([]SubTask, 32)
	for i := range tasks {
		tasks[i] = SubTask{ID: fmt.Sprintf("task-%d", i), Prompt: "complete task"}
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	t.Cleanup(func() { cancel(context.Canceled) })
	cause := errors.New("stop queued work")
	done := make(chan struct{})
	var results []BatchResult
	var batchErr error
	go func() {
		results, batchErr = rt.dispatcher.Execute(ctx, BatchRequest{Tasks: tasks, Parallel: true})
		close(done)
	}()
	select {
	case <-secondStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("second task did not start")
	}
	cancel(cause)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("batch did not stop after cancellation")
	}
	if !errors.Is(batchErr, context.Canceled) || !errors.Is(batchErr, cause) {
		t.Fatalf("batch error = %v, want cancellation cause", batchErr)
	}
	if calls.Load() != 2 || len(results) != len(tasks) {
		t.Fatalf("calls=%d results=%d, want 2 calls and %d results", calls.Load(), len(results), len(tasks))
	}
	completed := 0
	for i, result := range results {
		if result.TaskID != tasks[i].ID {
			t.Errorf("result %d task ID = %q, want %q", i, result.TaskID, tasks[i].ID)
		}
		if result.Error == "" {
			completed++
			if result.Summary != "retained result" {
				t.Errorf("completed result lost: %+v", result)
			}
		}
	}
	if completed != 1 || len(rt.dispatcher.semaphore) != 0 {
		t.Fatalf("completed=%d held slots=%d, want 1 completion and no held slots", completed, len(rt.dispatcher.semaphore))
	}
}

func BenchmarkBatchDispatcher_CanceledQueue(b *testing.B) {
	for _, count := range []int{128, 4096} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			d := &BatchDispatcher{semaphore: make(chan struct{}, 1)}
			d.semaphore <- struct{}{}
			tasks := make([]SubTask, count)
			for i := range tasks {
				tasks[i] = SubTask{ID: fmt.Sprintf("task-%d", i), Prompt: "queued task"}
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				results, err := d.executeParallel(ctx, tasks)
				if len(results) != count || !errors.Is(err, context.Canceled) {
					b.Fatal("lost canceled tasks")
				}
			}
		})
	}
}
