package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
)

func TestExecuteOneShot_ConfiguredOutputLimitPreservesEvidence(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("BUCKLEY_SKILLS_PATH", t.TempDir())
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-qm", "initial"},
	} {
		if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git: %s: %v", output, err)
		}
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			fmt.Fprint(w, `{"data":[{"id":"output-test","supported_parameters":["tools"]}]}`)
			return
		}
		var req model.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if requests.Add(1) == 1 {
			arguments, _ := json.Marshal(map[string]any{
				"language": "bash",
				"code":     "printf 'answer=42\\n'; printf '%04096d' 0",
			})
			writeOneShotArtifactRouteSSE(t, w, map[string]any{"tool_calls": []map[string]any{{
				"index": 0, "id": "code-output", "type": "function",
				"function": map[string]any{"name": "run_code", "arguments": string(arguments)},
			}}}, "tool_calls")
			return
		}
		var output string
		for _, msg := range req.Messages {
			if msg.Role == "tool" {
				output = model.ExtractTextContentOrEmpty(msg.Content)
			}
		}
		if !strings.Contains(output, "answer=42") || !strings.Contains(output, "stdout_truncated") || len(output) > 1024 {
			t.Errorf("configured output limit lost or ignored: bytes=%d", len(output))
		}
		writeOneShotArtifactRouteSSE(t, w, map[string]any{"content": "Observed answer: 42."}, "stop")
	}))
	t.Cleanup(server.Close)
	cfg := config.DefaultConfig()
	cfg.Approval.Mode, cfg.Sandbox.Mode, cfg.Sandbox.AllowUnsafe = "yolo", "disabled", true
	cfg.ToolMiddleware.MaxResultBytes = 128
	cfg.Models.DefaultProvider = "openai_compatible"
	cfg.Providers.OpenAICompatible.Enabled = true
	cfg.Providers.OpenAICompatible.APIKey = "test-key"
	cfg.Providers.OpenAICompatible.BaseURL = server.URL
	cfg.Providers.OpenAICompatible.Models = []string{"output-test"}
	cfg.Providers.OpenAICompatible.SupportedParameters = map[string][]string{"output-test": {"tools"}}
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	code := -1
	var stdout string
	stderr := captureStderr(t, func() {
		stdout = captureStdout(t, func() {
			code = executeOneShot("Run the snippet and report its observed answer.", cfg, mgr, nil, nil, oneShotOptions{
				ModelOverride: "openai_compatible/output-test",
				AllowedTools:  []string{"run_code"},
			})
		})
	})
	if code != 0 || requests.Load() != 2 || !strings.Contains(stdout, "Observed answer: 42.") {
		t.Fatalf("code=%d requests=%d stdout=%q stderr=%q", code, requests.Load(), stdout, stderr)
	}
}
