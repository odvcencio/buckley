package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/acp"
	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tool"
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

func acpSSEChunkReasoningContent(t *testing.T, w io.Writer, reasoningContent, content, finishReason string) {
	t.Helper()
	var b strings.Builder
	b.WriteString(`{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{`)
	wrote := false
	if reasoningContent != "" {
		b.WriteString(`"reasoning_content":"` + reasoningContent + `"`)
		wrote = true
	}
	if content != "" {
		if wrote {
			b.WriteString(",")
		}
		b.WriteString(`"content":"` + content + `"`)
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

func acpSSEJSON(t *testing.T, w io.Writer, payload any) {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal SSE payload: %v", err)
	}
	_, _ = io.WriteString(w, "data: "+string(data)+"\n\n")
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

func TestStreamACPTurn_ForwardsReasoningContentAliasAndFinalOnce(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		acpSSEChunkReasoningContent(t, w, "alias ", "", "")
		acpSSEChunkReasoningContent(t, w, "reasoning", "", "")
		acpSSEChunkReasoningContent(t, w, "", "done", "stop")
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
	collector := &collectingStream{}

	msg, _, err := streamACPTurn(context.Background(), mgr, model.ChatRequest{
		Model: "gpt-4o",
		Messages: []model.Message{{
			Role:    "user",
			Content: "hi",
		}},
	}, collector.fn)
	if err != nil {
		t.Fatalf("streamACPTurn: %v", err)
	}

	if got := strings.Join(collector.thoughtChunks(), ""); got != "alias reasoning" {
		t.Fatalf("thought chunks = %q, want reasoning_content alias stream", got)
	}
	if got := strings.Join(collector.messageChunks(), ""); got != "done" {
		t.Fatalf("message chunks = %q, want final content exactly once", got)
	}
	if got := model.ExtractTextContentOrEmpty(msg.Content); got != "done" {
		t.Fatalf("final message = %q, want done", got)
	}
	if msg.Reasoning != "alias reasoning" {
		t.Fatalf("final reasoning = %q, want alias reasoning", msg.Reasoning)
	}
}

func TestStreamACPTurn_PreservesReasoningContentForCompatibleToolTurnContinuation(t *testing.T) {
	rawReasoning := "思\n\n\n考\n\n\n alpha\n\n\n beta\n\n\n γ\n\n\n delta\n\n\n epsilon\n\n\n zeta\n\n\n eta\n\n\n."
	toolArgs := `{"path":"fixtures/未知.txt"}`
	var requestCount int
	var secondRequest map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if requestCount == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			acpSSEJSON(t, w, map[string]any{
				"id":    "chatcmpl-1",
				"model": "glm-5.3-flash",
				"choices": []any{map[string]any{
					"index":         0,
					"delta":         map[string]any{"role": "assistant", "reasoning_content": rawReasoning},
					"finish_reason": nil,
				}},
			})
			acpSSEJSON(t, w, map[string]any{
				"id":    "chatcmpl-1",
				"model": "glm-5.3-flash",
				"choices": []any{map[string]any{
					"index": 0,
					"delta": map[string]any{"tool_calls": []any{map[string]any{
						"index": 0,
						"id":    "call_α",
						"type":  "function",
						"function": map[string]any{
							"name":      "read_fixture",
							"arguments": toolArgs,
						},
					}}},
					"finish_reason": "tool_calls",
				}},
			})
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&secondRequest); err != nil {
			t.Fatalf("decode second request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-2","model":"glm-5.3-flash","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	t.Cleanup(server.Close)

	cfg := config.DefaultConfig()
	cfg.Providers.OpenAICompatible.Enabled = true
	cfg.Providers.OpenAICompatible.APIKey = "test-key"
	cfg.Providers.OpenAICompatible.BaseURL = server.URL
	cfg.Providers.OpenAICompatible.Models = []string{"glm-5.3-flash"}
	cfg.Providers.OpenAICompatible.SupportedParameters = map[string][]string{
		"glm-5.3-flash": {"tools", "reasoning_content"},
	}
	cfg.Models.DefaultProvider = "openai_compatible"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	turn, _, err := streamACPTurn(context.Background(), mgr, model.ChatRequest{
		Model: "openai_compatible/glm-5.3-flash",
		Messages: []model.Message{{
			Role:    "user",
			Content: "read the fixture",
		}},
		Tools: []map[string]any{{"type": "function", "function": map[string]any{
			"name":        "read_fixture",
			"description": "read fixture",
			"parameters":  map[string]any{"type": "object"},
		}}},
	}, nil)
	if err != nil {
		t.Fatalf("streamACPTurn: %v", err)
	}
	if turn.Reasoning != rawReasoning {
		t.Fatalf("ACP turn reasoning changed exact reasoning_content bytes:\n got %q\nwant %q", turn.Reasoning, rawReasoning)
	}
	if len(turn.ToolCalls) != 1 || turn.ToolCalls[0].ID != "call_α" ||
		turn.ToolCalls[0].Function.Name != "read_fixture" ||
		turn.ToolCalls[0].Function.Arguments != toolArgs {
		t.Fatalf("ACP turn tool call did not round-trip: %+v", turn.ToolCalls)
	}

	_, err = mgr.ChatCompletion(context.Background(), model.ChatRequest{
		Model: "openai_compatible/glm-5.3-flash",
		Messages: []model.Message{
			{Role: "user", Content: "read the fixture"},
			turn,
			{Role: "tool", ToolCallID: "call_α", Name: "read_fixture", Content: `{"nonce":"値-123"}`},
		},
	})
	if err != nil {
		t.Fatalf("second ChatCompletion: %v", err)
	}
	messages := secondRequest["messages"].([]any)
	assistant := messages[1].(map[string]any)
	if got := assistant["reasoning_content"]; got != rawReasoning {
		t.Fatalf("second request reasoning_content changed:\n got %#v\nwant %q", got, rawReasoning)
	}
	if _, ok := assistant["reasoning"]; ok {
		t.Fatalf("second request leaked generic reasoning field: %#v", assistant["reasoning"])
	}
	toolCalls := assistant["tool_calls"].([]any)
	call := toolCalls[0].(map[string]any)
	if call["id"] != "call_α" {
		t.Fatalf("second request tool id = %v, want call_α", call["id"])
	}
	fn := call["function"].(map[string]any)
	if fn["name"] != "read_fixture" || fn["arguments"] != toolArgs {
		t.Fatalf("second request tool function = %#v", fn)
	}
}

func TestStreamACPTurn_PreservesPartialContentAfterInterruptedStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		acpSSEChunk(t, w, "partial answer", "private reasoning", "")
		// Deliberately omit [DONE] to simulate a provider connection reset.
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

	msg, _, err := streamACPTurn(context.Background(), mgr, model.ChatRequest{
		Model: "gpt-4o", Messages: []model.Message{{Role: "user", Content: "hi"}},
	}, nil)
	if got := model.ExtractTextContentOrEmpty(msg.Content); got != "partial answer" {
		t.Fatalf("message content = %q, want preserved partial answer", got)
	}
	var partial *partialStreamTurnError
	if !errors.As(err, &partial) {
		t.Fatalf("error = %v, want partialStreamTurnError", err)
	}
	if partial.text != "partial answer" {
		t.Fatalf("partial text = %q, want assistant content only", partial.text)
	}
	if got := model.ExtractTextContentOrEmpty(partial.turn.Message.Content); got != "partial answer" {
		t.Fatalf("partial turn content = %q, want preserved assistant content", got)
	}
	if strings.Contains(partial.text, "private reasoning") {
		t.Fatalf("partial text leaked reasoning: %q", partial.text)
	}
}

func TestDrainACPStreamTurn_DrainsBufferedChunkAfterProviderError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	chunks := make(chan model.StreamChunk)
	errs := make(chan error, 1)
	providerErr := errors.New("provider stream failed")
	errs <- providerErr
	close(errs)

	go func() {
		defer close(chunks)
		time.Sleep(10 * time.Millisecond)
		finish := "stop"
		chunks <- model.StreamChunk{Choices: []model.StreamChoice{{
			Delta:        model.MessageDelta{Content: "late buffered content"},
			FinishReason: &finish,
		}}}
	}()

	collector := &collectingStream{}
	turn, err := drainACPStreamTurn(ctx, model.ChatRequest{Model: "gpt-4o"}, chunks, errs, collector.fn, false)
	var partial *partialStreamTurnError
	if !errors.As(err, &partial) || !errors.Is(err, providerErr) {
		t.Fatalf("error = %v, want partialStreamTurnError wrapping provider error", err)
	}
	if got := model.ExtractTextContentOrEmpty(turn.Message.Content); got != "late buffered content" {
		t.Fatalf("turn content = %q, want drained chunk content", got)
	}
	if partial.text != "late buffered content" {
		t.Fatalf("partial text = %q, want drained chunk content", partial.text)
	}
	if got := strings.Join(collector.messageChunks(), ""); got != "late buffered content" {
		t.Fatalf("streamed chunks = %q, want drained content delivered", got)
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

func TestRunACPLoop_RejectsProviderTruncationFinishReason(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		acpSSEChunk(t, w, "partial answer", "", "length")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
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
	conv := conversation.New("session-truncated")
	conv.AddUserMessage("answer")

	text, err := runACPLoop(
		context.Background(), cfg, mgr, conv, tool.NewEmptyRegistry(), nil, nil,
		"acp-test/no-tools-model", "", "session-truncated", nil,
		func(string, ...interface{}) {}, (&collectingStream{}).fn,
	)
	var incomplete *agentloop.IncompleteTurnError
	if err == nil || !errors.As(err, &incomplete) || !strings.Contains(err.Error(), "truncated at its output limit") {
		t.Fatalf("error = %v, want provider truncation rejected as incomplete", err)
	}
	if text != "partial answer" {
		t.Fatalf("text = %q, want preserved incomplete draft", text)
	}
}
