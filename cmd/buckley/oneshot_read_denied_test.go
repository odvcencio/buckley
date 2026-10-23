package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
)

func TestOneShotSources_ExplicitDenialsOverrideReadRoot(t *testing.T) {
	for _, layer := range []string{"approval", "sandbox"} {
		t.Run(layer, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("BUCKLEY_SKILLS_PATH", t.TempDir())
			root := t.TempDir()
			path := filepath.Join(root, "spec.md")
			if err := os.WriteFile(path, []byte("MUST_NOT_REACH_MODEL"), 0o644); err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int32
			var denied atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					fmt.Fprint(w, `{"data":[{"id":"fixture","supported_parameters":["tools"]}]}`)
					return
				}
				var request struct {
					Messages []struct {
						Role    string
						Content json.RawMessage
					}
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if requests.Add(1) == 1 {
					arguments, _ := json.Marshal(map[string]string{"path": path})
					writeOneShotArtifactRouteSSE(t, w, map[string]any{"tool_calls": []map[string]any{{"id": "read-source", "type": "function", "function": map[string]string{"name": "read_file", "arguments": string(arguments)}}}}, "tool_calls")
					return
				}
				for _, message := range request.Messages {
					if message.Role != "tool" {
						continue
					}
					if strings.Contains(string(message.Content), "MUST_NOT_REACH_MODEL") {
						t.Error("denied source reached model")
					}
					if strings.Contains(string(message.Content), "denied") {
						denied.Store(true)
					}
				}
				writeOneShotArtifactRouteSSE(t, w, map[string]any{"content": "The configured source is denied."}, "stop")
			}))
			t.Cleanup(server.Close)
			cfg := config.DefaultConfig()
			cfg.Models.DefaultProvider = "openai_compatible"
			cfg.Providers.OpenAICompatible.Enabled = true
			cfg.Providers.OpenAICompatible.APIKey = "test-key"
			cfg.Providers.OpenAICompatible.BaseURL = server.URL
			cfg.Providers.OpenAICompatible.Models = []string{"fixture"}
			cfg.Providers.OpenAICompatible.SupportedParameters = map[string][]string{"fixture": {"tools"}}
			if layer == "approval" {
				cfg.Approval.DeniedPaths = []string{root}
			} else {
				cfg.Sandbox.DeniedPaths = []string{root}
			}
			mgr, err := model.NewManager(cfg)
			if err != nil {
				t.Fatal(err)
			}
			captureStderr(t, func() {
				captureStdout(t, func() {
					executeOneShot("Read the approved spec source.", cfg, mgr, nil, nil, oneShotOptions{
						ModelOverride: "openai_compatible/fixture", ReadRoots: []string{root}, AllowedTools: []string{"read_file"},
						Limits: acpLoopLimits{TaskIntent: agentloop.ReadOnlyIntent, MaxElapsedSeconds: 15},
					})
				})
			})
			if !denied.Load() {
				t.Fatalf("model did not receive the denial; requests=%d", requests.Load())
			}
		})
	}
}
