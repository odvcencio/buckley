package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/oneshot/commands"
)

type fallbackCommandDefinition struct{ oneshot.Definition }

func (fallbackCommandDefinition) ContextSources() []oneshot.ContextSource { return nil }
func (fallbackCommandDefinition) SystemPrompt() string                    { return "generate" }
func (fallbackCommandDefinition) BuildPrompt(*oneshot.Context) string     { return "generate" }
func (d fallbackCommandDefinition) Repair(raw json.RawMessage) (json.RawMessage, []string) {
	return d.Definition.(oneshot.RepairableDefinition).Repair(raw)
}

func TestUtilityValidationFallbacks_ConfiguredOrder(t *testing.T) {
	for _, def := range []oneshot.Definition{commands.CommitDefinition{}, commands.PRDefinition{}} {
		t.Run(def.Name(), func(t *testing.T) {
			var mu sync.Mutex
			var order []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/models" {
					_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "primary"}, map[string]any{"id": "second"}, map[string]any{"id": "third"}}})
					return
				}
				if r.URL.Path != "/chat/completions" {
					http.NotFound(w, r)
					return
				}
				var request struct {
					Model string `json:"model"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				order = append(order, request.Model)
				mu.Unlock()
				action := "ship"
				if request.Model == "third" {
					action = "fix"
				}
				args, _ := json.Marshal(map[string]any{"action": action, "scope": "project", "subject": strings.Repeat("word ", 30), "title": strings.Repeat("word ", 30), "body": []string{"Keep details"}, "summary": "Keep details", "changes": []string{"Keep details"}})
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "test", "model": request.Model, "choices": []any{map[string]any{"index": 0, "finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "call", "type": "function", "function": map[string]any{"name": def.Tool().Name, "arguments": string(args)}}}}}}})
			}))
			t.Cleanup(server.Close)
			cfg := config.DefaultConfig()
			cfg.Models.DefaultProvider = "openai_compatible"
			cfg.Models.Reasoning = "off"
			cfg.Providers.OpenAICompatible.Enabled = true
			cfg.Providers.OpenAICompatible.APIKey = "test-key"
			cfg.Providers.OpenAICompatible.BaseURL = server.URL
			primary := "openai_compatible/primary"
			cfg.Models.FallbackChains = map[string][]string{primary: {"openai_compatible/second", "openai_compatible/third"}}
			mgr, err := model.NewManager(cfg)
			if err != nil {
				t.Fatal(err)
			}
			invoker, err := newOneshotToolInvoker(oneshotBackendAPI, def.Name(), primary, cfg, mgr, nil)
			if err != nil {
				t.Fatal(err)
			}
			framework := withUtilityValidationFallbacks(oneshot.NewFramework(invoker, nil), oneshotBackendAPI, def.Name(), primary, cfg, mgr, nil)
			result, err := framework.Run(context.Background(), fallbackCommandDefinition{def}, oneshot.RunOpts{})
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(result.Value)
			if err != nil {
				t.Fatal(err)
			}
			if err := def.Validate(raw); err != nil {
				t.Fatalf("final result invalid: %v", err)
			}
			mu.Lock()
			defer mu.Unlock()
			want := []string{"primary", "primary", "second", "second", "third"}
			if !reflect.DeepEqual(order, want) {
				t.Fatalf("order=%v want=%v", order, want)
			}
		})
	}
}
