package acp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAgentServe_ShutdownCancelsAndDrainsRequests(t *testing.T) {
	for _, mode := range []string{"disconnect", "read error", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			input, client := io.Pipe()
			t.Cleanup(func() { _ = input.Close() })
			t.Cleanup(func() { _ = client.Close() })
			started, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			agent := NewAgent("test", "0.1", AgentHandlers{
				OnPrompt: func(ctx context.Context, _ *AgentSession, _ []ContentBlock, _ StreamFunc) (*PromptResult, error) {
					close(started)
					<-ctx.Done()
					close(cancelled)
					<-release
					return nil, ctx.Err()
				},
			})
			agent.sessions["active"] = &AgentSession{ID: "active"}
			done := make(chan error, 1)
			go func() { done <- agent.Serve(ctx, input, io.Discard) }()
			if err := json.NewEncoder(client).Encode(&Request{
				JSONRPC: "2.0", ID: "1", Method: "session/prompt",
				Params: mustMarshal(t, PromptParams{SessionID: "active", Prompt: []ContentBlock{NewTextContent("wait")}}),
			}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("prompt did not start")
			}
			switch mode {
			case "disconnect":
				_ = client.Close()
			case "read error":
				_ = client.CloseWithError(errors.New("client failed"))
			case "cancel":
				cancel()
			}
			select {
			case <-cancelled:
			case <-time.After(time.Second):
				t.Fatal("shutdown left the prompt running")
			}
			select {
			case err := <-done:
				t.Fatalf("Serve returned before request cleanup: %v", err)
			default:
			}
			unblock()
			select {
			case err := <-done:
				if mode == "disconnect" && err != nil {
					t.Fatalf("disconnect error = %v", err)
				}
				if mode == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation error = %v", err)
				}
				if mode == "read error" && (err == nil || !strings.Contains(err.Error(), "client failed")) {
					t.Fatalf("read error = %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("Serve remained blocked after shutdown")
			}
			if len(agent.activePrompts) != 0 {
				t.Fatal("shutdown retained an active prompt")
			}
		})
	}
}

type notifyingWriteCloser struct {
	*io.PipeWriter
	started chan struct{}
	once    sync.Once
}

func (w *notifyingWriteCloser) Write(data []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	return w.PipeWriter.Write(data)
}

func TestAgentServe_ShutdownUnblocksWrites(t *testing.T) {
	for _, mode := range []string{"disconnect", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			input, client := io.Pipe()
			unreadOutput, output := io.Pipe()
			t.Cleanup(func() { _ = input.Close() })
			t.Cleanup(func() { _ = client.Close() })
			t.Cleanup(func() { _ = output.Close() })
			t.Cleanup(func() { _ = unreadOutput.Close() })
			writer := &notifyingWriteCloser{PipeWriter: output, started: make(chan struct{})}
			agent := NewAgent("test", "0.1", AgentHandlers{})
			done := make(chan error, 1)
			go func() { done <- agent.Serve(ctx, input, writer) }()
			if err := json.NewEncoder(client).Encode(&Request{JSONRPC: "2.0", ID: "1", Method: "_shutdown"}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-writer.started:
			case <-time.After(time.Second):
				t.Fatal("response did not start writing")
			}
			if mode == "cancel" {
				cancel()
			} else {
				_ = client.Close()
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("shutdown remained blocked writing to the client")
			}
		})
	}
}
