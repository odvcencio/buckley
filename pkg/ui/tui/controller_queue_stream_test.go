package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/telemetry"
)

func TestStreamResponse_ContinuouslyQueuedTurnsBoundContextAndPreserveHistory(t *testing.T) {
	const turns = 10
	answer := strings.Repeat("useful output ", 640)
	var ctrl *Controller
	var requests, maxRequestBytes int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests++
		maxRequestBytes = max(maxRequestBytes, len(body))
		if requests < turns {
			ctrl.submitPromptWithIntent(fmt.Sprintf("queued request %d", requests), false, agentloop.ReadOnlyIntent)
		}
		payload, _ := json.Marshal(map[string]any{
			"id": fmt.Sprintf("response-%d", requests), "model": "gpt-4o",
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": answer}, "finish_reason": "stop"}},
		})
		w.Header().Set("Content-Type", "text/event-stream")
		writeTerminalPartialSSE(t, w, string(payload))
		writeTerminalPartialSSE(t, w, "[DONE]")
	}))
	defer server.Close()
	ctrl, sess, app := newTerminalPartialController(t, server.URL, nil)
	ctrl.cfg.Resources.SessionMemoryBytes = 64 << 10
	ctrl.cfg.Memory.HyphaeRecall = false
	sess.undoStoreChecked = true
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sess.Cancel, sess.Streaming = cancel, true
	// Consume UI events as a running terminal would, so this tests the turn
	// lifecycle rather than the intentionally bounded event delivery queue.
	done := make(chan struct{})
	go func() {
		defer close(done)
		ctrl.streamResponseWithIntent(ctx, "first request", sess, agentloop.ReadOnlyIntent)
	}()
	timeout := time.NewTimer(60 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case <-done:
			goto finished
		case <-app.messages:
		case <-timeout.C:
			t.Fatal("queued streaming did not finish")
		}
	}
finished:
	if requests != turns {
		t.Fatalf("provider requests = %d, want %d", requests, turns)
	}
	if maxRequestBytes > 96<<10 {
		t.Fatalf("queued context grew to %d bytes despite a 32 KiB working budget", maxRequestBytes)
	}
	if sess.Streaming || sess.Cancel != nil || len(sess.MessageQueue) != 0 {
		t.Fatalf("stream lifecycle not released: streaming=%v queue=%d", sess.Streaming, len(sess.MessageQueue))
	}
	if ctx.Err() != context.Canceled {
		t.Fatal("completed request context was not released")
	}
	full := conversation.New(sess.ID)
	if err := full.LoadFromStorage(ctrl.store); err != nil {
		t.Fatal(err)
	}
	var users, assistants int
	for _, msg := range full.Messages {
		switch msg.Role {
		case "user":
			want := "first request"
			if users > 0 {
				want = fmt.Sprintf("queued request %d", users)
			}
			if got := conversation.GetContentAsString(msg.Content); got != want {
				t.Fatalf("user %d = %q, want %q", users, got, want)
			}
			users++
		case "assistant":
			if conversation.GetContentAsString(msg.Content) != answer {
				t.Fatalf("assistant %d lost its full output", assistants)
			}
			assistants++
		}
	}
	if users != turns || assistants != turns {
		t.Fatalf("canonical history users/assistants = %d/%d", users, assistants)
	}
	resumed := conversation.New(sess.ID)
	if err := resumed.LoadWorkingSet(ctrl.store, sessionWorkingMemoryBytes(ctrl.cfg)); err != nil {
		t.Fatal(err)
	}
	if !resumed.HasPartialHistory() || resumed.WorkingSetBytes() > sessionWorkingMemoryBytes(ctrl.cfg) {
		t.Fatalf("resume not bounded: partial=%v bytes=%d", resumed.HasPartialHistory(), resumed.WorkingSetBytes())
	}
	t.Logf("turns=%d max_request_bytes=%d working_bytes=%d canonical_messages=%d", requests, maxRequestBytes, sess.Conversation.WorkingSetBytes(), len(full.Messages))
}

func TestFinishStreamTurn_PreservesQueuedOptionsAndReleasesContexts(t *testing.T) {
	ctrl, sess, _ := newTerminalPartialController(t, "http://unused.invalid", nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sess.Cancel, sess.Streaming = cancel, true
	sess.MessageQueue = []QueuedMessage{
		{Content: "first", TaskIntent: agentloop.MutationIntent, DisableTools: true, Steering: true},
		{Content: "second", TaskIntent: agentloop.ReadOnlyIntent},
	}
	nextCtx, queued, ok := ctrl.finishStreamTurn(sess, true)
	if !ok || queued.Content != "first" || queued.TaskIntent != agentloop.MutationIntent || !queued.Steering || !sess.DisableToolsNextTurn {
		t.Fatalf("queued options lost: queued=%+v disable_tools=%v", queued, sess.DisableToolsNextTurn)
	}
	if ctx.Err() != context.Canceled || nextCtx.Err() != nil || !sess.Streaming || sess.Cancel == nil || len(sess.MessageQueue) != 1 || sess.MessageQueue[0].Content != "second" {
		t.Fatal("queued turn did not take ownership after releasing the completed context")
	}
	lastCtx, queued, ok := ctrl.finishStreamTurn(sess, true)
	if !ok || queued.Content != "second" || queued.TaskIntent != agentloop.ReadOnlyIntent || sess.DisableToolsNextTurn || nextCtx.Err() != context.Canceled {
		t.Fatal("second queued turn lost its options or previous context")
	}
	_, _, ok = ctrl.finishStreamTurn(sess, true)
	if ok || lastCtx.Err() != context.Canceled || sess.MessageQueue != nil || sess.Streaming || sess.Cancel != nil {
		t.Fatal("drained queue retained a context or backing storage")
	}
}

func TestStreamResponse_QueuedSteeringAndFailure(t *testing.T) {
	for _, steering := range []bool{true, false} {
		t.Run(fmt.Sprintf("steering=%v", steering), func(t *testing.T) {
			var ctrl *Controller
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				requests++
				if requests == 1 {
					ctrl.submitPromptWithIntent("next request", steering, agentloop.ReadOnlyIntent)
					if steering {
						select {
						case <-r.Context().Done():
						case <-time.After(5 * time.Second):
							t.Error("steering did not cancel the provider request")
						}
					} else {
						http.Error(w, `{"error":{"message":"invalid fixture request","type":"invalid_request_error"}}`, http.StatusBadRequest)
					}
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				writeTerminalPartialSSE(t, w, `{"id":"next","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"steering applied"},"finish_reason":"stop"}]}`)
				writeTerminalPartialSSE(t, w, "[DONE]")
			}))
			defer server.Close()
			hub := telemetry.NewHub()
			events, unsubscribe := hub.Subscribe()
			defer unsubscribe()
			ctrl, sess, _ := newTerminalPartialController(t, server.URL, hub)
			ctrl.cfg.Memory.HyphaeRecall = false
			sess.undoStoreChecked = true
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			sess.Cancel, sess.Streaming = cancel, true
			ctrl.emitStreaming(sess.ID, true)
			ctrl.streamResponseWithIntent(ctx, "first request", sess, agentloop.ReadOnlyIntent)
			wantRequests, wantQueued := 1, 1
			if steering {
				wantRequests, wantQueued = 2, 0
				finals := assistantFinals(sess.Conversation.Messages)
				if len(finals) != 1 || finals[0] != "steering applied" {
					t.Fatalf("steering finals = %v", finals)
				}
			}
			if requests != wantRequests || len(sess.MessageQueue) != wantQueued || sess.Streaming || sess.Cancel != nil || ctx.Err() != context.Canceled {
				t.Fatalf("requests=%d queue=%d streaming=%v context=%v", requests, len(sess.MessageQueue), sess.Streaming, ctx.Err())
			}
			starts, ends := 0, 0
			for {
				select {
				case event := <-events:
					switch event.Type {
					case telemetry.EventModelStreamStarted:
						starts++
					case telemetry.EventModelStreamEnded:
						ends++
					}
				default:
					if starts != wantRequests || ends != starts {
						t.Fatalf("stream telemetry starts/ends = %d/%d, want %d/%d", starts, ends, wantRequests, wantRequests)
					}
					return
				}
			}
		})
	}
}
