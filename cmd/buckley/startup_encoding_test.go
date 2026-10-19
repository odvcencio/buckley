package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestMain_EncodingOverrideReachesModel(t *testing.T) {
	for _, tc := range []struct {
		name                            string
		configuredToon, wantJSON, piped bool
		flags                           []string
	}{
		{"json-overrides-toon", true, true, false, []string{"--encoding", "json"}},
		{"toon-overrides-json", false, false, false, []string{"--encoding=toon"}},
		{"json-alias", true, true, false, []string{"--json"}},
		{"piped-json", true, true, true, []string{"--encoding=json"}},
		{"configured-json", false, true, false, nil},
		{"configured-toon", true, false, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := make(chan string, 1)
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					fmt.Fprint(w, `{"data":[{"id":"encoding-fixture","supported_parameters":["tools"]}]}`)
					return
				}
				var request struct {
					Messages []struct {
						Role    string `json:"role"`
						Content string `json:"content"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				requests.Add(1)
				var delta map[string]any
				reason := "tool_calls"
				for _, message := range request.Messages {
					if message.Role == "tool" {
						wire <- message.Content
						delta, reason = map[string]any{"content": "Read complete."}, "stop"
					}
				}
				if delta == nil {
					delta = map[string]any{"tool_calls": []any{map[string]any{
						"index": 0, "id": "read-note", "type": "function",
						"function": map[string]any{"name": "read_file", "arguments": `{"path":"note.txt"}`},
					}}}
				}
				payload, _ := json.Marshal(map[string]any{"id": "fixture", "model": "encoding-fixture", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": reason}}})
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", payload)
			}))
			defer server.Close()
			root := t.TempDir()
			work, data := filepath.Join(root, "work"), filepath.Join(root, "data")
			for _, dir := range []string{work, data} {
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			const evidence = "encoder-wire-evidence-v1"
			if err := os.WriteFile(filepath.Join(work, "note.txt"), []byte(evidence), 0o600); err != nil {
				t.Fatal(err)
			}
			trust, err := loadProjectTrustStore(filepath.Join(data, projectTrustFileName))
			if err != nil {
				t.Fatal(err)
			}
			if err := trust.Set(work, projectTrustTrusted); err != nil {
				t.Fatal(err)
			}
			cfg := config.DefaultConfig()
			cfg.Providers = config.ProviderConfig{OpenAICompatible: config.OpenAICompatibleConfig{
				Enabled: true, APIKey: "fixture-key", BaseURL: server.URL, Models: []string{"encoding-fixture"},
				SupportedParameters: map[string][]string{"encoding-fixture": {"tools"}},
			}}
			cfg.Models.DefaultProvider = "openai_compatible"
			cfg.Models.Execution = "openai_compatible/encoding-fixture"
			cfg.Models.Planning, cfg.Models.Review, cfg.Models.Light = cfg.Models.Execution, cfg.Models.Execution, cfg.Models.Execution
			cfg.Encoding.UseToon = tc.configuredToon
			cfg.Memory.HyphaeRecall = false
			encoded, err := yaml.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			configFile := filepath.Join(root, "config.yaml")
			if err := os.WriteFile(configFile, encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			args := []string{"-test.run=^TestMain_EncodingOverrideReachesModel$", "--", "--config", configFile, "--quiet", "--task-intent", "read_only", "--tools", "read_file"}
			args = append(args, tc.flags...)
			const prompt = "Read note.txt and report its value."
			if !tc.piped {
				args = append(args, "-p", prompt)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], args...)
			cmd.Dir = work
			for _, entry := range os.Environ() {
				key, _, _ := strings.Cut(entry, "=")
				if strings.HasPrefix(key, "BUCKLEY_") || strings.HasPrefix(key, "GIT_") || slices.Contains([]string{"OPENROUTER_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GOOGLE_API_KEY"}, key) {
					continue
				}
				cmd.Env = append(cmd.Env, entry)
			}
			cmd.Env = append(cmd.Env, "BUCKLEY_TEST_RUN_MAIN=1", "BUCKLEY_CODEX_ENABLED=false", "BUCKLEY_DATA_DIR="+data, "BUCKLEY_DB_PATH="+filepath.Join(data, "buckley.db"), "BUCKLEY_LOG_DIR="+filepath.Join(root, "logs"), "BUCKLEY_SKILLS_PATH="+filepath.Join(root, "skills"))
			if tc.piped {
				cmd.Stdin = strings.NewReader(prompt)
			}
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("CLI startup failed: %v\n%s", err, output)
			}
			if requests.Load() != 2 {
				t.Fatalf("provider requests = %d, want 2", requests.Load())
			}
			select {
			case content := <-wire:
				if !strings.Contains(content, evidence) {
					t.Fatalf("tool evidence was not delivered: %s", content)
				}
				if tc.wantJSON {
					var result builtin.Result
					if err := json.Unmarshal([]byte(content), &result); err != nil || !result.Success || result.Data["content"] != evidence {
						t.Fatalf("model did not receive JSON evidence: %s (error=%v)", content, err)
					}
				} else if json.Valid([]byte(content)) {
					t.Fatalf("model received JSON instead of TOON: %s", content)
				}
			default:
				t.Fatal("model did not receive a tool result")
			}
		})
	}
}
