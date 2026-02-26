package model

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestStreamCancellation_ChannelsClose tests that both chunk and error channels
// close properly when context is cancelled mid-stream.
func TestStreamCancellation_ChannelsClose(t *testing.T) {
	// Create test server that streams slowly
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		// Send 5 chunks with delays
		for i := range 5 {
			chunk := ollamaChatResponse{
				Model: "test-model",
				Message: ollamaMessage{
					Role:    "assistant",
					Content: fmt.Sprintf("chunk %d", i),
				},
				Done: i == 4,
			}
			data, _ := json.Marshal(chunk)
			w.Write(data)
			w.Write([]byte("\n"))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			// Delay between chunks
			time.Sleep(50 * time.Millisecond)
		}
	}))
	defer server.Close()

	provider := NewOllamaProvider(server.URL, false)
	req := ChatRequest{
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: "test"}},
	}

	// Create cancellable context
	ctx, cancel := context.WithCancel(context.Background())

	chunkChan, errChan := provider.ChatCompletionStream(ctx, req)

	// Read a couple chunks
	chunksRead := 0
	for range 2 {
		select {
		case chunk, ok := <-chunkChan:
			if !ok {
				t.Fatal("chunk channel closed too early")
			}
			if len(chunk.Choices) == 0 {
				t.Error("received empty chunk")
			}
			chunksRead++
		case err := <-errChan:
			t.Fatalf("unexpected error before cancellation: %v", err)
		case <-time.After(1 * time.Second):
			t.Fatal("timeout waiting for chunks")
		}
	}

	if chunksRead != 2 {
		t.Errorf("expected to read 2 chunks, got %d", chunksRead)
	}

	// Cancel context
	cancel()

	// Verify both channels eventually close (no hang/leak)
	chunkClosed := false
	errClosed := false
	timeout := time.After(2 * time.Second)

drainLoop:
	for {
		select {
		case _, ok := <-chunkChan:
			if !ok {
				chunkClosed = true
			}
			if chunkClosed && errClosed {
				break drainLoop
			}
		case _, ok := <-errChan:
			if !ok {
				errClosed = true
			}
			if chunkClosed && errClosed {
				break drainLoop
			}
		case <-timeout:
			if !chunkClosed {
				t.Error("chunk channel did not close after context cancellation")
			}
			if !errClosed {
				t.Error("error channel did not close after context cancellation")
			}
			break drainLoop
		}
	}

	if !chunkClosed || !errClosed {
		t.Errorf("channels not closed properly: chunkClosed=%v, errClosed=%v", chunkClosed, errClosed)
	}
}

// TestStreamCancellation_NoGoroutineLeak tests that no goroutines leak after
// context cancellation and channel drain.
func TestStreamCancellation_NoGoroutineLeak(t *testing.T) {
	// Track server connections for cleanup
	activeConns := make(map[string]bool)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		for i := range 10 {
			// Check if client disconnected
			select {
			case <-r.Context().Done():
				return
			default:
			}

			chunk := ollamaChatResponse{
				Model: "test-model",
				Message: ollamaMessage{
					Role:    "assistant",
					Content: fmt.Sprintf("chunk %d", i),
				},
				Done: i == 9,
			}
			data, _ := json.Marshal(chunk)
			w.Write(data)
			w.Write([]byte("\n"))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			time.Sleep(20 * time.Millisecond)
		}
	}))
	defer server.Close()

	provider := NewOllamaProvider(server.URL, false)
	req := ChatRequest{
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: "test"}},
	}

	// Warm up: run once to initialize any persistent HTTP connections
	{
		ctx, cancel := context.WithCancel(context.Background())
		chunkChan, errChan := provider.ChatCompletionStream(ctx, req)
		<-chunkChan
		cancel()
		for range chunkChan {
		}
		<-errChan
	}

	// Force GC and get baseline goroutine count after warmup
	runtime.GC()
	time.Sleep(100 * time.Millisecond)
	baselineGoroutines := runtime.NumGoroutine()

	// Run multiple stream cancellations
	for range 5 {
		ctx, cancel := context.WithCancel(context.Background())
		chunkChan, errChan := provider.ChatCompletionStream(ctx, req)

		// Read a few chunks
		for range 2 {
			select {
			case <-chunkChan:
			case <-errChan:
			case <-time.After(500 * time.Millisecond):
				cancel()
				t.Fatal("timeout waiting for chunks")
			}
		}

		// Cancel and drain
		cancel()
		for range chunkChan {
		}
		// errChan should close automatically
		<-errChan
	}

	// Force GC and allow goroutines to clean up
	runtime.GC()
	time.Sleep(200 * time.Millisecond)

	// Check for goroutine leaks
	finalGoroutines := runtime.NumGoroutine()
	goroutineDiff := finalGoroutines - baselineGoroutines

	// HTTP client maintains connection pools, so allow some variance
	// The key is that repeated cancellations don't cause unbounded growth
	// Allow up to 5 goroutines for HTTP connection pooling and background tasks
	if goroutineDiff > 5 {
		t.Errorf("potential goroutine leak: baseline=%d, final=%d, diff=%d",
			baselineGoroutines, finalGoroutines, goroutineDiff)
		t.Logf("Note: some variance expected due to HTTP connection pooling")
	}

	_ = activeConns // avoid unused warning
}

// TestStreamCancellation_ErrorOnCancel tests that cancelling context before
// any chunks are sent results in clean error handling.
func TestStreamCancellation_ErrorOnCancel(t *testing.T) {
	// Create test server that delays before sending
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Delay to allow cancellation to happen first
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		chunk := ollamaChatResponse{
			Model: "test-model",
			Message: ollamaMessage{
				Role:    "assistant",
				Content: "response",
			},
			Done: true,
		}
		data, _ := json.Marshal(chunk)
		w.Write(data)
	}))
	defer server.Close()

	provider := NewOllamaProvider(server.URL, false)
	req := ChatRequest{
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: "test"}},
	}

	// Create context that will be cancelled immediately
	ctx, cancel := context.WithCancel(context.Background())

	chunkChan, errChan := provider.ChatCompletionStream(ctx, req)

	// Cancel immediately before any chunks
	cancel()

	// Verify we either get an error or channels close cleanly
	timeout := time.After(2 * time.Second)
	gotError := false
	chunkClosed := false
	errClosed := false

receiveLoop:
	for {
		select {
		case _, ok := <-chunkChan:
			if !ok {
				chunkClosed = true
			}
			// It's OK to receive some chunks
		case err, ok := <-errChan:
			if !ok {
				errClosed = true
			} else if err != nil {
				gotError = true
				// Context cancellation errors are acceptable
				if !strings.Contains(err.Error(), "context") {
					t.Logf("received non-context error (acceptable): %v", err)
				}
			}
		case <-timeout:
			t.Error("timeout waiting for stream to complete after cancellation")
			break receiveLoop
		}

		if chunkClosed && errClosed {
			break receiveLoop
		}
	}

	// Either we got an error or channels closed cleanly
	if !chunkClosed {
		t.Error("chunk channel did not close")
	}
	if !errClosed {
		t.Error("error channel did not close")
	}

	// Note: we don't require an error because the implementation may
	// cleanly close on ctx.Done() without sending an error
	t.Logf("cancellation handled: gotError=%v, chunkClosed=%v, errClosed=%v",
		gotError, chunkClosed, errClosed)
}

// TestStreamCancellation_MidChunk tests cancellation while processing a chunk
func TestStreamCancellation_MidChunk(t *testing.T) {
	cancelTrigger := make(chan struct{})
	cancelComplete := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		for i := range 20 {
			// Signal that we can cancel after first chunk
			if i == 1 {
				close(cancelTrigger)
				// Wait a bit for cancellation to happen
				<-cancelComplete
			}

			chunk := ollamaChatResponse{
				Model: "test-model",
				Message: ollamaMessage{
					Role:    "assistant",
					Content: fmt.Sprintf("chunk %d", i),
				},
				Done: i == 19,
			}
			data, _ := json.Marshal(chunk)
			w.Write(data)
			w.Write([]byte("\n"))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer server.Close()

	provider := NewOllamaProvider(server.URL, false)
	req := ChatRequest{
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: "test"}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	chunkChan, errChan := provider.ChatCompletionStream(ctx, req)

	// Wait for signal to cancel
	go func() {
		<-cancelTrigger
		time.Sleep(5 * time.Millisecond)
		cancel()
		close(cancelComplete)
	}()

	// Consume chunks until cancelled
	chunksReceived := 0
	timeout := time.After(5 * time.Second)

receiveLoop:
	for {
		select {
		case _, ok := <-chunkChan:
			if !ok {
				break receiveLoop
			}
			chunksReceived++
		case err, ok := <-errChan:
			if !ok {
				break receiveLoop
			}
			if err != nil {
				// Context errors are expected
				break receiveLoop
			}
		case <-timeout:
			t.Fatal("timeout waiting for stream cancellation")
		}
	}

	// We should have received at least the first chunk but not all 20
	if chunksReceived == 0 {
		t.Error("expected to receive at least one chunk before cancellation")
	}
	if chunksReceived >= 20 {
		t.Error("expected cancellation to stop stream before all chunks sent")
	}

	t.Logf("received %d chunks before cancellation", chunksReceived)
}

// TestStreamCancellation_MultipleStreams tests multiple concurrent streams
// with different cancellation timings.
func TestStreamCancellation_MultipleStreams(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		for i := range 10 {
			chunk := ollamaChatResponse{
				Model: "test-model",
				Message: ollamaMessage{
					Role:    "assistant",
					Content: fmt.Sprintf("chunk %d", i),
				},
				Done: i == 9,
			}
			data, _ := json.Marshal(chunk)
			w.Write(data)
			w.Write([]byte("\n"))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			time.Sleep(20 * time.Millisecond)
		}
	}))
	defer server.Close()

	provider := NewOllamaProvider(server.URL, false)

	// Launch 3 concurrent streams with different cancel timings
	done := make(chan bool, 3)

	for streamID := range 3 {
		go func(id int) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			req := ChatRequest{
				Model:    "test-model",
				Messages: []Message{{Role: "user", Content: fmt.Sprintf("test %d", id)}},
			}

			chunkChan, errChan := provider.ChatCompletionStream(ctx, req)

			// Read different number of chunks based on stream ID
			chunksToRead := id + 1
			for range chunksToRead {
				select {
				case <-chunkChan:
				case <-errChan:
				case <-time.After(1 * time.Second):
					t.Errorf("stream %d: timeout", id)
					done <- false
					return
				}
			}

			// Cancel
			cancel()

			// Drain channels
			for range chunkChan {
			}
			<-errChan

			done <- true
		}(streamID)
	}

	// Wait for all streams
	timeout := time.After(5 * time.Second)
	for range 3 {
		select {
		case success := <-done:
			if !success {
				t.Error("stream failed")
			}
		case <-timeout:
			t.Fatal("timeout waiting for concurrent streams")
		}
	}
}

// TestStreamCancellation_ServerError tests cancellation with server errors
func TestStreamCancellation_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return error response
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error": "internal server error"}`))
	}))
	defer server.Close()

	provider := NewOllamaProvider(server.URL, false)
	req := ChatRequest{
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: "test"}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	chunkChan, errChan := provider.ChatCompletionStream(ctx, req)

	// Should get error
	timeout := time.After(2 * time.Second)
	gotError := false

receiveLoop:
	for {
		select {
		case _, ok := <-chunkChan:
			if !ok {
				break receiveLoop
			}
			t.Error("did not expect chunks on error response")
		case err, ok := <-errChan:
			if !ok {
				break receiveLoop
			}
			if err != nil {
				gotError = true
				if !strings.Contains(err.Error(), "500") && !strings.Contains(err.Error(), "failed") {
					t.Errorf("unexpected error message: %v", err)
				}
			}
		case <-timeout:
			t.Fatal("timeout waiting for error")
		}
	}

	if !gotError {
		t.Error("expected error from server, got none")
	}
}
