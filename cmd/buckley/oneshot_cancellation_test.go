//go:build !windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/acp"
	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
)

func TestSessionShutdown_ClosesOutstandingWorkers(t *testing.T) {
	mode := os.Getenv("BUCKLEY_TEST_SESSION_SHUTDOWN")
	if mode == "" {
		for _, mode := range []string{"one-shot", "ACP", "ACP with MCP"} {
			t.Run(mode, func(t *testing.T) {
				// Each host owns process-wide delegation limits and executable state.
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSessionShutdown_ClosesOutstandingWorkers$")
				cmd.Env = append(os.Environ(), "BUCKLEY_TEST_SESSION_SHUTDOWN="+mode, "TMPDIR="+t.TempDir())
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("session cleanup failed: %v\n%s", err, output)
				}
			})
		}
		return
	}
	t.Setenv("BUCKLEY_DELEGATION_DEPTH", "0")
	root, tmp := t.TempDir(), t.TempDir()
	t.Chdir(root)
	t.Setenv("TMPDIR", tmp)
	t.Setenv("BUCKLEY_SKILLS_PATH", t.TempDir())
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "initial"}} {
		if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git: %s: %v", output, err)
		}
	}
	control := t.TempDir()
	script := filepath.Join(control, "worker.sh")
	pidsPath, cleanupPath := filepath.Join(control, "worker.pids"), filepath.Join(control, "cleanup")
	cleanupCommand := "printf cleaned > " + shellQuote(cleanupPath) + "; exit 0"
	source := fmt.Sprintf("#!/bin/sh\ntrap %s TERM\nsleep 30 &\nprintf '%%s %%s' \"$$\" \"$!\" > %s\nwait\n", shellQuote(cleanupCommand), shellQuote(pidsPath))
	if err := os.WriteFile(script, []byte(source), 0700); err != nil {
		t.Fatal(err)
	}
	previousExecutable := os.Args[0]
	os.Args[0] = script
	t.Cleanup(func() { os.Args[0] = previousExecutable })
	t.Cleanup(func() {
		data, _ := os.ReadFile(pidsPath)
		for _, field := range strings.Fields(string(data)) {
			pid, err := strconv.Atoi(field)
			if err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			fmt.Fprint(w, `{"data":[{"id":"worker-test","supported_parameters":["tools"]}]}`)
			return
		}
		if requests.Add(1) == 1 {
			writeOneShotArtifactRouteSSE(t, w, map[string]any{"tool_calls": []map[string]any{{
				"index": 0, "id": "spawn", "type": "function", "function": map[string]any{
					"name": "spawn_subagent", "arguments": `{"initial_task":"wait for shutdown"}`,
				},
			}}}, "tool_calls")
			return
		}
		var request model.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		for {
			if data, _ := os.ReadFile(pidsPath); len(data) > 0 {
				break
			}
			if ctx.Err() != nil {
				t.Errorf("worker did not start: messages=%+v", request.Messages)
				break
			}
			time.Sleep(time.Millisecond)
		}
		writeOneShotArtifactRouteSSE(t, w, map[string]any{"content": "Parent complete."}, "stop")
	}))
	t.Cleanup(server.Close)
	cfg := config.DefaultConfig()
	cfg.Models.DefaultProvider = "openai_compatible"
	cfg.Providers.OpenAICompatible.Enabled = true
	cfg.Providers.OpenAICompatible.APIKey = "test-key"
	cfg.Providers.OpenAICompatible.BaseURL = server.URL
	cfg.Providers.OpenAICompatible.Models = []string{"worker-test"}
	cfg.Providers.OpenAICompatible.SupportedParameters = map[string][]string{"worker-test": {"tools"}}
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	switch mode {
	case "one-shot":
		code := -1
		stderr := captureStderr(t, func() {
			_ = captureStdout(t, func() {
				code = executeOneShot("Delegate the observation, then finish.", cfg, mgr, nil, nil, oneShotOptions{
					ModelOverride: "openai_compatible/worker-test",
					AllowedTools:  []string{"spawn_subagent"},
					Limits:        acpLoopLimits{TaskIntent: agentloop.ReadOnlyIntent},
				})
			})
		})
		if code != 0 || requests.Load() != 2 {
			t.Fatalf("parent did not complete: code=%d requests=%d stderr=%s", code, requests.Load(), stderr)
		}
	case "ACP", "ACP with MCP":
		handler, cleanup := makePromptHandler(cfg, mgr, nil, nil, root, func(string, ...interface{}) {}, nil, nil)
		t.Cleanup(cleanup)
		session := &acp.AgentSession{ID: "shutdown", WorkingDirectory: root, Mode: acpModePrefix + "openai_compatible/worker-test"}
		if mode == "ACP with MCP" {
			session.McpServers = []acp.McpServer{{Name: "fake", Command: acpFakeMCPServerBin, Args: []string{"-tools=0"}}}
		}
		if _, err := handler(context.Background(), session, []acp.ContentBlock{acp.NewTextContent("Delegate the observation, then finish.")}, func(acp.SessionUpdate) error { return nil }); err != nil {
			t.Fatalf("ACP prompt: %v", err)
		}
		cleanup()
		cleanup()
	}
	if _, err := os.Stat(cleanupPath); err != nil {
		t.Fatalf("parent returned without shutting down its worker: %v", err)
	}
	files, err := os.ReadDir(tmp)
	if err != nil || len(files) != 0 {
		t.Fatalf("session worker temporary files remain: %v, %v", files, err)
	}
}

func TestOneShot_SignalPreservesPartialOutput(t *testing.T) {
	if endpoint := os.Getenv("BUCKLEY_TEST_ONESHOT_SIGNAL_URL"); endpoint != "" {
		t.Chdir(os.Getenv("BUCKLEY_TEST_ONESHOT_SIGNAL_ROOT"))
		cfg := config.DefaultConfig()
		cfg.Models.DefaultProvider = "openai_compatible"
		cfg.Providers.OpenAICompatible.Enabled = true
		cfg.Providers.OpenAICompatible.APIKey = "test-key"
		cfg.Providers.OpenAICompatible.BaseURL = endpoint
		cfg.Providers.OpenAICompatible.Models = []string{"signal-test"}
		cfg.Providers.OpenAICompatible.SupportedParameters = map[string][]string{"signal-test": {"tools"}}
		mgr, err := model.NewManager(cfg)
		if err != nil {
			t.Fatal(err)
		}
		code := executeOneShot("Summarize the available evidence.", cfg, mgr, nil, nil, oneShotOptions{
			ModelOverride: "openai_compatible/signal-test",
			AllowedTools:  []string{},
			Limits:        acpLoopLimits{TaskIntent: agentloop.ReadOnlyIntent},
		})
		if code != 1 {
			t.Fatalf("interrupted one-shot returned %d, want 1", code)
		}
		return
	}

	for _, sig := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			streaming := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					fmt.Fprint(w, `{"data":[{"id":"signal-test","supported_parameters":["tools"]}]}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"id\":\"partial\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Retained partial evidence.\"}}]}\n\n")
				w.(http.Flusher).Flush()
				close(streaming)
				<-r.Context().Done()
			}))
			t.Cleanup(server.Close)
			cmd := exec.Command(os.Args[0], "-test.run=^TestOneShot_SignalPreservesPartialOutput$")
			cmd.Env = append(os.Environ(), "BUCKLEY_TEST_ONESHOT_SIGNAL_URL="+server.URL, "BUCKLEY_TEST_ONESHOT_SIGNAL_ROOT="+t.TempDir(), "BUCKLEY_SKILLS_PATH="+t.TempDir(), "TMPDIR="+t.TempDir())
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill() })
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-streaming:
			case err := <-done:
				t.Fatalf("one-shot exited before streaming: %v\n%s", err, stderr.String())
			case <-time.After(15 * time.Second):
				t.Fatal("one-shot did not start streaming")
			}
			// One-shot output stays buffered until the turn ends. Give the
			// stream reader time to receive the flushed fragment before shutdown.
			time.Sleep(100 * time.Millisecond)
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("signal bypassed one-shot cleanup: %v\n%s", err, stderr.String())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("one-shot remained blocked after signal")
			}
			if !strings.Contains(stdout.String(), "Retained partial evidence.") || !strings.Contains(stderr.String(), `stop_reason="cancelled"`) {
				t.Fatalf("interrupted result lost evidence or classification: stdout=%s stderr=%s", &stdout, &stderr)
			}
		})
	}
}
