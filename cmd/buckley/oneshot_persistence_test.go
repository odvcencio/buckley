package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
)

func TestApplyOneShotPersistence_DefaultsAndOverrides(t *testing.T) {
	for _, tc := range []struct {
		name                string
		unattended, persist bool
		configured          *int
		want                int
	}{
		{"attended", false, false, nil, 0}, {"unattended", true, false, nil, 200}, {"explicit", false, true, nil, 200}, {"disabled", true, false, new(int), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Approval.Mode = "safe"
			cfg.Oneshot.MaxContinuations = tc.configured
			if tc.unattended {
				cfg.Approval.Mode = "yolo"
				cfg.Sandbox.AllowUnsafe = true
				cfg.Sandbox.Mode = "disabled"
			}
			var log bytes.Buffer
			limits := applyOneShotPersistence(cfg, acpLoopLimits{TaskIntent: agentloop.MutationIntent, Persist: tc.persist}, &log)
			if limits.MaxContinuations != tc.want {
				t.Fatalf("limits=%+v", limits)
			}
			if tc.want > 0 {
				limits.OnContinuation(1, "missing verification")
				if !strings.Contains(log.String(), "missing verification") {
					t.Fatal("no log")
				}
			}
		})
	}
}

func TestParseStartupOptions_Persist(t *testing.T) {
	opts, err := parseStartupOptions([]string{"--persist", "-p", "change the code"})
	if err != nil || !opts.persist {
		t.Fatalf("opts=%+v err=%v", opts, err)
	}
	limits := applyOneShotPersistence(config.DefaultConfig(), acpLoopLimits{Persist: opts.persist}, &bytes.Buffer{})
	if limits.TaskIntent != agentloop.MutationIntent || limits.MaxContinuations != 200 || limits.allowHostTools {
		t.Fatalf("limits=%+v", limits)
	}
}

func TestOneShot_PersistsUntilPostChangeVerification(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make unavailable")
	}
	for _, verificationTool := range []string{"run_verification", "run_shell"} {
		t.Run(verificationTool, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			t.Setenv("GOWORK", "off")
			t.Setenv("BUCKLEY_SKILLS_PATH", t.TempDir())
			t.Setenv("BUCKLEY_APPROVAL_MODE", "yolo")
			t.Setenv("BUCKLEY_UNSAFE", "1")
			t.Setenv("BUCKLEY_TOOL_SANDBOX_MODE", "disabled")
			for name, content := range map[string]string{"target.txt": "before\n", "Makefile": "check:\n\t@test \"$$(cat target.txt)\" != before\n"} {
				if err := os.WriteFile(name, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			for _, args := range [][]string{{"init", "-q"}, {"add", "."}} {
				if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
					t.Fatalf("git: %s %v", out, err)
				}
			}
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/models" {
					fmt.Fprint(w, `{"data":[{"id":"persist-test","supported_parameters":["tools"]}]}`)
					return
				}
				var req model.ChatRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				n := requests.Add(1)
				offered := false
				for _, def := range req.Tools {
					fn, _ := def["function"].(map[string]any)
					offered = offered || fn["name"] == "run_verification"
				}
				if !offered {
					t.Error("mutation session omitted run_verification")
				}
				name, args := "edit_file", map[string]any{"path": "target.txt", "old_string": "before", "new_string": "after"}
				switch n {
				case 2, 3, 6:
					writeOneShotArtifactRouteSSE(t, w, map[string]any{"content": "Next step: run the required check."}, "stop")
					return
				case 4, 7:
					name, args = verificationTool, map[string]any{"command": "make check"}
				case 5:
					args = map[string]any{"path": "target.txt", "old_string": "after", "new_string": "after again"}
				case 8:
					writeOneShotArtifactRouteSSE(t, w, map[string]any{"content": "Task complete and verified."}, "stop")
					return
				default:
					if n != 1 {
						t.Errorf("unexpected request %d", n)
						writeOneShotArtifactRouteSSE(t, w, map[string]any{"content": "Unexpected request"}, "stop")
						return
					}
				}
				if n == 4 || n == 7 {
					found := false
					for _, msg := range req.Messages {
						text := model.ExtractTextContentOrEmpty(msg.Content)
						if msg.Role == "user" && strings.Contains(text, "Unmet completion criterion") && strings.Contains(text, "Next step: run the required check") {
							found = true
						}
					}
					if !found {
						t.Error("continuation lost the criterion or next step")
					}
				}
				encoded, _ := json.Marshal(args)
				writeOneShotArtifactRouteSSE(t, w, map[string]any{"tool_calls": []map[string]any{{"index": 0, "id": fmt.Sprint(n), "type": "function", "function": map[string]any{"name": name, "arguments": string(encoded)}}}}, "tool_calls")
			}))
			t.Cleanup(server.Close)
			cfg := config.DefaultConfig()
			config.ApplyEnvOverridesForTest(cfg)
			cfg.AgentController.EmergencyFuse.ModelRequests = 12
			cfg.Models.DefaultProvider = "openai_compatible"
			cfg.Providers.OpenAICompatible.Enabled = true
			cfg.Providers.OpenAICompatible.APIKey = "test-key"
			cfg.Providers.OpenAICompatible.BaseURL = server.URL
			cfg.Providers.OpenAICompatible.Models = []string{"persist-test"}
			cfg.Providers.OpenAICompatible.SupportedParameters = map[string][]string{"persist-test": {"tools"}}
			mgr, err := model.NewManager(cfg)
			if err != nil {
				t.Fatal(err)
			}
			oldQuiet := quietMode
			quietMode = true
			t.Cleanup(func() { quietMode = oldQuiet })
			code := -1
			var stdout string
			stderr := captureStderr(t, func() {
				stdout = captureStdout(t, func() {
					code = executeOneShotWithTaskIntent("Edit target, then check it.", cfg, mgr, nil, nil, nil, nil, "openai_compatible/persist-test", []string{"edit_file", "run_verification", "run_shell"}, false, agentloop.MutationIntent)
				})
			})
			if code != 0 || requests.Load() != 8 || strings.Count(stderr, "One-shot continuation:") != 3 || !strings.Contains(stdout, "Task complete and verified") {
				t.Fatalf("code=%d requests=%d stdout=%s stderr=%s", code, requests.Load(), stdout, stderr)
			}
		})
	}
}
