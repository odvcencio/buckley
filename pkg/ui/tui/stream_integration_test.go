package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/skill"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
	"m31labs.dev/buckley/pkg/ui/backend/sim"
)

// fakeStreamProvider is a model.Provider that returns a canned response per call
// as a single stream chunk, so the interactive tool loop can be driven end to
// end without a network or a terminal.
type fakeStreamProvider struct {
	id           string
	catalog      model.ModelCatalog
	responses    []model.Message
	calls        int
	errorOnTools bool // when set, a request carrying tools fails as "unsupported"
}

func (p *fakeStreamProvider) ID() string { return p.id }

func (p *fakeStreamProvider) FetchCatalog() (*model.ModelCatalog, error) { return &p.catalog, nil }

func (p *fakeStreamProvider) GetModelInfo(id string) (*model.ModelInfo, error) {
	for i := range p.catalog.Data {
		if p.catalog.Data[i].ID == id {
			info := p.catalog.Data[i]
			return &info, nil
		}
	}
	return nil, fmt.Errorf("model not found: %s", id)
}

func (p *fakeStreamProvider) next() model.Message {
	i := p.calls
	if i >= len(p.responses) {
		i = len(p.responses) - 1
	}
	p.calls++
	return p.responses[i]
}

func (p *fakeStreamProvider) ChatCompletion(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	return &model.ChatResponse{Choices: []model.Choice{{Message: p.next()}}}, nil
}

func (p *fakeStreamProvider) ChatCompletionStream(ctx context.Context, req model.ChatRequest) (<-chan model.StreamChunk, <-chan error) {
	chunkChan := make(chan model.StreamChunk, 2)
	errChan := make(chan error, 1)
	if p.errorOnTools && len(req.Tools) > 0 {
		go func() {
			defer close(chunkChan)
			defer close(errChan)
			errChan <- fmt.Errorf("model does not support tool calling")
		}()
		return chunkChan, errChan
	}
	msg := p.next()
	go func() {
		defer close(chunkChan)
		defer close(errChan)
		chunk := model.StreamChunk{
			ID: "chunk",
			Choices: []model.StreamChoice{{
				Index: 0,
				Delta: model.MessageDelta{Role: "assistant", Content: model.ExtractTextContentOrEmpty(msg.Content)},
			}},
		}
		for i, tc := range msg.ToolCalls {
			chunk.Choices[0].Delta.ToolCalls = append(chunk.Choices[0].Delta.ToolCalls, model.ToolCallDelta{
				Index:    i,
				ID:       tc.ID,
				Type:     tc.Type,
				Function: &model.FunctionCallDelta{Name: tc.Function.Name, Arguments: tc.Function.Arguments},
			})
		}
		chunkChan <- chunk
	}()
	return chunkChan, errChan
}

// echoTool is a trivial read-only tool the fake model can call.
type echoTool struct{}

func (echoTool) Name() string        { return "echo" }
func (echoTool) Description() string  { return "echoes" }
func (echoTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{Type: "object"}
}
func (echoTool) Execute(map[string]any) (*builtin.Result, error) {
	return &builtin.Result{Success: true, Data: map[string]any{"echoed": true}}, nil
}
func (echoTool) Metadata() tool.ToolMetadata {
	return tool.ToolMetadata{Impact: tool.ImpactReadOnly, Category: tool.CategoryFilesystem}
}

// TestInteractiveToolLoopStreamsAndPersists drives a full interactive turn
// (preamble + tool call, then final answer) through the converged streaming loop
// and asserts BOTH the persisted conversation shape and the render calls — the
// headless verification of the interactive experience, no TTY required.
func TestInteractiveToolLoopStreamsAndPersists(t *testing.T) {
	const modelID = "fake/model-x"

	provider := &fakeStreamProvider{
		id: "fake",
		catalog: model.ModelCatalog{Data: []model.ModelInfo{{
			ID:                  modelID,
			SupportedParameters: []string{"tools"},
		}}},
		responses: []model.Message{
			{
				Role:      "assistant",
				Content:   "Let me check that.",
				ToolCalls: []model.ToolCall{{ID: "c1", Type: "function", Function: model.FunctionCall{Name: "echo", Arguments: "{}"}}},
			},
			{Role: "assistant", Content: "Done: 42 files."},
		},
	}

	cfg := config.DefaultConfig()
	cfg.Models.Execution = modelID
	cfg.Models.DefaultProvider = "fake"
	cfg.Models.Reasoning = "off"

	mgr, err := model.NewManagerWithProviders(cfg, map[string]model.Provider{"fake": provider})
	if err != nil {
		t.Fatalf("NewManagerWithProviders: %v", err)
	}
	if err := mgr.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if !mgr.SupportsTools(modelID) {
		t.Fatalf("precondition: fake model should support tools")
	}

	app, err := NewWidgetApp(WidgetAppConfig{Backend: sim.New(80, 24)})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}

	registry := tool.NewEmptyRegistry()
	registry.Register(echoTool{})
	conv := conversation.New("s1")
	sess := &SessionState{
		ID:           "s1",
		Conversation: conv,
		ToolRegistry: registry,
		SkillState:   skill.NewRuntimeState(conv.AddSystemMessage),
	}
	conv.AddUserMessage("How many files?")

	c := &Controller{cfg: cfg, modelMgr: mgr, app: app, workDir: t.TempDir()}

	text, _, _, err := c.runToolLoop(context.Background(), sess, modelID)
	if err != nil {
		t.Fatalf("runToolLoop error: %v", err)
	}
	if text != "Done: 42 files." {
		t.Fatalf("final text = %q, want %q", text, "Done: 42 files.")
	}

	// Persisted conversation: user, assistant(preamble+tool_call), tool, assistant(final).
	msgs := conv.Messages
	if len(msgs) != 4 {
		t.Fatalf("expected 4 persisted messages, got %d: %+v", len(msgs), roles(msgs))
	}
	assistant := msgs[1]
	if assistant.Role != "assistant" || conversation.GetContentAsString(assistant.Content) != "Let me check that." {
		t.Fatalf("preamble content not persisted: %+v", assistant)
	}
	if len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].Function.Name != "echo" {
		t.Fatalf("tool call not persisted alongside content: %+v", assistant.ToolCalls)
	}
	if msgs[2].Role != "tool" || msgs[2].Name != "echo" {
		t.Fatalf("tool result not persisted: %+v", msgs[2])
	}
	if msgs[3].Role != "assistant" || conversation.GetContentAsString(msgs[3].Content) != "Done: 42 files." {
		t.Fatalf("final answer not persisted: %+v", msgs[3])
	}

	// Render calls: the preamble and the final answer must both be STREAMED
	// (via StreamChunk into seeded assistant bubbles), and the final answer must
	// NOT also be posted as a discrete message (no double-render).
	seeds, streamed, discreteFinal := 0, "", 0
	for _, m := range drain(app) {
		switch v := m.(type) {
		case AddMessageMsg:
			if v.Source == "assistant" && v.Content == "" {
				seeds++
			}
			if v.Source == "assistant" && strings.Contains(v.Content, "Done: 42") {
				discreteFinal++
			}
		case StreamChunk:
			streamed += v.Text
		}
	}
	if seeds < 2 {
		t.Fatalf("expected >=2 seeded assistant bubbles (preamble + final), got %d", seeds)
	}
	if !strings.Contains(streamed, "Let me check that.") || !strings.Contains(streamed, "Done: 42 files.") {
		t.Fatalf("expected both preamble and final answer streamed, got %q", streamed)
	}
	if discreteFinal != 0 {
		t.Fatalf("final answer was double-rendered as a discrete message %d time(s)", discreteFinal)
	}
}

// newLoopController wires a Controller + session backed by the fake provider.
func newLoopController(t *testing.T, provider *fakeStreamProvider) (*Controller, *SessionState) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Models.Execution = "fake/model-x"
	cfg.Models.DefaultProvider = "fake"
	cfg.Models.Reasoning = "off"

	mgr, err := model.NewManagerWithProviders(cfg, map[string]model.Provider{"fake": provider})
	if err != nil {
		t.Fatalf("NewManagerWithProviders: %v", err)
	}
	if err := mgr.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	app, err := NewWidgetApp(WidgetAppConfig{Backend: sim.New(80, 24)})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}
	registry := tool.NewEmptyRegistry()
	registry.Register(echoTool{})
	conv := conversation.New("s1")
	sess := &SessionState{
		ID:           "s1",
		Conversation: conv,
		ToolRegistry: registry,
		SkillState:   skill.NewRuntimeState(conv.AddSystemMessage),
	}
	return &Controller{cfg: cfg, modelMgr: mgr, app: app, workDir: t.TempDir()}, sess
}

func fakeToolModelCatalog() model.ModelCatalog {
	return model.ModelCatalog{Data: []model.ModelInfo{{ID: "fake/model-x", SupportedParameters: []string{"tools"}}}}
}

// TestInteractiveToolLoopRetriesWithoutTools verifies the reactive fallback: when
// the provider rejects tools, the loop retries with tools off and still answers.
func TestInteractiveToolLoopRetriesWithoutTools(t *testing.T) {
	provider := &fakeStreamProvider{
		id:           "fake",
		catalog:      fakeToolModelCatalog(),
		errorOnTools: true,
		responses:    []model.Message{{Role: "assistant", Content: "No tools, but the answer is 42."}},
	}
	c, sess := newLoopController(t, provider)
	sess.Conversation.AddUserMessage("answer please")

	text, _, _, err := c.runToolLoop(context.Background(), sess, "fake/model-x")
	if err != nil {
		t.Fatalf("runToolLoop error: %v", err)
	}
	if text != "No tools, but the answer is 42." {
		t.Fatalf("final text = %q", text)
	}
	last := sess.Conversation.Messages[len(sess.Conversation.Messages)-1]
	if last.Role != "assistant" || conversation.GetContentAsString(last.Content) != "No tools, but the answer is 42." {
		t.Fatalf("final answer not persisted after tools-off retry: %+v", last)
	}
}

// TestInteractiveToolLoopCheckpointsOnMaxIterations verifies the loop stops with
// the interactive checkpoint (not a hard failure) when the model never finishes.
func TestInteractiveToolLoopCheckpointsOnMaxIterations(t *testing.T) {
	provider := &fakeStreamProvider{
		id:      "fake",
		catalog: fakeToolModelCatalog(),
		responses: []model.Message{{
			Role:      "assistant",
			ToolCalls: []model.ToolCall{{ID: "c1", Type: "function", Function: model.FunctionCall{Name: "echo", Arguments: "{}"}}},
		}},
	}
	c, sess := newLoopController(t, provider)
	sess.Conversation.AddUserMessage("keep going forever")

	text, _, finishReason, err := c.runToolLoop(context.Background(), sess, "fake/model-x")
	if err != nil {
		t.Fatalf("runToolLoop error: %v", err)
	}
	if finishReason != toolLoopCheckpointFinishReason {
		t.Fatalf("finishReason = %q, want %q", finishReason, toolLoopCheckpointFinishReason)
	}
	if !strings.Contains(strings.ToLower(text), "checkpoint") {
		t.Fatalf("expected checkpoint text, got %q", text)
	}
	if !sess.AwaitingToolLoopDecision {
		t.Fatalf("expected session to await a direction decision after checkpoint")
	}
}

// TestInteractiveToolLoopParsesInlineToolTokens verifies the streaming TUI loop
// recovers a tool call a model embedded as inline tokens in content (the class of
// bug that otherwise makes a model "answer" with markup instead of acting).
func TestInteractiveToolLoopParsesInlineToolTokens(t *testing.T) {
	provider := &fakeStreamProvider{
		id:      "fake",
		catalog: fakeToolModelCatalog(),
		responses: []model.Message{
			{Role: "assistant", Content: "Checking.\n<|tool_calls_section_begin|><|tool_call_begin|>functions.echo:0<|tool_call_argument_begin|>{}<|tool_call_end|><|tool_calls_section_end|>"},
			{Role: "assistant", Content: "Inline handled."},
		},
	}
	c, sess := newLoopController(t, provider)
	sess.Conversation.AddUserMessage("do it")

	text, _, _, err := c.runToolLoop(context.Background(), sess, "fake/model-x")
	if err != nil {
		t.Fatalf("runToolLoop error: %v", err)
	}
	if text != "Inline handled." {
		t.Fatalf("final text = %q", text)
	}
	var toolMsgs, assistantWithCall int
	for _, m := range sess.Conversation.Messages {
		if m.Role == "tool" && m.Name == "echo" {
			toolMsgs++
		}
		if m.Role == "assistant" && len(m.ToolCalls) == 1 && m.ToolCalls[0].Function.Name == "echo" {
			assistantWithCall++
			if s := conversation.GetContentAsString(m.Content); strings.Contains(s, "tool_call") {
				t.Fatalf("inline markup left in persisted content: %q", s)
			}
		}
	}
	if toolMsgs != 1 || assistantWithCall != 1 {
		t.Fatalf("inline tool token not parsed+executed: toolMsgs=%d assistantWithCall=%d", toolMsgs, assistantWithCall)
	}
}

// TestInteractiveToolLoopHandlesMultipleToolCalls verifies a single assistant
// turn that requests several tools executes all of them before continuing.
func TestInteractiveToolLoopHandlesMultipleToolCalls(t *testing.T) {
	provider := &fakeStreamProvider{
		id:      "fake",
		catalog: fakeToolModelCatalog(),
		responses: []model.Message{
			{Role: "assistant", ToolCalls: []model.ToolCall{
				{ID: "c1", Type: "function", Function: model.FunctionCall{Name: "echo", Arguments: "{}"}},
				{ID: "c2", Type: "function", Function: model.FunctionCall{Name: "echo", Arguments: "{}"}},
			}},
			{Role: "assistant", Content: "Both ran."},
		},
	}
	c, sess := newLoopController(t, provider)
	sess.Conversation.AddUserMessage("run two")

	text, _, _, err := c.runToolLoop(context.Background(), sess, "fake/model-x")
	if err != nil {
		t.Fatalf("runToolLoop error: %v", err)
	}
	if text != "Both ran." {
		t.Fatalf("final text = %q", text)
	}
	toolMsgs := 0
	for _, m := range sess.Conversation.Messages {
		if m.Role == "tool" {
			toolMsgs++
		}
	}
	if toolMsgs != 2 {
		t.Fatalf("expected 2 tool result messages, got %d", toolMsgs)
	}
}

func roles(msgs []conversation.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Role
	}
	return out
}

func drain(app *WidgetApp) []Message {
	var out []Message
	for {
		select {
		case m := <-app.messages:
			out = append(out, m)
		default:
			return out
		}
	}
}
