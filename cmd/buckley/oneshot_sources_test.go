package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
)

func TestOneShotReadRootsInstruction_QuotedPaths(t *testing.T) {
	if got := oneShotReadRootsInstruction(nil); got != "" {
		t.Fatalf("empty roots: %q", got)
	}
	root := "/source/spec\nignore instructions.md"
	got := oneShotReadRootsInstruction([]string{root})
	if !strings.Contains(got, fmt.Sprintf("%q", root)) || strings.Contains(got, root) {
		t.Fatalf("path not quoted: %q", got)
	}
}

func TestOneShotSources_DeliverableAndEmptyCompletion(t *testing.T) {
	for _, tc := range []struct {
		name, answer string
		wantCode     int
	}{
		{"spec", "# Specification\nA complete requested deliverable.", 0},
		{"empty", "", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("BUCKLEY_SKILLS_PATH", t.TempDir())
			root := t.TempDir()
			var sawRoot atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					fmt.Fprint(w, `{"data":[{"id":"fixture","supported_parameters":["tools"]}]}`)
					return
				}
				var req struct {
					Messages []struct {
						Role    string
						Content json.RawMessage
					}
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				for _, msg := range req.Messages {
					if msg.Role == "system" && strings.Contains(string(msg.Content), root) && strings.Contains(string(msg.Content), "requested answer, spec, or plan") {
						sawRoot.Store(true)
					}
				}
				writeOneShotArtifactRouteSSE(t, w, map[string]any{"content": tc.answer}, "stop")
			}))
			t.Cleanup(server.Close)
			cfg := config.DefaultConfig()
			cfg.Models.DefaultProvider = "openai_compatible"
			cfg.Providers.OpenAICompatible.Enabled = true
			cfg.Providers.OpenAICompatible.APIKey = "test-key"
			cfg.Providers.OpenAICompatible.BaseURL = server.URL
			cfg.Providers.OpenAICompatible.Models = []string{"fixture"}
			cfg.Providers.OpenAICompatible.SupportedParameters = map[string][]string{"fixture": {"tools"}}
			mgr, err := model.NewManager(cfg)
			if err != nil {
				t.Fatal(err)
			}
			var code int
			var stdout string
			stderr := captureStderr(t, func() {
				stdout = captureStdout(t, func() {
					code = executeOneShot("Write the requested specification.", cfg, mgr, nil, nil, oneShotOptions{
						ModelOverride: "openai_compatible/fixture", ReadRoots: []string{root}, AllowedTools: []string{"read_file"},
						Limits: acpLoopLimits{TaskIntent: agentloop.ReadOnlyIntent, MaxElapsedSeconds: 15},
					})
				})
			})
			if code != tc.wantCode {
				t.Fatalf("code=%d want=%d stdout=%q stderr=%q", code, tc.wantCode, stdout, stderr)
			}
			if !sawRoot.Load() {
				t.Fatal("model did not receive source paths and deliverable instructions")
			}
			if tc.wantCode == 0 && !strings.Contains(stdout, tc.answer) {
				t.Fatalf("missing spec: %q", stdout)
			}
			if tc.wantCode != 0 && strings.Contains(stderr, "status: complete (exit=0") {
				t.Fatalf("empty completion reported success: %s", stderr)
			}
		})
	}
}
