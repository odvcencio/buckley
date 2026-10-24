package tui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStreamResponse_PartialRetryLogsCauseAndHonorsCancellation(t *testing.T) {
	var requests atomic.Int32
	secret := "sk-or-v1-" + strings.Repeat("b", 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		writeTerminalPartialSSE(t, w, `{"id":"partial-retry","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"partial public draft"},"finish_reason":null}]}`)
		writeTerminalPartialSSE(t, w, `{"error":{"message":"upstream overloaded; token `+secret+`","code":503,"type":"server_error"}}`)
	}))
	t.Cleanup(server.Close)
	ctrl, sess, app := newTerminalPartialController(t, server.URL, nil)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	done := make(chan struct{})
	go func() { ctrl.streamResponse(ctx, "inspect this repository", sess); close(done) }()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case message := <-app.messages:
			if notice, ok := message.(AddMessageMsg); ok && notice.Source == "system" && strings.Contains(notice.Content, "Retrying model request") {
				if !strings.Contains(notice.Content, "upstream overloaded") || strings.Contains(notice.Content, secret) {
					t.Fatal(notice.Content)
				}
				cancel()
				goto canceled
			}
		case <-deadline:
			cancel()
			t.Fatal("partial response did not produce a retry notice")
		}
	}
canceled:
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not interrupt retry backoff")
	}
	if requests.Load() != 1 {
		t.Fatalf("canceled retry made %d provider requests", requests.Load())
	}
	if !conversationHasSystemContaining(sess.Conversation.Messages, "upstream overloaded") {
		t.Fatal("retry cause missing from conversation")
	}
	stored, err := ctrl.store.GetAllMessages(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range stored {
		if strings.Contains(message.Content, secret) {
			t.Fatal("credential leaked to persisted transcript")
		}
		if message.Role == "system" && strings.Contains(message.Content, "Retrying model request") && strings.Contains(message.Content, "upstream overloaded") {
			found = true
		}
	}
	if !found {
		t.Fatal("actual provider error was not persisted")
	}
	if strings.Contains(strings.Join(assistantFinals(sess.Conversation.Messages), " "), "partial public draft") {
		t.Fatal("partial text became an accepted final response")
	}
}
