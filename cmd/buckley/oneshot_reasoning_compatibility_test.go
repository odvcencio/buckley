package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/tools"
)

type compatibilityReasoningChecker struct{ model.CapabilityResolution }

func (c compatibilityReasoningChecker) SupportsReasoning(string) bool { return c.Supported() }
func (c compatibilityReasoningChecker) ResolveReasoningCapability(string) model.CapabilityResolution {
	return c.CapabilityResolution
}

func TestOneshotReasoningCompatibility_ScopesKnownRoute(t *testing.T) {
	for _, tc := range []struct {
		name, provider, modelID, command string
		state                            model.CapabilityState
		wantCompatibility, wantWarning   bool
	}{
		{"commit", "openrouter", "qwen/qwen3.8-flash", "commit", model.CapabilitySupported, true, true},
		{"pr", "openrouter", "qwen/qwen3.8-flash", "pr", model.CapabilitySupported, true, true},
		{"review", "openrouter", "qwen/qwen3.8-flash", "review", model.CapabilitySupported, true, false},
		{"other model", "openrouter", "openai/gpt-5", "commit", model.CapabilitySupported, false, false},
		{"other qwen", "openrouter", "qwen/qwen3-coder", "commit", model.CapabilitySupported, false, false},
		{"other provider", "openai_compatible", "qwen/qwen3.8-flash", "commit", model.CapabilitySupported, false, false},
		{"unsupported", "openrouter", "qwen/qwen3.8-flash", "commit", model.CapabilityNotAdvertised, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Models.Reasoning = "high"
			checker := compatibilityReasoningChecker{model.CapabilityResolution{
				Model: tc.modelID, ProviderID: tc.provider, State: tc.state,
			}}
			profile, diagnostic := resolveOneshotRequestProfileWithDiagnostic(tc.command, cfg, checker, "route-alias")
			if profile.DisableReasoningForForcedTools != tc.wantCompatibility || (diagnostic != "") != tc.wantWarning {
				t.Fatalf("profile = %+v, diagnostic = %q", profile, diagnostic)
			}
			if checker.Supported() && (profile.Reasoning == nil || profile.Reasoning.Effort != "high") {
				t.Fatalf("free-text reasoning changed: %+v", profile.Reasoning)
			}
			if cfg.Models.Reasoning != "high" {
				t.Fatal("compatibility changed the configured reasoning effort")
			}
		})
	}
}

func TestOneshotReasoningCompatibility_OpenRouterWire(t *testing.T) {
	for _, effort := range []string{"auto", "high", "off"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", effort, stream), func(t *testing.T) {
				requests := make(chan model.ChatRequest, 2)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if r.URL.Path == "/models" {
						_, _ = io.WriteString(w, `{"data":[{"id":"qwen/qwen3.8-flash","context_length":32768,"supported_parameters":["tools","reasoning"]}]}`)
						return
					}
					if r.URL.Path != "/chat/completions" {
						http.NotFound(w, r)
						return
					}
					var req model.ChatRequest
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						t.Errorf("decode request: %v", err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					requests <- req
					if len(req.Tools) > 0 && (req.Reasoning == nil || req.Reasoning.Enabled == nil || *req.Reasoning.Enabled) {
						w.WriteHeader(http.StatusBadRequest)
						_, _ = io.WriteString(w, `{"error":{"message":"The tool_choice parameter does not support being set to required or object in thinking mode"}}`)
						return
					}
					if req.Stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"type\":\"function\",\"function\":{\"name\":\"submit_commit\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
						return
					}
					_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"summary","tool_calls":[{"id":"call-1","type":"function","function":{"name":"submit_commit","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
				}))
				t.Cleanup(server.Close)
				cfg := config.DefaultConfig()
				cfg.Models.Reasoning = effort
				cfg.Models.DefaultProvider = "openrouter"
				cfg.Providers.OpenRouter = config.ProviderSettings{Enabled: true, APIKey: "test-key", BaseURL: server.URL}
				mgr, err := model.NewManager(cfg)
				if err != nil {
					t.Fatal(err)
				}
				if err := mgr.RefreshProviderCatalog("openrouter"); err != nil {
					t.Fatal(err)
				}
				mgr.RoutingHooks().Register(func(decision *model.RoutingDecision) *model.RoutingDecision {
					decision.SelectedModel = "qwen/qwen3.8-flash"
					return decision
				})
				toolInvoker, err := newOneshotToolInvoker(oneshotBackendAPI, "commit", "route-alias", cfg, mgr, nil)
				if err != nil {
					t.Fatal(err)
				}
				inv := toolInvoker.(*oneshot.DefaultInvoker)
				tool := tools.Definition{Name: "submit_commit", Parameters: tools.ObjectSchema(map[string]tools.Property{}, "")}
				var result *oneshot.Result
				if stream {
					result, _, err = inv.InvokeStream(context.Background(), "system", "user", tool, nil, nil)
				} else {
					result, _, err = inv.Invoke(context.Background(), "system", "user", tool, nil)
				}
				if err != nil || result == nil || !result.HasToolCall() {
					t.Fatalf("invoke = %+v, %v", result, err)
				}
				req := <-requests
				if req.Model != "qwen/qwen3.8-flash" || req.ToolChoice != "required" || len(req.Tools) != 1 || req.Reasoning.Effort != "" {
					t.Fatalf("forced request = %+v", req)
				}
				if _, _, err := inv.InvokeText(context.Background(), "system", "user", nil); err != nil {
					t.Fatal(err)
				}
				textReq := <-requests
				if len(textReq.Tools) != 0 || textReq.ToolChoice != "" || textReq.Reasoning == nil {
					t.Fatalf("text request = %+v", textReq)
				}
				if effort == "off" {
					if textReq.Reasoning.Enabled == nil || *textReq.Reasoning.Enabled {
						t.Fatal("explicit reasoning off was lost")
					}
				} else if textReq.Reasoning.Effort == "" || textReq.Reasoning.Enabled != nil {
					t.Fatalf("free-text reasoning changed: %+v", textReq.Reasoning)
				}
			})
		}
	}
}
