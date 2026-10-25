package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/agentspec"
	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/skill"
	"m31labs.dev/buckley/pkg/tool"
)

func TestOneShot_DiscoveryPreservesToolContracts(t *testing.T) {
	for _, mode := range []string{"default", "discovery", "explicit", "profile", "artifact"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("BUCKLEY_SKILLS_PATH", t.TempDir())
			t.Setenv("BUCKLEY_DATA_DIR", t.TempDir())
			root := t.TempDir()
			t.Chdir(root)
			if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("owned-note\n"), 0600); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "oneshot-discovery-fixture"}} {
				if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
					t.Fatalf("git: %v %s", err, out)
				}
			}
			artifact := oneShotArtifactRouteJSON(t)
			var mu sync.Mutex
			var requests []model.ChatRequest
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					fmt.Fprint(w, `{"data":[{"id":"discovery-test","supported_parameters":["tools"]}]}`)
					return
				}
				var request model.ChatRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					http.Error(w, "invalid request", 400)
					return
				}
				mu.Lock()
				requests = append(requests, request)
				round := len(requests)
				mu.Unlock()
				name, arguments := "", ""
				switch {
				case mode == "artifact" && round == 1:
					body, _ := json.Marshal(map[string]json.RawMessage{"artifact": json.RawMessage(artifact)})
					name, arguments = "submit_artifact", string(body)
				case mode == "discovery" && round == 1:
					name, arguments = "discover_tools", `{"names":["git_log"]}`
				case mode == "discovery" && round == 2, (mode == "explicit" || mode == "profile") && round == 1:
					name, arguments = "git_log", `{"count":1}`
				case mode == "default" && round == 1:
					name, arguments = "read_file", `{"path":"note.txt"}`
				}
				if name != "" {
					writeOneShotArtifactRouteSSE(t, w, map[string]any{"tool_calls": []map[string]any{{"index": 0, "id": fmt.Sprintf("call-%d", round), "type": "function", "function": map[string]any{"name": name, "arguments": arguments}}}}, "tool_calls")
				} else {
					writeOneShotArtifactRouteSSE(t, w, map[string]any{"content": "observed requested evidence"}, "stop")
				}
			}))
			defer server.Close()
			cfg := config.DefaultConfig()
			cfg.Models.DefaultProvider = "openai_compatible"
			cfg.Providers.OpenRouter.Enabled = false
			cfg.Providers.OpenAICompatible.Enabled = true
			cfg.Providers.OpenAICompatible.APIKey = "test-key"
			cfg.Providers.OpenAICompatible.BaseURL = server.URL
			cfg.Providers.OpenAICompatible.Models = []string{"discovery-test"}
			cfg.Providers.OpenAICompatible.SupportedParameters = map[string][]string{"discovery-test": {"tools"}}
			mgr, err := model.NewManager(cfg)
			if err != nil {
				t.Fatal(err)
			}
			opts := oneShotOptions{ModelOverride: "openai_compatible/discovery-test", Limits: acpLoopLimits{TaskIntent: agentloop.ReadOnlyIntent, MaxToolCalls: 4, MaxElapsedSeconds: 20}}
			if mode == "explicit" {
				opts.AllowedTools = []string{"git_log"}
			}
			if mode == "profile" {
				opts.AgentProfile = &agentspec.RuntimeProfile{Spec: &agentspec.Spec{Tools: agentspec.ToolSpec{Allow: []string{"git_log"}}}}
			}
			if mode == "artifact" {
				opts.OutputSchema = artifactv1.SchemaVersion
			}
			code := -1
			var stdout string
			stderr := captureStderr(t, func() {
				stdout = captureStdout(t, func() {
					code = executeOneShot("Gather the requested evidence and return the result.", cfg, mgr, nil, nil, opts)
				})
			})
			if code != 0 {
				t.Fatalf("exit=%d stderr=%s stdout=%s", code, stderr, stdout)
			}
			mu.Lock()
			defer mu.Unlock()
			want := 2
			if mode == "discovery" {
				want = 3
			}
			if mode == "artifact" {
				want = 1
			}
			if len(requests) != want {
				t.Fatalf("requests=%d want=%d", len(requests), want)
			}
			for i, request := range requests {
				names := map[string]bool{}
				for _, definition := range request.Tools {
					function, _ := definition["function"].(map[string]any)
					name, _ := function["name"].(string)
					names[name] = true
				}
				schemas, err := json.Marshal(request.Tools)
				if err != nil {
					t.Fatal(err)
				}
				if len(schemas) > 12000 {
					t.Errorf("request %d schemas=%d bytes, want bounded working set", i+1, len(schemas))
				}
				if mode == "explicit" || mode == "profile" {
					if len(names) != 1 || !names["git_log"] {
						t.Errorf("explicit contract changed: %v", names)
					}
				} else {
					if !names["read_file"] || !names["discover_tools"] || names["git_blame"] || names["git_log"] != (mode == "discovery" && i > 0) {
						t.Errorf("request %d working set=%v", i+1, names)
					}
					if mode == "artifact" && !names["submit_artifact"] {
						t.Error("required submission tool hidden")
					}
				}
			}
			if mode != "artifact" {
				messages, _ := json.Marshal(requests[len(requests)-1].Messages)
				marker := "oneshot-discovery-fixture"
				if mode == "default" {
					marker = "owned-note"
				}
				if !strings.Contains(string(messages), marker) {
					t.Fatalf("actual tool evidence missing from final request: %s", messages)
				}
			}
		})
	}
}

func TestBuildACPToolTurn_ExplicitSkillFilterExposesSpecializedTools(t *testing.T) {
	registry := tool.NewRegistry()
	registry.EnableDynamicDiscovery([]string{"read_file"})
	state := skill.NewRuntimeState(nil)
	state.SetToolFilter([]string{"git_log"})
	turn := buildACPToolTurn(registry, state, nil, true, false, false, agentloop.ReadOnlyIntent)
	if !turn.Enabled || len(turn.Tools) != 1 || turn.Tools[0]["function"].(map[string]any)["name"] != "git_log" {
		t.Fatalf("explicit skill tool hidden by working set: %+v", turn)
	}
}
