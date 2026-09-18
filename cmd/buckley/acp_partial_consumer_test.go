package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"m31labs.dev/buckley/pkg/acp"
	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/rules"
	"m31labs.dev/buckley/pkg/tool"
)

func newACPPartialConsumerManager(t *testing.T, handler http.HandlerFunc) (*config.Config, *model.Manager) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai"
	cfg.Models.Execution = "acp-test/no-tools-model"
	cfg.Models.Curated = []string{"acp-test/no-tools-model"}

	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return cfg, mgr
}

func runACPPartialConsumerPrompt(t *testing.T, cfg *config.Config, mgr *model.Manager, session *acp.AgentSession, observer agentloop.LifecycleObserver, collector *collectingStream) error {
	t.Helper()
	handler, cleanup := makePromptHandler(cfg, mgr, nil, nil, t.TempDir(), func(string, ...interface{}) {}, nil, observer)
	t.Cleanup(cleanup)
	_, err := handler(context.Background(), session, []acp.ContentBlock{acp.NewTextContent("produce a partial answer")}, collector.fn)
	return err
}

func acpMessageText(updates []acp.SessionUpdate) string {
	var out strings.Builder
	for _, update := range updates {
		if update.SessionUpdate != acp.SessionUpdateAgentMessageChunk {
			continue
		}
		block, ok := update.Content.(acp.ContentBlock)
		if ok {
			out.WriteString(block.Text)
		}
	}
	return out.String()
}

func acpUsageTotals(updates []acp.SessionUpdate) []uint64 {
	var totals []uint64
	for _, update := range updates {
		if update.SessionUpdate == acp.SessionUpdateUsageUpdate && update.UsageUsed != nil {
			totals = append(totals, *update.UsageUsed)
		}
	}
	return totals
}

func acpLifecycleModelTerminalEvents(events []agentloop.LifecycleEvent) []agentloop.LifecycleEvent {
	var out []agentloop.LifecycleEvent
	for _, event := range events {
		if event.Type == agentloop.LifecycleModelResponse || event.Type == agentloop.LifecycleModelError {
			out = append(out, event)
		}
	}
	return out
}

func TestACPHandlerProviderErrorStreamsObservedPartialOnceAndKeepsAuditIdentity(t *testing.T) {
	t.Parallel()

	cfg, mgr := newACPPartialConsumerManager(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+
			`{"id":"chatcmpl-public-partial","model":"gpt-4o-2026-09-05","choices":[{"index":0,"delta":{"content":"public partial draft"},"finish_reason":null}],`+
			`"usage":{"prompt_tokens":9,"completion_tokens":6,"total_tokens":15}}`+
			"\n\ndata: "+
			`{"error":{"message":"provider failed with secret-token-49","type":"server_error","code":"provider_failed"}}`+
			"\n\n")
	})

	var lifecycleEvents []agentloop.LifecycleEvent
	collector := &collectingStream{}
	err := runACPPartialConsumerPrompt(t, cfg, mgr, &acp.AgentSession{ID: "acp-public-partial", Mode: acpModePrefix + "acp-test/no-tools-model"}, func(event agentloop.LifecycleEvent) {
		lifecycleEvents = append(lifecycleEvents, event)
	}, collector)
	if err == nil {
		t.Fatalf("handler error = nil, want provider error")
	}

	text := acpMessageText(collector.updates)
	if strings.Count(text, "public partial draft") != 1 {
		t.Fatalf("public partial draft count in ACP messages = %d, text=%q", strings.Count(text, "public partial draft"), text)
	}
	if !strings.Contains(text, "Incomplete draft (not accepted):") {
		t.Fatalf("ACP text = %q, want explicit incomplete draft label", text)
	}
	if strings.Contains(text, "secret-token-49") {
		t.Fatalf("provider secret leaked into ACP incomplete notice: text=%q", text)
	}
	if got := acpUsageTotals(collector.updates); len(got) != 0 {
		t.Fatalf("ACP public usage updates = %+v, want none for incomplete draft", got)
	}

	modelResponses := acpLifecycleModelTerminalEvents(lifecycleEvents)
	if len(modelResponses) != 1 {
		t.Fatalf("model response lifecycle events = %d, want 1: %+v", len(modelResponses), lifecycleEvents)
	}
	got := modelResponses[0]
	if got.ResponseID != "chatcmpl-public-partial" || got.ResponseModel != "gpt-4o-2026-09-05" || got.RequestedModel != "acp-test/no-tools-model" || got.ProviderID != "openai" {
		t.Fatalf("lifecycle execution identity = %+v, want provider-routed partial identity", got)
	}
	if got.Usage.TotalTokens != 15 {
		t.Fatalf("lifecycle usage = %+v, want retained observed partial usage", got.Usage)
	}
}

func TestACPHandlerTaskIntentLabelsIncompleteDraftAndKeepsAuditIdentity(t *testing.T) {
	t.Parallel()

	cfg, mgr := newACPPartialConsumerManager(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+
			`{"id":"chatcmpl-withheld-partial","model":"gpt-4o-2026-09-05","choices":[{"index":0,"delta":{"content":"withheld mutation draft","reasoning":"private chain secret"},"finish_reason":"length"}],`+
			`"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`+
			"\n\ndata: [DONE]\n\n")
	})

	session := &acp.AgentSession{
		ID:          "acp-withheld-partial",
		Mode:        acpModePrefix + "acp-test/no-tools-model",
		Environment: map[string]string{acpTaskIntentEnvKey: string(agentloop.MutationIntent)},
	}
	var lifecycleEvents []agentloop.LifecycleEvent
	collector := &collectingStream{}
	err := runACPPartialConsumerPrompt(t, cfg, mgr, session, func(event agentloop.LifecycleEvent) {
		lifecycleEvents = append(lifecycleEvents, event)
	}, collector)
	if err == nil {
		t.Fatalf("handler error = nil, want incomplete task-intent truncation")
	}

	text := acpMessageText(collector.updates)
	if strings.Count(text, "withheld mutation draft") != 1 || !strings.Contains(text, "Incomplete draft (not accepted):") {
		t.Fatalf("task-intent incomplete draft text = %q, want labeled draft exactly once", text)
	}
	if strings.Contains(text, "private chain secret") || strings.Contains(err.Error(), "private chain secret") {
		t.Fatalf("private reasoning leaked across ACP boundary: text=%q err=%v", text, err)
	}
	if !strings.Contains(strings.ToLower(text), "incomplete") {
		t.Fatalf("ACP messages = %q, want sanitized incomplete notice", text)
	}
	if got := acpUsageTotals(collector.updates); len(got) != 0 {
		t.Fatalf("ACP public usage updates = %+v, want no accepted usage update for withheld draft", got)
	}

	modelResponses := acpLifecycleModelTerminalEvents(lifecycleEvents)
	if len(modelResponses) != 1 {
		t.Fatalf("model response lifecycle events = %d, want 1: %+v", len(modelResponses), lifecycleEvents)
	}
	got := modelResponses[0]
	if got.ResponseID != "chatcmpl-withheld-partial" || got.ResponseModel != "gpt-4o-2026-09-05" || got.RequestedModel != "acp-test/no-tools-model" || got.ProviderID != "openai" {
		t.Fatalf("lifecycle execution identity = %+v, want retained withheld-draft identity", got)
	}
	if got.Usage.TotalTokens != 18 {
		t.Fatalf("lifecycle usage = %+v, want retained withheld-draft usage", got.Usage)
	}
}

func TestACPHandlerPartialToolDeltaDoesNotExecuteOrSurfaceDraft(t *testing.T) {
	t.Parallel()

	cfg, mgr := newACPPartialConsumerManager(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+
			`{"id":"chatcmpl-tool-fragment","model":"gpt-4o-2026-09-05","choices":[{"index":0,"delta":{"content":"tool prelude","tool_calls":[{"index":0,"id":"call-partial","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"secret\"}"}}]},"finish_reason":null}],`+
			`"usage":{"prompt_tokens":13,"completion_tokens":8,"total_tokens":21}}`+
			"\n\ndata: "+
			`{"error":{"message":"provider stopped mid tool call","type":"server_error","code":"provider_failed"}}`+
			"\n\n")
	})

	collector := &collectingStream{}
	err := runACPPartialConsumerPrompt(t, cfg, mgr, &acp.AgentSession{ID: "acp-partial-tool", Mode: acpModePrefix + "acp-test/no-tools-model"}, nil, collector)
	if err == nil {
		t.Fatalf("handler error = nil, want provider error")
	}
	text := acpMessageText(collector.updates)
	if strings.Contains(text, "tool prelude") {
		t.Fatalf("partial tool-associated draft surfaced as ACP output: %q", text)
	}
	for _, update := range collector.updates {
		if update.SessionUpdate == acp.SessionUpdateToolCall || update.SessionUpdate == acp.SessionUpdateToolCallUpdate {
			t.Fatalf("partial tool delta executed or surfaced tool update: %+v", update)
		}
	}
}

func TestACPHandlerRepeatedTextualToolMarkupIsWithheldFromIncompleteDraft(t *testing.T) {
	t.Parallel()

	const attemptedCall = `<search_text>
<query>do not surface this as public draft</query>
<path>/tmp/secret-tool-fragment</path>
</search_text>`
	var requests atomic.Int32
	cfg, mgr := newACPPartialConsumerManager(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		payload, err := json.Marshal(map[string]any{
			"id":    "chatcmpl-textual-tool-markup",
			"model": "gpt-4o-2026-09-05",
			"choices": []any{map[string]any{
				"index":         0,
				"delta":         map[string]any{"content": attemptedCall},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 17, "completion_tokens": 9, "total_tokens": 26},
		})
		if err != nil {
			t.Fatalf("marshal SSE payload: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", payload)
	})

	collector := &collectingStream{}
	err := runACPPartialConsumerPrompt(t, cfg, mgr, &acp.AgentSession{ID: "acp-textual-tool-markup", Mode: acpModePrefix + "acp-test/no-tools-model"}, nil, collector)
	if err == nil {
		t.Fatalf("handler error = nil, want exhausted textual tool-markup incomplete result")
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("provider requests = %d, want one repair attempt then incomplete", got)
	}
	text := acpMessageText(collector.updates)
	if strings.Contains(text, attemptedCall) || strings.Contains(text, "/tmp/secret-tool-fragment") {
		t.Fatalf("textual tool markup surfaced as incomplete draft: %q", text)
	}
	if strings.Contains(text, "Incomplete draft (not accepted):") {
		t.Fatalf("unsafe textual tool markup received incomplete-draft label: %q", text)
	}
}

func TestACPHandlerTextualToolMarkupProviderErrorIsWithheldFromIncompleteDraft(t *testing.T) {
	t.Parallel()

	const attemptedCall = `<search_text>
<query>provider error must not bless this markup</query>
<path>/tmp/provider-error-tool-fragment</path>
</search_text>`
	cfg, mgr := newACPPartialConsumerManager(t, func(w http.ResponseWriter, _ *http.Request) {
		payload, err := json.Marshal(map[string]any{
			"id":    "chatcmpl-textual-markup-error",
			"model": "gpt-4o-2026-09-05",
			"choices": []any{map[string]any{
				"index":         0,
				"delta":         map[string]any{"content": attemptedCall},
				"finish_reason": nil,
			}},
			"usage": map[string]any{"prompt_tokens": 19, "completion_tokens": 11, "total_tokens": 30},
		})
		if err != nil {
			t.Fatalf("marshal SSE payload: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: {\"error\":{\"message\":\"provider stopped after markup\",\"type\":\"server_error\",\"code\":\"provider_failed\"}}\n\n", payload)
	})

	collector := &collectingStream{}
	err := runACPPartialConsumerPrompt(t, cfg, mgr, &acp.AgentSession{ID: "acp-textual-markup-error", Mode: acpModePrefix + "acp-test/no-tools-model"}, nil, collector)
	if err == nil {
		t.Fatalf("handler error = nil, want provider error")
	}
	text := acpMessageText(collector.updates)
	if strings.Contains(text, attemptedCall) || strings.Contains(text, "/tmp/provider-error-tool-fragment") {
		t.Fatalf("provider-error textual tool markup surfaced as incomplete draft: %q", text)
	}
	if strings.Contains(text, "Incomplete draft (not accepted):") {
		t.Fatalf("unsafe provider-error textual markup received incomplete-draft label: %q", text)
	}
}

func TestRunACPLoopLegacyAlreadyDeliveredPartialErrorIsNotMarkedForHandlerSalvage(t *testing.T) {
	t.Parallel()

	cfg, mgr := newACPPartialConsumerManager(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+
			`{"id":"chatcmpl-delivered-partial","model":"gpt-4o-2026-09-05","choices":[{"index":0,"delta":{"content":"already delivered draft"},"finish_reason":null}],`+
			`"usage":{"prompt_tokens":7,"completion_tokens":4,"total_tokens":11}}`+
			"\n\ndata: "+
			`{"error":{"message":"provider failed after delivered draft","type":"server_error","code":"provider_failed"}}`+
			"\n\n")
	})
	conv := conversation.New("acp-delivered-partial")
	conv.AddUserMessage("stream then fail")
	collector := &collectingStream{}

	text, err := runACPLoopWithLimits(context.Background(), cfg, mgr, conv, tool.NewEmptyRegistry(), nil, nil, "acp-test/no-tools-model", "", "acp-delivered-partial", nil, func(string, ...interface{}) {}, collector.fn, acpLoopLimits{VerificationDepth: "legacy"})
	if err == nil || text != "already delivered draft" {
		t.Fatalf("runACPLoopWithLimits = %q, %v, want delivered partial error", text, err)
	}
	if got := strings.Join(collector.messageChunks(), ""); got != "already delivered draft" {
		t.Fatalf("streamed draft = %q, want already delivered draft", got)
	}
	if shouldStreamACPIncompleteDraft(text, err) {
		t.Fatalf("already delivered draft was marked eligible for handler salvage")
	}
}

func TestRunACPLoopLegacyStreamCallbackErrorCountsAsAttemptedDelivery(t *testing.T) {
	t.Parallel()

	cfg, mgr := newACPPartialConsumerManager(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+
			`{"id":"chatcmpl-callback-error","model":"gpt-4o-2026-09-05","choices":[{"index":0,"delta":{"content":"uncertain callback draft"},"finish_reason":null}],`+
			`"usage":{"prompt_tokens":7,"completion_tokens":4,"total_tokens":11}}`+
			"\n\ndata: "+
			`{"error":{"message":"provider failed after callback error","type":"server_error","code":"provider_failed"}}`+
			"\n\n")
	})
	conv := conversation.New("acp-callback-error")
	conv.AddUserMessage("stream callback fails")
	var messageCallbacks int
	streamErr := errors.New("client write failed")
	stream := func(update acp.SessionUpdate) error {
		if update.SessionUpdate == acp.SessionUpdateAgentMessageChunk {
			messageCallbacks++
			return streamErr
		}
		return nil
	}

	text, err := runACPLoopWithLimits(context.Background(), cfg, mgr, conv, tool.NewEmptyRegistry(), nil, nil, "acp-test/no-tools-model", "", "acp-callback-error", nil, func(string, ...interface{}) {}, stream, acpLoopLimits{VerificationDepth: "legacy"})
	if err == nil || text != "uncertain callback draft" {
		t.Fatalf("runACPLoopWithLimits = %q, %v, want callback-attempted partial error", text, err)
	}
	if messageCallbacks != 1 {
		t.Fatalf("message callback invocations = %d, want exactly one attempted delivery", messageCallbacks)
	}
	if shouldStreamACPIncompleteDraft(text, err) {
		t.Fatalf("callback-error draft was marked eligible for whole-draft replay")
	}
}

func TestRunACPLoopLegacyNilStreamDoesNotCountAsAttemptedDelivery(t *testing.T) {
	t.Parallel()

	cfg, mgr := newACPPartialConsumerManager(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+
			`{"id":"chatcmpl-nil-stream","model":"gpt-4o-2026-09-05","choices":[{"index":0,"delta":{"content":"nil stream draft"},"finish_reason":null}],`+
			`"usage":{"prompt_tokens":7,"completion_tokens":4,"total_tokens":11}}`+
			"\n\ndata: "+
			`{"error":{"message":"provider failed without stream","type":"server_error","code":"provider_failed"}}`+
			"\n\n")
	})
	conv := conversation.New("acp-nil-stream")
	conv.AddUserMessage("no stream available")

	text, err := runACPLoopWithLimits(context.Background(), cfg, mgr, conv, tool.NewEmptyRegistry(), nil, nil, "acp-test/no-tools-model", "", "acp-nil-stream", nil, func(string, ...interface{}) {}, nil, acpLoopLimits{VerificationDepth: "legacy"})
	if err == nil || text != "nil stream draft" {
		t.Fatalf("runACPLoopWithLimits = %q, %v, want nil-stream partial error", text, err)
	}
	if !shouldStreamACPIncompleteDraft(text, err) {
		t.Fatalf("nil stream was incorrectly counted as attempted delivery")
	}
}

func TestRunACPLoopLegacyBufferedRetryFailureIsMarkedForHandlerSalvage(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	cfg, mgr := newACPPartialConsumerManager(t, func(w http.ResponseWriter, _ *http.Request) {
		attempt := requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-buffered-%d\",\"model\":\"gpt-4o-2026-09-05\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"buffered retry draft %d\"},\"finish_reason\":null}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":3,\"total_tokens\":8}}\n\n", attempt, attempt)
		// Deliberately omit [DONE] on both attempts. The first partial EOF is
		// retryable; the second is retained but was never delivered to the stream.
	})
	conv := conversation.New("acp-buffered-retry")
	conv.AddUserMessage("retry then fail")
	collector := &collectingStream{}

	text, err := runACPLoopWithLimits(context.Background(), cfg, mgr, conv, tool.NewEmptyRegistry(), nil, nil, "acp-test/no-tools-model", "", "acp-buffered-retry", nil, func(string, ...interface{}) {}, collector.fn, acpLoopLimits{VerificationDepth: "legacy"})
	if err == nil || text != "buffered retry draft 2" {
		t.Fatalf("runACPLoopWithLimits = %q, %v, want second buffered partial error", text, err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("provider requests = %d, want retry then terminal failure", got)
	}
	if got := strings.Join(collector.messageChunks(), ""); got != "" {
		t.Fatalf("buffered retry failure leaked stream text before handler salvage: %q", got)
	}
	if !shouldStreamACPIncompleteDraft(text, err) {
		t.Fatalf("buffered retry failure was falsely marked delivered")
	}
}

func TestRunACPLoopPriorDeliveredRoundDoesNotSuppressCurrentIncompleteDraft(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	cfg, mgr := newACPPartialConsumerManager(t, func(w http.ResponseWriter, r *http.Request) {
		attempt := requests.Add(1)
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.Contains(string(body), `"tools"`) {
			_, _ = io.WriteString(w, "data: "+
				`{"id":"chatcmpl-tool-prelude","model":"gpt-4o-2026-09-05","choices":[{"index":0,"delta":{"content":"accepted tool prelude","tool_calls":[{"index":0,"id":"call-prior","type":"function","function":{"name":"stub_probe","arguments":"{}"}}]},"finish_reason":"tool_calls"}],`+
				`"usage":{"prompt_tokens":23,"completion_tokens":5,"total_tokens":28}}`+
				"\n\ndata: [DONE]\n\n")
			return
		}
		if attempt != 2 {
			t.Fatalf("unexpected finalization attempt %d body=%s", attempt, body)
		}
		_, _ = io.WriteString(w, "data: "+
			`{"id":"chatcmpl-later-incomplete","model":"gpt-4o-2026-09-05","choices":[{"index":0,"delta":{"content":"later incomplete draft"},"finish_reason":"length"}],`+
			`"usage":{"prompt_tokens":29,"completion_tokens":6,"total_tokens":35}}`+
			"\n\ndata: [DONE]\n\n")
	})
	engine, err := rules.NewDefaultEngine()
	if err != nil {
		t.Fatalf("rules.NewDefaultEngine: %v", err)
	}
	registry := tool.NewEmptyRegistry()
	probe := &acpProbeTool{}
	registry.Register(probe)
	conv := conversation.New("acp-prior-round")
	conv.AddUserMessage("use tool then summarize")
	collector := &collectingStream{}

	text, err := runACPLoopWithLimits(context.Background(), cfg, mgr, conv, registry, nil, engine, "gpt-4o", "", "acp-prior-round", nil, func(string, ...interface{}) {}, collector.fn, acpLoopLimits{MaxCostUSD: 0.25, MaxModelRequests: 2})
	if err == nil || text != "later incomplete draft" {
		t.Fatalf("runACPLoopWithLimits = %q, %v, want later incomplete draft", text, err)
	}
	if probe.callCount() != 1 {
		t.Fatalf("probe calls = %d, want prior accepted tool call only once", probe.callCount())
	}
	if got := strings.Join(collector.messageChunks(), ""); !strings.Contains(got, "accepted tool prelude") || strings.Contains(got, "later incomplete draft") {
		t.Fatalf("streamed chunks = %q, want prior prelude only before handler salvage", got)
	}
	if !shouldStreamACPIncompleteDraft(text, err) {
		t.Fatalf("prior delivered round suppressed current incomplete draft salvage")
	}
}

func TestACPHandlerEmptyCancelHasNoDraftUsageOrModelIdentity(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	cfg, mgr := newACPPartialConsumerManager(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var lifecycleEvents []agentloop.LifecycleEvent
	collector := &collectingStream{}
	handler, cleanup := makePromptHandler(cfg, mgr, nil, nil, t.TempDir(), func(string, ...interface{}) {}, nil, func(event agentloop.LifecycleEvent) {
		lifecycleEvents = append(lifecycleEvents, event)
	})
	t.Cleanup(cleanup)
	_, err := handler(ctx, &acp.AgentSession{ID: "acp-empty-cancel", Mode: acpModePrefix + "acp-test/no-tools-model"}, []acp.ContentBlock{acp.NewTextContent("cancel before model")}, collector.fn)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("handler error = %v, want context canceled", err)
	}
	if text := acpMessageText(collector.updates); strings.Contains(text, "draft") {
		t.Fatalf("empty cancel produced draft-like ACP text: %q", text)
	}
	if got := acpUsageTotals(collector.updates); len(got) != 0 {
		t.Fatalf("empty cancel usage updates = %+v, want none", got)
	}
	if modelResponses := acpLifecycleModelTerminalEvents(lifecycleEvents); len(modelResponses) != 0 {
		t.Fatalf("empty cancel model responses = %+v, want none", modelResponses)
	}
}
