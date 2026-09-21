package rlm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/telemetry"
)

func newCoordinatorTestManager(t *testing.T, server *httptest.Server) *model.Manager {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai"

	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	// NewRuntime requires a non-empty catalog (ModelRouter validates
	// against it); the OpenAI provider's catalog is a static list, so this
	// never touches the test server.
	if err := mgr.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return mgr
}

// TestRuntimeExecute_SecondRequestCarriesToolCallAndResult is the
// cross-round transcript invariant carried over from the pkg/headless,
// pkg/ui/tui, and pkg/rlm SubAgent agentloop.Controller migrations: after
// round one dispatches a tool, round two's request must contain the
// assistant tool-call message and its tool result, or the model loops on
// stale history.
func TestRuntimeExecute_SecondRequestCarriesToolCallAndResult(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) == 1 {
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-1","model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_coord_1","type":"function","function":{"name":"set_answer","arguments":"{\"content\":\"partial\",\"ready\":false,\"confidence\":0.1}"}}]},"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}
			}`)
			return
		}
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-2","model":"gpt-4o",
			"choices":[{"index":0,"message":{"role":"assistant","content":"final answer"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":12,"completion_tokens":4,"total_tokens":16}
		}`)
	}))
	defer server.Close()

	mgr := newCoordinatorTestManager(t, server)
	rt, err := NewRuntime(Config{}, RuntimeDeps{Models: mgr})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}

	answer, err := rt.Execute(context.Background(), "answer the question")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if answer.Content != "final answer" {
		t.Fatalf("Content = %q, want %q", answer.Content, "final answer")
	}
	if !answer.Ready {
		t.Fatal("expected Ready == true")
	}
	if len(bodies) != 2 {
		t.Fatalf("expected 2 model requests, got %d", len(bodies))
	}

	second := bodies[1]
	if !strings.Contains(second, "call_coord_1") {
		t.Fatalf("second request missing assistant tool-call message: %s", second)
	}
	if !strings.Contains(second, `"role":"tool"`) {
		t.Fatalf("second request missing tool result message: %s", second)
	}
}

// TestRuntimeExecute_AccumulatesStateAcrossRounds is the accumulated-state
// invariant: after two set_answer tool rounds (each below the confidence
// threshold, so the loop keeps going) and a final text answer, Answer must
// reflect the sum of every round's token usage and the last tool call's
// confidence, and the final request must still carry both prior tool
// exchanges, not just the most recent one.
func TestRuntimeExecute_AccumulatesStateAcrossRounds(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "application/json")
		switch len(bodies) {
		case 1:
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-1","model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_coord_1","type":"function","function":{"name":"set_answer","arguments":"{\"content\":\"partial one\",\"ready\":false,\"confidence\":0.2}"}}]},"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}
			}`)
		case 2:
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-2","model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_coord_2","type":"function","function":{"name":"set_answer","arguments":"{\"content\":\"partial two\",\"ready\":false,\"confidence\":0.4}"}}]},"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":20,"completion_tokens":6,"total_tokens":26}
			}`)
		default:
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-3","model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","content":"final synthesis"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":30,"completion_tokens":7,"total_tokens":37}
			}`)
		}
	}))
	defer server.Close()

	mgr := newCoordinatorTestManager(t, server)
	rt, err := NewRuntime(Config{}, RuntimeDeps{Models: mgr})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}

	answer, err := rt.Execute(context.Background(), "answer the question")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if answer.Content != "final synthesis" {
		t.Fatalf("Content = %q, want %q", answer.Content, "final synthesis")
	}
	if !answer.Ready {
		t.Fatal("expected Ready == true")
	}
	if len(bodies) != 3 {
		t.Fatalf("expected 3 model requests, got %d", len(bodies))
	}
	if answer.TokensUsed != 78 { // 15 + 26 + 37
		t.Errorf("TokensUsed = %d, want 78", answer.TokensUsed)
	}
	if answer.Confidence != 0.4 {
		t.Errorf("Confidence = %v, want 0.4 (from the last set_answer call)", answer.Confidence)
	}

	final := bodies[2]
	if !strings.Contains(final, "call_coord_1") || !strings.Contains(final, "call_coord_2") {
		t.Fatalf("final request missing accumulated tool calls: %s", final)
	}
}

func TestRuntimeExecute_CompactsCoordinatorContextWithSelectedRoute(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"tiny-route"}]}`)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) == 1 {
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-1","model":"tiny-route",
				"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_coord_1","type":"function","function":{"name":"set_answer","arguments":"{\"content\":\"partial\",\"ready\":false,\"confidence\":0.1}"}}]},"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}
			}`)
			return
		}
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-2","model":"tiny-route",
			"choices":[{"index":0,"message":{"role":"assistant","content":"final answer"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":12,"completion_tokens":4,"total_tokens":16}
		}`)
	}))
	defer server.Close()

	cfg := config.DefaultConfig()
	cfg.Providers.OpenRouter.Enabled = false
	cfg.Providers.OpenAI.Enabled = false
	cfg.Providers.OpenAICompatible.Enabled = true
	cfg.Providers.OpenAICompatible.BaseURL = server.URL
	cfg.Providers.OpenAICompatible.APIKey = "test-key"
	cfg.Providers.OpenAICompatible.Models = []string{"tiny-route"}
	cfg.Providers.OpenAICompatible.ContextLengths = map[string]int{"openai_compatible/tiny-route": 4096}
	cfg.Providers.OpenAICompatible.SupportedParameters = map[string][]string{"openai_compatible/tiny-route": {"tools"}}
	cfg.Models.DefaultProvider = "openai_compatible"
	cfg.Models.Execution = "openai_compatible/tiny-route"
	cfg.Models.Planning = "openai_compatible/tiny-route"
	cfg.Models.Review = "openai_compatible/tiny-route"
	cfg.Models.FallbackChains = map[string][]string{}
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := mgr.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	mgr.RoutingHooks().Register(func(decision *model.RoutingDecision) *model.RoutingDecision {
		decision.SelectedModel = "openai_compatible/tiny-route"
		return decision
	})

	rtCfg := DefaultConfig()
	rtCfg.Coordinator.Model = "alias/coordinator"
	rt, err := NewRuntime(rtCfg, RuntimeDeps{Models: mgr})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}

	const (
		taskStart  = "ROOT SCOPE: inspect route-bound context. "
		acceptance = "ACCEPTANCE: preserve this ending after compaction"
	)
	task := taskStart + strings.Repeat("route-bound-context ", 2500) + acceptance
	answer, err := rt.Execute(context.Background(), task)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if answer.Content != "final answer" || !answer.Ready {
		t.Fatalf("answer = %+v, want final answer", answer)
	}
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want 2", len(bodies))
	}
	messages, ok := bodies[1]["messages"].([]any)
	if !ok {
		t.Fatalf("second request messages = %#v", bodies[1]["messages"])
	}
	second := fmt.Sprint(messages)
	for _, want := range []string{"root task middle compacted", taskStart, acceptance} {
		if !strings.Contains(second, want) {
			t.Fatalf("second request missing %q after selected-route compaction: %#v", want, messages)
		}
	}
}

func TestRuntimeExecute_ToollessCoordinatorOmitsToolsAndAcceptsStopText(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-toolless","model":"o1-mini",
			"choices":[{"index":0,"message":{"role":"assistant","content":"direct coordinator answer"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13}
		}`)
	}))
	defer server.Close()

	mgr := newCoordinatorTestManager(t, server)
	cfg := DefaultConfig()
	cfg.Coordinator.Model = "openai/o1-mini"
	rt, err := NewRuntime(cfg, RuntimeDeps{Models: mgr})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}

	answer, err := rt.Execute(context.Background(), "answer directly")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if answer.Content != "direct coordinator answer" || !answer.Ready {
		t.Fatalf("answer = %+v, want ready direct text answer", answer)
	}
	if _, ok := body["tools"]; ok {
		t.Fatalf("request included tools for catalog-confirmed toolless coordinator: %v", body["tools"])
	}
	if _, ok := body["tool_choice"]; ok {
		t.Fatalf("request included tool_choice for catalog-confirmed toolless coordinator: %v", body["tool_choice"])
	}
	messages, ok := body["messages"].([]any)
	if !ok || len(messages) == 0 {
		t.Fatalf("messages = %#v, want request messages", body["messages"])
	}
	system, ok := messages[0].(map[string]any)
	if !ok {
		t.Fatalf("first message = %#v, want object", messages[0])
	}
	if content, _ := system["content"].(string); !strings.Contains(content, "Do not emit, describe, or pretend to call coordinator tools") {
		t.Fatalf("system prompt = %q, want honest toolless instruction", content)
	}
}

func TestRuntimeExecute_ToollessCoordinatorUnexpectedToolCallDoesNotExecute(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) == 1 {
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-surprise-tool","model":"o1-mini",
				"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_unexpected","type":"function","function":{"name":"set_answer","arguments":"{\"content\":\"forged tool answer\",\"ready\":true,\"confidence\":1}"}}]},"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13}
			}`)
			return
		}
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-direct-after-surprise","model":"o1-mini",
			"choices":[{"index":0,"message":{"role":"assistant","content":"direct answer after rejected tool"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}
		}`)
	}))
	defer server.Close()

	mgr := newCoordinatorTestManager(t, server)
	cfg := DefaultConfig()
	cfg.Coordinator.Model = "openai/o1-mini"
	rt, err := NewRuntime(cfg, RuntimeDeps{Models: mgr})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}

	answer, err := rt.Execute(context.Background(), "answer directly")
	var incomplete *agentloop.IncompleteTurnError
	if err == nil || !errors.As(err, &incomplete) || incomplete.Code != "unoffered_tool_call" {
		t.Fatalf("Execute error = %v, want unoffered tool-call incomplete error", err)
	}
	if answer == nil || answer.Ready || !strings.Contains(answer.Content, "unexecuted tool call") {
		t.Fatalf("answer = %+v, want retained safe incomplete evidence", answer)
	}
	if strings.Contains(answer.Content, "forged tool answer") || len(answer.TaskResults) != 0 {
		t.Fatalf("answer = %+v, want no executed tool effects", answer)
	}
	if len(bodies) != 1 {
		t.Fatalf("requests = %d, want no retry or follow-up history request", len(bodies))
	}
	if _, ok := bodies[0]["tools"]; ok {
		t.Fatalf("first request included tools in toolless mode: %v", bodies[0]["tools"])
	}
	if _, ok := bodies[0]["tool_choice"]; ok {
		t.Fatalf("first request included tool_choice in toolless mode: %v", bodies[0]["tool_choice"])
	}
}

func TestRuntimeExecute_CatalogToollessCompatibleSurpriseNeverReaddsNoopTool(t *testing.T) {
	for _, providerID := range []string{"openai_compatible", "litellm"} {
		t.Run(providerID, func(t *testing.T) {
			var bodies []map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/model/info":
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = io.WriteString(w, `{"error":"force /models fallback"}`)
				case "/models":
					_, _ = io.WriteString(w, fmt.Sprintf(`{"data":[{"id":"%s/tiny-toolless","name":"tiny-toolless","context_length":4096,"supported_parameters":[]}]}`, providerID))
				case "/chat/completions":
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatalf("decode request: %v", err)
					}
					bodies = append(bodies, body)
					if len(bodies) == 1 {
						_, _ = io.WriteString(w, `{
							"id":"chatcmpl-surprise-tool","model":"tiny-toolless",
							"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_unexpected","type":"function","function":{"name":"set_answer","arguments":"{\"content\":\"forged compatible answer\",\"ready\":true,\"confidence\":1}"}}]},"finish_reason":"tool_calls"}],
							"usage":{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13}
						}`)
						return
					}
					_, _ = io.WriteString(w, `{
						"id":"chatcmpl-direct-after-surprise","model":"tiny-toolless",
						"choices":[{"index":0,"message":{"role":"assistant","content":"direct compatible answer after rejected tool"},"finish_reason":"stop"}],
						"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}
					}`)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()

			cfg := config.DefaultConfig()
			cfg.Models.DefaultProvider = providerID
			cfg.Models.Execution = providerID + "/tiny-toolless"
			cfg.Models.Planning = providerID + "/tiny-toolless"
			cfg.Models.Review = providerID + "/tiny-toolless"
			cfg.Models.FallbackChains = map[string][]string{}
			switch providerID {
			case "openai_compatible":
				cfg.Providers.OpenAICompatible.Enabled = true
				cfg.Providers.OpenAICompatible.APIKey = "test-key"
				cfg.Providers.OpenAICompatible.BaseURL = server.URL
			case "litellm":
				cfg.Providers.LiteLLM.Enabled = true
				cfg.Providers.LiteLLM.APIKey = "test-key"
				cfg.Providers.LiteLLM.BaseURL = server.URL
			}
			mgr, err := model.NewManager(cfg)
			if err != nil {
				t.Fatalf("NewManager: %v", err)
			}
			if err := mgr.Initialize(); err != nil {
				t.Fatalf("Initialize: %v", err)
			}
			mgr.RoutingHooks().Register(func(decision *model.RoutingDecision) *model.RoutingDecision {
				if decision != nil && decision.RequestedModel == "alias/coordinator" {
					decision.SelectedModel = providerID + "/tiny-toolless"
				}
				return decision
			})

			rtCfg := DefaultConfig()
			rtCfg.Coordinator.Model = "alias/coordinator"
			rt, err := NewRuntime(rtCfg, RuntimeDeps{Models: mgr})
			if err != nil {
				t.Fatalf("NewRuntime: %v", err)
			}

			answer, err := rt.Execute(context.Background(), "answer directly")
			var incomplete *agentloop.IncompleteTurnError
			if err == nil || !errors.As(err, &incomplete) || incomplete.Code != "unoffered_tool_call" {
				t.Fatalf("Execute error = %v, want unoffered tool-call incomplete error", err)
			}
			if answer == nil || answer.Ready || !strings.Contains(answer.Content, "unexecuted tool call") {
				t.Fatalf("answer = %+v, want retained safe incomplete evidence", answer)
			}
			if strings.Contains(answer.Content, "forged compatible answer") || len(answer.TaskResults) != 0 {
				t.Fatalf("answer = %+v, want no executed tool effects", answer)
			}
			if len(bodies) != 1 {
				t.Fatalf("requests = %d, want no retry or follow-up history request", len(bodies))
			}
			for index, body := range bodies {
				if _, ok := body["tools"]; ok {
					t.Fatalf("request %d included tools in catalog-toolless mode: %v", index+1, body["tools"])
				}
				if _, ok := body["tool_choice"]; ok {
					t.Fatalf("request %d included tool_choice in catalog-toolless mode: %v", index+1, body["tool_choice"])
				}
			}
		})
	}
}

func TestRuntimeExecute_CoordinatorToolModeUsesSelectedRoute(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-routed","model":"gpt-4o",
			"choices":[{"index":0,"message":{"role":"assistant","content":"routed direct answer"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13}
		}`)
	}))
	defer server.Close()

	mgr := newCoordinatorTestManager(t, server)
	mgr.RoutingHooks().Register(func(decision *model.RoutingDecision) *model.RoutingDecision {
		if decision.RequestedModel == "openai/o1-mini" {
			decision.SelectedModel = "openai/gpt-4o"
		}
		return decision
	})
	cfg := DefaultConfig()
	cfg.Coordinator.Model = "openai/o1-mini"
	rt, err := NewRuntime(cfg, RuntimeDeps{Models: mgr})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}

	answer, err := rt.Execute(context.Background(), "answer through routed coordinator")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if answer.Content != "routed direct answer" || !answer.Ready {
		t.Fatalf("answer = %+v, want routed stop text", answer)
	}
	if body["model"] != "gpt-4o" {
		t.Fatalf("wire model = %v, want selected route normalized to gpt-4o", body["model"])
	}
	if !requestHasToolMap(body, "delegate") {
		t.Fatalf("request omitted coordinator tools despite selected route supporting tools: %#v", body["tools"])
	}
}

func requestHasToolMap(body map[string]any, name string) bool {
	tools, ok := body["tools"].([]any)
	if !ok {
		return false
	}
	for _, rawTool := range tools {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		function, ok := tool["function"].(map[string]any)
		if !ok {
			continue
		}
		if function["name"] == name {
			return true
		}
	}
	return false
}

func TestRuntimeExecute_SetAnswerPersistsPublicArtifactAndDecisionEntries(t *testing.T) {
	const privateSentinel = "PRIVATE_REASONING_SENTINEL"
	longContent := strings.Repeat("é", 1500)
	longArtifact := strings.Repeat("artifact-é", 80)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		response := coordinatorSetAnswerResponseWithDetails("chatcmpl-persist", longContent, true, 0.99, 10, 5, []string{longArtifact}, []string{"ship it"})
		response = strings.Replace(response, `"role":"assistant","tool_calls"`, `"role":"assistant","reasoning":"`+privateSentinel+`","tool_calls"`, 1)
		_, _ = io.WriteString(w, response)
	}))
	defer server.Close()

	store, err := storage.New(filepath.Join(t.TempDir(), "scratchpad.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()
	mgr := newCoordinatorTestManager(t, server)
	cfg := DefaultConfig()
	cfg.Scratchpad.PersistArtifacts = true
	cfg.Scratchpad.PersistDecisions = true
	rt, err := NewRuntime(cfg, RuntimeDeps{Models: mgr, Store: store})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}

	answer, err := rt.Execute(context.Background(), "persist public answer state")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !answer.Ready || answer.Content != longContent {
		t.Fatalf("answer = %+v, want retained public content", answer)
	}

	entries, err := store.ListScratchpadEntries(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListScratchpadEntries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("persisted entries = %d, want decision and artifact", len(entries))
	}
	summaries, err := rt.scratchpad.ListSummaries(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListSummaries: %v", err)
	}
	if len(summaries) != 0 {
		t.Fatalf("active scratchpad summaries = %+v, want coordinator answer rows store-only", summaries)
	}
	reloaded := NewScratchpad(store, nil, cfg.Scratchpad)
	reloadedSummaries, err := reloaded.ListSummaries(context.Background(), 10)
	if err != nil {
		t.Fatalf("reloaded ListSummaries: %v", err)
	}
	if len(reloadedSummaries) != 0 {
		t.Fatalf("reloaded scratchpad summaries = %+v, want durable-only rows excluded", reloadedSummaries)
	}
	byType := map[string]string{}
	for _, entry := range entries {
		byType[entry.EntryType] = string(entry.Raw)
		if strings.Contains(string(entry.Raw), privateSentinel) || strings.Contains(entry.Summary, privateSentinel) || strings.Contains(entry.Metadata, privateSentinel) {
			t.Fatalf("private sentinel leaked in %s entry: raw=%q summary=%q metadata=%q", entry.EntryType, entry.Raw, entry.Summary, entry.Metadata)
		}
		if len(entry.Summary) > coordinatorAnswerScratchpadItemBytes {
			t.Fatalf("summary length = %d, want <= %d", len(entry.Summary), coordinatorAnswerScratchpadItemBytes)
		}
	}
	if byType[string(EntryTypeDecision)] == "" {
		t.Fatalf("missing decision entry: %+v", entries)
	}
	if byType[string(EntryTypeArtifact)] == "" {
		t.Fatalf("missing artifact entry: %+v", entries)
	}
	var decision coordinatorAnswerScratchpadPayload
	if err := json.Unmarshal([]byte(byType[string(EntryTypeDecision)]), &decision); err != nil {
		t.Fatalf("decision raw JSON: %v", err)
	}
	if !decision.Ready || decision.Confidence != 0.99 || len(decision.Artifacts) != 1 || len(decision.NextSteps) != 1 {
		t.Fatalf("decision payload = %+v", decision)
	}
	if len(decision.Content) > coordinatorAnswerScratchpadTextBytes || len(decision.Artifacts[0]) > coordinatorAnswerScratchpadItemBytes {
		t.Fatalf("decision payload not bounded: content=%d artifact=%d", len(decision.Content), len(decision.Artifacts[0]))
	}
}

func TestRuntimeExecute_SetAnswerPersistenceFlagsDisableStoreWrites(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, coordinatorSetAnswerResponseWithDetails("chatcmpl-disabled", "disabled persistence answer", true, 0.8, 3, 2, []string{"artifact-ref"}, nil))
	}))
	defer server.Close()

	store, err := storage.New(filepath.Join(t.TempDir(), "scratchpad.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()
	mgr := newCoordinatorTestManager(t, server)
	cfg := DefaultConfig()
	cfg.Scratchpad.PersistArtifacts = false
	cfg.Scratchpad.PersistDecisions = false
	rt, err := NewRuntime(cfg, RuntimeDeps{Models: mgr, Store: store})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}

	answer, err := rt.Execute(context.Background(), "do not persist public answer state")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !answer.Ready || answer.Content != "disabled persistence answer" {
		t.Fatalf("answer = %+v", answer)
	}
	entries, err := store.ListScratchpadEntries(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListScratchpadEntries: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("persisted entries = %+v, want none when persistence flags disabled", entries)
	}
	summaries, err := rt.scratchpad.ListSummaries(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListSummaries: %v", err)
	}
	if len(summaries) != 0 {
		t.Fatalf("active scratchpad summaries = %+v, want none when persistence flags disabled", summaries)
	}
}

func TestRuntimeExecute_SetAnswerScratchpadStoreFailureIsBestEffort(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, coordinatorSetAnswerResponseWithDetails("chatcmpl-store-failure", "answer survives store failure", true, 0.8, 3, 2, []string{"artifact-ref"}, nil))
	}))
	defer server.Close()

	store, err := storage.New(filepath.Join(t.TempDir(), "scratchpad.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	mgr := newCoordinatorTestManager(t, server)
	cfg := DefaultConfig()
	cfg.Scratchpad.PersistArtifacts = true
	cfg.Scratchpad.PersistDecisions = true
	rt, err := NewRuntime(cfg, RuntimeDeps{Models: mgr, Store: store})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	answer, err := rt.Execute(context.Background(), "store failure should not reject set_answer")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !answer.Ready || answer.Content != "answer survives store failure" {
		t.Fatalf("answer = %+v", answer)
	}
}

func TestRuntimeExecute_RoundGuardFinalizesAndReportsTermination(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) == 1 {
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-1","model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_coord_guard","type":"function","function":{"name":"set_answer","arguments":"{\"content\":\"partial\",\"ready\":false,\"confidence\":0.1}"}}]},"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}
			}`)
			return
		}
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-2","model":"gpt-4o",
			"choices":[{"index":0,"message":{"role":"assistant","content":"guarded final synthesis"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":30,"completion_tokens":10,"total_tokens":40}
		}`)
	}))
	defer server.Close()

	mgr := newCoordinatorTestManager(t, server)
	hub := telemetry.NewHub()
	events, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	cfg := DefaultConfig()
	cfg.Coordinator.MaxIterations = 1
	rt, err := NewRuntime(cfg, RuntimeDeps{Models: mgr, Telemetry: hub, SessionID: "guard-session"})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}

	answer, err := rt.Execute(context.Background(), "answer from available evidence")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if answer.Content != "guarded final synthesis" || !answer.Ready {
		t.Fatalf("answer = %+v, want conclusive guard synthesis", answer)
	}
	if answer.TokensUsed != 55 {
		t.Fatalf("TokensUsed = %d, want finalization usage included", answer.TokensUsed)
	}
	if len(bodies) != 2 || !strings.Contains(bodies[1], "stopped further tool execution") {
		t.Fatalf("finalization request missing or ungrounded: %v", bodies)
	}

	foundTermination := false
	for {
		select {
		case event := <-events:
			if event.Type == telemetry.EventDebug && event.Data["source"] == "rlm.controller" {
				foundTermination = event.Data["termination_kind"] == "round_limit" && event.Data["finalization_attempted"] == true
			}
		default:
			if !foundTermination {
				t.Fatal("missing RLM controller termination telemetry")
			}
			return
		}
	}
}
