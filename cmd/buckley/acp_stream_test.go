package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"m31labs.dev/buckley/v2/pkg/acp"
	"m31labs.dev/buckley/v2/pkg/config"
	"m31labs.dev/buckley/v2/pkg/conversation"
	"m31labs.dev/buckley/v2/pkg/model"
	"m31labs.dev/buckley/v2/pkg/tool"
)

// acpSSEChunk writes one "data: {...}\n\n" line to w, matching the
// OpenAI-compatible SSE format model.ParseSSEStream expects.
func acpSSEChunk(t *testing.T, w io.Writer, content, reasoning, finishReason string) {
	t.Helper()
	var b strings.Builder
	b.WriteString(`{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{`)
	wrote := false
	if content != "" {
		b.WriteString(`"content":"` + content + `"`)
		wrote = true
	}
	if reasoning != "" {
		if wrote {
			b.WriteString(",")
		}
		b.WriteString(`"reasoning":"` + reasoning + `"`)
		wrote = true
	}
	b.WriteString(`}`)
	if finishReason != "" {
		b.WriteString(`,"finish_reason":"` + finishReason + `"`)
	} else {
		b.WriteString(`,"finish_reason":null`)
	}
	b.WriteString(`}]}`)
	_, _ = io.WriteString(w, "data: "+b.String()+"\n\n")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// newACPStreamingTestManager starts an httptest SSE server that streams
// chunks []string as separate agent_message_chunk-worthy deltas, then
// returns a *model.Manager routed to it plus the exact model ID to request.
func newACPStreamingTestManager(t *testing.T, chunks []string, reasoningChunks []string) (*model.Manager, string) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, rc := range reasoningChunks {
			acpSSEChunk(t, w, "", rc, "")
		}
		for i, c := range chunks {
			finish := ""
			if i == len(chunks)-1 {
				finish = "stop"
			}
			acpSSEChunk(t, w, c, "", finish)
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	t.Cleanup(server.Close)

	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai"

	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return mgr, "gpt-4o"
}

// collectingStream records every session/update SessionUpdate a StreamFunc
// receives, in order, so tests can inspect exactly what was sent to the
// (simulated) ACP client.
type collectingStream struct {
	updates []acp.SessionUpdate
}

func (c *collectingStream) fn(update acp.SessionUpdate) error {
	c.updates = append(c.updates, update)
	return nil
}

func (c *collectingStream) messageChunks() []string {
	var out []string
	for _, u := range c.updates {
		if u.SessionUpdate != acp.SessionUpdateAgentMessageChunk {
			continue
		}
		if block, ok := u.Content.(acp.ContentBlock); ok {
			out = append(out, block.Text)
		}
	}
	return out
}

func (c *collectingStream) thoughtChunks() []string {
	var out []string
	for _, u := range c.updates {
		if u.SessionUpdate != acp.SessionUpdateAgentThoughtChunk {
			continue
		}
		if block, ok := u.Content.(acp.ContentBlock); ok {
			out = append(out, block.Text)
		}
	}
	return out
}

// TestStreamACPTurn_ForwardsContentPerChunk locks S1: streamACPTurn must
// forward each provider content delta as its own agent_message_chunk while
// the turn is still in flight, not buffer the whole turn into one final
// chunk. The concatenation of every emitted chunk must equal the final
// accumulated message exactly.
func TestStreamACPTurn_ForwardsContentPerChunk(t *testing.T) {
	t.Parallel()

	wantChunks := []string{"Hello", ", ", "world", "!"}
	mgr, modelID := newACPStreamingTestManager(t, wantChunks, nil)

	req := model.ChatRequest{Model: modelID, Messages: []model.Message{{Role: "user", Content: "hi"}}}
	collector := &collectingStream{}

	msg, _, err := streamACPTurn(context.Background(), mgr, req, collector.fn)
	if err != nil {
		t.Fatalf("streamACPTurn: %v", err)
	}

	gotChunks := collector.messageChunks()
	if len(gotChunks) != len(wantChunks) {
		t.Fatalf("agent_message_chunk count = %d, want %d (chunks=%#v)", len(gotChunks), len(wantChunks), gotChunks)
	}
	for i, want := range wantChunks {
		if gotChunks[i] != want {
			t.Fatalf("chunk[%d] = %q, want %q", i, gotChunks[i], want)
		}
	}

	wantFinal := strings.Join(wantChunks, "")
	gotFinal := strings.Join(gotChunks, "")
	if gotFinal != wantFinal {
		t.Fatalf("concatenated chunks = %q, want %q", gotFinal, wantFinal)
	}

	content, ok := msg.Content.(string)
	if !ok {
		t.Fatalf("msg.Content type = %T, want string", msg.Content)
	}
	if content != wantFinal {
		t.Fatalf("final accumulated message = %q, want %q (must equal concatenated chunks)", content, wantFinal)
	}
}

// TestStreamACPTurn_ForwardsReasoningPerChunk locks the reasoning half of
// S1: incremental reasoning deltas stream as agent_thought_chunk while the
// turn is in flight, not as a single chunk at the end.
func TestStreamACPTurn_ForwardsReasoningPerChunk(t *testing.T) {
	t.Parallel()

	wantReasoning := []string{"Let me ", "think ", "about this."}
	mgr, modelID := newACPStreamingTestManager(t, []string{"done"}, wantReasoning)

	req := model.ChatRequest{Model: modelID, Messages: []model.Message{{Role: "user", Content: "hi"}}}
	collector := &collectingStream{}

	msg, _, err := streamACPTurn(context.Background(), mgr, req, collector.fn)
	if err != nil {
		t.Fatalf("streamACPTurn: %v", err)
	}

	gotThoughts := collector.thoughtChunks()
	if len(gotThoughts) != len(wantReasoning) {
		t.Fatalf("agent_thought_chunk count = %d, want %d (chunks=%#v)", len(gotThoughts), len(wantReasoning), gotThoughts)
	}
	for i, want := range wantReasoning {
		if gotThoughts[i] != want {
			t.Fatalf("thought[%d] = %q, want %q", i, gotThoughts[i], want)
		}
	}

	if msg.Reasoning != strings.Join(wantReasoning, "") {
		t.Fatalf("accumulated reasoning = %q, want %q", msg.Reasoning, strings.Join(wantReasoning, ""))
	}
}

// TestRunACPLoop_StreamsPerTokenWithNoDuplicateFinalChunk is the
// acceptance test for S1 end to end: running a full prompt turn through
// runACPLoop must emit the response as multiple agent_message_chunk
// notifications (not one at turn end), and their concatenation must equal
// the turn's final message exactly -- with no extra chunk repeating the
// whole text again.
func TestRunACPLoop_StreamsPerTokenWithNoDuplicateFinalChunk(t *testing.T) {
	t.Parallel()

	wantChunks := []string{"The answer ", "is ", "42."}
	// Deliberately not a model the OpenAI provider's static catalog
	// recognizes: this keeps SupportsTools false so the turn takes the
	// plain-text path deterministically, independent of the tool-turn
	// machinery this test does not exercise.
	mgr, _ := newACPStreamingTestManager(t, wantChunks, nil)
	const modelID = "acp-test/no-tools-model"

	cfg := config.DefaultConfig()
	conv := conversation.New("session-1")
	conv.AddUserMessage("what is the answer?")
	registry := tool.NewEmptyRegistry()
	collector := &collectingStream{}

	text, err := runACPLoop(context.Background(), cfg, mgr, conv, registry, nil, nil, modelID, "", "session-1", nil, func(string, ...interface{}) {}, collector.fn)
	if err != nil {
		t.Fatalf("runACPLoop: %v", err)
	}

	wantFinal := strings.Join(wantChunks, "")
	if text != wantFinal {
		t.Fatalf("runACPLoop text = %q, want %q", text, wantFinal)
	}

	gotChunks := collector.messageChunks()
	if len(gotChunks) < 2 {
		t.Fatalf("agent_message_chunk count = %d, want >= 2 (per-token streaming, not one chunk at turn end); chunks=%#v", len(gotChunks), gotChunks)
	}
	gotFinal := strings.Join(gotChunks, "")
	if gotFinal != wantFinal {
		t.Fatalf("concatenated agent_message_chunk content = %q, want %q (must equal final message with no duplicate trailing chunk)", gotFinal, wantFinal)
	}
}
