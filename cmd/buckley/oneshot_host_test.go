package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/protocol"
)

func TestOneShotHostToolsAllowed_RequiresAllOptIns(t *testing.T) {
	for _, mode := range []string{"safe", "auto", "ask", "yolo"} {
		for _, unsafe := range []bool{false, true} {
			for _, sandbox := range []string{"workspace", "disabled"} {
				cfg := config.DefaultConfig()
				cfg.Approval.Mode, cfg.Sandbox.AllowUnsafe, cfg.Sandbox.Mode = mode, unsafe, sandbox
				if got, want := oneShotHostToolsAllowed(cfg), mode == "yolo" && unsafe && sandbox == "disabled"; got != want {
					t.Fatalf("mode=%s unsafe=%v sandbox=%s allowed=%v", mode, unsafe, sandbox, got)
				}
			}
		}
	}
}

func TestOneShotLongMutation_RetainsExplicitLimits(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.AgentController.EmergencyFuse.ModelRequests = 0
	cfg.AgentController.EmergencyFuse.ToolExecutions = 0
	g := newACPToolLoopGovernorWithLimits(cfg, acpLoopLimits{longMutation: true})
	for i := range 3000 {
		if d := g.BeginRound(); d.Stop {
			t.Fatalf("round %d: %+v", i, d)
		}
		if d := g.Observe("read_file", "{}", "stable", true); d.Stop {
			t.Fatalf("call %d: %+v", i, d)
		}
	}
	for _, explicit := range []bool{false, true} {
		limits := acpLoopLimits{longMutation: true}
		cfg.AgentController.EmergencyFuse.ModelRequests = 2
		if explicit {
			cfg.AgentController.EmergencyFuse.ModelRequests = 0
			limits.MaxModelRequests = 2
		}
		g = newACPToolLoopGovernorWithLimits(cfg, limits)
		g.BeginRound()
		g.BeginRound()
		if d := g.BeginRound(); !d.Stop || d.Kind != "round_limit" {
			t.Fatalf("explicit=%v decision=%+v", explicit, d)
		}
	}
	compiled := &protocol.Protocol{Mode: protocol.ModeDynamic, Stages: []protocol.Stage{{MaxTurns: 8, MaxReadOnlyCalls: 4}}}
	limits := applyOneShotProtocolLimits(acpLoopLimits{longMutation: true}, compiled, nil, "")
	if limits.StepCap != 0 || limits.MaxReadOnlyCalls != 0 {
		t.Fatalf("implicit limits: %+v", limits)
	}
}

func TestOneShotMutation_FortyHostToolCalls(t *testing.T) {
	for _, mode := range []string{"yolo", "safe"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			t.Setenv("GOWORK", "off")
			t.Setenv("BUCKLEY_APPROVAL_MODE", mode)
			t.Setenv("BUCKLEY_UNSAFE", "1")
			t.Setenv("BUCKLEY_TOOL_SANDBOX_MODE", "disabled")
			t.Setenv("BUCKLEY_SKILLS_PATH", t.TempDir())
			if out, err := exec.Command("git", "init", "-q").CombinedOutput(); err != nil {
				t.Fatalf("git init: %s %v", out, err)
			}
			if err := os.WriteFile("target.txt", []byte("before\n"), 0600); err != nil {
				t.Fatal(err)
			}
			for name, content := range map[string]string{
				"go.mod":         "module host-test\n\ngo 1.26\n",
				"verify_test.go": "package fixture\nimport (\"os\"; \"testing\")\nfunc TestEdit(t *testing.T) { b, err := os.ReadFile(\"target.txt\"); if err != nil || string(b) != \"after\\n\" { t.Fatalf(\"content=%q err=%v\", b, err) } }\n",
			} {
				if err := os.WriteFile(name, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if out, err := exec.Command("git", "add", "target.txt").CombinedOutput(); err != nil {
				t.Fatalf("git add: %s %v", out, err)
			}
			outside := filepath.Join(t.TempDir(), "AGENTS.md")
			if err := os.WriteFile(outside, []byte("outside instructions"), 0600); err != nil {
				t.Fatal(err)
			}
			var networkCalls, requests atomic.Int32
			network := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				networkCalls.Add(1)
				fmt.Fprint(w, "network passed")
			}))
			t.Cleanup(network.Close)
			host, port, err := net.SplitHostPort(network.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			networkCommand := fmt.Sprintf("exec 3<>/dev/tcp/%s/%s; printf 'GET / HTTP/1.0\\r\\nHost: localhost\\r\\n\\r\\n' >&3; cat <&3", host, port)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/models" {
					fmt.Fprint(w, `{"data":[{"id":"host-test","supported_parameters":["tools"]}]}`)
					return
				}
				var req model.ChatRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				n := requests.Add(1)
				if req.ToolChoice == "none" || n > 40 {
					writeOneShotArtifactRouteSSE(t, w, map[string]any{"content": "Task complete."}, "stop")
					return
				}
				if mode == "yolo" {
					var shellOffered bool
					for _, def := range req.Tools {
						fn, _ := def["function"].(map[string]any)
						if fn["name"] == "run_shell" {
							shellOffered = true
						}
					}
					if !shellOffered {
						t.Error("YOLO request omitted run_shell")
					}
					if n > 1 {
						var last string
						for _, msg := range req.Messages {
							if msg.Role == "tool" {
								last = model.ExtractTextContentOrEmpty(msg.Content)
							}
						}
						if strings.Contains(last, "success: false") || strings.Contains(last, `"success":false`) || strings.HasPrefix(last, "Error:") {
							t.Errorf("tool failed: %s", last)
						}
						if n == 2 && !strings.Contains(last, "outside instructions") {
							t.Errorf("outside read missing: %s", last)
						}
					}
				}
				name, args := "read_file", map[string]any{"path": outside}
				switch n {
				case 38:
					name, args = "run_shell", map[string]any{"command": networkCommand}
				case 39:
					name, args = "edit_file", map[string]any{"path": "target.txt", "old_string": "before", "new_string": "after"}
				case 40:
					name, args = "run_tests", map[string]any{"path": "."}
				}
				arguments, _ := json.Marshal(args)
				writeOneShotArtifactRouteSSE(t, w, map[string]any{"tool_calls": []map[string]any{{"index": 0, "id": fmt.Sprintf("call-%d", n), "type": "function", "function": map[string]any{"name": name, "arguments": string(arguments)}}}}, "tool_calls")
			}))
			t.Cleanup(server.Close)
			cfg := config.DefaultConfig()
			config.ApplyEnvOverridesForTest(cfg)
			cfg.Models.DefaultProvider = "openai_compatible"
			cfg.Providers.OpenAICompatible.Enabled = true
			cfg.Providers.OpenAICompatible.APIKey = "test-key"
			cfg.Providers.OpenAICompatible.BaseURL = server.URL
			cfg.Providers.OpenAICompatible.Models = []string{"host-test"}
			cfg.Providers.OpenAICompatible.SupportedParameters = map[string][]string{"host-test": {"tools"}}
			mgr, err := model.NewManager(cfg)
			if err != nil {
				t.Fatal(err)
			}
			oldQuiet := quietMode
			quietMode = true
			t.Cleanup(func() { quietMode = oldQuiet })
			var stdout string
			code := -1
			stderr := captureStderr(t, func() {
				stdout = captureStdout(t, func() {
					code = executeOneShotWithTaskIntent("Read instructions, check network, edit target.txt, then verify.", cfg, mgr, nil, nil, nil, nil, "openai_compatible/host-test", []string{"read_file", "edit_file", "run_shell", "run_tests"}, false, agentloop.MutationIntent)
				})
			})
			content, err := os.ReadFile("target.txt")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "yolo" {
				if code != 0 || requests.Load() != 41 || networkCalls.Load() != 1 || string(content) != "after\n" || strings.Contains(stderr, "stop_reason=") {
					t.Fatalf("code=%d requests=%d network=%d content=%q stdout=%s stderr=%s", code, requests.Load(), networkCalls.Load(), content, stdout, stderr)
				}
			} else {
				if code != 1 || networkCalls.Load() != 0 || string(content) != "before\n" || strings.Count(stderr, `stop_reason="exact_repeat:`) != 2 || !strings.Contains(stderr, "code=missing_observable_change") {
					t.Fatalf("safe run code=%d network=%d content=%q stderr=%s", code, networkCalls.Load(), content, stderr)
				}
			}
		})
	}
}
