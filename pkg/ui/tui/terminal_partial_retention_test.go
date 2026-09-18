package tui

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/telemetry"
	"m31labs.dev/fluffyui/backend/sim"
)

const terminalPartialPrivateSentinel = "private-terminal-partial-sentinel"

func writeTerminalPartialSSE(t *testing.T, w http.ResponseWriter, payload string) {
	t.Helper()
	_, _ = io.WriteString(w, "data: "+payload+"\n\n")
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func newTerminalPartialController(t *testing.T, serverURL string, hub *telemetry.Hub) (*Controller, *SessionState, *WidgetApp) {
	t.Helper()
	cfg := newStreamIntegrationConfig(serverURL)
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	app, err := NewWidgetApp(WidgetAppConfig{Backend: sim.New(80, 24)})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}
	store, err := storage.New(t.TempDir() + "/terminal-partial.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSession("session-1"); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}
	conv := conversation.New("session-1")
	sess := &SessionState{ID: "session-1", Conversation: conv}
	ctrl := &Controller{app: app, cfg: cfg, modelMgr: mgr, store: store, telemetry: hub, workDir: t.TempDir(), sessions: []*SessionState{sess}}
	return ctrl, sess, app
}

func assertTerminalPartialRenderedAndPersisted(t *testing.T, app *WidgetApp, sess *SessionState, wantDraft string, wantTokens int) {
	t.Helper()
	if finals := assistantFinals(sess.Conversation.Messages); len(finals) != 0 {
		t.Fatalf("accepted assistant finals = %v, want none", finals)
	}
	if !conversationHasSystemContaining(sess.Conversation.Messages, "Incomplete result:") {
		t.Fatalf("conversation missing incomplete notice: %+v", roleList(sess.Conversation.Messages))
	}
	var renderedNotice, renderedDraft, acceptedAssistant, cleanupReplacement, tokenUpdates int
	for _, msg := range drainAllMessages(app) {
		switch v := msg.(type) {
		case AddMessageMsg:
			if v.Source == "system" && strings.Contains(v.Content, "Incomplete result:") {
				renderedNotice++
				if strings.Contains(v.Content, "maximum context length") || strings.Contains(v.Content, terminalPartialPrivateSentinel) {
					t.Fatalf("system notice leaked raw terminal/private detail: %q", v.Content)
				}
			}
			if v.Source == "assistant" && strings.Contains(v.Content, "Preserved draft (incomplete):") && strings.Contains(v.Content, wantDraft) {
				renderedDraft++
				if strings.Contains(v.Content, terminalPartialPrivateSentinel) {
					t.Fatalf("preserved draft leaked private reasoning: %q", v.Content)
				}
				continue
			}
			if v.Source == "assistant" && strings.TrimSpace(v.Content) == wantDraft {
				acceptedAssistant++
			}
		case ReplaceLastMessageMsg:
			if strings.Contains(v.Content, "could not accept this streamed draft as final") {
				cleanupReplacement++
				if strings.Contains(v.Content, wantDraft) || strings.Contains(v.Content, terminalPartialPrivateSentinel) {
					t.Fatalf("cleanup replacement leaked draft/private detail: %q", v.Content)
				}
			}
		case TokensMsg:
			if v.Tokens == wantTokens {
				tokenUpdates++
			}
		}
	}
	if renderedNotice != 1 || renderedDraft != 1 {
		t.Fatalf("rendered notice/draft = %d/%d, want 1/1", renderedNotice, renderedDraft)
	}
	if acceptedAssistant != 0 {
		t.Fatalf("rendered accepted assistant success %d time(s), want none", acceptedAssistant)
	}
	if cleanupReplacement != 1 {
		t.Fatalf("cleanup replacements = %d, want 1 streamed draft rejection", cleanupReplacement)
	}
	if tokenUpdates != 1 {
		t.Fatalf("token updates for retained usage = %d, want 1", tokenUpdates)
	}
	var persistedDrafts int
	for _, msg := range sess.Conversation.Messages {
		text := conversation.GetContentAsString(msg.Content)
		if strings.Contains(text, terminalPartialPrivateSentinel) {
			t.Fatalf("persisted message leaked private reasoning: role=%s text=%q", msg.Role, text)
		}
		if msg.Role == "assistant" && msg.IsTruncated && strings.Contains(text, "Preserved draft (incomplete):") && strings.Contains(text, wantDraft) {
			persistedDrafts++
		}
	}
	if persistedDrafts != 1 {
		t.Fatalf("persisted incomplete drafts = %d, want 1", persistedDrafts)
	}
}

func assertTerminalPartialIdentityTelemetry(t *testing.T, events <-chan telemetry.Event, wantResponseID string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case event := <-events:
			if event.Type != telemetry.EventAgentModelResponse && event.Type != telemetry.EventAgentModelError {
				continue
			}
			if got, _ := event.Data["response_id"].(string); got == wantResponseID {
				if event.Data["response_model"] != "gpt-4o" {
					t.Fatalf("response_model = %v, want gpt-4o in event %+v", event.Data["response_model"], event)
				}
				return
			}
		case <-deadline:
			t.Fatalf("missing model response telemetry identity response_id=%q", wantResponseID)
		}
	}
}

func waitForThinkingContaining(t *testing.T, app *WidgetApp, text string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case msg := <-app.messages:
			if add, ok := msg.(AddMessageMsg); ok && add.Source == "thinking" && strings.Contains(add.Content, text) {
				return
			}
		case <-deadline:
			t.Fatalf("missing thinking message containing %q", text)
		}
	}
}

func TestStreamResponseRetainsTerminalProviderPartial(t *testing.T) {
	var requests int32
	const draft = "public partial draft"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := atomic.AddInt32(&requests, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		writeTerminalPartialSSE(t, w, `{"id":"partial-provider","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"`+draft+`","reasoning":"`+terminalPartialPrivateSentinel+`"},"finish_reason":null}]}`)
		writeTerminalPartialSSE(t, w, `{"id":"partial-provider","model":"gpt-4o","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":7,"total_tokens":17}}`)
		writeTerminalPartialSSE(t, w, `{"error":{"message":"maximum context length exceeded after partial","code":"context_length_exceeded","type":"invalid_request_error"}}`)
		if request > 1 {
			t.Errorf("unexpected rebuy request %d", request)
		}
	}))
	defer server.Close()

	hub := telemetry.NewHub()
	events, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	ctrl, sess, app := newTerminalPartialController(t, server.URL, hub)

	ctrl.streamResponse(context.Background(), "produce a partial", sess)

	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Fatalf("provider requests = %d, want 1 no-rebuy terminal partial", got)
	}
	assertTerminalPartialRenderedAndPersisted(t, app, sess, draft, 17)
	assertTerminalPartialIdentityTelemetry(t, events, "partial-provider")
}

func TestStreamResponseRetainsCancelAfterObservedPartial(t *testing.T) {
	var requests int32
	observed := make(chan struct{})
	const draft = "public cancel draft"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		writeTerminalPartialSSE(t, w, `{"id":"partial-cancel","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"`+draft+`","reasoning_details":[{"type":"reasoning.text","text":"`+terminalPartialPrivateSentinel+`"}]},"finish_reason":null}],"usage":{"prompt_tokens":8,"completion_tokens":5,"total_tokens":13}}`)
		close(observed)
		<-r.Context().Done()
	}))
	defer server.Close()

	hub := telemetry.NewHub()
	events, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	ctrl, sess, app := newTerminalPartialController(t, server.URL, hub)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ctrl.streamResponse(ctx, "cancel after partial", sess)
	}()

	select {
	case <-observed:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not observe partial request")
	}
	waitForThinkingContaining(t, app, terminalPartialPrivateSentinel)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("streamResponse did not return after cancellation")
	}

	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Fatalf("provider requests = %d, want 1", got)
	}
	assertTerminalPartialRenderedAndPersisted(t, app, sess, draft, 13)
	assertTerminalPartialIdentityTelemetry(t, events, "partial-cancel")
}

func TestStreamResponsePreCancelledDoesNotInventIncompleteArtifact(t *testing.T) {
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	ctrl, sess, app := newTerminalPartialController(t, server.URL, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ctrl.streamResponseWithIntent(ctx, "already cancelled", sess, agentloop.ReadOnlyIntent)

	if got := atomic.LoadInt32(&requests); got != 0 {
		t.Fatalf("provider requests = %d, want none for pre-cancel", got)
	}
	if conversationHasSystemContaining(sess.Conversation.Messages, "Incomplete result:") {
		t.Fatalf("pre-cancel invented incomplete notice: %+v", roleList(sess.Conversation.Messages))
	}
	for _, msg := range sess.Conversation.Messages {
		if msg.Role == "assistant" || msg.Role == "system" {
			t.Fatalf("pre-cancel invented artifact message: %+v", msg)
		}
	}
	for _, msg := range drainAllMessages(app) {
		if add, ok := msg.(AddMessageMsg); ok && strings.Contains(add.Content, "Preserved draft (incomplete):") {
			t.Fatalf("pre-cancel rendered preserved draft: %q", add.Content)
		}
	}
}
