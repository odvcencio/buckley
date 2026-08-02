package tui

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"m31labs.dev/buckley/v2/pkg/config"
	"m31labs.dev/buckley/v2/pkg/conversation"
	"m31labs.dev/buckley/v2/pkg/model"
	"m31labs.dev/fluffyui/backend/sim"
)

// openAIChatStreamTestServer serves a multi-chunk SSE chat/completions
// response so runToolLoop is driven through the real streaming client
// (pkg/model's OpenAI provider, not a fake channel), proving live token
// streaming end to end: each element of chunks becomes its own SSE "data:"
// frame, flushed separately.
func openAIChatStreamTestServer(t *testing.T, chunks []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		write := func(payload string) {
			_, _ = io.WriteString(w, "data: "+payload+"\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
		write(`{"id":"resp-1","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`)
		for _, chunk := range chunks {
			write(`{"id":"resp-1","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"` + chunk + `"},"finish_reason":null}]}`)
		}
		write(`{"id":"resp-1","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		write(`[DONE]`)
	}))
}

func newStreamIntegrationConfig(baseURL string) *config.Config {
	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = baseURL
	cfg.Models.DefaultProvider = "openai"
	return cfg
}

// TestRunToolLoopStreamsAssistantTextLiveWithoutDoubleRender drives one
// interactive turn through a real multi-chunk SSE response and asserts: the
// transcript receives the text incrementally via StreamChunk (not one
// discrete AddMessage at the end), the streamed text concatenates to
// exactly the persisted final content, and the final answer is never also
// posted as a second, discrete AddMessage (no double-render).
func TestRunToolLoopStreamsAssistantTextLiveWithoutDoubleRender(t *testing.T) {
	server := openAIChatStreamTestServer(t, []string{"Hello", ", ", "world", "!"})
	defer server.Close()

	cfg := newStreamIntegrationConfig(server.URL)
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	app, err := NewWidgetApp(WidgetAppConfig{Backend: sim.New(80, 24)})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}

	conv := conversation.New("session-1")
	conv.AddUserMessage("say hello")
	sess := &SessionState{ID: "session-1", Conversation: conv}

	ctrl := &Controller{app: app, cfg: cfg, modelMgr: mgr, workDir: t.TempDir()}

	text, _, finishReason, streamed, err := ctrl.runToolLoop(context.Background(), sess, "gpt-4o")
	if err != nil {
		t.Fatalf("runToolLoop error: %v", err)
	}
	if finishReason != "stop" {
		t.Fatalf("finishReason = %q, want stop", finishReason)
	}
	if !streamed {
		t.Fatal("expected runToolLoop to report the final answer as already streamed")
	}
	if text != "Hello, world!" {
		t.Fatalf("final text = %q, want %q", text, "Hello, world!")
	}

	// Mirror the production call path (controller_stream.go's
	// streamResponse): renderStreamResponse runs after runToolLoop and is
	// exactly what must skip re-adding the answer when streamed is true.
	ctrl.renderStreamResponse(text, finishReason, streamed)

	// Persisted history matches the streamed answer exactly.
	last := conv.Messages[len(conv.Messages)-1]
	if last.Role != "assistant" || conversation.GetContentAsString(last.Content) != text {
		t.Fatalf("persisted assistant message = %+v, want content %q", last, text)
	}

	seeds := 0
	discreteFinal := 0
	var streamedText strings.Builder
	sawStreamDone := false
	for _, m := range drainAllMessages(app) {
		switch v := m.(type) {
		case AddMessageMsg:
			if v.Source == "assistant" && v.Content == "" {
				seeds++
			}
			if v.Source == "assistant" && v.Content != "" {
				discreteFinal++
			}
		case StreamChunk:
			if v.SessionID != sess.ID {
				t.Fatalf("StreamChunk session id = %q, want %q", v.SessionID, sess.ID)
			}
			streamedText.WriteString(v.Text)
		case StreamDone:
			sawStreamDone = true
		}
	}

	if seeds != 1 {
		t.Fatalf("expected exactly one seeded empty assistant bubble, got %d", seeds)
	}
	if discreteFinal != 0 {
		t.Fatalf("final answer was double-rendered as a discrete AddMessage %d time(s)", discreteFinal)
	}
	if !sawStreamDone {
		t.Fatal("expected a StreamDone message to close the streamed bubble")
	}
	if streamedText.String() != text {
		t.Fatalf("streamed text = %q, want it to equal the final persisted content %q exactly", streamedText.String(), text)
	}
}

// TestRunToolLoopContinuationDoesNotStream proves the continuation
// (non-streaming ChatCompletionWithContinuation) path keeps today's
// behavior: the final answer is rendered as a single discrete AddMessage,
// never through StreamChunk, since callToolLoopTurn calls the coordinator
// directly instead of the streaming client for an eligible turn.
func TestRunToolLoopContinuationDoesNotStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"resp_1",
			"model":"gpt-5.4",
			"status":"completed",
			"output":[{
				"id":"msg_1",
				"type":"message",
				"role":"assistant",
				"status":"completed",
				"content":[{"type":"output_text","text":"non-streamed answer"}]
			}]
		}`)
	}))
	defer server.Close()

	cfg := newStreamIntegrationConfig(server.URL)
	cfg.Models.ProviderContinuation = true
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	app, err := NewWidgetApp(WidgetAppConfig{Backend: sim.New(80, 24)})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}

	conv := conversation.New("session-1")
	conv.AddUserMessage("hello")
	sess := &SessionState{ID: "session-1", Conversation: conv}

	ctrl := &Controller{app: app, cfg: cfg, modelMgr: mgr, workDir: t.TempDir()}

	text, _, finishReason, streamed, err := ctrl.runToolLoop(context.Background(), sess, "gpt-5.4")
	if err != nil {
		t.Fatalf("runToolLoop error: %v", err)
	}
	if streamed {
		t.Fatal("continuation turns must not report streamed=true")
	}
	if text != "non-streamed answer" {
		t.Fatalf("final text = %q", text)
	}

	ctrl.renderStreamResponse(text, finishReason, streamed)

	discreteFinal := 0
	sawStreamChunk := false
	for _, m := range drainAllMessages(app) {
		switch v := m.(type) {
		case AddMessageMsg:
			if v.Source == "assistant" && v.Content == text {
				discreteFinal++
			}
		case StreamChunk:
			sawStreamChunk = true
		}
	}
	if discreteFinal != 1 {
		t.Fatalf("expected exactly one discrete AddMessage for the non-streamed continuation answer, got %d", discreteFinal)
	}
	if sawStreamChunk {
		t.Fatal("continuation path must not emit StreamChunk")
	}
}
