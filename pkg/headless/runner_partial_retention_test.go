package headless

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/telemetry"
	"m31labs.dev/buckley/pkg/tool"
)

func TestRunner_ManagerProviderPartialErrorRetainsHistoryUsageAndLifecycleIdentity(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		cancelAfter bool
	}{
		{name: "provider error", err: errors.New("provider terminal failure sk-" + strings.Repeat("x", 30))},
		{name: "caller canceled after observed response", err: context.Canceled, cancelAfter: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			provider := &headlessPartialProvider{
				modelID: "fake-model",
				response: headlessPartialResponse(
					"fake-"+strings.ReplaceAll(tt.name, " ", "-"),
					"fake-wire-model",
					"public partial from "+tt.name,
					model.Usage{PromptTokens: 11, CompletionTokens: 13, TotalTokens: 24},
				),
				err: tt.err,
				beforeReturn: func() {
					if tt.cancelAfter {
						cancel()
					}
				},
			}
			hub := telemetry.NewHub()
			defer hub.Close()
			events, unsubscribe := hub.Subscribe()
			defer unsubscribe()
			emitter := &mockEmitter{}
			runner := newHeadlessProviderTestRunner(t, provider, hub, emitter)

			err := runner.runConversationLoopForCommand(ctx, nil, "")
			var incomplete *agentloop.IncompleteTurnError
			if !errors.As(err, &incomplete) || incomplete.FinishReason != agentloop.FinishReasonModelError {
				t.Fatalf("runConversationLoopForCommand error = %v, want model-error incomplete", err)
			}
			if !errors.Is(err, tt.err) {
				t.Fatalf("runConversationLoopForCommand error = %v, want raw cause %v", err, tt.err)
			}
			if provider.calls != 1 {
				t.Fatalf("provider calls = %d, want no replay after observed partial", provider.calls)
			}
			usage := runner.Usage()
			if usage.PromptTokens != 11 || usage.CompletionTokens != 13 || usage.TotalTokens != 24 {
				t.Fatalf("usage = %+v, want partial response usage exactly once", usage)
			}

			assertOneIncompleteWarning(t, emitter.events, tt.err.Error())
			assertIncompleteHistory(t, runner, "public partial from "+tt.name, tt.err.Error())
			assertObservedLifecycleIdentity(t, events, provider.response.ID, provider.response.Model)
		})
	}
}

func TestRunner_HTTPPartialPersistsHistoryUsageAndLifecycleIdentity(t *testing.T) {
	root := t.TempDir()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-partial-1",
			"model":"gpt-4o-wire",
			"choices":[{"index":0,"message":{"role":"assistant","content":"public truncated draft","reasoning":"PRIVATE_HEADLESS_REASONING"},"finish_reason":"length"}],
			"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}
		}`)
	}))
	defer server.Close()

	hub := telemetry.NewHub()
	defer hub.Close()
	events, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	emitter := &mockEmitter{}
	runner := newHeadlessContractTestRunner(t, server.URL, root, tool.NewEmptyRegistry())
	runner.emitter = emitter
	runner.telemetry = hub

	err := runner.runConversationLoopForCommand(context.Background(), nil, "")
	var incomplete *agentloop.IncompleteTurnError
	if !errors.As(err, &incomplete) || incomplete.FinishReason != agentloop.FinishReasonInvalidCompletion {
		t.Fatalf("runConversationLoopForCommand error = %v, want invalid-completion incomplete", err)
	}
	if requests != 1 {
		t.Fatalf("model requests = %d, want no replay after partial terminal response", requests)
	}
	usage := runner.Usage()
	if usage.PromptTokens != 3 || usage.CompletionTokens != 4 || usage.TotalTokens != 7 {
		t.Fatalf("usage = %+v, want partial response usage exactly once", usage)
	}

	var warningEvents int
	for _, event := range emitter.events {
		if event.Type != EventWarning {
			continue
		}
		warningEvents++
		for _, value := range event.Data {
			if text, ok := value.(string); ok && strings.Contains(text, "PRIVATE_HEADLESS_REASONING") {
				t.Fatalf("warning event leaked private reasoning: %+v", event)
			}
		}
	}
	if warningEvents != 1 {
		t.Fatalf("warning events = %d, want one incomplete warning; events=%+v", warningEvents, emitter.events)
	}

	reloaded := conversation.New(runner.sessionID)
	if err := reloaded.LoadFromStorage(runner.store); err != nil {
		t.Fatalf("LoadFromStorage: %v", err)
	}
	var notices, drafts int
	for _, msg := range reloaded.Messages {
		text := conversation.GetContentAsString(msg.Content)
		if strings.Contains(text, "PRIVATE_HEADLESS_REASONING") {
			t.Fatalf("persisted message leaked private reasoning: %+v", msg)
		}
		if msg.Role == "system" && strings.Contains(text, "Incomplete result:") {
			notices++
		}
		if msg.Role == "assistant" && msg.IsTruncated && strings.Contains(text, "Preserved draft (incomplete):\npublic truncated draft") {
			drafts++
		}
	}
	if notices != 1 || drafts != 1 {
		t.Fatalf("persisted notices=%d drafts=%d messages=%+v", notices, drafts, reloaded.Messages)
	}

	assertObservedLifecycleIdentity(t, events, "chatcmpl-partial-1", "gpt-4o-wire")
}

func TestRunner_PreRunEmptyCancellationDoesNotPersistPartialNotice(t *testing.T) {
	root := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("pre-run cancellation should not call model")
	}))
	defer server.Close()
	emitter := &mockEmitter{}
	runner := newHeadlessContractTestRunner(t, server.URL, root, tool.NewEmptyRegistry())
	runner.emitter = emitter

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := runner.runConversationLoopForCommand(ctx, nil, "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runConversationLoopForCommand error = %v, want context canceled", err)
	}
	if len(emitter.events) != 0 {
		t.Fatalf("pre-run cancellation emitted events: %+v", emitter.events)
	}
	reloaded := conversation.New(runner.sessionID)
	if err := reloaded.LoadFromStorage(runner.store); err != nil {
		t.Fatalf("LoadFromStorage: %v", err)
	}
	if len(reloaded.Messages) != 0 {
		t.Fatalf("pre-run cancellation persisted messages: %+v", reloaded.Messages)
	}
}

func assertObservedLifecycleIdentity(t *testing.T, events <-chan telemetry.Event, responseID, responseModel string) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case event := <-events:
			if event.Type != telemetry.EventAgentModelResponse && event.Type != telemetry.EventAgentModelError {
				continue
			}
			if event.Data["response_id"] == responseID && event.Data["response_model"] == responseModel {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for model response lifecycle identity %q/%q", responseID, responseModel)
		}
	}
}

func newHeadlessProviderTestRunner(t *testing.T, provider *headlessPartialProvider, hub *telemetry.Hub, emitter EventEmitter) *Runner {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Models.DefaultProvider = provider.ID()
	cfg.Models.Execution = provider.modelID
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	installHeadlessProvider(t, mgr, provider)
	store := newTestStore(t)
	sessionID := "headless-provider-partial-" + strings.ReplaceAll(provider.response.ID, "_", "-")
	if err := store.EnsureSession(sessionID); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}
	return &Runner{
		sessionID:     sessionID,
		session:       &storage.Session{ID: sessionID},
		conv:          conversation.New(sessionID),
		store:         store,
		config:        cfg,
		modelManager:  mgr,
		tools:         tool.NewEmptyRegistry(),
		modelOverride: provider.modelID,
		emitter:       emitter,
		telemetry:     hub,
		approvalChan:  make(chan ApprovalResponse, 1),
	}
}

func installHeadlessProvider(t *testing.T, mgr *model.Manager, provider *headlessPartialProvider) {
	t.Helper()
	modelInfo := model.ModelInfo{ID: provider.modelID}
	providerID := provider.ID()
	setManagerField(t, mgr, "providers", map[string]model.Provider{providerID: provider})
	setManagerField(t, mgr, "providerOrder", []string{providerID})
	setManagerField(t, mgr, "catalog", map[string]model.ModelInfo{provider.modelID: modelInfo})
	setManagerField(t, mgr, "providerModels", map[string][]string{providerID: {provider.modelID}})
	setManagerField(t, mgr, "modelProviders", map[string]string{provider.modelID: providerID})
}

func setManagerField(t *testing.T, mgr *model.Manager, name string, value any) {
	t.Helper()
	field := reflect.ValueOf(mgr).Elem().FieldByName(name)
	if !field.IsValid() {
		t.Fatalf("model.Manager field %q not found", name)
	}
	reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().Set(reflect.ValueOf(value))
}

type headlessPartialProvider struct {
	modelID      string
	response     *model.ChatResponse
	err          error
	beforeReturn func()
	calls        int
}

func (p *headlessPartialProvider) ID() string { return "headless_partial" }

func (p *headlessPartialProvider) FetchCatalog() (*model.ModelCatalog, error) {
	return &model.ModelCatalog{Data: []model.ModelInfo{{ID: p.modelID}}}, nil
}

func (p *headlessPartialProvider) GetModelInfo(modelID string) (*model.ModelInfo, error) {
	if modelID != p.modelID {
		return nil, errors.New("unexpected model")
	}
	return &model.ModelInfo{ID: p.modelID}, nil
}

func (p *headlessPartialProvider) ChatCompletion(context.Context, model.ChatRequest) (*model.ChatResponse, error) {
	p.calls++
	if p.beforeReturn != nil {
		p.beforeReturn()
	}
	return p.response, p.err
}

func (p *headlessPartialProvider) ChatCompletionStream(context.Context, model.ChatRequest) (<-chan model.StreamChunk, <-chan error) {
	chunks := make(chan model.StreamChunk)
	errs := make(chan error, 1)
	close(chunks)
	errs <- errors.New("unexpected stream request")
	close(errs)
	return chunks, errs
}

func headlessPartialResponse(id, wireModel, content string, usage model.Usage) *model.ChatResponse {
	return &model.ChatResponse{
		ID: id, Model: wireModel,
		Choices: []model.Choice{{
			Message:      model.Message{Role: "assistant", Content: content, Reasoning: "PRIVATE_HEADLESS_REASONING"},
			FinishReason: "stop",
		}},
		Usage:        usage,
		UsagePresent: true,
	}
}

func assertOneIncompleteWarning(t *testing.T, events []RunnerEvent, forbidden string) {
	t.Helper()
	warnings := 0
	for _, event := range events {
		if event.Type == EventError {
			t.Fatalf("partial terminal emitted error event: %+v", event)
		}
		if event.Type != EventWarning {
			continue
		}
		warnings++
		for _, value := range event.Data {
			text, _ := value.(string)
			if strings.Contains(text, forbidden) || strings.Contains(text, "PRIVATE_HEADLESS_REASONING") {
				t.Fatalf("warning leaked private/raw text: %+v", event)
			}
		}
	}
	if warnings != 1 {
		t.Fatalf("warning events = %d, want one incomplete warning; events=%+v", warnings, events)
	}
}

func assertIncompleteHistory(t *testing.T, runner *Runner, draft, forbidden string) {
	t.Helper()
	reloaded := conversation.New(runner.sessionID)
	if err := reloaded.LoadFromStorage(runner.store); err != nil {
		t.Fatalf("LoadFromStorage: %v", err)
	}
	var notices, drafts int
	for _, msg := range reloaded.Messages {
		text := conversation.GetContentAsString(msg.Content)
		if strings.Contains(text, forbidden) || strings.Contains(text, "PRIVATE_HEADLESS_REASONING") {
			t.Fatalf("persisted message leaked private/raw text: %+v", msg)
		}
		if msg.Role == "system" && strings.Contains(text, "Incomplete result:") {
			notices++
		}
		if msg.Role == "assistant" && msg.IsTruncated && strings.Contains(text, "Preserved draft (incomplete):\n"+draft) {
			drafts++
		}
	}
	if notices != 1 || drafts != 1 {
		t.Fatalf("persisted notices=%d drafts=%d messages=%+v", notices, drafts, reloaded.Messages)
	}
}
